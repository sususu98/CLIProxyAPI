package auth

import (
	"bytes"

	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// modelFieldPaths lists all JSON paths where model name may appear in API responses.
var modelFieldPaths = []string{"model", "modelVersion", "response.model", "response.modelVersion", "message.model"}

// rewriteModelInResponse replaces model names in JSON response data.
// This is used to ensure the client sees the requested alias instead of the upstream model name.
func rewriteModelInResponse(data []byte, originalModel string) []byte {
	if originalModel == "" || len(data) == 0 {
		return data
	}
	for _, path := range modelFieldPaths {
		if gjson.GetBytes(data, path).Exists() {
			data, _ = sjson.SetBytes(data, path, originalModel)
			log.Debugf("response rewriter: rewrote model at path %s to %s", path, originalModel)
		}
	}
	return data
}

// StreamRewriteOptions contains options for stream chunk rewriting.
type StreamRewriteOptions struct {
	// RewriteModel is the model name to use in response (empty = no rewrite)
	RewriteModel string
	// StripThinking indicates whether to strip thinking events from the stream
	StripThinking bool
}

// StreamRewriter handles rewriting of SSE stream chunks with support for
// model name rewriting and thinking block stripping.
//
// IMPORTANT: This struct is NOT thread-safe. Each StreamRewriter instance
// should be used by a single goroutine only. The conductor.go creates a new
// instance per stream request, so this is safe in the current usage pattern.
type StreamRewriter struct {
	options              StreamRewriteOptions
	thinkingBlockIndexes map[int]bool // tracks which block indexes are thinking blocks
	pendingBuf           []byte       // buffer for incomplete SSE events across chunks
}

// NewStreamRewriter creates a new stream rewriter with the given options.
func NewStreamRewriter(options StreamRewriteOptions) *StreamRewriter {
	return &StreamRewriter{
		options:              options,
		thinkingBlockIndexes: make(map[int]bool),
		pendingBuf:           nil,
	}
}

// RewriteChunk rewrites a stream chunk, handling both model name replacement
// and optionally stripping thinking events. It buffers incomplete SSE events
// across chunk boundaries to handle network fragmentation.
func (r *StreamRewriter) RewriteChunk(chunk []byte) []byte {
	// If no rewriting needed, return original
	if r.options.RewriteModel == "" && !r.options.StripThinking {
		return chunk
	}

	// Prepend any pending data from previous chunk
	if len(r.pendingBuf) > 0 {
		chunk = append(r.pendingBuf, chunk...)
		r.pendingBuf = nil
	}

	// SSE format: "data: {json}\n\n" - events are separated by double newlines
	// Find the last complete event boundary (double newline or trailing newline)
	lastDoubleNewline := bytes.LastIndex(chunk, []byte("\n\n"))
	lastNewline := -1
	if len(chunk) > 0 && chunk[len(chunk)-1] == '\n' {
		lastNewline = len(chunk) - 1
	}

	// Determine if we have an incomplete event at the end
	var processChunk []byte
	if lastDoubleNewline >= 0 {
		// Check if there's more data after the last complete event
		afterComplete := chunk[lastDoubleNewline+2:]
		if len(afterComplete) > 0 && !bytes.Equal(afterComplete, []byte("\n")) {
			// There's incomplete data after the last complete event
			processChunk = chunk[:lastDoubleNewline+2]
			r.pendingBuf = make([]byte, len(afterComplete))
			copy(r.pendingBuf, afterComplete)
		} else {
			processChunk = chunk
		}
	} else if lastNewline >= 0 && gjson.ValidBytes(extractLastDataPayload(chunk)) {
		// Single line with valid JSON - process it
		processChunk = chunk
	} else if len(chunk) > 0 {
		// No complete event boundary found, buffer the entire chunk
		r.pendingBuf = make([]byte, len(chunk))
		copy(r.pendingBuf, chunk)
		return nil
	} else {
		return chunk
	}

	lines := bytes.Split(processChunk, []byte("\n"))
	var result [][]byte
	var pendingEvent []byte
	skipBlanks := false

	for _, line := range lines {
		if len(line) == 0 && skipBlanks {
			continue
		}
		if len(line) != 0 && skipBlanks {
			skipBlanks = false
		}

		if bytes.HasPrefix(line, []byte("event:")) {
			pendingEvent = line
			continue
		}

		if jsonData, found := bytes.CutPrefix(line, []byte("data: ")); found && len(jsonData) > 0 && jsonData[0] == '{' {
			// Validate JSON before processing to handle split payloads
			if !gjson.ValidBytes(jsonData) {
				// Incomplete JSON, buffer for next chunk
				if pendingEvent != nil {
					r.pendingBuf = append(pendingEvent, '\n')
					r.pendingBuf = append(r.pendingBuf, line...)
					pendingEvent = nil
				} else {
					r.pendingBuf = append(r.pendingBuf, line...)
				}
				continue
			}

			// Check if this is a thinking event that should be stripped
			if r.options.StripThinking && r.isThinkingEvent(jsonData) {
				pendingEvent = nil
				skipBlanks = true
				continue
			}

			if pendingEvent != nil {
				result = append(result, pendingEvent)
				pendingEvent = nil
			}

			// Rewrite model name if needed
			rewritten := jsonData
			if r.options.RewriteModel != "" {
				rewritten = rewriteModelInResponse(jsonData, r.options.RewriteModel)
			}
			result = append(result, append([]byte("data: "), rewritten...))
			continue
		}

		if pendingEvent != nil {
			result = append(result, pendingEvent)
			pendingEvent = nil
		}
		result = append(result, line)
	}

	if pendingEvent != nil {
		result = append(result, pendingEvent)
	}

	return bytes.Join(result, []byte("\n"))
}

// extractLastDataPayload extracts the JSON payload from the last data line if present.
func extractLastDataPayload(chunk []byte) []byte {
	lines := bytes.Split(chunk, []byte("\n"))
	for i := len(lines) - 1; i >= 0; i-- {
		if jsonData, found := bytes.CutPrefix(lines[i], []byte("data: ")); found && len(jsonData) > 0 {
			return jsonData
		}
	}
	return nil
}

// isThinkingEvent checks if an SSE data payload is a thinking-related event.
// This includes content_block_start with thinking type, content_block_delta with thinking_delta,
// and content_block_stop for thinking blocks.
func (r *StreamRewriter) isThinkingEvent(data []byte) bool {
	eventType := gjson.GetBytes(data, "type").String()

	switch eventType {
	case "content_block_start":
		// Check if content_block.type is "thinking"
		blockType := gjson.GetBytes(data, "content_block.type").String()
		if blockType == "thinking" {
			// Track this block index as a thinking block
			index := int(gjson.GetBytes(data, "index").Int())
			r.thinkingBlockIndexes[index] = true
			log.Debugf("response rewriter: stripping thinking block start at index %d", index)
			return true
		}
		return false

	case "content_block_delta":
		// First check if this delta belongs to a tracked thinking block by index
		index := int(gjson.GetBytes(data, "index").Int())
		if r.thinkingBlockIndexes[index] {
			return true
		}
		// Also filter by delta type for thinking_delta and signature_delta
		deltaType := gjson.GetBytes(data, "delta.type").String()
		return deltaType == "thinking_delta" || deltaType == "signature_delta"

	case "content_block_stop":
		// Check if this stop event is for a thinking block
		index := int(gjson.GetBytes(data, "index").Int())
		if r.thinkingBlockIndexes[index] {
			delete(r.thinkingBlockIndexes, index)
			log.Debugf("response rewriter: stripping thinking block stop at index %d", index)
			return true
		}
		return false
	}

	return false
}

// stripThinkingBlocksFromResponse removes thinking blocks from a non-streaming response.
func stripThinkingBlocksFromResponse(data []byte) []byte {
	if len(data) == 0 {
		return data
	}

	// Check if response has content array
	contentResult := gjson.GetBytes(data, "content")
	if !contentResult.Exists() || !contentResult.IsArray() {
		return data
	}

	// Filter out thinking blocks from content array
	var filteredContent []any
	var strippedCount int
	contentResult.ForEach(func(_, value gjson.Result) bool {
		blockType := value.Get("type").String()
		if blockType != "thinking" {
			filteredContent = append(filteredContent, value.Value())
		} else {
			strippedCount++
		}
		return true
	})

	if strippedCount > 0 {
		log.Debugf("response rewriter: stripped %d thinking blocks from non-streaming response", strippedCount)
	}

	// Replace content array with filtered version
	result, err := sjson.SetBytes(data, "content", filteredContent)
	if err != nil {
		return data
	}
	return result
}
