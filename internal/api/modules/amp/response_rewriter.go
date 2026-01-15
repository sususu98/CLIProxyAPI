package amp

import (
	"bytes"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ResponseRewriter wraps a gin.ResponseWriter to intercept and modify the response body
// It's used to rewrite model names in responses when model mapping is used
type ResponseRewriter struct {
	gin.ResponseWriter
	body                  *bytes.Buffer
	originalModel         string
	isStreaming           bool
	stripThinkingResponse bool
	thinkingBlockIndexes  map[int]bool // tracks which block indexes are thinking blocks
}

// NewResponseRewriter creates a new response rewriter for model name substitution
func NewResponseRewriter(w gin.ResponseWriter, originalModel string) *ResponseRewriter {
	return &ResponseRewriter{
		ResponseWriter: w,
		body:           &bytes.Buffer{},
		originalModel:  originalModel,
	}
}

// NewResponseRewriterWithOptions creates a new response rewriter with additional options
func NewResponseRewriterWithOptions(w gin.ResponseWriter, originalModel string, stripThinkingResponse bool) *ResponseRewriter {
	return &ResponseRewriter{
		ResponseWriter:        w,
		body:                  &bytes.Buffer{},
		originalModel:         originalModel,
		stripThinkingResponse: stripThinkingResponse,
	}
}

// Write intercepts response writes and buffers them for model name replacement
func (rw *ResponseRewriter) Write(data []byte) (int, error) {
	// Detect streaming on first write
	if rw.body.Len() == 0 && !rw.isStreaming {
		contentType := rw.Header().Get("Content-Type")
		rw.isStreaming = strings.Contains(contentType, "text/event-stream") ||
			strings.Contains(contentType, "stream")
	}

	if rw.isStreaming {
		n, err := rw.ResponseWriter.Write(rw.rewriteStreamChunk(data))
		if err == nil {
			if flusher, ok := rw.ResponseWriter.(http.Flusher); ok {
				flusher.Flush()
			}
		}
		return n, err
	}
	return rw.body.Write(data)
}

// Flush writes the buffered response with model names rewritten
func (rw *ResponseRewriter) Flush() {
	if rw.isStreaming {
		if flusher, ok := rw.ResponseWriter.(http.Flusher); ok {
			flusher.Flush()
		}
		return
	}
	if rw.body.Len() > 0 {
		if _, err := rw.ResponseWriter.Write(rw.rewriteModelInResponse(rw.body.Bytes())); err != nil {
			log.Warnf("amp response rewriter: failed to write rewritten response: %v", err)
		}
	}
}

// modelFieldPaths lists all JSON paths where model name may appear
var modelFieldPaths = []string{"model", "modelVersion", "response.modelVersion", "message.model"}

// rewriteModelInResponse replaces all occurrences of the mapped model with the original model in JSON
// It also suppresses "thinking" blocks if "tool_use" is present to ensure Amp client compatibility
func (rw *ResponseRewriter) rewriteModelInResponse(data []byte) []byte {
	// 1. Amp Compatibility: Suppress thinking blocks if tool use is detected
	// The Amp client struggles when both thinking and tool_use blocks are present
	if gjson.GetBytes(data, `content.#(type=="tool_use")`).Exists() {
		filtered := gjson.GetBytes(data, `content.#(type!="thinking")#`)
		if filtered.Exists() {
			originalCount := gjson.GetBytes(data, "content.#").Int()
			filteredCount := filtered.Get("#").Int()

			if originalCount > filteredCount {
				var err error
				data, err = sjson.SetBytes(data, "content", filtered.Value())
				if err != nil {
					log.Warnf("Amp ResponseRewriter: failed to suppress thinking blocks: %v", err)
				} else {
					log.Debugf("Amp ResponseRewriter: Suppressed %d thinking blocks due to tool usage", originalCount-filteredCount)
					// Log the result for verification
					log.Debugf("Amp ResponseRewriter: Resulting content: %s", gjson.GetBytes(data, "content").String())
				}
			}
		}
	}

	if rw.originalModel == "" {
		return data
	}
	for _, path := range modelFieldPaths {
		if gjson.GetBytes(data, path).Exists() {
			data, _ = sjson.SetBytes(data, path, rw.originalModel)
		}
	}
	return data
}

// rewriteStreamChunk rewrites model names in SSE stream chunks and optionally strips thinking events
func (rw *ResponseRewriter) rewriteStreamChunk(chunk []byte) []byte {
	// SSE format: "data: {json}\n\n"
	lines := bytes.Split(chunk, []byte("\n"))
	var result [][]byte

	for _, line := range lines {
		// Handle data lines
		if jsonData, found := bytes.CutPrefix(line, []byte("data: ")); found {
			if len(jsonData) > 0 && jsonData[0] == '{' {
				// Check if this is a thinking event that should be stripped
				if rw.stripThinkingResponse && rw.isThinkingEvent(jsonData) {
					log.Debugf("amp response rewriter: stripping thinking event")
					continue // Skip this line entirely
				}

				// Rewrite JSON in the data line (model names and possibly strip thinking from content)
				rewritten := rw.rewriteModelInResponse(jsonData)
				result = append(result, append([]byte("data: "), rewritten...))
				continue
			}
		}
		result = append(result, line)
	}

	return bytes.Join(result, []byte("\n"))
}

// isThinkingEvent checks if an SSE data payload is a thinking-related event.
// This includes content_block_start with thinking type, content_block_delta with thinking_delta,
// and content_block_stop for thinking blocks.
func (rw *ResponseRewriter) isThinkingEvent(data []byte) bool {
	eventType := gjson.GetBytes(data, "type").String()

	switch eventType {
	case "content_block_start":
		// Check if content_block.type is "thinking"
		blockType := gjson.GetBytes(data, "content_block.type").String()
		if blockType == "thinking" {
			// Track this block index as a thinking block
			index := int(gjson.GetBytes(data, "index").Int())
			if rw.thinkingBlockIndexes == nil {
				rw.thinkingBlockIndexes = make(map[int]bool)
			}
			rw.thinkingBlockIndexes[index] = true
			return true
		}
		return false

	case "content_block_delta":
		// Check if delta.type is "thinking_delta" or "signature_delta"
		deltaType := gjson.GetBytes(data, "delta.type").String()
		return deltaType == "thinking_delta" || deltaType == "signature_delta"

	case "content_block_stop":
		// Check if this stop event is for a thinking block
		index := int(gjson.GetBytes(data, "index").Int())
		if rw.thinkingBlockIndexes != nil && rw.thinkingBlockIndexes[index] {
			delete(rw.thinkingBlockIndexes, index)
			return true
		}
		return false
	}

	return false
}
