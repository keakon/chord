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
		case message.StreamDeltaThinkingEnd:
			thinkingEndIdx = i
		case message.StreamDeltaToolUseStart:
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
		`data: {"id":"chatcmpl-test","model":"sample/test-model","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"Shell"}}]}}]}`,
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

func TestParseOpenAISSEStream_PreservesReasoningContentWithoutMarkers(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"id":"chatcmpl-test","model":"sample/test-model","choices":[{"index":0,"delta":{"reasoning_content":"I need to inspect the file"}}]}`,
		`data: {"id":"chatcmpl-test","model":"sample/test-model","choices":[{"index":0,"delta":{"reasoning_content":" before calling a tool."}}]}`,
		`data: {"id":"chatcmpl-test","model":"sample/test-model","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"Read"}}]}}]}`,
		`data: {"id":"chatcmpl-test","model":"sample/test-model","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"path\":\"README.md\"}"}}]}}]}`,
		`data: {"id":"chatcmpl-test","model":"sample/test-model","choices":[{"index":0,"finish_reason":"tool_calls"}]}`,
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
	if resp.ReasoningContent != "I need to inspect the file before calling a tool." {
		t.Fatalf("ReasoningContent = %q", resp.ReasoningContent)
	}
	if resp.ThinkingToolcallMarkerHit {
		t.Fatal("ThinkingToolcallMarkerHit should remain false for plain reasoning text")
	}
}

func TestParseOpenAISSEStream_AggregatesPromptCacheUsage(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"id":"chatcmpl-test","model":"sample/test-model","choices":[{"index":0,"delta":{"content":"Hello"}}]}`,
		`data: {"id":"chatcmpl-test","model":"sample/test-model","choices":[{"index":0,"finish_reason":"stop"}],"usage":{"prompt_tokens":10855,"completion_tokens":42,"total_tokens":10897,"prompt_cache_hit_tokens":10752,"prompt_cache_miss_tokens":103}}`,
		`data: [DONE]`,
		"",
	}, "\n")

	resp, err := parseOpenAISSEStream(strings.NewReader(stream), nil, nil)
	if err != nil {
		t.Fatalf("parseOpenAISSEStream returned error: %v", err)
	}
	if resp == nil || resp.Usage == nil {
		t.Fatal("expected non-nil response usage")
	}
	if got := resp.Usage.InputTokens; got != 10855 {
		t.Fatalf("InputTokens = %d, want 10855", got)
	}
	if got := resp.Usage.CacheReadTokens; got != 10752 {
		t.Fatalf("CacheReadTokens = %d, want 10752", got)
	}
	if got := resp.Usage.OutputTokens; got != 42 {
		t.Fatalf("OutputTokens = %d, want 42", got)
	}
}

func TestParseOpenAISSEStream_AggregatesDeepSeekCacheReadUsage(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"id":"chatcmpl-test","model":"deepseek-v4-flash","choices":[{"index":0,"delta":{"content":"Hello"}}]}`,
		`data: {"id":"chatcmpl-test","model":"deepseek-v4-flash","choices":[{"index":0,"finish_reason":"stop"}],"usage":{"prompt_tokens":72531,"completion_tokens":120,"total_tokens":72651,"cache_read_tokens":31232,"completion_tokens_details":{"reasoning_tokens":17}}}`,
		`data: [DONE]`,
		"",
	}, "\n")

	resp, err := parseOpenAISSEStream(strings.NewReader(stream), nil, nil)
	if err != nil {
		t.Fatalf("parseOpenAISSEStream returned error: %v", err)
	}
	if resp == nil || resp.Usage == nil {
		t.Fatal("expected non-nil response usage")
	}
	if got := resp.Usage.InputTokens; got != 72531 {
		t.Fatalf("InputTokens = %d, want 72531", got)
	}
	if got := resp.Usage.CacheReadTokens; got != 31232 {
		t.Fatalf("CacheReadTokens = %d, want 31232", got)
	}
	if got := resp.Usage.OutputTokens; got != 120 {
		t.Fatalf("OutputTokens = %d, want 120", got)
	}
	if got := resp.Usage.ReasoningTokens; got != 17 {
		t.Fatalf("ReasoningTokens = %d, want 17", got)
	}
}

func TestParseOpenAISSEStream_AggregatesOpenAICachedTokenDetails(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"id":"chatcmpl-test","model":"sample/test-model","choices":[{"index":0,"delta":{"content":"Hello"}}]}`,
		`data: {"id":"chatcmpl-test","model":"sample/test-model","choices":[{"index":0,"finish_reason":"stop"}],"usage":{"prompt_tokens":2048,"completion_tokens":8,"total_tokens":2056,"prompt_tokens_details":{"cached_tokens":1024}}}`,
		`data: [DONE]`,
		"",
	}, "\n")

	resp, err := parseOpenAISSEStream(strings.NewReader(stream), nil, nil)
	if err != nil {
		t.Fatalf("parseOpenAISSEStream returned error: %v", err)
	}
	if resp == nil || resp.Usage == nil {
		t.Fatal("expected non-nil response usage")
	}
	if got := resp.Usage.CacheReadTokens; got != 1024 {
		t.Fatalf("CacheReadTokens = %d, want 1024", got)
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
