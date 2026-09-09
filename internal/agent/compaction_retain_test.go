package agent

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

// estimatorTokensPerChar returns a deterministic estimator that charges one
// token per byte/character, so budgets and truncation points are easy to
// reason about in tests.
func estimatorTokensPerChar(text string) int { return len(text) }

func TestSelectCheckpointRetainedRecentBlocksChronologicalOrderAndLabels(t *testing.T) {
	head := []message.Message{
		{Role: message.RoleUser, Content: "Refactor the parser into two passes."},
		{Role: message.RoleAssistant, Content: "Done."},
		{Role: message.RoleUser, Content: "Check the tokenizer unit tests."},
		{Role: message.RoleAssistant, Content: "On it."},
		{Role: message.RoleUser, Content: "Re-run the full suite."},
		{Role: message.RoleAssistant, Content: "Interrupted while re-running the suite; only the parser package compiled so far.", StopReason: "interrupted"},
	}
	blocks := selectCheckpointRetainedRecentBlocks(head, compactRetainRecentUserMessages, 1<<20, estimatorTokensPerChar)
	if len(blocks) != 4 {
		t.Fatalf("len(blocks) = %d, want 4 (three user messages + the interrupted reply), got %+v", len(blocks), blocks)
	}
	// Newest-first collection: interrupted reply, then the three user messages
	// in reverse order.
	if blocks[0].label != retainedInterruptedAssistantLabel {
		t.Fatalf("newest block label = %q, want %q", blocks[0].label, retainedInterruptedAssistantLabel)
	}
	wantTexts := []string{
		"Re-run the full suite.",
		"Check the tokenizer unit tests.",
		"Refactor the parser into two passes.",
	}
	for i, want := range wantTexts {
		if blocks[i+1].label != retainedUserLabel {
			t.Fatalf("block %d label = %q, want %q", i+1, blocks[i+1].label, retainedUserLabel)
		}
		if blocks[i+1].text != want {
			t.Fatalf("block %d text = %q, want %q", i+1, blocks[i+1].text, want)
		}
	}
	section := renderCheckpointRetainedRecentMessages(head, compactRetainRecentUserMessages, 1<<20, estimatorTokensPerChar)
	if !strings.HasPrefix(section, retainedRecentMessagesHeading) {
		t.Fatalf("section does not start with %q:\n%s", retainedRecentMessagesHeading, section)
	}
	// Rendering is chronological (oldest first), so the interrupted reply ends
	// the section.
	first := strings.Index(section, "Refactor the parser into two passes.")
	second := strings.Index(section, "Check the tokenizer unit tests.")
	third := strings.Index(section, "Re-run the full suite.")
	partial := strings.Index(section, "Interrupted while re-running the suite")
	if !(first < second && second < third && third < partial) {
		t.Fatalf("section order is not chronological:\n%s", section)
	}
	if !strings.Contains(section, retainedInterruptedAssistantLabel) {
		t.Fatalf("section missing interrupted-assistant label:\n%s", section)
	}
	if !strings.Contains(section, "> Refactor the parser into two passes.") {
		t.Fatalf("retained message not blockquoted:\n%s", section)
	}
}

func TestSelectCheckpointRetainedRecentBlocksExcludesSyntheticUserMessages(t *testing.T) {
	head := []message.Message{
		{Role: message.RoleUser, Content: "Original real request."},
		{Role: message.RoleAssistant, Content: "Worked on it."},
		{Role: message.RoleUser, Content: "loop notice", Kind: message.KindLoopNotice},
		{Role: message.RoleUser, Content: "[Context Summary]\nPrior checkpoint.", IsCompactionSummary: true},
		{Role: message.RoleUser, Content: "stream continue", Kind: message.KindStreamContinue},
		{Role: message.RoleUser, Content: "mailbox ping", Kind: message.KindSubAgentMailbox},
		{Role: message.RoleUser, Content: "Background job finished.", Kind: message.KindBackgroundResult},
		{Role: message.RoleUser, Content: "hook feedback", Kind: message.KindHookFeedback},
		{Role: message.RoleUser, Content: "Final real instruction."},
	}
	blocks := selectCheckpointRetainedRecentBlocks(head, compactRetainRecentUserMessages, 1<<20, estimatorTokensPerChar)
	if len(blocks) != 2 {
		t.Fatalf("len(blocks) = %d, want 2 real messages only: %+v", len(blocks), blocks)
	}
	if blocks[0].text != "Final real instruction." || blocks[1].text != "Original real request." {
		t.Fatalf("blocks = %+v, want [Final real instruction., Original real request.]", blocks)
	}
}

func TestSelectCheckpointRetainedRecentBlocksTruncatesOldestBlockToFitBudget(t *testing.T) {
	longOld := strings.Repeat("b", 40)
	head := []message.Message{
		{Role: message.RoleUser, Content: longOld},
		{Role: message.RoleAssistant, Content: "ok"},
		{Role: message.RoleUser, Content: "newest instruction"},
	}
	// Budget 20 tokens: the newest message (18 chars) fits whole; the older
	// one must be cut to the remaining 2 tokens and marked truncated. Nothing
	// older than the truncated block is considered.
	blocks := selectCheckpointRetainedRecentBlocks(head, compactRetainRecentUserMessages, 20, estimatorTokensPerChar)
	if len(blocks) != 2 {
		t.Fatalf("len(blocks) = %d, want 2: %+v", len(blocks), blocks)
	}
	if blocks[0].text != "newest instruction" || blocks[0].truncated {
		t.Fatalf("newest block = %+v, want whole untruncated newest message", blocks[0])
	}
	if blocks[1].text != "bb" || !blocks[1].truncated {
		t.Fatalf("oldest block = %+v, want text %q truncated to remaining budget", blocks[1], "bb")
	}
	section := renderCheckpointRetainedRecentMessages(head, compactRetainRecentUserMessages, 20, estimatorTokensPerChar)
	if !strings.Contains(section, "> bb\n> [truncated]") {
		t.Fatalf("section missing truncated marker after cut text:\n%s", section)
	}
	// The truncated (oldest) message renders before the whole newest one.
	if strings.Index(section, "> bb") > strings.Index(section, "> newest instruction") {
		t.Fatalf("truncated oldest message must render before the newest one:\n%s", section)
	}
	if strings.Contains(section, strings.Repeat("b", 40)) {
		t.Fatalf("section must not contain the full oversized message:\n%s", section)
	}
}

func TestSelectCheckpointRetainedRecentBlocksSkipsNonNewestInterruptedReplies(t *testing.T) {
	t.Run("superseded by a newer real user message", func(t *testing.T) {
		head := []message.Message{
			{Role: message.RoleUser, Content: "Do task X."},
			{Role: message.RoleAssistant, Content: "Stale partial about task X.", StopReason: "interrupted"},
			{Role: message.RoleUser, Content: "Never mind, do task Y instead."},
			{Role: message.RoleAssistant, Content: "Done with task Y."},
		}
		blocks := selectCheckpointRetainedRecentBlocks(head, compactRetainRecentUserMessages, 1<<20, estimatorTokensPerChar)
		for _, b := range blocks {
			if b.label == retainedInterruptedAssistantLabel {
				t.Fatalf("superseded interrupted reply must not be retained: %+v", blocks)
			}
		}
		if len(blocks) != 2 || blocks[0].text != "Never mind, do task Y instead." {
			t.Fatalf("blocks = %+v, want the two real user messages only", blocks)
		}
	})
	t.Run("already continued after stream-continue prompt", func(t *testing.T) {
		head := []message.Message{
			{Role: message.RoleUser, Content: "Do task X."},
			{Role: message.RoleAssistant, Content: "Partial reply to task X.", StopReason: "interrupted"},
			{Role: message.RoleUser, Content: "continue", Kind: message.KindStreamContinue},
			{Role: message.RoleAssistant, Content: "Continued and finished task X."},
		}
		blocks := selectCheckpointRetainedRecentBlocks(head, compactRetainRecentUserMessages, 1<<20, estimatorTokensPerChar)
		for _, b := range blocks {
			if b.label == retainedInterruptedAssistantLabel {
				t.Fatalf("already-continued interrupted reply must not be retained: %+v", blocks)
			}
		}
	})
	t.Run("interrupted mid tool loop with tool calls", func(t *testing.T) {
		head := []message.Message{
			{Role: message.RoleUser, Content: "Inspect the tree."},
			{Role: message.RoleAssistant, Content: "partial", StopReason: "interrupted", ToolCalls: []message.ToolCall{{ID: "g-1", Name: "grep"}}},
		}
		blocks := selectCheckpointRetainedRecentBlocks(head, compactRetainRecentUserMessages, 1<<20, estimatorTokensPerChar)
		if len(blocks) != 1 || blocks[0].label != retainedUserLabel {
			t.Fatalf("blocks = %+v, want only the real user message (tool-call partial has no resume value)", blocks)
		}
	})
}

func TestRetainedRecentSectionRendersEmptyForEmptyHead(t *testing.T) {
	if got := renderCheckpointRetainedRecentMessages(nil, compactRetainRecentUserMessages, 1<<20, estimatorTokensPerChar); got != "" {
		t.Fatalf("render of empty head = %q, want empty", got)
	}
	if got := renderCheckpointRetainedRecentMessages([]message.Message{{Role: message.RoleUser, Content: "x"}}, compactRetainRecentUserMessages, 0, estimatorTokensPerChar); got != "" {
		t.Fatalf("render with zero budget = %q, want empty", got)
	}
}

func TestRetainedRecentSectionPlacedBetweenHistoryMapAndEvidence(t *testing.T) {
	retained := renderCheckpointRetainedRecentMessages([]message.Message{
		{Role: message.RoleUser, Content: "Keep the public API stable."},
	}, compactRetainRecentUserMessages, 1<<20, estimatorTokensPerChar)
	if retained == "" {
		t.Fatal("expected a non-empty retained section")
	}
	content := buildCompactionCheckpointMessage(
		"## Goal\n- continue",
		[]string{"history-1.md: topic"},
		"model_summary",
		[]evidenceItem{{Title: "Latest tool result", Excerpt: "exact error text"}},
		retained,
	)
	historyAt := strings.Index(content, "Archived history files")
	headingAt := strings.Index(content, retainedRecentMessagesHeading)
	evidenceAt := strings.Index(content, message.CompactionEvidenceTag)
	if historyAt < 0 || headingAt < 0 || evidenceAt < 0 {
		t.Fatalf("checkpoint missing expected sections:\n%s", content)
	}
	if !(historyAt < headingAt && headingAt < evidenceAt) {
		t.Fatalf("retained section must sit after the archived-history map and before the evidence artifact:\n%s", content)
	}
	// The evidence artifact is the checkpoint's final region: no display-hint
	// tail follows it (the card is always fully expanded).
	if strings.Contains(content, "[Context display hint]") {
		t.Fatalf("checkpoint still carries the display-hint tail:\n%s", content)
	}
	if !strings.HasSuffix(strings.TrimSpace(content), "exact error text") {
		t.Fatalf("evidence artifact must be the trailing section:\n%s", content)
	}
	if !strings.Contains(content, "> Keep the public API stable.") {
		t.Fatalf("checkpoint missing retained message text:\n%s", content)
	}
}

func TestTruncateRetainedTextToBudgetIsRuneSafe(t *testing.T) {
	text := "a" + strings.Repeat("\U0001F30D", 10) // a + ten 4-byte globe runes
	for budget := 1; budget < len(text); budget++ {
		got := truncateRetainedTextToBudget(text, budget, estimatorTokensPerChar)
		if !utf8.ValidString(got) {
			t.Fatalf("budget %d produced invalid UTF-8: %q", budget, got)
		}
		if got == text {
			t.Fatalf("budget %d should not fit the whole %d-byte text", budget, len(text))
		}
	}
	// A cut that would land inside a multi-byte rune backs off to the rune
	// start: budget 6 on "a<globe><globe>" (bytes 1+4+4) fits "a" plus one
	// whole globe, never half of it.
	got := truncateRetainedTextToBudget("a\U0001F30D\U0001F30D", 6, estimatorTokensPerChar)
	if got != "a\U0001F30D" {
		t.Fatalf("truncated = %q, want %q", got, "a\U0001F30D")
	}
	// A budget that fits at most the first rune keeps exactly that rune.
	if got := truncateRetainedTextToBudget("ab", 1, estimatorTokensPerChar); got != "a" {
		t.Fatalf("truncated = %q, want %q", got, "a")
	}
}

func TestSelectCheckpointRetainedRecentBlocksKeepsCompactContextDeclaringAssistant(t *testing.T) {
	cc := message.ToolCall{ID: "cc-1", Name: tools.NameCompactContext}
	head := []message.Message{
		{Role: message.RoleUser, Content: "Refactor the parser."},
		{Role: message.RoleAssistant, Content: "Explored the code and found the shape of the fix."},
		{Role: message.RoleUser, Content: "Land it."},
		{Role: message.RoleAssistant, Content: "Phase wrapped up; checkpointing now.", ToolCalls: []message.ToolCall{cc}},
	}
	blocks := selectCheckpointRetainedRecentBlocks(head, compactRetainRecentUserMessages, 1<<20, estimatorTokensPerChar)
	// Newest first: the cc-declaring assistant body, then the two real user
	// messages; the earlier analysis assistant is not retained.
	if len(blocks) != 3 {
		t.Fatalf("retained %d blocks, want 3:\n%+v", len(blocks), blocks)
	}
	if blocks[0].label != retainedCheckpointRequestLabel || !strings.Contains(blocks[0].text, "Phase wrapped up") {
		t.Fatalf("newest block must be the cc-declaring assistant, got %+v", blocks[0])
	}
	section := renderCheckpointRetainedRecentMessages(head, compactRetainRecentUserMessages, 1<<20, estimatorTokensPerChar)
	if !strings.Contains(section, retainedCheckpointRequestLabel) {
		t.Fatalf("section missing the checkpoint-request label:\n%s", section)
	}
	if !strings.Contains(section, "Phase wrapped up; checkpointing now.") {
		t.Fatalf("section missing the cc-declaring assistant body:\n%s", section)
	}
	// Chronological order inside the section: the cc body renders after the
	// user messages it answers.
	if strings.Index(section, "> Refactor the parser.") > strings.Index(section, "> Phase wrapped up; checkpointing now.") {
		t.Fatalf("cc-declaring body must render chronologically after the older user message:\n%s", section)
	}
}

func TestSelectCheckpointRetainedRecentBlocksSkipsEmptyOrSupersededCheckpointRequests(t *testing.T) {
	cc := message.ToolCall{ID: "cc-1", Name: tools.NameCompactContext}
	// A cc-declaring assistant with an empty body has no own text to retain.
	empty := []message.Message{
		{Role: message.RoleUser, Content: "original"},
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{cc}},
	}
	if blocks := selectCheckpointRetainedRecentBlocks(empty, compactRetainRecentUserMessages, 1<<20, estimatorTokensPerChar); len(blocks) != 1 {
		t.Fatalf("empty cc body must not be retained, got %+v", blocks)
	}
	// A real user message newer than the cc request supersedes it (for
	// example a skip that let the model continue and the user then corrected
	// course): retention must not pull the model back to the abandoned
	// checkpoint request.
	superseded := []message.Message{
		{Role: message.RoleUser, Content: "Phase A."},
		{Role: message.RoleAssistant, Content: "Checkpointing phase A.", ToolCalls: []message.ToolCall{cc}},
		{Role: message.RoleUser, Content: "Do not checkpoint; keep the context and continue."},
	}
	blocks := selectCheckpointRetainedRecentBlocks(superseded, compactRetainRecentUserMessages, 1<<20, estimatorTokensPerChar)
	// The newer user message supersedes the cc request: no checkpoint-request
	// block is retained, and the retained blocks are exactly the real user
	// messages, newest first.
	if len(blocks) != 2 {
		t.Fatalf("expected the two user messages only, got %+v", blocks)
	}
	for _, b := range blocks {
		if b.label != retainedUserLabel {
			t.Fatalf("superseded cc request must not be retained, got %+v", blocks)
		}
	}
	if blocks[0].text != "Do not checkpoint; keep the context and continue." {
		t.Fatalf("newest retained block must be the newer user message, got %+v", blocks)
	}
}

func TestSelectCheckpointRetainedRecentBlocksBudgetPrefersNewerOverCheckpointRequest(t *testing.T) {
	cc := message.ToolCall{ID: "cc-1", Name: tools.NameCompactContext}
	head := []message.Message{
		{Role: message.RoleUser, Content: "aaaaaaaaaa"},                                                    // 10
		{Role: message.RoleUser, Content: "bbbbbbbbbb"},                                                    // 10
		{Role: message.RoleAssistant, Content: strings.Repeat("c", 10), ToolCalls: []message.ToolCall{cc}}, // 10
	}
	blocks := selectCheckpointRetainedRecentBlocks(head, compactRetainRecentUserMessages, 25, estimatorTokensPerChar)
	// Newest first: cc body (10) and the newest user (10) are kept whole; the
	// oldest user cannot fit the remaining 5 tokens, so it is kept as a
	// truncated prefix and retention stops there.
	if len(blocks) != 3 || blocks[0].label != retainedCheckpointRequestLabel || blocks[1].label != retainedUserLabel {
		t.Fatalf("budget must keep the cc body and newest user whole, got %+v", blocks)
	}
	if blocks[0].text != strings.Repeat("c", 10) || blocks[1].text != "bbbbbbbbbb" {
		t.Fatalf("unexpected kept blocks: %+v", blocks)
	}
	if !blocks[2].truncated || blocks[2].text != "aaaaa" {
		t.Fatalf("oldest user must be truncated to the remaining budget, got %+v", blocks[2])
	}
}

func TestEffectiveCompactionRetainRecentTokensFallbackAndOverride(t *testing.T) {
	a := &MainAgent{}
	if got := a.effectiveCompactionRetainRecentTokens(); got != compactRetainRecentDefaultTokens {
		t.Fatalf("default retention budget = %d, want built-in default %d", got, compactRetainRecentDefaultTokens)
	}
	a.projectConfig = &config.Config{Context: config.ContextConfig{Compaction: config.CompactionConfig{RetainRecentTokens: 8192}}}
	if got := a.effectiveCompactionRetainRecentTokens(); got != 8192 {
		t.Fatalf("project override retention budget = %d, want 8192", got)
	}
	a.projectConfig = nil
	a.globalConfig = &config.Config{Context: config.ContextConfig{Compaction: config.CompactionConfig{RetainRecentTokens: 2048}}}
	if got := a.effectiveCompactionRetainRecentTokens(); got != 2048 {
		t.Fatalf("global override retention budget = %d, want 2048", got)
	}
}
