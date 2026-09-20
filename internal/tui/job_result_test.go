package tui

import (
	"strings"
	"testing"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/message"
)

// backgroundResultAppended builds the durable-append event a finished
// background job emits: the card is driven by the persisted
// KindBackgroundResult message, not by a live completion notification.
func backgroundResultAppended(targetAgentID, messageID, raw string) agent.BackgroundResultAppendedEvent {
	return agent.BackgroundResultAppendedEvent{
		Message: message.Message{
			Role:    message.RoleUser,
			Kind:    message.KindBackgroundResult,
			Content: raw,
			Mailbox: &message.MailboxMetadata{MessageID: messageID, Kind: string(agent.SubAgentMailboxKindBackgroundResult)},
		},
		TargetAgentID: targetAgentID,
	}
}

func TestBackgroundResultStatusLineClassifiesProductionStatuses(t *testing.T) {
	tests := []struct {
		status    string
		wantGlyph string
		wantLine  string
	}{
		{"completed (exit code 0)", "✓", "Completed successfully"},
		{"failed (exit code 7)", "✗", "Error: exit code 7"},
		{"failed (signal: killed)", "✗", "Error: signal: killed"},
		{"killed (timed out after 120s)", "✗", "Error: timed out after 120s"},
		{"killed (terminated on session switch)", "•", "killed (terminated on session switch)"},
		{"", "•", "Finished"},
	}
	for _, tt := range tests {
		glyph, line := backgroundResultStatusLine(tt.status)
		if glyph != tt.wantGlyph || line != tt.wantLine {
			t.Errorf("backgroundResultStatusLine(%q) = (%q, %q), want (%q, %q)", tt.status, glyph, line, tt.wantGlyph, tt.wantLine)
		}
	}
}

func TestBackgroundResultAppendedEventAppendsDurableStatusBlock(t *testing.T) {
	m := NewModelWithSize(nil, 120, 30)

	_ = m.handleAgentEvent(agentEventMsg{event: backgroundResultAppended("builder-2", "subagent-1", "[Background job job-1 finished]\n\nDescription: Run production build\nStatus: completed (exit code 0)")})

	block, ok := m.viewport.FindStatusBlockByBackgroundObject("subagent-1")
	if !ok {
		t.Fatal("expected durable status block for background object result")
	}
	if block.AgentID != "builder-2" {
		t.Fatalf("block.AgentID = %q, want builder-2", block.AgentID)
	}
	if !strings.Contains(block.Content, "Run production build") {
		t.Fatalf("block.Content = %q, want build description", block.Content)
	}
	if block.StatusTitle != "JOB RESULT" {
		t.Fatalf("block.StatusTitle = %q, want JOB RESULT", block.StatusTitle)
	}
	if !block.Collapsed {
		t.Fatal("background result card must start collapsed")
	}
	rendered := stripANSI(strings.Join(block.Render(100, ""), "\n"))
	for _, want := range []string{"JOB RESULT #1", "✓ ▸ job-1 · Run production build"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered background result missing %q:\n%s", want, rendered)
		}
	}
	if strings.Contains(rendered, "Completed successfully") {
		t.Fatalf("folded card repeats the success summary its ✓ glyph already shows:\n%s", rendered)
	}
	if strings.Contains(rendered, "[Background job job-1 finished]") {
		t.Fatalf("rendered background result repeated raw header:\n%s", rendered)
	}
}

func TestBackgroundResultAppendedEventRendersFailureInBodyUnderStableLabel(t *testing.T) {
	m := NewModelWithSize(nil, 120, 30)

	_ = m.handleAgentEvent(agentEventMsg{event: backgroundResultAppended("", "subagent-2", "[Background job job-1 finished]\n\nDescription: Start integration service\nStatus: killed (timed out after 120s)\n\nRelevant output:\nINFO: Application startup complete.")})

	block, ok := m.viewport.FindStatusBlockByBackgroundObject("subagent-2")
	if !ok {
		t.Fatal("expected background result block")
	}
	if !block.Collapsed {
		t.Fatal("background result card must start collapsed")
	}
	collapsed := stripANSI(strings.Join(block.Render(110, ""), "\n"))
	for _, want := range []string{
		"JOB RESULT #1",
		"✗ ▸ job-1 · Start integration service",
		"Error: timed out after 120s",
	} {
		if !strings.Contains(collapsed, want) {
			t.Fatalf("collapsed failure missing %q:\n%s", want, collapsed)
		}
	}
	if strings.Contains(collapsed, "Relevant output:") || strings.Contains(collapsed, "INFO: Application startup complete.") {
		t.Fatalf("collapsed failure leaked its output body:\n%s", collapsed)
	}

	if !block.ToggleAtWidth(110) || block.Collapsed {
		t.Fatal("toggling the failure card must expand it")
	}
	rendered := stripANSI(strings.Join(block.Render(110, ""), "\n"))
	for _, want := range []string{
		"JOB RESULT #1",
		"✗ ▾ job-1 · Start integration service",
		"Error: timed out after 120s",
		"Relevant output:",
		"INFO: Application startup complete.",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered failure missing %q:\n%s", want, rendered)
		}
	}
	if strings.Contains(rendered, "Error: killed") {
		t.Fatalf("rendered failure retained redundant status verb:\n%s", rendered)
	}
}

func TestBackgroundResultAppendedEventShowsCompactDurationButCopiesOriginalNote(t *testing.T) {
	m := NewModelWithSize(nil, 120, 30)
	raw := "[Background job job-duration finished]\n\nDescription: Run tests\nStatus: completed (exit code 0)\n\nRelevant output:\nok\n(command took 17.1s)"
	_ = m.handleAgentEvent(agentEventMsg{event: backgroundResultAppended("", "subagent-3", raw)})

	block, ok := m.viewport.FindStatusBlockByBackgroundObject("subagent-3")
	if !ok {
		t.Fatal("expected background result block")
	}
	rendered := stripANSI(strings.Join(block.Render(110, ""), "\n"))
	if !strings.Contains(rendered, "⏱ 17s") {
		t.Fatalf("card missing compact duration:\n%s", rendered)
	}
	if strings.Contains(rendered, "Completed successfully") {
		t.Fatalf("folded card repeats the success summary:\n%s", rendered)
	}
	if strings.Contains(rendered, "command took 17.1s") {
		t.Fatalf("card exposed model duration note:\n%s", rendered)
	}
	if got := blockCopyContent(block); !strings.Contains(got, "(command took 17.1s)") {
		t.Fatalf("copied background result lost original duration note:\n%s", got)
	}
}

func TestBackgroundResultAppendedEventHighlightsMarkdownOutputFence(t *testing.T) {
	m := NewModelWithSize(nil, 120, 30)
	_ = m.handleAgentEvent(agentEventMsg{event: backgroundResultAppended("", "subagent-4", "[Background job job-2 finished]\n\nDescription: Apply patch\nStatus: completed (exit code 0)\n\nRelevant output:\n"+
		"```diff\n--- a/main.go\n+++ b/main.go\n@@ -1 +1 @@\n-old\n+new\n```")})

	block, ok := m.viewport.FindStatusBlockByBackgroundObject("subagent-4")
	if !ok {
		t.Fatal("expected background result block")
	}
	if !block.ToggleAtWidth(110) || block.Collapsed {
		t.Fatal("expected the background result card to expand")
	}
	rendered := strings.Join(block.Render(110, ""), "\n")
	if !strings.Contains(stripANSI(rendered), "DIFF") {
		t.Fatalf("rendered fenced output missing language label:\n%s", stripANSI(rendered))
	}
	if !strings.Contains(rendered, "\x1b[") {
		t.Fatalf("rendered fenced output contains no ANSI styling:\n%s", rendered)
	}
}

func TestMessagesToBlocksRestoresBackgroundResultCard(t *testing.T) {
	nextID := 0
	blocks := messagesToBlocks([]message.Message{{
		Role:    message.RoleUser,
		Kind:    message.KindBackgroundResult,
		Content: "[Background job job-9 finished]\n\nDescription: Run production build\nStatus: completed (exit code 0)",
	}}, &nextID)

	if len(blocks) != 1 {
		t.Fatalf("len(blocks) = %d, want 1", len(blocks))
	}
	block := blocks[0]
	if block.Type != BlockStatus || block.StatusTitle != "JOB RESULT" || block.BackgroundObjectID != "job-9" {
		t.Fatalf("restored block = %#v, want JOB RESULT status for job-9", block)
	}
	if !block.Collapsed {
		t.Fatal("restored background result card must start collapsed")
	}
	rendered := stripANSI(strings.Join(block.Render(100, ""), "\n"))
	if !strings.Contains(rendered, "✓ ▸ job-9 · Run production build") || strings.Contains(rendered, "[Background job job-9 finished]") {
		t.Fatalf("restored background result rendered incorrectly:\n%s", rendered)
	}
}

func TestBackgroundResultAppendedEventUpdatesExistingDurableStatusBlock(t *testing.T) {
	m := NewModelWithSize(nil, 120, 30)
	// The existing card was created by an earlier delivery of the same durable
	// mailbox row, so it is keyed by that row's message id.
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockStatus, Content: "old", BackgroundObjectID: "mb-row-7", AgentID: "builder-2"})

	_ = m.handleAgentEvent(agentEventMsg{event: backgroundResultAppended("builder-2", "mb-row-7", "[Background job job-7 finished]\n\nDescription: Run backend tests\nStatus: completed (exit code 0)")})

	block, ok := m.viewport.FindStatusBlockByBackgroundObject("mb-row-7")
	if !ok {
		t.Fatal("expected durable background status block to still exist")
	}
	if !strings.Contains(block.Content, "Run backend tests") {
		t.Fatalf("updated block content = %q, want backend tests", block.Content)
	}
}

// The per-process job id restarts at job-1 after a restart, so a resumed
// session's new job-1 must not overwrite the restored card of the previous
// run's job-1: the durable mailbox row identity, not the headline id, decides
// whether a re-delivery updates an existing card.
func TestBackgroundResultAppendedEventDoesNotOverwriteRestoredCardWithReusedJobID(t *testing.T) {
	m := NewModelWithSize(nil, 120, 30)
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockStatus, StatusTitle: backgroundResultCardTitle, Content: "✓ job-1 · Run production build (previous run)", BackgroundObjectID: "mb-old", AgentID: "", Collapsed: true})

	_ = m.handleAgentEvent(agentEventMsg{event: backgroundResultAppended("main", "mb-new", "[Background job job-1 finished]\n\nDescription: Run production build\nStatus: completed (exit code 0)")})

	old, ok := m.viewport.FindStatusBlockByBackgroundObject("mb-old")
	if !ok || !strings.Contains(old.Content, "previous run") {
		t.Fatalf("restored previous-run card = (%#v, %v), want it intact", old, ok)
	}
	fresh, ok := m.viewport.FindStatusBlockByBackgroundObject("mb-new")
	if !ok || !strings.Contains(fresh.Content, "Run production build") {
		t.Fatalf("new-process result card = (%#v, %v), want its own card", fresh, ok)
	}
}

func TestBackgroundResultAppendedEventForMainAgentVisibleInMainView(t *testing.T) {
	m := NewModelWithSize(nil, 120, 30)
	m.viewport.SetFilter("main")

	_ = m.handleAgentEvent(agentEventMsg{event: backgroundResultAppended("main", "mb-main-3", "[Background job job-3 finished]\n\nDescription: Run integration tests\nStatus: completed (exit code 0)")})

	block, ok := m.viewport.FindStatusBlockByBackgroundObject("mb-main-3")
	if !ok {
		t.Fatal("expected durable status block for main-agent background result")
	}
	if block.AgentID != "" {
		t.Fatalf("block.AgentID = %q, want empty main attribution", block.AgentID)
	}
	visible := false
	for _, b := range m.viewport.visibleBlocks() {
		if b != nil && b.BackgroundObjectID == "mb-main-3" {
			visible = true
			break
		}
	}
	if !visible {
		t.Fatal("expected background result block to be visible under the main filter")
	}
}

func TestBackgroundResultAppendedEventRemovesQueuedMailboxEntry(t *testing.T) {
	m := NewModelWithSize(nil, 120, 30)
	m.upsertQueuedMailbox(agent.SubAgentMailboxMessage{
		MessageID:    "subagent-7",
		OwnerAgentID: "",
		Kind:         agent.SubAgentMailboxKindBackgroundResult,
		Summary:      "Run production build",
	})
	if got := len(m.mailboxQueue); got != 1 {
		t.Fatalf("len(mailboxQueue) = %d, want the queued result before delivery", got)
	}

	_ = m.handleAgentEvent(agentEventMsg{event: backgroundResultAppended("main", "subagent-7", "[Background job job-4 finished]\n\nDescription: Run production build\nStatus: completed (exit code 0)")})

	if got := len(m.mailboxQueue); got != 0 {
		t.Fatalf("len(mailboxQueue) = %d, want the delivered result removed from the pending area", got)
	}
	if _, ok := m.viewport.FindStatusBlockByBackgroundObject("subagent-7"); !ok {
		t.Fatal("expected the delivered background result card")
	}
}

func TestFilterBlocksByAgentMainIncludesMainAttributedBlocks(t *testing.T) {
	blocks := []*Block{
		{ID: 1, AgentID: ""},
		{ID: 2, AgentID: "main"},
		{ID: 3, AgentID: "builder-2"},
	}
	filtered := filterBlocksByAgent(blocks, "main")
	if len(filtered) != 2 {
		t.Fatalf("len(filtered) = %d, want 2", len(filtered))
	}
	if filtered[0].ID != 1 || filtered[1].ID != 2 {
		t.Fatalf("filtered IDs = [%d %d], want [1 2]", filtered[0].ID, filtered[1].ID)
	}
}

func jobResultCardCount(m Model) int {
	count := 0
	for _, block := range m.viewport.blocks {
		if block != nil && block.Type == BlockStatus && block.StatusTitle == backgroundResultCardTitle {
			count++
		}
	}
	return count
}

func TestBackgroundResultAppendedEventUsesMessageIDWhenHeadlineCarriesNoJobID(t *testing.T) {
	m := NewModelWithSize(nil, 120, 30)
	// No "[Background job ...]" header, so the parser cannot derive a background object id.
	raw := "Background job finished\n\nDescription: Run production build\nStatus: completed (exit code 0)"

	_ = m.handleAgentEvent(agentEventMsg{event: backgroundResultAppended("builder-2", "subagent-a", raw)})
	block, ok := m.viewport.FindStatusBlockByBackgroundObject("subagent-a")
	if !ok {
		t.Fatal("a result with no job id in its headline must fall back to the mailbox message id as the card identity")
	}
	if block.BackgroundObjectID != "subagent-a" {
		t.Fatalf("BackgroundObjectID = %q, want the mailbox message id", block.BackgroundObjectID)
	}

	// Re-delivering the same result updates that card instead of adding one.
	_ = m.handleAgentEvent(agentEventMsg{event: backgroundResultAppended("builder-2", "subagent-a", raw)})
	if cards := jobResultCardCount(m); cards != 1 {
		t.Fatalf("JOB RESULT cards after re-delivery = %d, want 1 (same result must merge)", cards)
	}

	// A different job with the same description is its own card.
	_ = m.handleAgentEvent(agentEventMsg{event: backgroundResultAppended("builder-2", "subagent-b", raw)})
	if cards := jobResultCardCount(m); cards != 2 {
		t.Fatalf("JOB RESULT cards = %d, want 2 (distinct jobs must not merge)", cards)
	}
}

// foldedBackgroundResultRows strips the card frame off a render and returns the
// rows its body occupies: one entry per visible row, without the rail column or
// the trailing padding the card wrapper adds.
func foldedBackgroundResultRows(t *testing.T, block *Block, width int) []string {
	t.Helper()
	rendered := stripANSI(strings.Join(block.Render(width, ""), "\n"))
	var rows []string
	for line := range strings.SplitSeq(rendered, "\n") {
		row := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "│"))
		if row == "" || strings.HasPrefix(row, "JOB RESULT") {
			continue
		}
		rows = append(rows, row)
	}
	if len(rows) == 0 {
		t.Fatalf("card rendered no body rows:\n%s", rendered)
	}
	return rows
}

// A folded JOB RESULT card reads like a folded tool card: one headline row with
// the measured duration trailing it, nothing else. The successful summary the ✓
// already states is gone, and so is the quiet duration, which says something
// only while a job is still running.
func TestBackgroundResultFoldedCarriesElapsedOnTheHeadlineAlone(t *testing.T) {
	m := NewModelWithSize(nil, 120, 30)
	raw := "[Background job job-40 finished]\n\nStatus: completed (exit code 0)\nElapsed: 17s\nQuiet: 0s\nPurpose: Run gateway tests, race, quality checks\n\nRelevant output:\ncoverage check passed: total 75.9%"

	_ = m.handleAgentEvent(agentEventMsg{event: backgroundResultAppended("", "mb-fold-elapsed", raw)})
	block, ok := m.viewport.FindStatusBlockByBackgroundObject("mb-fold-elapsed")
	if !ok {
		t.Fatal("expected background result block")
	}
	if !block.Collapsed {
		t.Fatal("background result card must start collapsed")
	}
	rows := foldedBackgroundResultRows(t, block, 90)
	if len(rows) != 1 {
		t.Fatalf("folded rows = %d (%v), want the headline alone", len(rows), rows)
	}
	if !strings.Contains(rows[0], "✓ ▸ job-40 · Run gateway tests, race, quality checks") {
		t.Fatalf("folded headline = %q, want the job id and purpose", rows[0])
	}
	if !strings.HasSuffix(rows[0], elapsedGlyph+" 17s") {
		t.Fatalf("folded headline = %q, want the duration trailing it", rows[0])
	}
	rendered := strings.Join(rows, "\n")
	if strings.Contains(rendered, "quiet") {
		t.Fatalf("folded card kept the quiet duration:\n%s", rendered)
	}
	if strings.Contains(rendered, "coverage check passed") || strings.Contains(rendered, "Relevant output:") {
		t.Fatalf("folded card leaked its output body:\n%s", rendered)
	}
}

// Opening the card must not move the measurement: a tool card keeps its elapsed
// on the header whether the card is folded or open, and the row that used to
// carry it only restated what the ✓ glyph already said.
func TestBackgroundResultExpandedKeepsElapsedOnTheHeadline(t *testing.T) {
	m := NewModelWithSize(nil, 120, 30)
	raw := "[Background job job-45 finished]\n\nStatus: completed (exit code 0)\nElapsed: 42s\nQuiet: 0s\nPurpose: Run the migration\n\nRelevant output:\napplied 3 migrations"

	_ = m.handleAgentEvent(agentEventMsg{event: backgroundResultAppended("", "mb-open-elapsed", raw)})
	block, ok := m.viewport.FindStatusBlockByBackgroundObject("mb-open-elapsed")
	if !ok {
		t.Fatal("expected background result block")
	}
	headlineOf := func(rows []string, label string) string {
		for _, row := range rows {
			if strings.Contains(row, "job-45") {
				return row
			}
		}
		t.Fatalf("%s card has no job-45 headline: %v", label, rows)
		return ""
	}
	folded := foldedBackgroundResultRows(t, block, 90)
	if headline := headlineOf(folded, "folded"); !strings.HasSuffix(headline, elapsedGlyph+" 42s") {
		t.Fatalf("folded headline = %q, want the duration trailing it", headline)
	}

	if !block.ToggleAtWidth(90) || block.Collapsed {
		t.Fatal("toggling must expand the card")
	}
	rows := foldedBackgroundResultRows(t, block, 90)
	headline := headlineOf(rows, "expanded")
	if !strings.HasSuffix(headline, elapsedGlyph+" 42s") {
		t.Fatalf("expanded headline = %q, want the duration to stay on the headline", headline)
	}
	rendered := strings.Join(rows, "\n")
	if strings.Contains(rendered, "Completed successfully") {
		t.Fatalf("expanded card repeats the success summary:\n%s", rendered)
	}
	if !strings.Contains(rendered, "applied 3 migrations") {
		t.Fatalf("expanded card lost its output body:\n%s", rendered)
	}
}

// An expanded headline keeps every word when the elapsed tail is appended: the
// wrap holds room for the tail, so it is not cut out of the last line to make
// the tail fit. At width 50 the natural wrap would fill the last line and lose
// the words before the tail.
func TestBackgroundResultExpandedHeadlineKeepsTextBesideTheElapsedTail(t *testing.T) {
	const purpose = "run the full gateway test suite with the race detector and every quality gate enabledxxxxxxxxx"
	block := &Block{Type: BlockStatus, StatusTitle: backgroundResultCardTitle,
		Content: "✓ job-43 · " + purpose + "\nCompleted successfully · " + elapsedGlyph + " 17s"}

	rows := foldedBackgroundResultRows(t, block, 50)
	joined := strings.Join(rows, " ")
	if strings.Contains(joined, "…") {
		t.Fatalf("expanded headline truncated text to fit the elapsed tail:\n%s", strings.Join(rows, "\n"))
	}
	if !strings.Contains(joined, purpose) {
		t.Fatalf("expanded headline dropped text beside the elapsed tail:\n%s", strings.Join(rows, "\n"))
	}
	if last := rows[len(rows)-1]; !strings.HasSuffix(last, elapsedGlyph+" 17s") {
		t.Fatalf("expanded headline lost the elapsed tail: %q", last)
	}
}

// A failed job keeps a second row — the failure detail the glyph does not name —
// but its duration still rides the headline, so every card measures its time in
// the same column.
func TestBackgroundResultFoldedFailureKeepsErrorRowAndMovesElapsed(t *testing.T) {
	m := NewModelWithSize(nil, 120, 30)
	raw := "[Background job job-42 finished]\n\nStatus: failed (exit code 7)\nElapsed: 12s\nQuiet: 9s\nPurpose: failing job\n\nRelevant output:\nboom"

	_ = m.handleAgentEvent(agentEventMsg{event: backgroundResultAppended("", "mb-fold-failed", raw)})
	block, ok := m.viewport.FindStatusBlockByBackgroundObject("mb-fold-failed")
	if !ok {
		t.Fatal("expected background result block")
	}
	rows := foldedBackgroundResultRows(t, block, 90)
	if len(rows) != 2 {
		t.Fatalf("folded rows = %d (%v), want the headline and its failure detail", len(rows), rows)
	}
	if !strings.Contains(rows[0], "✗ ▸ job-42 · failing job") || !strings.HasSuffix(rows[0], elapsedGlyph+" 12s") {
		t.Fatalf("folded failure headline = %q, want the job summary with its duration", rows[0])
	}
	if rows[1] != "↳ Error: exit code 7" {
		t.Fatalf("folded failure row = %q, want the failure detail alone", rows[1])
	}
}

// The headline yields width to the duration instead of pushing it off the card:
// the row truncates with "…" and the measurement stays readable, which is how a
// narrow tool card header behaves.
func TestBackgroundResultFoldedHeadlineYieldsWidthToTheElapsedTail(t *testing.T) {
	m := NewModelWithSize(nil, 120, 30)
	raw := "[Background job job-43 finished]\n\nStatus: completed (exit code 0)\nElapsed: 17s\nQuiet: 0s\nPurpose: Run the full gateway test suite with the race detector and every quality gate enabled\n\nRelevant output:\nok"

	_ = m.handleAgentEvent(agentEventMsg{event: backgroundResultAppended("", "mb-fold-narrow", raw)})
	block, ok := m.viewport.FindStatusBlockByBackgroundObject("mb-fold-narrow")
	if !ok {
		t.Fatal("expected background result block")
	}
	rows := foldedBackgroundResultRows(t, block, 50)
	if len(rows) != 1 {
		t.Fatalf("narrow folded rows = %d (%v), want the headline kept on one row", len(rows), rows)
	}
	if !strings.Contains(rows[0], "…") {
		t.Fatalf("narrow headline = %q, want truncation around the duration", rows[0])
	}
	if !strings.HasSuffix(rows[0], elapsedGlyph+" 17s") {
		t.Fatalf("narrow headline = %q, want the duration to survive truncation", rows[0])
	}
}

func TestBackgroundResultCardParsesPurposeAndCommand(t *testing.T) {
	m := NewModelWithSize(nil, 120, 30)
	raw := "[Background job job-9 finished]\n\nKind: bash\nStatus: completed\n" +
		"Purpose: Run the full test suite\nCommand: go test ./...\n\n" +
		"Relevant output:\nok\n\nRead its output with job_output(job-9)."

	_ = m.handleAgentEvent(agentEventMsg{event: backgroundResultAppended("main", "subagent-9", raw)})
	block, ok := m.viewport.FindStatusBlockByBackgroundObject("subagent-9")
	if !ok {
		t.Fatal("expected a background result card for job-9")
	}
	if !strings.Contains(block.Content, "Run the full test suite") {
		t.Fatalf("card content = %q, want the purpose to appear", block.Content)
	}
	for _, leaked := range []string{"Purpose:", "Command:", "Kind:"} {
		if strings.Contains(block.Content, leaked) {
			t.Fatalf("card content leaked raw field label %q: %q", leaked, block.Content)
		}
	}
	if !strings.Contains(block.Content, "Relevant output:") || !strings.Contains(block.Content, "ok") {
		t.Fatalf("card content = %q, want the relevant output section preserved", block.Content)
	}
}

func TestBackgroundResultCardFoldsOutputAndExpandsOnToggle(t *testing.T) {
	m := NewModelWithSize(nil, 120, 30)
	raw := "[Background job job-fold finished]\n\nDescription: Run folded tests\nStatus: completed (exit code 0)\n\nRelevant output:\nline one\nline two"

	_ = m.handleAgentEvent(agentEventMsg{event: backgroundResultAppended("", "subagent-fold", raw)})
	block, ok := m.viewport.FindStatusBlockByBackgroundObject("subagent-fold")
	if !ok {
		t.Fatal("expected background result block")
	}
	if !block.Collapsed {
		t.Fatal("background result card must start collapsed")
	}
	collapsed := stripANSI(strings.Join(block.Render(110, ""), "\n"))
	if !strings.Contains(collapsed, "✓ ▸ job-fold · Run folded tests") {
		t.Fatalf("collapsed card missing the job headline:\n%s", collapsed)
	}
	if strings.Contains(collapsed, "Completed successfully") {
		t.Fatalf("folded card repeats the success summary its ✓ glyph already shows:\n%s", collapsed)
	}
	for _, hidden := range []string{"Relevant output:", "line one", "line two"} {
		if strings.Contains(collapsed, hidden) {
			t.Fatalf("collapsed card leaked %q:\n%s", hidden, collapsed)
		}
	}

	if !block.ToggleAtWidth(110) || block.Collapsed {
		t.Fatal("toggling must expand the card")
	}
	expanded := stripANSI(strings.Join(block.Render(110, ""), "\n"))
	for _, want := range []string{"✓ ▾ job-fold · Run folded tests", "Relevant output:", "line one", "line two"} {
		if !strings.Contains(expanded, want) {
			t.Fatalf("expanded card missing %q:\n%s", want, expanded)
		}
	}
	// The success summary duplicates the ✓ headline, so opening the card must
	// not bring it back.
	if strings.Contains(expanded, "Completed successfully") {
		t.Fatalf("expanded card repeats the success summary its ✓ glyph already shows:\n%s", expanded)
	}
}

func TestStatusCardFoldFamilies(t *testing.T) {
	// A runtime notice folds to a one-line summary when its body hides more
	// than the first line.
	notice := &Block{Type: BlockStatus, StatusTitle: "LOOP", Content: "Target:\n- finish current task"}
	if !notice.ToggleAtWidth(100) || !notice.Collapsed {
		t.Fatal("multi-line runtime notices must fold")
	}

	// A sub-agent mailbox card carries the worker model's own message, so it
	// stays expanded and space is a no-op on it.
	mailbox := &Block{Type: BlockStatus, StatusTitle: "AGENT MESSAGE", StatusKind: "progress", Content: "worker report\nmore detail"}
	if mailbox.ToggleAtWidth(100) {
		t.Fatal("mailbox status cards must not fold")
	}
	if mailbox.Collapsed {
		t.Fatal("mailbox status cards must stay expanded")
	}

	// A JOB RESULT card keeps folding to each job's headline.
	job := &Block{Type: BlockStatus, StatusTitle: backgroundResultCardTitle, Content: "✓ job-1 · task\nfinished"}
	if !job.ToggleAtWidth(100) || !job.Collapsed {
		t.Fatal("JOB RESULT cards must fold")
	}
}

// The header matcher accepts only the headline the job registry writes. A
// result persisted by the removed spawn tool is body text now: it must not be
// mistaken for a headline, and it must not yield a job id, because that id
// names a handle no tool can use any more. The card still renders — the
// Description/Status/Relevant output fields parse as before — so the loss is
// limited to the id on the headline.
func TestBackgroundResultHeaderMatchesOnlyTheCurrentFormat(t *testing.T) {
	const legacy = "[Background job job-1 completed]\n\nDescription: Start integration service\nStatus: finished"
	nextID := 0
	blocks := messagesToBlocks([]message.Message{{
		Role:    message.RoleUser,
		Kind:    message.KindBackgroundResult,
		Content: legacy,
	}}, &nextID)
	if len(blocks) != 1 {
		t.Fatalf("len(blocks) = %d, want 1", len(blocks))
	}
	block := blocks[0]
	if block.BackgroundObjectID != "" {
		t.Fatalf("legacy header yielded job id %q, want none", block.BackgroundObjectID)
	}
	if block.StatusTitle != backgroundResultCardTitle {
		t.Fatalf("legacy result must still render as a JOB RESULT card, got %q", block.StatusTitle)
	}

	folded := stripANSI(strings.Join(block.Render(120, ""), "\n"))
	if !strings.Contains(folded, "Start integration service") {
		t.Fatalf("folded legacy card lost its description:\n%s", folded)
	}
	if strings.Contains(folded, "job-1") {
		t.Fatalf("folded legacy card must not name an unusable id:\n%s", folded)
	}

	if !block.ToggleAtWidth(120) || block.Collapsed {
		t.Fatal("toggling must expand the legacy card")
	}
	expanded := stripANSI(strings.Join(block.Render(120, ""), "\n"))
	if !strings.Contains(expanded, "[Background job job-1 completed]") {
		t.Fatalf("expanded legacy card must keep the unrecognized line as body text:\n%s", expanded)
	}

	// The current format still yields its id, so the strictness above is a
	// format check and not a blanket refusal.
	nextID = 0
	blocks = messagesToBlocks([]message.Message{{
		Role:    message.RoleUser,
		Kind:    message.KindBackgroundResult,
		Content: "[Background job job-7 finished]\n\nDescription: Run tests\nStatus: completed (exit code 0)",
	}}, &nextID)
	if len(blocks) != 1 || blocks[0].BackgroundObjectID != "job-7" {
		t.Fatalf("current-format header must yield job-7, got %q", blocks[0].BackgroundObjectID)
	}
}
