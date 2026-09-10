package tui

import (
	"strings"
	"testing"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/message"
)

// backgroundResultAppended builds the durable-append event a finished
// background object emits: the card is driven by the persisted
// KindBackgroundResult message, not by the live SpawnFinishedEvent.
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

func TestBackgroundResultAppendedEventAppendsDurableStatusBlock(t *testing.T) {
	m := NewModelWithSize(nil, 120, 30)

	_ = m.handleAgentEvent(agentEventMsg{event: backgroundResultAppended("builder-2", "subagent-1", "[Job job-1 finished]\n\nDescription: Run production build\nStatus: finished (exit 0)")})

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
	rendered := stripANSI(strings.Join(block.Render(100, ""), "\n"))
	for _, want := range []string{"JOB RESULT #1", "✓ job-1 · Run production build", "Completed successfully"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered background result missing %q:\n%s", want, rendered)
		}
	}
	if strings.Contains(rendered, "[Job job-1 finished]") {
		t.Fatalf("rendered background result repeated raw header:\n%s", rendered)
	}
}

func TestBackgroundResultAppendedEventRendersFailureInBodyUnderStableLabel(t *testing.T) {
	m := NewModelWithSize(nil, 120, 30)

	_ = m.handleAgentEvent(agentEventMsg{event: backgroundResultAppended("", "subagent-2", "[Background job job-1 completed]\n\nDescription: Start integration service\nStatus: finished (error: command timed out after 120s: exit status 143)\nReview this result before continuing.\n\nRelevant output:\nINFO: Application startup complete.")})

	block, ok := m.viewport.FindStatusBlockByBackgroundObject("job-1")
	if !ok {
		t.Fatal("expected background result block")
	}
	rendered := stripANSI(strings.Join(block.Render(110, ""), "\n"))
	for _, want := range []string{
		"JOB RESULT #1",
		"✗ job-1 · Start integration service",
		"Error: command timed out after 120s: exit status 143",
		"Relevant output:",
		"INFO: Application startup complete.",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered failure missing %q:\n%s", want, rendered)
		}
	}
	if strings.Contains(rendered, "finished: finished") {
		t.Fatalf("rendered failure retained duplicate status wording:\n%s", rendered)
	}
}

func TestBackgroundResultAppendedEventShowsCompactDurationButCopiesOriginalNote(t *testing.T) {
	m := NewModelWithSize(nil, 120, 30)
	raw := "[Job job-duration finished]\n\nDescription: Run tests\nStatus: finished (exit 0)\n\nRelevant output:\nok\n(command took 17.1s)"
	_ = m.handleAgentEvent(agentEventMsg{event: backgroundResultAppended("", "subagent-3", raw)})

	block, ok := m.viewport.FindStatusBlockByBackgroundObject("job-duration")
	if !ok {
		t.Fatal("expected background result block")
	}
	rendered := stripANSI(strings.Join(block.Render(110, ""), "\n"))
	if !strings.Contains(rendered, "⏱ 17s") {
		t.Fatalf("card missing compact duration:\n%s", rendered)
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
	_ = m.handleAgentEvent(agentEventMsg{event: backgroundResultAppended("", "subagent-4", "[Job job-2 finished]\n\nDescription: Apply patch\nStatus: finished (exit 0)\n\nRelevant output:\n"+
		"```diff\n--- a/main.go\n+++ b/main.go\n@@ -1 +1 @@\n-old\n+new\n```")})

	block, ok := m.viewport.FindStatusBlockByBackgroundObject("job-2")
	if !ok {
		t.Fatal("expected background result block")
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
		Content: "[Job job-9 result]\n\nDescription: Run production build\nStatus: finished (exit 0)",
	}}, &nextID)

	if len(blocks) != 1 {
		t.Fatalf("len(blocks) = %d, want 1", len(blocks))
	}
	block := blocks[0]
	if block.Type != BlockStatus || block.StatusTitle != "JOB RESULT" || block.BackgroundObjectID != "job-9" {
		t.Fatalf("restored block = %#v, want JOB RESULT status for job-9", block)
	}
	rendered := stripANSI(strings.Join(block.Render(100, ""), "\n"))
	if !strings.Contains(rendered, "✓ job-9 · Run production build") || strings.Contains(rendered, "[Job job-9 result]") {
		t.Fatalf("restored background result rendered incorrectly:\n%s", rendered)
	}
}

func TestBackgroundResultAppendedEventUpdatesExistingDurableStatusBlock(t *testing.T) {
	m := NewModelWithSize(nil, 120, 30)
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockStatus, Content: "old", BackgroundObjectID: "job-7", AgentID: "builder-2"})

	_ = m.handleAgentEvent(agentEventMsg{event: backgroundResultAppended("builder-2", "subagent-5", "[Job job-7 finished]\n\nDescription: Run backend tests\nStatus: finished (exit 0)")})

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

	_ = m.handleAgentEvent(agentEventMsg{event: backgroundResultAppended("main", "subagent-6", "[Job job-3 finished]\n\nDescription: Run integration tests\nStatus: finished (exit 0)")})

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

	_ = m.handleAgentEvent(agentEventMsg{event: backgroundResultAppended("main", "subagent-7", "[Job job-4 finished]\n\nDescription: Run production build\nStatus: finished (exit 0)")})

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
	// No "[Job ...]" header, so the parser cannot derive a background object id.
	raw := "Background job finished\n\nDescription: Run production build\nStatus: finished (exit 0)"

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
