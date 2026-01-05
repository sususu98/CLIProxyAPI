package auth

import (
	"context"
	"errors"
	"sync"
	"testing"

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
			want:    "ac980658-63bd-4fb3-97ba-8da64cb1e344",
		},
		{
			name:    "no_session",
			payload: `{"metadata":{"user_id":"user_abc123"}}`,
			want:    "",
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
