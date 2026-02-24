// Package executor provides runtime execution capabilities for various AI service providers.
// This file implements the Antigravity executor that proxies requests to the antigravity
// upstream using OAuth credentials.
package executor

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v6/sdk/auth"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	antigravityBaseURLDaily        = "https://daily-cloudcode-pa.googleapis.com"
	antigravitySandboxBaseURLDaily = "https://daily-cloudcode-pa.sandbox.googleapis.com"
	antigravityBaseURLProd         = "https://cloudcode-pa.googleapis.com"
	antigravityCountTokensPath     = "/v1internal:countTokens"
	antigravityStreamPath          = "/v1internal:streamGenerateContent"
	antigravityGeneratePath        = "/v1internal:generateContent"
	antigravityModelsPath          = "/v1internal:fetchAvailableModels"
	antigravityClientID            = "1071006060591-tmhssin2h21lcre235vtolojh4g403ep.apps.googleusercontent.com"
	antigravityClientSecret        = "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"
	defaultAntigravityAgent        = "antigravity/1.18.4 darwin/arm64"
	antigravityAuthType            = "antigravity"
	refreshSkew                    = 3000 * time.Second
	systemInstruction              = "You are Antigravity, a powerful agentic AI coding assistant designed by the Google Deepmind team working on Advanced Agentic Coding.You are pair programming with a USER to solve their coding task. The task may require creating a new codebase, modifying or debugging an existing codebase, or simply answering a question.**Absolute paths only****Proactiveness**"

	// webSearchGeminiModel is the Gemini model used for web search functionality
	webSearchGeminiModel = "gemini-2.5-flash"

	antigravitySetUserSettingsPath = "/v1internal:setUserSettings"
)

var (
	randSource      = rand.New(rand.NewSource(time.Now().UnixNano()))
	randSourceMutex sync.Mutex
	// antigravityPrimaryModelsCache keeps the latest non-empty model list fetched
	// from any antigravity auth. Empty fetches never overwrite this cache.
	antigravityPrimaryModelsCache struct {
		mu     sync.RWMutex
		models []*registry.ModelInfo
	}
)

func cloneAntigravityModels(models []*registry.ModelInfo) []*registry.ModelInfo {
	if len(models) == 0 {
		return nil
	}
	out := make([]*registry.ModelInfo, 0, len(models))
	for _, model := range models {
		if model == nil || strings.TrimSpace(model.ID) == "" {
			continue
		}
		out = append(out, cloneAntigravityModelInfo(model))
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func cloneAntigravityModelInfo(model *registry.ModelInfo) *registry.ModelInfo {
	if model == nil {
		return nil
	}
	clone := *model
	if len(model.SupportedGenerationMethods) > 0 {
		clone.SupportedGenerationMethods = append([]string(nil), model.SupportedGenerationMethods...)
	}
	if len(model.SupportedParameters) > 0 {
		clone.SupportedParameters = append([]string(nil), model.SupportedParameters...)
	}
	if model.Thinking != nil {
		thinkingClone := *model.Thinking
		if len(model.Thinking.Levels) > 0 {
			thinkingClone.Levels = append([]string(nil), model.Thinking.Levels...)
		}
		clone.Thinking = &thinkingClone
	}
	return &clone
}

func storeAntigravityPrimaryModels(models []*registry.ModelInfo) bool {
	cloned := cloneAntigravityModels(models)
	if len(cloned) == 0 {
		return false
	}
	antigravityPrimaryModelsCache.mu.Lock()
	antigravityPrimaryModelsCache.models = cloned
	antigravityPrimaryModelsCache.mu.Unlock()
	return true
}

func loadAntigravityPrimaryModels() []*registry.ModelInfo {
	antigravityPrimaryModelsCache.mu.RLock()
	cloned := cloneAntigravityModels(antigravityPrimaryModelsCache.models)
	antigravityPrimaryModelsCache.mu.RUnlock()
	return cloned
}

func fallbackAntigravityPrimaryModels() []*registry.ModelInfo {
	models := loadAntigravityPrimaryModels()
	if len(models) > 0 {
		log.Debugf("antigravity executor: using cached primary model list (%d models)", len(models))
	}
	return models
}

// AntigravityExecutor proxies requests to the antigravity upstream.
type AntigravityExecutor struct {
	cfg *config.Config
}

// NewAntigravityExecutor creates a new Antigravity executor instance.
//
// Parameters:
//   - cfg: The application configuration
//
// Returns:
//   - *AntigravityExecutor: A new Antigravity executor instance
func NewAntigravityExecutor(cfg *config.Config) *AntigravityExecutor {
	return &AntigravityExecutor{cfg: cfg}
}

// Identifier returns the executor identifier.
func (e *AntigravityExecutor) Identifier() string { return antigravityAuthType }

// PrepareRequest injects Antigravity credentials into the outgoing HTTP request.
func (e *AntigravityExecutor) PrepareRequest(req *http.Request, auth *cliproxyauth.Auth) error {
	if req == nil {
		return nil
	}
	token, _, errToken := e.ensureAccessToken(req.Context(), auth)
	if errToken != nil {
		return errToken
	}
	if strings.TrimSpace(token) == "" {
		return statusErr{code: http.StatusUnauthorized, msg: "missing access token"}
	}
	req.Header.Set("Authorization", "Bearer "+token)
	return nil
}

// HttpRequest injects Antigravity credentials into the request and executes it.
func (e *AntigravityExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("antigravity executor: request is nil")
	}
	if ctx == nil {
		ctx = req.Context()
	}
	httpReq := req.WithContext(ctx)
	if err := e.PrepareRequest(httpReq, auth); err != nil {
		return nil, err
	}
	httpClient := newProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	return httpClient.Do(httpReq)
}

// Execute performs a non-streaming request to the Antigravity API.
func (e *AntigravityExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	if opts.Alt == "responses/compact" {
		return resp, statusErr{code: http.StatusNotImplemented, msg: "/responses/compact not supported"}
	}
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	isClaude := strings.Contains(strings.ToLower(baseModel), "claude")

	// Check for web search tool - if present and model is Claude, use Gemini directly instead
	// (Claude doesn't support web_search natively, so we route to Gemini)
	if isClaude && doWebSearchTool(req.Payload) {
		token, updatedAuth, errToken := e.ensureAccessToken(ctx, auth)
		if errToken != nil {
			return resp, errToken
		}
		if updatedAuth != nil {
			auth = updatedAuth
		}
		log.Debugf("antigravity executor: web_search tool detected, using Gemini for non-stream: %s", req.Model)
		return e.executeWebSearchOnly(ctx, auth, token, req, opts)
	}

	if isClaude || strings.Contains(baseModel, "gemini-3-pro") {
		return e.executeClaudeNonStream(ctx, auth, req, opts)
	}

	token, updatedAuth, errToken := e.ensureAccessToken(ctx, auth)
	if errToken != nil {
		return resp, errToken
	}
	if updatedAuth != nil {
		auth = updatedAuth
	}

	reporter := newUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.trackFailure(ctx, &err)

	from := opts.SourceFormat
	to := sdktranslator.FromString("antigravity")

	originalPayloadSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = opts.OriginalRequest
	}
	originalPayload := originalPayloadSource
	originalTranslated := sdktranslator.TranslateRequest(from, to, baseModel, originalPayload, false)
	translated := sdktranslator.TranslateRequest(from, to, baseModel, req.Payload, false)

	translated, err = thinking.ApplyThinking(translated, req.Model, from.String(), to.String(), e.Identifier())
	if err != nil {
		return resp, err
	}

	requestedModel := payloadRequestedModel(opts, req.Model)
	translated = applyPayloadConfigWithRoot(e.cfg, baseModel, "antigravity", "request", translated, originalTranslated, requestedModel)

	baseURLs := antigravityBaseURLFallbackOrder(auth)
	httpClient := newProxyAwareHTTPClient(ctx, e.cfg, auth, 0)

	attempts := antigravityRetryAttempts(auth, e.cfg)

attemptLoop:
	for attempt := 0; attempt < attempts; attempt++ {
		var lastStatus int
		var lastBody []byte
		var lastErr error

		for idx, baseURL := range baseURLs {
			httpReq, errReq := e.buildRequest(ctx, auth, token, baseModel, translated, false, opts.Alt, baseURL)
			if errReq != nil {
				err = errReq
				return resp, err
			}

			httpResp, errDo := httpClient.Do(httpReq)
			if errDo != nil {
				recordAPIResponseError(ctx, e.cfg, errDo)
				if errors.Is(errDo, context.Canceled) || errors.Is(errDo, context.DeadlineExceeded) {
					return resp, errDo
				}
				lastStatus = 0
				lastBody = nil
				lastErr = errDo
				if idx+1 < len(baseURLs) {
					log.Debugf("antigravity executor: request error on base url %s, retrying with fallback base url: %s", baseURL, baseURLs[idx+1])
					continue
				}
				err = errDo
				return resp, err
			}

			recordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
			bodyBytes, errRead := io.ReadAll(httpResp.Body)
			if errClose := httpResp.Body.Close(); errClose != nil {
				log.Errorf("antigravity executor: close response body error: %v", errClose)
			}
			if errRead != nil {
				recordAPIResponseError(ctx, e.cfg, errRead)
				err = errRead
				return resp, err
			}
			appendAPIResponseChunk(ctx, e.cfg, bodyBytes)

			if httpResp.StatusCode < http.StatusOK || httpResp.StatusCode >= http.StatusMultipleChoices {
				log.Debugf("antigravity executor: upstream error status: %d, body: %s", httpResp.StatusCode, summarizeErrorBody(httpResp.Header.Get("Content-Type"), bodyBytes))
				lastStatus = httpResp.StatusCode
				lastBody = append([]byte(nil), bodyBytes...)
				lastErr = nil
				if httpResp.StatusCode == http.StatusTooManyRequests {
					if retryAfter, parseErr := parseRetryDelay(bodyBytes); parseErr == nil && retryAfter != nil {
						log.Debugf("antigravity executor: rate limited with retryDelay %s, returning to conductor", *retryAfter)
						return resp, statusErr{code: httpResp.StatusCode, msg: string(bodyBytes), retryAfter: retryAfter}
					}
					if idx+1 < len(baseURLs) {
						log.Debugf("antigravity executor: rate limited on base url %s, retrying with fallback base url: %s", baseURL, baseURLs[idx+1])
						continue
					}
				}
				if antigravityShouldRetryNoCapacity(httpResp.StatusCode, bodyBytes) {
					if retryAfter := ParseAndClampRetryDelay(bodyBytes); retryAfter != nil {
						log.Debugf("antigravity executor: no capacity with retryDelay %s, returning to conductor", *retryAfter)
						return resp, statusErr{code: httpResp.StatusCode, msg: string(bodyBytes), retryAfter: retryAfter}
					}
					if idx+1 < len(baseURLs) {
						log.Debugf("antigravity executor: no capacity on base url %s, retrying with fallback base url: %s", baseURL, baseURLs[idx+1])
						continue
					}
					if attempt+1 < attempts {
						delay := DefaultNoCapacityRetryDelay(attempt)
						log.Debugf("antigravity executor: no capacity for model %s, retrying in %s (attempt %d/%d)", baseModel, delay, attempt+1, attempts)
						if errWait := antigravityWait(ctx, delay); errWait != nil {
							return resp, errWait
						}
						continue attemptLoop
					}
				}
				sErr := statusErr{code: httpResp.StatusCode, msg: string(bodyBytes)}
				err = sErr
				return resp, err
			}

			reporter.publish(ctx, parseAntigravityUsage(bodyBytes))
			var param any
			converted := sdktranslator.TranslateNonStream(ctx, to, from, req.Model, opts.OriginalRequest, translated, bodyBytes, &param)
			resp = cliproxyexecutor.Response{Payload: []byte(converted), Headers: httpResp.Header.Clone()}
			reporter.ensurePublished(ctx)
			return resp, nil
		}

		switch {
		case lastStatus != 0:
			sErr := statusErr{code: lastStatus, msg: string(lastBody)}
			if lastStatus == http.StatusTooManyRequests {
				if retryAfter, parseErr := parseRetryDelay(lastBody); parseErr == nil && retryAfter != nil {
					sErr.retryAfter = retryAfter
				}
			}
			err = sErr
		case lastErr != nil:
			err = lastErr
		default:
			err = statusErr{code: http.StatusServiceUnavailable, msg: "antigravity executor: no base url available"}
		}
		return resp, err
	}

	return resp, err
}

// executeClaudeNonStream performs a claude non-streaming request to the Antigravity API.
func (e *AntigravityExecutor) executeClaudeNonStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	token, updatedAuth, errToken := e.ensureAccessToken(ctx, auth)
	if errToken != nil {
		return resp, errToken
	}
	if updatedAuth != nil {
		auth = updatedAuth
	}

	reporter := newUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.trackFailure(ctx, &err)

	from := opts.SourceFormat
	to := sdktranslator.FromString("antigravity")

	originalPayloadSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = opts.OriginalRequest
	}
	originalPayload := originalPayloadSource
	originalTranslated := sdktranslator.TranslateRequest(from, to, baseModel, originalPayload, true)
	translated := sdktranslator.TranslateRequest(from, to, baseModel, req.Payload, true)

	translated, err = thinking.ApplyThinking(translated, req.Model, from.String(), to.String(), e.Identifier())
	if err != nil {
		return resp, err
	}

	requestedModel := payloadRequestedModel(opts, req.Model)
	translated = applyPayloadConfigWithRoot(e.cfg, baseModel, "antigravity", "request", translated, originalTranslated, requestedModel)

	baseURLs := antigravityBaseURLFallbackOrder(auth)
	httpClient := newProxyAwareHTTPClient(ctx, e.cfg, auth, 0)

	attempts := antigravityRetryAttempts(auth, e.cfg)

attemptLoop:
	for attempt := 0; attempt < attempts; attempt++ {
		var lastStatus int
		var lastBody []byte
		var lastErr error

		for idx, baseURL := range baseURLs {
			httpReq, errReq := e.buildRequest(ctx, auth, token, baseModel, translated, true, opts.Alt, baseURL)
			if errReq != nil {
				err = errReq
				return resp, err
			}

			httpResp, errDo := httpClient.Do(httpReq)
			if errDo != nil {
				recordAPIResponseError(ctx, e.cfg, errDo)
				if errors.Is(errDo, context.Canceled) || errors.Is(errDo, context.DeadlineExceeded) {
					return resp, errDo
				}
				lastStatus = 0
				lastBody = nil
				lastErr = errDo
				if idx+1 < len(baseURLs) {
					log.Debugf("antigravity executor: request error on base url %s, retrying with fallback base url: %s", baseURL, baseURLs[idx+1])
					continue
				}
				err = errDo
				return resp, err
			}
			recordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
			if httpResp.StatusCode < http.StatusOK || httpResp.StatusCode >= http.StatusMultipleChoices {
				bodyBytes, errRead := io.ReadAll(httpResp.Body)
				if errClose := httpResp.Body.Close(); errClose != nil {
					log.Errorf("antigravity executor: close response body error: %v", errClose)
				}
				if errRead != nil {
					recordAPIResponseError(ctx, e.cfg, errRead)
					if errors.Is(errRead, context.Canceled) || errors.Is(errRead, context.DeadlineExceeded) {
						err = errRead
						return resp, err
					}
					if errCtx := ctx.Err(); errCtx != nil {
						err = errCtx
						return resp, err
					}
					lastStatus = 0
					lastBody = nil
					lastErr = errRead
					if idx+1 < len(baseURLs) {
						log.Debugf("antigravity executor: read error on base url %s, retrying with fallback base url: %s", baseURL, baseURLs[idx+1])
						continue
					}
					err = errRead
					return resp, err
				}
				appendAPIResponseChunk(ctx, e.cfg, bodyBytes)
				lastStatus = httpResp.StatusCode
				lastBody = append([]byte(nil), bodyBytes...)
				lastErr = nil
				if httpResp.StatusCode == http.StatusTooManyRequests {
					if retryAfter, parseErr := parseRetryDelay(bodyBytes); parseErr == nil && retryAfter != nil {
						log.Debugf("antigravity executor: rate limited with retryDelay %s, returning to conductor", *retryAfter)
						return resp, statusErr{code: httpResp.StatusCode, msg: string(bodyBytes), retryAfter: retryAfter}
					}
					if idx+1 < len(baseURLs) {
						log.Debugf("antigravity executor: rate limited on base url %s, retrying with fallback base url: %s", baseURL, baseURLs[idx+1])
						continue
					}
				}
				if antigravityShouldRetryNoCapacity(httpResp.StatusCode, bodyBytes) {
					if retryAfter := ParseAndClampRetryDelay(bodyBytes); retryAfter != nil {
						log.Debugf("antigravity executor: no capacity with retryDelay %s, returning to conductor", *retryAfter)
						return resp, statusErr{code: httpResp.StatusCode, msg: string(bodyBytes), retryAfter: retryAfter}
					}
					if idx+1 < len(baseURLs) {
						log.Debugf("antigravity executor: no capacity on base url %s, retrying with fallback base url: %s", baseURL, baseURLs[idx+1])
						continue
					}
					if attempt+1 < attempts {
						delay := DefaultNoCapacityRetryDelay(attempt)
						log.Debugf("antigravity executor: no capacity for model %s, retrying in %s (attempt %d/%d)", baseModel, delay, attempt+1, attempts)
						if errWait := antigravityWait(ctx, delay); errWait != nil {
							return resp, errWait
						}
						continue attemptLoop
					}
				}
				sErr := statusErr{code: httpResp.StatusCode, msg: string(bodyBytes)}
				err = sErr
				return resp, err
			}

			out := make(chan cliproxyexecutor.StreamChunk)
			go func(resp *http.Response) {
				defer close(out)
				defer func() {
					if errClose := resp.Body.Close(); errClose != nil {
						log.Errorf("antigravity executor: close response body error: %v", errClose)
					}
				}()
				scanner := bufio.NewScanner(resp.Body)
				scanner.Buffer(nil, streamScannerBuffer)
				for scanner.Scan() {
					line := scanner.Bytes()
					appendAPIResponseChunk(ctx, e.cfg, line)

					// Filter usage metadata for all models
					// Only retain usage statistics in the terminal chunk
					line = FilterSSEUsageMetadata(line)

					payload := jsonPayload(line)
					if payload == nil {
						continue
					}

					if detail, ok := parseAntigravityStreamUsage(payload); ok {
						reporter.publish(ctx, detail)
					}

					out <- cliproxyexecutor.StreamChunk{Payload: payload}
				}
				if errScan := scanner.Err(); errScan != nil {
					recordAPIResponseError(ctx, e.cfg, errScan)
					reporter.publishFailure(ctx)
					out <- cliproxyexecutor.StreamChunk{Err: errScan}
				} else {
					reporter.ensurePublished(ctx)
				}
			}(httpResp)

			var buffer bytes.Buffer
			for chunk := range out {
				if chunk.Err != nil {
					return resp, chunk.Err
				}
				if len(chunk.Payload) > 0 {
					_, _ = buffer.Write(chunk.Payload)
					_, _ = buffer.Write([]byte("\n"))
				}
			}
			resp = cliproxyexecutor.Response{Payload: e.convertStreamToNonStream(buffer.Bytes())}

			reporter.publish(ctx, parseAntigravityUsage(resp.Payload))
			var param any
			converted := sdktranslator.TranslateNonStream(ctx, to, from, req.Model, opts.OriginalRequest, translated, resp.Payload, &param)
			resp = cliproxyexecutor.Response{Payload: []byte(converted), Headers: httpResp.Header.Clone()}
			reporter.ensurePublished(ctx)

			return resp, nil
		}

		switch {
		case lastStatus != 0:
			sErr := statusErr{code: lastStatus, msg: string(lastBody)}
			if lastStatus == http.StatusTooManyRequests {
				if retryAfter, parseErr := parseRetryDelay(lastBody); parseErr == nil && retryAfter != nil {
					sErr.retryAfter = retryAfter
				}
			}
			err = sErr
		case lastErr != nil:
			err = lastErr
		default:
			err = statusErr{code: http.StatusServiceUnavailable, msg: "antigravity executor: no base url available"}
		}
		return resp, err
	}

	return resp, err
}

func (e *AntigravityExecutor) convertStreamToNonStream(stream []byte) []byte {
	responseTemplate := ""
	var traceID string
	var finishReason string
	var modelVersion string
	var responseID string
	var role string
	var usageRaw string
	parts := make([]map[string]interface{}, 0)
	var pendingKind string
	var pendingText strings.Builder
	var pendingThoughtSig string

	flushPending := func() {
		if pendingKind == "" {
			return
		}
		text := pendingText.String()
		switch pendingKind {
		case "text":
			if strings.TrimSpace(text) == "" {
				pendingKind = ""
				pendingText.Reset()
				pendingThoughtSig = ""
				return
			}
			parts = append(parts, map[string]interface{}{"text": text})
		case "thought":
			if strings.TrimSpace(text) == "" && pendingThoughtSig == "" {
				pendingKind = ""
				pendingText.Reset()
				pendingThoughtSig = ""
				return
			}
			part := map[string]interface{}{"thought": true}
			part["text"] = text
			if pendingThoughtSig != "" {
				part["thoughtSignature"] = pendingThoughtSig
			}
			parts = append(parts, part)
		}
		pendingKind = ""
		pendingText.Reset()
		pendingThoughtSig = ""
	}

	normalizePart := func(partResult gjson.Result) map[string]interface{} {
		var m map[string]interface{}
		_ = json.Unmarshal([]byte(partResult.Raw), &m)
		if m == nil {
			m = map[string]interface{}{}
		}
		sig := partResult.Get("thoughtSignature").String()
		if sig == "" {
			sig = partResult.Get("thought_signature").String()
		}
		if sig != "" {
			m["thoughtSignature"] = sig
			delete(m, "thought_signature")
		}
		if inlineData, ok := m["inline_data"]; ok {
			m["inlineData"] = inlineData
			delete(m, "inline_data")
		}
		return m
	}

	for _, line := range bytes.Split(stream, []byte("\n")) {
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) == 0 || !gjson.ValidBytes(trimmed) {
			continue
		}

		root := gjson.ParseBytes(trimmed)
		responseNode := root.Get("response")
		if !responseNode.Exists() {
			if root.Get("candidates").Exists() {
				responseNode = root
			} else {
				continue
			}
		}
		responseTemplate = responseNode.Raw

		if traceResult := root.Get("traceId"); traceResult.Exists() && traceResult.String() != "" {
			traceID = traceResult.String()
		}

		if roleResult := responseNode.Get("candidates.0.content.role"); roleResult.Exists() {
			role = roleResult.String()
		}

		if finishResult := responseNode.Get("candidates.0.finishReason"); finishResult.Exists() && finishResult.String() != "" {
			finishReason = finishResult.String()
		}

		if modelResult := responseNode.Get("modelVersion"); modelResult.Exists() && modelResult.String() != "" {
			modelVersion = modelResult.String()
		}
		if responseIDResult := responseNode.Get("responseId"); responseIDResult.Exists() && responseIDResult.String() != "" {
			responseID = responseIDResult.String()
		}
		if usageResult := responseNode.Get("usageMetadata"); usageResult.Exists() {
			usageRaw = usageResult.Raw
		} else if usageMetadataResult := root.Get("usageMetadata"); usageMetadataResult.Exists() {
			usageRaw = usageMetadataResult.Raw
		}

		if partsResult := responseNode.Get("candidates.0.content.parts"); partsResult.IsArray() {
			for _, part := range partsResult.Array() {
				hasFunctionCall := part.Get("functionCall").Exists()
				hasInlineData := part.Get("inlineData").Exists() || part.Get("inline_data").Exists()
				sig := part.Get("thoughtSignature").String()
				if sig == "" {
					sig = part.Get("thought_signature").String()
				}
				text := part.Get("text").String()
				thought := part.Get("thought").Bool()

				if hasFunctionCall || hasInlineData {
					flushPending()
					parts = append(parts, normalizePart(part))
					continue
				}

				if thought || part.Get("text").Exists() {
					kind := "text"
					if thought {
						kind = "thought"
					}
					if pendingKind != "" && pendingKind != kind {
						flushPending()
					}
					pendingKind = kind
					pendingText.WriteString(text)
					if kind == "thought" && sig != "" {
						pendingThoughtSig = sig
					}
					continue
				}

				flushPending()
				parts = append(parts, normalizePart(part))
			}
		}
	}
	flushPending()

	if responseTemplate == "" {
		responseTemplate = `{"candidates":[{"content":{"role":"model","parts":[]}}]}`
	}

	partsJSON, _ := json.Marshal(parts)
	responseTemplate, _ = sjson.SetRaw(responseTemplate, "candidates.0.content.parts", string(partsJSON))
	if role != "" {
		responseTemplate, _ = sjson.Set(responseTemplate, "candidates.0.content.role", role)
	}
	if finishReason != "" {
		responseTemplate, _ = sjson.Set(responseTemplate, "candidates.0.finishReason", finishReason)
	}
	if modelVersion != "" {
		responseTemplate, _ = sjson.Set(responseTemplate, "modelVersion", modelVersion)
	}
	if responseID != "" {
		responseTemplate, _ = sjson.Set(responseTemplate, "responseId", responseID)
	}
	if usageRaw != "" {
		responseTemplate, _ = sjson.SetRaw(responseTemplate, "usageMetadata", usageRaw)
	} else if !gjson.Get(responseTemplate, "usageMetadata").Exists() {
		responseTemplate, _ = sjson.Set(responseTemplate, "usageMetadata.promptTokenCount", 0)
		responseTemplate, _ = sjson.Set(responseTemplate, "usageMetadata.candidatesTokenCount", 0)
		responseTemplate, _ = sjson.Set(responseTemplate, "usageMetadata.totalTokenCount", 0)
	}

	output := `{"response":{},"traceId":""}`
	output, _ = sjson.SetRaw(output, "response", responseTemplate)
	if traceID != "" {
		output, _ = sjson.Set(output, "traceId", traceID)
	}
	return []byte(output)
}

// ExecuteStream performs a streaming request to the Antigravity API.
func (e *AntigravityExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	if opts.Alt == "responses/compact" {
		return nil, statusErr{code: http.StatusNotImplemented, msg: "/responses/compact not supported"}
	}
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	ctx = context.WithValue(ctx, "alt", "")

	token, updatedAuth, errToken := e.ensureAccessToken(ctx, auth)
	if errToken != nil {
		return nil, errToken
	}
	if updatedAuth != nil {
		auth = updatedAuth
	}

	// Web search tool + Claude model: route to Gemini instead (Claude doesn't support web_search natively)
	isClaude := strings.Contains(strings.ToLower(baseModel), "claude")
	if isClaude && doWebSearchTool(req.Payload) {
		log.Debugf("antigravity executor: web_search tool detected, using Gemini for stream: %s", req.Model)
		chunks, wsErr := e.executeWebSearchOnlyStream(ctx, auth, token, req, opts)
		if wsErr != nil {
			return nil, wsErr
		}
		return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
	}

	reporter := newUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.trackFailure(ctx, &err)

	from := opts.SourceFormat
	to := sdktranslator.FromString("antigravity")

	originalPayloadSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = opts.OriginalRequest
	}
	originalPayload := originalPayloadSource
	originalTranslated := sdktranslator.TranslateRequest(from, to, baseModel, originalPayload, true)
	translated := sdktranslator.TranslateRequest(from, to, baseModel, req.Payload, true)

	translated, err = thinking.ApplyThinking(translated, req.Model, from.String(), to.String(), e.Identifier())
	if err != nil {
		return nil, err
	}

	requestedModel := payloadRequestedModel(opts, req.Model)
	translated = applyPayloadConfigWithRoot(e.cfg, baseModel, "antigravity", "request", translated, originalTranslated, requestedModel)

	baseURLs := antigravityBaseURLFallbackOrder(auth)
	httpClient := newProxyAwareHTTPClient(ctx, e.cfg, auth, 0)

	attempts := antigravityRetryAttempts(auth, e.cfg)

attemptLoop:
	for attempt := 0; attempt < attempts; attempt++ {
		var lastStatus int
		var lastBody []byte
		var lastErr error

		for idx, baseURL := range baseURLs {
			httpReq, errReq := e.buildRequest(ctx, auth, token, baseModel, translated, true, opts.Alt, baseURL)
			if errReq != nil {
				err = errReq
				return nil, err
			}
			httpResp, errDo := httpClient.Do(httpReq)
			if errDo != nil {
				recordAPIResponseError(ctx, e.cfg, errDo)
				if errors.Is(errDo, context.Canceled) || errors.Is(errDo, context.DeadlineExceeded) {
					return nil, errDo
				}
				lastStatus = 0
				lastBody = nil
				lastErr = errDo
				if idx+1 < len(baseURLs) {
					log.Debugf("antigravity executor: request error on base url %s, retrying with fallback base url: %s", baseURL, baseURLs[idx+1])
					continue
				}
				err = errDo
				return nil, err
			}
			recordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
			if httpResp.StatusCode < http.StatusOK || httpResp.StatusCode >= http.StatusMultipleChoices {
				bodyBytes, errRead := io.ReadAll(httpResp.Body)
				if errClose := httpResp.Body.Close(); errClose != nil {
					log.Errorf("antigravity executor: close response body error: %v", errClose)
				}
				if errRead != nil {
					recordAPIResponseError(ctx, e.cfg, errRead)
					if errors.Is(errRead, context.Canceled) || errors.Is(errRead, context.DeadlineExceeded) {
						err = errRead
						return nil, err
					}
					if errCtx := ctx.Err(); errCtx != nil {
						err = errCtx
						return nil, err
					}
					lastStatus = 0
					lastBody = nil
					lastErr = errRead
					if idx+1 < len(baseURLs) {
						log.Debugf("antigravity executor: read error on base url %s, retrying with fallback base url: %s", baseURL, baseURLs[idx+1])
						continue
					}
					err = errRead
					return nil, err
				}
				appendAPIResponseChunk(ctx, e.cfg, bodyBytes)
				lastStatus = httpResp.StatusCode
				lastBody = append([]byte(nil), bodyBytes...)
				lastErr = nil
				if httpResp.StatusCode == http.StatusTooManyRequests {
					if retryAfter, parseErr := parseRetryDelay(bodyBytes); parseErr == nil && retryAfter != nil {
						log.Debugf("antigravity executor: rate limited with retryDelay %s, returning to conductor", *retryAfter)
						return nil, statusErr{code: httpResp.StatusCode, msg: string(bodyBytes), retryAfter: retryAfter}
					}
					if idx+1 < len(baseURLs) {
						log.Debugf("antigravity executor: rate limited on base url %s, retrying with fallback base url: %s", baseURL, baseURLs[idx+1])
						continue
					}
				}
				if antigravityShouldRetryNoCapacity(httpResp.StatusCode, bodyBytes) {
					if retryAfter := ParseAndClampRetryDelay(bodyBytes); retryAfter != nil {
						log.Debugf("antigravity executor: no capacity with retryDelay %s, returning to conductor", *retryAfter)
						return nil, statusErr{code: httpResp.StatusCode, msg: string(bodyBytes), retryAfter: retryAfter}
					}
					if idx+1 < len(baseURLs) {
						log.Debugf("antigravity executor: no capacity on base url %s, retrying with fallback base url: %s", baseURL, baseURLs[idx+1])
						continue
					}
					if attempt+1 < attempts {
						delay := DefaultNoCapacityRetryDelay(attempt)
						log.Debugf("antigravity executor: no capacity for model %s, retrying in %s (attempt %d/%d)", baseModel, delay, attempt+1, attempts)
						if errWait := antigravityWait(ctx, delay); errWait != nil {
							return nil, errWait
						}
						continue attemptLoop
					}
				}
				sErr := statusErr{code: httpResp.StatusCode, msg: string(bodyBytes)}
				err = sErr
				return nil, err
			}

			out := make(chan cliproxyexecutor.StreamChunk)
			go func(resp *http.Response) {
				defer close(out)
				defer func() {
					if errClose := resp.Body.Close(); errClose != nil {
						log.Errorf("antigravity executor: close response body error: %v", errClose)
					}
				}()
				scanner := bufio.NewScanner(resp.Body)
				scanner.Buffer(nil, streamScannerBuffer)
				var param any
				for scanner.Scan() {
					line := scanner.Bytes()
					appendAPIResponseChunk(ctx, e.cfg, line)

					// Filter usage metadata for all models
					// Only retain usage statistics in the terminal chunk
					line = FilterSSEUsageMetadata(line)

					payload := jsonPayload(line)
					if payload == nil {
						continue
					}

					if detail, ok := parseAntigravityStreamUsage(payload); ok {
						reporter.publish(ctx, detail)
					}

					chunks := sdktranslator.TranslateStream(ctx, to, from, req.Model, opts.OriginalRequest, translated, bytes.Clone(payload), &param)
					for i := range chunks {
						out <- cliproxyexecutor.StreamChunk{Payload: []byte(chunks[i])}
					}
				}
				tail := sdktranslator.TranslateStream(ctx, to, from, req.Model, opts.OriginalRequest, translated, []byte("[DONE]"), &param)
				for i := range tail {
					out <- cliproxyexecutor.StreamChunk{Payload: []byte(tail[i])}
				}
				if errScan := scanner.Err(); errScan != nil {
					recordAPIResponseError(ctx, e.cfg, errScan)
					reporter.publishFailure(ctx)
					out <- cliproxyexecutor.StreamChunk{Err: errScan}
				} else {
					reporter.ensurePublished(ctx)
				}
			}(httpResp)
			return &cliproxyexecutor.StreamResult{Headers: httpResp.Header.Clone(), Chunks: out}, nil
		}

		switch {
		case lastStatus != 0:
			sErr := statusErr{code: lastStatus, msg: string(lastBody)}
			if lastStatus == http.StatusTooManyRequests {
				if retryAfter, parseErr := parseRetryDelay(lastBody); parseErr == nil && retryAfter != nil {
					sErr.retryAfter = retryAfter
				}
			}
			err = sErr
		case lastErr != nil:
			err = lastErr
		default:
			err = statusErr{code: http.StatusServiceUnavailable, msg: "antigravity executor: no base url available"}
		}
		return nil, err
	}

	return nil, err
}

// Refresh refreshes the authentication credentials using the refresh token.
func (e *AntigravityExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	if auth == nil {
		return auth, nil
	}
	updated, errRefresh := e.refreshToken(ctx, auth.Clone())
	if errRefresh != nil {
		return nil, errRefresh
	}
	return updated, nil
}

// CountTokens counts tokens for the given request using the Antigravity API.
func (e *AntigravityExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	token, updatedAuth, errToken := e.ensureAccessToken(ctx, auth)
	if errToken != nil {
		return cliproxyexecutor.Response{}, errToken
	}
	if updatedAuth != nil {
		auth = updatedAuth
	}
	if strings.TrimSpace(token) == "" {
		return cliproxyexecutor.Response{}, statusErr{code: http.StatusUnauthorized, msg: "missing access token"}
	}

	from := opts.SourceFormat
	to := sdktranslator.FromString("antigravity")
	respCtx := context.WithValue(ctx, "alt", opts.Alt)

	// Prepare payload once (doesn't depend on baseURL)
	payload := sdktranslator.TranslateRequest(from, to, baseModel, req.Payload, false)

	payload, err := thinking.ApplyThinking(payload, req.Model, from.String(), to.String(), e.Identifier())
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}

	payload = deleteJSONField(payload, "project")
	payload = deleteJSONField(payload, "model")
	payload = deleteJSONField(payload, "request.safetySettings")

	baseURLs := antigravityBaseURLFallbackOrder(auth)
	httpClient := newProxyAwareHTTPClient(ctx, e.cfg, auth, 0)

	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}

	var lastStatus int
	var lastBody []byte
	var lastErr error

	for idx, baseURL := range baseURLs {
		base := strings.TrimSuffix(baseURL, "/")
		if base == "" {
			base = buildBaseURL(auth)
		}

		var requestURL strings.Builder
		requestURL.WriteString(base)
		requestURL.WriteString(antigravityCountTokensPath)
		if opts.Alt != "" {
			requestURL.WriteString("?$alt=")
			requestURL.WriteString(url.QueryEscape(opts.Alt))
		}

		httpReq, errReq := http.NewRequestWithContext(ctx, http.MethodPost, requestURL.String(), bytes.NewReader(payload))
		if errReq != nil {
			return cliproxyexecutor.Response{}, errReq
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Authorization", "Bearer "+token)
		httpReq.Header.Set("User-Agent", resolveUserAgent(auth))
		httpReq.Header.Set("Accept", "application/json")
		if host := resolveHost(base); host != "" {
			httpReq.Host = host
		}

		recordAPIRequest(ctx, e.cfg, upstreamRequestLog{
			URL:       requestURL.String(),
			Method:    http.MethodPost,
			Headers:   httpReq.Header.Clone(),
			Body:      payload,
			Provider:  e.Identifier(),
			AuthID:    authID,
			AuthLabel: authLabel,
			AuthType:  authType,
			AuthValue: authValue,
		})

		httpResp, errDo := httpClient.Do(httpReq)
		if errDo != nil {
			recordAPIResponseError(ctx, e.cfg, errDo)
			if errors.Is(errDo, context.Canceled) || errors.Is(errDo, context.DeadlineExceeded) {
				return cliproxyexecutor.Response{}, errDo
			}
			lastStatus = 0
			lastBody = nil
			lastErr = errDo
			if idx+1 < len(baseURLs) {
				log.Debugf("antigravity executor: request error on base url %s, retrying with fallback base url: %s", baseURL, baseURLs[idx+1])
				continue
			}
			return cliproxyexecutor.Response{}, errDo
		}

		recordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
		bodyBytes, errRead := io.ReadAll(httpResp.Body)
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("antigravity executor: close response body error: %v", errClose)
		}
		if errRead != nil {
			recordAPIResponseError(ctx, e.cfg, errRead)
			return cliproxyexecutor.Response{}, errRead
		}
		appendAPIResponseChunk(ctx, e.cfg, bodyBytes)

		if httpResp.StatusCode >= http.StatusOK && httpResp.StatusCode < http.StatusMultipleChoices {
			count := gjson.GetBytes(bodyBytes, "totalTokens").Int()
			translated := sdktranslator.TranslateTokenCount(respCtx, to, from, count, bodyBytes)
			return cliproxyexecutor.Response{Payload: []byte(translated), Headers: httpResp.Header.Clone()}, nil
		}

		lastStatus = httpResp.StatusCode
		lastBody = append([]byte(nil), bodyBytes...)
		lastErr = nil
		if httpResp.StatusCode == http.StatusTooManyRequests {
			if retryAfter, parseErr := parseRetryDelay(bodyBytes); parseErr == nil && retryAfter != nil {
				log.Debugf("antigravity executor: rate limited with retryDelay %s, returning to conductor", *retryAfter)
				return cliproxyexecutor.Response{}, statusErr{code: httpResp.StatusCode, msg: string(bodyBytes), retryAfter: retryAfter}
			}
			if idx+1 < len(baseURLs) {
				log.Debugf("antigravity executor: rate limited on base url %s, retrying with fallback base url: %s", baseURL, baseURLs[idx+1])
				continue
			}
		}
		sErr := statusErr{code: httpResp.StatusCode, msg: string(bodyBytes)}
		return cliproxyexecutor.Response{}, sErr
	}

	switch {
	case lastStatus != 0:
		sErr := statusErr{code: lastStatus, msg: string(lastBody)}
		if lastStatus == http.StatusTooManyRequests {
			if retryAfter, parseErr := parseRetryDelay(lastBody); parseErr == nil && retryAfter != nil {
				sErr.retryAfter = retryAfter
			}
		}
		return cliproxyexecutor.Response{}, sErr
	case lastErr != nil:
		return cliproxyexecutor.Response{}, lastErr
	default:
		return cliproxyexecutor.Response{}, statusErr{code: http.StatusServiceUnavailable, msg: "antigravity executor: no base url available"}
	}
}

// FetchAntigravityModels retrieves available models using the supplied auth.
func FetchAntigravityModels(ctx context.Context, auth *cliproxyauth.Auth, cfg *config.Config) []*registry.ModelInfo {
	exec := &AntigravityExecutor{cfg: cfg}
	token, updatedAuth, errToken := exec.ensureAccessToken(ctx, auth)
	if errToken != nil || token == "" {
		return fallbackAntigravityPrimaryModels()
	}
	if updatedAuth != nil {
		auth = updatedAuth
	}

	baseURLs := antigravityBaseURLFallbackOrder(auth)
	httpClient := newProxyAwareHTTPClient(ctx, cfg, auth, 0)

	for idx, baseURL := range baseURLs {
		modelsURL := baseURL + antigravityModelsPath
		httpReq, errReq := http.NewRequestWithContext(ctx, http.MethodPost, modelsURL, bytes.NewReader([]byte(`{}`)))
		if errReq != nil {
			return fallbackAntigravityPrimaryModels()
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Authorization", "Bearer "+token)
		httpReq.Header.Set("User-Agent", resolveUserAgent(auth))
		if host := resolveHost(baseURL); host != "" {
			httpReq.Host = host
		}

		httpResp, errDo := httpClient.Do(httpReq)
		if errDo != nil {
			if errors.Is(errDo, context.Canceled) || errors.Is(errDo, context.DeadlineExceeded) {
				return fallbackAntigravityPrimaryModels()
			}
			if idx+1 < len(baseURLs) {
				log.Debugf("antigravity executor: models request error on base url %s, retrying with fallback base url: %s", baseURL, baseURLs[idx+1])
				continue
			}
			return fallbackAntigravityPrimaryModels()
		}

		bodyBytes, errRead := io.ReadAll(httpResp.Body)
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("antigravity executor: close response body error: %v", errClose)
		}
		if errRead != nil {
			if idx+1 < len(baseURLs) {
				log.Debugf("antigravity executor: models read error on base url %s, retrying with fallback base url: %s", baseURL, baseURLs[idx+1])
				continue
			}
			return fallbackAntigravityPrimaryModels()
		}
		if httpResp.StatusCode < http.StatusOK || httpResp.StatusCode >= http.StatusMultipleChoices {
			if httpResp.StatusCode == http.StatusTooManyRequests && idx+1 < len(baseURLs) {
				log.Debugf("antigravity executor: models request rate limited on base url %s, retrying with fallback base url: %s", baseURL, baseURLs[idx+1])
				continue
			}
			if idx+1 < len(baseURLs) {
				log.Debugf("antigravity executor: models request failed with status %d on base url %s, retrying with fallback base url: %s", httpResp.StatusCode, baseURL, baseURLs[idx+1])
				continue
			}
			return fallbackAntigravityPrimaryModels()
		}

		result := gjson.GetBytes(bodyBytes, "models")
		if !result.Exists() {
			if idx+1 < len(baseURLs) {
				log.Debugf("antigravity executor: models field missing on base url %s, retrying with fallback base url: %s", baseURL, baseURLs[idx+1])
				continue
			}
			return fallbackAntigravityPrimaryModels()
		}

		now := time.Now().Unix()
		modelConfig := registry.GetAntigravityModelConfig()
		models := make([]*registry.ModelInfo, 0, len(result.Map()))
		for originalName, modelData := range result.Map() {
			modelID := strings.TrimSpace(originalName)
			if modelID == "" {
				continue
			}
			switch modelID {
			case "chat_20706", "chat_23310", "gemini-2.5-flash-thinking", "gemini-3-pro-low", "gemini-2.5-pro":
				continue
			}
			modelCfg := modelConfig[modelID]

			// Extract displayName from upstream response, fallback to modelID
			displayName := modelData.Get("displayName").String()
			if displayName == "" {
				displayName = modelID
			}

			modelInfo := &registry.ModelInfo{
				ID:          modelID,
				Name:        modelID,
				Description: displayName,
				DisplayName: displayName,
				Version:     modelID,
				Object:      "model",
				Created:     now,
				OwnedBy:     antigravityAuthType,
				Type:        antigravityAuthType,
			}
			// Look up Thinking support from static config using upstream model name.
			if modelCfg != nil {
				if modelCfg.Thinking != nil {
					modelInfo.Thinking = modelCfg.Thinking
				}
				if modelCfg.MaxCompletionTokens > 0 {
					modelInfo.MaxCompletionTokens = modelCfg.MaxCompletionTokens
				}
			}
			models = append(models, modelInfo)
		}
		if len(models) == 0 {
			if idx+1 < len(baseURLs) {
				log.Debugf("antigravity executor: empty models list on base url %s, retrying with fallback base url: %s", baseURL, baseURLs[idx+1])
				continue
			}
			log.Debug("antigravity executor: fetched empty model list; retaining cached primary model list")
			return fallbackAntigravityPrimaryModels()
		}
		storeAntigravityPrimaryModels(models)
		return models
	}
	return fallbackAntigravityPrimaryModels()
}

func (e *AntigravityExecutor) ensureAccessToken(ctx context.Context, auth *cliproxyauth.Auth) (string, *cliproxyauth.Auth, error) {
	if auth == nil {
		return "", nil, statusErr{code: http.StatusUnauthorized, msg: "missing auth"}
	}
	accessToken := metaStringValue(auth.Metadata, "access_token")
	expiry := tokenExpiry(auth.Metadata)
	if accessToken != "" && expiry.After(time.Now().Add(refreshSkew)) {
		return accessToken, nil, nil
	}
	refreshCtx := context.Background()
	if ctx != nil {
		if rt, ok := ctx.Value("cliproxy.roundtripper").(http.RoundTripper); ok && rt != nil {
			refreshCtx = context.WithValue(refreshCtx, "cliproxy.roundtripper", rt)
		}
	}
	updated, errRefresh := e.refreshToken(refreshCtx, auth.Clone())
	if errRefresh != nil {
		return "", nil, errRefresh
	}
	return metaStringValue(updated.Metadata, "access_token"), updated, nil
}

func (e *AntigravityExecutor) refreshToken(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	if auth == nil {
		return nil, statusErr{code: http.StatusUnauthorized, msg: "missing auth"}
	}
	refreshToken := metaStringValue(auth.Metadata, "refresh_token")
	if refreshToken == "" {
		return auth, statusErr{code: http.StatusUnauthorized, msg: "missing refresh token"}
	}

	form := url.Values{}
	form.Set("client_id", antigravityClientID)
	form.Set("client_secret", antigravityClientSecret)
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refreshToken)

	httpReq, errReq := http.NewRequestWithContext(ctx, http.MethodPost, "https://oauth2.googleapis.com/token", strings.NewReader(form.Encode()))
	if errReq != nil {
		return auth, errReq
	}
	httpReq.Header.Set("Host", "oauth2.googleapis.com")
	httpReq.Header.Set("User-Agent", defaultAntigravityAgent)
	httpReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	httpClient := newProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpResp, errDo := httpClient.Do(httpReq)
	if errDo != nil {
		return auth, errDo
	}
	defer func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("antigravity executor: close response body error: %v", errClose)
		}
	}()

	bodyBytes, errRead := io.ReadAll(httpResp.Body)
	if errRead != nil {
		return auth, errRead
	}

	if httpResp.StatusCode < http.StatusOK || httpResp.StatusCode >= http.StatusMultipleChoices {
		sErr := statusErr{code: httpResp.StatusCode, msg: string(bodyBytes)}
		if httpResp.StatusCode == http.StatusTooManyRequests {
			if retryAfter, parseErr := parseRetryDelay(bodyBytes); parseErr == nil && retryAfter != nil {
				sErr.retryAfter = retryAfter
			}
		}
		return auth, sErr
	}

	var tokenResp struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
		TokenType    string `json:"token_type"`
	}
	if errUnmarshal := json.Unmarshal(bodyBytes, &tokenResp); errUnmarshal != nil {
		return auth, errUnmarshal
	}

	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	auth.Metadata["access_token"] = tokenResp.AccessToken
	if tokenResp.RefreshToken != "" {
		auth.Metadata["refresh_token"] = tokenResp.RefreshToken
	}
	auth.Metadata["expires_in"] = tokenResp.ExpiresIn
	now := time.Now()
	auth.Metadata["timestamp"] = now.UnixMilli()
	auth.Metadata["expired"] = now.Add(time.Duration(tokenResp.ExpiresIn) * time.Second).Format(time.RFC3339)
	auth.Metadata["type"] = antigravityAuthType
	if errProject := e.ensureAntigravityProjectID(ctx, auth, tokenResp.AccessToken); errProject != nil {
		log.Warnf("antigravity executor: ensure project id failed: %v", errProject)
	}
	e.applyUserSettings(ctx, auth, tokenResp.AccessToken)
	return auth, nil
}

func (e *AntigravityExecutor) ensureAntigravityProjectID(ctx context.Context, auth *cliproxyauth.Auth, accessToken string) error {
	if auth == nil {
		return nil
	}

	if auth.Metadata["project_id"] != nil {
		return nil
	}

	token := strings.TrimSpace(accessToken)
	if token == "" {
		token = metaStringValue(auth.Metadata, "access_token")
	}
	if token == "" {
		return nil
	}

	httpClient := newProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	projectID, errFetch := sdkAuth.FetchAntigravityProjectID(ctx, token, httpClient)
	if errFetch != nil {
		return errFetch
	}
	if strings.TrimSpace(projectID) == "" {
		return nil
	}
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	auth.Metadata["project_id"] = strings.TrimSpace(projectID)

	return nil
}

func (e *AntigravityExecutor) applyUserSettings(ctx context.Context, auth *cliproxyauth.Auth, accessToken string) {
	token := strings.TrimSpace(accessToken)
	if token == "" {
		return
	}

	settingsURL := buildBaseURL(auth) + antigravitySetUserSettingsPath
	body := `{"user_settings":{"telemetry_enabled":false,"user_data_collection_force_disabled":true,"marketing_emails_enabled":false}}`

	req, errReq := http.NewRequestWithContext(ctx, http.MethodPost, settingsURL, strings.NewReader(body))
	if errReq != nil {
		log.Warnf("antigravity executor: build setUserSettings request failed: %v", errReq)
		return
	}
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "antigravity/1.107.0 darwin/arm64 google-api-nodejs-client/10.3.0")
	req.Header.Set("X-Goog-Api-Client", "gl-node/22.21.1")
	req.Header.Set("Connection", "keep-alive")

	httpClient := newProxyAwareHTTPClient(ctx, e.cfg, auth, 5*time.Second)
	resp, errDo := httpClient.Do(req)
	if errDo != nil {
		log.Warnf("antigravity executor: setUserSettings request failed: %v", errDo)
		return
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.ReadAll(resp.Body)

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		log.Warnf("antigravity executor: setUserSettings returned HTTP %d", resp.StatusCode)
		return
	}
	label := metaStringValue(auth.Metadata, "email")
	if label == "" {
		label = auth.Label
	}
	log.Infof("antigravity executor: setUserSettings applied for %s", label)
}

func (e *AntigravityExecutor) buildRequest(ctx context.Context, auth *cliproxyauth.Auth, token, modelName string, payload []byte, stream bool, alt, baseURL string) (*http.Request, error) {
	if token == "" {
		return nil, statusErr{code: http.StatusUnauthorized, msg: "missing access token"}
	}

	base := strings.TrimSuffix(baseURL, "/")
	if base == "" {
		base = buildBaseURL(auth)
	}
	path := antigravityGeneratePath
	if stream {
		path = antigravityStreamPath
	}
	var requestURL strings.Builder
	requestURL.WriteString(base)
	requestURL.WriteString(path)
	if stream {
		if alt != "" {
			requestURL.WriteString("?$alt=")
			requestURL.WriteString(url.QueryEscape(alt))
		} else {
			requestURL.WriteString("?alt=sse")
		}
	} else if alt != "" {
		requestURL.WriteString("?$alt=")
		requestURL.WriteString(url.QueryEscape(alt))
	}

	// Extract project_id from auth metadata if available
	projectID := ""
	if auth != nil && auth.Metadata != nil {
		if pid, ok := auth.Metadata["project_id"].(string); ok {
			projectID = strings.TrimSpace(pid)
		}
	}
	payload = geminiToAntigravity(modelName, payload, projectID)
	payload, _ = sjson.SetBytes(payload, "model", modelName)

	// Clean and rename tool schemas: parametersJsonSchema -> parameters
	// PERF: Clean each schema individually for cache efficiency. Full payload cleaning
	// always misses cache due to dynamic content (messages, timestamps).
	isClaudeOrG3High := strings.Contains(modelName, "claude") || strings.Contains(modelName, "gemini-3-pro-high")
	payloadStr := string(payload)
	{
		paths := make([]string, 0)
		util.Walk(gjson.ParseBytes(payload), "", "parametersJsonSchema", &paths)

		if len(paths) > 0 {
			const suffix = "parametersJsonSchema"

			for _, p := range paths {
				schemaRaw := gjson.Get(payloadStr, p).Raw
				newPath := p[:len(p)-len(suffix)] + "parameters"

				if schemaRaw == "" || schemaRaw == "null" {
					if renamed, err := util.RenameKey(payloadStr, p, newPath); err == nil {
						payloadStr = renamed
					}
					continue
				}

				var cleanedSchema string
				if isClaudeOrG3High {
					cleanedSchema = util.CleanJSONSchemaForAntigravity(schemaRaw)
				} else {
					cleanedSchema = util.CleanJSONSchemaForGemini(schemaRaw)
				}

				// Atomic: apply both SetRaw and Delete only if both succeed
				updated, err := sjson.SetRaw(payloadStr, newPath, cleanedSchema)
				if err != nil {
					continue
				}
				updated, err = sjson.Delete(updated, p)
				if err != nil {
					continue
				}
				payloadStr = updated
			}
		}
	}

	if isClaudeOrG3High {
		systemInstructionPartsResult := gjson.Get(payloadStr, "request.systemInstruction.parts")
		payloadStr, _ = sjson.Set(payloadStr, "request.systemInstruction.role", "user")
		payloadStr, _ = sjson.Set(payloadStr, "request.systemInstruction.parts.0.text", systemInstruction)
		payloadStr, _ = sjson.Set(payloadStr, "request.systemInstruction.parts.1.text", fmt.Sprintf("Please ignore following [ignore]%s[/ignore]", systemInstruction))

		if systemInstructionPartsResult.Exists() && systemInstructionPartsResult.IsArray() {
			for _, partResult := range systemInstructionPartsResult.Array() {
				payloadStr, _ = sjson.SetRaw(payloadStr, "request.systemInstruction.parts.-1", partResult.Raw)
			}
		}
	}

	if strings.Contains(modelName, "claude") {
		payloadStr, _ = sjson.Set(payloadStr, "request.toolConfig.functionCallingConfig.mode", "VALIDATED")
		// Clamp maxOutputTokens to model's MaxCompletionTokens to prevent 400 INVALID_ARGUMENT.
		// This runs unconditionally, unlike normalizeClaudeBudget which only runs with thinking config.
		if modelInfo := registry.LookupModelInfo(modelName, "antigravity"); modelInfo != nil && modelInfo.MaxCompletionTokens > 0 {
			if maxTok := gjson.Get(payloadStr, "request.generationConfig.maxOutputTokens"); maxTok.Exists() && int(maxTok.Int()) > modelInfo.MaxCompletionTokens {
				payloadStr, _ = sjson.Set(payloadStr, "request.generationConfig.maxOutputTokens", modelInfo.MaxCompletionTokens)
			}
		}
	} else {
		payloadStr, _ = sjson.Delete(payloadStr, "request.generationConfig.maxOutputTokens")
	}

	httpReq, errReq := http.NewRequestWithContext(ctx, http.MethodPost, requestURL.String(), strings.NewReader(payloadStr))
	if errReq != nil {
		return nil, errReq
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+token)
	httpReq.Header.Set("User-Agent", resolveUserAgent(auth))
	if stream {
		httpReq.Header.Set("Accept", "text/event-stream")
	} else {
		httpReq.Header.Set("Accept", "application/json")
	}
	if host := resolveHost(base); host != "" {
		httpReq.Host = host
	}

	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}
	recordAPIRequest(ctx, e.cfg, upstreamRequestLog{
		URL:       requestURL.String(),
		Method:    http.MethodPost,
		Headers:   httpReq.Header.Clone(),
		Body:      []byte(payloadStr),
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})

	return httpReq, nil
}

func tokenExpiry(metadata map[string]any) time.Time {
	if metadata == nil {
		return time.Time{}
	}
	if expStr, ok := metadata["expired"].(string); ok {
		expStr = strings.TrimSpace(expStr)
		if expStr != "" {
			if parsed, errParse := time.Parse(time.RFC3339, expStr); errParse == nil {
				return parsed
			}
		}
	}
	expiresIn, hasExpires := int64Value(metadata["expires_in"])
	tsMs, hasTimestamp := int64Value(metadata["timestamp"])
	if hasExpires && hasTimestamp {
		return time.Unix(0, tsMs*int64(time.Millisecond)).Add(time.Duration(expiresIn) * time.Second)
	}
	return time.Time{}
}

func metaStringValue(metadata map[string]any, key string) string {
	if metadata == nil {
		return ""
	}
	if v, ok := metadata[key]; ok {
		switch typed := v.(type) {
		case string:
			return strings.TrimSpace(typed)
		case []byte:
			return strings.TrimSpace(string(typed))
		}
	}
	return ""
}

func int64Value(value any) (int64, bool) {
	switch typed := value.(type) {
	case int:
		return int64(typed), true
	case int64:
		return typed, true
	case float64:
		return int64(typed), true
	case json.Number:
		if i, errParse := typed.Int64(); errParse == nil {
			return i, true
		}
	case string:
		if strings.TrimSpace(typed) == "" {
			return 0, false
		}
		if i, errParse := strconv.ParseInt(strings.TrimSpace(typed), 10, 64); errParse == nil {
			return i, true
		}
	}
	return 0, false
}

func buildBaseURL(auth *cliproxyauth.Auth) string {
	if baseURLs := antigravityBaseURLFallbackOrder(auth); len(baseURLs) > 0 {
		return baseURLs[0]
	}
	return antigravityBaseURLDaily
}

func resolveHost(base string) string {
	parsed, errParse := url.Parse(base)
	if errParse != nil {
		return ""
	}
	if parsed.Host != "" {
		return parsed.Host
	}
	return strings.TrimPrefix(strings.TrimPrefix(base, "https://"), "http://")
}

func resolveUserAgent(auth *cliproxyauth.Auth) string {
	if auth != nil {
		if auth.Attributes != nil {
			if ua := strings.TrimSpace(auth.Attributes["user_agent"]); ua != "" {
				return ua
			}
		}
		if auth.Metadata != nil {
			if ua, ok := auth.Metadata["user_agent"].(string); ok && strings.TrimSpace(ua) != "" {
				return strings.TrimSpace(ua)
			}
		}
	}
	return defaultAntigravityAgent
}

func antigravityRetryAttempts(auth *cliproxyauth.Auth, cfg *config.Config) int {
	retry := 0
	if cfg != nil {
		retry = cfg.RequestRetry
	}
	if auth != nil {
		if override, ok := auth.RequestRetryOverride(); ok {
			retry = override
		}
	}
	if retry < 0 {
		retry = 0
	}
	attempts := retry + 1
	if attempts < 1 {
		return 1
	}
	return attempts
}

func antigravityShouldRetryNoCapacity(statusCode int, body []byte) bool {
	if statusCode != http.StatusServiceUnavailable {
		return false
	}
	if len(body) == 0 {
		return false
	}
	msg := strings.ToLower(string(body))
	return strings.Contains(msg, "no capacity available")
}

func antigravityWait(ctx context.Context, wait time.Duration) error {
	if wait <= 0 {
		return nil
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func antigravityBaseURLFallbackOrder(auth *cliproxyauth.Auth) []string {
	if base := resolveCustomAntigravityBaseURL(auth); base != "" {
		return []string{base}
	}
	return []string{
		antigravityBaseURLDaily,
		antigravitySandboxBaseURLDaily,
		// antigravityBaseURLProd,
	}
}

func resolveCustomAntigravityBaseURL(auth *cliproxyauth.Auth) string {
	if auth == nil {
		return ""
	}
	if auth.Attributes != nil {
		if v := strings.TrimSpace(auth.Attributes["base_url"]); v != "" {
			return strings.TrimSuffix(v, "/")
		}
	}
	if auth.Metadata != nil {
		if v, ok := auth.Metadata["base_url"].(string); ok {
			v = strings.TrimSpace(v)
			if v != "" {
				return strings.TrimSuffix(v, "/")
			}
		}
	}
	return ""
}

// resolveRequestType maps a model name to the Antigravity requestType.
// Mapping derived from real Antigravity client traffic analysis.
func resolveRequestType(modelName string) string {
	agentModels := []string{
		"gemini-3.1-pro-high", "gemini-3.1-pro-low", "gemini-3-flash",
		"claude-sonnet-4-6", "claude-opus-4-6-thinking", "gpt-oss-120b-medium",
	}
	for _, m := range agentModels {
		if modelName == m {
			return "agent"
		}
	}
	if modelName == "gemini-3-pro-image" {
		return "image_gen"
	}
	if modelName == "gemini-2.5-flash-lite" || modelName == "gemini-2.5-flash" {
		return "checkpoint"
	}
	// Default to agent for unknown models (e.g. future model variants)
	log.Warnf("resolveRequestType: unknown model %q, defaulting to agent", modelName)
	return "agent"
}
func geminiToAntigravity(modelName string, payload []byte, projectID string) []byte {
	reqType := resolveRequestType(modelName)
	template, _ := sjson.Set(string(payload), "model", modelName)
	template, _ = sjson.Set(template, "userAgent", "antigravity")
	template, _ = sjson.Set(template, "requestType", reqType)
	// Use real project ID from auth if available, otherwise generate random (legacy fallback)
	if projectID != "" {
		template, _ = sjson.Set(template, "project", projectID)
	} else {
		template, _ = sjson.Set(template, "project", generateProjectID())
	}

	sessionID := generateStableSessionID(payload)
	template, _ = sjson.Set(template, "requestId", generateRequestIDForType(reqType, payload, sessionID))

	// image_gen requests do not include sessionId per real client behavior
	if reqType != "image_gen" {
		template, _ = sjson.Set(template, "request.sessionId", sessionID)
	}
	template, _ = sjson.Delete(template, "request.safetySettings")
	if toolConfig := gjson.Get(template, "toolConfig"); toolConfig.Exists() && !gjson.Get(template, "request.toolConfig").Exists() {
		template, _ = sjson.SetRaw(template, "request.toolConfig", toolConfig.Raw)
		template, _ = sjson.Delete(template, "toolConfig")
	}
	return []byte(template)
}

// geminiToAntigravityWebSearch wraps a web search payload for Antigravity.
// Web search requests have no requestId, no userAgent, and no sessionId per real client behavior.
func geminiToAntigravityWebSearch(payload []byte, projectID string) []byte {
	template := string(payload)
	if projectID != "" {
		template, _ = sjson.Set(template, "project", projectID)
	} else {
		template, _ = sjson.Set(template, "project", generateProjectID())
	}
	template, _ = sjson.Delete(template, "request.safetySettings")
	return []byte(template)
}

// generateRequestIDForType dispatches to the correct requestId format based on requestType.
func generateRequestIDForType(reqType string, payload []byte, sessionID string) string {
	switch reqType {
	case "checkpoint":
		return "checkpoint/" + uuid.NewString()
	case "agent", "image_gen", "browser_subagent":
		return generateTimestampRequestID(reqType, payload, sessionID)
	default:
		return generateTimestampRequestID("agent", payload, sessionID)
	}
}

// generateTimestampRequestID creates a requestId in the format: {prefix}/{unix_ms}/{conversation_uuid}/{step_counter}
func generateTimestampRequestID(prefix string, payload []byte, sessionID string) string {
	ts := time.Now().UnixMilli()
	convUUID := deriveConversationUUID(sessionID)

	lastStep := parseLastStepID(payload)
	var counter int
	if lastStep > 0 {
		counter = lastStep + 2
	} else if hasModelContent(payload) {
		// Has model responses but no step IDs (proxy-generated content)
		counter = 1
	} else {
		// First message, no prior conversation
		counter = 2
	}

	return fmt.Sprintf("%s/%d/%s/%d", prefix, ts, convUUID, counter)
}

// parseLastStepID extracts the maximum Step Id from request contents.
func parseLastStepID(payload []byte) int {
	contents := gjson.GetBytes(payload, "request.contents")
	if !contents.IsArray() {
		return 0
	}
	maxStep := 0
	for _, content := range contents.Array() {
		if content.Get("role").String() != "model" {
			continue
		}
		parts := content.Get("parts")
		if !parts.IsArray() {
			continue
		}
		for _, part := range parts.Array() {
			text := part.Get("text").String()
			if idx := strings.Index(text, "Step Id: "); idx >= 0 {
				numStr := text[idx+9:]
				if endIdx := strings.IndexAny(numStr, " \n\r\t"); endIdx > 0 {
					numStr = numStr[:endIdx]
				}
				if n, err := strconv.Atoi(numStr); err == nil && n > maxStep {
					maxStep = n
				}
			}
		}
	}
	return maxStep
}

// hasModelContent checks whether the request payload contains any model-role entries.
func hasModelContent(payload []byte) bool {
	contents := gjson.GetBytes(payload, "request.contents")
	if !contents.IsArray() {
		return false
	}
	for _, content := range contents.Array() {
		if content.Get("role").String() == "model" {
			return true
		}
	}
	return false
}

// deriveConversationUUID generates a stable UUID v4 from a sessionId hash,
// ensuring the same conversation always produces the same conversation UUID.
func deriveConversationUUID(sessionID string) string {
	h := sha256.Sum256([]byte(sessionID))
	// Set UUID version 4 bits
	h[6] = (h[6] & 0x0f) | 0x40
	// Set UUID variant bits
	h[8] = (h[8] & 0x3f) | 0x80
	u, err := uuid.FromBytes(h[:16])
	if err != nil {
		return uuid.NewString()
	}
	return u.String()
}

func generateSessionID() string {
	randSourceMutex.Lock()
	n := randSource.Int63n(9_000_000_000_000_000_000)
	randSourceMutex.Unlock()
	return "-" + strconv.FormatInt(n, 10)
}

func generateStableSessionID(payload []byte) string {
	contents := gjson.GetBytes(payload, "request.contents")
	if contents.IsArray() {
		for _, content := range contents.Array() {
			if content.Get("role").String() == "user" {
				text := content.Get("parts.0.text").String()
				if text != "" {
					h := sha256.Sum256([]byte(text))
					n := int64(binary.BigEndian.Uint64(h[:8])) & 0x7FFFFFFFFFFFFFFF
					return "-" + strconv.FormatInt(n, 10)
				}
			}
		}
	}
	return generateSessionID()
}

func generateProjectID() string {
	adjectives := []string{"useful", "bright", "swift", "calm", "bold"}
	nouns := []string{"fuze", "wave", "spark", "flow", "core"}
	randSourceMutex.Lock()
	adj := adjectives[randSource.Intn(len(adjectives))]
	noun := nouns[randSource.Intn(len(nouns))]
	randSourceMutex.Unlock()
	randomPart := strings.ToLower(uuid.NewString())[:5]
	return adj + "-" + noun + "-" + randomPart
}

func doWebSearchTool(payload []byte) bool {
	tools := gjson.GetBytes(payload, "tools")
	if !tools.IsArray() {
		return false
	}
	for _, tool := range tools.Array() {
		toolType := tool.Get("type").String()
		if strings.HasPrefix(toolType, "web_search") {
			return true
		}
	}
	return false
}

func extractUserQuery(payload []byte) string {
	messages := gjson.GetBytes(payload, "messages")
	if !messages.IsArray() {
		return ""
	}
	arr := messages.Array()
	for i := len(arr) - 1; i >= 0; i-- {
		msg := arr[i]
		if msg.Get("role").String() == "user" {
			content := msg.Get("content")
			if content.Type == gjson.String {
				return content.String()
			}
			if content.IsArray() {
				for _, item := range content.Array() {
					if item.Get("type").String() == "text" {
						return item.Get("text").String()
					}
				}
			}
		}
	}
	return ""
}

func (e *AntigravityExecutor) executeGeminiWebSearch(ctx context.Context, auth *cliproxyauth.Auth, token, query string) ([]byte, error) {
	if query == "" {
		return nil, fmt.Errorf("empty query")
	}

	geminiPayload := `{"model":"","request":{"contents":[],"tools":[{"googleSearch":{}}],"generationConfig":{"candidateCount":1}},"requestType":"web_search"}`
	geminiPayload, _ = sjson.Set(geminiPayload, "model", webSearchGeminiModel)
	geminiPayload, _ = sjson.Set(geminiPayload, "request.contents.0.role", "user")
	geminiPayload, _ = sjson.Set(geminiPayload, "request.contents.0.parts.0.text", query)
	geminiPayload, _ = sjson.Set(geminiPayload, "request.systemInstruction.role", "user")
	geminiPayload, _ = sjson.Set(geminiPayload, "request.systemInstruction.parts.0.text", "You are a search engine bot. You will be given a query from a user. Your task is to search the web for relevant information that will help the user. You MUST perform a web search. Do not respond or interact with the user, please respond as if they typed the query into a search bar.")

	projectID := ""
	if auth != nil && auth.Metadata != nil {
		if pid, ok := auth.Metadata["project_id"].(string); ok {
			projectID = strings.TrimSpace(pid)
		}
	}
	geminiPayload = string(geminiToAntigravityWebSearch([]byte(geminiPayload), projectID))

	baseURLs := antigravityBaseURLFallbackOrder(auth)
	httpClient := newProxyAwareHTTPClient(ctx, e.cfg, auth, 0)

	for _, baseURL := range baseURLs {
		base := strings.TrimSuffix(baseURL, "/")
		requestURL := base + antigravityGeneratePath

		httpReq, errReq := http.NewRequestWithContext(ctx, http.MethodPost, requestURL, bytes.NewReader([]byte(geminiPayload)))
		if errReq != nil {
			continue
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Authorization", "Bearer "+token)
		httpReq.Header.Set("User-Agent", resolveUserAgent(auth))
		httpReq.Header.Set("Accept", "application/json")
		if host := resolveHost(base); host != "" {
			httpReq.Host = host
		}

		httpResp, errDo := httpClient.Do(httpReq)
		if errDo != nil {
			log.Debugf("antigravity web search: request failed: %v", errDo)
			continue
		}

		bodyBytes, errRead := io.ReadAll(httpResp.Body)
		_ = httpResp.Body.Close()
		if errRead != nil {
			continue
		}

		if httpResp.StatusCode < http.StatusOK || httpResp.StatusCode >= http.StatusMultipleChoices {
			log.Debugf("antigravity web search: upstream error status: %d", httpResp.StatusCode)
			continue
		}

		log.Debugf("antigravity web search: got response for query: %s", query)
		return bodyBytes, nil
	}

	return nil, fmt.Errorf("web search failed")
}

func (e *AntigravityExecutor) executeWebSearchOnly(ctx context.Context, auth *cliproxyauth.Auth, token string, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	reporter := newUsageReporter(ctx, e.Identifier(), req.Model, auth)

	query := extractUserQuery(req.Payload)
	if query == "" {
		reporter.publishFailure(ctx)
		return cliproxyexecutor.Response{}, fmt.Errorf("no user query found for web search")
	}

	geminiResp, err := e.executeGeminiWebSearch(ctx, auth, token, query)
	if err != nil {
		reporter.publishFailure(ctx)
		return cliproxyexecutor.Response{}, err
	}

	reporter.publish(ctx, parseAntigravityUsage(geminiResp))
	claudeResp := convertGeminiToClaudeNonStream(req.Model, geminiResp)
	reporter.ensurePublished(ctx)

	return cliproxyexecutor.Response{Payload: []byte(claudeResp)}, nil
}

func (e *AntigravityExecutor) executeWebSearchOnlyStream(ctx context.Context, auth *cliproxyauth.Auth, token string, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (<-chan cliproxyexecutor.StreamChunk, error) {
	reporter := newUsageReporter(ctx, e.Identifier(), req.Model, auth)

	query := extractUserQuery(req.Payload)
	if query == "" {
		reporter.publishFailure(ctx)
		return nil, fmt.Errorf("no user query found for web search")
	}

	geminiResp, err := e.executeGeminiWebSearch(ctx, auth, token, query)
	if err != nil {
		reporter.publishFailure(ctx)
		return nil, err
	}

	reporter.publish(ctx, parseAntigravityUsage(geminiResp))

	out := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(out)
		defer reporter.ensurePublished(ctx)

		sseEvents := convertGeminiToClaudeSSEStream(req.Model, geminiResp)
		for _, event := range sseEvents {
			select {
			case <-ctx.Done():
				return
			case out <- cliproxyexecutor.StreamChunk{Payload: []byte(event)}:
			}
		}
	}()

	return out, nil
}

// groundingSupport represents a citation segment from Gemini's groundingSupports
type groundingSupport struct {
	StartIndex int64
	EndIndex   int64
	Text       string
	ChunkURLs  []string
	ChunkTitle string
}

// parseGroundingSupports extracts fine-grained citation data from Gemini response
func parseGroundingSupports(groundingMetadata gjson.Result) []groundingSupport {
	var supports []groundingSupport

	groundingChunks := groundingMetadata.Get("groundingChunks")
	if !groundingChunks.IsArray() {
		return supports
	}

	// Build chunk index -> (url, title) mapping
	chunks := groundingChunks.Array()
	chunkData := make([]struct {
		URL   string
		Title string
	}, len(chunks))

	for i, chunk := range chunks {
		web := chunk.Get("web")
		if web.Exists() {
			chunkData[i].URL = web.Get("uri").String()
			chunkData[i].Title = web.Get("title").String()
		}
	}

	// Parse groundingSupports
	groundingSupportsArr := groundingMetadata.Get("groundingSupports")
	if !groundingSupportsArr.IsArray() {
		return supports
	}

	for _, support := range groundingSupportsArr.Array() {
		segment := support.Get("segment")
		if !segment.Exists() {
			continue
		}

		gs := groundingSupport{
			StartIndex: segment.Get("startIndex").Int(),
			EndIndex:   segment.Get("endIndex").Int(),
			Text:       segment.Get("text").String(),
		}

		// Get referenced chunk URLs
		chunkIndices := support.Get("groundingChunkIndices")
		if chunkIndices.IsArray() {
			for _, idx := range chunkIndices.Array() {
				i := int(idx.Int())
				if i >= 0 && i < len(chunkData) {
					gs.ChunkURLs = append(gs.ChunkURLs, chunkData[i].URL)
					if gs.ChunkTitle == "" {
						gs.ChunkTitle = chunkData[i].Title
					}
				}
			}
		}

		supports = append(supports, gs)
	}

	return supports
}

// buildCitedTextBlocks splits text into blocks based on groundingSupports
// Returns: slice of (text, citations) pairs where citations may be nil for non-cited text
// NOTE: Gemini's startIndex/endIndex are BYTE indices, not rune indices
func buildCitedTextBlocks(textContent string, supports []groundingSupport) []struct {
	Text      string
	Citations []map[string]interface{}
} {
	var blocks []struct {
		Text      string
		Citations []map[string]interface{}
	}

	if len(supports) == 0 {
		// No citations, return single text block
		if textContent != "" {
			blocks = append(blocks, struct {
				Text      string
				Citations []map[string]interface{}
			}{Text: textContent, Citations: nil})
		}
		return blocks
	}

	textBytes := []byte(textContent)
	lastEnd := int64(0)

	for _, s := range supports {
		// Add non-cited text before this citation
		if s.StartIndex > lastEnd {
			start := int(lastEnd)
			end := int(s.StartIndex)
			if end > len(textBytes) {
				end = len(textBytes)
			}
			if start < end {
				blocks = append(blocks, struct {
					Text      string
					Citations []map[string]interface{}
				}{Text: string(textBytes[start:end]), Citations: nil})
			}
		}

		// Add cited text block
		if s.Text != "" && len(s.ChunkURLs) > 0 {
			citation := map[string]interface{}{
				"type":       "web_search_result_location",
				"cited_text": s.Text,
				"url":        s.ChunkURLs[0], // Use first URL
				"title":      s.ChunkTitle,
			}
			blocks = append(blocks, struct {
				Text      string
				Citations []map[string]interface{}
			}{Text: s.Text, Citations: []map[string]interface{}{citation}})
		}

		if s.EndIndex > lastEnd {
			lastEnd = s.EndIndex
		}
	}

	// Add remaining non-cited text
	if int(lastEnd) < len(textBytes) {
		blocks = append(blocks, struct {
			Text      string
			Citations []map[string]interface{}
		}{Text: string(textBytes[lastEnd:]), Citations: nil})
	}

	return blocks
}

func convertGeminiToClaudeNonStream(model string, geminiResp []byte) string {
	textContent := ""
	if parts := gjson.GetBytes(geminiResp, "response.candidates.0.content.parts"); parts.IsArray() {
		for _, part := range parts.Array() {
			if text := part.Get("text"); text.Exists() {
				textContent += text.String()
			}
		}
	} else if parts := gjson.GetBytes(geminiResp, "candidates.0.content.parts"); parts.IsArray() {
		for _, part := range parts.Array() {
			if text := part.Get("text"); text.Exists() {
				textContent += text.String()
			}
		}
	}

	groundingMetadata := gjson.GetBytes(geminiResp, "response.candidates.0.groundingMetadata")
	if !groundingMetadata.Exists() {
		groundingMetadata = gjson.GetBytes(geminiResp, "candidates.0.groundingMetadata")
	}

	inputTokens := gjson.GetBytes(geminiResp, "response.usageMetadata.promptTokenCount").Int()
	if inputTokens == 0 {
		inputTokens = gjson.GetBytes(geminiResp, "usageMetadata.promptTokenCount").Int()
	}
	outputTokens := gjson.GetBytes(geminiResp, "response.usageMetadata.candidatesTokenCount").Int()
	if outputTokens == 0 {
		outputTokens = gjson.GetBytes(geminiResp, "usageMetadata.candidatesTokenCount").Int()
	}

	msgID := fmt.Sprintf("msg_%s", uuid.New().String()[:24])
	toolUseID := fmt.Sprintf("srvtoolu_%d", time.Now().UnixNano())

	searchQuery := ""
	if queries := groundingMetadata.Get("webSearchQueries"); queries.IsArray() && len(queries.Array()) > 0 {
		searchQuery = queries.Array()[0].String()
	}

	content := []map[string]interface{}{}

	serverToolUse := map[string]interface{}{
		"type":  "server_tool_use",
		"id":    toolUseID,
		"name":  "web_search",
		"input": map[string]interface{}{"query": searchQuery},
	}
	content = append(content, serverToolUse)

	webSearchResults := []map[string]interface{}{}
	groundingChunks := groundingMetadata.Get("groundingChunks")
	if groundingChunks.IsArray() {
		for _, chunk := range groundingChunks.Array() {
			web := chunk.Get("web")
			if web.Exists() {
				result := map[string]interface{}{
					"type":     "web_search_result",
					"page_age": nil,
				}
				if title := web.Get("title"); title.Exists() {
					result["title"] = title.String()
				}
				if uri := web.Get("uri"); uri.Exists() {
					result["url"] = uri.String()
				}
				webSearchResults = append(webSearchResults, result)
			}
		}
	}
	webSearchToolResult := map[string]interface{}{
		"type":        "web_search_tool_result",
		"tool_use_id": toolUseID,
		"content":     webSearchResults,
	}
	content = append(content, webSearchToolResult)

	supports := parseGroundingSupports(groundingMetadata)
	textBlocks := buildCitedTextBlocks(textContent, supports)

	for _, block := range textBlocks {
		textBlock := map[string]interface{}{
			"type": "text",
			"text": block.Text,
		}
		if block.Citations != nil {
			textBlock["citations"] = block.Citations
		}
		content = append(content, textBlock)
	}

	response := map[string]interface{}{
		"id":            msgID,
		"type":          "message",
		"role":          "assistant",
		"content":       content,
		"model":         model,
		"stop_reason":   "end_turn",
		"stop_sequence": nil,
		"usage": map[string]interface{}{
			"input_tokens":  inputTokens,
			"output_tokens": outputTokens,
			"server_tool_use": map[string]interface{}{
				"web_search_requests": 1,
			},
		},
	}

	respJSON, _ := json.Marshal(response)
	return string(respJSON)
}

func convertGeminiToClaudeSSEStream(model string, geminiResp []byte) []string {
	var events []string

	textContent := ""
	if parts := gjson.GetBytes(geminiResp, "response.candidates.0.content.parts"); parts.IsArray() {
		for _, part := range parts.Array() {
			if text := part.Get("text"); text.Exists() {
				textContent += text.String()
			}
		}
	} else if parts := gjson.GetBytes(geminiResp, "candidates.0.content.parts"); parts.IsArray() {
		for _, part := range parts.Array() {
			if text := part.Get("text"); text.Exists() {
				textContent += text.String()
			}
		}
	}

	groundingMetadata := gjson.GetBytes(geminiResp, "response.candidates.0.groundingMetadata")
	if !groundingMetadata.Exists() {
		groundingMetadata = gjson.GetBytes(geminiResp, "candidates.0.groundingMetadata")
	}

	inputTokens := gjson.GetBytes(geminiResp, "response.usageMetadata.promptTokenCount").Int()
	if inputTokens == 0 {
		inputTokens = gjson.GetBytes(geminiResp, "usageMetadata.promptTokenCount").Int()
	}
	outputTokens := gjson.GetBytes(geminiResp, "response.usageMetadata.candidatesTokenCount").Int()
	if outputTokens == 0 {
		outputTokens = gjson.GetBytes(geminiResp, "usageMetadata.candidatesTokenCount").Int()
	}

	msgID := fmt.Sprintf("msg_%s", uuid.New().String()[:24])
	toolUseID := fmt.Sprintf("srvtoolu_%d", time.Now().UnixNano())

	searchQuery := ""
	if queries := groundingMetadata.Get("webSearchQueries"); queries.IsArray() && len(queries.Array()) > 0 {
		searchQuery = queries.Array()[0].String()
	}

	messageStart := fmt.Sprintf(`{"type":"message_start","message":{"id":"%s","type":"message","role":"assistant","content":[],"model":"%s","stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":%d,"output_tokens":0}}}`,
		msgID, model, inputTokens)
	events = append(events, "event: message_start\ndata: "+messageStart+"\n\n")

	contentIndex := 0

	serverToolUseStart := fmt.Sprintf(`{"type":"content_block_start","index":%d,"content_block":{"type":"server_tool_use","id":"%s","name":"web_search","input":{}}}`,
		contentIndex, toolUseID)
	events = append(events, "event: content_block_start\ndata: "+serverToolUseStart+"\n\n")

	if searchQuery != "" {
		queryJSON, _ := sjson.Set(`{}`, "query", searchQuery)
		inputDelta := fmt.Sprintf(`{"type":"content_block_delta","index":%d,"delta":{"type":"input_json_delta","partial_json":""}}`, contentIndex)
		inputDelta, _ = sjson.Set(inputDelta, "delta.partial_json", queryJSON)
		events = append(events, "event: content_block_delta\ndata: "+inputDelta+"\n\n")
	}

	events = append(events, fmt.Sprintf("event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":%d}\n\n", contentIndex))
	contentIndex++

	webSearchResults := "[]"
	groundingChunks := groundingMetadata.Get("groundingChunks")
	if groundingChunks.IsArray() {
		for _, chunk := range groundingChunks.Array() {
			web := chunk.Get("web")
			if web.Exists() {
				result := `{"type":"web_search_result"}`
				if title := web.Get("title"); title.Exists() {
					result, _ = sjson.Set(result, "title", title.String())
				}
				if uri := web.Get("uri"); uri.Exists() {
					result, _ = sjson.Set(result, "url", uri.String())
				}
				result, _ = sjson.Set(result, "page_age", nil)
				webSearchResults, _ = sjson.SetRaw(webSearchResults, "-1", result)
			}
		}
	}

	webSearchToolResultStart := fmt.Sprintf(`{"type":"content_block_start","index":%d,"content_block":{"type":"web_search_tool_result","tool_use_id":"%s","content":[]}}`,
		contentIndex, toolUseID)
	webSearchToolResultStart, _ = sjson.SetRaw(webSearchToolResultStart, "content_block.content", webSearchResults)
	events = append(events, "event: content_block_start\ndata: "+webSearchToolResultStart+"\n\n")
	events = append(events, fmt.Sprintf("event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":%d}\n\n", contentIndex))
	contentIndex++

	supports := parseGroundingSupports(groundingMetadata)
	textBlocks := buildCitedTextBlocks(textContent, supports)

	for _, block := range textBlocks {
		if block.Text == "" {
			continue
		}

		if block.Citations != nil {
			textBlockStart := fmt.Sprintf(`{"type":"content_block_start","index":%d,"content_block":{"citations":[],"type":"text","text":""}}`, contentIndex)
			events = append(events, "event: content_block_start\ndata: "+textBlockStart+"\n\n")

			for _, citation := range block.Citations {
				citationJSON, _ := json.Marshal(citation)
				citationDelta := fmt.Sprintf(`{"type":"content_block_delta","index":%d,"delta":{"type":"citations_delta","citation":%s}}`,
					contentIndex, string(citationJSON))
				events = append(events, "event: content_block_delta\ndata: "+citationDelta+"\n\n")
			}
		} else {
			textBlockStart := fmt.Sprintf(`{"type":"content_block_start","index":%d,"content_block":{"type":"text","text":""}}`, contentIndex)
			events = append(events, "event: content_block_start\ndata: "+textBlockStart+"\n\n")
		}

		runes := []rune(block.Text)
		chunkSize := 50
		for i := 0; i < len(runes); i += chunkSize {
			end := i + chunkSize
			if end > len(runes) {
				end = len(runes)
			}
			chunk := string(runes[i:end])
			textDelta := fmt.Sprintf(`{"type":"content_block_delta","index":%d,"delta":{"type":"text_delta","text":""}}`, contentIndex)
			textDelta, _ = sjson.Set(textDelta, "delta.text", chunk)
			events = append(events, "event: content_block_delta\ndata: "+textDelta+"\n\n")
		}

		events = append(events, fmt.Sprintf("event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":%d}\n\n", contentIndex))
		contentIndex++
	}

	messageDelta := fmt.Sprintf(`{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"input_tokens":%d,"output_tokens":%d,"server_tool_use":{"web_search_requests":1}}}`,
		inputTokens, outputTokens)
	events = append(events, "event: message_delta\ndata: "+messageDelta+"\n\n")

	events = append(events, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")

	return events
}
