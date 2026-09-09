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

// TestLatestPriorCheckpointBodyRetainsTypedStateBeyondRuneCap pins the
// typed-carry protection across the display truncation: the typed JSON line of
// a model-driven checkpoint sits late in the body, past the first 2400 runes
// once claims fill their cap, so a plain from-the-top truncation drops it. The
// display carry must keep the typed section after the truncated
// natural-language lines so the next generation can still parse the prior
// decisions/claims out of the carry.
func TestLatestPriorCheckpointBodyRetainsTypedStateBeyondRuneCap(t *testing.T) {
	msg := benchmarkPriorTypedCheckpointMessage()
	body := latestPriorCheckpointStrippedBody([]message.Message{msg})
	if body == "" {
		t.Fatal("prior checkpoint body must be found")
	}
	if runeCount(body) <= compactCheckpointCarryMaxChars {
		t.Fatal("fixture prior body must exceed the display carry cap")
	}
	got := latestPriorCheckpointBody([]message.Message{msg})
	state, found, malformed := typedStateFromBody(got)
	if !found || malformed {
		t.Fatalf("display carry must retain the typed state found=%v malformed=%v:\n%s", found, malformed, got)
	}
	hasCarried := false
	for _, item := range state.Decisions {
		if strings.HasPrefix(item, "carried-d-") {
			hasCarried = true
		}
	}
	if !hasCarried {
		t.Fatalf("retained typed block must carry the prior decisions: %v", state.Decisions)
	}
	// The natural-language part above the retained typed section still
	// respects the rune budget; only the machine block is exempt. In this
	// fixture the prelude is short enough to survive the truncation verbatim,
	// so no disclosure note is expected.
	idx := strings.Index(got, typedStateSectionHeading)
	if idx < 0 {
		t.Fatalf("typed section missing from the retained carry:\n%s", got)
	}
	if runeCount(got[:idx]) > compactCheckpointCarryMaxChars {
		t.Fatalf("natural-language carry exceeded the rune cap: %d", runeCount(got[:idx]))
	}
}

// TestTruncateCarryKeepingTypedStateNoOpCases pins the guard rails of the
// typed-carry truncation: a body that already fits, a body without a typed
// block, and a typed heading without a JSON line all take the ordinary
// truncation/identity paths.
func TestTruncateCarryKeepingTypedStateNoOpCases(t *testing.T) {
	// A body that fits is returned verbatim.
	short := "## Next Step\n- run tests\n\n## Typed Checkpoint State\n- {\"decisions\":[\"d1\"]}"
	if got := truncateCarryKeepingTypedState(short, 200); got != short {
		t.Fatalf("fitting body must be returned unchanged: %q", got)
	}
	// No typed block: plain line truncation with the disclosure note.
	plain := "## Next Step\n- run tests\n" + strings.Repeat("x", 300)
	got := truncateCarryKeepingTypedState(plain, 100)
	if strings.Contains(got, typedStateSectionHeading) {
		t.Fatalf("body without a typed block must not gain one: %q", got)
	}
	if !strings.Contains(got, "Earlier checkpoint content omitted") {
		t.Fatalf("plain truncation must disclose dropped content: %q", got)
	}
	// A typed heading that closes the body with no JSON line degrades to plain
	// truncation (no dangling typed heading).
	dangling := "## Next Step\n- run tests\n" + strings.Repeat("z", 300) + "\n## Typed Checkpoint State"
	got = truncateCarryKeepingTypedState(dangling, 100)
	if strings.Contains(got, typedStateSectionHeading) {
		t.Fatalf("a typed heading without its JSON line must not be carried: %q", got)
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
func TestPriorCheckpointCarryDoesNotConsumeProseQuotingTheTypedHeading(t *testing.T) {
	// Prose that merely quotes the typed-state heading — inside a decision, a
	// claim key, or a free line — is ordinary body text: parsing must not read
	// it as the machine block, and the carry must keep it verbatim instead of
	// deleting it.
	body := "## Current User Request\n- finish the migration\n\n## Key Decisions\n- keep the literal `## Typed Checkpoint State` marker inside this decision\n\n## Active Objective\n- land it\n\n## Next Step\n- run the focused tests"
	msg := checkpointMessageForCarryTest(body, nil)
	if got := latestPriorCheckpointBody([]message.Message{msg}); got != body {
		t.Fatalf("carry must preserve prose quoting the typed heading verbatim, got:\n%s", got)
	}
	if _, found, malformed := typedStateFromBody(body); found || malformed {
		t.Fatalf("prose-only mention must not parse as a typed block found=%v malformed=%v", found, malformed)
	}
}

func TestTypedStateSectionLocatedByStandaloneHeadingLine(t *testing.T) {
	typed := renderTypedStateJSON(checkpointTypedState{Decisions: []string{"d1: real decision"}})
	// Mid-line mentions of the heading text appear in prose and even inside a
	// claim key rendered into the JSON payload; only the standalone heading
	// line that opens the real section may start the machine block.
	body := "## Key Decisions\n- the plan quotes `## Typed Checkpoint State` mid-sentence\n\n" +
		"## Typed Checkpoint State\n" + typed
	state, found, malformed := typedStateFromBody(body)
	if !found || malformed {
		t.Fatalf("real typed section must parse past mid-line prose found=%v malformed=%v:\n%s", found, malformed, body)
	}
	if !containsString(state.Decisions, "d1: real decision") {
		t.Fatalf("decisions = %v, want the real block's decision", state.Decisions)
	}

	claimBody := "## Typed Checkpoint State\n" + renderTypedStateJSON(checkpointTypedState{Claims: map[string]checkpointClaim{
		"## Typed Checkpoint State": {Kind: "observed", EvidenceRefs: []string{"ev-1"}, Status: typedClaimStatusActive},
	}})
	state, found, malformed = typedStateFromBody(claimBody)
	if !found || malformed {
		t.Fatalf("claim key equal to the heading text must parse as data, not section marker found=%v malformed=%v", found, malformed)
	}
	if got := state.Claims["## Typed Checkpoint State"]; got.Kind != "observed" || len(got.EvidenceRefs) != 1 {
		t.Fatalf("heading-text claim key lost: %#v", got)
	}

	// A prose doppelgänger that renders the heading as its own line ahead of
	// the real section is only a candidate; the parser skips it and reads the
	// section whose payload is JSON.
	fake := "## Key Decisions\n- summary\n\n## Typed Checkpoint State\n- plain prose that is not a JSON payload\n\n## Active Objective\n- still going\n\n## Typed Checkpoint State\n" + typed
	state, found, malformed = typedStateFromBody(fake)
	if !found || malformed {
		t.Fatalf("real typed section must win over a prose doppelgänger found=%v malformed=%v:\n%s", found, malformed, fake)
	}
	if !containsString(state.Decisions, "d1: real decision") {
		t.Fatalf("doppelgänger shadowed the real block: %v", state.Decisions)
	}

	// A carry that must truncate keeps the real JSON payload, never the prose
	// doppelgänger line.
	oversized := "## Active Objective\n- work\n\n" + fake + "\n" + strings.Repeat("- filler that pushes the body past the carry budget\n", 40)
	carried := truncateCarryKeepingTypedState(oversized, compactCheckpointCarryMaxChars)
	state, found, malformed = typedStateFromBody(carried)
	if !found || malformed {
		t.Fatalf("truncated carry must retain the real typed block found=%v malformed=%v:\n%s", found, malformed, carried)
	}
	if !containsString(state.Decisions, "d1: real decision") {
		t.Fatalf("truncated carry lost the real decision: %v", state.Decisions)
	}
}

func TestPriorCheckpointCarryKeepsRealTypedBlockPastQuotedHeading(t *testing.T) {
	// End to end through the display carry: a model-driven checkpoint whose
	// prose quotes the heading text and whose real typed section sits beyond
	// the rune cap must still carry the parseable machine block.
	typed := renderTypedStateJSON(checkpointTypedState{Decisions: []string{"carried-d-1"}})
	filler := strings.Repeat("- a natural-language line quoted with `## Typed Checkpoint State` inside it\n", 40)
	summary := "## Current User Request\n- continue\n\n## Key Decisions\n" + filler + "## Typed Checkpoint State\n" + typed
	if runeCount(summary) <= compactCheckpointCarryMaxChars {
		t.Fatal("fixture body must exceed the display carry cap")
	}
	msg := checkpointMessageForCarryTest(summary, nil)
	got := latestPriorCheckpointBody([]message.Message{msg})
	state, found, malformed := typedStateFromBody(got)
	if !found || malformed {
		t.Fatalf("display carry must keep the real typed block parseable found=%v malformed=%v:\n%s", found, malformed, got)
	}
	if !containsString(state.Decisions, "carried-d-1") {
		t.Fatalf("carried decision lost: %v", state.Decisions)
	}
}

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
