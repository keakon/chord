package agent

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/keakon/chord/internal/message"
)

func checkpointMessageForCarryTest(summary string, historyRefs []string) message.Message {
	return message.Message{
		Role:                message.RoleUser,
		Content:             buildCompactionCheckpointMessage(summary, historyRefs, "model_summary", nil),
		IsCompactionSummary: true,
	}
}

func TestLatestPriorCheckpointBodyStripsWrapperAnchorsAndFooter(t *testing.T) {
	anchors := compactionAnchors{
		OriginalRequest: "build the loader",
		Constraints:     []string{"do not touch the public API"},
	}
	summary := "## Current User Request\n- finish the migration\n\n## Active Objective\n- land it\n\n## Key Decisions\n- keep archival profile\n\n## Next Step\n- run the full suite"
	msg := checkpointMessageForCarryTest(withCompactionAnchors(summary, anchors), []string{"history-1.md"})

	got := latestPriorCheckpointBody([]message.Message{msg})
	if got != summary {
		t.Fatalf("carried body = %q, want %q", got, summary)
	}
	for _, forbidden := range []string{"[Context Summary]", "[Context compressed]", "Archived history files", "[Session Anchors]", "do not touch the public API", "[Context display hint]"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("carried body must not contain %q:\n%s", forbidden, got)
		}
	}
}

func TestLatestPriorCheckpointBodyDoesNotCompound(t *testing.T) {
	// A checkpoint that already carries a `## Previous Checkpoint` section
	// (produced by an earlier run of this mechanism) must not carry that
	// section again on the next extraction: consecutive compactions would
	// otherwise compound the whole history of carried blocks.
	inner := appendPriorCheckpointCarry("## Current User Request\n- newer task\n\n## Key Decisions\n- d1\n\n## Next Step\n- s1",
		"## Current User Request\n- older task\n\n## Next Step\n- older-step")
	msg := checkpointMessageForCarryTest(inner, nil)

	got := latestPriorCheckpointBody([]message.Message{msg})
	if strings.Contains(got, priorCheckpointSectionHeading) || strings.Contains(got, "older-step") {
		t.Fatalf("carry must not compound across generations, got:\n%s", got)
	}
	if !strings.Contains(got, "## Key Decisions") {
		t.Fatalf("carried body must keep the checkpoint's own sections:\n%s", got)
	}
}

func TestLatestPriorCheckpointBodyReturnsNewestAndSkipsPlainMessages(t *testing.T) {
	old := checkpointMessageForCarryTest("## Next Step\n- old", nil)
	new := checkpointMessageForCarryTest("## Next Step\n- new", nil)
	msgs := []message.Message{
		{Role: message.RoleUser, Content: "plain user message"},
		old,
		{Role: message.RoleAssistant, Content: "assistant"},
		new,
	}
	got := latestPriorCheckpointBody(msgs)
	if !strings.Contains(got, "- new") || strings.Contains(got, "- old") {
		t.Fatalf("carried body must come from the newest checkpoint, got:\n%s", got)
	}
	if latestPriorCheckpointBody([]message.Message{{Role: message.RoleUser, Content: "no checkpoint"}}) != "" {
		t.Fatal("no checkpoint must yield no carried body")
	}
	if latestPriorCheckpointBody(nil) != "" {
		t.Fatal("empty messages must yield no carried body")
	}
}

func TestLatestPriorCheckpointBodyTruncatesAtLineBoundary(t *testing.T) {
	lines := "## Decisions\n- keep the parser contract\n- preserve tool ordering\n## Next Step\n- run the focused tests\n"
	msg := checkpointMessageForCarryTest(lines+strings.Repeat("x", compactCheckpointCarryMaxChars), nil)

	got := latestPriorCheckpointBody([]message.Message{msg})
	if !strings.Contains(got, "- preserve tool ordering") {
		t.Fatalf("carry dropped a complete preceding item: %q", got)
	}
	if !strings.Contains(got, "Earlier checkpoint content omitted") {
		t.Fatalf("truncated carry must disclose omitted content: %q", got)
	}
	if strings.Contains(got, strings.Repeat("x", 32)) {
		t.Fatalf("carry must not include a partial oversized line: %q", got)
	}
}

func TestCheckpointCarryBudgetAndFormatting(t *testing.T) {
	body := "## Decision\n- preserve ordering\n  continuation of the decision\n\n" + strings.Repeat("oversized ", 300)
	for _, budget := range []int{-1, 0, 1, 77, 78, 100, 160, 2400} {
		got := truncateCheckpointCarryLines(body, budget)
		if len([]rune(got)) > max(budget, 0) {
			t.Fatalf("budget %d exceeded: %d", budget, len([]rune(got)))
		}
	}
	got := truncateCheckpointCarryLines(body, 200)
	if !strings.Contains(got, "\n  continuation of the decision") {
		t.Fatalf("indentation was changed: %q", got)
	}
	if got := truncateCheckpointCarryLines("## Decision\n- complete", 200); got != "## Decision\n- complete" {
		t.Fatalf("untruncated content changed: %q", got)
	}
}

func TestCheckpointCarryTruncationBoundaries(t *testing.T) {
	const omitted = "[Earlier checkpoint content omitted; read the archive for the complete record.]"
	omittedChars := utf8.RuneCountInString(omitted)
	oversized := strings.Repeat("x", omittedChars+20)
	for _, test := range []struct {
		name   string
		body   string
		budget int
		want   string
	}{
		{name: "empty", body: " \n ", budget: 200},
		{name: "negative budget", body: oversized, budget: -1},
		{name: "zero budget", body: oversized, budget: 0},
		{name: "short content", body: "keep", budget: 4, want: "keep"},
		{name: "below omission budget", body: oversized, budget: omittedChars - 1},
		{name: "exact omission budget", body: oversized, budget: omittedChars, want: omitted},
		{name: "oversized first line", body: oversized + "\nkeep", budget: omittedChars + 10, want: omitted},
		{name: "exact line budget", body: "keep\n" + oversized, budget: omittedChars + 5, want: "keep\n" + omitted},
		{name: "line exceeds budget", body: "keep\n" + oversized, budget: omittedChars + 4, want: omitted},
		{name: "unicode exact content", body: "保持顺序", budget: 4, want: "保持顺序"},
		{name: "unicode exact line", body: "保持顺序\n" + oversized, budget: omittedChars + 5, want: "保持顺序\n" + omitted},
		{name: "unicode line exceeds budget", body: "保持顺序\n" + oversized, budget: omittedChars + 4, want: omitted},
		{name: "CRLF intact", body: "keep\r\norder", budget: 200, want: "keep\r\norder"},
		{name: "CRLF truncated", body: "keep\r\norder\r\n" + oversized, budget: omittedChars + 13, want: "keep\r\norder\n" + omitted},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := truncateCheckpointCarryLines(test.body, test.budget)
			if got != test.want {
				t.Fatalf("truncateCheckpointCarryLines() = %q, want %q", got, test.want)
			}
			if !utf8.ValidString(got) || utf8.RuneCountInString(got) > max(test.budget, 0) {
				t.Fatalf("invalid output or exceeded budget: %q", got)
			}
		})
	}
}

func TestAppendPriorCheckpointCarryAppendsOnceAndIsIdempotent(t *testing.T) {
	base := "## Current User Request\n- task\n\n## Next Step\n- run tests"
	carry := "## Active Objective\n- ship it"
	once := appendPriorCheckpointCarry(base, carry)
	if strings.Count(once, priorCheckpointSectionHeading) != 1 {
		t.Fatalf("carry appended %d times, want once:\n%s", strings.Count(once, priorCheckpointSectionHeading), once)
	}
	if !strings.HasSuffix(once, priorCheckpointSectionHeading+"\n"+carry) {
		t.Fatalf("carry must be the final section:\n%s", once)
	}
	twice := appendPriorCheckpointCarry(once, carry)
	if twice != once {
		t.Fatal("appending a carry to a body that already carries one must be a no-op")
	}
	if got := appendPriorCheckpointCarry(base, ""); got != base {
		t.Fatal("empty carry must leave the summary unchanged")
	}
	if got := appendPriorCheckpointCarry("", carry); got != "" {
		t.Fatal("empty summary must stay empty")
	}
}

func TestPriorCheckpointSurfacedInSummarizePrompt(t *testing.T) {
	input := &compactionInput{PriorCheckpoint: "## Key Decisions\n- keep archival profile"}
	prompt := buildCompactionPromptWithKeyFiles(input, "history-1.md", nil, nil, nil, nil)
	if !strings.Contains(prompt, "Prior durable checkpoint") || !strings.Contains(prompt, "Do not silently drop or contradict") || !strings.Contains(prompt, "## Key Decisions\n- keep archival profile") {
		t.Fatalf("prompt must surface the prior checkpoint with a fold instruction, got:\n%s", prompt)
	}

	empty := &compactionInput{}
	prompt = buildCompactionPromptWithKeyFiles(empty, "history-1.md", nil, nil, nil, nil)
	if strings.Contains(prompt, "Prior durable checkpoint") {
		t.Fatalf("prompt must not mention a prior checkpoint when none exists:\n%s", prompt)
	}
}

// TestCompactionDraftCarriesPriorCheckpointBody covers the wiring end to end:
// a second compaction whose archived head begins with an earlier checkpoint
// appends that checkpoint's durable body verbatim as the final section, so the
// structured content the first checkpoint established is referenced
// deterministically instead of being left to the summarizer's discretion.
func TestCompactionDraftCarriesPriorCheckpointBody(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)

	snapshot := []message.Message{
		{Role: message.RoleUser, Content: "port the config loader to the new schema"},
		{Role: message.RoleAssistant, Content: "a1"},
		{Role: message.RoleUser, Content: "keep the public API stable"},
		{Role: message.RoleAssistant, Content: "a2"},
	}
	a.resetRuntimeEvidenceFromMessages(snapshot)
	evidenceItems := a.evidenceItemsForCompaction(a.ctxMgr.GetMaxTokens())

	first, err := a.produceCompactionDraftAsync(t.Context(), snapshot, false, 1,
		compactionTarget{sessionEpoch: a.sessionEpoch}, len(snapshot), compactionProfileArchival,
		"port the config loader to the new schema", evidenceItems, a.captureCompactionArchiveMeta())
	if err != nil {
		t.Fatalf("first produceCompactionDraftAsync: %v", err)
	}
	if first.Skip || len(first.NewMessages) == 0 {
		t.Fatalf("unexpected first draft: %+v", first)
	}
	if strings.Contains(first.NewMessages[0].Content, priorCheckpointSectionHeading) {
		t.Fatal("first compaction has no prior checkpoint, so no carry section may appear")
	}

	// Second round: the history starts with the first checkpoint.
	next := append([]message.Message{first.NewMessages[0]}, []message.Message{
		{Role: message.RoleAssistant, Content: "continuing"},
		{Role: message.RoleUser, Content: "now add the migration command"},
		{Role: message.RoleAssistant, Content: "done"},
	}...)
	a.resetRuntimeEvidenceFromMessages(next)
	secondEvidence := a.evidenceItemsForCompaction(a.ctxMgr.GetMaxTokens())
	second, err := a.produceCompactionDraftAsync(t.Context(), next, false, 2,
		compactionTarget{sessionEpoch: a.sessionEpoch}, len(next), compactionProfileArchival,
		first.NewMessages[0].Content, secondEvidence, a.captureCompactionArchiveMeta())
	if err != nil {
		t.Fatalf("second produceCompactionDraftAsync: %v", err)
	}
	if second.Skip || len(second.NewMessages) == 0 {
		t.Fatalf("unexpected second draft: %+v", second)
	}

	secondContent := second.NewMessages[0].Content
	if strings.Count(secondContent, priorCheckpointSectionHeading) != 1 {
		t.Fatalf("second checkpoint must carry exactly one previous-checkpoint section:\n%s", secondContent)
	}
	expectedCarry := latestPriorCheckpointBody(first.NewMessages)
	_, after, ok := strings.Cut(secondContent, "\n"+priorCheckpointSectionHeading+"\n")
	if !ok {
		t.Fatalf("second checkpoint carries no previous-checkpoint section:\n%s", secondContent)
	}
	carried := strings.TrimSpace(after)
	carried = strings.TrimSpace(strings.Split(carried, "[Context compressed]")[0])
	if carried != expectedCarry {
		t.Fatalf("carried body = %q, want %q", carried, expectedCarry)
	}
}
