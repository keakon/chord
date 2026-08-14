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

func TestParseOpenAISSEStream_AcceptsReasoningAliasesWithoutDuplication(t *testing.T) {
	tests := []struct {
		name  string
		delta string
		want  string
	}{
		{
			name:  "reasoning",
			delta: `"reasoning":"provider reasoning"`,
			want:  "provider reasoning",
		},
		{
			name:  "reasoning_text",
			delta: `"reasoning_text":"provider reasoning text"`,
			want:  "provider reasoning text",
		},
		{
			name:  "prefer reasoning_content",
			delta: `"reasoning_content":"preferred","reasoning":"duplicate","reasoning_text":"duplicate"`,
			want:  "preferred",
		},
		{
			name:  "empty primary falls back to alias",
			delta: `"reasoning_content":"","reasoning":"alias text"`,
			want:  "alias text",
		},
		{
			name:  "non-string alias falls back",
			delta: `"reasoning":{"summary":"ignored"},"reasoning_text":"alias text"`,
			want:  "alias text",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stream := strings.Join([]string{
				`data: {"id":"chatcmpl-test","model":"sample/test-model","choices":[{"index":0,"delta":{` + tt.delta + `}}]}`,
				`data: {"id":"chatcmpl-test","model":"sample/test-model","choices":[{"index":0,"delta":{"content":"answer"}}]}`,
				`data: {"id":"chatcmpl-test","model":"sample/test-model","choices":[{"index":0,"finish_reason":"stop"}]}`,
				`data: [DONE]`,
				"",
			}, "\n")

			var thinkingDeltas []string
			var thinkingEndCount int
			resp, err := parseOpenAISSEStream(strings.NewReader(stream), func(delta message.StreamDelta) {
				switch delta.Type {
				case message.StreamDeltaThinking:
					thinkingDeltas = append(thinkingDeltas, delta.Text)
				case message.StreamDeltaThinkingEnd:
					thinkingEndCount++
				}
			}, nil)
			if err != nil {
				t.Fatalf("parseOpenAISSEStream returned error: %v", err)
			}
			if resp == nil {
				t.Fatal("expected non-nil response")
			}
			if resp.ReasoningContent != tt.want {
				t.Fatalf("ReasoningContent = %q, want %q", resp.ReasoningContent, tt.want)
			}
			if got := strings.Join(thinkingDeltas, ""); got != tt.want {
				t.Fatalf("thinking deltas = %q, want %q", got, tt.want)
			}
			if thinkingEndCount != 1 {
				t.Fatalf("thinking_end count = %d, want 1", thinkingEndCount)
			}
		})
	}
}

func TestParseOpenAISSEStream_IgnoresNonStringReasoningAliases(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"id":"chatcmpl-test","model":"sample/test-model","choices":[{"index":0,"delta":{"reasoning":{"summary":"ignored"},"reasoning_text":["ignored"],"content":"answer"}}]}`,
		`data: {"id":"chatcmpl-test","model":"sample/test-model","choices":[{"index":0,"finish_reason":"stop"}]}`,
		`data: [DONE]`,
		"",
	}, "\n")

	resp, err := parseOpenAISSEStream(strings.NewReader(stream), nil, nil)
	if err != nil {
		t.Fatalf("parseOpenAISSEStream returned error: %v", err)
	}
	if resp == nil || resp.Content != "answer" || resp.ReasoningContent != "" {
		t.Fatalf("response = %#v, want answer without reasoning", resp)
	}
}

func TestParseOpenAISSEStream_StillRejectsMalformedJSON(t *testing.T) {
	stream := "data: {\"choices\":[{\"delta\":{\"reasoning\":{}}}]\n"
	if _, err := parseOpenAISSEStream(strings.NewReader(stream), nil, nil); err == nil || !strings.Contains(err.Error(), "parse stream chunk") {
		t.Fatalf("parseOpenAISSEStream error = %v, want malformed chunk error", err)
	}
}

// TestParseOpenAISSEStream_AliasSwitchMidStreamContinuesOneThinkingBlock covers
// gateways that fail over between upstreams mid-response: the alias field may
// change between chunks, but the reasoning must accumulate into one block with
// a single thinking-end, not restart or duplicate.
func TestParseOpenAISSEStream_AliasSwitchMidStreamContinuesOneThinkingBlock(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"id":"chatcmpl-test","model":"sample/test-model","choices":[{"index":0,"delta":{"reasoning":"first half"}}]}`,
		`data: {"id":"chatcmpl-test","model":"sample/test-model","choices":[{"index":0,"delta":{"reasoning_content":" second half"}}]}`,
		`data: {"id":"chatcmpl-test","model":"sample/test-model","choices":[{"index":0,"delta":{"content":"answer"}}]}`,
		`data: {"id":"chatcmpl-test","model":"sample/test-model","choices":[{"index":0,"finish_reason":"stop"}]}`,
		`data: [DONE]`,
		"",
	}, "\n")

	var thinkingDeltas []string
	var thinkingEndCount int
	resp, err := parseOpenAISSEStream(strings.NewReader(stream), func(delta message.StreamDelta) {
		switch delta.Type {
		case message.StreamDeltaThinking:
			thinkingDeltas = append(thinkingDeltas, delta.Text)
		case message.StreamDeltaThinkingEnd:
			thinkingEndCount++
		}
	}, nil)
	if err != nil {
		t.Fatalf("parseOpenAISSEStream returned error: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	if resp.ReasoningContent != "first half second half" {
		t.Fatalf("ReasoningContent = %q, want the switched aliases concatenated", resp.ReasoningContent)
	}
	if got := strings.Join(thinkingDeltas, ""); got != "first half second half" {
		t.Fatalf("thinking deltas = %q, want continuous accumulation across the alias switch", got)
	}
	if thinkingEndCount != 1 {
		t.Fatalf("thinking_end count = %d, want 1", thinkingEndCount)
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

func TestParseOpenAISSEStream_AggregatesOpenAIPromptTokenDetails(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"id":"chatcmpl-test","model":"sample/test-model","choices":[{"index":0,"delta":{"content":"Hello"}}]}`,
		`data: {"id":"chatcmpl-test","model":"sample/test-model","choices":[{"index":0,"finish_reason":"stop"}],"usage":{"prompt_tokens":2048,"completion_tokens":8,"total_tokens":2056,"prompt_tokens_details":{"cached_tokens":1024,"cache_write_tokens":256}}}`,
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
	if got := resp.Usage.InputTokens; got != 2048 {
		t.Fatalf("InputTokens = %d, want raw provider input 2048", got)
	}
	if got := resp.Usage.CacheReadTokens; got != 1024 {
		t.Fatalf("CacheReadTokens = %d, want 1024", got)
	}
	if got := resp.Usage.CacheWriteTokens; got != 256 {
		t.Fatalf("CacheWriteTokens = %d, want 256", got)
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
