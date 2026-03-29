package diff

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

func TestSummarizeExcludedModels_NormalizesAndDedupes(t *testing.T) {
	summary := SummarizeExcludedModels([]string{"A", " a ", "B", "b"})
	if summary.count != 2 {
		t.Fatalf("expected 2 unique entries, got %d", summary.count)
	}
	if summary.hash == "" {
		t.Fatal("expected non-empty hash")
	}
	if empty := SummarizeExcludedModels(nil); empty.count != 0 || empty.hash != "" {
		t.Fatalf("expected empty summary for nil input, got %+v", empty)
	}
}

func TestDiffOAuthExcludedModelChanges(t *testing.T) {
	oldMap := map[string][]string{
		"ProviderA": {"model-1", "model-2"},
		"providerB": {"x"},
	}
	newMap := map[string][]string{
		"providerA": {"model-1", "model-3"},
		"providerC": {"y"},
	}

	changes, affected := DiffOAuthExcludedModelChanges(oldMap, newMap)
	expectContains(t, changes, "oauth-excluded-models[providera]: updated (2 -> 2 entries)")
	expectContains(t, changes, "oauth-excluded-models[providerb]: removed")
	expectContains(t, changes, "oauth-excluded-models[providerc]: added (1 entries)")

	if len(affected) != 3 {
		t.Fatalf("expected 3 affected providers, got %d", len(affected))
	}
}

func TestSummarizeAmpModelMappings(t *testing.T) {
	summary := SummarizeAmpModelMappings([]config.AmpModelMapping{
		{From: "a", To: "A"},
		{From: "b", To: "B"},
		{From: " ", To: " "}, // ignored
	})
	if summary.count != 2 {
		t.Fatalf("expected 2 entries, got %d", summary.count)
	}
	if summary.hash == "" {
		t.Fatal("expected non-empty hash")
	}
	if empty := SummarizeAmpModelMappings(nil); empty.count != 0 || empty.hash != "" {
		t.Fatalf("expected empty summary for nil input, got %+v", empty)
	}
	if blank := SummarizeAmpModelMappings([]config.AmpModelMapping{{From: " ", To: " "}}); blank.count != 0 || blank.hash != "" {
		t.Fatalf("expected blank mappings ignored, got %+v", blank)
	}
}

func TestSummarizeOAuthExcludedModels_NormalizesKeys(t *testing.T) {
	out := SummarizeOAuthExcludedModels(map[string][]string{
		"ProvA": {"X"},
		"":      {"ignored"},
	})
	if len(out) != 1 {
		t.Fatalf("expected only non-empty key summary, got %d", len(out))
	}
	if _, ok := out["prova"]; !ok {
		t.Fatalf("expected normalized key 'prova', got keys %v", out)
	}
	if out["prova"].count != 1 || out["prova"].hash == "" {
		t.Fatalf("unexpected summary %+v", out["prova"])
	}
	if outEmpty := SummarizeOAuthExcludedModels(nil); outEmpty != nil {
		t.Fatalf("expected nil map for nil input, got %v", outEmpty)
	}
}

func TestSummarizeVertexModels(t *testing.T) {
	summary := SummarizeVertexModels([]config.VertexCompatModel{
		{Name: "m1"},
		{Name: " ", Alias: "alias"},
		{}, // ignored
	})
	if summary.count != 2 {
		t.Fatalf("expected 2 vertex models, got %d", summary.count)
	}
	if summary.hash == "" {
		t.Fatal("expected non-empty hash")
	}
	if empty := SummarizeVertexModels(nil); empty.count != 0 || empty.hash != "" {
		t.Fatalf("expected empty summary for nil input, got %+v", empty)
	}
	if blank := SummarizeVertexModels([]config.VertexCompatModel{{Name: " "}}); blank.count != 0 || blank.hash != "" {
		t.Fatalf("expected blank model ignored, got %+v", blank)
	}
}

func expectContains(t *testing.T, list []string, target string) {
	t.Helper()
	for _, entry := range list {
		if entry == target {
			return
		}
	}
	t.Fatalf("expected list to contain %q, got %#v", target, list)
}

func TestDiffOAuthModelPlanAccessChanges(t *testing.T) {
	tests := []struct {
		name         string
		oldMap       map[string][]config.ModelPlanAccess
		newMap       map[string][]config.ModelPlanAccess
		wantChanges  int
		wantAffected int
	}{
		{
			name:         "both nil",
			wantChanges:  0,
			wantAffected: 0,
		},
		{
			name:   "added provider",
			oldMap: nil,
			newMap: map[string][]config.ModelPlanAccess{
				"codex": {{Pattern: "gpt-5.4", DeniedPlans: []string{"free"}}},
			},
			wantChanges:  1,
			wantAffected: 1,
		},
		{
			name: "removed provider",
			oldMap: map[string][]config.ModelPlanAccess{
				"codex": {{Pattern: "gpt-5.4", DeniedPlans: []string{"free"}}},
			},
			newMap:       nil,
			wantChanges:  1,
			wantAffected: 1,
		},
		{
			name: "updated rules",
			oldMap: map[string][]config.ModelPlanAccess{
				"codex": {{Pattern: "gpt-5.4", DeniedPlans: []string{"free"}}},
			},
			newMap: map[string][]config.ModelPlanAccess{
				"codex": {{Pattern: "gpt-5.4", DeniedPlans: []string{"free", "team"}}},
			},
			wantChanges:  1,
			wantAffected: 1,
		},
		{
			name: "no change",
			oldMap: map[string][]config.ModelPlanAccess{
				"codex": {{Pattern: "gpt-5.4", DeniedPlans: []string{"free"}}},
			},
			newMap: map[string][]config.ModelPlanAccess{
				"codex": {{Pattern: "gpt-5.4", DeniedPlans: []string{"free"}}},
			},
			wantChanges:  0,
			wantAffected: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			changes, affected := DiffOAuthModelPlanAccessChanges(tt.oldMap, tt.newMap)
			if len(changes) != tt.wantChanges {
				t.Errorf("changes count = %d, want %d; changes: %v", len(changes), tt.wantChanges, changes)
			}
			if len(affected) != tt.wantAffected {
				t.Errorf("affected count = %d, want %d; affected: %v", len(affected), tt.wantAffected, affected)
			}
		})
	}
}
