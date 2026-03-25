package executor

import (
	"strings"
	"testing"
)

func TestRejectEmptyToolUseName(t *testing.T) {
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
			name: "no tool_use passes",
			payload: `{"messages":[
				{"role":"user","content":"hello"},
				{"role":"assistant","content":[{"type":"text","text":"hi"}]}
			]}`,
			wantErr: false,
		},
		{
			name: "no messages passes",
			payload: `{"model":"test"}`,
			wantErr: false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := rejectEmptyToolUseName([]byte(tt.payload))
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
