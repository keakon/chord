package llm

import (
	"strings"
	"testing"
)

func TestParseOpenAISSEStream_ThinkingToolcallMarkerHit(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"id":"chatcmpl-test","model":"kimi-k2.5","choices":[{"index":0,"delta":{"reasoning_content":"<|tool_calls_section_begin|>"}}]}`,
		`data: {"id":"chatcmpl-test","model":"kimi-k2.5","choices":[{"index":0,"delta":{"reasoning_content":"functions.Bash:11 <|tool_call_argument_begin|>"}}]}`,
		`data: {"id":"chatcmpl-test","model":"kimi-k2.5","choices":[{"index":0,"finish_reason":"stop"}]}`,
		`data: [DONE]`,
		"",
	}, "\n")

	resp, err := parseOpenAISSEStream(strings.NewReader(stream), nil, nil)
	if err != nil {
		t.Fatalf("parseOpenAISSEStream returned error: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	if !resp.ThinkingToolcallMarkerHit {
		t.Fatal("expected ThinkingToolcallMarkerHit=true")
	}
	if resp.ReasoningContent == "" {
		t.Fatal("expected ReasoningContent to be populated when marker hit")
	}
	if !strings.Contains(resp.ReasoningContent, "<|tool_calls_section_begin|>") {
		t.Fatalf("expected ReasoningContent to contain marker, got %q", resp.ReasoningContent)
	}
	if got := len(resp.ToolCalls); got != 0 {
		t.Fatalf("expected no tool calls, got %d", got)
	}
	if resp.StopReason != "stop" {
		t.Fatalf("expected stop_reason=stop, got %q", resp.StopReason)
	}
}

func TestParseOpenAISSEStream_ThinkingToolcallMarkerSplitAcrossChunks(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"id":"chatcmpl-test","model":"kimi-k2.5","choices":[{"index":0,"delta":{"reasoning_content":"<|tool_call_begin|> functions.Bash:"}}]}`,
		`data: {"id":"chatcmpl-test","model":"kimi-k2.5","choices":[{"index":0,"delta":{"reasoning_content":"11 <|tool_call_argument_begin|> {"}}]}`,
		`data: {"id":"chatcmpl-test","model":"kimi-k2.5","choices":[{"index":0,"finish_reason":"stop"}]}`,
		`data: [DONE]`,
		"",
	}, "\n")

	resp, err := parseOpenAISSEStream(strings.NewReader(stream), nil, nil)
	if err != nil {
		t.Fatalf("parseOpenAISSEStream returned error: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	if !resp.ThinkingToolcallMarkerHit {
		t.Fatal("expected ThinkingToolcallMarkerHit=true for split marker")
	}
}

func TestParseOpenAISSEStream_NormalToolCalls_NoThinkingMarker(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"id":"chatcmpl-test","model":"gpt-5.5-mini","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"Read"}}]}}]}`,
		`data: {"id":"chatcmpl-test","model":"gpt-5.5-mini","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"path\":\"README.md\"}"}}]}}]}`,
		`data: {"id":"chatcmpl-test","model":"gpt-5.5-mini","choices":[{"index":0,"finish_reason":"tool_calls"}]}`,
		`data: [DONE]`,
		"",
	}, "\n")

	resp, err := parseOpenAISSEStream(strings.NewReader(stream), nil, nil)
	if err != nil {
		t.Fatalf("parseOpenAISSEStream returned error: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	if resp.ThinkingToolcallMarkerHit {
		t.Fatal("expected ThinkingToolcallMarkerHit=false")
	}
	if resp.ReasoningContent != "" {
		t.Fatalf("expected empty ReasoningContent when no marker hit, got %q", resp.ReasoningContent)
	}
	if got := len(resp.ToolCalls); got != 1 {
		t.Fatalf("expected one tool call, got %d", got)
	}
	if resp.ToolCalls[0].Name != "Read" {
		t.Fatalf("expected tool call name Read, got %q", resp.ToolCalls[0].Name)
	}
}
