package tui

import (
	"testing"

	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/recovery"
)

func TestInputQueueWithdrawCommandRestoresComposer(t *testing.T) {
	backend := &sessionControlAgent{}
	m := NewModel(backend)
	m.mode = ModeInsert
	m.queuedDrafts = []queuedDraft{{ID: "draft", InputID: "input", Revision: 1, Content: "sample task", DeliveryManaged: true, DeliveryStage: recovery.PendingInputStageQueued}}
	m.input.SetValue("/queue withdraw 1")
	m.handleInputQueueCommand("/queue withdraw 1")
	for op := range m.inputQueue.pending {
		m.handleInputQueueResult(agent.InputQueueOperationResultEvent{Target: backend.InputDeliveryTarget(), OperationID: op, Result: agent.InputQueueCommitted})
	}
	if m.input.Value() != "sample task" {
		t.Fatalf("withdraw did not restore composer: %q; drafts=%+v", m.input.Value(), m.queuedDrafts)
	}
}

func TestInputQueueWithdrawPreservesNewComposerAndEditableCopy(t *testing.T) {
	backend := &sessionControlAgent{}
	m := NewModel(backend)
	m.mode = ModeInsert
	draft := queuedDraft{ID: "draft", InputID: "input", Revision: 1, Content: "sample task", DeliveryManaged: true, DeliveryStage: recovery.PendingInputStageQueued}
	op := m.nextInputQueueOperation(inputQueueAction{draft: draft, kind: "withdraw"})
	m.input.SetValue("another task")
	m.handleInputQueueResult(agent.InputQueueOperationResultEvent{Target: backend.InputDeliveryTarget(), OperationID: op, Result: agent.InputQueueCommitted})
	if m.input.Value() != "another task" || len(m.queuedDrafts) != 1 || m.queuedDrafts[0].DeliveryManaged || !m.queuedDrafts[0].Withdrawn {
		t.Fatal("withdraw lost local draft ownership")
	}
	if m.drainQueuedDrafts() != nil || len(m.queuedDrafts) != 1 {
		t.Fatal("withdrawn draft auto-sent")
	}
	m.input.Reset()
	m.mode = ModeInputQueue
	m.handleInputQueueKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if m.input.Value() != "sample task" || len(m.queuedDrafts) != 0 {
		t.Fatal("withdrawn draft cannot be edited")
	}
}
