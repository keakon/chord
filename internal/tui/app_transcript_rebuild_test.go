package tui

import (
	"testing"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func findRebuiltToolBlock(blocks []*Block, toolID string) *Block {
	for _, b := range blocks {
		if b != nil && b.Type == BlockToolCall && b.ToolID == toolID {
			return b
		}
	}
	return nil
}

// A durable compaction replaces the archived head with one summary card, so
// every surviving tail row shifts left. State carry-over must match the card
// itself, not whichever unrelated card happened to sit at the same old index.
func TestCompactionRebuildPreservesTailEditCardState(t *testing.T) {
	backend := &sessionControlAgent{}
	m := NewModelWithSize(backend, 120, 24)
	// Pre-compaction viewport: the rebuilt Edit card lands on index 1, where a
	// collapsed Read card sits before compaction.
	m.viewport.AppendBlock(&Block{ID: 0, Type: BlockUser, Content: "first prompt"})
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockToolCall, ToolName: tools.NameRead, ToolID: "read-1", Content: `{"path":"a.go"}`, Collapsed: true, ResultDone: true})
	m.viewport.AppendBlock(&Block{ID: 2, Type: BlockToolCall, ToolName: tools.NameEdit, ToolID: "edit-1", Content: `{"path":"foo.txt"}`, Collapsed: false, ResultDone: true})
	m.nextBlockID = 3

	backend.messages = []message.Message{
		{Role: "user", IsCompactionSummary: true, Content: "[Context Summary]\nsummary\n\n[Context compressed]"},
		{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "edit-1", Name: tools.NameEdit, Args: []byte(`{"path":"foo.txt","patch":"@@\n-a\n+b\n"}`)}}},
		{Role: "tool", ToolCallID: "edit-1", Content: "Applied patch to foo.txt (+1 -1)", ToolStatus: string(agent.ToolResultStatusSuccess)},
	}
	m.rebuildViewportFromMessagesWithReason("session_restored")

	edit := findRebuiltToolBlock(m.viewport.visibleBlocks(), "edit-1")
	if edit == nil {
		t.Fatal("rebuilt transcript lost the preserved-tail Edit card")
	}
	if edit.Collapsed {
		t.Fatal("preserved-tail Edit card folded after compaction; it should stay expanded")
	}
	if edit.ID != 2 {
		t.Fatalf("preserved-tail Edit card ID = %d, want its pre-compaction ID 2", edit.ID)
	}
}

func TestCompactionRebuildPreservesGenericToolDetailState(t *testing.T) {
	backend := &sessionControlAgent{}
	m := NewModelWithSize(backend, 120, 24)
	m.viewport.AppendBlock(&Block{ID: 0, Type: BlockUser, Content: "first prompt"})
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockToolCall, ToolName: tools.NameRead, ToolID: "read-1", Content: `{"path":"a.go"}`, Collapsed: true, ResultDone: true})
	m.viewport.AppendBlock(&Block{ID: 2, Type: BlockToolCall, ToolName: tools.NameGrep, ToolID: "grep-1", Content: `{"pattern":"needle"}`, Collapsed: true, ToolCallDetailExpanded: true, ResultDone: true})
	m.nextBlockID = 3

	backend.messages = []message.Message{
		{Role: "user", IsCompactionSummary: true, Content: "[Context Summary]\nsummary\n\n[Context compressed]"},
		{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "grep-1", Name: tools.NameGrep, Args: []byte(`{"pattern":"needle"}`)}}},
		{Role: "tool", ToolCallID: "grep-1", Content: "a.go:1:needle", ToolStatus: string(agent.ToolResultStatusSuccess)},
	}
	m.rebuildViewportFromMessagesWithReason("session_restored")

	grep := findRebuiltToolBlock(m.viewport.visibleBlocks(), "grep-1")
	if grep == nil {
		t.Fatal("rebuilt transcript lost the preserved-tail Grep card")
	}
	if !grep.Collapsed {
		t.Fatal("Grep card unexpectedly expanded after compaction")
	}
	if !grep.ToolCallDetailExpanded {
		t.Fatal("preserved-tail Grep card lost its detail-expanded state after compaction")
	}
	if grep.ID != 2 {
		t.Fatalf("preserved-tail Grep card ID = %d, want 2", grep.ID)
	}
}

func TestCompactionRebuildPreservesThinkingCollapsedState(t *testing.T) {
	backend := &sessionControlAgent{}
	m := NewModelWithSize(backend, 120, 24)
	m.viewport.AppendBlock(&Block{ID: 0, Type: BlockUser, Content: "first prompt"})
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockThinking, AgentID: "sub-1", Content: "other thought", ThinkingCollapsed: false})
	m.viewport.AppendBlock(&Block{ID: 2, Type: BlockThinking, Content: "reasoning text", ThinkingCollapsed: true})
	m.nextBlockID = 3

	backend.messages = []message.Message{
		{Role: "user", IsCompactionSummary: true, Content: "[Context Summary]\nsummary\n\n[Context compressed]"},
		{Role: "assistant", ThinkingBlocks: []message.ThinkingBlock{{Thinking: "reasoning text"}}},
	}
	m.rebuildViewportFromMessagesWithReason("session_restored")

	var thinking *Block
	for _, b := range m.viewport.visibleBlocks() {
		if b != nil && b.Type == BlockThinking && b.Content == "reasoning text" {
			thinking = b
		}
	}
	if thinking == nil {
		t.Fatal("rebuilt transcript lost the preserved-tail thinking card")
	}
	if !thinking.ThinkingCollapsed {
		t.Fatal("preserved-tail collapsed thinking card re-expanded after compaction")
	}
	if thinking.ID != 2 {
		t.Fatalf("preserved-tail thinking card ID = %d, want 2", thinking.ID)
	}
}

func TestCompactionRebuildDoesNotAdoptAmbiguousDuplicateCardState(t *testing.T) {
	backend := &sessionControlAgent{}
	m := NewModelWithSize(backend, 120, 24)
	m.viewport.AppendBlock(&Block{ID: 0, Type: BlockUser, Content: "first prompt"})
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockThinking, Content: "repeated reasoning", ThinkingCollapsed: true})
	m.viewport.AppendBlock(&Block{ID: 2, Type: BlockThinking, Content: "repeated reasoning", ThinkingCollapsed: false})
	m.nextBlockID = 3

	backend.messages = []message.Message{
		{Role: "user", IsCompactionSummary: true, Content: "[Context Summary]\nsummary\n\n[Context compressed]"},
		{Role: "assistant", ThinkingBlocks: []message.ThinkingBlock{{Thinking: "repeated reasoning"}}},
	}
	m.rebuildViewportFromMessagesWithReason("session_restored")

	var thinking *Block
	for _, b := range m.viewport.visibleBlocks() {
		if b != nil && b.Type == BlockThinking && b.Content == "repeated reasoning" {
			thinking = b
		}
	}
	if thinking == nil {
		t.Fatal("rebuilt transcript lost the surviving duplicate thinking card")
	}
	if thinking.ID == 1 || thinking.ID == 2 {
		t.Fatalf("ambiguous duplicate thinking card reused an old ID %d", thinking.ID)
	}
	if thinking.ThinkingCollapsed {
		t.Fatal("ambiguous duplicate thinking card inherited an old collapsed state")
	}
}

// A card with no counterpart in the previous transcript keeps the state the
// builder gave it instead of borrowing the collapse state of the old card that
// sat at the same index.
func TestCompactionRebuildDoesNotBorrowStateFromUnrelatedCard(t *testing.T) {
	backend := &sessionControlAgent{}
	m := NewModelWithSize(backend, 120, 24)
	m.viewport.AppendBlock(&Block{ID: 0, Type: BlockUser, Content: "first prompt"})
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockToolCall, ToolName: tools.NameEdit, ToolID: "edit-old", Content: `{"path":"old.txt"}`, Collapsed: true, ResultDone: true})
	m.nextBlockID = 2

	backend.messages = []message.Message{
		{Role: "user", IsCompactionSummary: true, Content: "[Context Summary]\nsummary\n\n[Context compressed]"},
		{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "edit-new", Name: tools.NameEdit, Args: []byte(`{"path":"new.txt","patch":"@@\n-a\n+b\n"}`)}}},
		{Role: "tool", ToolCallID: "edit-new", Content: "Applied patch to new.txt (+1 -1)", ToolStatus: string(agent.ToolResultStatusSuccess)},
	}
	m.rebuildViewportFromMessagesWithReason("session_restored")

	edit := findRebuiltToolBlock(m.viewport.visibleBlocks(), "edit-new")
	if edit == nil {
		t.Fatal("rebuilt transcript lost the new Edit card")
	}
	if edit.Collapsed {
		t.Fatal("new Edit card inherited a collapsed state from an unrelated old card")
	}
	if edit.ID == 1 {
		t.Fatal("new Edit card reused the ID of the unrelated old card")
	}
}

// A session switch leaves the outgoing session's cards in the viewport, and the
// incoming transcript can carry identical text without it being the same card.
// The rebuild reason cannot separate the two cases — a durable compaction
// rebuilds with the same "session_restored" reason — so the epoch is what marks
// the transcript as belonging to another session, and adoption must stop there.
func TestSessionSwitchRebuildAdoptsNoStateFromOutgoingTranscript(t *testing.T) {
	backend := &sessionControlAgent{}
	m := NewModelWithSize(backend, 120, 24)

	m.viewport.AppendBlock(&Block{ID: 0, Type: BlockUser, Content: "same prompt"})
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockThinking, Content: "reasoning text", ThinkingCollapsed: true})
	m.nextBlockID = 2

	// What the session-restore handler does before it rebuilds.
	m.sessionTranscriptEpoch++

	backend.messages = []message.Message{
		{Role: "user", Content: "same prompt"},
		{Role: "assistant", ThinkingBlocks: []message.ThinkingBlock{{Thinking: "reasoning text"}}},
	}
	m.rebuildViewportFromMessagesWithReason("session_restored")

	var user, thinking *Block
	for _, b := range m.viewport.visibleBlocks() {
		switch b.Type {
		case BlockUser:
			user = b
		case BlockThinking:
			thinking = b
		}
	}
	if user == nil || thinking == nil {
		t.Fatal("rebuilt transcript lost a card")
	}
	if user.ID == 0 {
		t.Fatal("incoming user card reused the outgoing session's ID")
	}
	if thinking.ID == 1 {
		t.Fatal("incoming thinking card reused the outgoing session's ID")
	}
	if thinking.ThinkingCollapsed {
		t.Fatal("incoming thinking card inherited the outgoing session's collapsed state")
	}
}

// The epoch only separates sessions: a rebuild inside one session still adopts,
// so the guard above cannot be satisfied by disabling adoption outright.
func TestSameSessionRebuildStillAdoptsCardState(t *testing.T) {
	backend := &sessionControlAgent{}
	m := NewModelWithSize(backend, 120, 24)

	m.viewport.AppendBlock(&Block{ID: 0, Type: BlockThinking, Content: "reasoning text", ThinkingCollapsed: true})
	m.nextBlockID = 1

	backend.messages = []message.Message{
		{Role: "assistant", ThinkingBlocks: []message.ThinkingBlock{{Thinking: "reasoning text"}}},
	}
	m.rebuildViewportFromMessagesWithReason("session_restored")

	thinking := m.viewport.visibleBlocks()[0]
	if thinking.ID != 0 || !thinking.ThinkingCollapsed {
		t.Fatalf("same-session rebuild must still adopt state, got ID=%d collapsed=%v", thinking.ID, thinking.ThinkingCollapsed)
	}
}

// A JOB RESULT card rebuilt from the durable background_result row must adopt
// the live card's state through its durable identity — the mailbox message id,
// with the background object id as fallback — so a compaction rebuild does not
// reset a card whose long output would never match by content digest.
func TestCompactionRebuildPreservesBackgroundResultCardState(t *testing.T) {
	backend := &sessionControlAgent{}
	m := NewModelWithSize(backend, 120, 24)
	// The live card, as BackgroundResultAppendedEvent builds it: parsed
	// headline content plus the durable mailbox row identity as the card key.
	m.viewport.AppendBlock(&Block{ID: 0, Type: BlockUser, Content: "first prompt"})
	m.viewport.AppendBlock(&Block{
		ID:                 1,
		Type:               BlockStatus,
		StatusTitle:        backgroundResultCardTitle,
		Content:            "✓ job-1 · Run production build",
		BackgroundObjectID: "mb-1",
		MailboxMessageID:   "mb-1",
		Collapsed:          true,
	})
	m.nextBlockID = 2

	backend.messages = []message.Message{
		{Role: "user", IsCompactionSummary: true, Content: "[Context Summary]\nsummary\n\n[Context compressed]"},
		{Role: "user", Kind: message.KindBackgroundResult, Content: "✓ job-1 · Run production build\nStatus: completed (exit code 0)", Mailbox: &message.MailboxMetadata{MessageID: "mb-1"}},
	}
	m.rebuildViewportFromMessagesWithReason("session_restored")

	var restored *Block
	for _, b := range m.viewport.visibleBlocks() {
		if b.Type == BlockStatus && b.MailboxMessageID == "mb-1" {
			restored = b
			break
		}
	}
	if restored == nil {
		t.Fatal("rebuilt transcript lost the JOB RESULT card")
	}
	if restored.ID != 1 {
		t.Fatalf("rebuilt JOB RESULT card ID = %d, want its pre-compaction ID 1", restored.ID)
	}
	if !restored.Collapsed {
		t.Fatal("rebuilt JOB RESULT card lost its folded state")
	}
}

// A background result whose headline is not the registry's "[Background job
// <id> finished]" line derives no background object id from its content, so
// the rebuild must fall back to the durable mailbox message id — the same
// fallback the live event path applies — or a re-delivery after the rebuild
// would look the card up by that identity, miss, and append a duplicate.
func TestCompactionRebuildFallsBackToMailboxIdentityForBackgroundResults(t *testing.T) {
	backend := &sessionControlAgent{}
	m := NewModelWithSize(backend, 120, 24)
	backend.messages = []message.Message{
		{Role: "user", IsCompactionSummary: true, Content: "[Context Summary]\nsummary\n\n[Context compressed]"},
		{Role: "user", Kind: message.KindBackgroundResult, Content: "Background job finished\nStatus: completed (exit code 0)", Mailbox: &message.MailboxMetadata{MessageID: "mb-9"}},
	}
	m.rebuildViewportFromMessagesWithReason("session_restored")

	var restored *Block
	for _, b := range m.viewport.visibleBlocks() {
		if b.Type == BlockStatus && b.MailboxMessageID == "mb-9" {
			restored = b
			break
		}
	}
	if restored == nil {
		t.Fatal("rebuilt transcript lost the JOB RESULT card")
	}
	if restored.BackgroundObjectID != "mb-9" {
		t.Fatalf("rebuilt card BackgroundObjectID = %q, want the mailbox message id", restored.BackgroundObjectID)
	}
}

// Repeated identical rows still pair in transcript order: with an unchanged
// occurrence count, the nth rebuilt copy adopts the nth previous card's state,
// so identical prompts do not reset each other's fold state and ID.
func TestCompactionRebuildPairsRepeatedUserCardsInOrder(t *testing.T) {
	backend := &sessionControlAgent{}
	m := NewModelWithSize(backend, 120, 24)
	m.viewport.AppendBlock(&Block{ID: 0, Type: BlockUser, Content: "continue"})
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockUser, Content: "continue", Collapsed: true})
	m.nextBlockID = 2

	backend.messages = []message.Message{
		{Role: "user", Content: "continue"},
		{Role: "user", Content: "continue"},
	}
	m.rebuildViewportFromMessagesWithReason("session_restored")

	blocks := m.viewport.visibleBlocks()
	if len(blocks) != 2 {
		t.Fatalf("len(blocks) = %d, want 2", len(blocks))
	}
	if blocks[0].ID != 0 || blocks[0].Collapsed {
		t.Fatalf("first repeated card: ID = %d collapsed = %v, want ID 0 expanded", blocks[0].ID, blocks[0].Collapsed)
	}
	if blocks[1].ID != 1 || !blocks[1].Collapsed {
		t.Fatalf("second repeated card: ID = %d collapsed = %v, want ID 1 folded", blocks[1].ID, blocks[1].Collapsed)
	}
}

// A stream's end event can still be sitting in the event queue when a full
// rebuild captures the committed message, so the live card it replaces may
// still carry Streaming=true. If the committed content equals what that card
// already streamed, identity adoption pairs the two; copying the flag would
// then leave the rebuilt card streaming forever, because the rebuild drops the
// stream state that could have settled it.
func TestCompactionRebuildDoesNotAdoptStreamingFromLiveCard(t *testing.T) {
	backend := &sessionControlAgent{}
	m := NewModelWithSize(backend, 120, 24)
	m.viewport.AppendBlock(&Block{ID: 7, Type: BlockAssistant, Content: "the final answer", Streaming: true})
	m.nextBlockID = 8

	backend.messages = []message.Message{
		{Role: "assistant", Content: "the final answer"},
	}
	m.rebuildViewportFromMessagesWithReason("session_restored")

	blocks := m.viewport.visibleBlocks()
	if len(blocks) != 1 {
		t.Fatalf("rebuilt card count = %d, want 1", len(blocks))
	}
	if blocks[0].ID != 7 {
		t.Fatalf("rebuilt card ID = %d, want the adopted live card ID 7", blocks[0].ID)
	}
	if blocks[0].Streaming {
		t.Fatal("a card rebuilt from a committed message must not stay Streaming")
	}
}
