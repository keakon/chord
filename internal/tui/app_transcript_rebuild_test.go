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
