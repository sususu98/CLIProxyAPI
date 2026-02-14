package antigravity

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/thinking"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func TestApply_ClampsMaxOutputTokens(t *testing.T) {
	t.Parallel()

	applier := &Applier{}
	modelInfo := &registry.ModelInfo{
		ID:                  "claude-opus-4-6-thinking",
		MaxCompletionTokens: 64000,
		Thinking:            &registry.ThinkingSupport{Min: 1024, Max: 128000, ZeroAllowed: true, DynamicAllowed: true},
	}

	tests := []struct {
		name         string
		maxOutput    int
		budget       int
		wantMax      int
		wantBudgetLT int
	}{
		{"exceeds model limit", 65536, 4096, 64000, 0},
		{"at model limit", 64000, 4096, 64000, 0},
		{"below model limit", 32000, 4096, 32000, 0},
		{"budget also clamped after maxOutput clamp", 65536, 64000, 64000, 64000},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			body := buildTestBody(t, tt.maxOutput, tt.budget)
			config := thinking.ThinkingConfig{Mode: thinking.ModeBudget, Budget: tt.budget}

			result, err := applier.Apply(body, config, modelInfo)
			if err != nil {
				t.Fatalf("Apply error: %v", err)
			}

			gotMax := int(gjson.GetBytes(result, "request.generationConfig.maxOutputTokens").Int())
			if gotMax != tt.wantMax {
				t.Errorf("maxOutputTokens = %d, want %d", gotMax, tt.wantMax)
			}

			if tt.wantBudgetLT > 0 {
				gotBudget := int(gjson.GetBytes(result, "request.generationConfig.thinkingConfig.thinkingBudget").Int())
				if gotBudget >= tt.wantBudgetLT {
					t.Errorf("thinkingBudget = %d, should be < %d", gotBudget, tt.wantBudgetLT)
				}
			}
		})
	}
}

func TestApply_NoMaxOutputTokens_UsesModelDefault(t *testing.T) {
	t.Parallel()

	applier := &Applier{}
	modelInfo := &registry.ModelInfo{
		ID:                  "claude-opus-4-6-thinking",
		MaxCompletionTokens: 64000,
		Thinking:            &registry.ThinkingSupport{Min: 1024, Max: 128000, ZeroAllowed: true, DynamicAllowed: true},
	}

	body := []byte(`{"request":{"generationConfig":{"thinkingConfig":{"thinkingBudget":4096,"includeThoughts":true}}}}`)
	config := thinking.ThinkingConfig{Mode: thinking.ModeBudget, Budget: 4096}

	result, err := applier.Apply(body, config, modelInfo)
	if err != nil {
		t.Fatalf("Apply error: %v", err)
	}

	gotMax := int(gjson.GetBytes(result, "request.generationConfig.maxOutputTokens").Int())
	if gotMax != 64000 {
		t.Errorf("maxOutputTokens = %d, want 64000 (model default)", gotMax)
	}
}

func buildTestBody(t *testing.T, maxOutputTokens, budget int) []byte {
	t.Helper()
	body := []byte(`{"request":{"generationConfig":{}}}`)
	body, _ = sjson.SetBytes(body, "request.generationConfig.maxOutputTokens", maxOutputTokens)
	body, _ = sjson.SetBytes(body, "request.generationConfig.thinkingConfig.thinkingBudget", budget)
	body, _ = sjson.SetBytes(body, "request.generationConfig.thinkingConfig.includeThoughts", true)
	return body
}
