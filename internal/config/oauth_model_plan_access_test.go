package config

import (
	"testing"
)

func TestNormalizeOAuthModelPlanAccess(t *testing.T) {
	tests := []struct {
		name  string
		input map[string][]ModelPlanAccess
		want  int // number of providers in result
	}{
		{
			name:  "nil input",
			input: nil,
			want:  0,
		},
		{
			name:  "empty input",
			input: map[string][]ModelPlanAccess{},
			want:  0,
		},
		{
			name: "valid denied-plans",
			input: map[string][]ModelPlanAccess{
				"codex": {
					{Pattern: "gpt-5.4", DeniedPlans: []string{"free"}},
				},
			},
			want: 1,
		},
		{
			name: "valid allowed-plans",
			input: map[string][]ModelPlanAccess{
				"codex": {
					{Pattern: "gpt-5.4", AllowedPlans: []string{"plus", "team"}},
				},
			},
			want: 1,
		},
		{
			name: "mutual exclusion: both set drops rule",
			input: map[string][]ModelPlanAccess{
				"codex": {
					{Pattern: "gpt-5.4", AllowedPlans: []string{"plus"}, DeniedPlans: []string{"free"}},
				},
			},
			want: 0, // rule dropped, provider has no valid rules → not in output
		},
		{
			name: "empty pattern dropped",
			input: map[string][]ModelPlanAccess{
				"codex": {
					{Pattern: "", DeniedPlans: []string{"free"}},
				},
			},
			want: 0,
		},
		{
			name: "empty plans dropped",
			input: map[string][]ModelPlanAccess{
				"codex": {
					{Pattern: "gpt-5.4"},
				},
			},
			want: 0,
		},
		{
			name: "normalizes provider key to lowercase",
			input: map[string][]ModelPlanAccess{
				"Codex": {
					{Pattern: "gpt-5.4", DeniedPlans: []string{"free"}},
				},
			},
			want: 1,
		},
		{
			name: "deduplicates plans",
			input: map[string][]ModelPlanAccess{
				"codex": {
					{Pattern: "gpt-5.4", DeniedPlans: []string{"free", "FREE", " free "}},
				},
			},
			want: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := NormalizeOAuthModelPlanAccess(tt.input)
			if tt.want == 0 {
				if result != nil {
					t.Errorf("expected nil result, got %v", result)
				}
				return
			}
			if len(result) != tt.want {
				t.Errorf("provider count = %d, want %d; result: %v", len(result), tt.want, result)
			}
		})
	}
}

func TestNormalizeOAuthModelPlanAccess_DeduplicatesPlans(t *testing.T) {
	input := map[string][]ModelPlanAccess{
		"codex": {
			{Pattern: "gpt-5.4", DeniedPlans: []string{"free", "FREE", " free ", "team"}},
		},
	}
	result := NormalizeOAuthModelPlanAccess(input)
	rules := result["codex"]
	if len(rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rules))
	}
	if len(rules[0].DeniedPlans) != 2 {
		t.Errorf("expected 2 deduplicated plans (free, team), got %v", rules[0].DeniedPlans)
	}
}
