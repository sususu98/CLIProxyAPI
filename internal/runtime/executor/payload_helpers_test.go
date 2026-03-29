package executor

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/tidwall/gjson"
)

func TestApplyArrayElementFilter_HasKey(t *testing.T) {
	t.Parallel()

	payload := []byte(`{"tools":[{"googleSearch":{}},{"functionDeclarations":[{"name":"test"}]}]}`)
	match := config.ElementMatch{Key: "googleSearch"}

	result := applyArrayElementFilter(payload, "tools", match)

	tools := gjson.GetBytes(result, "tools")
	if !tools.IsArray() {
		t.Fatal("expected tools to be an array")
	}
	if len(tools.Array()) != 1 {
		t.Errorf("expected 1 element, got %d", len(tools.Array()))
	}
	if !tools.Array()[0].Get("functionDeclarations").Exists() {
		t.Error("expected functionDeclarations element to remain")
	}
}

func TestApplyArrayElementFilter_HasKeyWithValue(t *testing.T) {
	t.Parallel()

	payload := []byte(`{"tools":[{"googleSearch":{}},{"googleSearch":{"mode":"grounding"}},{"other":{}}]}`)
	match := config.ElementMatch{Key: "googleSearch", Value: map[string]any{}}

	result := applyArrayElementFilter(payload, "tools", match)

	tools := gjson.GetBytes(result, "tools")
	if len(tools.Array()) != 2 {
		t.Errorf("expected 2 elements (mode:grounding and other), got %d", len(tools.Array()))
	}
}

func TestApplyArrayElementFilter_NonExistentPath(t *testing.T) {
	t.Parallel()

	payload := []byte(`{"other":"value"}`)
	match := config.ElementMatch{Key: "googleSearch"}

	result := applyArrayElementFilter(payload, "tools", match)

	if string(result) != string(payload) {
		t.Error("expected payload to remain unchanged for non-existent path")
	}
}

func TestApplyArrayElementFilter_EmptyKey(t *testing.T) {
	t.Parallel()

	payload := []byte(`{"tools":[{"googleSearch":{}}]}`)
	match := config.ElementMatch{Key: ""}

	result := applyArrayElementFilter(payload, "tools", match)

	if string(result) != string(payload) {
		t.Error("expected payload to remain unchanged for empty key")
	}
}

func TestApplyArrayElementFilter_NonObjectElements(t *testing.T) {
	t.Parallel()

	payload := []byte(`{"items":["string",123,{"googleSearch":{}},null]}`)
	match := config.ElementMatch{Key: "googleSearch"}

	result := applyArrayElementFilter(payload, "items", match)

	items := gjson.GetBytes(result, "items")
	if len(items.Array()) != 3 {
		t.Errorf("expected 3 elements (non-objects preserved), got %d", len(items.Array()))
	}
}

func TestApplyArrayElementFilter_AllFiltered(t *testing.T) {
	t.Parallel()

	payload := []byte(`{"tools":[{"googleSearch":{}},{"googleSearch":{"x":1}}]}`)
	match := config.ElementMatch{Key: "googleSearch"}

	result := applyArrayElementFilter(payload, "tools", match)

	tools := gjson.GetBytes(result, "tools")
	if !tools.IsArray() {
		t.Fatal("expected tools to remain an array")
	}
	if len(tools.Array()) != 0 {
		t.Errorf("expected empty array, got %d elements", len(tools.Array()))
	}
}

func TestApplyPayloadConfigWithRoot_ArrayElementFilter(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Payload: config.PayloadConfig{
			Filter: []config.PayloadFilterRule{
				{
					Models: []config.PayloadModelRule{
						{Name: "gemini-3-*", Protocol: "antigravity"},
					},
					ArrayElementFilter: []config.ArrayElementFilter{
						{Path: "tools", Match: config.ElementMatch{Key: "googleSearch"}},
					},
				},
			},
		},
	}

	payload := []byte(`{"model":"gemini-3-flash","request":{"tools":[{"googleSearch":{}},{"functionDeclarations":[]}]}}`)

	result := applyPayloadConfigWithRoot(cfg, "gemini-3-flash", "antigravity", "request", payload, nil, "gemini-3-flash")

	tools := gjson.GetBytes(result, "request.tools")
	if len(tools.Array()) != 1 {
		t.Errorf("expected 1 tool after filtering, got %d", len(tools.Array()))
	}
	if !tools.Array()[0].Get("functionDeclarations").Exists() {
		t.Error("expected functionDeclarations to remain")
	}
}

func TestApplyPayloadConfigWithRoot_MultipleFilters(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Payload: config.PayloadConfig{
			Filter: []config.PayloadFilterRule{
				{
					Models: []config.PayloadModelRule{
						{Name: "*", Protocol: "antigravity"},
					},
					ArrayElementFilter: []config.ArrayElementFilter{
						{Path: "tools", Match: config.ElementMatch{Key: "googleSearch"}},
						{Path: "tools", Match: config.ElementMatch{Key: "codeExecution"}},
					},
				},
			},
		},
	}

	payload := []byte(`{"request":{"tools":[{"googleSearch":{}},{"codeExecution":{}},{"functionDeclarations":[]}]}}`)

	result := applyPayloadConfigWithRoot(cfg, "test-model", "antigravity", "request", payload, nil, "test-model")

	tools := gjson.GetBytes(result, "request.tools")
	if len(tools.Array()) != 1 {
		t.Errorf("expected 1 tool after filtering both googleSearch and codeExecution, got %d", len(tools.Array()))
	}
}

func TestElementMatchValueEquals_TypeAware(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		actual   string
		expected any
		want     bool
	}{
		{"empty object match", `{}`, map[string]any{}, true},
		{"object with content", `{"mode":"grounding"}`, map[string]any{"mode": "grounding"}, true},
		{"object mismatch", `{"mode":"grounding"}`, map[string]any{}, false},
		{"number float", `1.0`, float64(1.0), true},
		{"number int as float", `1`, float64(1), true},
		{"string match", `"hello"`, "hello", true},
		{"string mismatch", `"hello"`, "world", false},
		{"bool true", `true`, true, true},
		{"bool false", `false`, false, true},
		{"bool mismatch", `true`, false, false},
		{"null match", `null`, nil, true},
		{"array match", `[1,2,3]`, []any{float64(1), float64(2), float64(3)}, true},
		{"array mismatch order", `[1,2,3]`, []any{float64(3), float64(2), float64(1)}, false},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			actual := gjson.Parse(tt.actual)
			got := elementMatchValueEquals(actual, tt.expected)
			if got != tt.want {
				t.Errorf("elementMatchValueEquals(%s, %v) = %v, want %v", tt.actual, tt.expected, got, tt.want)
			}
		})
	}
}
