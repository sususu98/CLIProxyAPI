package util

import (
	"encoding/base64"
	"testing"
)

func TestValidateClaudeMessagesRequest(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		payload  string
		wantErr  bool
		wantText string
	}{
		{
			name:     "empty messages rejected",
			payload:  `{"messages":[]}`,
			wantErr:  true,
			wantText: "messages: at least one message is required",
		},
		{
			name:     "empty string content rejected",
			payload:  `{"messages":[{"role":"user","content":""}]}`,
			wantErr:  true,
			wantText: "messages: content must not be empty string",
		},
		{
			name: "tool use missing name rejected",
			payload: `{"messages":[
				{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"","input":{}}]}
			]}`,
			wantErr:  true,
			wantText: "messages.0.content.0.tool_use.name: Field required",
		},
		{
			name: "tool use missing id rejected",
			payload: `{"messages":[
				{"role":"assistant","content":[{"type":"tool_use","name":"read_file","input":{}}]}
			]}`,
			wantErr:  true,
			wantText: "messages.0.content.0.tool_use.id: Field required",
		},
		{
			name: "duplicate tool use ids rejected",
			payload: `{"messages":[
				{"role":"user","content":"hi"},
				{"role":"assistant","content":[
					{"type":"tool_use","id":"toolu_dup","name":"read_file","input":{}},
					{"type":"tool_use","id":"toolu_dup","name":"write_file","input":{}}
				]}
			]}`,
			wantErr:  true,
			wantText: `messages.1.content.1.tool_use.id: Duplicate value "toolu_dup"`,
		},
		{
			name: "valid payload passes",
			payload: `{"messages":[
				{"role":"user","content":"hi"},
				{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"read_file","input":{}}]},
				{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"ok"}]}
			]}`,
			wantErr: false,
		},
		{
			name: "tool result must immediately follow tool use",
			payload: `{"messages":[
				{"role":"user","content":"hi"},
				{"role":"assistant","content":[{"type":"tool_use","id":"toolu_2","name":"read_file","input":{}}]},
				{"role":"user","content":[{"type":"text","text":"still waiting"}]},
				{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_2","content":"ok"}]}
			]}`,
			wantErr:  true,
			wantText: "messages.1: `tool_use` ids were found without `tool_result` blocks immediately after: toolu_2. Each `tool_use` block must have a corresponding `tool_result` block in the next message.",
		},
		{
			name: "next message after tool use must be user role",
			payload: `{"messages":[
				{"role":"user","content":"hi"},
				{"role":"assistant","content":[{"type":"tool_use","id":"toolu_role","name":"read_file","input":{}}]},
				{"role":"assistant","content":[{"type":"tool_result","tool_use_id":"toolu_role","content":"ok"}]}
			]}`,
			wantErr:  true,
			wantText: "messages.2.role: tool_result blocks must be provided in the next user message after tool_use",
		},
		{
			name: "all tool uses in message must be satisfied by next message",
			payload: `{"messages":[
				{"role":"user","content":"hi"},
				{"role":"assistant","content":[
					{"type":"tool_use","id":"toolu_3","name":"read_file","input":{}},
					{"type":"tool_use","id":"toolu_4","name":"write_file","input":{}}
				]},
				{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_3","content":"ok"}]}
			]}`,
			wantErr:  true,
			wantText: "messages.1: `tool_use` ids were found without `tool_result` blocks immediately after: toolu_4. Each `tool_use` block must have a corresponding `tool_result` block in the next message.",
		},
		{
			name: "unexpected tool result id rejected",
			payload: `{"messages":[
				{"role":"user","content":"hi"},
				{"role":"assistant","content":[{"type":"tool_use","id":"toolu_expected","name":"read_file","input":{}}]},
				{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_other","content":"ok"}]}
			]}`,
			wantErr:  true,
			wantText: `messages.2.content.0.tool_result.tool_use_id: Unexpected value "toolu_other"; expected tool results for the immediately preceding tool_use ids`,
		},
		{
			name: "duplicate tool result ids rejected",
			payload: `{"messages":[
				{"role":"user","content":"hi"},
				{"role":"assistant","content":[{"type":"tool_use","id":"toolu_result_dup","name":"read_file","input":{}}]},
				{"role":"user","content":[
					{"type":"tool_result","tool_use_id":"toolu_result_dup","content":"ok"},
					{"type":"tool_result","tool_use_id":"toolu_result_dup","content":"again"}
				]}
			]}`,
			wantErr:  true,
			wantText: `messages.2.content.1.tool_result.tool_use_id: Duplicate value "toolu_result_dup"`,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateClaudeMessagesRequest([]byte(tt.payload))
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if err.Message != tt.wantText {
					t.Fatalf("message = %q, want %q", err.Message, tt.wantText)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestValidateClaudeImagesForGoogleUpstream(t *testing.T) {
	t.Parallel()

	pngHeader := base64.StdEncoding.EncodeToString([]byte("\x89PNG\x0D\x0A\x1A\x0A" + "\x00\x00\x00\x0DIHDR" + string(make([]byte, 128))))
	svgData := base64.StdEncoding.EncodeToString([]byte(`<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg"></svg>`))

	tests := []struct {
		name     string
		payload  string
		wantErr  bool
		wantText string
	}{
		{
			name:     "missing media type rejected",
			payload:  `{"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","data":"` + pngHeader + `"}}]}]}`,
			wantErr:  true,
			wantText: "messages.0.content.0.source.media_type: Field required",
		},
		{
			name:     "missing data rejected",
			payload:  `{"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png"}}]}]}`,
			wantErr:  true,
			wantText: "messages.0.content.0.source.data: Field required",
		},
		{
			name:     "svg top level image rejected",
			payload:  `{"model":"claude-opus-4-5","messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + svgData + `"}}]}]}`,
			wantErr:  true,
			wantText: `messages.0.content.0.source: SVG images are not supported by requested model "claude-opus-4-5"`,
		},
		{
			name:     "svg tool result image rejected",
			payload:  `{"model":"claude-opus-4-5","messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + svgData + `"}}]}]}]}`,
			wantErr:  true,
			wantText: `messages.0.content.0.content.0.source: SVG images are not supported by requested model "claude-opus-4-5"`,
		},
		{
			name:    "valid png image passes",
			payload: `{"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + pngHeader + `"}}]}]}`,
			wantErr: false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateClaudeImagesForGoogleUpstream([]byte(tt.payload))
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if err.Message != tt.wantText {
					t.Fatalf("message = %q, want %q", err.Message, tt.wantText)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}
