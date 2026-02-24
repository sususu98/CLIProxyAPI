package management

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/runtime/geminicli"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"golang.org/x/net/proxy"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

const defaultAPICallTimeout = 60 * time.Second

const (
	geminiOAuthClientID     = "681255809395-oo8ft2oprdrnp9e3aqf6av3hmdib135j.apps.googleusercontent.com"
	geminiOAuthClientSecret = "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"
)

var geminiOAuthScopes = []string{
	"https://www.googleapis.com/auth/cloud-platform",
	"https://www.googleapis.com/auth/userinfo.email",
	"https://www.googleapis.com/auth/userinfo.profile",
}

const (
	antigravityOAuthClientID     = "1071006060591-tmhssin2h21lcre235vtolojh4g403ep.apps.googleusercontent.com"
	antigravityOAuthClientSecret = "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"
)

var antigravityOAuthTokenURL = "https://oauth2.googleapis.com/token"

type apiCallRequest struct {
	AuthIndexSnake  *string           `json:"auth_index"`
	AuthIndexCamel  *string           `json:"authIndex"`
	AuthIndexPascal *string           `json:"AuthIndex"`
	Method          string            `json:"method"`
	URL             string            `json:"url"`
	Header          map[string]string `json:"header"`
	Data            string            `json:"data"`
}

type apiCallResponse struct {
	StatusCode int                 `json:"status_code"`
	Header     map[string][]string `json:"header"`
	Body       string              `json:"body"`
}

// APICall makes a generic HTTP request on behalf of the management API caller.
// It is protected by the management middleware.
//
// Endpoint:
//
//	POST /v0/management/api-call
//
// Authentication:
//
//	Same as other management APIs (requires a management key and remote-management rules).
//	You can provide the key via:
//	- Authorization: Bearer <key>
//	- X-Management-Key: <key>
//
// Request JSON:
//   - auth_index / authIndex / AuthIndex (optional):
//     The credential "auth_index" from GET /v0/management/auth-files (or other endpoints returning it).
//     If omitted or not found, credential-specific proxy/token substitution is skipped.
//   - method (required): HTTP method, e.g. GET, POST, PUT, PATCH, DELETE.
//   - url (required): Absolute URL including scheme and host, e.g. "https://api.example.com/v1/ping".
//   - header (optional): Request headers map.
//     Supports magic variable "$TOKEN$" which is replaced using the selected credential:
//     1) metadata.access_token
//     2) attributes.api_key
//     3) metadata.token / metadata.id_token / metadata.cookie
//     Example: {"Authorization":"Bearer $TOKEN$"}.
//     Note: if you need to override the HTTP Host header, set header["Host"].
//   - data (optional): Raw request body as string (useful for POST/PUT/PATCH).
//
// Proxy selection (highest priority first):
//  1. Selected credential proxy_url
//  2. Global config proxy-url
//  3. Direct connect (environment proxies are not used)
//
// Response JSON (returned with HTTP 200 when the APICall itself succeeds):
//   - status_code: Upstream HTTP status code.
//   - header: Upstream response headers.
//   - body: Upstream response body as string.
//
// Example:
//
//	curl -sS -X POST "http://127.0.0.1:8317/v0/management/api-call" \
//	  -H "Authorization: Bearer <MANAGEMENT_KEY>" \
//	  -H "Content-Type: application/json" \
//	  -d '{"auth_index":"<AUTH_INDEX>","method":"GET","url":"https://api.example.com/v1/ping","header":{"Authorization":"Bearer $TOKEN$"}}'
//
//	curl -sS -X POST "http://127.0.0.1:8317/v0/management/api-call" \
//	  -H "Authorization: Bearer 831227" \
//	  -H "Content-Type: application/json" \
//	  -d '{"auth_index":"<AUTH_INDEX>","method":"POST","url":"https://api.example.com/v1/fetchAvailableModels","header":{"Authorization":"Bearer $TOKEN$","Content-Type":"application/json","User-Agent":"cliproxyapi"},"data":"{}"}'
func (h *Handler) APICall(c *gin.Context) {
	var body apiCallRequest
	if errBindJSON := c.ShouldBindJSON(&body); errBindJSON != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}

	method := strings.ToUpper(strings.TrimSpace(body.Method))
	if method == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing method"})
		return
	}

	urlStr := strings.TrimSpace(body.URL)
	if urlStr == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing url"})
		return
	}
	parsedURL, errParseURL := url.Parse(urlStr)
	if errParseURL != nil || parsedURL.Scheme == "" || parsedURL.Host == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid url"})
		return
	}

	authIndex := firstNonEmptyString(body.AuthIndexSnake, body.AuthIndexCamel, body.AuthIndexPascal)
	auth := h.authByIndex(authIndex)

	reqHeaders := body.Header
	if reqHeaders == nil {
		reqHeaders = map[string]string{}
	}

	var hostOverride string
	var token string
	var tokenResolved bool
	var tokenErr error
	for key, value := range reqHeaders {
		if !strings.Contains(value, "$TOKEN$") {
			continue
		}
		if !tokenResolved {
			token, tokenErr = h.resolveTokenForAuth(c.Request.Context(), auth)
			tokenResolved = true
		}
		if auth != nil && token == "" {
			if tokenErr != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "auth token refresh failed"})
				return
			}
			c.JSON(http.StatusBadRequest, gin.H{"error": "auth token not found"})
			return
		}
		if token == "" {
			continue
		}
		reqHeaders[key] = strings.ReplaceAll(value, "$TOKEN$", token)
	}

	var requestBody io.Reader
	if body.Data != "" {
		requestBody = strings.NewReader(body.Data)
	}

	req, errNewRequest := http.NewRequestWithContext(c.Request.Context(), method, urlStr, requestBody)
	if errNewRequest != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "failed to build request"})
		return
	}

	for key, value := range reqHeaders {
		if strings.EqualFold(key, "host") {
			hostOverride = strings.TrimSpace(value)
			continue
		}
		req.Header.Set(key, value)
	}
	if hostOverride != "" {
		req.Host = hostOverride
	}

	httpClient := &http.Client{
		Timeout: defaultAPICallTimeout,
	}
	httpClient.Transport = h.apiCallTransport(auth)

	resp, errDo := httpClient.Do(req)
	if errDo != nil {
		log.WithError(errDo).Debug("management APICall request failed")
		c.JSON(http.StatusBadGateway, gin.H{"error": "request failed"})
		return
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("response body close error: %v", errClose)
		}
	}()

	respBody, errReadAll := io.ReadAll(resp.Body)
	if errReadAll != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "failed to read response"})
		return
	}

	c.JSON(http.StatusOK, apiCallResponse{
		StatusCode: resp.StatusCode,
		Header:     resp.Header,
		Body:       string(respBody),
	})
}

func firstNonEmptyString(values ...*string) string {
	for _, v := range values {
		if v == nil {
			continue
		}
		if out := strings.TrimSpace(*v); out != "" {
			return out
		}
	}
	return ""
}

func tokenValueForAuth(auth *coreauth.Auth) string {
	if auth == nil {
		return ""
	}
	if v := tokenValueFromMetadata(auth.Metadata); v != "" {
		return v
	}
	if auth.Attributes != nil {
		if v := strings.TrimSpace(auth.Attributes["api_key"]); v != "" {
			return v
		}
	}
	if shared := geminicli.ResolveSharedCredential(auth.Runtime); shared != nil {
		if v := tokenValueFromMetadata(shared.MetadataSnapshot()); v != "" {
			return v
		}
	}
	return ""
}

func (h *Handler) resolveTokenForAuth(ctx context.Context, auth *coreauth.Auth) (string, error) {
	if auth == nil {
		return "", nil
	}

	provider := strings.ToLower(strings.TrimSpace(auth.Provider))
	if provider == "gemini-cli" {
		token, errToken := h.refreshGeminiOAuthAccessToken(ctx, auth)
		return token, errToken
	}
	if provider == "antigravity" {
		token, errToken := h.refreshAntigravityOAuthAccessToken(ctx, auth)
		return token, errToken
	}

	return tokenValueForAuth(auth), nil
}

func (h *Handler) refreshGeminiOAuthAccessToken(ctx context.Context, auth *coreauth.Auth) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if auth == nil {
		return "", nil
	}

	metadata, updater := geminiOAuthMetadata(auth)
	if len(metadata) == 0 {
		return "", fmt.Errorf("gemini oauth metadata missing")
	}

	base := make(map[string]any)
	if tokenRaw, ok := metadata["token"].(map[string]any); ok && tokenRaw != nil {
		base = cloneMap(tokenRaw)
	}

	var token oauth2.Token
	if len(base) > 0 {
		if raw, errMarshal := json.Marshal(base); errMarshal == nil {
			_ = json.Unmarshal(raw, &token)
		}
	}

	if token.AccessToken == "" {
		token.AccessToken = stringValue(metadata, "access_token")
	}
	if token.RefreshToken == "" {
		token.RefreshToken = stringValue(metadata, "refresh_token")
	}
	if token.TokenType == "" {
		token.TokenType = stringValue(metadata, "token_type")
	}
	if token.Expiry.IsZero() {
		if expiry := stringValue(metadata, "expiry"); expiry != "" {
			if ts, errParseTime := time.Parse(time.RFC3339, expiry); errParseTime == nil {
				token.Expiry = ts
			}
		}
	}

	conf := &oauth2.Config{
		ClientID:     geminiOAuthClientID,
		ClientSecret: geminiOAuthClientSecret,
		Scopes:       geminiOAuthScopes,
		Endpoint:     google.Endpoint,
	}

	ctxToken := ctx
	httpClient := &http.Client{
		Timeout:   defaultAPICallTimeout,
		Transport: h.apiCallTransport(auth),
	}
	ctxToken = context.WithValue(ctxToken, oauth2.HTTPClient, httpClient)

	src := conf.TokenSource(ctxToken, &token)
	currentToken, errToken := src.Token()
	if errToken != nil {
		return "", errToken
	}

	merged := buildOAuthTokenMap(base, currentToken)
	fields := buildOAuthTokenFields(currentToken, merged)
	if updater != nil {
		updater(fields)
	}
	return strings.TrimSpace(currentToken.AccessToken), nil
}

func (h *Handler) refreshAntigravityOAuthAccessToken(ctx context.Context, auth *coreauth.Auth) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if auth == nil {
		return "", nil
	}

	metadata := auth.Metadata
	if len(metadata) == 0 {
		return "", fmt.Errorf("antigravity oauth metadata missing")
	}

	current := strings.TrimSpace(tokenValueFromMetadata(metadata))
	if current != "" && !antigravityTokenNeedsRefresh(metadata) {
		return current, nil
	}

	refreshToken := stringValue(metadata, "refresh_token")
	if refreshToken == "" {
		return "", fmt.Errorf("antigravity refresh token missing")
	}

	tokenURL := strings.TrimSpace(antigravityOAuthTokenURL)
	if tokenURL == "" {
		tokenURL = "https://oauth2.googleapis.com/token"
	}
	form := url.Values{}
	form.Set("client_id", antigravityOAuthClientID)
	form.Set("client_secret", antigravityOAuthClientSecret)
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refreshToken)

	req, errReq := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if errReq != nil {
		return "", errReq
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	httpClient := &http.Client{
		Timeout:   defaultAPICallTimeout,
		Transport: h.apiCallTransport(auth),
	}
	resp, errDo := httpClient.Do(req)
	if errDo != nil {
		return "", errDo
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("response body close error: %v", errClose)
		}
	}()

	bodyBytes, errRead := io.ReadAll(resp.Body)
	if errRead != nil {
		return "", errRead
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return "", fmt.Errorf("antigravity oauth token refresh failed: status %d: %s", resp.StatusCode, strings.TrimSpace(string(bodyBytes)))
	}

	var tokenResp struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
		TokenType    string `json:"token_type"`
	}
	if errUnmarshal := json.Unmarshal(bodyBytes, &tokenResp); errUnmarshal != nil {
		return "", errUnmarshal
	}

	if strings.TrimSpace(tokenResp.AccessToken) == "" {
		return "", fmt.Errorf("antigravity oauth token refresh returned empty access_token")
	}

	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	now := time.Now()
	auth.Metadata["access_token"] = strings.TrimSpace(tokenResp.AccessToken)
	if strings.TrimSpace(tokenResp.RefreshToken) != "" {
		auth.Metadata["refresh_token"] = strings.TrimSpace(tokenResp.RefreshToken)
	}
	if tokenResp.ExpiresIn > 0 {
		auth.Metadata["expires_in"] = tokenResp.ExpiresIn
		auth.Metadata["timestamp"] = now.UnixMilli()
		auth.Metadata["expired"] = now.Add(time.Duration(tokenResp.ExpiresIn) * time.Second).Format(time.RFC3339)
	}
	auth.Metadata["type"] = "antigravity"

	if h != nil && h.authManager != nil {
		auth.LastRefreshedAt = now
		auth.UpdatedAt = now
		_, _ = h.authManager.Update(ctx, auth)
	}

	applyAntigravityUserSettings(ctx, auth, strings.TrimSpace(tokenResp.AccessToken), h.apiCallTransport(auth))

	return strings.TrimSpace(tokenResp.AccessToken), nil
}

func antigravityTokenNeedsRefresh(metadata map[string]any) bool {
	// Refresh a bit early to avoid requests racing token expiry.
	const skew = 30 * time.Second

	if metadata == nil {
		return true
	}
	if expStr, ok := metadata["expired"].(string); ok {
		if ts, errParse := time.Parse(time.RFC3339, strings.TrimSpace(expStr)); errParse == nil {
			return !ts.After(time.Now().Add(skew))
		}
	}
	expiresIn := int64Value(metadata["expires_in"])
	timestampMs := int64Value(metadata["timestamp"])
	if expiresIn > 0 && timestampMs > 0 {
		exp := time.UnixMilli(timestampMs).Add(time.Duration(expiresIn) * time.Second)
		return !exp.After(time.Now().Add(skew))
	}
	return true
}

func applyAntigravityUserSettings(ctx context.Context, auth *coreauth.Auth, accessToken string, transport http.RoundTripper) {
	if strings.TrimSpace(accessToken) == "" {
		return
	}

	baseURL := "https://daily-cloudcode-pa.googleapis.com"
	if auth != nil && auth.Metadata != nil {
		if v, ok := auth.Metadata["base_url"].(string); ok && strings.TrimSpace(v) != "" {
			baseURL = strings.TrimSuffix(strings.TrimSpace(v), "/")
		}
	}

	settingsURL := baseURL + "/v1internal:setUserSettings"
	body := `{"user_settings":{"telemetry_enabled":false,"user_data_collection_force_disabled":true,"marketing_emails_enabled":false}}`

	req, errReq := http.NewRequestWithContext(ctx, http.MethodPost, settingsURL, strings.NewReader(body))
	if errReq != nil {
		log.Warnf("antigravity management: build setUserSettings request failed: %v", errReq)
		return
	}
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "antigravity/1.107.0 darwin/arm64 google-api-nodejs-client/10.3.0")
	req.Header.Set("X-Goog-Api-Client", "gl-node/22.21.1")
	req.Header.Set("Connection", "keep-alive")

	httpClient := &http.Client{Timeout: defaultAPICallTimeout, Transport: transport}
	resp, errDo := httpClient.Do(req)
	if errDo != nil {
		log.Warnf("antigravity management: setUserSettings request failed: %v", errDo)
		return
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.ReadAll(resp.Body)

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		log.Warnf("antigravity management: setUserSettings returned HTTP %d", resp.StatusCode)
		return
	}

	label := ""
	if auth != nil && auth.Metadata != nil {
		if email, ok := auth.Metadata["email"].(string); ok {
			label = email
		}
	}
	if label == "" && auth != nil {
		label = auth.Label
	}
	log.Infof("antigravity management: setUserSettings applied for %s", label)
}

func int64Value(raw any) int64 {
	switch typed := raw.(type) {
	case int:
		return int64(typed)
	case int32:
		return int64(typed)
	case int64:
		return typed
	case uint:
		return int64(typed)
	case uint32:
		return int64(typed)
	case uint64:
		if typed > uint64(^uint64(0)>>1) {
			return 0
		}
		return int64(typed)
	case float32:
		return int64(typed)
	case float64:
		return int64(typed)
	case json.Number:
		if i, errParse := typed.Int64(); errParse == nil {
			return i
		}
	case string:
		if s := strings.TrimSpace(typed); s != "" {
			if i, errParse := json.Number(s).Int64(); errParse == nil {
				return i
			}
		}
	}
	return 0
}

func geminiOAuthMetadata(auth *coreauth.Auth) (map[string]any, func(map[string]any)) {
	if auth == nil {
		return nil, nil
	}
	if shared := geminicli.ResolveSharedCredential(auth.Runtime); shared != nil {
		snapshot := shared.MetadataSnapshot()
		return snapshot, func(fields map[string]any) { shared.MergeMetadata(fields) }
	}
	return auth.Metadata, func(fields map[string]any) {
		if auth.Metadata == nil {
			auth.Metadata = make(map[string]any)
		}
		for k, v := range fields {
			auth.Metadata[k] = v
		}
	}
}

func stringValue(metadata map[string]any, key string) string {
	if len(metadata) == 0 || key == "" {
		return ""
	}
	if v, ok := metadata[key].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

func cloneMap(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func buildOAuthTokenMap(base map[string]any, tok *oauth2.Token) map[string]any {
	merged := cloneMap(base)
	if merged == nil {
		merged = make(map[string]any)
	}
	if tok == nil {
		return merged
	}
	if raw, errMarshal := json.Marshal(tok); errMarshal == nil {
		var tokenMap map[string]any
		if errUnmarshal := json.Unmarshal(raw, &tokenMap); errUnmarshal == nil {
			for k, v := range tokenMap {
				merged[k] = v
			}
		}
	}
	return merged
}

func buildOAuthTokenFields(tok *oauth2.Token, merged map[string]any) map[string]any {
	fields := make(map[string]any, 5)
	if tok != nil && tok.AccessToken != "" {
		fields["access_token"] = tok.AccessToken
	}
	if tok != nil && tok.TokenType != "" {
		fields["token_type"] = tok.TokenType
	}
	if tok != nil && tok.RefreshToken != "" {
		fields["refresh_token"] = tok.RefreshToken
	}
	if tok != nil && !tok.Expiry.IsZero() {
		fields["expiry"] = tok.Expiry.Format(time.RFC3339)
	}
	if len(merged) > 0 {
		fields["token"] = cloneMap(merged)
	}
	return fields
}

func tokenValueFromMetadata(metadata map[string]any) string {
	if len(metadata) == 0 {
		return ""
	}
	if v, ok := metadata["accessToken"].(string); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	if v, ok := metadata["access_token"].(string); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	if tokenRaw, ok := metadata["token"]; ok && tokenRaw != nil {
		switch typed := tokenRaw.(type) {
		case string:
			if v := strings.TrimSpace(typed); v != "" {
				return v
			}
		case map[string]any:
			if v, ok := typed["access_token"].(string); ok && strings.TrimSpace(v) != "" {
				return strings.TrimSpace(v)
			}
			if v, ok := typed["accessToken"].(string); ok && strings.TrimSpace(v) != "" {
				return strings.TrimSpace(v)
			}
		case map[string]string:
			if v := strings.TrimSpace(typed["access_token"]); v != "" {
				return v
			}
			if v := strings.TrimSpace(typed["accessToken"]); v != "" {
				return v
			}
		}
	}
	if v, ok := metadata["token"].(string); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	if v, ok := metadata["id_token"].(string); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	if v, ok := metadata["cookie"].(string); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return ""
}

func (h *Handler) authByIndex(authIndex string) *coreauth.Auth {
	authIndex = strings.TrimSpace(authIndex)
	if authIndex == "" || h == nil || h.authManager == nil {
		return nil
	}
	auths := h.authManager.List()
	for _, auth := range auths {
		if auth == nil {
			continue
		}
		auth.EnsureIndex()
		if auth.Index == authIndex {
			return auth
		}
	}
	return nil
}

func (h *Handler) apiCallTransport(auth *coreauth.Auth) http.RoundTripper {
	var proxyCandidates []string
	if auth != nil {
		if proxyStr := strings.TrimSpace(auth.ProxyURL); proxyStr != "" {
			proxyCandidates = append(proxyCandidates, proxyStr)
		}
	}
	if h != nil && h.cfg != nil {
		if proxyStr := strings.TrimSpace(h.cfg.ProxyURL); proxyStr != "" {
			proxyCandidates = append(proxyCandidates, proxyStr)
		}
	}

	for _, proxyStr := range proxyCandidates {
		if transport := buildProxyTransport(proxyStr); transport != nil {
			return transport
		}
	}

	transport, ok := http.DefaultTransport.(*http.Transport)
	if !ok || transport == nil {
		return &http.Transport{Proxy: nil}
	}
	clone := transport.Clone()
	clone.Proxy = nil
	return clone
}

// authCheckRequest is the request body for POST /v0/management/auth-check.
type authCheckRequest struct {
	AuthIndexSnake  *string `json:"auth_index"`
	AuthIndexCamel  *string `json:"authIndex"`
	AuthIndexPascal *string `json:"AuthIndex"`
}

// authCheckResponse is the response for POST /v0/management/auth-check.
type authCheckResponse struct {
	Status         string `json:"status"`
	StatusCode     int    `json:"status_code"`
	Message        string `json:"message,omitempty"`
	AuthIndex      string `json:"auth_index,omitempty"`
	Provider       string `json:"provider,omitempty"`
	Label          string `json:"label,omitempty"`
	Email          string `json:"email,omitempty"`
	ValidationURL  string `json:"validation_url,omitempty"`
	UpstreamBody   string `json:"upstream_body,omitempty"`
	TokenRefreshed bool   `json:"token_refreshed"`
}

// AuthCheck performs a health check on a specific OAuth account by sending a fully
// disguised streamGenerateContent request to the upstream API, mimicking real
// Antigravity client behavior.
//
// Endpoint:
//
//	POST /v0/management/auth-check
//
// Authentication:
//
//	Same as other management APIs (requires a management key and remote-management rules).
//
// Request JSON:
//   - auth_index / authIndex / AuthIndex (required):
//     The credential "auth_index" from GET /v0/management/auth-files.
//
// Response JSON:
//   - status: "ok" | "validation_required" | "token_error" | "rate_limited" | "forbidden" | "no_capacity" | "unavailable" | "error"
//   - status_code: Upstream HTTP status code (0 if request failed before reaching upstream).
//   - message: Human-readable status description.
//   - auth_index: The auth index that was checked.
//   - provider: The auth provider (e.g., "antigravity").
//   - label: The auth label.
//   - email: The auth email if available.
//   - validation_url: The Google account verification URL if status is "validation_required".
//   - upstream_body: Raw upstream response body (truncated for large responses).
//   - token_refreshed: Whether the access token was refreshed during the check.
//
// Example:
//
//	curl -sS -X POST "http://127.0.0.1:8317/v0/management/auth-check" \
//	  -H "Authorization: Bearer <MANAGEMENT_KEY>" \
//	  -H "Content-Type: application/json" \
//	  -d '{"auth_index":"<AUTH_INDEX>"}'
func (h *Handler) AuthCheck(c *gin.Context) {
	var body authCheckRequest
	if errBind := c.ShouldBindJSON(&body); errBind != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}

	authIndex := firstNonEmptyString(body.AuthIndexSnake, body.AuthIndexCamel, body.AuthIndexPascal)
	if authIndex == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "auth_index is required"})
		return
	}

	auth := h.authByIndex(authIndex)
	if auth == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "auth not found", "auth_index": authIndex})
		return
	}

	provider := strings.ToLower(strings.TrimSpace(auth.Provider))
	if provider != "antigravity" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "auth-check currently only supports antigravity provider", "provider": provider})
		return
	}

	result := h.performAntigravityAuthCheck(c.Request.Context(), auth, authIndex)
	c.JSON(http.StatusOK, result)
}

// performAntigravityAuthCheck refreshes the token and sends a minimal disguised
// streamGenerateContent request to verify account health.
func (h *Handler) performAntigravityAuthCheck(ctx context.Context, auth *coreauth.Auth, authIndex string) authCheckResponse {
	resp := authCheckResponse{
		AuthIndex: authIndex,
		Provider:  strings.TrimSpace(auth.Provider),
		Label:     auth.Label,
	}
	if auth.Metadata != nil {
		if email, ok := auth.Metadata["email"].(string); ok {
			resp.Email = email
		}
	}

	tokenBefore := strings.TrimSpace(tokenValueFromMetadata(auth.Metadata))
	token, errToken := h.refreshAntigravityOAuthAccessToken(ctx, auth)
	if errToken != nil {
		resp.Status = "token_error"
		resp.Message = fmt.Sprintf("token refresh failed: %v", errToken)
		return resp
	}
	if strings.TrimSpace(token) == "" {
		resp.Status = "token_error"
		resp.Message = "token refresh returned empty access_token"
		return resp
	}
	resp.TokenRefreshed = token != tokenBefore

	baseURLs := authCheckBaseURLs(auth)

	userAgent := "antigravity/1.18.4 darwin/arm64"
	if auth.Attributes != nil {
		if ua := strings.TrimSpace(auth.Attributes["user_agent"]); ua != "" {
			userAgent = ua
		}
	}
	if auth.Metadata != nil {
		if ua, ok := auth.Metadata["user_agent"].(string); ok && strings.TrimSpace(ua) != "" {
			userAgent = strings.TrimSpace(ua)
		}
	}

	projectID := ""
	if auth.Metadata != nil {
		if pid, ok := auth.Metadata["project_id"].(string); ok {
			projectID = strings.TrimSpace(pid)
		}
	}

	payload, errPayload := buildAuthCheckPayload(projectID)
	if errPayload != nil {
		resp.Status = "error"
		resp.Message = fmt.Sprintf("failed to build payload: %v", errPayload)
		return resp
	}

	httpClient := &http.Client{
		Timeout:   defaultAPICallTimeout,
		Transport: h.apiCallTransport(auth),
	}

	for _, baseURL := range baseURLs {
		requestURL := baseURL + "/v1internal:streamGenerateContent?alt=sse"

		httpReq, errReq := http.NewRequestWithContext(ctx, http.MethodPost, requestURL, strings.NewReader(string(payload)))
		if errReq != nil {
			resp.Status = "error"
			resp.Message = fmt.Sprintf("failed to build request: %v", errReq)
			return resp
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Authorization", "Bearer "+token)
		httpReq.Header.Set("User-Agent", userAgent)
		httpReq.Header.Set("Accept", "text/event-stream")

		httpResp, errDo := httpClient.Do(httpReq)
		if errDo != nil {
			resp.Status = "error"
			resp.Message = fmt.Sprintf("request failed: %v", errDo)
			continue
		}

		bodyReader := io.LimitReader(httpResp.Body, 8192)
		bodyBytes, errRead := io.ReadAll(bodyReader)
		_ = httpResp.Body.Close()
		if errRead != nil {
			resp.Status = "error"
			resp.StatusCode = httpResp.StatusCode
			resp.Message = fmt.Sprintf("failed to read response: %v", errRead)
			return resp
		}

		resp.StatusCode = httpResp.StatusCode
		resp.Status, resp.Message, resp.ValidationURL = classifyAuthCheckResult(httpResp.StatusCode, bodyBytes)
		if resp.Status != "ok" {
			resp.UpstreamBody = string(bodyBytes)
		}
		return resp
	}

	return resp
}

func authCheckBaseURLs(auth *coreauth.Auth) []string {
	if auth != nil {
		if auth.Attributes != nil {
			if v := strings.TrimSpace(auth.Attributes["base_url"]); v != "" {
				return []string{strings.TrimSuffix(v, "/")}
			}
		}
		if auth.Metadata != nil {
			if v, ok := auth.Metadata["base_url"].(string); ok && strings.TrimSpace(v) != "" {
				return []string{strings.TrimSuffix(strings.TrimSpace(v), "/")}
			}
		}
	}
	return []string{
		"https://daily-cloudcode-pa.googleapis.com",
		"https://daily-cloudcode-pa.sandbox.googleapis.com",
	}
}

func buildAuthCheckPayload(projectID string) ([]byte, error) {
	if projectID == "" {
		projectID = "probe-" + uuid.NewString()[:8]
	}

	type part struct {
		Text string `json:"text"`
	}
	type content struct {
		Role  string `json:"role"`
		Parts []part `json:"parts"`
	}
	type genConfig struct {
		Temperature    float64 `json:"temperature"`
		CandidateCount int     `json:"candidateCount"`
	}
	type request struct {
		Contents          []content `json:"contents"`
		SystemInstruction content   `json:"systemInstruction"`
		GenerationConfig  genConfig `json:"generationConfig"`
		SessionID         string    `json:"sessionId"`
	}
	type envelope struct {
		Model       string  `json:"model"`
		UserAgent   string  `json:"userAgent"`
		RequestType string  `json:"requestType"`
		Project     string  `json:"project"`
		RequestID   string  `json:"requestId"`
		Request     request `json:"request"`
	}

	payload := envelope{
		Model:       "gemini-3-flash",
		UserAgent:   "antigravity",
		RequestType: "agent",
		Project:     projectID,
		RequestID:   "agent-" + uuid.NewString(),
		Request: request{
			Contents: []content{
				{Role: "user", Parts: []part{{Text: "hi"}}},
			},
			SystemInstruction: content{
				Role:  "user",
				Parts: []part{{Text: "Reply with only the word OK."}},
			},
			GenerationConfig: genConfig{
				Temperature:    0,
				CandidateCount: 1,
			},
			SessionID: "-1234567890",
		},
	}

	return json.Marshal(payload)
}

// classifyAuthCheckResult determines the auth status from the upstream response.
func classifyAuthCheckResult(statusCode int, body []byte) (status, message, validationURL string) {
	bodyStr := string(body)

	if statusCode >= 200 && statusCode < 300 {
		return "ok", "account is healthy", ""
	}

	if statusCode == 403 {
		if strings.Contains(bodyStr, "VALIDATION_REQUIRED") {
			valURL := gjson.GetBytes(body, "error.details.#(metadata.validation_url).metadata.validation_url").String()
			return "validation_required", "account requires Google verification", valURL
		}
		return "forbidden", fmt.Sprintf("upstream returned 403: %s", summarizeBody(bodyStr)), ""
	}

	if statusCode == 401 {
		return "token_error", "upstream rejected token (401 Unauthorized)", ""
	}

	if statusCode == 429 {
		return "rate_limited", "account is rate limited (429)", ""
	}

	if statusCode == 503 {
		if strings.Contains(strings.ToLower(bodyStr), "no capacity") {
			return "no_capacity", "no capacity available (503)", ""
		}
		return "unavailable", fmt.Sprintf("service unavailable (503): %s", summarizeBody(bodyStr)), ""
	}

	if statusCode >= 500 {
		return "unavailable", fmt.Sprintf("upstream error (%d): %s", statusCode, summarizeBody(bodyStr)), ""
	}

	return "error", fmt.Sprintf("upstream returned HTTP %d: %s", statusCode, summarizeBody(bodyStr)), ""
}

// summarizeBody returns a truncated version of the body for error messages.
func summarizeBody(body string) string {
	body = strings.TrimSpace(body)
	if len(body) > 200 {
		return body[:200] + "..."
	}
	return body
}

func buildProxyTransport(proxyStr string) *http.Transport {
	proxyStr = strings.TrimSpace(proxyStr)
	if proxyStr == "" {
		return nil
	}

	proxyURL, errParse := url.Parse(proxyStr)
	if errParse != nil {
		log.WithError(errParse).Debug("parse proxy URL failed")
		return nil
	}
	if proxyURL.Scheme == "" || proxyURL.Host == "" {
		log.Debug("proxy URL missing scheme/host")
		return nil
	}

	if proxyURL.Scheme == "socks5" {
		var proxyAuth *proxy.Auth
		if proxyURL.User != nil {
			username := proxyURL.User.Username()
			password, _ := proxyURL.User.Password()
			proxyAuth = &proxy.Auth{User: username, Password: password}
		}
		dialer, errSOCKS5 := proxy.SOCKS5("tcp", proxyURL.Host, proxyAuth, proxy.Direct)
		if errSOCKS5 != nil {
			log.WithError(errSOCKS5).Debug("create SOCKS5 dialer failed")
			return nil
		}
		return &http.Transport{
			Proxy: nil,
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return dialer.Dial(network, addr)
			},
		}
	}

	if proxyURL.Scheme == "http" || proxyURL.Scheme == "https" {
		return &http.Transport{Proxy: http.ProxyURL(proxyURL)}
	}

	log.Debugf("unsupported proxy scheme: %s", proxyURL.Scheme)
	return nil
}
