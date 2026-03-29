package cliproxy

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
)

func TestApplyOAuthModelAlias_Rename(t *testing.T) {
	cfg := &config.Config{
		OAuthModelAlias: map[string][]config.OAuthModelAlias{
			"codex": {
				{Name: "gpt-5", Alias: "g5"},
			},
		},
	}
	models := []*ModelInfo{
		{ID: "gpt-5", Name: "models/gpt-5"},
	}

	out := applyOAuthModelAlias(cfg, "codex", "oauth", models)
	if len(out) != 1 {
		t.Fatalf("expected 1 model, got %d", len(out))
	}
	if out[0].ID != "g5" {
		t.Fatalf("expected model id %q, got %q", "g5", out[0].ID)
	}
	if out[0].Name != "models/g5" {
		t.Fatalf("expected model name %q, got %q", "models/g5", out[0].Name)
	}
}

func TestApplyOAuthModelAlias_ForkAddsAlias(t *testing.T) {
	cfg := &config.Config{
		OAuthModelAlias: map[string][]config.OAuthModelAlias{
			"codex": {
				{Name: "gpt-5", Alias: "g5", Fork: true},
			},
		},
	}
	models := []*ModelInfo{
		{ID: "gpt-5", Name: "models/gpt-5"},
	}

	out := applyOAuthModelAlias(cfg, "codex", "oauth", models)
	if len(out) != 2 {
		t.Fatalf("expected 2 models, got %d", len(out))
	}
	if out[0].ID != "gpt-5" {
		t.Fatalf("expected first model id %q, got %q", "gpt-5", out[0].ID)
	}
	if out[1].ID != "g5" {
		t.Fatalf("expected second model id %q, got %q", "g5", out[1].ID)
	}
	if out[1].Name != "models/g5" {
		t.Fatalf("expected forked model name %q, got %q", "models/g5", out[1].Name)
	}
}

func TestApplyOAuthModelAlias_ForkAddsMultipleAliases(t *testing.T) {
	cfg := &config.Config{
		OAuthModelAlias: map[string][]config.OAuthModelAlias{
			"codex": {
				{Name: "gpt-5", Alias: "g5", Fork: true},
				{Name: "gpt-5", Alias: "g5-2", Fork: true},
			},
		},
	}
	models := []*ModelInfo{
		{ID: "gpt-5", Name: "models/gpt-5"},
	}

	out := applyOAuthModelAlias(cfg, "codex", "oauth", models)
	if len(out) != 3 {
		t.Fatalf("expected 3 models, got %d", len(out))
	}
	if out[0].ID != "gpt-5" {
		t.Fatalf("expected first model id %q, got %q", "gpt-5", out[0].ID)
	}
	if out[1].ID != "g5" {
		t.Fatalf("expected second model id %q, got %q", "g5", out[1].ID)
	}
	if out[1].Name != "models/g5" {
		t.Fatalf("expected forked model name %q, got %q", "models/g5", out[1].Name)
	}
	if out[2].ID != "g5-2" {
		t.Fatalf("expected third model id %q, got %q", "g5-2", out[2].ID)
	}
	if out[2].Name != "models/g5-2" {
		t.Fatalf("expected forked model name %q, got %q", "models/g5-2", out[2].Name)
	}
}

func TestApplyOAuthModelAlias_NameWithSuffixMatchesUpstream(t *testing.T) {
	cfg := &config.Config{
		OAuthModelAlias: map[string][]config.OAuthModelAlias{
			"antigravity": {
				{Name: "gemini-2.5-flash(0)", Alias: "claude-haiku-4-5-20251001", Fork: true},
			},
		},
	}
	models := []*ModelInfo{
		{ID: "gemini-2.5-flash", Name: "models/gemini-2.5-flash"},
	}

	out := applyOAuthModelAlias(cfg, "antigravity", "oauth", models)
	if len(out) != 2 {
		t.Fatalf("expected 2 models (original + alias), got %d", len(out))
	}
	if out[0].ID != "gemini-2.5-flash" {
		t.Fatalf("expected first model id %q, got %q", "gemini-2.5-flash", out[0].ID)
	}
	if out[1].ID != "claude-haiku-4-5-20251001" {
		t.Fatalf("expected second model id %q, got %q", "claude-haiku-4-5-20251001", out[1].ID)
	}
}

func TestApplyOAuthModelAlias_NameWithSuffixRename(t *testing.T) {
	cfg := &config.Config{
		OAuthModelAlias: map[string][]config.OAuthModelAlias{
			"antigravity": {
				{Name: "gemini-2.5-flash(0)", Alias: "claude-haiku"},
			},
		},
	}
	models := []*ModelInfo{
		{ID: "gemini-2.5-flash", Name: "models/gemini-2.5-flash"},
	}

	out := applyOAuthModelAlias(cfg, "antigravity", "oauth", models)
	if len(out) != 1 {
		t.Fatalf("expected 1 model, got %d", len(out))
	}
	if out[0].ID != "claude-haiku" {
		t.Fatalf("expected model id %q, got %q", "claude-haiku", out[0].ID)
	}
}

func TestApplyOAuthModelAlias_MultipleSuffixedNamesSameBase(t *testing.T) {
	cfg := &config.Config{
		OAuthModelAlias: map[string][]config.OAuthModelAlias{
			"antigravity": {
				{Name: "gemini-2.5-flash(0)", Alias: "alias-a", Fork: true},
				{Name: "gemini-2.5-flash(high)", Alias: "alias-b", Fork: true},
			},
		},
	}
	models := []*ModelInfo{
		{ID: "gemini-2.5-flash", Name: "models/gemini-2.5-flash"},
	}

	out := applyOAuthModelAlias(cfg, "antigravity", "oauth", models)
	if len(out) != 3 {
		t.Fatalf("expected 3 models (original + 2 aliases), got %d", len(out))
	}
	if out[0].ID != "gemini-2.5-flash" {
		t.Fatalf("expected first model id %q, got %q", "gemini-2.5-flash", out[0].ID)
	}
	if out[1].ID != "alias-a" {
		t.Fatalf("expected second model id %q, got %q", "alias-a", out[1].ID)
	}
	if out[2].ID != "alias-b" {
		t.Fatalf("expected third model id %q, got %q", "alias-b", out[2].ID)
	}
}
