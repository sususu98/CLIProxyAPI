// Package claude provides request translation functionality for Claude Code API compatibility.
// This package handles the conversion of Claude Code API requests into Gemini CLI-compatible
// JSON format, transforming message contents, system instructions, and tool declarations
// into the format expected by Gemini CLI API clients. It performs JSON data transformation
// to ensure compatibility between Claude Code API format and Gemini CLI API's expected format.
package claude

import (
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/cache"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/translator/gemini/common"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func detectAndLogImageMime(declared, base64Data string) string {
	corrected := util.DetectImageMimeType(declared, base64Data)
	if corrected != declared {
		log.Debugf("antigravity claude request: image mime corrected: %s -> %s", declared, corrected)
	}
	return corrected
}

func extractClaudeThinkingSignature(contentResult gjson.Result) string {
	if signature := strings.TrimSpace(contentResult.Get("signature").String()); signature != "" {
		return signature
	}

	thinkingResult := contentResult.Get("thinking")
	if thinkingResult.IsObject() {
		if signature := strings.TrimSpace(thinkingResult.Get("signature").String()); signature != "" {
			return signature
		}
	}

	return ""
}

func normalizeModelPartOrder(clientContentJSON string, targetIsClaude bool) string {
	if targetIsClaude || gjson.Get(clientContentJSON, "role").String() != "model" {
		return clientContentJSON
	}

	partsResult := gjson.Get(clientContentJSON, "parts")
	if !partsResult.IsArray() {
		return clientContentJSON
	}

	parts := partsResult.Array()
	var thinkingParts []gjson.Result
	var otherParts []gjson.Result
	for _, part := range parts {
		if part.Get("thought").Bool() {
			thinkingParts = append(thinkingParts, part)
		} else {
			otherParts = append(otherParts, part)
		}
	}
	if len(thinkingParts) == 0 {
		return clientContentJSON
	}

	firstPartIsThinking := parts[0].Get("thought").Bool()
	if firstPartIsThinking && len(thinkingParts) == 1 {
		return clientContentJSON
	}

	var newParts []interface{}
	for _, p := range thinkingParts {
		newParts = append(newParts, p.Value())
	}
	for _, p := range otherParts {
		newParts = append(newParts, p.Value())
	}

	clientContentJSON, _ = sjson.Set(clientContentJSON, "parts", newParts)
	return clientContentJSON
}

func appendOrMergeAntigravityContent(contentsJSON, clientContentJSON string, targetIsClaude bool) (string, bool) {
	partsResult := gjson.Get(clientContentJSON, "parts")
	if !partsResult.IsArray() || len(partsResult.Array()) == 0 {
		return contentsJSON, false
	}

	clientContentJSON = normalizeModelPartOrder(clientContentJSON, targetIsClaude)

	contentsResult := gjson.Parse(contentsJSON)
	if !contentsResult.IsArray() {
		contentsJSON, _ = sjson.SetRaw("[]", "-1", clientContentJSON)
		return contentsJSON, true
	}

	contents := contentsResult.Array()
	if len(contents) == 0 {
		contentsJSON, _ = sjson.SetRaw(contentsJSON, "-1", clientContentJSON)
		return contentsJSON, true
	}

	lastIndex := len(contents) - 1
	lastRole := contents[lastIndex].Get("role").String()
	currentRole := gjson.Get(clientContentJSON, "role").String()
	if lastRole != "" && lastRole == currentRole {
		for _, part := range partsResult.Array() {
			contentsJSON, _ = sjson.SetRaw(contentsJSON, fmt.Sprintf("%d.parts.-1", lastIndex), part.Raw)
		}

		if currentRole == "model" {
			mergedContent := gjson.Get(contentsJSON, fmt.Sprintf("%d", lastIndex)).Raw
			normalizedMerged := normalizeModelPartOrder(mergedContent, targetIsClaude)
			contentsJSON, _ = sjson.SetRaw(contentsJSON, fmt.Sprintf("%d", lastIndex), normalizedMerged)
		}

		return contentsJSON, true
	}

	contentsJSON, _ = sjson.SetRaw(contentsJSON, "-1", clientContentJSON)
	return contentsJSON, true
}

// ConvertClaudeRequestToAntigravity parses and transforms a Claude Code API request into Gemini CLI API format.
// It extracts the model name, system instruction, message contents, and tool declarations
// from the raw JSON request and returns them in the format expected by the Gemini CLI API.
// The function performs the following transformations:
// 1. Extracts the model information from the request
// 2. Restructures the JSON to match Gemini CLI API format
// 3. Converts system instructions to the expected format
// 4. Maps message contents with proper role transformations
// 5. Handles tool declarations and tool choices
// 6. Maps generation configuration parameters
//
// Parameters:
//   - modelName: The name of the model to use for the request
//   - rawJSON: The raw JSON request data from the Claude Code API
//   - stream: A boolean indicating if the request is for a streaming response (unused in current implementation)
//
// Returns:
//   - []byte: The transformed request data in Gemini CLI API format
func ConvertClaudeRequestToAntigravity(modelName string, inputRawJSON []byte, _ bool) []byte {
	enableThoughtTranslate := true
	rawJSON := inputRawJSON
	targetIsClaude := cache.GetModelGroup(modelName) == "claude"

	// system instruction
	systemInstructionJSON := ""
	hasSystemInstruction := false
	systemResult := gjson.GetBytes(rawJSON, "system")
	if systemResult.IsArray() {
		systemResults := systemResult.Array()
		systemInstructionJSON = `{"role":"user","parts":[]}`
		for i := 0; i < len(systemResults); i++ {
			systemPromptResult := systemResults[i]
			systemTypePromptResult := systemPromptResult.Get("type")
			if systemTypePromptResult.Type == gjson.String && systemTypePromptResult.String() == "text" {
				systemPrompt := systemPromptResult.Get("text").String()
				if strings.HasPrefix(systemPrompt, "x-anthropic-billing-header:") {
					continue
				}
				partJSON := `{}`
				if systemPrompt != "" {
					partJSON, _ = sjson.Set(partJSON, "text", systemPrompt)
				}
				systemInstructionJSON, _ = sjson.SetRaw(systemInstructionJSON, "parts.-1", partJSON)
				hasSystemInstruction = true
			}
		}
	} else if systemResult.Type == gjson.String {
		systemInstructionJSON = `{"role":"user","parts":[{"text":""}]}`
		systemInstructionJSON, _ = sjson.Set(systemInstructionJSON, "parts.0.text", systemResult.String())
		hasSystemInstruction = true
	}

	// contents
	contentsJSON := "[]"
	hasContents := false

	// tool_use_id → tool_name lookup, populated incrementally during the main loop.
	// Claude's tool_result references tool_use by ID; Gemini requires functionResponse.name.
	toolNameByID := make(map[string]string)
	sanitizedToolIDByOriginal := make(map[string]string)

	// Pre-scan: collect all tool_result IDs to detect orphan tool_use blocks.
	// Claude requires every tool_use to have a matching tool_result in the next message.
	// Clients may truncate conversation history, leaving orphan tool_use blocks that
	// cause "tool_use ids were found without tool_result blocks" errors.
	resolvedToolIDs := make(map[string]bool)
	messagesResult := gjson.GetBytes(rawJSON, "messages")
	if messagesResult.IsArray() {
		for _, msg := range messagesResult.Array() {
			contents := msg.Get("content")
			if !contents.IsArray() {
				continue
			}
			for _, c := range contents.Array() {
				if c.Get("type").String() == "tool_result" {
					if id := c.Get("tool_use_id").String(); id != "" {
						resolvedToolIDs[id] = true
					}
				}
			}
		}
	}

	if messagesResult.IsArray() {
		messageResults := messagesResult.Array()
		numMessages := len(messageResults)
		for i := 0; i < numMessages; i++ {
			messageResult := messageResults[i]
			roleResult := messageResult.Get("role")
			if roleResult.Type != gjson.String {
				continue
			}
			originalRole := roleResult.String()
			role := originalRole
			if role == "assistant" {
				role = "model"
			}
			clientContentJSON := `{"role":"","parts":[]}`
			clientContentJSON, _ = sjson.Set(clientContentJSON, "role", role)
			contentsResult := messageResult.Get("content")
			if contentsResult.IsArray() {
				contentResults := contentsResult.Array()
				numContents := len(contentResults)
				var currentMessageThinkingSignature string
				for j := 0; j < numContents; j++ {
					contentResult := contentResults[j]
					contentTypeResult := contentResult.Get("type")
					if contentTypeResult.Type == gjson.String && contentTypeResult.String() == "thinking" {
						// Use GetThinkingText to handle wrapped thinking objects
						thinkingText := thinking.GetThinkingText(contentResult)
						hasThinkingText := strings.TrimSpace(thinkingText) != ""

						signature := ""
						if cache.SignatureCacheEnabled() {
							// Cache mode: prefer cached signature, validate length
							if hasThinkingText {
								if cachedSig := cache.GetCachedSignature(modelName, thinkingText); cachedSig != "" {
									signature = cachedSig
								}
							}

							// Fallback to client signature only if cache miss and client signature is valid
							if signature == "" {
								rawSignature := extractClaudeThinkingSignature(contentResult)
								if rawSignature != "" {
									clientSig := ""
									arrayClientSignatures := strings.SplitN(rawSignature, "#", 2)
									if len(arrayClientSignatures) == 2 {
										if cache.GetModelGroup(modelName) == arrayClientSignatures[0] {
											clientSig = arrayClientSignatures[1]
										}
									}
									if cache.HasValidSignature(modelName, clientSig) {
										signature = clientSig
									}
								}
							}

							// Store for subsequent tool_use in the same message and across messages
							if cache.HasValidSignature(modelName, signature) {
								currentMessageThinkingSignature = signature
							}

							// Skip unsigned thinking blocks when cache is enabled
							if !cache.HasValidSignature(modelName, signature) {
								log.Warnf("antigravity claude request: dropping thinking block with invalid signature (cache mode, textLen=%d)", len(thinkingText))
								enableThoughtTranslate = false
								continue
							}

							if !hasThinkingText {
								log.Warn("antigravity claude request: dropping thinking block with empty text")
								continue
							}
						} else {
							rawSig := extractClaudeThinkingSignature(contentResult)
							if rawSig != "" {
								sigType := detectSignatureType(rawSig)
								modelGroup := cache.GetModelGroup(modelName)
								if sigType != "" && sigType == modelGroup {
									signature = normalizeSignatureForModel(rawSig, modelName)
									currentMessageThinkingSignature = signature
								}
							}
							if signature == "" {
								log.Warnf("antigravity claude request: dropping thinking block with incompatible signature (bypass mode, model=%s, textLen=%d)", modelName, len(thinkingText))
								continue
							}

							if !hasThinkingText {
								log.Warn("antigravity claude request: dropping thinking block with empty text")
								continue
							}
						}

						// Send as thought block
						// Always include "text" field — Google Antigravity API requires it
						// even for redacted thinking where the text is empty.
						partJSON := `{}`
						partJSON, _ = sjson.Set(partJSON, "thought", true)
						partJSON, _ = sjson.Set(partJSON, "text", thinkingText)
						partJSON, _ = sjson.Set(partJSON, "thoughtSignature", signature)
						clientContentJSON, _ = sjson.SetRaw(clientContentJSON, "parts.-1", partJSON)
					} else if contentTypeResult.Type == gjson.String && contentTypeResult.String() == "redacted_thinking" {
						continue
					} else if contentTypeResult.Type == gjson.String && contentTypeResult.String() == "text" {
						prompt := contentResult.Get("text").String()
						// Skip empty text parts to avoid Gemini API error:
						// "required oneof field 'data' must have one initialized field"
						if prompt == "" {
							continue
						}
						partJSON := `{}`
						partJSON, _ = sjson.Set(partJSON, "text", prompt)
						clientContentJSON, _ = sjson.SetRaw(clientContentJSON, "parts.-1", partJSON)
					} else if contentTypeResult.Type == gjson.String && contentTypeResult.String() == "tool_use" {
						// NOTE: Do NOT inject dummy thinking blocks here.
						// Antigravity API validates signatures, so dummy values are rejected.

						functionName := util.SanitizeFunctionName(contentResult.Get("name").String())
						argsResult := contentResult.Get("input")
						originalFunctionID := contentResult.Get("id").String()
						functionID := originalFunctionID
						if originalFunctionID != "" {
							functionID = util.SanitizeClaudeToolID(originalFunctionID)
							sanitizedToolIDByOriginal[originalFunctionID] = functionID
						}

						// Skip orphan tool_use blocks that have no matching tool_result
						if originalFunctionID != "" && !resolvedToolIDs[originalFunctionID] {
							log.Warnf("antigravity claude request: skipping orphan tool_use id=%s name=%s (no matching tool_result in conversation)", originalFunctionID, functionName)
							continue
						}

						if functionID != "" && functionName != "" {
							toolNameByID[functionID] = functionName
						}

						// Handle both object and string input formats
						var argsRaw string
						if argsResult.IsObject() {
							argsRaw = argsResult.Raw
						} else if argsResult.Type == gjson.String {
							// Input is a JSON string, parse and validate it
							parsed := gjson.Parse(argsResult.String())
							if parsed.IsObject() {
								argsRaw = parsed.Raw
							}
						}

						if argsRaw != "" {
							partJSON := `{}`

							// Attach signature for tool calls — only from current message's thinking block
							if cache.SignatureCacheEnabled() {
								if cache.HasValidSignature(modelName, currentMessageThinkingSignature) {
									partJSON, _ = sjson.Set(partJSON, "thoughtSignature", currentMessageThinkingSignature)
								} else if !targetIsClaude {
									const skipSentinel = "skip_thought_signature_validator"
									partJSON, _ = sjson.Set(partJSON, "thoughtSignature", skipSentinel)
								}
							} else {
								if currentMessageThinkingSignature != "" {
									partJSON, _ = sjson.Set(partJSON, "thoughtSignature", currentMessageThinkingSignature)
								} else if !targetIsClaude {
									const skipSentinel = "skip_thought_signature_validator"
									partJSON, _ = sjson.Set(partJSON, "thoughtSignature", skipSentinel)
								}
							}

							if functionID != "" {
								partJSON, _ = sjson.Set(partJSON, "functionCall.id", functionID)
							}
							partJSON, _ = sjson.Set(partJSON, "functionCall.name", functionName)
							partJSON, _ = sjson.SetRaw(partJSON, "functionCall.args", argsRaw)
							clientContentJSON, _ = sjson.SetRaw(clientContentJSON, "parts.-1", partJSON)
						}
					} else if contentTypeResult.Type == gjson.String && contentTypeResult.String() == "tool_result" {
						toolCallID := contentResult.Get("tool_use_id").String()
						sanitizedToolCallID := toolCallID
						if sanitizedID, ok := sanitizedToolIDByOriginal[toolCallID]; ok {
							sanitizedToolCallID = sanitizedID
						} else if toolCallID != "" {
							sanitizedToolCallID = util.SanitizeClaudeToolID(toolCallID)
							sanitizedToolIDByOriginal[toolCallID] = sanitizedToolCallID
						}
						if toolCallID != "" {
							funcName, ok := toolNameByID[sanitizedToolCallID]
							if !ok {
								// Fallback: derive a semantic name from the ID by stripping
								// the last two dash-separated segments (e.g. "get_weather-call-123" → "get_weather").
								// Only use the raw ID as a last resort when the heuristic produces an empty string.
								parts := strings.Split(toolCallID, "-")
								if len(parts) > 2 {
									funcName = strings.Join(parts[:len(parts)-2], "-")
								}
								if funcName == "" {
									funcName = toolCallID
								}
								log.Warnf("antigravity claude request: tool_result references unknown tool_use_id=%s, derived function name=%s", toolCallID, funcName)
							}
							functionResponseResult := contentResult.Get("content")

							functionResponseJSON := `{}`
							functionResponseJSON, _ = sjson.Set(functionResponseJSON, "id", sanitizedToolCallID)
							functionResponseJSON, _ = sjson.Set(functionResponseJSON, "name", util.SanitizeFunctionName(funcName))

							responseData := ""
							if functionResponseResult.Type == gjson.String {
								responseData = functionResponseResult.String()
								functionResponseJSON, _ = sjson.Set(functionResponseJSON, "response.result", responseData)
							} else if functionResponseResult.IsArray() {
								frResults := functionResponseResult.Array()
								nonImageCount := 0
								lastNonImageRaw := ""
								filteredJSON := "[]"
								imagePartsJSON := "[]"
								for _, fr := range frResults {
									if fr.Get("type").String() == "image" && fr.Get("source.type").String() == "base64" {
										inlineDataJSON := `{}`
										data := fr.Get("source.data").String()
										if mimeType := fr.Get("source.media_type").String(); mimeType != "" {
											inlineDataJSON, _ = sjson.Set(inlineDataJSON, "mimeType", detectAndLogImageMime(mimeType, data))
										}
										if data != "" {
											inlineDataJSON, _ = sjson.Set(inlineDataJSON, "data", data)
										}

										imagePartJSON := `{}`
										imagePartJSON, _ = sjson.SetRaw(imagePartJSON, "inlineData", inlineDataJSON)
										imagePartsJSON, _ = sjson.SetRaw(imagePartsJSON, "-1", imagePartJSON)
										continue
									}

									nonImageCount++
									lastNonImageRaw = fr.Raw
									filteredJSON, _ = sjson.SetRaw(filteredJSON, "-1", fr.Raw)
								}

								if nonImageCount == 1 {
									functionResponseJSON, _ = sjson.SetRaw(functionResponseJSON, "response.result", lastNonImageRaw)
								} else if nonImageCount > 1 {
									functionResponseJSON, _ = sjson.SetRaw(functionResponseJSON, "response.result", filteredJSON)
								} else {
									functionResponseJSON, _ = sjson.Set(functionResponseJSON, "response.result", "")
								}

								// Place image data inside functionResponse.parts as inlineData
								// instead of as sibling parts in the outer content, to avoid
								// base64 data bloating the text context.
								if gjson.Get(imagePartsJSON, "#").Int() > 0 {
									functionResponseJSON, _ = sjson.SetRaw(functionResponseJSON, "parts", imagePartsJSON)
								}

							} else if functionResponseResult.IsObject() {
								if functionResponseResult.Get("type").String() == "image" && functionResponseResult.Get("source.type").String() == "base64" {
									inlineDataJSON := `{}`
									data := functionResponseResult.Get("source.data").String()
									if mimeType := functionResponseResult.Get("source.media_type").String(); mimeType != "" {
										inlineDataJSON, _ = sjson.Set(inlineDataJSON, "mimeType", detectAndLogImageMime(mimeType, data))
									}
									if data != "" {
										inlineDataJSON, _ = sjson.Set(inlineDataJSON, "data", data)
									}

									imagePartJSON := `{}`
									imagePartJSON, _ = sjson.SetRaw(imagePartJSON, "inlineData", inlineDataJSON)
									imagePartsJSON := "[]"
									imagePartsJSON, _ = sjson.SetRaw(imagePartsJSON, "-1", imagePartJSON)
									functionResponseJSON, _ = sjson.SetRaw(functionResponseJSON, "parts", imagePartsJSON)
									functionResponseJSON, _ = sjson.Set(functionResponseJSON, "response.result", "")
								} else {
									functionResponseJSON, _ = sjson.SetRaw(functionResponseJSON, "response.result", functionResponseResult.Raw)
								}
							} else if functionResponseResult.Raw != "" {
								functionResponseJSON, _ = sjson.SetRaw(functionResponseJSON, "response.result", functionResponseResult.Raw)
							} else {
								// Content field is missing entirely — .Raw is empty which
								// causes sjson.SetRaw to produce invalid JSON (e.g. "result":}).
								functionResponseJSON, _ = sjson.Set(functionResponseJSON, "response.result", "")
							}

							partJSON := `{}`
							partJSON, _ = sjson.SetRaw(partJSON, "functionResponse", functionResponseJSON)
							clientContentJSON, _ = sjson.SetRaw(clientContentJSON, "parts.-1", partJSON)
						}
					} else if contentTypeResult.Type == gjson.String && contentTypeResult.String() == "image" {
						sourceResult := contentResult.Get("source")
						if sourceResult.Get("type").String() == "base64" {
							inlineDataJSON := `{}`
							data := sourceResult.Get("data").String()
							if mimeType := sourceResult.Get("media_type").String(); mimeType != "" {
								inlineDataJSON, _ = sjson.Set(inlineDataJSON, "mimeType", detectAndLogImageMime(mimeType, data))
							}
							if data != "" {
								inlineDataJSON, _ = sjson.Set(inlineDataJSON, "data", data)
							}

							partJSON := `{}`
							partJSON, _ = sjson.SetRaw(partJSON, "inlineData", inlineDataJSON)
							clientContentJSON, _ = sjson.SetRaw(clientContentJSON, "parts.-1", partJSON)
						}
					}
				}

				// Skip messages with empty parts array to avoid Gemini API error:
				// "required oneof field 'data' must have one initialized field"
				partsCheck := gjson.Get(clientContentJSON, "parts")
				if !partsCheck.IsArray() || len(partsCheck.Array()) == 0 {
					continue
				}

				var appended bool
				contentsJSON, appended = appendOrMergeAntigravityContent(contentsJSON, clientContentJSON, targetIsClaude)
				if appended {
					hasContents = true
				}
			} else if contentsResult.Type == gjson.String {
				prompt := contentsResult.String()
				partJSON := `{}`
				if prompt != "" {
					partJSON, _ = sjson.Set(partJSON, "text", prompt)
				}
				clientContentJSON, _ = sjson.SetRaw(clientContentJSON, "parts.-1", partJSON)
				var appended bool
				contentsJSON, appended = appendOrMergeAntigravityContent(contentsJSON, clientContentJSON, targetIsClaude)
				if appended {
					hasContents = true
				}
			}
		}
	}

	// Gemini requires that a functionCall turn follows a user or functionResponse turn.
	// If the first content is a model turn with a functionCall, prepend a minimal user turn.
	if hasContents {
		firstContent := gjson.Parse(contentsJSON).Array()
		if len(firstContent) > 0 && firstContent[0].Get("role").String() == "model" {
			hasFunctionCall := false
			for _, p := range firstContent[0].Get("parts").Array() {
				if p.Get("functionCall").Exists() {
					hasFunctionCall = true
					break
				}
			}
			if hasFunctionCall {
				contentsJSON, _ = sjson.SetRaw(`[{"role":"user","parts":[{"text":""}]}]`, "-1", gjson.Parse(contentsJSON).Array()[0].Raw)
				for k := 1; k < len(firstContent); k++ {
					contentsJSON, _ = sjson.SetRaw(contentsJSON, "-1", firstContent[k].Raw)
				}
			}
		}
	}

	// tools
	toolsJSON := ""
	toolDeclCount := 0
	allowedToolKeys := []string{"name", "description", "behavior", "parameters", "parametersJsonSchema", "response", "responseJsonSchema"}
	toolsResult := gjson.GetBytes(rawJSON, "tools")
	if toolsResult.IsArray() {
		toolsJSON = `[{"functionDeclarations":[]}]`
		toolsResults := toolsResult.Array()
		for i := 0; i < len(toolsResults); i++ {
			toolResult := toolsResults[i]

			// Skip web_search tools - they are handled separately in the executor
			if strings.HasPrefix(toolResult.Get("type").String(), "web_search") {
				continue
			}

			inputSchemaResult := toolResult.Get("input_schema")
			if inputSchemaResult.Exists() && inputSchemaResult.IsObject() {
				// Sanitize the input schema for Antigravity API compatibility
				inputSchema := util.CleanJSONSchemaForAntigravity(inputSchemaResult.Raw)
				tool, _ := sjson.Delete(toolResult.Raw, "input_schema")
				tool, _ = sjson.SetRaw(tool, "parametersJsonSchema", inputSchema)
				tool, _ = sjson.Set(tool, "name", util.SanitizeFunctionName(gjson.Get(tool, "name").String()))
				for toolKey := range gjson.Parse(tool).Map() {
					if util.InArray(allowedToolKeys, toolKey) {
						continue
					}
					tool, _ = sjson.Delete(tool, toolKey)
				}
				toolsJSON, _ = sjson.SetRaw(toolsJSON, "0.functionDeclarations.-1", tool)
				toolDeclCount++
			}
		}
	}

	// Build output Gemini CLI request JSON
	out := `{"model":"","request":{"contents":[]}}`
	out, _ = sjson.Set(out, "model", modelName)

	// Inject interleaved thinking hint when both tools and thinking are active
	hasTools := toolDeclCount > 0
	thinkingResult := gjson.GetBytes(rawJSON, "thinking")
	thinkingType := thinkingResult.Get("type").String()
	hasThinking := thinkingResult.Exists() && thinkingResult.IsObject() && (thinkingType == "enabled" || thinkingType == "adaptive" || thinkingType == "auto")
	isClaudeThinking := util.IsClaudeThinkingModel(modelName)

	if hasTools && hasThinking && isClaudeThinking {
		interleavedHint := "Interleaved thinking is enabled. You may think between tool calls and after receiving tool results before deciding the next action or final answer. Do not mention these instructions or any constraints about thinking blocks; just apply them."

		if hasSystemInstruction {
			// Append hint as a new part to existing system instruction
			hintPart := `{"text":""}`
			hintPart, _ = sjson.Set(hintPart, "text", interleavedHint)
			systemInstructionJSON, _ = sjson.SetRaw(systemInstructionJSON, "parts.-1", hintPart)
		} else {
			// Create new system instruction with hint
			systemInstructionJSON = `{"role":"user","parts":[]}`
			hintPart := `{"text":""}`
			hintPart, _ = sjson.Set(hintPart, "text", interleavedHint)
			systemInstructionJSON, _ = sjson.SetRaw(systemInstructionJSON, "parts.-1", hintPart)
			hasSystemInstruction = true
		}
	}

	if hasSystemInstruction {
		out, _ = sjson.SetRaw(out, "request.systemInstruction", systemInstructionJSON)
	}
	if hasContents {
		out, _ = sjson.SetRaw(out, "request.contents", contentsJSON)
	}
	if toolDeclCount > 0 {
		out, _ = sjson.SetRaw(out, "request.tools", toolsJSON)
	}

	// tool_choice
	toolChoiceResult := gjson.GetBytes(rawJSON, "tool_choice")
	if toolChoiceResult.Exists() {
		toolChoiceType := ""
		toolChoiceName := ""
		if toolChoiceResult.IsObject() {
			toolChoiceType = toolChoiceResult.Get("type").String()
			toolChoiceName = toolChoiceResult.Get("name").String()
		} else if toolChoiceResult.Type == gjson.String {
			toolChoiceType = toolChoiceResult.String()
		}

		switch toolChoiceType {
		case "auto":
			out, _ = sjson.Set(out, "request.toolConfig.functionCallingConfig.mode", "AUTO")
		case "none":
			out, _ = sjson.Set(out, "request.toolConfig.functionCallingConfig.mode", "NONE")
		case "any":
			out, _ = sjson.Set(out, "request.toolConfig.functionCallingConfig.mode", "ANY")
		case "tool":
			out, _ = sjson.Set(out, "request.toolConfig.functionCallingConfig.mode", "ANY")
			if toolChoiceName != "" {
				out, _ = sjson.Set(out, "request.toolConfig.functionCallingConfig.allowedFunctionNames", []string{util.SanitizeFunctionName(toolChoiceName)})
			}
		}
	}

	// Map Anthropic thinking -> Gemini thinkingBudget/include_thoughts
	if t := gjson.GetBytes(rawJSON, "thinking"); enableThoughtTranslate && t.Exists() && t.IsObject() {
		thinkingType := t.Get("type").String()
		switch thinkingType {
		case "enabled":
			if b := t.Get("budget_tokens"); b.Exists() && b.Type == gjson.Number {
				budget := int(b.Int())
				out, _ = sjson.Set(out, "request.generationConfig.thinkingConfig.thinkingBudget", budget)
				out, _ = sjson.Set(out, "request.generationConfig.thinkingConfig.includeThoughts", true)
			}
		case "adaptive", "auto":
			// For adaptive thinking:
			// - If output_config.effort is explicitly present, pass through as thinkingLevel.
			// - Otherwise, treat it as "enabled with target-model maximum" and emit high.
			// ApplyThinking handles clamping to target model's supported levels.
			effort := ""
			if v := gjson.GetBytes(rawJSON, "output_config.effort"); v.Exists() && v.Type == gjson.String {
				effort = strings.ToLower(strings.TrimSpace(v.String()))
			}
			if effort != "" {
				out, _ = sjson.Set(out, "request.generationConfig.thinkingConfig.thinkingLevel", effort)
			} else {
				out, _ = sjson.Set(out, "request.generationConfig.thinkingConfig.thinkingLevel", "high")
			}
			out, _ = sjson.Set(out, "request.generationConfig.thinkingConfig.includeThoughts", true)
		}
	}
	if v := gjson.GetBytes(rawJSON, "temperature"); v.Exists() && v.Type == gjson.Number {
		out, _ = sjson.Set(out, "request.generationConfig.temperature", v.Num)
	}
	if v := gjson.GetBytes(rawJSON, "top_p"); v.Exists() && v.Type == gjson.Number {
		out, _ = sjson.Set(out, "request.generationConfig.topP", v.Num)
	}
	if v := gjson.GetBytes(rawJSON, "top_k"); v.Exists() && v.Type == gjson.Number {
		out, _ = sjson.Set(out, "request.generationConfig.topK", v.Num)
	}
	if v := gjson.GetBytes(rawJSON, "max_tokens"); v.Exists() && v.Type == gjson.Number {
		out, _ = sjson.Set(out, "request.generationConfig.maxOutputTokens", v.Num)
	}

	outBytes := []byte(out)
	outBytes = common.AttachDefaultSafetySettings(outBytes, "request.safetySettings")

	return outBytes
}

// detectSignatureType identifies which model family a signature belongs to.
// Returns "claude", "gemini", or "" (unknown).
//
// detectSignatureType identifies the model family that produced a signature.
//
// Detection is based on Base64 prefix and protobuf internal structure:
//
//   - Ci/Ck/Cm/Cl prefixes: Gemini direct API thinking signatures (Field 1, 0x0a)
//   - ZT prefix: Gemini tool call signatures (Base64-encoded UUID v4)
//   - E prefix: decode Base64, parse protobuf Field 2 → Field 1 length:
//     F1 ∈ [60,100] bytes → Claude (ECDSA-P256 signing block, 70 or 72 bytes)
//     F1 > 100 bytes → Gemini-on-antigravity (different signing structure)
//   - R prefix: 2-layer Base64 (Google Vertex direct), inner starts with E → Claude
//
// Supports optional "{modelGroup}#" prefix (stripped before analysis).
func detectSignatureType(rawSignature string) string {
	sig := rawSignature
	if idx := strings.Index(sig, "#"); idx != -1 {
		sig = sig[idx+1:]
	}
	if len(sig) < 20 {
		return ""
	}

	// Gemini direct API: Ci/Ck/Cm/Cl (thinking) or ZT (tool call)
	if strings.HasPrefix(sig, "Ci") || strings.HasPrefix(sig, "Ck") ||
		strings.HasPrefix(sig, "Cm") || strings.HasPrefix(sig, "Cl") ||
		strings.HasPrefix(sig, "ZT") {
		return "gemini"
	}

	// R prefix: 2-layer Base64 (Google Vertex direct)
	// Decode outer layer and validate inner E-prefix with protobuf check
	if strings.HasPrefix(sig, "R") {
		firstLayer, err := base64.StdEncoding.DecodeString(sig)
		if err != nil || len(firstLayer) == 0 || firstLayer[0] != 'E' {
			return ""
		}
		return classifyEPrefixSignature(string(firstLayer))
	}

	// E prefix: could be Claude or Gemini-on-antigravity
	// Distinguish by protobuf Field 1 (signing block) length:
	//   Claude: 70-72 bytes (ECDSA-P256 + channel/backend metadata)
	//   Gemini-on-antigravity: 1000+ bytes (completely different structure)
	if strings.HasPrefix(sig, "E") {
		return classifyEPrefixSignature(sig)
	}

	return ""
}

// classifyEPrefixSignature decodes an E-prefix Base64 signature and inspects
// the protobuf Field 1 (signing block) length to distinguish Claude from
// Gemini-on-antigravity signatures.
func classifyEPrefixSignature(sig string) string {
	decoded, err := base64.StdEncoding.DecodeString(sig)
	if err != nil || len(decoded) < 6 {
		return ""
	}
	if decoded[0] != 0x12 {
		return ""
	}
	// Skip Field 2 varint length
	offset := 1
	for offset < len(decoded) && offset < 6 {
		if decoded[offset]&0x80 == 0 {
			offset++
			break
		}
		offset++
	}
	// Expect Field 1 tag (0x0A)
	if offset >= len(decoded) || decoded[offset] != 0x0A {
		return ""
	}
	offset++
	// Read Field 1 varint length
	f1Len := 0
	shift := uint(0)
	for i := 0; i < 4 && offset+i < len(decoded); i++ {
		b := decoded[offset+i]
		f1Len |= int(b&0x7F) << shift
		if b&0x80 == 0 {
			break
		}
		shift += 7
	}
	if f1Len >= 60 && f1Len <= 100 {
		return "claude"
	}
	if f1Len > 100 {
		return "gemini"
	}
	return ""
}

// normalizeSignatureForModel converts a signature to the format expected by the target model
// on Antigravity. Used in bypass mode.
//
// For Claude models: E... -> base64(E...) (2-layer), R... -> as-is
// For Gemini models: pass through without modification
func normalizeSignatureForModel(rawSignature, modelName string) string {
	sig := rawSignature
	if idx := strings.Index(sig, "#"); idx != -1 {
		sig = sig[idx+1:]
	}

	if cache.GetModelGroup(modelName) == "claude" {
		if strings.HasPrefix(sig, "E") {
			return base64.StdEncoding.EncodeToString([]byte(sig))
		}
		return sig
	}

	return sig
}
