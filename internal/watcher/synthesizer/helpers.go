package synthesizer

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/watcher/diff"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

// StableIDGenerator generates stable, deterministic IDs for auth entries.
// It uses SHA256 hashing with collision handling via counters.
// It is not safe for concurrent use.
type StableIDGenerator struct {
	counters map[string]int
}

// NewStableIDGenerator creates a new StableIDGenerator instance.
func NewStableIDGenerator() *StableIDGenerator {
	return &StableIDGenerator{counters: make(map[string]int)}
}

// Next generates a stable ID based on the kind and parts.
// Returns the full ID (kind:hash) and the short hash portion.
func (g *StableIDGenerator) Next(kind string, parts ...string) (string, string) {
	if g == nil {
		return kind + ":000000000000", "000000000000"
	}
	hasher := sha256.New()
	hasher.Write([]byte(kind))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		hasher.Write([]byte{0})
		hasher.Write([]byte(trimmed))
	}
	digest := hex.EncodeToString(hasher.Sum(nil))
	if len(digest) < 12 {
		digest = fmt.Sprintf("%012s", digest)
	}
	short := digest[:12]
	key := kind + ":" + short
	index := g.counters[key]
	g.counters[key] = index + 1
	if index > 0 {
		short = fmt.Sprintf("%s-%d", short, index)
	}
	return fmt.Sprintf("%s:%s", kind, short), short
}

// ApplyAuthExcludedModelsMeta applies excluded models metadata to an auth entry.
// It computes a hash of excluded models and sets the auth_kind attribute.
// For OAuth entries, perKey (from the JSON file's excluded-models field) is merged
// with the global oauth-excluded-models config for the provider.
func ApplyAuthExcludedModelsMeta(auth *coreauth.Auth, cfg *config.Config, perKey []string, authKind string) {
	if auth == nil || cfg == nil {
		return
	}
	authKindKey := strings.ToLower(strings.TrimSpace(authKind))
	seen := make(map[string]struct{})
	add := func(list []string) {
		for _, entry := range list {
			if trimmed := strings.TrimSpace(entry); trimmed != "" {
				key := strings.ToLower(trimmed)
				if _, exists := seen[key]; exists {
					continue
				}
				seen[key] = struct{}{}
			}
		}
	}
	if authKindKey == "apikey" {
		add(perKey)
	} else {
		// For OAuth: merge per-account excluded models with global provider-level exclusions
		add(perKey)
		if cfg.OAuthExcludedModels != nil {
			providerKey := strings.ToLower(strings.TrimSpace(auth.Provider))
			add(cfg.OAuthExcludedModels[providerKey])
		}
	}
	combined := make([]string, 0, len(seen))
	for k := range seen {
		combined = append(combined, k)
	}
	sort.Strings(combined)
	hash := diff.ComputeExcludedModelsHash(combined)
	if auth.Attributes == nil {
		auth.Attributes = make(map[string]string)
	}
	if hash != "" {
		auth.Attributes["excluded_models_hash"] = hash
	}
	// Store the combined excluded models list so that routing can read it at runtime
	if len(combined) > 0 {
		auth.Attributes["excluded_models"] = strings.Join(combined, ",")
	}
	if authKind != "" {
		auth.Attributes["auth_kind"] = authKind
	}
}

// addConfigHeadersToAttrs adds header configuration to auth attributes.
// Headers are prefixed with "header:" in the attributes map.
func addConfigHeadersToAttrs(headers map[string]string, attrs map[string]string) {
	if len(headers) == 0 || attrs == nil {
		return
	}
	for hk, hv := range headers {
		key := strings.TrimSpace(hk)
		val := strings.TrimSpace(hv)
		if key == "" || val == "" {
			continue
		}
		attrs["header:"+key] = val
	}
}

// ApplyOAuthPlanAccess resolves the account's plan type and merges plan-restricted
// model patterns into the auth's excluded_models attribute.
// This enables plan-based model routing: e.g., free Codex accounts cannot access gpt-5.4.
func ApplyOAuthPlanAccess(auth *coreauth.Auth, cfg *config.Config, filePath string) {
	if auth == nil || cfg == nil || len(cfg.OAuthModelPlanAccess) == 0 {
		return
	}
	provider := strings.ToLower(auth.Provider)
	rules := cfg.OAuthModelPlanAccess[provider]
	if len(rules) == 0 {
		return
	}

	// Resolve plan type from JWT or filename.
	plan := resolveOAuthPlan(provider, auth.Metadata, filePath)
	if plan == "" {
		return // Unknown plan — permissive by default, skip exclusions.
	}
	if auth.Attributes == nil {
		auth.Attributes = make(map[string]string)
	}
	auth.Attributes["plan"] = plan

	// Collect patterns that this plan is NOT allowed to access.
	var planExcluded []string
	for _, rule := range rules {
		if shouldExcludeByPlan(plan, rule) {
			planExcluded = append(planExcluded, rule.Pattern)
		}
	}
	if len(planExcluded) == 0 {
		return
	}

	// Merge plan-restricted patterns into existing excluded_models.
	mergeIntoExcludedModels(auth, planExcluded)
}

// resolveOAuthPlan determines the plan type for an OAuth account.
// Currently only Codex accounts have plan-based routing.
func resolveOAuthPlan(provider string, metadata map[string]any, filePath string) string {
	switch provider {
	case "codex":
		return resolveCodexPlan(metadata, filePath)
	default:
		return ""
	}
}

// resolveCodexPlan extracts the Codex account plan type.
// Primary source: JWT id_token claim (chatgpt_plan_type).
// Fallback: filename suffix inference (-plus, -team, -pro).
func resolveCodexPlan(metadata map[string]any, filePath string) string {
	// Primary: parse JWT id_token for plan type.
	if idToken, ok := metadata["id_token"].(string); ok && idToken != "" {
		if claims, err := codex.ParseJWTToken(idToken); err == nil && claims != nil {
			if plan := strings.ToLower(strings.TrimSpace(claims.CodexAuthInfo.ChatgptPlanType)); plan != "" {
				return plan
			}
		}
	}

	// Fallback: infer plan from filename suffix.
	return inferPlanFromFilename(filePath)
}

// inferPlanFromFilename extracts plan type from Codex auth filename conventions.
// Filenames follow the pattern: codex-{email}-{plan}.json
// Known plan suffixes: -free, -plus, -team, -pro. No suffix implies unknown.
func inferPlanFromFilename(filePath string) string {
	base := strings.ToLower(filepath.Base(filePath))
	base = strings.TrimSuffix(base, ".json")
	// Known plan type suffixes in Codex credential filenames.
	knownPlans := []string{"free", "plus", "team", "pro"}
	for _, plan := range knownPlans {
		if strings.HasSuffix(base, "-"+plan) {
			return plan
		}
	}
	// No recognized plan suffix — could be free or unknown.
	return ""
}

// shouldExcludeByPlan determines whether a plan should be excluded for the given rule.
// Supports two modes:
//   - allowed-plans (whitelist): plan is excluded if NOT in the list.
//   - denied-plans (blacklist): plan is excluded if IN the list.
func shouldExcludeByPlan(plan string, rule config.ModelPlanAccess) bool {
	if len(rule.DeniedPlans) > 0 {
		// Blacklist mode: exclude if plan is in the denied list.
		for _, denied := range rule.DeniedPlans {
			if plan == denied {
				return true
			}
		}
		return false
	}
	// Whitelist mode (default): exclude if plan is NOT in the allowed list.
	for _, allowed := range rule.AllowedPlans {
		if plan == allowed {
			return false
		}
	}
	return true
}

// mergeIntoExcludedModels adds new patterns to the auth's excluded_models attribute
// and recomputes the hash. Deduplicates and sorts the combined list.
func mergeIntoExcludedModels(auth *coreauth.Auth, patterns []string) {
	if auth.Attributes == nil {
		auth.Attributes = make(map[string]string)
	}

	// Parse existing excluded models.
	seen := make(map[string]struct{})
	existing := auth.Attributes["excluded_models"]
	if existing != "" {
		for _, entry := range strings.Split(existing, ",") {
			if trimmed := strings.TrimSpace(entry); trimmed != "" {
				seen[strings.ToLower(trimmed)] = struct{}{}
			}
		}
	}

	// Add new plan-restricted patterns.
	for _, pattern := range patterns {
		key := strings.ToLower(strings.TrimSpace(pattern))
		if key != "" {
			seen[key] = struct{}{}
		}
	}

	// Rebuild sorted list.
	combined := make([]string, 0, len(seen))
	for k := range seen {
		combined = append(combined, k)
	}
	sort.Strings(combined)

	// Update attributes.
	if len(combined) > 0 {
		auth.Attributes["excluded_models"] = strings.Join(combined, ",")
		auth.Attributes["excluded_models_hash"] = diff.ComputeExcludedModelsHash(combined)
	}
}
