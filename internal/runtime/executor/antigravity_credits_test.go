package executor

import (
	"sync"
	"testing"

	"github.com/tidwall/gjson"
)

func TestCreditsClassifyAntigravity429(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		body     string
		expected antigravity429Category
	}{
		{"empty body", "", antigravity429Unknown},
		{"quota_exhausted keyword", `{"error":{"status":"RESOURCE_EXHAUSTED","message":"quota_exhausted"}}`, antigravity429QuotaExhausted},
		{"quota exhausted with space", `{"error":{"message":"Quota exhausted for this model"}}`, antigravity429QuotaExhausted},
		{"rate limited with RetryInfo", `{"error":{"details":[{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"30s"}]}}`, antigravity429RateLimited},
		{"unknown 429 body", `{"error":{"message":"some other error"}}`, antigravity429Unknown},
		{"case insensitive", `{"error":{"message":"QUOTA_EXHAUSTED"}}`, antigravity429QuotaExhausted},
		{"retry info case insensitive", `{"error":{"details":[{"@type":"TYPE.GOOGLEAPIS.COM/GOOGLE.RPC.RETRYINFO","retryDelay":"5s"}]}}`, antigravity429RateLimited},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := classifyAntigravity429([]byte(tt.body))
			if result != tt.expected {
				t.Errorf("classifyAntigravity429(%q) = %q, want %q", tt.body, result, tt.expected)
			}
		})
	}
}

func TestCreditsInjectEnabledCreditTypes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   string
		wantNil bool
	}{
		{"valid JSON", `{"contents":[]}`, false},
		{"invalid JSON", `not json`, false},
		{"empty JSON object", `{}`, false},
		{"existing field", `{"enabledCreditTypes":["OTHER"]}`, false},
		{"nested request body", `{"request":{"contents":[]}}`, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := injectEnabledCreditTypes([]byte(tt.input))
			if tt.wantNil {
				if result != nil {
					t.Fatalf("injectEnabledCreditTypes(%q) = %s, want nil", tt.input, string(result))
				}
				return
			}

			if result == nil {
				t.Fatalf("injectEnabledCreditTypes(%q) returned nil", tt.input)
			}

			assertCreditTypesInjected(t, result)

			if gjson.GetBytes(result, "enabledCreditTypes.0").String() != "GOOGLE_ONE_AI" {
				t.Fatalf("enabledCreditTypes[0] = %q, want %q", gjson.GetBytes(result, "enabledCreditTypes.0").String(), "GOOGLE_ONE_AI")
			}

			if gjson.GetBytes(result, "enabledCreditTypes.1").Exists() {
				t.Fatalf("enabledCreditTypes should contain exactly one entry, got %s", string(result))
			}
		})
	}
}

func TestCreditsShouldMarkCreditsExhausted(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		statusCode int
		body       string
		expected   bool
	}{
		{"insufficient credits", 429, `insufficient credits`, true},
		{"resource exhausted", 429, `Resource has been exhausted`, false},
		{"google_one_ai keyword", 429, `google_one_ai`, true},
		{"server error 500", 500, `insufficient credits`, false},
		{"server error 503", 503, `insufficient credits`, false},
		{"timeout 408", 408, `insufficient credits`, false},
		{"normal error", 400, `bad request`, false},
		{"empty body", 429, ``, false},
		{"minimum credit keyword", 403, `minimum credit amount for usage`, true},
		{"non 429/403 status with keyword", 400, `insufficient credits`, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := shouldMarkCreditsExhausted(tt.statusCode, []byte(tt.body))
			if result != tt.expected {
				t.Errorf("shouldMarkCreditsExhausted(%d, %q) = %v, want %v", tt.statusCode, tt.body, result, tt.expected)
			}
		})
	}
}

func TestCreditsExhaustionTracker(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		exercise func(*creditsExhaustionTracker) bool
		expected bool
	}{
		{
			name: "not exhausted initially",
			exercise: func(tracker *creditsExhaustionTracker) bool {
				return tracker.isExhausted("auth1")
			},
			expected: false,
		},
		{
			name: "mark then check",
			exercise: func(tracker *creditsExhaustionTracker) bool {
				tracker.markExhausted("auth1")
				return tracker.isExhausted("auth1")
			},
			expected: true,
		},
		{
			name: "clear before expiry",
			exercise: func(tracker *creditsExhaustionTracker) bool {
				tracker.markExhausted("auth1")
				tracker.clearExhausted("auth1")
				return tracker.isExhausted("auth1")
			},
			expected: false,
		},
		{
			name: "different auth IDs isolated",
			exercise: func(tracker *creditsExhaustionTracker) bool {
				tracker.markExhausted("auth1")
				return tracker.isExhausted("auth2")
			},
			expected: false,
		},
		{
			name: "blank auth ID ignored",
			exercise: func(tracker *creditsExhaustionTracker) bool {
				tracker.markExhausted("   ")
				return tracker.isExhausted("   ")
			},
			expected: false,
		},
		{
			name: "clear missing auth ID remains false",
			exercise: func(tracker *creditsExhaustionTracker) bool {
				tracker.clearExhausted("missing")
				return tracker.isExhausted("missing")
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tracker := &creditsExhaustionTracker{}
			got := tt.exercise(tracker)

			if got != tt.expected {
				t.Fatalf("exercise() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestCreditsExhaustionTracker_Concurrent(t *testing.T) {
	t.Parallel()

	tracker := &creditsExhaustionTracker{}
	authIDs := []string{"auth0", "auth1", "auth2", "auth3"}

	var wg sync.WaitGroup
	for i := range 10 {
		wg.Go(func() {
			for j := range 100 {
				authID := authIDs[(i+j)%len(authIDs)]
				tracker.markExhausted(authID)
				_ = tracker.isExhausted(authID)
				if (i+j)%3 == 0 {
					tracker.clearExhausted(authID)
				}
			}
		})
	}
	wg.Wait()
}

func assertCreditTypesInjected(t *testing.T, payload []byte) {
	t.Helper()

	if !gjson.ValidBytes(payload) {
		t.Fatalf("result is not valid JSON: %s", string(payload))
	}

	creditTypes := gjson.GetBytes(payload, "enabledCreditTypes")
	if !creditTypes.Exists() {
		t.Fatalf("enabledCreditTypes missing in %s", string(payload))
	}
	if !creditTypes.IsArray() {
		t.Fatalf("enabledCreditTypes should be an array in %s", string(payload))
	}
}

func TestCreditsExhaustionTracker_NilReceiver(t *testing.T) {
	t.Parallel()

	var tracker *creditsExhaustionTracker

	tracker.markExhausted("auth1")
	tracker.clearExhausted("auth1")

	if tracker.isExhausted("auth1") {
		t.Fatalf("nil tracker should never report exhausted")
	}
}

func TestCreditsTrackerInvalidStoredValue(t *testing.T) {
	t.Parallel()

	tracker := &creditsExhaustionTracker{}
	tracker.entries.Store("auth1", "not-a-time")

	if tracker.isExhausted("auth1") {
		t.Fatalf("invalid stored value should not be treated as exhausted")
	}

	if _, ok := tracker.entries.Load("auth1"); ok {
		t.Fatalf("invalid stored value should be deleted")
	}
}
