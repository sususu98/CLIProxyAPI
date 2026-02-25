package executor

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

func TestResolveUserAgent_Priority(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		cfg      *config.Config
		auth     *cliproxyauth.Auth
		expected string
	}{
		{
			name:     "nil cfg and nil auth returns compiled default",
			cfg:      nil,
			auth:     nil,
			expected: config.DefaultAntigravityAPIAgent,
		},
		{
			name:     "empty config returns compiled default",
			cfg:      &config.Config{},
			auth:     nil,
			expected: config.DefaultAntigravityAPIAgent,
		},
		{
			name: "config value overrides default",
			cfg: &config.Config{
				AntigravityUserAgents: config.AntigravityUserAgents{API: "custom/1.0"},
			},
			auth:     nil,
			expected: "custom/1.0",
		},
		{
			name: "whitespace-only config falls back to default",
			cfg: &config.Config{
				AntigravityUserAgents: config.AntigravityUserAgents{API: "   "},
			},
			auth:     nil,
			expected: config.DefaultAntigravityAPIAgent,
		},
		{
			name: "auth metadata overrides config",
			cfg: &config.Config{
				AntigravityUserAgents: config.AntigravityUserAgents{API: "config/1.0"},
			},
			auth: &cliproxyauth.Auth{
				Metadata: map[string]interface{}{"user_agent": "metadata/2.0"},
			},
			expected: "metadata/2.0",
		},
		{
			name: "auth attributes overrides metadata",
			cfg: &config.Config{
				AntigravityUserAgents: config.AntigravityUserAgents{API: "config/1.0"},
			},
			auth: &cliproxyauth.Auth{
				Attributes: map[string]string{"user_agent": "attr/3.0"},
				Metadata:   map[string]interface{}{"user_agent": "metadata/2.0"},
			},
			expected: "attr/3.0",
		},
		{
			name: "empty auth attribute falls through to metadata",
			cfg:  &config.Config{},
			auth: &cliproxyauth.Auth{
				Attributes: map[string]string{"user_agent": ""},
				Metadata:   map[string]interface{}{"user_agent": "metadata/2.0"},
			},
			expected: "metadata/2.0",
		},
		{
			name: "whitespace auth attribute falls through to metadata",
			cfg:  &config.Config{},
			auth: &cliproxyauth.Auth{
				Attributes: map[string]string{"user_agent": "  "},
				Metadata:   map[string]interface{}{"user_agent": "metadata/2.0"},
			},
			expected: "metadata/2.0",
		},
		{
			name: "empty metadata falls through to config",
			cfg: &config.Config{
				AntigravityUserAgents: config.AntigravityUserAgents{API: "config/1.0"},
			},
			auth: &cliproxyauth.Auth{
				Metadata: map[string]interface{}{"user_agent": ""},
			},
			expected: "config/1.0",
		},
		{
			name: "non-string metadata falls through to config",
			cfg: &config.Config{
				AntigravityUserAgents: config.AntigravityUserAgents{API: "config/1.0"},
			},
			auth: &cliproxyauth.Auth{
				Metadata: map[string]interface{}{"user_agent": 42},
			},
			expected: "config/1.0",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := resolveUserAgent(tt.cfg, tt.auth)
			if got != tt.expected {
				t.Errorf("resolveUserAgent() = %q, want %q", got, tt.expected)
			}
		})
	}
}
