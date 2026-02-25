package auth

import (
	"bytes"
	"net/http"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

type testStatusErr struct {
	code int
	msg  string
}

func (e *testStatusErr) Error() string   { return e.msg }
func (e *testStatusErr) StatusCode() int { return e.code }

type simpleErr struct {
	msg string
}

func (e *simpleErr) Error() string { return e.msg }

func TestIsCapacityExhaustedError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{"nil error", nil, false},
		{"503 capacity", &testStatusErr{http.StatusServiceUnavailable, "model capacity exhausted"}, true},
		{"503 unavailable", &testStatusErr{http.StatusServiceUnavailable, "service unavailable"}, true},
		{"503 internal server error", &testStatusErr{http.StatusServiceUnavailable, "internal server error"}, false},
		{"429 error", &testStatusErr{http.StatusTooManyRequests, "too many requests"}, false},
		{"400 error", &testStatusErr{http.StatusBadRequest, "bad request"}, false},
		{"200 success", &testStatusErr{http.StatusOK, "ok"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isCapacityExhaustedError(tt.err); got != tt.expected {
				t.Errorf("isCapacityExhaustedError() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestIsModelUnavailableError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{"nil error", nil, false},
		{"429 any message", &testStatusErr{http.StatusTooManyRequests, "rate limited"}, true},
		{"503 capacity", &testStatusErr{http.StatusServiceUnavailable, "capacity exceeded"}, true},
		{"503 Model capacity exhausted", &testStatusErr{http.StatusServiceUnavailable, "Model capacity exhausted"}, true},
		{"503 unavailable", &testStatusErr{http.StatusServiceUnavailable, "service unavailable"}, true},
		{"503 internal error", &testStatusErr{http.StatusServiceUnavailable, "internal error"}, false},
		{"400 error", &testStatusErr{http.StatusBadRequest, "bad request"}, false},
		{"plain error no StatusCode", &simpleErr{"some error"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isModelUnavailableError(tt.err); got != tt.expected {
				t.Errorf("isModelUnavailableError() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestFallbackController_Lifecycle(t *testing.T) {
	t.Run("no fallback configured", func(t *testing.T) {
		fc := newFallbackController("model-a")
		fc.Capture(OAuthModelAliasResult{FallbackModel: ""})

		if fc.Available() {
			t.Error("Available() = true, want false")
		}
		if fc.ShouldFallback(&testStatusErr{http.StatusServiceUnavailable, "capacity"}) {
			t.Error("ShouldFallback() = true, want false")
		}
		if fc.ShouldFallback(&testStatusErr{http.StatusTooManyRequests, "rate limited"}) {
			t.Error("ShouldFallback(429) = true, want false")
		}
	})

	t.Run("503 capacity triggers fallback after exhaustion", func(t *testing.T) {
		fc := newFallbackController("model-a")
		fc.Capture(OAuthModelAliasResult{FallbackModel: "model-b"})

		if !fc.Available() {
			t.Error("Available() = false, want true")
		}

		// ShouldFallback with 503 capacity error
		if !fc.ShouldFallback(&testStatusErr{http.StatusServiceUnavailable, "MODEL_CAPACITY_EXHAUSTED"}) {
			t.Error("ShouldFallback(503 capacity) = false, want true")
		}

		// Activate
		fc.Activate("test")
		if !fc.Used() {
			t.Error("Used() = false, want true")
		}

		// ApplyModel
		req := cliproxyexecutor.Request{Model: "model-a"}
		fc.ApplyModel(&req)
		if req.Model != "model-b" {
			t.Errorf("req.Model = %s, want model-b", req.Model)
		}
	})

	t.Run("429 triggers fallback after exhaustion", func(t *testing.T) {
		fc := newFallbackController("model-a")
		fc.Capture(OAuthModelAliasResult{FallbackModel: "model-b"})

		if !fc.ShouldFallback(&testStatusErr{http.StatusTooManyRequests, "rate limited"}) {
			t.Error("ShouldFallback(429) = false, want true")
		}
	})

	t.Run("non-capacity 503 does not trigger fallback", func(t *testing.T) {
		fc := newFallbackController("model-a")
		fc.Capture(OAuthModelAliasResult{FallbackModel: "model-b"})

		if fc.ShouldFallback(&testStatusErr{http.StatusServiceUnavailable, "internal error"}) {
			t.Error("ShouldFallback(503 internal) = true, want false")
		}
	})

	t.Run("400 does not trigger fallback", func(t *testing.T) {
		fc := newFallbackController("model-a")
		fc.Capture(OAuthModelAliasResult{FallbackModel: "model-b"})

		if fc.ShouldFallback(&testStatusErr{http.StatusBadRequest, "bad request"}) {
			t.Error("ShouldFallback(400) = true, want false")
		}
	})

	t.Run("fallback only fires once", func(t *testing.T) {
		fc := newFallbackController("model-a")
		fc.Capture(OAuthModelAliasResult{FallbackModel: "model-b"})
		fc.Activate("test")

		if fc.ShouldFallback(&testStatusErr{http.StatusServiceUnavailable, "capacity"}) {
			t.Error("ShouldFallback() after activation = true, want false")
		}
		if fc.ShouldFallback(&testStatusErr{http.StatusTooManyRequests, "rate limited"}) {
			t.Error("ShouldFallback(429) after activation = true, want false")
		}
	})

	t.Run("capture only records first fallback", func(t *testing.T) {
		fc := newFallbackController("model-a")
		fc.Capture(OAuthModelAliasResult{FallbackModel: "model-b"})
		fc.Capture(OAuthModelAliasResult{FallbackModel: "model-c"})
		fc.Activate("test")

		req := cliproxyexecutor.Request{Model: "model-a"}
		fc.ApplyModel(&req)
		if req.Model != "model-b" {
			t.Errorf("req.Model = %s, want model-b (first capture)", req.Model)
		}
	})

	t.Run("ApplyModel is no-op when not activated", func(t *testing.T) {
		fc := newFallbackController("model-a")
		fc.Capture(OAuthModelAliasResult{FallbackModel: "model-b"})

		req := cliproxyexecutor.Request{Model: "model-a"}
		fc.ApplyModel(&req)
		if req.Model != "model-a" {
			t.Errorf("req.Model = %s, want model-a (unchanged)", req.Model)
		}
	})

	t.Run("PostProcessResponse uses fallback alias", func(t *testing.T) {
		fc := newFallbackController("model-a")
		fc.Capture(OAuthModelAliasResult{
			FallbackModel: "model-b",
			ForceMapping:  true,
			OriginalAlias: "my-alias",
		})
		fc.Activate("test")

		resp := cliproxyexecutor.Response{Payload: []byte(`{"model":"model-b"}`)}
		fc.PostProcessResponse(&resp, OAuthModelAliasResult{})

		if !bytes.Contains(resp.Payload, []byte(`"model":"my-alias"`)) {
			t.Errorf("resp.Payload = %s, want model rewritten to my-alias", string(resp.Payload))
		}
	})

	t.Run("EffectiveAlias returns fallback alias when used", func(t *testing.T) {
		fc := newFallbackController("model-a")
		fallbackAlias := OAuthModelAliasResult{FallbackModel: "model-b", ForceMapping: true}
		currentAlias := OAuthModelAliasResult{ForceMapping: false}
		fc.Capture(fallbackAlias)
		fc.Activate("test")

		effective := fc.EffectiveAlias(currentAlias)
		if !effective.ForceMapping {
			t.Error("effective.ForceMapping = false, want true (from fallback)")
		}
	})

	t.Run("EffectiveAlias returns current alias when not used", func(t *testing.T) {
		fc := newFallbackController("model-a")
		fc.Capture(OAuthModelAliasResult{FallbackModel: "model-b", ForceMapping: true})
		currentAlias := OAuthModelAliasResult{ForceMapping: false}

		effective := fc.EffectiveAlias(currentAlias)
		if effective.ForceMapping {
			t.Error("effective.ForceMapping = true, want false (from current)")
		}
	})
}
