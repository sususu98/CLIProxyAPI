package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

func TestFillFirstSelectorPick_Deterministic(t *testing.T) {
	t.Parallel()

	selector := &FillFirstSelector{}
	auths := []*Auth{
		{ID: "b"},
		{ID: "a"},
		{ID: "c"},
	}

	got, err := selector.Pick(context.Background(), "gemini", "", cliproxyexecutor.Options{}, auths)
	if err != nil {
		t.Fatalf("Pick() error = %v", err)
	}
	if got == nil {
		t.Fatalf("Pick() auth = nil")
	}
	if got.ID != "a" {
		t.Fatalf("Pick() auth.ID = %q, want %q", got.ID, "a")
	}
}

func TestRoundRobinSelectorPick_CyclesDeterministic(t *testing.T) {
	t.Parallel()

	selector := &RoundRobinSelector{}
	auths := []*Auth{
		{ID: "b"},
		{ID: "a"},
		{ID: "c"},
	}

	want := []string{"a", "b", "c", "a", "b"}
	for i, id := range want {
		got, err := selector.Pick(context.Background(), "gemini", "", cliproxyexecutor.Options{}, auths)
		if err != nil {
			t.Fatalf("Pick() #%d error = %v", i, err)
		}
		if got == nil {
			t.Fatalf("Pick() #%d auth = nil", i)
		}
		if got.ID != id {
			t.Fatalf("Pick() #%d auth.ID = %q, want %q", i, got.ID, id)
		}
	}
}

func TestRoundRobinSelectorPick_Concurrent(t *testing.T) {
	selector := &RoundRobinSelector{}
	auths := []*Auth{
		{ID: "b"},
		{ID: "a"},
		{ID: "c"},
	}

	start := make(chan struct{})
	var wg sync.WaitGroup
	errCh := make(chan error, 1)

	goroutines := 32
	iterations := 100
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for j := 0; j < iterations; j++ {
				got, err := selector.Pick(context.Background(), "gemini", "", cliproxyexecutor.Options{}, auths)
				if err != nil {
					select {
					case errCh <- err:
					default:
					}
					return
				}
				if got == nil {
					select {
					case errCh <- errors.New("Pick() returned nil auth"):
					default:
					}
					return
				}
				if got.ID == "" {
					select {
					case errCh <- errors.New("Pick() returned auth with empty ID"):
					default:
					}
					return
				}
			}
		}()
	}

	close(start)
	wg.Wait()

	select {
	case err := <-errCh:
		t.Fatalf("concurrent Pick() error = %v", err)
	default:
	}
}

func TestExtractSessionID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		payload string
		want    string
	}{
		{
			name:    "valid_claude_code_format",
			payload: `{"metadata":{"user_id":"user_3f221fe75652cf9a89a31647f16274bb8036a9b85ac4dc226a4df0efec8dc04d_account__session_ac980658-63bd-4fb3-97ba-8da64cb1e344"}}`,
			want:    "claude:ac980658-63bd-4fb3-97ba-8da64cb1e344",
		},
		{
			name:    "no_session_but_user_id",
			payload: `{"metadata":{"user_id":"user_abc123"}}`,
			want:    "user:user_abc123",
		},
		{
			name:    "conversation_id",
			payload: `{"conversation_id":"conv-12345"}`,
			want:    "conv:conv-12345",
		},
		{
			name:    "no_metadata",
			payload: `{"model":"claude-3"}`,
			want:    "",
		},
		{
			name:    "empty_payload",
			payload: ``,
			want:    "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractSessionID([]byte(tt.payload))
			if got != tt.want {
				t.Errorf("extractSessionID() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSessionAffinitySelector_SameSessionSameAuth(t *testing.T) {
	t.Parallel()

	fallback := &RoundRobinSelector{}
	selector := NewSessionAffinitySelector(fallback)

	auths := []*Auth{
		{ID: "auth-a"},
		{ID: "auth-b"},
		{ID: "auth-c"},
	}

	// Use valid UUID format for session ID
	payload := []byte(`{"metadata":{"user_id":"user_xxx_account__session_ac980658-63bd-4fb3-97ba-8da64cb1e344"}}`)
	opts := cliproxyexecutor.Options{OriginalRequest: payload}

	// Same session should always pick the same auth
	first, err := selector.Pick(context.Background(), "claude", "claude-3", opts, auths)
	if err != nil {
		t.Fatalf("Pick() error = %v", err)
	}
	if first == nil {
		t.Fatalf("Pick() returned nil")
	}

	// Verify consistency: same session, same auths -> same result
	for i := 0; i < 10; i++ {
		got, err := selector.Pick(context.Background(), "claude", "claude-3", opts, auths)
		if err != nil {
			t.Fatalf("Pick() #%d error = %v", i, err)
		}
		if got.ID != first.ID {
			t.Fatalf("Pick() #%d auth.ID = %q, want %q (same session should pick same auth)", i, got.ID, first.ID)
		}
	}
}

func TestSessionAffinitySelector_NoSessionFallback(t *testing.T) {
	t.Parallel()

	fallback := &FillFirstSelector{}
	selector := NewSessionAffinitySelector(fallback)

	auths := []*Auth{
		{ID: "auth-b"},
		{ID: "auth-a"},
		{ID: "auth-c"},
	}

	// No session in payload, should fallback to FillFirstSelector (picks "auth-a" after sorting)
	payload := []byte(`{"model":"claude-3"}`)
	opts := cliproxyexecutor.Options{OriginalRequest: payload}

	got, err := selector.Pick(context.Background(), "claude", "claude-3", opts, auths)
	if err != nil {
		t.Fatalf("Pick() error = %v", err)
	}
	if got.ID != "auth-a" {
		t.Fatalf("Pick() auth.ID = %q, want %q (should fallback to FillFirst)", got.ID, "auth-a")
	}
}

func TestSessionAffinitySelector_DifferentSessionsDifferentAuths(t *testing.T) {
	t.Parallel()

	fallback := &RoundRobinSelector{}
	selector := NewSessionAffinitySelector(fallback)

	auths := []*Auth{
		{ID: "auth-a"},
		{ID: "auth-b"},
		{ID: "auth-c"},
	}

	// Use valid UUID format for session IDs
	session1 := []byte(`{"metadata":{"user_id":"user_xxx_account__session_11111111-1111-1111-1111-111111111111"}}`)
	session2 := []byte(`{"metadata":{"user_id":"user_xxx_account__session_22222222-2222-2222-2222-222222222222"}}`)

	opts1 := cliproxyexecutor.Options{OriginalRequest: session1}
	opts2 := cliproxyexecutor.Options{OriginalRequest: session2}

	auth1, _ := selector.Pick(context.Background(), "claude", "claude-3", opts1, auths)
	auth2, _ := selector.Pick(context.Background(), "claude", "claude-3", opts2, auths)

	// Different sessions may or may not pick different auths (depends on hash collision)
	// But each session should be consistent
	for i := 0; i < 5; i++ {
		got1, _ := selector.Pick(context.Background(), "claude", "claude-3", opts1, auths)
		got2, _ := selector.Pick(context.Background(), "claude", "claude-3", opts2, auths)
		if got1.ID != auth1.ID {
			t.Fatalf("session1 Pick() #%d inconsistent: got %q, want %q", i, got1.ID, auth1.ID)
		}
		if got2.ID != auth2.ID {
			t.Fatalf("session2 Pick() #%d inconsistent: got %q, want %q", i, got2.ID, auth2.ID)
		}
	}
}

func TestSessionAffinitySelector_FailoverWhenAuthUnavailable(t *testing.T) {
	t.Parallel()

	fallback := &RoundRobinSelector{}
	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: fallback,
		TTL:      time.Minute,
	})
	defer selector.Stop()

	auths := []*Auth{
		{ID: "auth-a"},
		{ID: "auth-b"},
		{ID: "auth-c"},
	}

	payload := []byte(`{"metadata":{"user_id":"user_xxx_account__session_failover-test-uuid"}}`)
	opts := cliproxyexecutor.Options{OriginalRequest: payload}

	// First pick establishes binding
	first, err := selector.Pick(context.Background(), "claude", "claude-3", opts, auths)
	if err != nil {
		t.Fatalf("Pick() error = %v", err)
	}

	// Remove the bound auth from available list (simulating rate limit)
	availableWithoutFirst := make([]*Auth, 0, len(auths)-1)
	for _, a := range auths {
		if a.ID != first.ID {
			availableWithoutFirst = append(availableWithoutFirst, a)
		}
	}

	// With failover enabled, should pick a new auth
	second, err := selector.Pick(context.Background(), "claude", "claude-3", opts, availableWithoutFirst)
	if err != nil {
		t.Fatalf("Pick() after failover error = %v", err)
	}
	if second.ID == first.ID {
		t.Fatalf("Pick() after failover returned same auth %q, expected different", first.ID)
	}

	// Subsequent picks should consistently return the new binding
	for i := 0; i < 5; i++ {
		got, _ := selector.Pick(context.Background(), "claude", "claude-3", opts, availableWithoutFirst)
		if got.ID != second.ID {
			t.Fatalf("Pick() #%d after failover inconsistent: got %q, want %q", i, got.ID, second.ID)
		}
	}
}

func TestExtractSessionID_ClaudeCodePriorityOverHeader(t *testing.T) {
	t.Parallel()

	// Claude Code metadata.user_id should have highest priority, even when X-Session-ID header is present
	headers := make(http.Header)
	headers.Set("X-Session-ID", "header-session-id")

	payload := []byte(`{"metadata":{"user_id":"user_xxx_account__session_ac980658-63bd-4fb3-97ba-8da64cb1e344"}}`)

	got := ExtractSessionID(headers, payload, nil)
	want := "claude:ac980658-63bd-4fb3-97ba-8da64cb1e344"
	if got != want {
		t.Errorf("ExtractSessionID() = %q, want %q (Claude Code should have highest priority over header)", got, want)
	}
}

func TestExtractSessionID_ClaudeCodePriorityOverIdempotencyKey(t *testing.T) {
	t.Parallel()

	// Claude Code metadata.user_id should have highest priority, even when idempotency_key is present
	metadata := map[string]any{"idempotency_key": "idem-12345"}
	payload := []byte(`{"metadata":{"user_id":"user_xxx_account__session_ac980658-63bd-4fb3-97ba-8da64cb1e344"}}`)

	got := ExtractSessionID(nil, payload, metadata)
	want := "claude:ac980658-63bd-4fb3-97ba-8da64cb1e344"
	if got != want {
		t.Errorf("ExtractSessionID() = %q, want %q (Claude Code should have highest priority over idempotency_key)", got, want)
	}
}

func TestExtractSessionID_Headers(t *testing.T) {
	t.Parallel()

	headers := make(http.Header)
	headers.Set("X-Session-ID", "my-explicit-session")

	got := ExtractSessionID(headers, nil, nil)
	want := "header:my-explicit-session"
	if got != want {
		t.Errorf("ExtractSessionID() with header = %q, want %q", got, want)
	}
}

func TestExtractSessionID_IdempotencyKey(t *testing.T) {
	t.Parallel()

	metadata := map[string]any{"idempotency_key": "idem-12345"}

	got := ExtractSessionID(nil, nil, metadata)
	want := "idem:idem-12345"
	if got != want {
		t.Errorf("ExtractSessionID() with idempotency_key = %q, want %q", got, want)
	}
}

func TestExtractSessionID_MessageHashFallback(t *testing.T) {
	t.Parallel()

	payload := []byte(`{"messages":[{"role":"user","content":"Hello world"}]}`)

	got := ExtractSessionID(nil, payload, nil)
	if got == "" {
		t.Error("ExtractSessionID() with messages should return hash-based session ID")
	}
	if !strings.HasPrefix(got, "msg:") {
		t.Errorf("ExtractSessionID() = %q, want prefix 'msg:'", got)
	}

	// Same messages should produce same hash
	got2 := ExtractSessionID(nil, payload, nil)
	if got != got2 {
		t.Errorf("ExtractSessionID() not stable: got %q then %q", got, got2)
	}
}

func TestSessionAffinitySelector_MultiModelSession(t *testing.T) {
	t.Parallel()

	fallback := &RoundRobinSelector{}
	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: fallback,
		TTL:      time.Minute,
	})
	defer selector.Stop()

	// auth-a supports only model-a, auth-b supports only model-b
	authA := &Auth{ID: "auth-a"}
	authB := &Auth{ID: "auth-b"}

	// Same session ID for all requests
	payload := []byte(`{"metadata":{"user_id":"user_xxx_account__session_multi-model-test"}}`)
	opts := cliproxyexecutor.Options{OriginalRequest: payload}

	// Request model-a with only auth-a available for that model
	authsForModelA := []*Auth{authA}
	pickedA, err := selector.Pick(context.Background(), "provider", "model-a", opts, authsForModelA)
	if err != nil {
		t.Fatalf("Pick() for model-a error = %v", err)
	}
	if pickedA.ID != "auth-a" {
		t.Fatalf("Pick() for model-a = %q, want auth-a", pickedA.ID)
	}

	// Request model-b with only auth-b available for that model
	authsForModelB := []*Auth{authB}
	pickedB, err := selector.Pick(context.Background(), "provider", "model-b", opts, authsForModelB)
	if err != nil {
		t.Fatalf("Pick() for model-b error = %v", err)
	}
	if pickedB.ID != "auth-b" {
		t.Fatalf("Pick() for model-b = %q, want auth-b", pickedB.ID)
	}

	// Switch back to model-a - should still get auth-a (separate binding per model)
	pickedA2, err := selector.Pick(context.Background(), "provider", "model-a", opts, authsForModelA)
	if err != nil {
		t.Fatalf("Pick() for model-a (2nd) error = %v", err)
	}
	if pickedA2.ID != "auth-a" {
		t.Fatalf("Pick() for model-a (2nd) = %q, want auth-a", pickedA2.ID)
	}

	// Verify bindings are stable for multiple calls
	for i := 0; i < 5; i++ {
		gotA, _ := selector.Pick(context.Background(), "provider", "model-a", opts, authsForModelA)
		gotB, _ := selector.Pick(context.Background(), "provider", "model-b", opts, authsForModelB)
		if gotA.ID != "auth-a" {
			t.Fatalf("Pick() #%d for model-a = %q, want auth-a", i, gotA.ID)
		}
		if gotB.ID != "auth-b" {
			t.Fatalf("Pick() #%d for model-b = %q, want auth-b", i, gotB.ID)
		}
	}
}

func TestExtractSessionID_MultimodalContent(t *testing.T) {
	t.Parallel()

	// Test array content format (multimodal)
	payload := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"Hello world"},{"type":"image","source":{"data":"..."}}]}]}`)

	got := ExtractSessionID(nil, payload, nil)
	if got == "" {
		t.Error("ExtractSessionID() with multimodal messages should return hash-based session ID")
	}
	if !strings.HasPrefix(got, "msg:") {
		t.Errorf("ExtractSessionID() = %q, want prefix 'msg:'", got)
	}

	// Same multimodal content should produce same hash
	got2 := ExtractSessionID(nil, payload, nil)
	if got != got2 {
		t.Errorf("ExtractSessionID() not stable: got %q then %q", got, got2)
	}

	// Different text content should produce different hash
	payload2 := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"Different content"}]}]}`)
	got3 := ExtractSessionID(nil, payload2, nil)
	if got == got3 {
		t.Errorf("ExtractSessionID() should produce different hash for different content")
	}
}

func TestSessionAffinitySelector_CrossProviderIsolation(t *testing.T) {
	t.Parallel()

	fallback := &RoundRobinSelector{}
	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: fallback,
		TTL:      time.Minute,
	})
	defer selector.Stop()

	authClaude := &Auth{ID: "auth-claude"}
	authGemini := &Auth{ID: "auth-gemini"}

	// Same session ID for both providers
	payload := []byte(`{"metadata":{"user_id":"user_xxx_account__session_cross-provider-test"}}`)
	opts := cliproxyexecutor.Options{OriginalRequest: payload}

	// Request via claude provider
	pickedClaude, err := selector.Pick(context.Background(), "claude", "claude-3", opts, []*Auth{authClaude})
	if err != nil {
		t.Fatalf("Pick() for claude error = %v", err)
	}
	if pickedClaude.ID != "auth-claude" {
		t.Fatalf("Pick() for claude = %q, want auth-claude", pickedClaude.ID)
	}

	// Same session but via gemini provider should get different auth
	pickedGemini, err := selector.Pick(context.Background(), "gemini", "gemini-2.5-pro", opts, []*Auth{authGemini})
	if err != nil {
		t.Fatalf("Pick() for gemini error = %v", err)
	}
	if pickedGemini.ID != "auth-gemini" {
		t.Fatalf("Pick() for gemini = %q, want auth-gemini", pickedGemini.ID)
	}

	// Verify both bindings remain stable
	for i := 0; i < 5; i++ {
		gotC, _ := selector.Pick(context.Background(), "claude", "claude-3", opts, []*Auth{authClaude})
		gotG, _ := selector.Pick(context.Background(), "gemini", "gemini-2.5-pro", opts, []*Auth{authGemini})
		if gotC.ID != "auth-claude" {
			t.Fatalf("Pick() #%d for claude = %q, want auth-claude", i, gotC.ID)
		}
		if gotG.ID != "auth-gemini" {
			t.Fatalf("Pick() #%d for gemini = %q, want auth-gemini", i, gotG.ID)
		}
	}
}

func TestSessionCache_GetAndRefresh(t *testing.T) {
	t.Parallel()

	cache := NewSessionCache(100 * time.Millisecond)
	defer cache.Stop()

	cache.Set("session1", "auth1")

	// Verify initial value
	got, ok := cache.GetAndRefresh("session1")
	if !ok || got != "auth1" {
		t.Fatalf("GetAndRefresh() = %q, %v, want auth1, true", got, ok)
	}

	// Wait half TTL and access again (should refresh)
	time.Sleep(60 * time.Millisecond)
	got, ok = cache.GetAndRefresh("session1")
	if !ok || got != "auth1" {
		t.Fatalf("GetAndRefresh() after 60ms = %q, %v, want auth1, true", got, ok)
	}

	// Wait another 60ms (total 120ms from original, but TTL refreshed at 60ms)
	// Entry should still be valid because TTL was refreshed
	time.Sleep(60 * time.Millisecond)
	got, ok = cache.GetAndRefresh("session1")
	if !ok || got != "auth1" {
		t.Fatalf("GetAndRefresh() after refresh = %q, %v, want auth1, true (TTL should have been refreshed)", got, ok)
	}

	// Now wait full TTL without access
	time.Sleep(110 * time.Millisecond)
	got, ok = cache.GetAndRefresh("session1")
	if ok {
		t.Fatalf("GetAndRefresh() after expiry = %q, %v, want '', false", got, ok)
	}
}

func TestSessionAffinitySelector_Concurrent(t *testing.T) {
	t.Parallel()

	fallback := &RoundRobinSelector{}
	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: fallback,
		TTL:      time.Minute,
	})
	defer selector.Stop()

	auths := []*Auth{
		{ID: "auth-a"},
		{ID: "auth-b"},
		{ID: "auth-c"},
	}

	payload := []byte(`{"metadata":{"user_id":"user_xxx_account__session_concurrent-test"}}`)
	opts := cliproxyexecutor.Options{OriginalRequest: payload}

	// First pick to establish binding
	first, err := selector.Pick(context.Background(), "claude", "claude-3", opts, auths)
	if err != nil {
		t.Fatalf("Initial Pick() error = %v", err)
	}
	expectedID := first.ID

	start := make(chan struct{})
	var wg sync.WaitGroup
	errCh := make(chan error, 1)

	goroutines := 32
	iterations := 50
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for j := 0; j < iterations; j++ {
				got, err := selector.Pick(context.Background(), "claude", "claude-3", opts, auths)
				if err != nil {
					select {
					case errCh <- err:
					default:
					}
					return
				}
				if got.ID != expectedID {
					select {
					case errCh <- fmt.Errorf("concurrent Pick() returned %q, want %q", got.ID, expectedID):
					default:
					}
					return
				}
			}
		}()
	}

	close(start)
	wg.Wait()

	select {
	case err := <-errCh:
		t.Fatalf("concurrent Pick() error = %v", err)
	default:
	}
}
