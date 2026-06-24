package llm

import (
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
)

func TestParseOpenAISSEStream_ThinkingToolcallMarkerHit(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"id":"chatcmpl-test","model":"kimi-k2.5","choices":[{"index":0,"delta":{"reasoning_content":"<|tool_calls_section_begin|>"}}]}`,
		`data: {"id":"chatcmpl-test","model":"kimi-k2.5","choices":[{"index":0,"delta":{"reasoning_content":"functions.Shell:11 <|tool_call_argument_begin|>"}}]}`,
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
		`data: {"id":"chatcmpl-test","model":"kimi-k2.5","choices":[{"index":0,"delta":{"reasoning_content":"<|tool_call_begin|> functions.Shell:"}}]}`,
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

func TestParseOpenAISSEStream_NormalToolCallsWithoutReasoning_LeavesReasoningContentEmpty(t *testing.T) {
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
		t.Fatalf("expected empty ReasoningContent when the stream contains no reasoning deltas, got %q", resp.ReasoningContent)
	}
	if got := len(resp.ToolCalls); got != 1 {
		t.Fatalf("expected one tool call, got %d", got)
	}
	if resp.ToolCalls[0].Name != "Read" {
		t.Fatalf("expected tool call name Read, got %q", resp.ToolCalls[0].Name)
	}
}

func TestParseOpenAISSEStream_DoesNotEmitToolCallbacksForMalformedToolCall(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"id":"chatcmpl-test","model":"glm","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"","type":"function","function":{"name":""}}]}}]}`,
		`data: {"id":"chatcmpl-test","model":"glm","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"command\":\"pwd\"}"}}]}}]}`,
		`data: {"id":"chatcmpl-test","model":"glm","choices":[{"index":0,"finish_reason":"tool_calls"}]}`,
		`data: [DONE]`,
		"",
	}, "\n")

	var callbacks []string
	resp, err := parseOpenAISSEStream(strings.NewReader(stream), func(delta message.StreamDelta) {
		if delta.Type == message.StreamDeltaToolUseStart || delta.Type == message.StreamDeltaToolUseDelta || delta.Type == message.StreamDeltaToolUseEnd {
			callbacks = append(callbacks, delta.Type)
		}
	}, nil)
	if err != nil {
		t.Fatalf("parseOpenAISSEStream returned error: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	if len(resp.ToolCalls) != 0 {
		t.Fatalf("tool calls = %#v, want malformed call discarded", resp.ToolCalls)
	}
	if len(callbacks) != 0 {
		t.Fatalf("tool callbacks = %#v, want none for malformed call", callbacks)
	}
}

func TestParseOpenAISSEStream_EmitsPairedCallbacksForValidToolCall(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"id":"chatcmpl-test","model":"gpt-5.5-mini","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"Read"}}]}}]}`,
		`data: {"id":"chatcmpl-test","model":"gpt-5.5-mini","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"path\":\"README.md\"}"}}]}}]}`,
		`data: {"id":"chatcmpl-test","model":"gpt-5.5-mini","choices":[{"index":0,"finish_reason":"tool_calls"}]}`,
		`data: [DONE]`,
		"",
	}, "\n")

	var events []string
	resp, err := parseOpenAISSEStream(strings.NewReader(stream), func(delta message.StreamDelta) {
		if delta.ToolCall != nil {
			events = append(events, delta.Type+":"+delta.ToolCall.ID+":"+delta.ToolCall.Name)
		}
	}, nil)
	if err != nil {
		t.Fatalf("parseOpenAISSEStream returned error: %v", err)
	}
	if resp == nil || len(resp.ToolCalls) != 1 {
		t.Fatalf("tool calls = %#v, want one valid call", resp)
	}
	if string(resp.ToolCalls[0].Args) != `{"path":"README.md"}` {
		t.Fatalf("tool args = %s, want README path", resp.ToolCalls[0].Args)
	}
	wantEvents := []string{
		"tool_use_start:call_1:Read",
		"tool_use_delta:call_1:Read",
		"tool_use_end:call_1:Read",
	}
	if strings.Join(events, "|") != strings.Join(wantEvents, "|") {
		t.Fatalf("tool callbacks = %#v, want %#v", events, wantEvents)
	}
}
