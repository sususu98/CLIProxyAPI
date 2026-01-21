package auth

import (
	"bytes"
	"testing"
)

func TestStreamRewriter_StripsThinkingEvents(t *testing.T) {
	rewriter := NewStreamRewriter(StreamRewriteOptions{StripThinking: true})

	chunk := []byte("event: content_block_start\n" +
		"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"thinking\",\"thinking\":\"\"}}\n\n" +
		"event: content_block_start\n" +
		"data: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")

	out := rewriter.RewriteChunk(chunk)

	if bytes.Contains(out, []byte(`"index":0`)) || bytes.Contains(out, []byte(`"thinking"`)) {
		t.Fatalf("expected thinking block to be stripped, got output: %s", string(out))
	}
	if !bytes.Contains(out, []byte(`"index":1`)) || !bytes.Contains(out, []byte(`"text"`)) {
		t.Fatalf("expected non-thinking block to remain, got output: %s", string(out))
	}
}

func TestStreamRewriter_RewritesModel(t *testing.T) {
	rewriter := NewStreamRewriter(StreamRewriteOptions{RewriteModel: "alias-model"})

	chunk := []byte("data: {\"model\":\"claude-opus-4-5\"}\n\n")
	out := rewriter.RewriteChunk(chunk)

	if !bytes.Contains(out, []byte(`"model":"alias-model"`)) {
		t.Fatalf("expected model to be rewritten, got output: %s", string(out))
	}
}

func TestStreamRewriter_RewritesResponseModel(t *testing.T) {
	rewriter := NewStreamRewriter(StreamRewriteOptions{RewriteModel: "alias-model"})

	chunk := []byte("data: {\"response\":{\"model\":\"gpt-5\"}}\n\n")
	out := rewriter.RewriteChunk(chunk)

	if !bytes.Contains(out, []byte(`"model":"alias-model"`)) {
		t.Fatalf("expected response.model to be rewritten, got output: %s", string(out))
	}
}
