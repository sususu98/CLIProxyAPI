package claude

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/cache"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/thinking"
	"github.com/tidwall/gjson"
)

func TestConvertClaudeRequestToAntigravity_BasicStructure(t *testing.T) {
	inputJSON := []byte(`{
		"model": "claude-3-5-sonnet-20240620",
		"messages": [
			{
				"role": "user",
				"content": [
					{"type": "text", "text": "Hello"}
				]
			}
		],
		"system": [
			{"type": "text", "text": "You are helpful"}
		]
	}`)

	output := ConvertClaudeRequestToAntigravity("claude-sonnet-4-5", inputJSON, false)
	outputStr := string(output)

	// Check model
	if gjson.Get(outputStr, "model").String() != "claude-sonnet-4-5" {
		t.Errorf("Expected model 'claude-sonnet-4-5', got '%s'", gjson.Get(outputStr, "model").String())
	}

	// Check contents exist
	contents := gjson.Get(outputStr, "request.contents")
	if !contents.Exists() || !contents.IsArray() {
		t.Error("request.contents should exist and be an array")
	}

	// Check role mapping (assistant -> model)
	firstContent := gjson.Get(outputStr, "request.contents.0")
	if firstContent.Get("role").String() != "user" {
		t.Errorf("Expected role 'user', got '%s'", firstContent.Get("role").String())
	}

	// Check systemInstruction
	sysInstruction := gjson.Get(outputStr, "request.systemInstruction")
	if !sysInstruction.Exists() {
		t.Error("systemInstruction should exist")
	}
	if sysInstruction.Get("parts.0.text").String() != "You are helpful" {
		t.Error("systemInstruction text mismatch")
	}
}

func TestConvertClaudeRequestToAntigravity_RoleMapping(t *testing.T) {
	inputJSON := []byte(`{
		"model": "claude-3-5-sonnet-20240620",
		"messages": [
			{"role": "user", "content": [{"type": "text", "text": "Hi"}]},
			{"role": "assistant", "content": [{"type": "text", "text": "Hello"}]}
		]
	}`)

	output := ConvertClaudeRequestToAntigravity("claude-sonnet-4-5", inputJSON, false)
	outputStr := string(output)

	// assistant should be mapped to model
	secondContent := gjson.Get(outputStr, "request.contents.1")
	if secondContent.Get("role").String() != "model" {
		t.Errorf("Expected role 'model' (mapped from 'assistant'), got '%s'", secondContent.Get("role").String())
	}
}

func TestConvertClaudeRequestToAntigravity_ThinkingBlocks(t *testing.T) {
	cache.ClearSignatureCache("")

	// Valid signature must be at least 50 characters
	validSignature := "abc123validSignature1234567890123456789012345678901234567890"
	thinkingText := "Let me think..."

	// Pre-cache the signature (simulating a previous response for the same thinking text)
	inputJSON := []byte(`{
		"model": "claude-sonnet-4-5-thinking",
		"messages": [
			{
				"role": "user",
				"content": [{"type": "text", "text": "Test user message"}]
			},
			{
				"role": "assistant",
				"content": [
					{"type": "thinking", "thinking": "` + thinkingText + `", "signature": "` + validSignature + `"},
					{"type": "text", "text": "Answer"}
				]
			}
		]
	}`)

	cache.CacheSignature("claude-sonnet-4-5-thinking", thinkingText, validSignature)

	output := ConvertClaudeRequestToAntigravity("claude-sonnet-4-5-thinking", inputJSON, false)
	outputStr := string(output)

	// Check thinking block conversion (now in contents.1 due to user message)
	firstPart := gjson.Get(outputStr, "request.contents.1.parts.0")
	if !firstPart.Get("thought").Bool() {
		t.Error("thinking block should have thought: true")
	}
	if firstPart.Get("text").String() != thinkingText {
		t.Error("thinking text mismatch")
	}
	if firstPart.Get("thoughtSignature").String() != validSignature {
		t.Errorf("Expected thoughtSignature '%s', got '%s'", validSignature, firstPart.Get("thoughtSignature").String())
	}
}

func TestConvertClaudeRequestToAntigravity_ThinkingBlockWithoutSignature(t *testing.T) {
	cache.ClearSignatureCache("")

	// With cache enabled (default), unsigned thinking blocks should be removed
	inputJSON := []byte(`{
		"model": "claude-sonnet-4-5-thinking",
		"messages": [
			{
				"role": "assistant",
				"content": [
					{"type": "thinking", "thinking": "Let me think..."},
					{"type": "text", "text": "Answer"}
				]
			}
		]
	}`)

	output := ConvertClaudeRequestToAntigravity("claude-sonnet-4-5-thinking", inputJSON, false)
	outputStr := string(output)

	// Without signature, thinking block should be removed (not converted to text)
	parts := gjson.Get(outputStr, "request.contents.0.parts").Array()
	if len(parts) != 1 {
		t.Fatalf("Expected 1 part (thinking removed), got %d", len(parts))
	}

	// Only text part should remain
	if parts[0].Get("thought").Bool() {
		t.Error("Thinking block should be removed, not preserved")
	}
	if parts[0].Get("text").String() != "Answer" {
		t.Errorf("Expected text 'Answer', got '%s'", parts[0].Get("text").String())
	}
}

func TestConvertClaudeRequestToAntigravity_ToolDeclarations(t *testing.T) {
	inputJSON := []byte(`{
		"model": "claude-3-5-sonnet-20240620",
		"messages": [],
		"tools": [
			{
				"name": "test_tool",
				"description": "A test tool",
				"input_schema": {
					"type": "object",
					"properties": {
						"name": {"type": "string"}
					},
					"required": ["name"]
				}
			}
		]
	}`)

	output := ConvertClaudeRequestToAntigravity("gemini-1.5-pro", inputJSON, false)
	outputStr := string(output)

	// Check tools structure
	tools := gjson.Get(outputStr, "request.tools")
	if !tools.Exists() {
		t.Error("Tools should exist in output")
	}

	funcDecl := gjson.Get(outputStr, "request.tools.0.functionDeclarations.0")
	if funcDecl.Get("name").String() != "test_tool" {
		t.Errorf("Expected tool name 'test_tool', got '%s'", funcDecl.Get("name").String())
	}

	// Check input_schema renamed to parametersJsonSchema
	if funcDecl.Get("parametersJsonSchema").Exists() {
		t.Log("parametersJsonSchema exists (expected)")
	}
	if funcDecl.Get("input_schema").Exists() {
		t.Error("input_schema should be removed")
	}
}

func TestConvertClaudeRequestToAntigravity_ToolChoice_SpecificTool(t *testing.T) {
	inputJSON := []byte(`{
		"model": "gemini-3-flash-preview",
		"messages": [
			{
				"role": "user",
				"content": [
					{"type": "text", "text": "hi"}
				]
			}
		],
		"tools": [
			{
				"name": "json",
				"description": "A JSON tool",
				"input_schema": {
					"type": "object",
					"properties": {}
				}
			}
		],
		"tool_choice": {"type": "tool", "name": "json"}
	}`)

	output := ConvertClaudeRequestToAntigravity("gemini-3-flash-preview", inputJSON, false)
	outputStr := string(output)

	if got := gjson.Get(outputStr, "request.toolConfig.functionCallingConfig.mode").String(); got != "ANY" {
		t.Fatalf("Expected toolConfig.functionCallingConfig.mode 'ANY', got '%s'", got)
	}
	allowed := gjson.Get(outputStr, "request.toolConfig.functionCallingConfig.allowedFunctionNames").Array()
	if len(allowed) != 1 || allowed[0].String() != "json" {
		t.Fatalf("Expected allowedFunctionNames ['json'], got %s", gjson.Get(outputStr, "request.toolConfig.functionCallingConfig.allowedFunctionNames").Raw)
	}
}

func TestConvertClaudeRequestToAntigravity_ToolUse(t *testing.T) {
	inputJSON := []byte(`{
		"model": "claude-3-5-sonnet-20240620",
		"messages": [
			{
				"role": "assistant",
				"content": [
					{
						"type": "tool_use",
						"id": "call_123",
						"name": "get_weather",
						"input": "{\"location\": \"Paris\"}"
					}
				]
			}
		]
	}`)

	output := ConvertClaudeRequestToAntigravity("claude-sonnet-4-5", inputJSON, false)
	outputStr := string(output)

	// Now we expect only 1 part (tool_use), no dummy thinking block injected
	parts := gjson.Get(outputStr, "request.contents.0.parts").Array()
	if len(parts) != 1 {
		t.Fatalf("Expected 1 part (tool only, no dummy injection), got %d", len(parts))
	}

	// Check function call conversion at parts[0]
	funcCall := parts[0].Get("functionCall")
	if !funcCall.Exists() {
		t.Error("functionCall should exist at parts[0]")
	}
	if funcCall.Get("name").String() != "get_weather" {
		t.Errorf("Expected function name 'get_weather', got '%s'", funcCall.Get("name").String())
	}
	if funcCall.Get("id").String() != "call_123" {
		t.Errorf("Expected function id 'call_123', got '%s'", funcCall.Get("id").String())
	}
	// Verify skip_thought_signature_validator is added (bypass for tools without valid thinking)
	expectedSig := "skip_thought_signature_validator"
	actualSig := parts[0].Get("thoughtSignature").String()
	if actualSig != expectedSig {
		t.Errorf("Expected thoughtSignature '%s', got '%s'", expectedSig, actualSig)
	}
}

func TestConvertClaudeRequestToAntigravity_ToolUse_WithSignature(t *testing.T) {
	cache.ClearSignatureCache("")

	validSignature := "abc123validSignature1234567890123456789012345678901234567890"
	thinkingText := "Let me think..."

	inputJSON := []byte(`{
		"model": "claude-sonnet-4-5-thinking",
		"messages": [
			{
				"role": "user",
				"content": [{"type": "text", "text": "Test user message"}]
			},
			{
				"role": "assistant",
				"content": [
					{"type": "thinking", "thinking": "` + thinkingText + `", "signature": "` + validSignature + `"},
					{
						"type": "tool_use",
						"id": "call_123",
						"name": "get_weather",
						"input": "{\"location\": \"Paris\"}"
					}
				]
			}
		]
	}`)

	cache.CacheSignature("claude-sonnet-4-5-thinking", thinkingText, validSignature)

	output := ConvertClaudeRequestToAntigravity("claude-sonnet-4-5-thinking", inputJSON, false)
	outputStr := string(output)

	// Check function call has the signature from the preceding thinking block (now in contents.1)
	part := gjson.Get(outputStr, "request.contents.1.parts.1")
	if part.Get("functionCall.name").String() != "get_weather" {
		t.Errorf("Expected functionCall, got %s", part.Raw)
	}
	if part.Get("thoughtSignature").String() != validSignature {
		t.Errorf("Expected thoughtSignature '%s' on tool_use, got '%s'", validSignature, part.Get("thoughtSignature").String())
	}
}

func TestConvertClaudeRequestToAntigravity_ReorderThinking(t *testing.T) {
	cache.ClearSignatureCache("")

	// Case: text block followed by thinking block -> should be reordered to thinking first
	validSignature := "abc123validSignature1234567890123456789012345678901234567890"
	thinkingText := "Planning..."

	inputJSON := []byte(`{
		"model": "claude-sonnet-4-5-thinking",
		"messages": [
			{
				"role": "user",
				"content": [{"type": "text", "text": "Test user message"}]
			},
			{
				"role": "assistant",
				"content": [
					{"type": "text", "text": "Here is the plan."},
					{"type": "thinking", "thinking": "` + thinkingText + `", "signature": "` + validSignature + `"}
				]
			}
		]
	}`)

	cache.CacheSignature("claude-sonnet-4-5-thinking", thinkingText, validSignature)

	output := ConvertClaudeRequestToAntigravity("claude-sonnet-4-5-thinking", inputJSON, false)
	outputStr := string(output)

	// Verify order: Thinking block MUST be first (now in contents.1 due to user message)
	parts := gjson.Get(outputStr, "request.contents.1.parts").Array()
	if len(parts) != 2 {
		t.Fatalf("Expected 2 parts, got %d", len(parts))
	}

	if !parts[0].Get("thought").Bool() {
		t.Error("First part should be thinking block after reordering")
	}
	if parts[1].Get("text").String() != "Here is the plan." {
		t.Error("Second part should be text block")
	}
}

func TestConvertClaudeRequestToAntigravity_ToolResult(t *testing.T) {
	inputJSON := []byte(`{
		"model": "claude-3-5-sonnet-20240620",
		"messages": [
			{
				"role": "assistant",
				"content": [
					{
						"type": "tool_use",
						"id": "get_weather-call-123",
						"name": "get_weather",
						"input": {"location": "Paris"}
					}
				]
			},
			{
				"role": "user",
				"content": [
					{
						"type": "tool_result",
						"tool_use_id": "get_weather-call-123",
						"content": "22C sunny"
					}
				]
			}
		]
	}`)

	output := ConvertClaudeRequestToAntigravity("claude-sonnet-4-5", inputJSON, false)
	outputStr := string(output)

	// Check function response conversion
	funcResp := gjson.Get(outputStr, "request.contents.1.parts.0.functionResponse")
	if !funcResp.Exists() {
		t.Error("functionResponse should exist")
	}
	if funcResp.Get("id").String() != "get_weather-call-123" {
		t.Errorf("Expected function id, got '%s'", funcResp.Get("id").String())
	}
	if funcResp.Get("name").String() != "get_weather" {
		t.Errorf("Expected function name 'get_weather', got '%s'", funcResp.Get("name").String())
	}
}

func TestConvertClaudeRequestToAntigravity_ToolResultName_TouluFormat(t *testing.T) {
	inputJSON := []byte(`{
		"model": "claude-haiku-4-5-20251001",
		"messages": [
			{
				"role": "assistant",
				"content": [
					{
						"type": "tool_use",
						"id": "toolu_tool-48fca351f12844eabf49dad8b63886d2",
						"name": "Glob",
						"input": {"pattern": "**/*.py"}
					},
					{
						"type": "tool_use",
						"id": "toolu_tool-cf2d061f75f845c49aacc18ee75ee708",
						"name": "Bash",
						"input": {"command": "ls"}
					}
				]
			},
			{
				"role": "user",
				"content": [
					{
						"type": "tool_result",
						"tool_use_id": "toolu_tool-48fca351f12844eabf49dad8b63886d2",
						"content": "file1.py\nfile2.py"
					},
					{
						"type": "tool_result",
						"tool_use_id": "toolu_tool-cf2d061f75f845c49aacc18ee75ee708",
						"content": "total 10"
					}
				]
			}
		]
	}`)

	output := ConvertClaudeRequestToAntigravity("claude-haiku-4-5-20251001", inputJSON, false)
	outputStr := string(output)

	funcResp0 := gjson.Get(outputStr, "request.contents.1.parts.0.functionResponse")
	if !funcResp0.Exists() {
		t.Fatal("first functionResponse should exist")
	}
	if got := funcResp0.Get("name").String(); got != "Glob" {
		t.Errorf("Expected name 'Glob' for toolu_ format, got '%s'", got)
	}

	funcResp1 := gjson.Get(outputStr, "request.contents.1.parts.1.functionResponse")
	if !funcResp1.Exists() {
		t.Fatal("second functionResponse should exist")
	}
	if got := funcResp1.Get("name").String(); got != "Bash" {
		t.Errorf("Expected name 'Bash' for toolu_ format, got '%s'", got)
	}
}

func TestConvertClaudeRequestToAntigravity_ToolResultName_CustomFormat(t *testing.T) {
	inputJSON := []byte(`{
		"model": "claude-haiku-4-5-20251001",
		"messages": [
			{
				"role": "assistant",
				"content": [
					{
						"type": "tool_use",
						"id": "Read-1773420180464065165-1327",
						"name": "Read",
						"input": {"file_path": "/tmp/test.py"}
					}
				]
			},
			{
				"role": "user",
				"content": [
					{
						"type": "tool_result",
						"tool_use_id": "Read-1773420180464065165-1327",
						"content": "file content here"
					}
				]
			}
		]
	}`)

	output := ConvertClaudeRequestToAntigravity("claude-haiku-4-5-20251001", inputJSON, false)
	outputStr := string(output)

	funcResp := gjson.Get(outputStr, "request.contents.1.parts.0.functionResponse")
	if !funcResp.Exists() {
		t.Fatal("functionResponse should exist")
	}
	if got := funcResp.Get("name").String(); got != "Read" {
		t.Errorf("Expected name 'Read', got '%s'", got)
	}
}

func TestConvertClaudeRequestToAntigravity_ToolResultName_NoMatchingToolUse_Heuristic(t *testing.T) {
	inputJSON := []byte(`{
		"model": "claude-sonnet-4-5",
		"messages": [
			{
				"role": "user",
				"content": [
					{
						"type": "tool_result",
						"tool_use_id": "get_weather-call-123",
						"content": "22C sunny"
					}
				]
			}
		]
	}`)

	output := ConvertClaudeRequestToAntigravity("claude-sonnet-4-5", inputJSON, false)
	outputStr := string(output)

	funcResp := gjson.Get(outputStr, "request.contents.0.parts.0.functionResponse")
	if !funcResp.Exists() {
		t.Fatal("functionResponse should exist")
	}
	if got := funcResp.Get("name").String(); got != "get_weather" {
		t.Errorf("Expected heuristic-derived name 'get_weather', got '%s'", got)
	}
}

func TestConvertClaudeRequestToAntigravity_ToolResultName_NoMatchingToolUse_RawID(t *testing.T) {
	inputJSON := []byte(`{
		"model": "claude-sonnet-4-5",
		"messages": [
			{
				"role": "user",
				"content": [
					{
						"type": "tool_result",
						"tool_use_id": "toolu_tool-48fca351f12844eabf49dad8b63886d2",
						"content": "result data"
					}
				]
			}
		]
	}`)

	output := ConvertClaudeRequestToAntigravity("claude-sonnet-4-5", inputJSON, false)
	outputStr := string(output)

	funcResp := gjson.Get(outputStr, "request.contents.0.parts.0.functionResponse")
	if !funcResp.Exists() {
		t.Fatal("functionResponse should exist")
	}
	got := funcResp.Get("name").String()
	if got == "" {
		t.Error("functionResponse.name must not be empty")
	}
	if got != "toolu_tool-48fca351f12844eabf49dad8b63886d2" {
		t.Errorf("Expected raw ID as last-resort name, got '%s'", got)
	}
}

func TestConvertClaudeRequestToAntigravity_ThinkingConfig(t *testing.T) {
	// Note: This test requires the model to be registered in the registry
	// with Thinking metadata. If the registry is not populated in test environment,
	// thinkingConfig won't be added. We'll test the basic structure only.
	inputJSON := []byte(`{
		"model": "claude-sonnet-4-5-thinking",
		"messages": [],
		"thinking": {
			"type": "enabled",
			"budget_tokens": 8000
		}
	}`)

	output := ConvertClaudeRequestToAntigravity("claude-sonnet-4-5-thinking", inputJSON, false)
	outputStr := string(output)

	// Check thinking config conversion (only if model supports thinking in registry)
	thinkingConfig := gjson.Get(outputStr, "request.generationConfig.thinkingConfig")
	if thinkingConfig.Exists() {
		if thinkingConfig.Get("thinkingBudget").Int() != 8000 {
			t.Errorf("Expected thinkingBudget 8000, got %d", thinkingConfig.Get("thinkingBudget").Int())
		}
		if !thinkingConfig.Get("includeThoughts").Bool() {
			t.Error("includeThoughts should be true")
		}
	} else {
		t.Log("thinkingConfig not present - model may not be registered in test registry")
	}
}

func TestConvertClaudeRequestToAntigravity_ImageContent(t *testing.T) {
	inputJSON := []byte(`{
		"model": "claude-3-5-sonnet-20240620",
		"messages": [
			{
				"role": "user",
				"content": [
					{
						"type": "image",
						"source": {
							"type": "base64",
							"media_type": "image/png",
							"data": "iVBORw0KGgoAAAANSUhEUg=="
						}
					}
				]
			}
		]
	}`)

	output := ConvertClaudeRequestToAntigravity("claude-sonnet-4-5", inputJSON, false)
	outputStr := string(output)

	// Check inline data conversion
	inlineData := gjson.Get(outputStr, "request.contents.0.parts.0.inlineData")
	if !inlineData.Exists() {
		t.Error("inlineData should exist")
	}
	if inlineData.Get("mimeType").String() != "image/png" {
		t.Error("mimeType mismatch")
	}
	if !strings.Contains(inlineData.Get("data").String(), "iVBORw0KGgo") {
		t.Error("data mismatch")
	}
}

func TestConvertClaudeRequestToAntigravity_GenerationConfig(t *testing.T) {
	inputJSON := []byte(`{
		"model": "claude-3-5-sonnet-20240620",
		"messages": [],
		"temperature": 0.7,
		"top_p": 0.9,
		"top_k": 40,
		"max_tokens": 2000
	}`)

	output := ConvertClaudeRequestToAntigravity("claude-sonnet-4-5", inputJSON, false)
	outputStr := string(output)

	genConfig := gjson.Get(outputStr, "request.generationConfig")
	if genConfig.Get("temperature").Float() != 0.7 {
		t.Errorf("Expected temperature 0.7, got %f", genConfig.Get("temperature").Float())
	}
	if genConfig.Get("topP").Float() != 0.9 {
		t.Errorf("Expected topP 0.9, got %f", genConfig.Get("topP").Float())
	}
	if genConfig.Get("topK").Float() != 40 {
		t.Errorf("Expected topK 40, got %f", genConfig.Get("topK").Float())
	}
	if genConfig.Get("maxOutputTokens").Float() != 2000 {
		t.Errorf("Expected maxOutputTokens 2000, got %f", genConfig.Get("maxOutputTokens").Float())
	}
}

// ============================================================================
// Trailing Unsigned Thinking Block Removal
// ============================================================================

func TestConvertClaudeRequestToAntigravity_TrailingUnsignedThinking_Removed(t *testing.T) {
	// Last assistant message ends with unsigned thinking block - should be removed
	inputJSON := []byte(`{
		"model": "claude-sonnet-4-5-thinking",
		"messages": [
			{
				"role": "user",
				"content": [{"type": "text", "text": "Hello"}]
			},
			{
				"role": "assistant",
				"content": [
					{"type": "text", "text": "Here is my answer"},
					{"type": "thinking", "thinking": "I should think more..."}
				]
			}
		]
	}`)

	output := ConvertClaudeRequestToAntigravity("claude-sonnet-4-5-thinking", inputJSON, false)
	outputStr := string(output)

	// The last part of the last assistant message should NOT be a thinking block
	lastMessageParts := gjson.Get(outputStr, "request.contents.1.parts")
	if !lastMessageParts.IsArray() {
		t.Fatal("Last message should have parts array")
	}
	parts := lastMessageParts.Array()
	if len(parts) == 0 {
		t.Fatal("Last message should have at least one part")
	}

	// The unsigned thinking should be removed, leaving only the text
	lastPart := parts[len(parts)-1]
	if lastPart.Get("thought").Bool() {
		t.Error("Trailing unsigned thinking block should be removed")
	}
}

func TestConvertClaudeRequestToAntigravity_TrailingSignedThinking_Kept(t *testing.T) {
	cache.ClearSignatureCache("")

	// Last assistant message ends with signed thinking block - should be kept
	validSignature := "abc123validSignature1234567890123456789012345678901234567890"
	thinkingText := "Valid thinking..."

	inputJSON := []byte(`{
		"model": "claude-sonnet-4-5-thinking",
		"messages": [
			{
				"role": "user",
				"content": [{"type": "text", "text": "Hello"}]
			},
			{
				"role": "assistant",
				"content": [
					{"type": "text", "text": "Here is my answer"},
					{"type": "thinking", "thinking": "` + thinkingText + `", "signature": "` + validSignature + `"}
				]
			}
		]
	}`)

	cache.CacheSignature("claude-sonnet-4-5-thinking", thinkingText, validSignature)

	output := ConvertClaudeRequestToAntigravity("claude-sonnet-4-5-thinking", inputJSON, false)
	outputStr := string(output)

	// The signed thinking block should be preserved
	lastMessageParts := gjson.Get(outputStr, "request.contents.1.parts")
	parts := lastMessageParts.Array()
	if len(parts) < 2 {
		t.Error("Signed thinking block should be preserved")
	}
}

func TestConvertClaudeRequestToAntigravity_MiddleUnsignedThinking_Removed(t *testing.T) {
	// Middle message has unsigned thinking - should be removed entirely
	inputJSON := []byte(`{
		"model": "claude-sonnet-4-5-thinking",
		"messages": [
			{
				"role": "assistant",
				"content": [
					{"type": "thinking", "thinking": "Middle thinking..."},
					{"type": "text", "text": "Answer"}
				]
			},
			{
				"role": "user",
				"content": [{"type": "text", "text": "Follow up"}]
			}
		]
	}`)

	output := ConvertClaudeRequestToAntigravity("claude-sonnet-4-5-thinking", inputJSON, false)
	outputStr := string(output)

	// Unsigned thinking should be removed entirely
	parts := gjson.Get(outputStr, "request.contents.0.parts").Array()
	if len(parts) != 1 {
		t.Fatalf("Expected 1 part (thinking removed), got %d", len(parts))
	}

	// Only text part should remain
	if parts[0].Get("thought").Bool() {
		t.Error("Thinking block should be removed, not preserved")
	}
	if parts[0].Get("text").String() != "Answer" {
		t.Errorf("Expected text 'Answer', got '%s'", parts[0].Get("text").String())
	}
}

// ============================================================================
// Tool + Thinking System Hint Injection
// ============================================================================

func TestConvertClaudeRequestToAntigravity_ToolAndThinking_HintInjected(t *testing.T) {
	// When both tools and thinking are enabled, hint should be injected into system instruction
	inputJSON := []byte(`{
		"model": "claude-sonnet-4-5-thinking",
		"messages": [{"role": "user", "content": [{"type": "text", "text": "Hello"}]}],
		"system": [{"type": "text", "text": "You are helpful."}],
		"tools": [
			{
				"name": "get_weather",
				"description": "Get weather",
				"input_schema": {"type": "object", "properties": {"location": {"type": "string"}}}
			}
		],
		"thinking": {"type": "enabled", "budget_tokens": 8000}
	}`)

	output := ConvertClaudeRequestToAntigravity("claude-sonnet-4-5-thinking", inputJSON, false)
	outputStr := string(output)

	// System instruction should contain the interleaved thinking hint
	sysInstruction := gjson.Get(outputStr, "request.systemInstruction")
	if !sysInstruction.Exists() {
		t.Fatal("systemInstruction should exist")
	}

	// Check if hint is appended
	sysText := sysInstruction.Get("parts").Array()
	found := false
	for _, part := range sysText {
		if strings.Contains(part.Get("text").String(), "Interleaved thinking is enabled") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("Interleaved thinking hint should be injected when tools and thinking are both active, got: %v", sysInstruction.Raw)
	}
}

func TestConvertClaudeRequestToAntigravity_ToolsOnly_NoHint(t *testing.T) {
	// When only tools are present (no thinking), hint should NOT be injected
	inputJSON := []byte(`{
		"model": "claude-sonnet-4-5",
		"messages": [{"role": "user", "content": [{"type": "text", "text": "Hello"}]}],
		"system": [{"type": "text", "text": "You are helpful."}],
		"tools": [
			{
				"name": "get_weather",
				"description": "Get weather",
				"input_schema": {"type": "object", "properties": {"location": {"type": "string"}}}
			}
		]
	}`)

	output := ConvertClaudeRequestToAntigravity("claude-sonnet-4-5", inputJSON, false)
	outputStr := string(output)

	// System instruction should NOT contain the hint
	sysInstruction := gjson.Get(outputStr, "request.systemInstruction")
	if sysInstruction.Exists() {
		for _, part := range sysInstruction.Get("parts").Array() {
			if strings.Contains(part.Get("text").String(), "Interleaved thinking is enabled") {
				t.Error("Hint should NOT be injected when only tools are present (no thinking)")
			}
		}
	}
}

func TestConvertClaudeRequestToAntigravity_ThinkingOnly_NoHint(t *testing.T) {
	// When only thinking is enabled (no tools), hint should NOT be injected
	inputJSON := []byte(`{
		"model": "claude-sonnet-4-5-thinking",
		"messages": [{"role": "user", "content": [{"type": "text", "text": "Hello"}]}],
		"system": [{"type": "text", "text": "You are helpful."}],
		"thinking": {"type": "enabled", "budget_tokens": 8000}
	}`)

	output := ConvertClaudeRequestToAntigravity("claude-sonnet-4-5-thinking", inputJSON, false)
	outputStr := string(output)

	// System instruction should NOT contain the hint (no tools)
	sysInstruction := gjson.Get(outputStr, "request.systemInstruction")
	if sysInstruction.Exists() {
		for _, part := range sysInstruction.Get("parts").Array() {
			if strings.Contains(part.Get("text").String(), "Interleaved thinking is enabled") {
				t.Error("Hint should NOT be injected when only thinking is present (no tools)")
			}
		}
	}
}

func TestConvertClaudeRequestToAntigravity_ToolResultNoContent(t *testing.T) {
	// Bug repro: tool_result with no content field produces invalid JSON
	inputJSON := []byte(`{
		"model": "claude-opus-4-6-thinking",
		"messages": [
			{
				"role": "assistant",
				"content": [
					{
						"type": "tool_use",
						"id": "MyTool-123-456",
						"name": "MyTool",
						"input": {"key": "value"}
					}
				]
			},
			{
				"role": "user",
				"content": [
					{
						"type": "tool_result",
						"tool_use_id": "MyTool-123-456"
					}
				]
			}
		]
	}`)

	output := ConvertClaudeRequestToAntigravity("claude-opus-4-6-thinking", inputJSON, true)
	outputStr := string(output)

	if !gjson.Valid(outputStr) {
		t.Errorf("Result is not valid JSON:\n%s", outputStr)
	}

	// Verify the functionResponse has a valid result value
	fr := gjson.Get(outputStr, "request.contents.1.parts.0.functionResponse.response.result")
	if !fr.Exists() {
		t.Error("functionResponse.response.result should exist")
	}
}

func TestConvertClaudeRequestToAntigravity_ToolResultNullContent(t *testing.T) {
	// Bug repro: tool_result with null content produces invalid JSON
	inputJSON := []byte(`{
		"model": "claude-opus-4-6-thinking",
		"messages": [
			{
				"role": "assistant",
				"content": [
					{
						"type": "tool_use",
						"id": "MyTool-123-456",
						"name": "MyTool",
						"input": {"key": "value"}
					}
				]
			},
			{
				"role": "user",
				"content": [
					{
						"type": "tool_result",
						"tool_use_id": "MyTool-123-456",
						"content": null
					}
				]
			}
		]
	}`)

	output := ConvertClaudeRequestToAntigravity("claude-opus-4-6-thinking", inputJSON, true)
	outputStr := string(output)

	if !gjson.Valid(outputStr) {
		t.Errorf("Result is not valid JSON:\n%s", outputStr)
	}
}

func TestConvertClaudeRequestToAntigravity_ToolResultWithImage(t *testing.T) {
	// tool_result with array content containing text + image should place
	// image data inside functionResponse.parts as inlineData, not as a
	// sibling part in the outer content (to avoid base64 context bloat).
	inputJSON := []byte(`{
		"model": "claude-3-5-sonnet-20240620",
		"messages": [
			{
				"role": "user",
				"content": [
					{
						"type": "tool_result",
						"tool_use_id": "Read-123-456",
						"content": [
							{
								"type": "text",
								"text": "File content here"
							},
							{
								"type": "image",
								"source": {
									"type": "base64",
									"media_type": "image/png",
									"data": "iVBORw0KGgoAAAANSUhEUg=="
								}
							}
						]
					}
				]
			}
		]
	}`)

	output := ConvertClaudeRequestToAntigravity("claude-sonnet-4-5", inputJSON, false)
	outputStr := string(output)

	if !gjson.Valid(outputStr) {
		t.Fatalf("Result is not valid JSON:\n%s", outputStr)
	}

	// Image should be inside functionResponse.parts, not as outer sibling part
	funcResp := gjson.Get(outputStr, "request.contents.0.parts.0.functionResponse")
	if !funcResp.Exists() {
		t.Fatal("functionResponse should exist")
	}

	// Text content should be in response.result
	resultText := funcResp.Get("response.result.text").String()
	if resultText != "File content here" {
		t.Errorf("Expected response.result.text = 'File content here', got '%s'", resultText)
	}

	// Image should be in functionResponse.parts[0].inlineData
	inlineData := funcResp.Get("parts.0.inlineData")
	if !inlineData.Exists() {
		t.Fatal("functionResponse.parts[0].inlineData should exist")
	}
	if inlineData.Get("mimeType").String() != "image/png" {
		t.Errorf("Expected mimeType 'image/png', got '%s'", inlineData.Get("mimeType").String())
	}
	if !strings.Contains(inlineData.Get("data").String(), "iVBORw0KGgo") {
		t.Error("data mismatch")
	}

	// Image should NOT be in outer parts (only functionResponse part should exist)
	outerParts := gjson.Get(outputStr, "request.contents.0.parts")
	if outerParts.IsArray() && len(outerParts.Array()) > 1 {
		t.Errorf("Expected only 1 outer part (functionResponse), got %d", len(outerParts.Array()))
	}
}

func TestConvertClaudeRequestToAntigravity_ToolResultWithSingleImage(t *testing.T) {
	// tool_result with single image object as content should place
	// image data inside functionResponse.parts, not as outer sibling part.
	inputJSON := []byte(`{
		"model": "claude-3-5-sonnet-20240620",
		"messages": [
			{
				"role": "user",
				"content": [
					{
						"type": "tool_result",
						"tool_use_id": "Read-789-012",
						"content": {
							"type": "image",
							"source": {
								"type": "base64",
								"media_type": "image/jpeg",
								"data": "/9j/4AAQSkZJRgABAQ=="
							}
						}
					}
				]
			}
		]
	}`)

	output := ConvertClaudeRequestToAntigravity("claude-sonnet-4-5", inputJSON, false)
	outputStr := string(output)

	if !gjson.Valid(outputStr) {
		t.Fatalf("Result is not valid JSON:\n%s", outputStr)
	}

	funcResp := gjson.Get(outputStr, "request.contents.0.parts.0.functionResponse")
	if !funcResp.Exists() {
		t.Fatal("functionResponse should exist")
	}

	// response.result should be empty (image only)
	if funcResp.Get("response.result").String() != "" {
		t.Errorf("Expected empty response.result for image-only content, got '%s'", funcResp.Get("response.result").String())
	}

	// Image should be in functionResponse.parts[0].inlineData
	inlineData := funcResp.Get("parts.0.inlineData")
	if !inlineData.Exists() {
		t.Fatal("functionResponse.parts[0].inlineData should exist")
	}
	if inlineData.Get("mimeType").String() != "image/jpeg" {
		t.Errorf("Expected mimeType 'image/jpeg', got '%s'", inlineData.Get("mimeType").String())
	}

	// Image should NOT be in outer parts
	outerParts := gjson.Get(outputStr, "request.contents.0.parts")
	if outerParts.IsArray() && len(outerParts.Array()) > 1 {
		t.Errorf("Expected only 1 outer part, got %d", len(outerParts.Array()))
	}
}

func TestConvertClaudeRequestToAntigravity_ToolResultWithMultipleImagesAndTexts(t *testing.T) {
	// tool_result with array content: 2 text items + 2 images
	// All images go into functionResponse.parts, texts into response.result array
	inputJSON := []byte(`{
		"model": "claude-3-5-sonnet-20240620",
		"messages": [
			{
				"role": "user",
				"content": [
					{
						"type": "tool_result",
						"tool_use_id": "Multi-001",
						"content": [
							{"type": "text", "text": "First text"},
							{
								"type": "image",
								"source": {"type": "base64", "media_type": "image/png", "data": "AAAA"}
							},
							{"type": "text", "text": "Second text"},
							{
								"type": "image",
								"source": {"type": "base64", "media_type": "image/jpeg", "data": "BBBB"}
							}
						]
					}
				]
			}
		]
	}`)

	output := ConvertClaudeRequestToAntigravity("claude-sonnet-4-5", inputJSON, false)
	outputStr := string(output)

	if !gjson.Valid(outputStr) {
		t.Fatalf("Result is not valid JSON:\n%s", outputStr)
	}

	funcResp := gjson.Get(outputStr, "request.contents.0.parts.0.functionResponse")
	if !funcResp.Exists() {
		t.Fatal("functionResponse should exist")
	}

	// Multiple text items => response.result is an array
	resultArr := funcResp.Get("response.result")
	if !resultArr.IsArray() {
		t.Fatalf("Expected response.result to be an array, got: %s", resultArr.Raw)
	}
	results := resultArr.Array()
	if len(results) != 2 {
		t.Fatalf("Expected 2 result items, got %d", len(results))
	}

	// Both images should be in functionResponse.parts
	imgParts := funcResp.Get("parts").Array()
	if len(imgParts) != 2 {
		t.Fatalf("Expected 2 image parts in functionResponse.parts, got %d", len(imgParts))
	}
	if imgParts[0].Get("inlineData.mimeType").String() != "image/png" {
		t.Errorf("Expected first image mimeType 'image/png', got '%s'", imgParts[0].Get("inlineData.mimeType").String())
	}
	if imgParts[0].Get("inlineData.data").String() != "AAAA" {
		t.Errorf("Expected first image data 'AAAA', got '%s'", imgParts[0].Get("inlineData.data").String())
	}
	if imgParts[1].Get("inlineData.mimeType").String() != "image/jpeg" {
		t.Errorf("Expected second image mimeType 'image/jpeg', got '%s'", imgParts[1].Get("inlineData.mimeType").String())
	}
	if imgParts[1].Get("inlineData.data").String() != "BBBB" {
		t.Errorf("Expected second image data 'BBBB', got '%s'", imgParts[1].Get("inlineData.data").String())
	}

	// Only 1 outer part (the functionResponse itself)
	outerParts := gjson.Get(outputStr, "request.contents.0.parts").Array()
	if len(outerParts) != 1 {
		t.Errorf("Expected 1 outer part, got %d", len(outerParts))
	}
}

func TestConvertClaudeRequestToAntigravity_ToolResultWithOnlyMultipleImages(t *testing.T) {
	// tool_result with only images (no text) — response.result should be empty string
	inputJSON := []byte(`{
		"model": "claude-3-5-sonnet-20240620",
		"messages": [
			{
				"role": "user",
				"content": [
					{
						"type": "tool_result",
						"tool_use_id": "ImgOnly-001",
						"content": [
							{
								"type": "image",
								"source": {"type": "base64", "media_type": "image/png", "data": "PNG1"}
							},
							{
								"type": "image",
								"source": {"type": "base64", "media_type": "image/gif", "data": "GIF1"}
							}
						]
					}
				]
			}
		]
	}`)

	output := ConvertClaudeRequestToAntigravity("claude-sonnet-4-5", inputJSON, false)
	outputStr := string(output)

	if !gjson.Valid(outputStr) {
		t.Fatalf("Result is not valid JSON:\n%s", outputStr)
	}

	funcResp := gjson.Get(outputStr, "request.contents.0.parts.0.functionResponse")
	if !funcResp.Exists() {
		t.Fatal("functionResponse should exist")
	}

	// No text => response.result should be empty string
	if funcResp.Get("response.result").String() != "" {
		t.Errorf("Expected empty response.result, got '%s'", funcResp.Get("response.result").String())
	}

	// Both images in functionResponse.parts
	imgParts := funcResp.Get("parts").Array()
	if len(imgParts) != 2 {
		t.Fatalf("Expected 2 image parts, got %d", len(imgParts))
	}
	if imgParts[0].Get("inlineData.mimeType").String() != "image/png" {
		t.Error("first image mimeType mismatch")
	}
	if imgParts[1].Get("inlineData.mimeType").String() != "image/gif" {
		t.Error("second image mimeType mismatch")
	}

	// Only 1 outer part
	outerParts := gjson.Get(outputStr, "request.contents.0.parts").Array()
	if len(outerParts) != 1 {
		t.Errorf("Expected 1 outer part, got %d", len(outerParts))
	}
}

func TestConvertClaudeRequestToAntigravity_ToolResultImageNotBase64(t *testing.T) {
	// image with source.type != "base64" should be treated as non-image (falls through)
	inputJSON := []byte(`{
		"model": "claude-3-5-sonnet-20240620",
		"messages": [
			{
				"role": "user",
				"content": [
					{
						"type": "tool_result",
						"tool_use_id": "NotB64-001",
						"content": [
							{"type": "text", "text": "some output"},
							{
								"type": "image",
								"source": {"type": "url", "url": "https://example.com/img.png"}
							}
						]
					}
				]
			}
		]
	}`)

	output := ConvertClaudeRequestToAntigravity("claude-sonnet-4-5", inputJSON, false)
	outputStr := string(output)

	if !gjson.Valid(outputStr) {
		t.Fatalf("Result is not valid JSON:\n%s", outputStr)
	}

	funcResp := gjson.Get(outputStr, "request.contents.0.parts.0.functionResponse")
	if !funcResp.Exists() {
		t.Fatal("functionResponse should exist")
	}

	// Non-base64 image is treated as non-image, so it goes into the filtered results
	// along with the text item. Since there are 2 non-image items, result is array.
	resultArr := funcResp.Get("response.result")
	if !resultArr.IsArray() {
		t.Fatalf("Expected response.result to be an array (2 non-image items), got: %s", resultArr.Raw)
	}
	results := resultArr.Array()
	if len(results) != 2 {
		t.Fatalf("Expected 2 result items, got %d", len(results))
	}

	// No functionResponse.parts (no base64 images collected)
	if funcResp.Get("parts").Exists() {
		t.Error("functionResponse.parts should NOT exist when no base64 images")
	}
}

func TestConvertClaudeRequestToAntigravity_ToolResultImageMissingData(t *testing.T) {
	// image with source.type=base64 but missing data field
	inputJSON := []byte(`{
		"model": "claude-3-5-sonnet-20240620",
		"messages": [
			{
				"role": "user",
				"content": [
					{
						"type": "tool_result",
						"tool_use_id": "NoData-001",
						"content": [
							{"type": "text", "text": "output"},
							{
								"type": "image",
								"source": {"type": "base64", "media_type": "image/png"}
							}
						]
					}
				]
			}
		]
	}`)

	output := ConvertClaudeRequestToAntigravity("claude-sonnet-4-5", inputJSON, false)
	outputStr := string(output)

	if !gjson.Valid(outputStr) {
		t.Fatalf("Result is not valid JSON:\n%s", outputStr)
	}

	funcResp := gjson.Get(outputStr, "request.contents.0.parts.0.functionResponse")
	if !funcResp.Exists() {
		t.Fatal("functionResponse should exist")
	}

	// The image is still classified as base64 image (type check passes),
	// but data field is missing => inlineData has mimeType but no data
	imgParts := funcResp.Get("parts").Array()
	if len(imgParts) != 1 {
		t.Fatalf("Expected 1 image part, got %d", len(imgParts))
	}
	if imgParts[0].Get("inlineData.mimeType").String() != "image/png" {
		t.Error("mimeType should still be set")
	}
	if imgParts[0].Get("inlineData.data").Exists() {
		t.Error("data should not exist when source.data is missing")
	}
}

func TestConvertClaudeRequestToAntigravity_ToolResultImageMissingMediaType(t *testing.T) {
	// image with source.type=base64 but missing media_type field
	inputJSON := []byte(`{
		"model": "claude-3-5-sonnet-20240620",
		"messages": [
			{
				"role": "user",
				"content": [
					{
						"type": "tool_result",
						"tool_use_id": "NoMime-001",
						"content": [
							{"type": "text", "text": "output"},
							{
								"type": "image",
								"source": {"type": "base64", "data": "AAAA"}
							}
						]
					}
				]
			}
		]
	}`)

	output := ConvertClaudeRequestToAntigravity("claude-sonnet-4-5", inputJSON, false)
	outputStr := string(output)

	if !gjson.Valid(outputStr) {
		t.Fatalf("Result is not valid JSON:\n%s", outputStr)
	}

	funcResp := gjson.Get(outputStr, "request.contents.0.parts.0.functionResponse")
	if !funcResp.Exists() {
		t.Fatal("functionResponse should exist")
	}

	// The image is still classified as base64 image,
	// but media_type is missing => inlineData has data but no mimeType
	imgParts := funcResp.Get("parts").Array()
	if len(imgParts) != 1 {
		t.Fatalf("Expected 1 image part, got %d", len(imgParts))
	}
	if imgParts[0].Get("inlineData.mimeType").Exists() {
		t.Error("mimeType should not exist when media_type is missing")
	}
	if imgParts[0].Get("inlineData.data").String() != "AAAA" {
		t.Error("data should still be set")
	}
}

func TestConvertClaudeRequestToAntigravity_ToolAndThinking_NoExistingSystem(t *testing.T) {
	// When tools + thinking but no system instruction, should create one with hint
	inputJSON := []byte(`{
		"model": "claude-sonnet-4-5-thinking",
		"messages": [{"role": "user", "content": [{"type": "text", "text": "Hello"}]}],
		"tools": [
			{
				"name": "get_weather",
				"description": "Get weather",
				"input_schema": {"type": "object", "properties": {"location": {"type": "string"}}}
			}
		],
		"thinking": {"type": "enabled", "budget_tokens": 8000}
	}`)

	output := ConvertClaudeRequestToAntigravity("claude-sonnet-4-5-thinking", inputJSON, false)
	outputStr := string(output)

	// System instruction should be created with hint
	sysInstruction := gjson.Get(outputStr, "request.systemInstruction")
	if !sysInstruction.Exists() {
		t.Fatal("systemInstruction should be created when tools + thinking are active")
	}

	sysText := sysInstruction.Get("parts").Array()
	found := false
	for _, part := range sysText {
		if strings.Contains(part.Get("text").String(), "Interleaved thinking is enabled") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("Interleaved thinking hint should be in created systemInstruction, got: %v", sysInstruction.Raw)
	}
}

// ============================================================================
// normalizeSignature Tests
// ============================================================================

func TestNormalizeSignature_CLIProxyAPIFormat(t *testing.T) {
	// "claude#..." format should extract the signature after "#"
	sig := "claude#R1234567890abcdef1234567890abcdef1234567890abcdef12"
	result := normalizeSignature(sig)

	expected := "R1234567890abcdef1234567890abcdef1234567890abcdef12"
	if result != expected {
		t.Errorf("Expected '%s', got '%s'", expected, result)
	}
}

func TestNormalizeSignature_GeminiFormat(t *testing.T) {
	// "gemini#..." format should extract the signature after "#"
	sig := "gemini#R1234567890abcdef1234567890abcdef1234567890abcdef12"
	result := normalizeSignature(sig)

	expected := "R1234567890abcdef1234567890abcdef1234567890abcdef12"
	if result != expected {
		t.Errorf("Expected '%s', got '%s'", expected, result)
	}
}

func TestNormalizeSignature_AnthropicFormat(t *testing.T) {
	// "E..." format (Anthropic direct, 1-layer Base64)
	// Should be Base64 encoded to 2-layer format (no prefix)
	sig := "EjIxMjM0NTY3ODkwYWJjZGVmMTIzNDU2Nzg5MGFiY2RlZjEyMzQ1Njc4OTBhYmNkZWYxMg=="

	result := normalizeSignature(sig)

	// Result should start with "R" (Base64 of "E" starts with "R")
	if !strings.HasPrefix(result, "R") {
		t.Errorf("Expected result to start with 'R', got '%s'", result)
	}
	// Encoded part should be longer than original (Base64 encoding adds ~33% overhead)
	if len(result) <= len(sig) {
		t.Errorf("Base64 encoded should be longer: input=%d, output=%d", len(sig), len(result))
	}
}

func TestNormalizeSignature_GoogleVertexFormat(t *testing.T) {
	// "R..." format (Google Vertex, 2-layer Base64)
	// Should return as-is (already correct format)
	sig := "R1234567890abcdef1234567890abcdef1234567890abcdef12"
	result := normalizeSignature(sig)

	if result != sig {
		t.Errorf("Expected '%s', got '%s'", sig, result)
	}
}

func TestNormalizeSignature_UnknownFormat(t *testing.T) {
	// Unknown format should be returned as-is (let original logic handle it)
	testCases := []string{
		"X1234567890",
		"1234567890",
		"abc",
	}

	for _, sig := range testCases {
		result := normalizeSignature(sig)
		if result != sig {
			t.Errorf("Expected '%s' (same as input), got '%s'", sig, result)
		}
	}
}

func TestNormalizeSignature_EmptyString(t *testing.T) {
	// Empty string should return empty
	result := normalizeSignature("")
	if result != "" {
		t.Errorf("Expected empty for empty input, got '%s'", result)
	}
}

func TestNormalizeSignature_CLIProxyAPIFormat_EmptyAfterPrefix(t *testing.T) {
	// "claude#" with nothing after should extract empty string
	sig := "claude#"
	result := normalizeSignature(sig)

	if result != "" {
		t.Errorf("Expected empty string, got '%s'", result)
	}
}

func TestNormalizeSignature_RoundTrip_AnthropicToGoogle(t *testing.T) {
	// Simulate: Anthropic returns E... -> we encode -> result starts with R
	anthropicSig := "EtgCCkgICxACGAIqQGF8Wm8HDPN/PnZe6Mv5SGcFreSRLSo8/i5qfxfx7dOxRoZGOQ"

	result := normalizeSignature(anthropicSig)

	// Result should start with R (base64 of 'E' = 0x45 = 69)
	// base64("E") = "RQ==" so base64("Etg...") starts with "R"
	if !strings.HasPrefix(result, "R") {
		t.Errorf("Encoded signature should start with 'R', got prefix '%c'", result[0])
	}
}

func TestConvertClaudeRequestToAntigravity_SignaturePassthrough(t *testing.T) {
	cache.ClearSignatureCache("")

	// Test that signature is correctly passed through to the output
	validSig := "claude#R1234567890abcdef1234567890abcdef1234567890abcdef12"

	inputJSON := []byte(`{
		"model": "claude-sonnet-4-5-thinking",
		"messages": [
			{
				"role": "assistant",
				"content": [
					{"type": "thinking", "thinking": "Let me think...", "signature": "` + validSig + `"},
					{"type": "text", "text": "Answer"}
				]
			}
		]
	}`)

	output := ConvertClaudeRequestToAntigravity("claude-sonnet-4-5-thinking", inputJSON, false)
	outputStr := string(output)

	// Check thinking block has thoughtSignature
	thoughtSig := gjson.Get(outputStr, "request.contents.0.parts.0.thoughtSignature").String()

	// Should be the part after "claude#"
	expectedSig := "R1234567890abcdef1234567890abcdef1234567890abcdef12"
	if thoughtSig != expectedSig {
		t.Errorf("Expected thoughtSignature '%s', got '%s'", expectedSig, thoughtSig)
	}
}

func TestConvertClaudeRequestToAntigravity_AnthropicSignatureFormat(t *testing.T) {
	original := cache.SignatureCacheEnabled()
	defer cache.SetSignatureCacheEnabled(original)

	cache.SetSignatureCacheEnabled(false)

	anthropicSig := "EoYDCkYIDBgCAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="

	inputJSON := []byte(`{
		"model": "claude-sonnet-4-5-thinking",
		"messages": [
			{
				"role": "assistant",
				"content": [
					{"type": "thinking", "thinking": "Thinking...", "signature": "` + anthropicSig + `"},
					{"type": "text", "text": "Answer"}
				]
			}
		]
	}`)

	output := ConvertClaudeRequestToAntigravity("claude-sonnet-4-5-thinking", inputJSON, false)
	outputStr := string(output)

	thoughtSig := gjson.Get(outputStr, "request.contents.0.parts.0.thoughtSignature").String()

	if !strings.HasPrefix(thoughtSig, "R") {
		t.Errorf("Expected thoughtSignature to start with 'R' (base64 of E...), got '%s'", thoughtSig)
	}

	if strings.Contains(thoughtSig, "#") {
		t.Errorf("thoughtSignature should not contain '#', got '%s'", thoughtSig)
	}
}

func TestConvertClaudeRequestToAntigravity_ToolUseInheritsSignature(t *testing.T) {
	// Test that tool_use inherits signature from preceding thinking block
	validSig := "claude#R1234567890abcdef1234567890abcdef1234567890abcdef12"

	inputJSON := []byte(`{
		"model": "claude-sonnet-4-5-thinking",
		"messages": [
			{
				"role": "assistant",
				"content": [
					{"type": "thinking", "thinking": "Let me use a tool...", "signature": "` + validSig + `"},
					{"type": "tool_use", "id": "tool_123", "name": "test_tool", "input": {"arg": "value"}}
				]
			}
		]
	}`)

	output := ConvertClaudeRequestToAntigravity("claude-sonnet-4-5-thinking", inputJSON, false)
	outputStr := string(output)

	// Both thinking and tool_use should have the same signature
	thinkingSig := gjson.Get(outputStr, "request.contents.0.parts.0.thoughtSignature").String()
	toolSig := gjson.Get(outputStr, "request.contents.0.parts.1.thoughtSignature").String()

	expectedSig := "R1234567890abcdef1234567890abcdef1234567890abcdef12"

	if thinkingSig != expectedSig {
		t.Errorf("Thinking thoughtSignature expected '%s', got '%s'", expectedSig, thinkingSig)
	}

	if toolSig != expectedSig {
		t.Errorf("Tool thoughtSignature expected '%s', got '%s'", expectedSig, toolSig)
	}
}

// ============================================================================
// SignatureCacheEnabled Toggle Tests
// ============================================================================

func TestSignatureCacheEnabled_DefaultTrue(t *testing.T) {
	if !cache.SignatureCacheEnabled() {
		t.Error("SignatureCacheEnabled should be true by default")
	}
}

func TestSignatureCacheEnabled_Toggle(t *testing.T) {
	original := cache.SignatureCacheEnabled()
	defer cache.SetSignatureCacheEnabled(original)

	cache.SetSignatureCacheEnabled(false)
	if cache.SignatureCacheEnabled() {
		t.Error("SignatureCacheEnabled should be false after SetSignatureCacheEnabled(false)")
	}

	cache.SetSignatureCacheEnabled(true)
	if !cache.SignatureCacheEnabled() {
		t.Error("SignatureCacheEnabled should be true after SetSignatureCacheEnabled(true)")
	}
}

func TestConvertClaudeRequestToAntigravity_CacheEnabled_DropsUnsigned(t *testing.T) {
	cache.ClearSignatureCache("")
	original := cache.SignatureCacheEnabled()
	defer cache.SetSignatureCacheEnabled(original)

	cache.SetSignatureCacheEnabled(true)

	inputJSON := []byte(`{
		"model": "claude-sonnet-4-5-thinking",
		"messages": [
			{
				"role": "assistant",
				"content": [
					{"type": "thinking", "thinking": "Let me think..."},
					{"type": "text", "text": "Answer"}
				]
			}
		]
	}`)

	output := ConvertClaudeRequestToAntigravity("claude-sonnet-4-5-thinking", inputJSON, false)
	outputStr := string(output)

	parts := gjson.Get(outputStr, "request.contents.0.parts").Array()
	if len(parts) != 1 {
		t.Fatalf("With cache enabled, expected 1 part (thinking dropped), got %d", len(parts))
	}

	if parts[0].Get("thought").Bool() {
		t.Error("Thinking block should be removed when unsigned and cache enabled")
	}
	if parts[0].Get("text").String() != "Answer" {
		t.Errorf("Expected text 'Answer', got '%s'", parts[0].Get("text").String())
	}
}

func TestConvertClaudeRequestToAntigravity_CacheDisabled_NormalizesAnthropicSignature(t *testing.T) {
	cache.ClearSignatureCache("")
	original := cache.SignatureCacheEnabled()
	defer cache.SetSignatureCacheEnabled(original)

	cache.SetSignatureCacheEnabled(false)

	// Valid Claude signature with 0x12 first byte
	anthropicSig := "EoYDCkYIDBgCAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="

	inputJSON := []byte(`{
		"model": "claude-sonnet-4-5-thinking",
		"messages": [
			{
				"role": "assistant",
				"content": [
					{"type": "thinking", "thinking": "Thinking...", "signature": "` + anthropicSig + `"},
					{"type": "text", "text": "Answer"}
				]
			}
		]
	}`)

	output := ConvertClaudeRequestToAntigravity("claude-sonnet-4-5-thinking", inputJSON, false)
	outputStr := string(output)

	thoughtSig := gjson.Get(outputStr, "request.contents.0.parts.0.thoughtSignature").String()

	if !strings.HasPrefix(thoughtSig, "R") {
		t.Errorf("Expected thoughtSignature to start with 'R' (base64 of E...), got '%s'", thoughtSig)
	}

	if strings.Contains(thoughtSig, "#") {
		t.Errorf("thoughtSignature should not contain '#', got '%s'", thoughtSig)
	}
}

// ============================================================================
// isValidClaudeSignature Tests
// ============================================================================

func TestIsValidClaudeSignature_ValidAnthropicMaxFormat(t *testing.T) {
	// Valid Max subscription: 0x12 prefix, Channel=12
	sig := "EoYDCkYIDBgCAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
	if !isValidClaudeSignature(sig) {
		t.Error("Valid Anthropic Max signature should pass validation")
	}
}

func TestIsValidClaudeSignature_ValidAnthropicAPIFormat(t *testing.T) {
	// Valid API: 0x12 prefix, Channel=11
	sig := "EoUICkYICxgCAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
	if !isValidClaudeSignature(sig) {
		t.Error("Valid Anthropic API signature should pass validation")
	}
}

func TestIsValidClaudeSignature_ValidAWSBedrockFormat(t *testing.T) {
	// Valid AWS Bedrock: 0x12 prefix, Channel=11, Field2=1
	sig := "Et0DCkgICxABGAIAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=="
	if !isValidClaudeSignature(sig) {
		t.Error("Valid AWS Bedrock signature should pass validation")
	}
}

func TestIsValidClaudeSignature_ValidGoogleVertexFormat(t *testing.T) {
	// Valid Google Vertex: 0x12 prefix, Channel=11, Field2=2
	sig := "EpUMCkgICxACGAIAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=="
	if !isValidClaudeSignature(sig) {
		t.Error("Valid Google Vertex 1-layer signature should pass validation")
	}
}

func TestIsValidClaudeSignature_ValidVertexTwoLayerFormat(t *testing.T) {
	// Valid 2-layer: R... that decodes to E... which decodes to 0x12 prefix
	innerSig := "EpUMCkgICxACGAIAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=="
	twoLayerSig := base64.StdEncoding.EncodeToString([]byte(innerSig))

	if !strings.HasPrefix(twoLayerSig, "R") {
		t.Fatalf("Test setup error: 2-layer signature should start with R, got %c", twoLayerSig[0])
	}

	if !isValidClaudeSignature(twoLayerSig) {
		t.Error("Valid 2-layer Vertex signature should pass validation")
	}
}

func TestIsValidClaudeSignature_WithCLIProxyAPIPrefix(t *testing.T) {
	// Valid signature with "claude#" prefix
	innerSig := "EoYDCkYIDBgCAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
	sig := "claude#" + innerSig

	if !isValidClaudeSignature(sig) {
		t.Error("Valid signature with 'claude#' prefix should pass validation")
	}
}

func TestIsValidClaudeSignature_InvalidEmptyString(t *testing.T) {
	if isValidClaudeSignature("") {
		t.Error("Empty signature should fail validation")
	}
}

func TestIsValidClaudeSignature_InvalidTooShort(t *testing.T) {
	if isValidClaudeSignature("EoAB") {
		t.Error("Short signature should fail validation")
	}
}

func TestIsValidClaudeSignature_InvalidWrongPrefix(t *testing.T) {
	// Does not start with E or R
	sig := "XoYDCkYIDBgCKkCTiMT2RGJPOfSxH3FmzDTRRwVQ5C2l8QHba5Ukg"
	if isValidClaudeSignature(sig) {
		t.Error("Signature with wrong prefix should fail validation")
	}
}

func TestIsValidClaudeSignature_InvalidProtobufStructure(t *testing.T) {
	// Valid Base64 but wrong first byte (not 0x12)
	// base64("ABC...") = first byte is 'A' = 0x41, not 0x12
	sig := "QUJDREVGMTIzNDU2Nzg5MDEyMzQ1Njc4OTAxMjM0NTY3ODkwMTIzNDU2Nzg5MA=="
	if isValidClaudeSignature(sig) {
		t.Error("Signature with wrong Protobuf structure should fail validation")
	}
}

func TestIsValidClaudeSignature_InvalidBase64(t *testing.T) {
	// E prefix but invalid Base64 content
	sig := "E!!!invalidbase64!!!1234567890123456789012345678901234567890"
	if isValidClaudeSignature(sig) {
		t.Error("Invalid Base64 signature should fail validation")
	}
}

func TestIsValidClaudeSignature_InvalidTwoLayerFirstLayerNotE(t *testing.T) {
	// R... but first layer doesn't decode to E...
	innerContent := "XYZnotEformat123456789012345678901234567890123456"
	twoLayerSig := base64.StdEncoding.EncodeToString([]byte(innerContent))

	if isValidClaudeSignature(twoLayerSig) {
		t.Error("2-layer signature with invalid inner format should fail validation")
	}
}

func TestConvertClaudeRequestToAntigravity_CacheDisabled_InvalidSignatureDropsThinking(t *testing.T) {
	cache.ClearSignatureCache("")
	original := cache.SignatureCacheEnabled()
	defer cache.SetSignatureCacheEnabled(original)

	cache.SetSignatureCacheEnabled(false)

	invalidSig := "invalid_signature"

	inputJSON := []byte(`{
		"model": "claude-sonnet-4-5-thinking",
		"messages": [
			{
				"role": "assistant",
				"content": [
					{"type": "thinking", "thinking": "Thinking...", "signature": "` + invalidSig + `"},
					{"type": "text", "text": "Answer"}
				]
			}
		]
	}`)

	output := ConvertClaudeRequestToAntigravity("claude-sonnet-4-5-thinking", inputJSON, false)
	outputStr := string(output)

	parts := gjson.Get(outputStr, "request.contents.0.parts").Array()
	if len(parts) != 1 {
		t.Fatalf("With invalid signature in bypass mode, expected 1 part (thinking dropped), got %d", len(parts))
	}

	if parts[0].Get("text").String() != "Answer" {
		t.Errorf("Expected text 'Answer', got '%s'", parts[0].Get("text").String())
	}
}

func TestConvertClaudeRequestToAntigravity_CacheDisabled_UnsignedDropsThinking(t *testing.T) {
	cache.ClearSignatureCache("")
	original := cache.SignatureCacheEnabled()
	defer cache.SetSignatureCacheEnabled(original)

	cache.SetSignatureCacheEnabled(false)

	inputJSON := []byte(`{
		"model": "claude-sonnet-4-5-thinking",
		"messages": [
			{
				"role": "assistant",
				"content": [
					{"type": "thinking", "thinking": "Let me think..."},
					{"type": "text", "text": "Answer"}
				]
			}
		]
	}`)

	output := ConvertClaudeRequestToAntigravity("claude-sonnet-4-5-thinking", inputJSON, false)
	outputStr := string(output)

	parts := gjson.Get(outputStr, "request.contents.0.parts").Array()
	if len(parts) != 1 {
		t.Fatalf("With no signature in bypass mode, expected 1 part (thinking dropped), got %d", len(parts))
	}

	if parts[0].Get("text").String() != "Answer" {
		t.Errorf("Expected text 'Answer', got '%s'", parts[0].Get("text").String())
	}
}

// ============================================================================
// Adaptive Thinking Mode Tests
// ============================================================================

func TestAdaptiveEffortToBudget(t *testing.T) {
	tests := []struct {
		effort   string
		expected int
	}{
		{"low", 4096},
		{"medium", 16384},
		{"high", 32768},
		{"max", 63998},
		{"LOW", 4096},
		{"High", 32768},
		{"", 32768},
		{"unknown", 32768},
	}

	for _, tt := range tests {
		tt := tt
		t.Run("effort_"+tt.effort, func(t *testing.T) {
			result := thinking.AdaptiveEffortToBudget(tt.effort)
			if result != tt.expected {
				t.Errorf("adaptiveEffortToBudget(%q) = %d, want %d", tt.effort, result, tt.expected)
			}
		})
	}
}

func TestConvertClaudeRequestToAntigravity_AdaptiveThinking_EffortLevels(t *testing.T) {
	tests := []struct {
		name     string
		effort   string
		expected string
	}{
		{"low", "low", "low"},
		{"medium", "medium", "medium"},
		{"high", "high", "high"},
		{"max", "max", "max"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			inputJSON := []byte(`{
				"model": "claude-opus-4-6-thinking",
				"messages": [{"role": "user", "content": [{"type": "text", "text": "Hello"}]}],
				"thinking": {"type": "adaptive"},
				"output_config": {"effort": "` + tt.effort + `"}
			}`)

			output := ConvertClaudeRequestToAntigravity("claude-opus-4-6-thinking", inputJSON, false)
			outputStr := string(output)

			thinkingConfig := gjson.Get(outputStr, "request.generationConfig.thinkingConfig")
			if !thinkingConfig.Exists() {
				t.Fatal("thinkingConfig should exist for adaptive thinking")
			}
			if thinkingConfig.Get("thinkingLevel").String() != tt.expected {
				t.Errorf("Expected thinkingLevel %q, got %q", tt.expected, thinkingConfig.Get("thinkingLevel").String())
			}
			if !thinkingConfig.Get("includeThoughts").Bool() {
				t.Error("includeThoughts should be true")
			}
		})
	}
}

func TestConvertClaudeRequestToAntigravity_AdaptiveThinking_NoEffort(t *testing.T) {
	inputJSON := []byte(`{
		"model": "claude-opus-4-6-thinking",
		"messages": [{"role": "user", "content": [{"type": "text", "text": "Hello"}]}],
		"thinking": {"type": "adaptive"}
	}`)

	output := ConvertClaudeRequestToAntigravity("claude-opus-4-6-thinking", inputJSON, false)
	outputStr := string(output)

	thinkingConfig := gjson.Get(outputStr, "request.generationConfig.thinkingConfig")
	if !thinkingConfig.Exists() {
		t.Fatal("thinkingConfig should exist for adaptive thinking without effort")
	}
	if thinkingConfig.Get("thinkingLevel").String() != "high" {
		t.Errorf("Expected default thinkingLevel \"high\", got %q", thinkingConfig.Get("thinkingLevel").String())
	}
	if !thinkingConfig.Get("includeThoughts").Bool() {
		t.Error("includeThoughts should be true")
	}
}

func TestConvertClaudeRequestToAntigravity_AdaptiveThinking_ToolAndThinking_HintInjected(t *testing.T) {
	inputJSON := []byte(`{
		"model": "claude-opus-4-6-thinking",
		"messages": [{"role": "user", "content": [{"type": "text", "text": "Hello"}]}],
		"system": [{"type": "text", "text": "You are helpful."}],
		"tools": [
			{
				"name": "get_weather",
				"description": "Get weather",
				"input_schema": {"type": "object", "properties": {"location": {"type": "string"}}}
			}
		],
		"thinking": {"type": "adaptive"},
		"output_config": {"effort": "high"}
	}`)

	output := ConvertClaudeRequestToAntigravity("claude-opus-4-6-thinking", inputJSON, false)
	outputStr := string(output)

	sysInstruction := gjson.Get(outputStr, "request.systemInstruction")
	if !sysInstruction.Exists() {
		t.Fatal("systemInstruction should exist")
	}

	found := false
	for _, part := range sysInstruction.Get("parts").Array() {
		if strings.Contains(part.Get("text").String(), "Interleaved thinking is enabled") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("Interleaved thinking hint should be injected for adaptive thinking + tools, got: %v", sysInstruction.Raw)
	}
}

func TestConvertClaudeRequestToAntigravity_AdaptiveThinking_NoTools_NoHint(t *testing.T) {
	inputJSON := []byte(`{
		"model": "claude-opus-4-6-thinking",
		"messages": [{"role": "user", "content": [{"type": "text", "text": "Hello"}]}],
		"system": [{"type": "text", "text": "You are helpful."}],
		"thinking": {"type": "adaptive"},
		"output_config": {"effort": "high"}
	}`)

	output := ConvertClaudeRequestToAntigravity("claude-opus-4-6-thinking", inputJSON, false)
	outputStr := string(output)

	sysInstruction := gjson.Get(outputStr, "request.systemInstruction")
	if sysInstruction.Exists() {
		for _, part := range sysInstruction.Get("parts").Array() {
			if strings.Contains(part.Get("text").String(), "Interleaved thinking is enabled") {
				t.Error("Hint should NOT be injected when only adaptive thinking is present (no tools)")
			}
		}
	}
}
