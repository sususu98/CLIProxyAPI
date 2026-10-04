package cliproxy

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

// A valid catalog must publish normally; fencing tests use the same response shape.
func TestAntigravityAsyncProbe_ValidCatalogPublishesWithoutSearchHints(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"models":{"gemini-3.1-flash-lite":{}}}`))
	}))
	defer server.Close()
	svc := &Service{cfg: &config.Config{}}
	auth := antigravityTestAuth("valid-catalog-no-search", server.URL)
	t.Cleanup(func() { GlobalModelRegistry().UnregisterClient(auth.ID) })
	svc.registerModelsForAuth(t.Context(), auth)
	svc.WaitAntigravityProbes()
	models := GlobalModelRegistry().GetModelsForClient(auth.ID)
	if len(models) != 1 || models[0].ID != "gemini-3.1-flash-lite" || !models[0].SupportsWebSearch {
		t.Fatalf("valid catalog did not publish with static search capability: %+v", models)
	}
}

func TestAntigravitySearchCapabilitiesComeFromCatalog(t *testing.T) {
	const searchable = "gemini-3.1-flash-lite"
	const unsearchable = "claude-opus-4-6-thinking"
	definitions := make(map[string]*ModelInfo)
	for _, model := range registry.GetAntigravityModels() {
		definitions[model.ID] = model
	}
	if definitions[searchable] == nil || !definitions[searchable].SupportsWebSearch || definitions[unsearchable] == nil || definitions[unsearchable].SupportsWebSearch {
		t.Fatal("expected searchable Gemini and unsearchable Claude catalog fixtures")
	}
	svc := &Service{cfg: &config.Config{OAuthModelAlias: map[string][]config.OAuthModelAlias{
		"antigravity": {{Name: searchable, Alias: "search-alias"}, {Name: unsearchable, Alias: "no-search-alias"}},
	}}}
	auth := antigravityTestAuth("catalog-search", "http://127.0.0.1:1")
	auth.Prefix = "tenant"
	for _, tc := range []struct{ name, body string }{
		{"absent", `{"models":{"gemini-3.1-flash-lite":{},"claude-opus-4-6-thinking":{}}}`},
		{"empty", `{"models":{"gemini-3.1-flash-lite":{},"claude-opus-4-6-thinking":{}},"webSearchModelIds":[]}`},
		{"contradictory", `{"models":{"gemini-3.1-flash-lite":{},"claude-opus-4-6-thinking":{}},"webSearchModelIds":["claude-opus-4-6-thinking"]}`},
		{"malformed-ignored-field", `{"models":{"gemini-3.1-flash-lite":{},"claude-opus-4-6-thinking":{}},"webSearchModelIds":42}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hints, ok := parseAntigravityModelCapabilityHints([]byte(tc.body))
			if !ok || len(hints.ModelIDs) != 2 {
				t.Fatalf("entitlements not parsed: %+v", hints)
			}
			models := svc.antigravityModelsForHints(auth, hints)
			if len(models) != 4 {
				t.Fatalf("expected aliases and prefixes, got %+v", models)
			}
			for _, model := range models {
				want := false
				switch model.ID {
				case "search-alias", "tenant/search-alias":
					want = true
				case "no-search-alias", "tenant/no-search-alias":
				default:
					t.Fatalf("unexpected model %q", model.ID)
				}
				if model.SupportsWebSearch != want {
					t.Fatalf("%s search=%v, want catalog value %v", model.ID, model.SupportsWebSearch, want)
				}
			}
		})
	}
	hints, ok := parseAntigravityModelCapabilityHints([]byte(`{"webSearchModelIds":["gemini-3.1-flash-lite"]}`))
	if !ok || hints.ModelIDs != nil {
		t.Fatal("search-only response granted model entitlements")
	}
}
