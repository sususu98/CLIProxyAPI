package antigravity

import (
	"fmt"
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

// --- Tests for nearestSupportedLevel ---

func TestNearestSupportedLevel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		level     string
		supported []string
		want      string
	}{
		// Direct matches
		{"direct match low", "low", []string{"low", "high"}, "low"},
		{"direct match high", "high", []string{"low", "high"}, "high"},
		{"direct match case insensitive", "HIGH", []string{"low", "high"}, "high"},
		{"direct match with whitespace", " high ", []string{" high ", "low"}, "high"},

		// Nearest level mapping for Gemini 3.x (Levels: ["low", "high"])
		{"minimal nearest to low", "minimal", []string{"low", "high"}, "low"},
		{"medium tie prefers high", "medium", []string{"low", "high"}, "high"},
		{"xhigh nearest to high", "xhigh", []string{"low", "high"}, "high"},

		// Models with three levels
		{"minimal nearest to low in 3-level", "minimal", []string{"low", "medium", "high"}, "low"},
		{"xhigh nearest to high in 3-level", "xhigh", []string{"low", "medium", "high"}, "high"},
		{"medium direct match in 3-level", "medium", []string{"low", "medium", "high"}, "medium"},

		// Single level models
		{"minimal to single high", "minimal", []string{"high"}, "high"},
		{"xhigh to single low", "xhigh", []string{"low"}, "low"},

		// Unknown level returns empty
		{"unknown level", "turbo", []string{"low", "high"}, ""},

		// Empty supported returns empty
		{"empty supported", "high", []string{}, ""},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := nearestSupportedLevel(tt.level, tt.supported)
			if got != tt.want {
				t.Errorf("nearestSupportedLevel(%q, %v) = %q, want %q", tt.level, tt.supported, got, tt.want)
			}
		})
	}
}

// --- Tests for budgetToLevel ---

func TestBudgetToLevel(t *testing.T) {
	t.Parallel()

	applier := &Applier{}
	// Primary fixture: matches real gemini-3.1-pro-high config
	gemini31Model := &registry.ModelInfo{
		ID:       "gemini-3.1-pro-high",
		Thinking: &registry.ThinkingSupport{Min: 128, Max: 32768, ZeroAllowed: false, DynamicAllowed: true, Levels: []string{"low", "medium", "high"}},
	}
	// Secondary fixture: 2-level model (e.g., gemini-3-pro-high)
	gemini3Model := &registry.ModelInfo{
		ID:       "gemini-3-pro-high",
		Thinking: &registry.ThinkingSupport{Min: 128, Max: 32768, ZeroAllowed: false, DynamicAllowed: true, Levels: []string{"low", "high"}},
	}

	tests := []struct {
		name      string
		model     *registry.ModelInfo
		budget    int
		wantOK    bool
		wantLevel thinking.ThinkingLevel
	}{
		// 3-level model: ["low", "medium", "high"]
		{"3L: budget 512 (minimal range) → low", gemini31Model, 512, true, thinking.LevelLow},
		{"3L: budget 1024 (low range) → low", gemini31Model, 1024, true, thinking.LevelLow},
		{"3L: budget 5000 (medium range) → medium", gemini31Model, 5000, true, thinking.LevelMedium},
		{"3L: budget 10001 (high range) → high", gemini31Model, 10001, true, thinking.LevelHigh},
		{"3L: budget 64000 (xhigh range) → high", gemini31Model, 64000, true, thinking.LevelHigh},
		{"3L: budget 1 (minimal range) → low", gemini31Model, 1, true, thinking.LevelLow},

		// 2-level model: ["low", "high"] — tie at medium prefers high
		{"2L: budget 5000 (medium range) → high (tie prefers high)", gemini3Model, 5000, true, thinking.LevelHigh},
		{"2L: budget 512 → low", gemini3Model, 512, true, thinking.LevelLow},
		{"2L: budget 64000 → high", gemini3Model, 64000, true, thinking.LevelHigh},

		// Special values should NOT convert
		{"budget 0 (none) → no conversion", gemini31Model, 0, false, ""},
		{"budget -1 (auto) → no conversion", gemini31Model, -1, false, ""},
		{"budget -2 (invalid) → no conversion", gemini31Model, -2, false, ""},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			config := thinking.ThinkingConfig{Mode: thinking.ModeBudget, Budget: tt.budget}
			result, ok := applier.budgetToLevel(config, tt.model)
			if ok != tt.wantOK {
				t.Fatalf("budgetToLevel(budget=%d) ok = %v, want %v", tt.budget, ok, tt.wantOK)
			}
			if ok {
				if result.Mode != thinking.ModeLevel {
					t.Errorf("Mode = %v, want ModeLevel", result.Mode)
				}
				if result.Level != tt.wantLevel {
					t.Errorf("Level = %q, want %q", result.Level, tt.wantLevel)
				}
				if result.Budget != 0 {
					t.Errorf("Budget = %d, want 0 (should be cleared)", result.Budget)
				}
			}
		})
	}
}

// --- Tests for Apply with Gemini hybrid models ---

func TestApply_GeminiHybrid_BudgetToLevelConversion(t *testing.T) {
	t.Parallel()

	applier := &Applier{}
	modelInfo := &registry.ModelInfo{
		ID:       "gemini-3.1-pro-high",
		Thinking: &registry.ThinkingSupport{Min: 128, Max: 32768, ZeroAllowed: false, DynamicAllowed: true, Levels: []string{"low", "medium", "high"}},
	}

	tests := []struct {
		name      string
		budget    int
		wantLevel string
	}{
		{"budget 64000 → thinkingLevel high", 64000, "high"},
		{"budget 10001 → thinkingLevel high", 10001, "high"},
		{"budget 5000 → thinkingLevel medium", 5000, "medium"},
		{"budget 512 → thinkingLevel low", 512, "low"},
		{"budget 1024 → thinkingLevel low", 1024, "low"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			body := []byte(`{"request":{"generationConfig":{"thinkingConfig":{"thinkingBudget":` + itoa(tt.budget) + `,"includeThoughts":true}}}}`)
			config := thinking.ThinkingConfig{Mode: thinking.ModeBudget, Budget: tt.budget}

			result, err := applier.Apply(body, config, modelInfo)
			if err != nil {
				t.Fatalf("Apply error: %v", err)
			}

			// Should have thinkingLevel, NOT thinkingBudget
			gotLevel := gjson.GetBytes(result, "request.generationConfig.thinkingConfig.thinkingLevel").String()
			if gotLevel != tt.wantLevel {
				t.Errorf("thinkingLevel = %q, want %q", gotLevel, tt.wantLevel)
			}

			// thinkingBudget should be removed
			if gjson.GetBytes(result, "request.generationConfig.thinkingConfig.thinkingBudget").Exists() {
				t.Errorf("thinkingBudget should not exist after budget→level conversion, got %v",
					gjson.GetBytes(result, "request.generationConfig.thinkingConfig.thinkingBudget").Value())
			}

			// includeThoughts should be true
			if !gjson.GetBytes(result, "request.generationConfig.thinkingConfig.includeThoughts").Bool() {
				t.Errorf("includeThoughts should be true")
			}
		})
	}
}

func TestApply_GeminiHybrid_ModeAutoKeepsBudgetFormat(t *testing.T) {
	t.Parallel()

	applier := &Applier{}
	modelInfo := &registry.ModelInfo{
		ID:       "gemini-3.1-pro-high",
		Thinking: &registry.ThinkingSupport{Min: 128, Max: 32768, ZeroAllowed: false, DynamicAllowed: true, Levels: []string{"low", "medium", "high"}},
	}

	body := []byte(`{"request":{"generationConfig":{}}}`)
	config := thinking.ThinkingConfig{Mode: thinking.ModeAuto, Budget: -1}

	result, err := applier.Apply(body, config, modelInfo)
	if err != nil {
		t.Fatalf("Apply error: %v", err)
	}

	// ModeAuto should keep thinkingBudget=-1, NOT convert to level
	gotBudget := gjson.GetBytes(result, "request.generationConfig.thinkingConfig.thinkingBudget").Int()
	if gotBudget != -1 {
		t.Errorf("thinkingBudget = %d, want -1 (ModeAuto should not convert to level)", gotBudget)
	}

	// thinkingLevel should NOT be set
	if gjson.GetBytes(result, "request.generationConfig.thinkingConfig.thinkingLevel").Exists() {
		t.Errorf("thinkingLevel should not exist for ModeAuto")
	}
}

func TestApply_GeminiHybrid_ModeLevelPassthrough(t *testing.T) {
	t.Parallel()

	applier := &Applier{}
	modelInfo := &registry.ModelInfo{
		ID:       "gemini-3.1-pro-high",
		Thinking: &registry.ThinkingSupport{Min: 128, Max: 32768, ZeroAllowed: false, DynamicAllowed: true, Levels: []string{"low", "medium", "high"}},
	}

	body := []byte(`{"request":{"generationConfig":{}}}`)
	config := thinking.ThinkingConfig{Mode: thinking.ModeLevel, Level: thinking.LevelHigh}

	result, err := applier.Apply(body, config, modelInfo)
	if err != nil {
		t.Fatalf("Apply error: %v", err)
	}

	// ModeLevel should pass through to applyLevelFormat directly
	gotLevel := gjson.GetBytes(result, "request.generationConfig.thinkingConfig.thinkingLevel").String()
	if gotLevel != "high" {
		t.Errorf("thinkingLevel = %q, want %q", gotLevel, "high")
	}

	if gjson.GetBytes(result, "request.generationConfig.thinkingConfig.thinkingBudget").Exists() {
		t.Errorf("thinkingBudget should not exist for ModeLevel")
	}
}

func TestApply_ClaudeModel_SkipsBudgetToLevelConversion(t *testing.T) {
	t.Parallel()

	applier := &Applier{}
	// Hypothetical Claude model with levels (shouldn't exist, but tests the isClaude guard)
	modelInfo := &registry.ModelInfo{
		ID:       "claude-sonnet-4-5",
		Thinking: &registry.ThinkingSupport{Min: 1024, Max: 128000, ZeroAllowed: true, DynamicAllowed: true, Levels: []string{"low", "high"}},
	}

	body := buildTestBody(t, 64000, 10001)
	config := thinking.ThinkingConfig{Mode: thinking.ModeBudget, Budget: 10001}

	result, err := applier.Apply(body, config, modelInfo)
	if err != nil {
		t.Fatalf("Apply error: %v", err)
	}

	// Claude models should keep budget format even if they have Levels
	if !gjson.GetBytes(result, "request.generationConfig.thinkingConfig.thinkingBudget").Exists() {
		t.Errorf("Claude model should use thinkingBudget, not thinkingLevel")
	}
}

func TestApply_GeminiHybrid_IncludeThoughtsFalsePreserved(t *testing.T) {
	t.Parallel()

	applier := &Applier{}
	modelInfo := &registry.ModelInfo{
		ID:       "gemini-3.1-pro-high",
		Thinking: &registry.ThinkingSupport{Min: 128, Max: 32768, ZeroAllowed: false, DynamicAllowed: true, Levels: []string{"low", "medium", "high"}},
	}

	// Explicitly set includeThoughts=false
	body := []byte(`{"request":{"generationConfig":{"thinkingConfig":{"thinkingBudget":10001,"includeThoughts":false}}}}`)
	config := thinking.ThinkingConfig{Mode: thinking.ModeBudget, Budget: 10001}

	result, err := applier.Apply(body, config, modelInfo)
	if err != nil {
		t.Fatalf("Apply error: %v", err)
	}

	// After budget→level conversion, includeThoughts=false should be preserved
	if gjson.GetBytes(result, "request.generationConfig.thinkingConfig.includeThoughts").Bool() {
		t.Errorf("includeThoughts should be false (user explicitly set it)")
	}

	gotLevel := gjson.GetBytes(result, "request.generationConfig.thinkingConfig.thinkingLevel").String()
	if gotLevel != "high" {
		t.Errorf("thinkingLevel = %q, want %q", gotLevel, "high")
	}
}

func TestApply_GeminiHybrid_InvalidBudgetFallsThroughToBudgetFormat(t *testing.T) {
	t.Parallel()

	applier := &Applier{}
	modelInfo := &registry.ModelInfo{
		ID:       "gemini-3.1-pro-high",
		Thinking: &registry.ThinkingSupport{Min: 128, Max: 32768, ZeroAllowed: false, DynamicAllowed: true, Levels: []string{"low", "medium", "high"}},
	}

	// budget < -1 is invalid for ConvertBudgetToLevel, should fall through to applyBudgetFormat
	body := []byte(`{"request":{"generationConfig":{"thinkingConfig":{"thinkingBudget":-5,"includeThoughts":true}}}}`)
	config := thinking.ThinkingConfig{Mode: thinking.ModeBudget, Budget: -5}

	result, err := applier.Apply(body, config, modelInfo)
	if err != nil {
		t.Fatalf("Apply error: %v", err)
	}

	// Should fall through to budget format since conversion failed
	if !gjson.GetBytes(result, "request.generationConfig.thinkingConfig.thinkingBudget").Exists() {
		t.Errorf("thinkingBudget should exist when budget→level conversion fails")
	}
	if gjson.GetBytes(result, "request.generationConfig.thinkingConfig.thinkingLevel").Exists() {
		t.Errorf("thinkingLevel should not exist when budget→level conversion fails")
	}
}

// itoa converts int to string for test body construction.
func itoa(n int) string {
	return fmt.Sprintf("%d", n)
}
