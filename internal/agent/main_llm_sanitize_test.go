package agent

import (
	"encoding/json"
	"testing"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func TestSanitizeResponseZeroWidthStripsModelGeneratedText(t *testing.T) {
	args := json.RawMessage(`{"path":"` + "README\u200b.md" + `"}`)
	resp := &message.Response{
		Content:          "ok \u200bbeep",
		ReasoningContent: "\u200cthink\u200b",
		ThinkingBlocks: []message.ThinkingBlock{
			{Thinking: "\u200blooks", Signature: "sig-1\u200b"},
		},
		ToolCalls: []message.ToolCall{{ID: "call-1", Name: tools.NameRead, Args: args}},
	}
	counts := sanitizeResponseZeroWidth(resp)
	if resp.Content != "ok beep" {
		t.Fatalf("content = %q, want zero-width chars stripped", resp.Content)
	}
	if resp.ReasoningContent != "think" {
		t.Fatalf("reasoning = %q, want zero-width chars stripped", resp.ReasoningContent)
	}
	if len(resp.ThinkingBlocks) != 1 || resp.ThinkingBlocks[0].Thinking != "looks" {
		t.Fatalf("thinking text = %#v, want stripped", resp.ThinkingBlocks)
	}
	// Replay-contract payloads must stay byte-identical even when they
	// carry the same invisible bytes as a contamination probe.

	if got := resp.ThinkingBlocks[0].Signature; got != "sig-1\u200b" {
		t.Fatalf("signature = %q, want preserved verbatim (replay contract)", got)
	}
	if got := string(resp.ToolCalls[0].Args); got != `{"path":"README.md"}` {
		t.Fatalf("tool call args = %s, want stripped", got)
	}
	for field, want := range map[string]int{
		"content":        1,
		"reasoning":      2,
		"thinking":       1,
		"tool_call_args": 1,
	} {
		got := counts[field]['\u200b'] + counts[field]['\u200c']
		if got != want {
			t.Fatalf("fields %q stripped count = %d, want %d", field, got, want)
		}
	}
	// An emoji zygote sequence must keep its ZWJs: strip semantics preserve
	// real joins (the same rule tools.StripZeroWidthFormat already owns).

	emojiJSON := `{"path":"` + "\U0001f468\u200d\U0001f469" + `"}`
	emojiArgs := json.RawMessage(emojiJSON)
	resp2 := &message.Response{ToolCalls: []message.ToolCall{{ID: "call-2", Name: tools.NameRead, Args: emojiArgs}}}
	sanitizeResponseZeroWidth(resp2)
	if got := string(resp2.ToolCalls[0].Args); got != emojiJSON {
		t.Fatalf("emoji ZWJ in tool args was stripped: %s", got)
	}
}

func TestFormatInvisibleCounts(t *testing.T) {
	got := formatInvisibleCounts(map[string]map[rune]int{
		"content":        {'\u200b': 1},
		"tool_call_args": {'\u200b': 2},
	})
	want := "content:U+200B×1; tool_call_args:U+200B×2"
	if got != want {
		t.Fatalf("formatInvisibleCounts() = %q, want %q", got, want)
	}
}

func TestRecordStreamingToolCallSanitizesArgs(t *testing.T) {
	turn := &Turn{}
	dirty := `{"command":"` + "ls\u200b-a" + `"}`
	turn.recordStreamingToolCall(PendingToolCall{
		CallID:   "call-1",
		Name:     tools.NameShell,
		ArgsJSON: dirty,
	})
	got := turn.streamingToolCalls["call-1"].ArgsJSON
	if got != `{"command":"ls-a"}` {
		t.Fatalf("streamed args mismatch: got=%#q want=%#q", got, `{"command":"ls-a"}`)
	}
	if turn.streamingToolCalls["call-1"].Name != tools.NameShell {
		t.Fatalf("streamed tool name = %q, want preserved", turn.streamingToolCalls["call-1"].Name)
	}
}
