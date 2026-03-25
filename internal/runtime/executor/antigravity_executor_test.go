package executor

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
)

func TestRejectInvalidClaudeMessagesRequest(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		payload string
		wantErr bool
		wantMsg string
	}{
		{
			name: "empty name rejected",
			payload: `{"messages":[
				{"role":"user","content":"hi"},
				{"role":"assistant","content":[
					{"type":"text","text":"checking"},
					{"type":"tool_use","id":"toolu_001","name":"","input":{}}
				]},
				{"role":"user","content":[
					{"type":"tool_result","tool_use_id":"toolu_001","content":"ok"}
				]}
			]}`,
			wantErr: true,
			wantMsg: "messages.1.content.1.tool_use.name: Field required",
		},
		{
			name: "whitespace-only name rejected",
			payload: `{"messages":[
				{"role":"user","content":"hi"},
				{"role":"assistant","content":[
					{"type":"tool_use","id":"toolu_002","name":"  ","input":{}}
				]},
				{"role":"user","content":[
					{"type":"tool_result","tool_use_id":"toolu_002","content":"ok"}
				]}
			]}`,
			wantErr: true,
			wantMsg: "messages.1.content.0.tool_use.name: Field required",
		},
		{
			name: "valid name passes",
			payload: `{"messages":[
				{"role":"user","content":"hi"},
				{"role":"assistant","content":[
					{"type":"tool_use","id":"toolu_003","name":"read_file","input":{}}
				]},
				{"role":"user","content":[
					{"type":"tool_result","tool_use_id":"toolu_003","content":"ok"}
				]}
			]}`,
			wantErr: false,
		},
		{
			name: "missing id rejected",
			payload: `{"messages":[
				{"role":"assistant","content":[
					{"type":"tool_use","name":"read_file","input":{}}
				]}
			]}`,
			wantErr: true,
			wantMsg: "messages.0.content.0.tool_use.id: Field required",
		},
		{
			name: "no tool_use passes",
			payload: `{"messages":[
				{"role":"user","content":"hello"},
				{"role":"assistant","content":[{"type":"text","text":"hi"}]}
			]}`,
			wantErr: false,
		},
		{
			name:    "no messages passes",
			payload: `{"model":"test"}`,
			wantErr: false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := rejectInvalidClaudeMessagesRequest([]byte(tt.payload))
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if !strings.Contains(err.Error(), tt.wantMsg) {
					t.Errorf("error = %q, want substring %q", err.Error(), tt.wantMsg)
				}
				if se, ok := err.(statusErr); ok {
					if se.code != 400 {
						t.Errorf("status code = %d, want 400", se.code)
					}
				} else {
					t.Errorf("expected statusErr, got %T", err)
				}
			} else {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
			}
		})
	}
}

func TestAntigravityExecutorExecute_RejectsSVGImagePayload(t *testing.T) {
	t.Parallel()

	svgData := base64.StdEncoding.EncodeToString([]byte(`<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg" width="200" height="200"></svg>`))
	payload := []byte(`{"model":"claude-opus-4-6","messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + svgData + `"}}]}]}`)

	executor := NewAntigravityExecutor(nil)
	_, err := executor.Execute(
		context.Background(),
		nil,
		cliproxyexecutor.Request{
			Model:   "claude-opus-4-6-thinking",
			Payload: payload,
		},
		cliproxyexecutor.Options{
			SourceFormat: sdktranslator.FromString("claude"),
		},
	)
	if err == nil {
		t.Fatal("expected SVG payload to be rejected before upstream call")
	}
	if !strings.Contains(err.Error(), "SVG images are not supported by requested model") || !strings.Contains(err.Error(), "claude-opus-4-6") {
		t.Fatalf("error = %q, want SVG rejection message", err.Error())
	}

	se, ok := err.(statusErr)
	if !ok {
		t.Fatalf("expected statusErr, got %T", err)
	}
	if se.code != 400 {
		t.Fatalf("status code = %d, want 400", se.code)
	}
}
