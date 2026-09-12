package tui

import (
	"strings"
	"testing"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/message"
)

// backgroundResultAppended builds the durable-append event a finished
// background job emits: the card is driven by the persisted
// KindBackgroundResult message, not by the live JobFinishedEvent.
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

	block, ok := m.viewport.FindStatusBlockByBackgroundObject("job-1")
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

	block, ok := m.viewport.FindStatusBlockByBackgroundObject("job-1")
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

	block, ok := m.viewport.FindStatusBlockByBackgroundObject("job-duration")
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

	block, ok := m.viewport.FindStatusBlockByBackgroundObject("job-2")
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
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockStatus, Content: "old", BackgroundObjectID: "job-7", AgentID: "builder-2"})

	_ = m.handleAgentEvent(agentEventMsg{event: backgroundResultAppended("builder-2", "subagent-5", "[Background job job-7 finished]\n\nDescription: Run backend tests\nStatus: completed (exit code 0)")})

	block, ok := m.viewport.FindStatusBlockByBackgroundObject("job-7")
	if !ok {
		t.Fatal("expected durable background status block to still exist")
	}
	if !strings.Contains(block.Content, "Run backend tests") {
		t.Fatalf("updated block content = %q, want backend tests", block.Content)
	}
}

func TestBackgroundResultAppendedEventForMainAgentVisibleInMainView(t *testing.T) {
	m := NewModelWithSize(nil, 120, 30)
	m.viewport.SetFilter("main")

	_ = m.handleAgentEvent(agentEventMsg{event: backgroundResultAppended("main", "subagent-6", "[Background job job-3 finished]\n\nDescription: Run integration tests\nStatus: completed (exit code 0)")})

	block, ok := m.viewport.FindStatusBlockByBackgroundObject("job-3")
	if !ok {
		t.Fatal("expected durable status block for main-agent background result")
	}
	if block.AgentID != "" {
		t.Fatalf("block.AgentID = %q, want empty main attribution", block.AgentID)
	}
	visible := false
	for _, b := range m.viewport.visibleBlocks() {
		if b != nil && b.BackgroundObjectID == "job-3" {
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
	if _, ok := m.viewport.FindStatusBlockByBackgroundObject("job-4"); !ok {
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

func TestBackgroundResultCardParsesPurposeAndCommand(t *testing.T) {
	m := NewModelWithSize(nil, 120, 30)
	raw := "[Background job job-9 finished]\n\nKind: bash\nStatus: completed\n" +
		"Purpose: Run the full test suite\nCommand: go test ./...\n\n" +
		"Relevant output:\nok\n\nRead its output with job_output(job-9)."

	_ = m.handleAgentEvent(agentEventMsg{event: backgroundResultAppended("main", "subagent-9", raw)})
	block, ok := m.viewport.FindStatusBlockByBackgroundObject("job-9")
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
	block, ok := m.viewport.FindStatusBlockByBackgroundObject("job-fold")
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
	for _, want := range []string{"✓ ▾ job-fold · Run folded tests", "Completed successfully", "Relevant output:", "line one", "line two"} {
		if !strings.Contains(expanded, want) {
			t.Fatalf("expanded card missing %q:\n%s", want, expanded)
		}
	}
}

func TestOnlyBackgroundResultStatusCardsFold(t *testing.T) {
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
