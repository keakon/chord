package llm

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
)

func TestAnthropicStreamCapturesRedactedThinking(t *testing.T) {
	sse := strings.Join([]string{
		"event: message_start",
		`data: {"type":"message_start","message":{"role":"assistant","usage":{"input_tokens":10}}}`,
		"",
		"event: content_block_start",
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"redacted_thinking","data":"enc-redacted"}}`,
		"",
		"event: content_block_stop",
		`data: {"type":"content_block_stop","index":0}`,
		"",
		"event: content_block_start",
		`data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`,
		"",
		"event: content_block_delta",
		`data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"hi"}}`,
		"",
		"event: content_block_stop",
		`data: {"type":"content_block_stop","index":1}`,
		"",
		"event: message_delta",
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}`,
		"",
		"event: message_stop",
		`data: {"type":"message_stop"}`,
		"",
	}, "\n")

	resp, err := parseSSEStream(strings.NewReader(sse), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.ThinkingBlocks) != 1 {
		t.Fatalf("expected 1 thinking block, got %d", len(resp.ThinkingBlocks))
	}
	tb := resp.ThinkingBlocks[0]
	if tb.Data != "enc-redacted" || tb.Thinking != "" || tb.Signature != "" {
		t.Fatalf("unexpected block: %+v", tb)
	}
	if !tb.Replayable() {
		t.Fatal("redacted block must be replayable")
	}
	if resp.Content != "hi" {
		t.Fatalf("unexpected content: %q", resp.Content)
	}
}

func TestAnthropicHistoryReplaysRedactedThinking(t *testing.T) {
	msgs := []message.Message{
		{Role: message.RoleUser, Content: "go"},
		{
			Role: message.RoleAssistant,
			ThinkingBlocks: []message.ThinkingBlock{
				{Data: "enc-redacted"},
				{Thinking: "visible", Signature: "sig"},
			},
			Content: "done",
		},
	}
	converted := convertMessages(msgs)
	if len(converted) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(converted))
	}
	blocks, ok := converted[1].Content.([]anthropicContent)
	if !ok {
		t.Fatalf("expected content blocks, got %T", converted[1].Content)
	}
	if blocks[0].Type != "redacted_thinking" || blocks[0].Data != "enc-redacted" {
		t.Fatalf("unexpected first block: %+v", blocks[0])
	}
	raw, err := json.Marshal(blocks[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "thinking\":") || strings.Contains(string(raw), "signature") {
		t.Fatalf("redacted block must not serialize thinking/signature fields: %s", raw)
	}
	if blocks[1].Type != "thinking" || blocks[1].Thinking != "visible" || blocks[1].Signature != "sig" {
		t.Fatalf("unexpected second block: %+v", blocks[1])
	}
}

func TestAnthropicHistoryReplaysOmittedThinking(t *testing.T) {
	block := message.ThinkingBlock{Signature: "sig-omitted"}
	if !block.Replayable() {
		t.Fatal("signature-only thinking block must be replayable")
	}
	converted := convertMessages([]message.Message{{
		Role:           message.RoleAssistant,
		ThinkingBlocks: []message.ThinkingBlock{block},
		ToolCalls:      []message.ToolCall{{ID: "call-1", Name: "read", Args: json.RawMessage(`{}`)}},
	}})
	if len(converted) != 1 {
		t.Fatalf("expected one assistant message, got %d", len(converted))
	}
	blocks, ok := converted[0].Content.([]anthropicContent)
	if !ok || len(blocks) != 2 {
		t.Fatalf("expected thinking and tool blocks, got %#v", converted[0].Content)
	}
	if blocks[0].Type != "thinking" || blocks[0].Thinking != "" || blocks[0].Signature != "sig-omitted" {
		t.Fatalf("signature-only thinking block changed: %+v", blocks[0])
	}
}

func TestGeminiStreamCapturesThoughtSignatures(t *testing.T) {
	sse := strings.Join([]string{
		`data: {"candidates":[{"content":{"parts":[{"text":"thinking...","thought":true,"thoughtSignature":"sig-text"}]}}]}`,
		`data: {"candidates":[{"content":{"parts":[{"functionCall":{"name":"read","args":{"path":"a.go"}},"thoughtSignature":"sig-fc"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5}}`,
		"",
	}, "\n")

	resp, err := parseGeminiSSEStream(strings.NewReader(sse), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.GeminiParts) != 2 || resp.GeminiParts[0].Type != "thought" || resp.GeminiParts[0].ThoughtSignature != "sig-text" {
		t.Fatalf("expected signed thought part captured, got %+v", resp.GeminiParts)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(resp.ToolCalls))
	}
	if resp.ToolCalls[0].ThoughtSignature != "sig-fc" {
		t.Fatalf("expected function-call signature captured, got %q", resp.ToolCalls[0].ThoughtSignature)
	}
}

func TestGeminiHistoryReplaysThoughtSignatures(t *testing.T) {
	msgs := []message.Message{
		{Role: message.RoleUser, Content: "go"},
		{
			Role:    message.RoleAssistant,
			Content: "working",
			GeminiParts: []message.GeminiReplayPart{
				{Type: "thought", Text: "thinking", ThoughtSignature: "sig-thought"},
				{Type: "text", Text: "working", ThoughtSignature: "sig-text"},
				{Type: "function_call", ToolCallID: "gemini_0", ThoughtSignature: "sig-fc"},
			},
			ToolCalls: []message.ToolCall{
				{ID: "gemini_0", Name: "read", Args: json.RawMessage(`{"path":"a.go"}`), ThoughtSignature: "sig-fc"},
			},
		},
		{Role: message.RoleTool, ToolCallID: "gemini_0", Content: "contents"},
	}
	contents := convertMessagesToGemini(msgs)
	if len(contents) != 3 {
		t.Fatalf("expected 3 contents, got %d", len(contents))
	}
	parts := contents[1].Parts
	if len(parts) != 3 {
		t.Fatalf("expected 3 assistant parts, got %d", len(parts))
	}
	if !parts[0].Thought || parts[0].Text != "thinking" || parts[0].ThoughtSignature != "sig-thought" {
		t.Fatalf("unexpected thought part: %+v", parts[0])
	}
	if parts[1].Thought || parts[1].Text != "working" || parts[1].ThoughtSignature != "sig-text" {
		t.Fatalf("unexpected text part: %+v", parts[1])
	}
	if parts[2].FunctionCall == nil || parts[2].ThoughtSignature != "sig-fc" {
		t.Fatalf("unexpected functionCall part: %+v", parts[2])
	}
}

func TestGeminiStreamPreservesSignatureOnlyPart(t *testing.T) {
	sse := strings.Join([]string{
		`data: {"candidates":[{"content":{"parts":[{"text":"answer"}]}}]}`,
		`data: {"candidates":[{"content":{"parts":[{"text":"","thoughtSignature":"sig-final"}]} ,"finishReason":"STOP"}]}`,
		"",
	}, "\n")
	resp, err := parseGeminiSSEStream(strings.NewReader(sse), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.GeminiParts) != 1 || resp.GeminiParts[0].Text != "answer" || resp.GeminiParts[0].ThoughtSignature != "sig-final" {
		t.Fatalf("signature-only delta must stay on its text part: %+v", resp.GeminiParts)
	}
}

func TestGeminiStreamPreservesSignedPartBoundaries(t *testing.T) {
	sse := strings.Join([]string{
		`data: {"candidates":[{"content":{"parts":[{"text":"first","thoughtSignature":"sig-1"}]}}]}`,
		`data: {"candidates":[{"content":{"parts":[{"text":"second","thoughtSignature":"sig-2"}]} ,"finishReason":"STOP"}]}`,
		"",
	}, "\n")
	resp, err := parseGeminiSSEStream(strings.NewReader(sse), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.GeminiParts) != 2 {
		t.Fatalf("signed parts must not be merged: %+v", resp.GeminiParts)
	}
	if resp.GeminiParts[0].Text != "first" || resp.GeminiParts[0].ThoughtSignature != "sig-1" || resp.GeminiParts[1].Text != "second" || resp.GeminiParts[1].ThoughtSignature != "sig-2" {
		t.Fatalf("signed part boundaries changed: %+v", resp.GeminiParts)
	}
}

func TestEnsureGeminiActiveLoopSignatures(t *testing.T) {
	contents := []geminiContent{
		{Role: "user", Parts: []geminiPart{{Text: "go"}}},
		{Role: "model", Parts: []geminiPart{{FunctionCall: &geminiFunctionCall{Name: "read"}}, {FunctionCall: &geminiFunctionCall{Name: "write"}}}},
		{Role: "user", Parts: []geminiPart{{FunctionResponse: &geminiFunctionResponse{Name: "read"}}}},
	}
	ensureGeminiActiveLoopSignatures(contents, "gemini-3.5-pro")
	want := geminiSkipThoughtSignatureValidator
	if contents[1].Parts[0].ThoughtSignature != want || contents[1].Parts[1].ThoughtSignature != "" {
		t.Fatalf("synthetic signature must be added only to the first function call: %+v", contents[1].Parts)
	}
}
