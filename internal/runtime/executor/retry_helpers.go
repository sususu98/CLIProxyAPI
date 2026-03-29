// Package executor provides runtime execution capabilities for various AI service providers.
// This file provides shared retry delay parsing and clamping utilities.
package executor

import (
	"fmt"
	"regexp"
	"strconv"
	"time"

	"github.com/tidwall/gjson"
)

// MaxRetryDelay is the maximum delay allowed when using server-suggested retry times.
// This prevents excessively long waits that could block requests indefinitely.
const MaxRetryDelay = 60 * time.Second

// parseRetryDelay extracts the retry delay from a Google API error response.
// The error response contains a RetryInfo.retryDelay field in the format "0.847655010s".
// Returns the parsed duration or an error if it cannot be determined.
//
// It attempts parsing in the following order:
// 1. RetryInfo.retryDelay (e.g., "37s", "0.847655010s")
// 2. ErrorInfo.metadata.quotaResetDelay (e.g., "373.801628ms")
// 3. error.message regex match (e.g., "Your quota will reset after 10s.")
func parseRetryDelay(errorBody []byte) (*time.Duration, error) {
	// Try to parse the retryDelay from the error response
	// Format: error.details[].retryDelay where @type == "type.googleapis.com/google.rpc.RetryInfo"
	details := gjson.GetBytes(errorBody, "error.details")
	if details.Exists() && details.IsArray() {
		for _, detail := range details.Array() {
			typeVal := detail.Get("@type").String()
			if typeVal == "type.googleapis.com/google.rpc.RetryInfo" {
				retryDelay := detail.Get("retryDelay").String()
				if retryDelay != "" {
					// Parse duration string like "0.847655010s"
					duration, err := time.ParseDuration(retryDelay)
					if err != nil {
						return nil, fmt.Errorf("failed to parse duration")
					}
					return &duration, nil
				}
			}
		}

		// Fallback: try ErrorInfo.metadata.quotaResetDelay (e.g., "373.801628ms")
		for _, detail := range details.Array() {
			typeVal := detail.Get("@type").String()
			if typeVal == "type.googleapis.com/google.rpc.ErrorInfo" {
				quotaResetDelay := detail.Get("metadata.quotaResetDelay").String()
				if quotaResetDelay != "" {
					duration, err := time.ParseDuration(quotaResetDelay)
					if err == nil {
						return &duration, nil
					}
				}
			}
		}
	}

	// Fallback: parse from error.message "Your quota will reset after Xs."
	message := gjson.GetBytes(errorBody, "error.message").String()
	if message != "" {
		re := regexp.MustCompile(`after\s+(\d+)s\.?`)
		if matches := re.FindStringSubmatch(message); len(matches) > 1 {
			seconds, err := strconv.Atoi(matches[1])
			if err == nil {
				duration := time.Duration(seconds) * time.Second
				return &duration, nil
			}
		}
	}

	return nil, fmt.Errorf("no RetryInfo found")
}

// ClampRetryDelay ensures the given duration does not exceed the specified maximum.
// If d exceeds max, max is returned. If d is negative or zero, it returns d unchanged.
func ClampRetryDelay(d time.Duration, max time.Duration) time.Duration {
	if d > max {
		return max
	}
	return d
}

// ParseAndClampRetryDelay parses the retry delay from an error response body
// and clamps it to MaxRetryDelay. Returns nil if no retry delay could be parsed.
func ParseAndClampRetryDelay(errorBody []byte) *time.Duration {
	delay, err := parseRetryDelay(errorBody)
	if err != nil || delay == nil {
		return nil
	}
	clamped := ClampRetryDelay(*delay, MaxRetryDelay)
	return &clamped
}

// DefaultNoCapacityRetryDelay returns a default delay for no-capacity errors
// based on the attempt number. Uses linear backoff: 250ms, 500ms, 750ms, ...
// up to a maximum of 2 seconds.
func DefaultNoCapacityRetryDelay(attempt int) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	delay := time.Duration(attempt+1) * 250 * time.Millisecond
	if delay > 2*time.Second {
		delay = 2 * time.Second
	}
	return delay
}
