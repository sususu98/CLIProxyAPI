package session

import (
	"net/http"
	"strings"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func TestExtractClassificationAndAliases(t *testing.T) {
	claudeUserID := `{"device_id":"d","account_uuid":"","session_id":"11111111-1111-4111-8111-111111111111"}`

	tests := []struct {
		name           string
		headers        http.Header
		payload        string
		metadata       map[string]any
		wantID         string
		wantAliases    []string
		wantSource     string
		wantConfidence string
		wantScope      string
	}{
		{
			name:           "multiple explicit headers collected as representative ID and aliases",
			headers:        http.Header{"X-Gateway-Session-Id": {"gw-1"}, "X-Claude-Code-Session-Id": {"cc-1"}, "Session-Id": {"cx-1"}},
			wantID:         "gateway:gw-1",
			wantAliases:    []string{"claude:cc-1", "codex:cx-1"},
			wantSource:     SourceGatewayHeader,
			wantConfidence: ConfidenceHigh,
			wantScope:      ScopeSession,
		},
		{
			name:           "claude code header and metadata collected as representative ID and aliases",
			headers:        http.Header{"X-Claude-Code-Session-Id": {"cc-1"}},
			payload:        `{"metadata":{"user_id":` + quote(claudeUserID) + `}}`,
			wantID:         "claude:cc-1",
			wantAliases:    []string{"claude:11111111-1111-4111-8111-111111111111"},
			wantSource:     SourceClientNativeHeader,
			wantConfidence: ConfidenceHigh,
			wantScope:      ScopeSession,
		},
		{
			name:           "claude code metadata only",
			payload:        `{"metadata":{"user_id":` + quote(claudeUserID) + `}}`,
			wantID:         "claude:11111111-1111-4111-8111-111111111111",
			wantSource:     SourceMetadataUserID,
			wantConfidence: ConfidenceHigh,
			wantScope:      ScopeSession,
		},
		{
			name:           "claude code legacy metadata suffix",
			payload:        `{"metadata":{"user_id":"user_abc_account__session_11111111-1111-4111-8111-111111111111"}}`,
			wantID:         "claude:11111111-1111-4111-8111-111111111111",
			wantSource:     SourceMetadataUserID,
			wantConfidence: ConfidenceHigh,
			wantScope:      ScopeSession,
		},
		{
			name:           "codex hyphenated session header with prompt cache key alias",
			headers:        http.Header{"Session-Id": {"019f7e3b"}, "Thread-Id": {"019f7e3b"}},
			payload:        `{"prompt_cache_key":"019f7e3b"}`,
			wantID:         "codex:019f7e3b",
			wantAliases:    []string{"pck:019f7e3b"},
			wantSource:     SourceClientNativeHeader,
			wantConfidence: ConfidenceHigh,
			wantScope:      ScopeSession,
		},
		{
			name:           "opencode session header and generic header aliases",
			headers:        http.Header{"X-Opencode-Session": {"ses_a"}, "X-Session-Id": {"ses_b"}},
			wantID:         "opencode:ses_a",
			wantAliases:    []string{"header:ses_b"},
			wantSource:     SourceClientNativeHeader,
			wantConfidence: ConfidenceHigh,
			wantScope:      ScopeSession,
		},
		{
			name:           "opencode generic and affinity headers",
			headers:        http.Header{"X-Session-Id": {"ses_c"}, "X-Session-Affinity": {"ses_c"}},
			wantID:         "header:ses_c",
			wantAliases:    []string{"affinity:ses_c"},
			wantSource:     SourceClientNativeHeader,
			wantConfidence: ConfidenceHigh,
			wantScope:      ScopeSession,
		},
		{
			name:           "affinity header alone",
			headers:        http.Header{"X-Session-Affinity": {"ses_d"}},
			wantID:         "affinity:ses_d",
			wantSource:     SourceClientNativeHeader,
			wantConfidence: ConfidenceHigh,
			wantScope:      ScopeSession,
		},
		{
			name:           "pi responses client request id",
			headers:        http.Header{"X-Client-Request-Id": {"pi-1"}},
			wantID:         "clientreq:pi-1",
			wantSource:     SourceClientRequestID,
			wantConfidence: ConfidenceMedium,
			wantScope:      ScopeSession,
		},
		{
			name:           "body metadata session id",
			payload:        `{"metadata":{"session_id":"meta-1"}}`,
			wantID:         "session:meta-1",
			wantSource:     SourceBodySession,
			wantConfidence: ConfidenceMedium,
			wantScope:      ScopeSession,
		},
		{
			name:           "prompt cache key keeps conversation alias",
			payload:        `{"prompt_cache_key":"cache-1","conversation":{"id":"conv_1"}}`,
			wantID:         "pck:cache-1",
			wantAliases:    []string{"conv:conv_1"},
			wantSource:     SourcePromptCacheKey,
			wantConfidence: ConfidenceMedium,
			wantScope:      ScopeSession,
		},
		{
			name:           "conversation string form",
			payload:        `{"conversation":"conv_2"}`,
			wantID:         "conv:conv_2",
			wantSource:     SourceBodyConversation,
			wantConfidence: ConfidenceHigh,
			wantScope:      ScopeSession,
		},
		{
			name:           "bare metadata user id is user scope fallback",
			payload:        `{"metadata":{"user_id":"user_123"}}`,
			wantID:         "user:user_123",
			wantSource:     SourceLegacyMetadataUser,
			wantConfidence: ConfidenceLow,
			wantScope:      ScopeUser,
		},
		{
			name:           "bare metadata user id is omitted when conversation_id exists",
			payload:        `{"conversation_id":"conv-123","metadata":{"user_id":"user_123"}}`,
			wantID:         "conv:conv-123",
			wantAliases:    nil,
			wantSource:     SourceBodyConversation,
			wantConfidence: ConfidenceHigh,
			wantScope:      ScopeSession,
		},
		{
			name:           "execution metadata fallback when no root session exists",
			metadata:       map[string]any{cliproxyexecutor.ExecutionSessionMetadataKey: "exec-1"},
			wantID:         "execution:exec-1",
			wantSource:     SourceExecutionSession,
			wantConfidence: ConfidenceHigh,
			wantScope:      ScopeTransport,
		},
		{
			name:           "thread header fallback when no root session exists",
			headers:        http.Header{"Thread-Id": {"thread-1"}},
			wantID:         "thread:thread-1",
			wantSource:     SourceClientNativeHeader,
			wantConfidence: ConfidenceMedium,
			wantScope:      ScopeThread,
		},
		{
			name:           "derived identity fallback",
			metadata:       map[string]any{cliproxyexecutor.DerivedSessionIDMetadataKey: "ctx:v1:abc"},
			payload:        `{"messages":[{"role":"user","content":"hello"}]}`,
			wantID:         "derived:ctx:v1:abc",
			wantSource:     SourceDerived,
			wantConfidence: ConfidenceMedium,
			wantScope:      ScopeSession,
		},
		{
			name:           "message hash low confidence fallback",
			payload:        `{"messages":[{"role":"user","content":"hello"}]}`,
			wantID:         "msg:",
			wantSource:     SourceMessageHash,
			wantConfidence: ConfidenceLow,
			wantScope:      ScopeSession,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Extract(tt.headers, []byte(tt.payload), tt.metadata)
			if tt.wantID == "msg:" {
				if !strings.HasPrefix(got.ID, "msg:") {
					t.Fatalf("ID = %q, want msg: prefix", got.ID)
				}
			} else if got.ID != tt.wantID {
				t.Fatalf("ID = %q, want %q", got.ID, tt.wantID)
			}
			if !equalStrings(got.Aliases, tt.wantAliases) {
				t.Fatalf("Aliases = %#v, want %#v", got.Aliases, tt.wantAliases)
			}
			if got.Source != tt.wantSource {
				t.Fatalf("Source = %q, want %q", got.Source, tt.wantSource)
			}
			if got.Confidence != tt.wantConfidence {
				t.Fatalf("Confidence = %q, want %q", got.Confidence, tt.wantConfidence)
			}
			if got.Scope != tt.wantScope {
				t.Fatalf("Scope = %q, want %q", got.Scope, tt.wantScope)
			}
		})
	}
}

func TestExtractCapturesThreadsWithoutOverridingRootSession(t *testing.T) {
	headers := http.Header{
		"Session-Id":                 {"root-1"},
		"Thread-Id":                  {"thread-1"},
		"X-Codex-Parent-Thread-Id":   {"thread-parent"},
		"X-Codex-Turn-Metadata":      {`{"turn_id":"turn-1"}`},
		"X-Gateway-Parent-Thread-Id": {"gateway-parent"},
	}

	got := Extract(headers, nil, map[string]any{cliproxyexecutor.ExecutionSessionMetadataKey: "transport-1"})

	if got.ID != "codex:root-1" {
		t.Fatalf("ID = %q, want codex:root-1", got.ID)
	}
	if got.Scope != ScopeSession {
		t.Fatalf("Scope = %q, want %q", got.Scope, ScopeSession)
	}
	for _, id := range got.IDs() {
		if id == "thread:thread-1" || id == "execution:transport-1" {
			t.Fatalf("non-root identifier %q was added as a session alias", id)
		}
	}
	if got.ThreadID != "thread-1" {
		t.Fatalf("ThreadID = %q, want thread-1", got.ThreadID)
	}
	// X-Gateway-Parent-Thread-Id is listed first, so the unified protocol wins.
	if got.ParentThreadID != "gateway-parent" {
		t.Fatalf("ParentThreadID = %q, want gateway-parent", got.ParentThreadID)
	}
	if got.ClientType != ClientTypeCodex {
		t.Fatalf("ClientType = %q, want %q", got.ClientType, ClientTypeCodex)
	}
}

func TestExtractReportsThreadsWithoutAnySession(t *testing.T) {
	got := Extract(http.Header{"X-Gateway-Thread-Id": {"thread-only"}}, nil, nil)

	if got.ID != "thread:thread-only" {
		t.Fatalf("ID = %q, want thread:thread-only", got.ID)
	}
	if got.ThreadID != "thread-only" {
		t.Fatalf("ThreadID = %q, want thread-only", got.ThreadID)
	}
}

func TestExtractRejectsUnsafeExplicitIdentifiers(t *testing.T) {
	tests := []struct {
		name    string
		headers http.Header
	}{
		{name: "control character", headers: http.Header{"X-Gateway-Session-Id": {"bad\nvalue"}}},
		{name: "oversized", headers: http.Header{"X-Gateway-Session-Id": {strings.Repeat("a", 257)}}},
		{name: "blank", headers: http.Header{"X-Gateway-Session-Id": {"   "}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Extract(tt.headers, nil, nil); got.ID != "" {
				t.Fatalf("ID = %q, want empty", got.ID)
			}
		})
	}
}

func TestExtractSkipsInvalidMultiValueHeaderEntries(t *testing.T) {
	headers := http.Header{"X-Session-Id": {"bad\nvalue", "good-value"}}

	if got := Extract(headers, nil, nil); got.ID != "header:good-value" {
		t.Fatalf("ID = %q, want header:good-value", got.ID)
	}
}

func TestExtractHeaderNamesAreCaseInsensitive(t *testing.T) {
	headers := http.Header{}
	headers["x-claude-code-session-id"] = []string{"cc-lower"}

	if got := Extract(headers, nil, nil); got.ID != "claude:cc-lower" {
		t.Fatalf("ID = %q, want claude:cc-lower", got.ID)
	}
}

func TestExtractReturnsEmptyIdentityWithoutSignals(t *testing.T) {
	got := Extract(nil, nil, nil)

	if !got.IsZero() {
		t.Fatalf("Extract() = %#v, want zero identity", got)
	}
	if got.FallbackID() != "" {
		t.Fatalf("FallbackID() = %q, want empty", got.FallbackID())
	}
	if len(got.IDs()) != 0 {
		t.Fatalf("IDs() = %#v, want empty", got.IDs())
	}
}

func TestIdentityIDsDeduplication(t *testing.T) {
	identity := Identity{
		ID:      "claude:cc-1",
		Aliases: []string{"affinity:aff-1", "claude:cc-1", "conv:conv-1", "affinity:aff-1", "  "},
	}
	got := identity.IDs()
	want := []string{"claude:cc-1", "affinity:aff-1", "conv:conv-1"}
	if !equalStrings(got, want) {
		t.Fatalf("Identity.IDs() = %#v, want %#v", got, want)
	}
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func quote(value string) string {
	var builder strings.Builder
	builder.WriteByte('"')
	for _, r := range value {
		if r == '"' || r == '\\' {
			builder.WriteByte('\\')
		}
		builder.WriteRune(r)
	}
	builder.WriteByte('"')
	return builder.String()
}
