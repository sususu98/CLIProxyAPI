package executor

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/cache"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/thinking"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	wsMaxRounds   = 4
	wsMaxSearches = 6
	wsTimeout     = 45 * time.Second
)

type webSearchOrchestrator struct {
	executor  *AntigravityExecutor
	ctx       context.Context
	cancel    context.CancelFunc
	auth      *cliproxyauth.Auth
	token     string
	model     string
	baseModel string
	cfg       *config.Config
	opts      cliproxyexecutor.Options

	out          chan cliproxyexecutor.StreamChunk
	contentIndex int
	msgID        string

	totalInputTokens  int64
	totalOutputTokens int64
	searchCount       int
	roundCount        int
}

type wsRoundResult struct {
	ThinkingTexts []string
	TextParts     []string
	FunctionCalls []wsFunctionCall
	FinishReason  string
	InputTokens   int64
	OutputTokens  int64
	RawModelParts string
}

type wsFunctionCall struct {
	Name  string
	Query string
}

type wsSearchResult struct {
	Title   string
	URL     string
	PageAge *string
}

func (e *AntigravityExecutor) orchestrateWebSearchStream(ctx context.Context, auth *cliproxyauth.Auth, token string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	orchestratorCtx, cancel := context.WithTimeout(ctx, wsTimeout)
	o := &webSearchOrchestrator{
		executor:  e,
		ctx:       orchestratorCtx,
		cancel:    cancel,
		auth:      auth,
		token:     token,
		model:     req.Model,
		baseModel: thinking.ParseSuffix(req.Model).ModelName,
		cfg:       e.cfg,
		opts:      opts,
		out:       make(chan cliproxyexecutor.StreamChunk),
		msgID:     fmt.Sprintf("msg_%s", uuid.New().String()[:24]),
	}

	go o.run(req.Payload)
	return &cliproxyexecutor.StreamResult{Chunks: o.out}, nil
}

func replaceWebSearchWithFunction(payload []byte) []byte {
	tools := gjson.GetBytes(payload, "tools")
	if !tools.IsArray() {
		return payload
	}

	newTools := "[]"
	for _, tool := range tools.Array() {
		current := tool.Raw
		if strings.HasPrefix(tool.Get("type").String(), "web_search") {
			current = `{"name":"web_search","description":"Search the web for current information. Use this when you need real-time data from the internet. Always call this tool when the user's question could benefit from up-to-date web information.","input_schema":{"type":"object","properties":{"query":{"type":"string","description":"The search query"}},"required":["query"]}}`
		}
		updated, errSet := sjson.SetRaw(newTools, "-1", current)
		if errSet == nil {
			newTools = updated
		}
	}

	out, errSet := sjson.SetRawBytes(payload, "tools", []byte(newTools))
	if errSet != nil {
		return payload
	}
	return out
}

func (o *webSearchOrchestrator) run(payload []byte) {
	defer close(o.out)
	defer o.cancel()

	modifiedPayload := replaceWebSearchWithFunction(payload)

	from := o.opts.SourceFormat
	to := sdktranslator.FromString("antigravity")

	originalPayloadSource := payload
	if len(o.opts.OriginalRequest) > 0 {
		originalPayloadSource = o.opts.OriginalRequest
	}
	originalModifiedPayload := replaceWebSearchWithFunction(originalPayloadSource)
	originalTranslated := sdktranslator.TranslateRequest(from, to, o.baseModel, originalModifiedPayload, true)
	translated := sdktranslator.TranslateRequest(from, to, o.baseModel, modifiedPayload, true)

	var err error
	translated, err = thinking.ApplyThinking(translated, o.model, from.String(), to.String(), o.executor.Identifier())
	if err != nil {
		o.emitError(err)
		return
	}

	requestedModel := payloadRequestedModel(o.opts, o.model)
	translated = applyPayloadConfigWithRoot(o.cfg, o.baseModel, "antigravity", "request", translated, originalTranslated, requestedModel)

	o.emitMessageStart()

	for o.roundCount = 0; o.roundCount < wsMaxRounds; o.roundCount++ {
		result, roundErr := o.executeClaudeRound(translated)
		if roundErr != nil {
			o.emitError(roundErr)
			return
		}

		o.totalInputTokens += result.InputTokens
		o.totalOutputTokens += result.OutputTokens

		if len(result.FunctionCalls) == 0 || o.searchCount >= wsMaxSearches {
			for _, text := range result.TextParts {
				if strings.TrimSpace(text) == "" {
					continue
				}
				idx := o.emitTextStart()
				o.emitTextDelta(idx, text)
				o.emitBlockStop(idx)
			}
			o.emitMessageDelta(mapFinishReason(result.FinishReason))
			o.emitMessageStop()
			return
		}

		var searchResults [][]wsSearchResult
		for _, call := range result.FunctionCalls {
			if o.searchCount >= wsMaxSearches {
				break
			}
			if call.Name != "web_search" {
				continue
			}

			toolUseID := fmt.Sprintf("srvtoolu_%d", time.Now().UnixNano())
			o.emitServerToolUse(toolUseID, call.Query)

			geminiResp, searchErr := o.executor.executeGeminiWebSearch(o.ctx, o.auth, o.token, call.Query)
			if searchErr != nil {
				log.WithError(searchErr).Warn("antigravity web search orchestrator: gemini web search failed")
				o.emitWebSearchToolResult(toolUseID, nil)
				o.searchCount++
				searchResults = append(searchResults, nil)
				continue
			}

			geminiResp = resolveGeminiResponseURLs(o.ctx, o.cfg, o.auth, geminiResp)
			results := extractSearchResults(geminiResp)
			o.emitWebSearchToolResult(toolUseID, results)
			o.searchCount++
			searchResults = append(searchResults, results)
		}

		translated = o.buildContinuation(translated, result, searchResults)
	}

	o.emitMessageDelta("end_turn")
	o.emitMessageStop()
}

func (o *webSearchOrchestrator) executeClaudeRound(payload []byte) (*wsRoundResult, error) {
	baseURLs := antigravityBaseURLFallbackOrder(o.auth)
	httpClient := newProxyAwareHTTPClient(o.ctx, o.cfg, o.auth, 0)
	allowThinking := supportsClaudeThinking(o.model, o.baseModel)

	var lastErr error
	for _, baseURL := range baseURLs {
		httpReq, errReq := o.executor.buildRequest(o.ctx, o.auth, o.token, o.baseModel, payload, true, "", baseURL)
		if errReq != nil {
			return nil, errReq
		}

		httpResp, errDo := httpClient.Do(httpReq)
		if errDo != nil {
			recordAPIResponseError(o.ctx, o.cfg, errDo)
			lastErr = errDo
			continue
		}

		recordAPIResponseMetadata(o.ctx, o.cfg, httpResp.StatusCode, httpResp.Header.Clone())
		if httpResp.StatusCode < http.StatusOK || httpResp.StatusCode >= http.StatusMultipleChoices {
			_ = httpResp.Body.Close()
			lastErr = statusErr{code: httpResp.StatusCode, msg: "antigravity web search stream upstream error"}
			continue
		}

		result := &wsRoundResult{}
		var rawParts []string
		scanner := bufio.NewScanner(httpResp.Body)
		scanner.Buffer(nil, streamScannerBuffer)

		thinkingOpen := false
		thinkingIndex := -1
		var pendingSignature string
		emittedAny := false

		for scanner.Scan() {
			line := bytes.Clone(scanner.Bytes())
			appendAPIResponseChunk(o.ctx, o.cfg, line)
			line = FilterSSEUsageMetadata(line)

			chunkPayload := jsonPayload(line)
			if chunkPayload == nil {
				continue
			}

			parts := gjson.GetBytes(chunkPayload, "response.candidates.0.content.parts")
			if !parts.IsArray() {
				parts = gjson.GetBytes(chunkPayload, "candidates.0.content.parts")
			}

			if parts.IsArray() {
				for _, part := range parts.Array() {
					rawParts = append(rawParts, part.Raw)

					isThought := part.Get("thought").Bool()
					text := part.Get("text").String()

					if isThought {
						// Check for thoughtSignature (signals end of thinking block)
						sig := part.Get("thoughtSignature").String()
						if sig == "" {
							sig = part.Get("thought_signature").String()
						}
						if sig != "" {
							pendingSignature = sig
						}
						if text != "" {
							result.ThinkingTexts = append(result.ThinkingTexts, text)
							if allowThinking {
								if !thinkingOpen {
									thinkingIndex = o.emitThinkingStart()
									thinkingOpen = true
									emittedAny = true
								}
								o.emitThinkingDelta(thinkingIndex, text)
							}
						}
						continue
					}

					if thinkingOpen {
						if pendingSignature != "" {
							o.emitSignatureDelta(thinkingIndex, pendingSignature)
							pendingSignature = ""
						}
						o.emitBlockStop(thinkingIndex)
						thinkingOpen = false
					}

					if fnCall := part.Get("functionCall"); fnCall.Exists() {
						name := fnCall.Get("name").String()
						query := fnCall.Get("args.query").String()
						if name == "web_search" {
							result.FunctionCalls = append(result.FunctionCalls, wsFunctionCall{Name: name, Query: query})
						}
						continue
					}

					if text != "" {
						result.TextParts = append(result.TextParts, text)
					}
				}
			}

			finishReason := gjson.GetBytes(chunkPayload, "response.candidates.0.finishReason")
			if !finishReason.Exists() {
				finishReason = gjson.GetBytes(chunkPayload, "candidates.0.finishReason")
			}
			if finishReason.Exists() {
				result.FinishReason = finishReason.String()
			}

			usage := gjson.GetBytes(chunkPayload, "response.usageMetadata")
			if !usage.Exists() {
				usage = gjson.GetBytes(chunkPayload, "usageMetadata")
			}
			if usage.Exists() {
				if input := usage.Get("promptTokenCount").Int(); input > 0 {
					result.InputTokens = input
				}
				if output := usage.Get("candidatesTokenCount").Int(); output > 0 {
					result.OutputTokens = output
				}
			}
		}

		if thinkingOpen {
			if pendingSignature != "" {
				o.emitSignatureDelta(thinkingIndex, pendingSignature)
			}
			o.emitBlockStop(thinkingIndex)
		}

		if errClose := httpResp.Body.Close(); errClose != nil {
			log.WithError(errClose).Error("antigravity web search orchestrator: close response body error")
		}

		if errScan := scanner.Err(); errScan != nil {
			recordAPIResponseError(o.ctx, o.cfg, errScan)
			if emittedAny {
				// Already sent SSE events to client; cannot retry on another baseURL
				return nil, errScan
			}
			lastErr = errScan
			continue
		}

		if len(rawParts) == 0 {
			result.RawModelParts = "[]"
		} else {
			var buf bytes.Buffer
			buf.WriteByte('[')
			for i, raw := range rawParts {
				if i > 0 {
					buf.WriteByte(',')
				}
				buf.WriteString(raw)
			}
			buf.WriteByte(']')
			result.RawModelParts = buf.String()
		}

		return result, nil
	}

	if lastErr == nil {
		lastErr = fmt.Errorf("antigravity web search orchestrator: no base url available")
	}
	return nil, lastErr
}

func (o *webSearchOrchestrator) emitSSE(eventType string, data string) {
	payload := []byte(fmt.Sprintf("event: %s\ndata: %s\n\n", eventType, data))
	select {
	case <-o.ctx.Done():
		return
	case o.out <- cliproxyexecutor.StreamChunk{Payload: payload}:
	}
}

func (o *webSearchOrchestrator) emitMessageStart() {
	data := fmt.Sprintf(`{"type":"message_start","message":{"id":"%s","type":"message","role":"assistant","content":[],"model":"%s","stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":%d,"output_tokens":0}}}`,
		o.msgID, o.model, o.totalInputTokens)
	o.emitSSE("message_start", data)
}

func (o *webSearchOrchestrator) emitThinkingStart() int {
	index := o.contentIndex
	o.contentIndex++
	data := fmt.Sprintf(`{"type":"content_block_start","index":%d,"content_block":{"type":"thinking","thinking":""}}`, index)
	o.emitSSE("content_block_start", data)
	return index
}

func (o *webSearchOrchestrator) emitThinkingDelta(index int, text string) {
	data := fmt.Sprintf(`{"type":"content_block_delta","index":%d,"delta":{"type":"thinking_delta","thinking":""}}`, index)
	data, _ = sjson.Set(data, "delta.thinking", text)
	o.emitSSE("content_block_delta", data)
}


func (o *webSearchOrchestrator) emitSignatureDelta(index int, signature string) {
	var sigValue string
	if cache.SignatureCacheEnabled() {
		sigValue = fmt.Sprintf("%s#%s", cache.GetModelGroup(o.baseModel), signature)
	} else if cache.GetModelGroup(o.baseModel) == "claude" {
		sigValue = signature // raw signature for non-cached claude
	} else {
		sigValue = signature
	}
	data, _ := sjson.Set(fmt.Sprintf(`{"type":"content_block_delta","index":%d,"delta":{"type":"signature_delta","signature":""}}`, index), "delta.signature", sigValue)
	o.emitSSE("content_block_delta", data)
}

func (o *webSearchOrchestrator) emitBlockStop(index int) {
	o.emitSSE("content_block_stop", fmt.Sprintf(`{"type":"content_block_stop","index":%d}`, index))
}

func (o *webSearchOrchestrator) emitServerToolUse(toolUseID, query string) int {
	index := o.contentIndex
	o.contentIndex++
	start := fmt.Sprintf(`{"type":"content_block_start","index":%d,"content_block":{"type":"server_tool_use","id":"%s","name":"web_search","input":{}}}`,
		index, toolUseID)
	o.emitSSE("content_block_start", start)

	queryJSON, _ := sjson.Set(`{}`, "query", query)
	delta := fmt.Sprintf(`{"type":"content_block_delta","index":%d,"delta":{"type":"input_json_delta","partial_json":""}}`, index)
	delta, _ = sjson.Set(delta, "delta.partial_json", queryJSON)
	o.emitSSE("content_block_delta", delta)

	o.emitBlockStop(index)
	return index
}

func (o *webSearchOrchestrator) emitWebSearchToolResult(toolUseID string, results []wsSearchResult) int {
	index := o.contentIndex
	o.contentIndex++

	contentRaw := "[]"
	for _, result := range results {
		entry := `{"type":"web_search_result","title":"","url":"","page_age":null}`
		entry, _ = sjson.Set(entry, "title", result.Title)
		entry, _ = sjson.Set(entry, "url", result.URL)
		if result.PageAge != nil {
			entry, _ = sjson.Set(entry, "page_age", *result.PageAge)
		}
		contentRaw, _ = sjson.SetRaw(contentRaw, "-1", entry)
	}

	start := fmt.Sprintf(`{"type":"content_block_start","index":%d,"content_block":{"type":"web_search_tool_result","tool_use_id":"%s","content":[]}}`,
		index, toolUseID)
	start, _ = sjson.SetRaw(start, "content_block.content", contentRaw)
	o.emitSSE("content_block_start", start)
	o.emitBlockStop(index)
	return index
}

func (o *webSearchOrchestrator) emitTextStart() int {
	index := o.contentIndex
	o.contentIndex++
	data := fmt.Sprintf(`{"type":"content_block_start","index":%d,"content_block":{"type":"text","text":""}}`, index)
	o.emitSSE("content_block_start", data)
	return index
}

func (o *webSearchOrchestrator) emitTextDelta(index int, text string) {
	data := fmt.Sprintf(`{"type":"content_block_delta","index":%d,"delta":{"type":"text_delta","text":""}}`, index)
	data, _ = sjson.Set(data, "delta.text", text)
	o.emitSSE("content_block_delta", data)
}

func (o *webSearchOrchestrator) emitMessageDelta(stopReason string) {
	if strings.TrimSpace(stopReason) == "" {
		stopReason = "end_turn"
	}
	data := fmt.Sprintf(`{"type":"message_delta","delta":{"stop_reason":"%s","stop_sequence":null},"usage":{"input_tokens":%d,"output_tokens":%d,"server_tool_use":{"web_search_requests":%d}}}`,
		stopReason, o.totalInputTokens, o.totalOutputTokens, o.searchCount)
	o.emitSSE("message_delta", data)
}

func (o *webSearchOrchestrator) emitMessageStop() {
	o.emitSSE("message_stop", `{"type":"message_stop"}`)
}

func (o *webSearchOrchestrator) emitError(err error) {
	if err == nil {
		return
	}
	data := `{"type":"error","error":{"type":"api_error","message":""}}`
	data, _ = sjson.Set(data, "error.message", err.Error())
	o.emitSSE("error", data)
}

func extractSearchResults(geminiResp []byte) []wsSearchResult {
	chunks := gjson.GetBytes(geminiResp, "response.candidates.0.groundingMetadata.groundingChunks")
	if !chunks.IsArray() {
		chunks = gjson.GetBytes(geminiResp, "candidates.0.groundingMetadata.groundingChunks")
	}
	if !chunks.IsArray() {
		return nil
	}

	results := make([]wsSearchResult, 0, len(chunks.Array()))
	for _, chunk := range chunks.Array() {
		web := chunk.Get("web")
		if !web.Exists() {
			continue
		}
		url := web.Get("uri").String()
		if strings.TrimSpace(url) == "" {
			continue
		}
		results = append(results, wsSearchResult{
			Title:   web.Get("title").String(),
			URL:     url,
			PageAge: nil,
		})
	}
	return results
}

func (o *webSearchOrchestrator) buildContinuation(payload []byte, result *wsRoundResult, searchResults [][]wsSearchResult) []byte {
	out := payload

	modelEntry := `{"role":"model","parts":[]}`
	rawParts := result.RawModelParts
	if strings.TrimSpace(rawParts) == "" {
		rawParts = "[]"
	}
	modelEntry, _ = sjson.SetRaw(modelEntry, "parts", rawParts)
	out, _ = sjson.SetRawBytes(out, "request.contents.-1", []byte(modelEntry))

	userEntry := `{"role":"user","parts":[]}`
	searchIdx := 0
	for _, call := range result.FunctionCalls {
		if call.Name != "web_search" {
			continue
		}
		var results []wsSearchResult
		if searchIdx < len(searchResults) {
			results = searchResults[searchIdx]
		}
		searchIdx++
		var content bytes.Buffer
		content.WriteString("Search results for: ")
		content.WriteString(call.Query)
		content.WriteString("\n\n")
		if len(results) == 0 {
			content.WriteString("No results found.")
		} else {
			for i, item := range results {
				content.WriteString(fmt.Sprintf("%d. %s - %s", i+1, item.Title, item.URL))
				if i < len(results)-1 {
					content.WriteString("\n")
				}
			}
		}

		part := `{"functionResponse":{"name":"web_search","response":{"content":""}}}`
		part, _ = sjson.Set(part, "functionResponse.response.content", content.String())
		userEntry, _ = sjson.SetRaw(userEntry, fmt.Sprintf("parts.%d", searchIdx-1), part)
	}

	if searchIdx > 0 {
		out, _ = sjson.SetRawBytes(out, "request.contents.-1", []byte(userEntry))
	}

	return out
}

func supportsClaudeThinking(model string, baseModel string) bool {
	parsed := thinking.ParseSuffix(model)
	name := strings.ToLower(parsed.ModelName)
	if !strings.Contains(strings.ToLower(baseModel), "claude") {
		return false
	}
	return strings.Contains(name, "thinking")
}

func mapFinishReason(reason string) string {
	switch strings.ToUpper(strings.TrimSpace(reason)) {
	case "MAX_TOKENS":
		return "max_tokens"
	case "STOP", "":
		return "end_turn"
	default:
		return "end_turn"
	}
}
