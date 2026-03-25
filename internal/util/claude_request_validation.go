package util

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"

	"github.com/tidwall/gjson"
)

type ClaudeRequestValidationError struct {
	Message string
}

func (e *ClaudeRequestValidationError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

func ValidateClaudeMessagesRequest(rawJSON []byte) *ClaudeRequestValidationError {
	messages := gjson.GetBytes(rawJSON, "messages")
	if !messages.IsArray() {
		return nil
	}

	messageArray := messages.Array()
	if len(messageArray) == 0 {
		return &ClaudeRequestValidationError{Message: "messages: at least one message is required"}
	}

	for i, msg := range messageArray {
		contentResult := msg.Get("content")
		if contentResult.Type == gjson.String && contentResult.String() == "" {
			return &ClaudeRequestValidationError{Message: "messages: content must not be empty string"}
		}
		if !contentResult.IsArray() {
			continue
		}

		for j, content := range contentResult.Array() {
			if content.Get("type").String() != "tool_use" {
				continue
			}
			if strings.TrimSpace(content.Get("name").String()) == "" {
				return &ClaudeRequestValidationError{Message: fmt.Sprintf("messages.%d.content.%d.tool_use.name: Field required", i, j)}
			}
			if strings.TrimSpace(content.Get("id").String()) == "" {
				return &ClaudeRequestValidationError{Message: fmt.Sprintf("messages.%d.content.%d.tool_use.id: Field required", i, j)}
			}
		}
	}

	return nil
}

func ValidateClaudeImagesForGoogleUpstream(rawJSON []byte) *ClaudeRequestValidationError {
	messages := gjson.GetBytes(rawJSON, "messages")
	if !messages.IsArray() {
		return nil
	}

	requestedModel := strings.TrimSpace(gjson.GetBytes(rawJSON, "model").String())

	for i, msg := range messages.Array() {
		contentResult := msg.Get("content")
		if !contentResult.IsArray() {
			continue
		}

		for j, content := range contentResult.Array() {
			path := fmt.Sprintf("messages.%d.content.%d", i, j)
			switch content.Get("type").String() {
			case "image":
				if err := validateClaudeImageBlock(path, content, requestedModel); err != nil {
					return err
				}
			case "tool_result":
				if err := validateClaudeToolResultImages(path, content.Get("content"), requestedModel); err != nil {
					return err
				}
			}
		}
	}

	return nil
}

func validateClaudeToolResultImages(path string, content gjson.Result, requestedModel string) *ClaudeRequestValidationError {
	if content.IsArray() {
		for i, item := range content.Array() {
			if item.Get("type").String() != "image" {
				continue
			}
			if err := validateClaudeImageBlock(fmt.Sprintf("%s.content.%d", path, i), item, requestedModel); err != nil {
				return err
			}
		}
		return nil
	}

	if content.IsObject() && content.Get("type").String() == "image" {
		return validateClaudeImageBlock(path+".content", content, requestedModel)
	}

	return nil
}

func validateClaudeImageBlock(path string, imageBlock gjson.Result, requestedModel string) *ClaudeRequestValidationError {
	source := imageBlock.Get("source")
	if source.Get("type").String() != "base64" {
		return nil
	}

	mimeType := strings.TrimSpace(source.Get("media_type").String())
	if mimeType == "" {
		return &ClaudeRequestValidationError{Message: path + ".source.media_type: Field required"}
	}

	data := strings.TrimSpace(source.Get("data").String())
	if data == "" {
		return &ClaudeRequestValidationError{Message: path + ".source.data: Field required"}
	}

	kind, err := inspectBase64ImagePayload(data)
	if err != nil {
		return &ClaudeRequestValidationError{Message: path + ".source.data: Invalid base64 image data"}
	}

	declared := strings.ToLower(mimeType)
	if strings.HasPrefix(declared, "image/svg") || kind == "image/svg+xml" {
		return &ClaudeRequestValidationError{Message: formatSVGModelRejection(path, requestedModel)}
	}
	if !strings.HasPrefix(kind, "image/") {
		return &ClaudeRequestValidationError{Message: path + ".source.data: Invalid image payload"}
	}

	return nil
}

func formatSVGModelRejection(path, requestedModel string) string {
	if strings.TrimSpace(requestedModel) == "" {
		return path + ".source: SVG images are not supported by the requested model"
	}
	return fmt.Sprintf("%s.source: SVG images are not supported by requested model %q", path, requestedModel)
}

func inspectBase64ImagePayload(data string) (string, error) {
	sample, err := decodeBase64Sample(data)
	if err != nil {
		return "", err
	}

	trimmed := bytes.TrimSpace(sample)
	lower := bytes.ToLower(trimmed)
	if bytes.Contains(lower, []byte("<svg")) {
		return "image/svg+xml", nil
	}

	return http.DetectContentType(trimmed), nil
}

func decodeBase64Sample(data string) ([]byte, error) {
	cleaned := strings.TrimSpace(data)
	if comma := strings.IndexByte(cleaned, ','); comma > 0 && strings.HasPrefix(cleaned[:comma], "data:") {
		cleaned = cleaned[comma+1:]
	}
	if len(cleaned) > 4096 {
		cleaned = cleaned[:4096]
	}
	cleaned = strings.TrimRight(cleaned, "=\n\r ")

	padded := cleaned + strings.Repeat("=", (4-len(cleaned)%4)%4)
	raw, err := base64.StdEncoding.DecodeString(padded)
	if err == nil {
		return raw, nil
	}
	return base64.RawStdEncoding.DecodeString(cleaned)
}
