package executor

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestReplaceWebSearchWithFunction_ReplacesWebSearchTool(t *testing.T) {
	t.Parallel()

	payload := []byte(`{"model":"claude-sonnet-4-20250514","tools":[{"type":"web_search_20250305","name":"web_search","max_uses":5}],"messages":[{"role":"user","content":"hello"}]}`)

	result := replaceWebSearchWithFunction(payload)

	tools := gjson.GetBytes(result, "tools")
	if !tools.IsArray() {
		t.Fatal("expected tools to be an array")
	}
	if len(tools.Array()) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools.Array()))
	}

	tool := tools.Array()[0]
	if tool.Get("type").Exists() {
		t.Error("replaced tool should not have 'type' field")
	}
	if tool.Get("name").String() != "web_search" {
		t.Errorf("expected name 'web_search', got %q", tool.Get("name").String())
	}
	if !tool.Get("input_schema.properties.query").Exists() {
		t.Error("expected input_schema with query property")
	}
}

func TestReplaceWebSearchWithFunction_PreservesOtherTools(t *testing.T) {
	t.Parallel()

	payload := []byte(`{"tools":[{"type":"web_search_20250305","name":"web_search"},{"name":"get_weather","description":"Get weather","input_schema":{"type":"object","properties":{"city":{"type":"string"}}}}]}`)

	result := replaceWebSearchWithFunction(payload)

	tools := gjson.GetBytes(result, "tools")
	if len(tools.Array()) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(tools.Array()))
	}

	// Second tool should be unchanged
	second := tools.Array()[1]
	if second.Get("name").String() != "get_weather" {
		t.Errorf("expected 'get_weather', got %q", second.Get("name").String())
	}
}

func TestReplaceWebSearchWithFunction_NoTools(t *testing.T) {
	t.Parallel()

	payload := []byte(`{"model":"claude-sonnet-4-20250514","messages":[]}`)
	result := replaceWebSearchWithFunction(payload)

	if string(result) != string(payload) {
		t.Error("expected payload unchanged when no tools field")
	}
}

func TestReplaceWebSearchWithFunction_EmptyTools(t *testing.T) {
	t.Parallel()

	payload := []byte(`{"tools":[],"messages":[]}`)
	result := replaceWebSearchWithFunction(payload)

	tools := gjson.GetBytes(result, "tools")
	if !tools.IsArray() || len(tools.Array()) != 0 {
		t.Error("expected empty tools array")
	}
}

func TestExtractSearchResults_WithGroundingChunks(t *testing.T) {
	t.Parallel()

	geminiResp := []byte(`{
		"candidates": [{
			"groundingMetadata": {
				"groundingChunks": [
					{"web": {"uri": "https://example.com/1", "title": "Example 1"}},
					{"web": {"uri": "https://example.com/2", "title": "Example 2"}},
					{"web": {"uri": "", "title": "Empty URL"}}
				]
			}
		}]
	}`)

	results := extractSearchResults(geminiResp)

	if len(results) != 2 {
		t.Fatalf("expected 2 results (empty URL filtered), got %d", len(results))
	}
	if results[0].Title != "Example 1" {
		t.Errorf("expected title 'Example 1', got %q", results[0].Title)
	}
	if results[0].URL != "https://example.com/1" {
		t.Errorf("expected URL 'https://example.com/1', got %q", results[0].URL)
	}
	if results[1].Title != "Example 2" {
		t.Errorf("expected title 'Example 2', got %q", results[1].Title)
	}
}

func TestExtractSearchResults_ResponseWrapper(t *testing.T) {
	t.Parallel()

	geminiResp := []byte(`{
		"response": {
			"candidates": [{
				"groundingMetadata": {
					"groundingChunks": [
						{"web": {"uri": "https://example.com", "title": "Test"}}
					]
				}
			}]
		}
	}`)

	results := extractSearchResults(geminiResp)

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].URL != "https://example.com" {
		t.Errorf("expected URL 'https://example.com', got %q", results[0].URL)
	}
}

func TestExtractSearchResults_NoGroundingMetadata(t *testing.T) {
	t.Parallel()

	geminiResp := []byte(`{"candidates":[{"content":{"parts":[{"text":"hello"}]}}]}`)
	results := extractSearchResults(geminiResp)

	if results != nil {
		t.Errorf("expected nil results, got %d items", len(results))
	}
}

func TestExtractSearchResults_EmptyResponse(t *testing.T) {
	t.Parallel()

	results := extractSearchResults([]byte(`{}`))
	if results != nil {
		t.Errorf("expected nil results for empty response")
	}
}

func TestMapFinishReason(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input    string
		expected string
	}{
		{"STOP", "end_turn"},
		{"stop", "end_turn"},
		{"", "end_turn"},
		{"MAX_TOKENS", "max_tokens"},
		{"max_tokens", "max_tokens"},
		{"SAFETY", "end_turn"},
		{"OTHER", "end_turn"},
		{"  STOP  ", "end_turn"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.input, func(t *testing.T) {
			result := mapFinishReason(tt.input)
			if result != tt.expected {
				t.Errorf("mapFinishReason(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestSupportsClaudeThinking(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		model     string
		baseModel string
		expected  bool
	}{
		{"claude thinking model", "claude-opus-4-5-thinking", "claude-opus-4-5", true},
		{"claude non-thinking model", "claude-sonnet-4-20250514", "claude-sonnet-4-20250514", false},
		{"gemini model", "gemini-2.5-pro", "gemini-2.5-pro", false},
		{"claude with suffix", "claude-sonnet-4-thinking-strip-thinking", "claude-sonnet-4", true},
		{"empty model", "", "", false},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			result := supportsClaudeThinking(tt.model, tt.baseModel)
			if result != tt.expected {
				t.Errorf("supportsClaudeThinking(%q, %q) = %v, want %v", tt.model, tt.baseModel, result, tt.expected)
			}
		})
	}
}

func TestBuildContinuation_AddsModelAndUserEntries(t *testing.T) {
	t.Parallel()

	payload := []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}}`)

	result := &wsRoundResult{
		RawModelParts: `[{"text":"I'll search for that"},{"functionCall":{"name":"web_search","args":{"query":"test"}}}]`,
		FunctionCalls: []wsFunctionCall{
			{Name: "web_search", Query: "test query"},
		},
	}

	searchResults := [][]wsSearchResult{
		{
			{Title: "Result 1", URL: "https://example.com/1"},
			{Title: "Result 2", URL: "https://example.com/2"},
		},
	}

	o := &webSearchOrchestrator{}
	out := o.buildContinuation(payload, result, searchResults)

	contents := gjson.GetBytes(out, "request.contents")
	if !contents.IsArray() {
		t.Fatal("expected request.contents to be an array")
	}
	if len(contents.Array()) != 3 {
		t.Fatalf("expected 3 entries (user + model + user-tool), got %d", len(contents.Array()))
	}

	// Check model entry
	modelEntry := contents.Array()[1]
	if modelEntry.Get("role").String() != "model" {
		t.Errorf("expected role 'model', got %q", modelEntry.Get("role").String())
	}
	if !modelEntry.Get("parts").IsArray() {
		t.Error("expected model entry to have parts array")
	}

	// Check user tool response entry
	userEntry := contents.Array()[2]
	if userEntry.Get("role").String() != "user" {
		t.Errorf("expected role 'user', got %q", userEntry.Get("role").String())
	}

	funcResp := userEntry.Get("parts.0.functionResponse")
	if !funcResp.Exists() {
		t.Fatal("expected functionResponse in user entry")
	}
	if funcResp.Get("name").String() != "web_search" {
		t.Errorf("expected name 'web_search', got %q", funcResp.Get("name").String())
	}
	content := funcResp.Get("response.content").String()
	if content == "" {
		t.Error("expected non-empty content in functionResponse")
	}
}

func TestBuildContinuation_NoSearchResults(t *testing.T) {
	t.Parallel()

	payload := []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}}`)

	result := &wsRoundResult{
		RawModelParts: `[{"text":"searching..."},{"functionCall":{"name":"web_search","args":{"query":"test"}}}]`,
		FunctionCalls: []wsFunctionCall{
			{Name: "web_search", Query: "test"},
		},
	}

	searchResults := [][]wsSearchResult{nil}

	o := &webSearchOrchestrator{}
	out := o.buildContinuation(payload, result, searchResults)

	contents := gjson.GetBytes(out, "request.contents")
	// Should still have 3 entries: original user + model + user with "No results found."
	if len(contents.Array()) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(contents.Array()))
	}

	content := contents.Array()[2].Get("parts.0.functionResponse.response.content").String()
	if content == "" {
		t.Error("expected content even for empty results")
	}
}

func TestBuildContinuation_EmptyRawParts(t *testing.T) {
	t.Parallel()

	payload := []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}}`)

	result := &wsRoundResult{
		RawModelParts: "",
		FunctionCalls: []wsFunctionCall{},
	}

	searchResults := [][]wsSearchResult{}

	o := &webSearchOrchestrator{}
	out := o.buildContinuation(payload, result, searchResults)

	contents := gjson.GetBytes(out, "request.contents")
	// model entry added but no user entry (no function calls)
	if len(contents.Array()) != 2 {
		t.Fatalf("expected 2 entries (original user + model), got %d", len(contents.Array()))
	}

	modelParts := contents.Array()[1].Get("parts")
	if !modelParts.IsArray() {
		t.Error("expected model parts to be array")
	}
}
