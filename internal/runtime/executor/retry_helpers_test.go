package executor

import (
	"testing"
	"time"
)

func TestParseRetryDelay_RetryInfo(t *testing.T) {
	body := []byte(`{
		"error": {
			"code": 503,
			"message": "No capacity available for model gemini-3-pro-high",
			"details": [
				{
					"@type": "type.googleapis.com/google.rpc.ErrorInfo",
					"reason": "MODEL_CAPACITY_EXHAUSTED"
				},
				{
					"@type": "type.googleapis.com/google.rpc.RetryInfo",
					"retryDelay": "37s"
				}
			]
		}
	}`)
	delay, err := parseRetryDelay(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if delay == nil {
		t.Fatal("expected delay, got nil")
	}
	if *delay != 37*time.Second {
		t.Fatalf("got %v, want %v", *delay, 37*time.Second)
	}
}

func TestParseRetryDelay_Milliseconds(t *testing.T) {
	body := []byte(`{
		"error": {
			"details": [
				{
					"@type": "type.googleapis.com/google.rpc.RetryInfo",
					"retryDelay": "0.847655010s"
				}
			]
		}
	}`)
	delay, err := parseRetryDelay(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if delay == nil {
		t.Fatal("expected delay, got nil")
	}
	expected := 847655010 * time.Nanosecond
	if *delay != expected {
		t.Fatalf("got %v, want %v", *delay, expected)
	}
}

func TestParseRetryDelay_QuotaResetDelay(t *testing.T) {
	body := []byte(`{
		"error": {
			"details": [
				{
					"@type": "type.googleapis.com/google.rpc.ErrorInfo",
					"metadata": {
						"quotaResetDelay": "373.801628ms"
					}
				}
			]
		}
	}`)
	delay, err := parseRetryDelay(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if delay == nil {
		t.Fatal("expected delay, got nil")
	}
	expected, _ := time.ParseDuration("373.801628ms")
	if *delay != expected {
		t.Fatalf("got %v, want %v", *delay, expected)
	}
}

func TestParseRetryDelay_MessageFallback(t *testing.T) {
	body := []byte(`{
		"error": {
			"message": "Your quota will reset after 10s."
		}
	}`)
	delay, err := parseRetryDelay(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if delay == nil {
		t.Fatal("expected delay, got nil")
	}
	if *delay != 10*time.Second {
		t.Fatalf("got %v, want %v", *delay, 10*time.Second)
	}
}

func TestParseRetryDelay_NoRetryInfo(t *testing.T) {
	body := []byte(`{"error": {"code": 500, "message": "Internal error"}}`)
	delay, err := parseRetryDelay(body)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if delay != nil {
		t.Fatalf("expected nil delay, got %v", *delay)
	}
}

func TestParseRetryDelay_EmptyBody(t *testing.T) {
	delay, err := parseRetryDelay([]byte{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if delay != nil {
		t.Fatalf("expected nil delay, got %v", *delay)
	}
}

func TestClampRetryDelay(t *testing.T) {
	tests := []struct {
		name     string
		input    time.Duration
		max      time.Duration
		expected time.Duration
	}{
		{"below max", 30 * time.Second, 60 * time.Second, 30 * time.Second},
		{"at max", 60 * time.Second, 60 * time.Second, 60 * time.Second},
		{"above max", 120 * time.Second, 60 * time.Second, 60 * time.Second},
		{"zero", 0, 60 * time.Second, 0},
		{"negative", -1 * time.Second, 60 * time.Second, -1 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ClampRetryDelay(tt.input, tt.max)
			if result != tt.expected {
				t.Errorf("ClampRetryDelay(%v, %v) = %v, want %v", tt.input, tt.max, result, tt.expected)
			}
		})
	}
}

func TestParseAndClampRetryDelay(t *testing.T) {
	tests := []struct {
		name     string
		body     []byte
		expected *time.Duration
	}{
		{
			name: "37s clamped to 37s",
			body: []byte(`{"error":{"details":[{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"37s"}]}}`),
			expected: func() *time.Duration {
				d := 37 * time.Second
				return &d
			}(),
		},
		{
			name: "120s clamped to 60s",
			body: []byte(`{"error":{"details":[{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"120s"}]}}`),
			expected: func() *time.Duration {
				d := 60 * time.Second
				return &d
			}(),
		},
		{
			name:     "no retry info returns nil",
			body:     []byte(`{"error":{"code":500}}`),
			expected: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ParseAndClampRetryDelay(tt.body)
			if tt.expected == nil {
				if result != nil {
					t.Errorf("expected nil, got %v", *result)
				}
				return
			}
			if result == nil {
				t.Errorf("expected %v, got nil", *tt.expected)
				return
			}
			if *result != *tt.expected {
				t.Errorf("got %v, want %v", *result, *tt.expected)
			}
		})
	}
}

func TestDefaultNoCapacityRetryDelay(t *testing.T) {
	tests := []struct {
		attempt  int
		expected time.Duration
	}{
		{0, 250 * time.Millisecond},
		{1, 500 * time.Millisecond},
		{2, 750 * time.Millisecond},
		{3, 1000 * time.Millisecond},
		{7, 2000 * time.Millisecond},
		{100, 2000 * time.Millisecond},
		{-1, 250 * time.Millisecond},
	}
	for _, tt := range tests {
		result := DefaultNoCapacityRetryDelay(tt.attempt)
		if result != tt.expected {
			t.Errorf("DefaultNoCapacityRetryDelay(%d) = %v, want %v", tt.attempt, result, tt.expected)
		}
	}
}
