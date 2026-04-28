package llm

import (
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
)

func TestParseOpenAISSEStream_ThinkingEndBeforeToolUse(t *testing.T) {
	// Simulates the GLM/DeepSeek pattern where reasoning_content is followed
	// directly by tool_calls without a content field in between.
	// Before the fix, thinking_end was never emitted in this case, causing
	// agent-side thinkingActive to stay true and the TUI to split thinking
	// into multiple cards.
	stream := strings.Join([]string{
		`data: {"id":"chatcmpl-test","model":"sample/test-model","choices":[{"index":0,"delta":{"reasoning_content":"I need to read the file"}}]}`,
		`data: {"id":"chatcmpl-test","model":"sample/test-model","choices":[{"index":0,"delta":{"reasoning_content":" first."}}]}`,
		`data: {"id":"chatcmpl-test","model":"sample/test-model","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"Read"}}]}}]}`,
		`data: {"id":"chatcmpl-test","model":"sample/test-model","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"path\":\"README.md\"}"}}]}}]}`,
		`data: {"id":"chatcmpl-test","model":"sample/test-model","choices":[{"index":0,"finish_reason":"tool_calls"}]}`,
		`data: [DONE]`,
		"",
	}, "\n")

	var deltas []message.StreamDelta
	cb := func(delta message.StreamDelta) {
		deltas = append(deltas, delta)
	}

	resp, err := parseOpenAISSEStream(strings.NewReader(stream), cb, nil)
	if err != nil {
		t.Fatalf("parseOpenAISSEStream returned error: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}

	// Verify thinking_end was emitted before tool_use_start.
	var thinkingEndIdx int = -1
	var toolUseStartIdx int = -1
	for i, d := range deltas {
		switch d.Type {
		case "thinking_end":
			thinkingEndIdx = i
		case "tool_use_start":
			toolUseStartIdx = i
		}
	}

	if thinkingEndIdx < 0 {
		t.Fatal("expected thinking_end delta to be emitted when tool_use_start arrives during thinking")
	}
	if toolUseStartIdx < 0 {
		t.Fatal("expected tool_use_start delta")
	}
	if thinkingEndIdx >= toolUseStartIdx {
		t.Fatalf("expected thinking_end (index %d) before tool_use_start (index %d)", thinkingEndIdx, toolUseStartIdx)
	}

	// Verify the response has the tool call. (Reasoning content in
	// OpenAI-compatible providers is only stored in ReasoningContent
	// when ThinkingToolcallMarkerHit is true, so we don't assert that.)
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(resp.ToolCalls))
	}
}

func TestParseOpenAISSEStream_ThinkingEndNotEmittedWhenNoToolCalls(t *testing.T) {
	// When reasoning_content is followed by content (not tool_calls),
	// the existing logic already emits thinking_end on content arrival.
	// This test verifies no duplicate thinking_end is emitted.
	stream := strings.Join([]string{
		`data: {"id":"chatcmpl-test","model":"sample/test-model","choices":[{"index":0,"delta":{"reasoning_content":"Let me think..."}}]}`,
		`data: {"id":"chatcmpl-test","model":"sample/test-model","choices":[{"index":0,"delta":{"content":"Here is the answer."}}]}`,
		`data: {"id":"chatcmpl-test","model":"sample/test-model","choices":[{"index":0,"finish_reason":"stop"}]}`,
		`data: [DONE]`,
		"",
	}, "\n")

	var thinkingEndCount int
	cb := func(delta message.StreamDelta) {
		if delta.Type == "thinking_end" {
			thinkingEndCount++
		}
	}

	resp, err := parseOpenAISSEStream(strings.NewReader(stream), cb, nil)
	if err != nil {
		t.Fatalf("parseOpenAISSEStream returned error: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	if thinkingEndCount != 1 {
		t.Fatalf("expected exactly 1 thinking_end, got %d", thinkingEndCount)
	}
}

func TestParseOpenAISSEStream_ThinkingEndNotDoubleEmittedWithToolAndContent(t *testing.T) {
	// When reasoning_content is followed by tool_calls and then content,
	// thinking_end should be emitted exactly once (from the tool_use_start path,
	// not again from the content path).
	stream := strings.Join([]string{
		`data: {"id":"chatcmpl-test","model":"sample/test-model","choices":[{"index":0,"delta":{"reasoning_content":"I need to check."}}]}`,
		`data: {"id":"chatcmpl-test","model":"sample/test-model","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"Bash"}}]}}]}`,
		`data: {"id":"chatcmpl-test","model":"sample/test-model","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"command\":\"ls\"}"}}]}}]}`,
		`data: {"id":"chatcmpl-test","model":"sample/test-model","choices":[{"index":0,"delta":{"content":"Done."}}]}`,
		`data: {"id":"chatcmpl-test","model":"sample/test-model","choices":[{"index":0,"finish_reason":"stop"}]}`,
		`data: [DONE]`,
		"",
	}, "\n")

	var thinkingEndCount int
	cb := func(delta message.StreamDelta) {
		if delta.Type == "thinking_end" {
			thinkingEndCount++
		}
	}

	resp, err := parseOpenAISSEStream(strings.NewReader(stream), cb, nil)
	if err != nil {
		t.Fatalf("parseOpenAISSEStream returned error: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	if thinkingEndCount != 1 {
		t.Fatalf("expected exactly 1 thinking_end, got %d", thinkingEndCount)
	}
}

func TestParseOpenAISSEStream_NoThinkingEndWithoutReasoning(t *testing.T) {
	// When there is no reasoning_content at all, thinking_end should never
	// be emitted, even with tool_calls.
	stream := strings.Join([]string{
		`data: {"id":"chatcmpl-test","model":"sample/test-model","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"Read"}}]}}]}`,
		`data: {"id":"chatcmpl-test","model":"sample/test-model","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"path\":\"README.md\"}"}}]}}]}`,
		`data: {"id":"chatcmpl-test","model":"sample/test-model","choices":[{"index":0,"finish_reason":"tool_calls"}]}`,
		`data: [DONE]`,
		"",
	}, "\n")

	var thinkingEndCount int
	cb := func(delta message.StreamDelta) {
		if delta.Type == "thinking_end" {
			thinkingEndCount++
		}
	}

	resp, err := parseOpenAISSEStream(strings.NewReader(stream), cb, nil)
	if err != nil {
		t.Fatalf("parseOpenAISSEStream returned error: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	if thinkingEndCount != 0 {
		t.Fatalf("expected 0 thinking_end (no reasoning), got %d", thinkingEndCount)
	}
}
