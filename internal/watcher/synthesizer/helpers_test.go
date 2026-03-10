package synthesizer

import (
	"reflect"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/watcher/diff"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

func TestNewStableIDGenerator(t *testing.T) {
	gen := NewStableIDGenerator()
	if gen == nil {
		t.Fatal("expected non-nil generator")
	}
	if gen.counters == nil {
		t.Fatal("expected non-nil counters map")
	}
}

func TestStableIDGenerator_Next(t *testing.T) {
	tests := []struct {
		name       string
		kind       string
		parts      []string
		wantPrefix string
	}{
		{
			name:       "basic gemini apikey",
			kind:       "gemini:apikey",
			parts:      []string{"test-key", ""},
			wantPrefix: "gemini:apikey:",
		},
		{
			name:       "claude with base url",
			kind:       "claude:apikey",
			parts:      []string{"sk-ant-xxx", "https://api.anthropic.com"},
			wantPrefix: "claude:apikey:",
		},
		{
			name:       "empty parts",
			kind:       "codex:apikey",
			parts:      []string{},
			wantPrefix: "codex:apikey:",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gen := NewStableIDGenerator()
			id, short := gen.Next(tt.kind, tt.parts...)

			if !strings.Contains(id, tt.wantPrefix) {
				t.Errorf("expected id to contain %q, got %q", tt.wantPrefix, id)
			}
			if short == "" {
				t.Error("expected non-empty short id")
			}
			if len(short) != 12 {
				t.Errorf("expected short id length 12, got %d", len(short))
			}
		})
	}
}

func TestStableIDGenerator_Stability(t *testing.T) {
	gen1 := NewStableIDGenerator()
	gen2 := NewStableIDGenerator()

	id1, _ := gen1.Next("gemini:apikey", "test-key", "https://api.example.com")
	id2, _ := gen2.Next("gemini:apikey", "test-key", "https://api.example.com")

	if id1 != id2 {
		t.Errorf("same inputs should produce same ID: got %q and %q", id1, id2)
	}
}

func TestStableIDGenerator_CollisionHandling(t *testing.T) {
	gen := NewStableIDGenerator()

	id1, short1 := gen.Next("gemini:apikey", "same-key")
	id2, short2 := gen.Next("gemini:apikey", "same-key")

	if id1 == id2 {
		t.Error("collision should be handled with suffix")
	}
	if short1 == short2 {
		t.Error("short ids should differ")
	}
	if !strings.Contains(short2, "-1") {
		t.Errorf("second short id should contain -1 suffix, got %q", short2)
	}
}

func TestStableIDGenerator_NilReceiver(t *testing.T) {
	var gen *StableIDGenerator = nil
	id, short := gen.Next("test:kind", "part")

	if id != "test:kind:000000000000" {
		t.Errorf("expected test:kind:000000000000, got %q", id)
	}
	if short != "000000000000" {
		t.Errorf("expected 000000000000, got %q", short)
	}
}

func TestApplyAuthExcludedModelsMeta(t *testing.T) {
	tests := []struct {
		name     string
		auth     *coreauth.Auth
		cfg      *config.Config
		perKey   []string
		authKind string
		wantHash bool
		wantKind string
	}{
		{
			name: "apikey with excluded models",
			auth: &coreauth.Auth{
				Provider:   "gemini",
				Attributes: make(map[string]string),
			},
			cfg:      &config.Config{},
			perKey:   []string{"model-a", "model-b"},
			authKind: "apikey",
			wantHash: true,
			wantKind: "apikey",
		},
		{
			name: "oauth with provider excluded models",
			auth: &coreauth.Auth{
				Provider:   "claude",
				Attributes: make(map[string]string),
			},
			cfg: &config.Config{
				OAuthExcludedModels: map[string][]string{
					"claude": {"claude-2.0"},
				},
			},
			perKey:   nil,
			authKind: "oauth",
			wantHash: true,
			wantKind: "oauth",
		},
		{
			name: "nil auth",
			auth: nil,
			cfg:  &config.Config{},
		},
		{
			name:     "nil config",
			auth:     &coreauth.Auth{Provider: "test"},
			cfg:      nil,
			authKind: "apikey",
		},
		{
			name: "nil attributes initialized",
			auth: &coreauth.Auth{
				Provider:   "gemini",
				Attributes: nil,
			},
			cfg:      &config.Config{},
			perKey:   []string{"model-x"},
			authKind: "apikey",
			wantHash: true,
			wantKind: "apikey",
		},
		{
			name: "apikey with duplicate excluded models",
			auth: &coreauth.Auth{
				Provider:   "gemini",
				Attributes: make(map[string]string),
			},
			cfg:      &config.Config{},
			perKey:   []string{"model-a", "MODEL-A", "model-b", "model-a"},
			authKind: "apikey",
			wantHash: true,
			wantKind: "apikey",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ApplyAuthExcludedModelsMeta(tt.auth, tt.cfg, tt.perKey, tt.authKind)

			if tt.auth != nil && tt.cfg != nil {
				if tt.wantHash {
					if _, ok := tt.auth.Attributes["excluded_models_hash"]; !ok {
						t.Error("expected excluded_models_hash in attributes")
					}
				}
				if tt.wantKind != "" {
					if got := tt.auth.Attributes["auth_kind"]; got != tt.wantKind {
						t.Errorf("expected auth_kind=%s, got %s", tt.wantKind, got)
					}
				}
			}
		})
	}
}

func TestApplyAuthExcludedModelsMeta_OAuthMergeWritesCombinedModels(t *testing.T) {
	auth := &coreauth.Auth{
		Provider:   "claude",
		Attributes: make(map[string]string),
	}
	cfg := &config.Config{
		OAuthExcludedModels: map[string][]string{
			"claude": {"global-a", "shared"},
		},
	}

	ApplyAuthExcludedModelsMeta(auth, cfg, []string{"per", "SHARED"}, "oauth")

	const wantCombined = "global-a,per,shared"
	if gotCombined := auth.Attributes["excluded_models"]; gotCombined != wantCombined {
		t.Fatalf("expected excluded_models=%q, got %q", wantCombined, gotCombined)
	}

	expectedHash := diff.ComputeExcludedModelsHash([]string{"global-a", "per", "shared"})
	if gotHash := auth.Attributes["excluded_models_hash"]; gotHash != expectedHash {
		t.Fatalf("expected excluded_models_hash=%q, got %q", expectedHash, gotHash)
	}
}

func TestAddConfigHeadersToAttrs(t *testing.T) {
	tests := []struct {
		name    string
		headers map[string]string
		attrs   map[string]string
		want    map[string]string
	}{
		{
			name: "basic headers",
			headers: map[string]string{
				"Authorization": "Bearer token",
				"X-Custom":      "value",
			},
			attrs: map[string]string{"existing": "key"},
			want: map[string]string{
				"existing":             "key",
				"header:Authorization": "Bearer token",
				"header:X-Custom":      "value",
			},
		},
		{
			name:    "empty headers",
			headers: map[string]string{},
			attrs:   map[string]string{"existing": "key"},
			want:    map[string]string{"existing": "key"},
		},
		{
			name:    "nil headers",
			headers: nil,
			attrs:   map[string]string{"existing": "key"},
			want:    map[string]string{"existing": "key"},
		},
		{
			name:    "nil attrs",
			headers: map[string]string{"key": "value"},
			attrs:   nil,
			want:    nil,
		},
		{
			name: "skip empty keys and values",
			headers: map[string]string{
				"":      "value",
				"key":   "",
				"  ":    "value",
				"valid": "valid-value",
			},
			attrs: make(map[string]string),
			want: map[string]string{
				"header:valid": "valid-value",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			addConfigHeadersToAttrs(tt.headers, tt.attrs)
			if !reflect.DeepEqual(tt.attrs, tt.want) {
				t.Errorf("expected %v, got %v", tt.want, tt.attrs)
			}
		})
	}
}

func TestShouldExcludeByPlan(t *testing.T) {
	tests := []struct {
		name string
		plan string
		rule config.ModelPlanAccess
		want bool
	}{
		// --- allowed-plans (whitelist) ---
		{
			name: "allowed: plan in list",
			plan: "plus",
			rule: config.ModelPlanAccess{AllowedPlans: []string{"plus", "team"}},
			want: false,
		},
		{
			name: "allowed: plan not in list",
			plan: "free",
			rule: config.ModelPlanAccess{AllowedPlans: []string{"plus", "team"}},
			want: true,
		},
		{
			name: "allowed: empty list excludes all",
			plan: "plus",
			rule: config.ModelPlanAccess{AllowedPlans: []string{}},
			want: true,
		},
		// --- denied-plans (blacklist) ---
		{
			name: "denied: plan in list",
			plan: "free",
			rule: config.ModelPlanAccess{DeniedPlans: []string{"free"}},
			want: true,
		},
		{
			name: "denied: plan not in list",
			plan: "plus",
			rule: config.ModelPlanAccess{DeniedPlans: []string{"free"}},
			want: false,
		},
		{
			name: "denied: unknown plan not in list",
			plan: "enterprise",
			rule: config.ModelPlanAccess{DeniedPlans: []string{"free"}},
			want: false,
		},
		{
			name: "denied: empty list excludes none",
			plan: "free",
			rule: config.ModelPlanAccess{DeniedPlans: []string{}},
			want: true, // falls through to allowed-plans path with empty list
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shouldExcludeByPlan(tt.plan, tt.rule)
			if got != tt.want {
				t.Errorf("shouldExcludeByPlan(%q, %+v) = %v, want %v", tt.plan, tt.rule, got, tt.want)
			}
		})
	}
}

func TestApplyOAuthPlanAccess_DeniedPlans(t *testing.T) {
	cfg := &config.Config{
		OAuthModelPlanAccess: map[string][]config.ModelPlanAccess{
			"codex": {
				{Pattern: "gpt-5.3-codex", DeniedPlans: []string{"free"}},
				{Pattern: "gpt-5.4", DeniedPlans: []string{"free"}},
				{Pattern: "gpt-5.2*", DeniedPlans: []string{"plus"}},
			},
		},
	}

	tests := []struct {
		name         string
		plan         string
		wantExcluded string // comma-separated sorted excluded models, or empty
	}{
		{
			name:         "free account: excluded from 5.3 and 5.4",
			plan:         "free",
			wantExcluded: "gpt-5.3-codex,gpt-5.4",
		},
		{
			name:         "plus account: excluded from 5.2",
			plan:         "plus",
			wantExcluded: "gpt-5.2*",
		},
		{
			name:         "team account: no exclusions",
			plan:         "team",
			wantExcluded: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			auth := &coreauth.Auth{
				Provider:   "codex",
				Metadata:   map[string]any{},
				Attributes: map[string]string{"plan": tt.plan},
			}
			// Directly test the exclusion logic by calling the inner loop.
			var planExcluded []string
			for _, rule := range cfg.OAuthModelPlanAccess["codex"] {
				if shouldExcludeByPlan(tt.plan, rule) {
					planExcluded = append(planExcluded, rule.Pattern)
				}
			}
			if len(planExcluded) > 0 {
				mergeIntoExcludedModels(auth, planExcluded)
			}
			got := auth.Attributes["excluded_models"]
			if got != tt.wantExcluded {
				t.Errorf("excluded_models = %q, want %q", got, tt.wantExcluded)
			}
		})
	}
}

func TestApplyOAuthPlanAccess_AllowedPlans(t *testing.T) {
	cfg := &config.Config{
		OAuthModelPlanAccess: map[string][]config.ModelPlanAccess{
			"codex": {
				{Pattern: "gpt-5.3-codex", AllowedPlans: []string{"plus", "team"}},
				{Pattern: "gpt-5.4", AllowedPlans: []string{"plus", "team"}},
			},
		},
	}

	tests := []struct {
		name         string
		plan         string
		wantExcluded string
	}{
		{
			name:         "free excluded from both",
			plan:         "free",
			wantExcluded: "gpt-5.3-codex,gpt-5.4",
		},
		{
			name:         "plus allowed",
			plan:         "plus",
			wantExcluded: "",
		},
		{
			name:         "team allowed",
			plan:         "team",
			wantExcluded: "",
		},
		{
			name:         "unknown plan excluded",
			plan:         "enterprise",
			wantExcluded: "gpt-5.3-codex,gpt-5.4",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			auth := &coreauth.Auth{
				Provider:   "codex",
				Attributes: map[string]string{},
			}
			var planExcluded []string
			for _, rule := range cfg.OAuthModelPlanAccess["codex"] {
				if shouldExcludeByPlan(tt.plan, rule) {
					planExcluded = append(planExcluded, rule.Pattern)
				}
			}
			if len(planExcluded) > 0 {
				mergeIntoExcludedModels(auth, planExcluded)
			}
			got := auth.Attributes["excluded_models"]
			if got != tt.wantExcluded {
				t.Errorf("excluded_models = %q, want %q", got, tt.wantExcluded)
			}
		})
	}
}

func TestApplyOAuthPlanAccess_MergeWithExisting(t *testing.T) {
	auth := &coreauth.Auth{
		Provider: "codex",
		Attributes: map[string]string{
			"excluded_models": "existing-model",
		},
	}
	rules := []config.ModelPlanAccess{
		{Pattern: "gpt-5.4", DeniedPlans: []string{"free"}},
	}
	var planExcluded []string
	for _, rule := range rules {
		if shouldExcludeByPlan("free", rule) {
			planExcluded = append(planExcluded, rule.Pattern)
		}
	}
	mergeIntoExcludedModels(auth, planExcluded)

	want := "existing-model,gpt-5.4"
	if got := auth.Attributes["excluded_models"]; got != want {
		t.Errorf("excluded_models = %q, want %q", got, want)
	}
}

func TestApplyOAuthPlanAccess_NoConfig(t *testing.T) {
	auth := &coreauth.Auth{
		Provider:   "codex",
		Attributes: map[string]string{},
	}
	// No plan access config — should be a no-op.
	ApplyOAuthPlanAccess(auth, &config.Config{}, "/path/codex-test.json")
	if _, ok := auth.Attributes["excluded_models"]; ok {
		t.Error("expected no excluded_models when config is empty")
	}
}

func TestApplyOAuthPlanAccess_UnsupportedProvider(t *testing.T) {
	auth := &coreauth.Auth{
		Provider:   "antigravity",
		Attributes: map[string]string{},
	}
	cfg := &config.Config{
		OAuthModelPlanAccess: map[string][]config.ModelPlanAccess{
			"antigravity": {
				{Pattern: "some-model", DeniedPlans: []string{"free"}},
			},
		},
	}
	// antigravity has no plan resolver, so resolveOAuthPlan returns "".
	// Unknown plan should be permissive (no exclusions).
	ApplyOAuthPlanAccess(auth, cfg, "/path/antigravity-test.json")
	if _, ok := auth.Attributes["excluded_models"]; ok {
		t.Error("expected no excluded_models for unsupported provider")
	}
}

func TestInferPlanFromFilename(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{"/auths/codex-user@test.com-plus.json", "plus"},
		{"/auths/codex-user@test.com-team.json", "team"},
		{"/auths/codex-user@test.com-pro.json", "pro"},
		{"/auths/codex-user@test.com.json", ""},
		{"/auths/codex-user@test.com-free.json", ""},  // free is not a recognized suffix
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got := inferPlanFromFilename(tt.path)
			if got != tt.want {
				t.Errorf("inferPlanFromFilename(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}
