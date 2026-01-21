package auth

import (
	"testing"

	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
)

func TestIsThinkingEnabledInPayload(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		format  sdktranslator.Format
		payload string
		want    bool
	}{
		{
			name:    "claude enabled",
			format:  sdktranslator.FormatClaude,
			payload: `{"model":"claude-sonnet-4-5","thinking":{"type":"enabled","budget_tokens":8192}}`,
			want:    true,
		},
		{
			name:    "claude disabled",
			format:  sdktranslator.FormatClaude,
			payload: `{"model":"claude-sonnet-4-5","thinking":{"type":"disabled","budget_tokens":8192}}`,
			want:    false,
		},
		{
			name:    "openai reasoning none",
			format:  sdktranslator.FormatOpenAI,
			payload: `{"model":"gpt-5","reasoning_effort":"none"}`,
			want:    false,
		},
		{
			name:    "openai reasoning high",
			format:  sdktranslator.FormatOpenAI,
			payload: `{"model":"gpt-5","reasoning_effort":"high"}`,
			want:    true,
		},
		{
			name:    "openai response reasoning medium",
			format:  sdktranslator.FormatOpenAIResponse,
			payload: `{"model":"gpt-5","reasoning":{"effort":"medium"}}`,
			want:    true,
		},
		{
			name:    "gemini budget none",
			format:  sdktranslator.FormatGemini,
			payload: `{"model":"gemini-2.5-pro","generationConfig":{"thinkingConfig":{"thinkingBudget":0}}}`,
			want:    false,
		},
		{
			name:    "gemini budget enabled",
			format:  sdktranslator.FormatGemini,
			payload: `{"model":"gemini-2.5-pro","generationConfig":{"thinkingConfig":{"thinkingBudget":8192}}}`,
			want:    true,
		},
		{
			name:    "gemini cli level none",
			format:  sdktranslator.FormatGeminiCLI,
			payload: `{"model":"gemini-3-pro","generationConfig":{"thinkingConfig":{"thinkingLevel":"none"}}}`,
			want:    false,
		},
		{
			name:    "gemini cli level high",
			format:  sdktranslator.FormatGeminiCLI,
			payload: `{"model":"gemini-3-pro","generationConfig":{"thinkingConfig":{"thinkingLevel":"high"}}}`,
			want:    true,
		},
		{
			name:    "suffix none",
			format:  sdktranslator.FormatClaude,
			payload: `{"model":"claude-sonnet-4-5(none)"}`,
			want:    false,
		},
		{
			name:    "suffix auto",
			format:  sdktranslator.FormatClaude,
			payload: `{"model":"claude-sonnet-4-5(auto)"}`,
			want:    true,
		},
		{
			name:    "suffix budget",
			format:  sdktranslator.FormatClaude,
			payload: `{"model":"claude-sonnet-4-5(8192)"}`,
			want:    true,
		},
		{
			name:    "suffix zero",
			format:  sdktranslator.FormatClaude,
			payload: `{"model":"claude-sonnet-4-5(0)"}`,
			want:    false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := isThinkingEnabledInPayload([]byte(tt.payload), tt.format)
			if got != tt.want {
				t.Fatalf("isThinkingEnabledInPayload() = %v, want %v", got, tt.want)
			}
		})
	}
}
