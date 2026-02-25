package config

import "testing"

func TestAntigravityUserAgents_ResolveAPIAgent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		ua       AntigravityUserAgents
		expected string
	}{
		{"empty config returns default", AntigravityUserAgents{}, DefaultAntigravityAPIAgent},
		{"whitespace-only returns default", AntigravityUserAgents{API: "   "}, DefaultAntigravityAPIAgent},
		{"custom value used", AntigravityUserAgents{API: "custom/1.0"}, "custom/1.0"},
		{"custom value trimmed", AntigravityUserAgents{API: "  custom/1.0  "}, "custom/1.0"},
		{"client field does not affect API", AntigravityUserAgents{Client: "other/2.0"}, DefaultAntigravityAPIAgent},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := tt.ua.ResolveAPIAgent()
			if got != tt.expected {
				t.Errorf("ResolveAPIAgent() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestAntigravityUserAgents_ResolveClientAgent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		ua       AntigravityUserAgents
		expected string
	}{
		{"empty config returns default", AntigravityUserAgents{}, DefaultAntigravityClientAgent},
		{"whitespace-only returns default", AntigravityUserAgents{Client: "  \t "}, DefaultAntigravityClientAgent},
		{"custom value used", AntigravityUserAgents{Client: "my-client/3.0"}, "my-client/3.0"},
		{"custom value trimmed", AntigravityUserAgents{Client: "  my-client/3.0  "}, "my-client/3.0"},
		{"api field does not affect client", AntigravityUserAgents{API: "other/1.0"}, DefaultAntigravityClientAgent},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := tt.ua.ResolveClientAgent()
			if got != tt.expected {
				t.Errorf("ResolveClientAgent() = %q, want %q", got, tt.expected)
			}
		})
	}
}
