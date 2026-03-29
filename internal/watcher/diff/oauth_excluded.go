package diff

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

type ExcludedModelsSummary struct {
	hash  string
	count int
}

// SummarizeExcludedModels normalizes and hashes an excluded-model list.
func SummarizeExcludedModels(list []string) ExcludedModelsSummary {
	if len(list) == 0 {
		return ExcludedModelsSummary{}
	}
	seen := make(map[string]struct{}, len(list))
	normalized := make([]string, 0, len(list))
	for _, entry := range list {
		if trimmed := strings.ToLower(strings.TrimSpace(entry)); trimmed != "" {
			if _, exists := seen[trimmed]; exists {
				continue
			}
			seen[trimmed] = struct{}{}
			normalized = append(normalized, trimmed)
		}
	}
	sort.Strings(normalized)
	return ExcludedModelsSummary{
		hash:  ComputeExcludedModelsHash(normalized),
		count: len(normalized),
	}
}

// SummarizeOAuthExcludedModels summarizes OAuth excluded models per provider.
func SummarizeOAuthExcludedModels(entries map[string][]string) map[string]ExcludedModelsSummary {
	if len(entries) == 0 {
		return nil
	}
	out := make(map[string]ExcludedModelsSummary, len(entries))
	for k, v := range entries {
		key := strings.ToLower(strings.TrimSpace(k))
		if key == "" {
			continue
		}
		out[key] = SummarizeExcludedModels(v)
	}
	return out
}

// DiffOAuthExcludedModelChanges compares OAuth excluded models maps.
func DiffOAuthExcludedModelChanges(oldMap, newMap map[string][]string) ([]string, []string) {
	oldSummary := SummarizeOAuthExcludedModels(oldMap)
	newSummary := SummarizeOAuthExcludedModels(newMap)
	keys := make(map[string]struct{}, len(oldSummary)+len(newSummary))
	for k := range oldSummary {
		keys[k] = struct{}{}
	}
	for k := range newSummary {
		keys[k] = struct{}{}
	}
	changes := make([]string, 0, len(keys))
	affected := make([]string, 0, len(keys))
	for key := range keys {
		oldInfo, okOld := oldSummary[key]
		newInfo, okNew := newSummary[key]
		switch {
		case okOld && !okNew:
			changes = append(changes, fmt.Sprintf("oauth-excluded-models[%s]: removed", key))
			affected = append(affected, key)
		case !okOld && okNew:
			changes = append(changes, fmt.Sprintf("oauth-excluded-models[%s]: added (%d entries)", key, newInfo.count))
			affected = append(affected, key)
		case okOld && okNew && oldInfo.hash != newInfo.hash:
			changes = append(changes, fmt.Sprintf("oauth-excluded-models[%s]: updated (%d -> %d entries)", key, oldInfo.count, newInfo.count))
			affected = append(affected, key)
		}
	}
	sort.Strings(changes)
	sort.Strings(affected)
	return changes, affected
}

// DiffOAuthModelPlanAccessChanges compares OAuthModelPlanAccess maps and returns
// human-readable changes and the list of affected provider keys.
func DiffOAuthModelPlanAccessChanges(oldMap, newMap map[string][]config.ModelPlanAccess) ([]string, []string) {
	oldHash := hashModelPlanAccess(oldMap)
	newHash := hashModelPlanAccess(newMap)
	keys := make(map[string]struct{}, len(oldHash)+len(newHash))
	for k := range oldHash {
		keys[k] = struct{}{}
	}
	for k := range newHash {
		keys[k] = struct{}{}
	}
	changes := make([]string, 0, len(keys))
	affected := make([]string, 0, len(keys))
	for key := range keys {
		oldH, okOld := oldHash[key]
		newH, okNew := newHash[key]
		switch {
		case okOld && !okNew:
			changes = append(changes, fmt.Sprintf("oauth-model-plan-access[%s]: removed", key))
			affected = append(affected, key)
		case !okOld && okNew:
			changes = append(changes, fmt.Sprintf("oauth-model-plan-access[%s]: added", key))
			affected = append(affected, key)
		case okOld && okNew && oldH != newH:
			changes = append(changes, fmt.Sprintf("oauth-model-plan-access[%s]: updated", key))
			affected = append(affected, key)
		}
	}
	sort.Strings(changes)
	sort.Strings(affected)
	return changes, affected
}

// hashModelPlanAccess computes a per-provider hash of ModelPlanAccess rules.
func hashModelPlanAccess(entries map[string][]config.ModelPlanAccess) map[string]string {
	if len(entries) == 0 {
		return nil
	}
	out := make(map[string]string, len(entries))
	for provider, rules := range entries {
		key := strings.ToLower(strings.TrimSpace(provider))
		if key == "" || len(rules) == 0 {
			continue
		}
		parts := make([]string, 0, len(rules))
		for _, rule := range rules {
			pattern := strings.ToLower(strings.TrimSpace(rule.Pattern))
			allowed := make([]string, len(rule.AllowedPlans))
			for i, p := range rule.AllowedPlans {
				allowed[i] = strings.ToLower(strings.TrimSpace(p))
			}
			sort.Strings(allowed)
			denied := make([]string, len(rule.DeniedPlans))
			for i, p := range rule.DeniedPlans {
				denied[i] = strings.ToLower(strings.TrimSpace(p))
			}
			sort.Strings(denied)
			parts = append(parts, pattern+":a="+strings.Join(allowed, ",")+":d="+strings.Join(denied, ","))
		}
		sort.Strings(parts)
		sum := sha256.Sum256([]byte(strings.Join(parts, "|")))
		out[key] = hex.EncodeToString(sum[:])
	}
	return out
}

type AmpModelMappingsSummary struct {
	hash  string
	count int
}

// SummarizeAmpModelMappings hashes Amp model mappings for change detection.
func SummarizeAmpModelMappings(mappings []config.AmpModelMapping) AmpModelMappingsSummary {
	if len(mappings) == 0 {
		return AmpModelMappingsSummary{}
	}
	entries := make([]string, 0, len(mappings))
	for _, mapping := range mappings {
		from := strings.TrimSpace(mapping.From)
		to := strings.TrimSpace(mapping.To)
		if from == "" && to == "" {
			continue
		}
		entries = append(entries, from+"->"+to)
	}
	if len(entries) == 0 {
		return AmpModelMappingsSummary{}
	}
	sort.Strings(entries)
	sum := sha256.Sum256([]byte(strings.Join(entries, "|")))
	return AmpModelMappingsSummary{
		hash:  hex.EncodeToString(sum[:]),
		count: len(entries),
	}
}
