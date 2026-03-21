package executor

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const creditsExhaustedDuration = 5 * time.Hour

type antigravity429Category string

const (
	antigravity429Unknown        antigravity429Category = "unknown"
	antigravity429RateLimited    antigravity429Category = "rate_limited"
	antigravity429QuotaExhausted antigravity429Category = "quota_exhausted"
)

var antigravityQuotaExhaustedKeywords = []string{
	"quota_exhausted", "quota exhausted",
}

var creditsExhaustedKeywords = []string{
	"google_one_ai", "insufficient credit", "insufficient credits",
	"not enough credit", "not enough credits",
	"credit exhausted", "credits exhausted",
	"minimumcreditamountforusage", "minimum credit amount for usage",
}

func classifyAntigravity429(body []byte) antigravity429Category {
	if len(body) == 0 {
		return antigravity429Unknown
	}

	bodyLower := strings.ToLower(string(body))
	for _, keyword := range antigravityQuotaExhaustedKeywords {
		if strings.Contains(bodyLower, keyword) {
			return antigravity429QuotaExhausted
		}
	}

	for _, detail := range gjson.GetBytes(body, "error.details").Array() {
		if strings.Contains(strings.ToLower(detail.Get("@type").String()), "retryinfo") {
			return antigravity429RateLimited
		}
	}

	return antigravity429Unknown
}

func injectEnabledCreditTypes(payload []byte) []byte {
	updated, err := sjson.SetBytes(payload, "enabledCreditTypes", []string{"GOOGLE_ONE_AI"})
	if err != nil {
		return nil
	}
	return updated
}

func shouldMarkCreditsExhausted(statusCode int, body []byte) bool {
	if statusCode >= http.StatusInternalServerError || statusCode == http.StatusRequestTimeout {
		return false
	}
	if statusCode != http.StatusTooManyRequests && statusCode != http.StatusForbidden {
		return false
	}

	bodyLower := strings.ToLower(string(body))
	for _, keyword := range creditsExhaustedKeywords {
		if strings.Contains(bodyLower, keyword) {
			return true
		}
	}

	return false
}

type creditsExhaustionTracker struct {
	entries sync.Map
}

var globalCreditsTracker = &creditsExhaustionTracker{}

func (t *creditsExhaustionTracker) isExhausted(authID string) bool {
	if t == nil || strings.TrimSpace(authID) == "" {
		return false
	}

	value, ok := t.entries.Load(authID)
	if !ok {
		return false
	}

	expiry, ok := value.(time.Time)
	if !ok {
		t.entries.Delete(authID)
		return false
	}
	if time.Now().After(expiry) {
		t.entries.Delete(authID)
		return false
	}

	return true
}

func (t *creditsExhaustionTracker) markExhausted(authID string) {
	if t == nil || strings.TrimSpace(authID) == "" {
		return
	}
	t.entries.Store(authID, time.Now().Add(creditsExhaustedDuration))
}

func (t *creditsExhaustionTracker) clearExhausted(authID string) {
	if t == nil || strings.TrimSpace(authID) == "" {
		return
	}
	t.entries.Delete(authID)
}

func (e *AntigravityExecutor) attemptCreditsRetry(
	ctx context.Context,
	auth *cliproxyauth.Auth,
	token string,
	baseModel string,
	translated []byte,
	stream bool,
	alt string,
	baseURL string,
	errorBody []byte,
) (*http.Response, error) {
	if e == nil || e.cfg == nil || e.cfg.AntigravityAICreditsEnabled == nil || !*e.cfg.AntigravityAICreditsEnabled {
		return nil, nil
	}
	if classifyAntigravity429(errorBody) != antigravity429QuotaExhausted {
		return nil, nil
	}
	if auth == nil {
		return nil, nil
	}

	authID := auth.ID
	if globalCreditsTracker.isExhausted(authID) {
		log.Debugf("antigravity executor: skipping AI Credits retry for exhausted auth %s", authID)
		return nil, nil
	}

	creditsPayload := injectEnabledCreditTypes(translated)
	if creditsPayload == nil {
		log.Warnf("antigravity executor: failed to inject enabledCreditTypes for auth %s", authID)
		return nil, nil
	}

	httpReq, err := e.buildRequest(ctx, auth, token, baseModel, creditsPayload, stream, alt, baseURL)
	if err != nil {
		return nil, err
	}

	log.Debugf("antigravity executor: retrying with AI Credits for auth %s", authID)
	httpClient := newProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		return nil, err
	}

	if httpResp.StatusCode >= http.StatusOK && httpResp.StatusCode < http.StatusMultipleChoices {
		globalCreditsTracker.clearExhausted(authID)
		log.Infof("antigravity executor: AI Credits retry succeeded for auth %s", authID)
		return httpResp, nil
	}

	bodyBytes, errRead := io.ReadAll(httpResp.Body)
	if errClose := httpResp.Body.Close(); errClose != nil {
		log.Errorf("antigravity executor: close response body error: %v", errClose)
	}
	if errRead != nil {
		return nil, errRead
	}

	if shouldMarkCreditsExhausted(httpResp.StatusCode, bodyBytes) {
		globalCreditsTracker.markExhausted(authID)
		log.Warnf("antigravity executor: marked AI Credits exhausted for auth %s after HTTP %d", authID, httpResp.StatusCode)
	} else {
		log.Debugf("antigravity executor: AI Credits retry failed for auth %s with HTTP %d", authID, httpResp.StatusCode)
	}

	return nil, statusErr{code: httpResp.StatusCode, msg: string(bodyBytes)}
}
