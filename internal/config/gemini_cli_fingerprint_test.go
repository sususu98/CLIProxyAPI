package config

import "testing"

func TestGeminiCLIFingerprint_ResolveUASuffix(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		cfg      GeminiCLIFingerprint
		expected string
	}{
		{"empty returns empty", GeminiCLIFingerprint{}, ""},
		{"whitespace-only returns empty", GeminiCLIFingerprint{UASuffix: "  \t "}, ""},
		{"custom value used", GeminiCLIFingerprint{UASuffix: "google-api-nodejs-client/10.5.0"}, "google-api-nodejs-client/10.5.0"},
		{"custom value trimmed", GeminiCLIFingerprint{UASuffix: "  custom/suffix  "}, "custom/suffix"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := tt.cfg.ResolveUASuffix()
			if got != tt.expected {
				t.Errorf("ResolveUASuffix() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestGeminiCLIFingerprint_ResolveAPIClient(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		cfg          GeminiCLIFingerprint
		defaultValue string
		expected     string
	}{
		{"empty returns default", GeminiCLIFingerprint{}, "compiled/default", "compiled/default"},
		{"whitespace-only returns default", GeminiCLIFingerprint{APIClientOverride: "   "}, "compiled/default", "compiled/default"},
		{"custom override used", GeminiCLIFingerprint{APIClientOverride: "custom/header"}, "compiled/default", "custom/header"},
		{"custom override trimmed", GeminiCLIFingerprint{APIClientOverride: "  custom/header  "}, "compiled/default", "custom/header"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := tt.cfg.ResolveAPIClient(tt.defaultValue)
			if got != tt.expected {
				t.Errorf("ResolveAPIClient() = %q, want %q", got, tt.expected)
			}
		})
	}
}
