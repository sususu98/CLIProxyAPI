package config

import "testing"

func TestSanitizeOAuthModelAlias_PreservesForkFlag(t *testing.T) {
	cfg := &Config{
		OAuthModelAlias: map[string][]OAuthModelAlias{
			" CoDeX ": {
				{Name: " gpt-5 ", Alias: " g5 ", Fork: true},
				{Name: "gpt-6", Alias: "g6"},
			},
		},
	}

	cfg.SanitizeOAuthModelAlias()

	aliases := cfg.OAuthModelAlias["codex"]
	if len(aliases) != 2 {
		t.Fatalf("expected 2 sanitized aliases, got %d", len(aliases))
	}
	if aliases[0].Name != "gpt-5" || aliases[0].Alias != "g5" || !aliases[0].Fork {
		t.Fatalf("expected first alias to be gpt-5->g5 fork=true, got name=%q alias=%q fork=%v", aliases[0].Name, aliases[0].Alias, aliases[0].Fork)
	}
	if aliases[1].Name != "gpt-6" || aliases[1].Alias != "g6" || aliases[1].Fork {
		t.Fatalf("expected second alias to be gpt-6->g6 fork=false, got name=%q alias=%q fork=%v", aliases[1].Name, aliases[1].Alias, aliases[1].Fork)
	}
}

func TestSanitizeOAuthModelAlias_AllowsMultipleAliasesForSameName(t *testing.T) {
	cfg := &Config{
		OAuthModelAlias: map[string][]OAuthModelAlias{
			"antigravity": {
				{Name: "gemini-claude-opus-4-5-thinking", Alias: "claude-opus-4-5-20251101", Fork: true},
				{Name: "gemini-claude-opus-4-5-thinking", Alias: "claude-opus-4-5-20251101-thinking", Fork: true},
				{Name: "gemini-claude-opus-4-5-thinking", Alias: "claude-opus-4-5", Fork: true},
			},
		},
	}

	cfg.SanitizeOAuthModelAlias()

	aliases := cfg.OAuthModelAlias["antigravity"]
	expected := []OAuthModelAlias{
		{Name: "gemini-claude-opus-4-5-thinking", Alias: "claude-opus-4-5-20251101", Fork: true},
		{Name: "gemini-claude-opus-4-5-thinking", Alias: "claude-opus-4-5-20251101-thinking", Fork: true},
		{Name: "gemini-claude-opus-4-5-thinking", Alias: "claude-opus-4-5", Fork: true},
	}
	if len(aliases) != len(expected) {
		t.Fatalf("expected %d sanitized aliases, got %d", len(expected), len(aliases))
	}
	for i, exp := range expected {
		if aliases[i].Name != exp.Name || aliases[i].Alias != exp.Alias || aliases[i].Fork != exp.Fork {
			t.Fatalf("expected alias %d to be name=%q alias=%q fork=%v, got name=%q alias=%q fork=%v", i, exp.Name, exp.Alias, exp.Fork, aliases[i].Name, aliases[i].Alias, aliases[i].Fork)
		}
	}
}

func TestSanitizeOAuthModelAlias_PreservesRoutingFields(t *testing.T) {
	cfg := &Config{
		OAuthModelAlias: map[string][]OAuthModelAlias{
			"antigravity": {
				{
					Name:                  " claude-opus-4-5-thinking ",
					Alias:                 " claude-opus-4-5 ",
					Fork:                  true,
					ForceMapping:          true,
					ToThinking:            " claude-opus-4-5-thinking ",
					ToNonThinking:         " claude-opus-4-5 ",
					StripThinkingResponse: true,
				},
			},
		},
	}

	cfg.SanitizeOAuthModelAlias()

	aliases := cfg.OAuthModelAlias["antigravity"]
	if len(aliases) != 1 {
		t.Fatalf("expected 1 sanitized alias, got %d", len(aliases))
	}
	got := aliases[0]
	if got.Name != "claude-opus-4-5-thinking" || got.Alias != "claude-opus-4-5" || !got.ForceMapping {
		t.Fatalf("expected alias to be normalized with force-mapping, got name=%q alias=%q force=%v", got.Name, got.Alias, got.ForceMapping)
	}
	if got.ToThinking != "claude-opus-4-5-thinking" || got.ToNonThinking != "claude-opus-4-5" {
		t.Fatalf("expected thinking routing fields to be trimmed, got toThinking=%q toNonThinking=%q", got.ToThinking, got.ToNonThinking)
	}
	if !got.StripThinkingResponse {
		t.Fatalf("expected strip-thinking-response to be preserved")
	}
}
