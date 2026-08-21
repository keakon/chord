package sessionview

import (
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
)

func textMsg(role message.Role, content string) message.Message {
	return message.Message{Role: role, Content: content}
}

func TestProjectUserPlainText(t *testing.T) {
	got, ok := Project(textMsg(message.RoleUser, "  Use focused tests first.  "))
	if !ok || got.Kind != KindUser || strings.TrimSpace(got.Text) != "Use focused tests first." {
		t.Fatalf("projected = %+v ok=%v", got, ok)
	}
	// Empty user message is skipped.
	if _, ok := Project(textMsg(message.RoleUser, "   ")); ok {
		t.Fatal("empty user message must be skipped")
	}
	// Synthetic kinds are excluded.
	if _, ok := Project(message.Message{Role: message.RoleUser, Content: "job done", Kind: message.KindBackgroundResult}); ok {
		t.Fatal("background result must be excluded")
	}
	if _, ok := Project(message.Message{Role: message.RoleUser, Content: "loop", Kind: message.KindLoopNotice}); ok {
		t.Fatal("loop notice must be excluded")
	}
	if _, ok := Project(message.Message{Role: message.RoleUser, Content: "mailbox", Kind: message.KindSubAgentMailbox}); ok {
		t.Fatal("mailbox must be excluded")
	}
}

func TestProjectUserPartsOnlyText(t *testing.T) {
	msg := message.Message{
		Role: message.RoleUser,
		Parts: []message.ContentPart{
			{Type: message.ContentPartText, Text: "hello "},
			{Type: message.ContentPartText, Text: `<file path="a.go">
world
</file>`},
			{Type: message.ContentPartImage, FileName: "pic.png"},
			{Type: message.ContentPartText, Text: "world"},
		},
	}
	got, ok := Project(msg)
	if !ok || got.Kind != KindUser {
		t.Fatalf("projected = %+v ok=%v", got, ok)
	}
	if got.Text != "hello world" {
		t.Fatalf("text = %q, want %q (file-injection text must be dropped)", got.Text, "hello world")
	}
	// Parts with only file-refs/binary must be skipped, never fall back to Content.
	onlyRefs := message.Message{
		Role:    message.RoleUser,
		Content: "should not leak",
		Parts: []message.ContentPart{
			{Type: message.ContentPartText, Text: `<file path="a.go">
secret body
</file>`},
			{Type: message.ContentPartImage, FileName: "x.png"},
		},
	}
	if _, ok := Project(onlyRefs); ok {
		t.Fatal("file-ref-only message must be skipped, no Content fallback")
	}
}

func TestProjectCompactionSummary(t *testing.T) {
	msg := message.Message{Role: message.RoleUser, Content: "[Context Summary]\nprior history", IsCompactionSummary: true}
	got, ok := SummaryProject(msg)
	if !ok || got.Kind != KindSummary {
		t.Fatalf("summary project = %+v ok=%v", got, ok)
	}
	// A compaction summary must not be projected as a plain user command.
	if _, ok := Project(msg); ok {
		t.Fatal("compaction summary must not project as plain user")
	}
}

func TestProjectAssistantStableOnly(t *testing.T) {
	cases := []struct {
		name string
		msg  message.Message
		want bool
	}{
		{"stable", textMsg(message.RoleAssistant, "done"), true},
		{"empty", textMsg(message.RoleAssistant, ""), false},
		{"interrupted", message.Message{Role: message.RoleAssistant, Content: "partial", StopReason: "interrupted"}, false},
		{"max_tokens", message.Message{Role: message.RoleAssistant, Content: "partial", StopReason: "max_tokens"}, false},
		{"tool_calls", message.Message{Role: message.RoleAssistant, Content: "", ToolCalls: []message.ToolCall{{ID: "c1", Name: "Read"}}}, false},
		// Provider-native payload fields accompany stable visible content and
		// must not cause a completed reply to be dropped.
		{"thinking", message.Message{Role: message.RoleAssistant, Content: "text", ThinkingBlocks: []message.ThinkingBlock{{Thinking: "hidden"}}}, true},
		{"reasoning", message.Message{Role: message.RoleAssistant, Content: "text", ReasoningContent: "hidden reasoning"}, true},
		{"responses_output", message.Message{Role: message.RoleAssistant, Content: "text", ResponsesOutput: []message.ResponsesOutputItem{{Type: "message"}}}, true},
		{"gemini_parts", message.Message{Role: message.RoleAssistant, Content: "text", GeminiParts: []message.GeminiReplayPart{{}}}, true},
		// A tool-call frame stays excluded even when it carries thinking.
		{"thinking_with_tool_calls", message.Message{Role: message.RoleAssistant, Content: "text", ThinkingBlocks: []message.ThinkingBlock{{Thinking: "hidden"}}, ToolCalls: []message.ToolCall{{ID: "c1", Name: "Read"}}}, false},
		{"synthetic kind", message.Message{Role: message.RoleAssistant, Content: "x", Kind: "loop_notice"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := Project(tc.msg)
			if ok != tc.want {
				t.Fatalf("Project(%s) ok=%v want %v", tc.name, ok, tc.want)
			}
			if tc.want && got.Text != strings.TrimSpace(tc.msg.Content) {
				t.Fatalf("Project(%s) text=%q, want the visible content %q", tc.name, got.Text, tc.msg.Content)
			}
		})
	}
}

func TestProjectExcludesToolsAndSystem(t *testing.T) {
	if _, ok := Project(textMsg(message.RoleTool, "ok")); ok {
		t.Fatal("tool message must be excluded")
	}
	if _, ok := Project(textMsg(message.RoleSystem, "sys")); ok {
		t.Fatal("system message must be excluded")
	}
}

func TestRetainKeepsNewestAndBounded(t *testing.T) {
	items := []Projected{
		{Kind: KindUser, Text: "msg-0"},
		{Kind: KindAssistant, Text: "msg-1"},
		{Kind: KindSummary, Text: "summary-old"},
		{Kind: KindUser, Text: "msg-2"},
		{Kind: KindAssistant, Text: "msg-3"},
	}
	kept, pruned := Retain(items, 6, 0)
	// Budget 6 tokens: newest user (msg-2) + newest summary always kept, plus
	// oldest-first fill.
	texts := make([]string, 0, len(kept))
	for _, k := range kept {
		texts = append(texts, k.Text)
	}
	joined := strings.Join(texts, "|")
	if !strings.Contains(joined, "msg-2") {
		t.Fatalf("newest user must be kept: %v", texts)
	}
	if !strings.Contains(joined, "summary-old") {
		t.Fatalf("newest summary must be kept: %v", texts)
	}
	// Order must be preserved (oldest → newest).
	for i := 1; i < len(texts); i++ {
		if indexOf(texts, texts[i]) <= indexOf(texts, texts[i-1]) && texts[i] != texts[i-1] {
			t.Fatalf("order not preserved: %v", texts)
		}
	}
	if pruned < 0 {
		t.Fatal("pruned must be non-negative")
	}
}

func indexOf(xs []string, s string) int {
	for i, x := range xs {
		if x == s {
			return i
		}
	}
	return -1
}

func TestRetainPrunesAndMarksOversized(t *testing.T) {
	big := strings.Repeat("x", 4000)
	items := []Projected{{Kind: KindUser, Text: "a"}, {Kind: KindUser, Text: big}}
	kept, pruned := Retain(items, 10, 100)
	// The oversized newest item is kept (UTF-8-safely truncated and marked
	// omitted); the older item is pruned once the budget is exhausted.
	if pruned != 1 {
		t.Fatalf("pruned = %d, want 1", pruned)
	}
	if len(kept) != 1 || !kept[0].Omitted {
		t.Fatalf("kept = %+v, want the truncated oversized item", kept)
	}
	if len(kept[0].Text) > 100 {
		t.Fatalf("oversized item not bounded: %d bytes", len(kept[0].Text))
	}
}

func TestRetainEmptyAndNoBudget(t *testing.T) {
	if kept, pruned := Retain(nil, 10, 0); len(kept) != 0 || pruned != 0 {
		t.Fatalf("empty input: %v %d", kept, pruned)
	}
	if kept, pruned := Retain([]Projected{{Kind: KindUser, Text: "x"}}, 0, 0); len(kept) != 0 || pruned != 1 {
		t.Fatalf("zero budget: %v %d", kept, pruned)
	}
}

func TestFingerprintStableAndSensitive(t *testing.T) {
	items := []Projected{{Kind: KindUser, Text: "a"}, {Kind: KindAssistant, Text: "b"}}
	f1 := Fingerprint(items)
	f2 := Fingerprint(items)
	if f1 != f2 {
		t.Fatal("fingerprint must be deterministic")
	}
	if Fingerprint(append(items, Projected{Kind: KindUser, Text: "c"})) == f1 {
		t.Fatal("fingerprint must change on append")
	}
	if Fingerprint([]Projected{{Kind: KindUser, Text: "a b"}}) == Fingerprint([]Projected{{Kind: KindUser, Text: "a"}}) {
		t.Fatal("fingerprint must be content-sensitive")
	}
}

func TestProjectBinaryPartNeverCopied(t *testing.T) {
	msg := message.Message{
		Role: message.RoleUser,
		Parts: []message.ContentPart{
			{Type: message.ContentPartPDF, FileName: "doc.pdf", Data: []byte{0x25, 0x50, 0x44, 0x46}},
		},
	}
	if _, ok := Project(msg); ok {
		t.Fatal("binary-only message must be skipped")
	}
}
