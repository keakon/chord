package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
)

func TestInputQueueSnapshotDoesNotCreateCardsOrFallback(t *testing.T) {
	backend := &sessionControlAgent{}
	m := NewModel(backend)
	snapshot := &agent.InputQueueSnapshot{Target: backend.InputDeliveryTarget(), ViewVersion: 1, State: &recovery.InputDeliveryState{
		Works:  []recovery.DeliveryWork{{WorkID: "work", Origin: recovery.DeliveryWorkUser, Outcome: recovery.DeliveryWorkFailed}},
		Inputs: []recovery.PendingInput{{InputID: "input", DraftID: "draft", Revision: 1, Stage: recovery.PendingInputStageQueued, DeliveryPolicy: recovery.InputDeliveryAfterCompletion, WaitWorkID: "work", Content: "sample"}}}}
	m.applyInputQueueSnapshot(snapshot)
	if len(m.queuedDrafts) != 1 || m.queuedDraftsAutoContinue() {
		t.Fatal("waiting projection is runnable or missing")
	}
	m.queueSyncEnabled = false
	if cmd := m.drainQueuedDrafts(); cmd != nil || len(m.queuedDrafts) != 1 {
		t.Fatal("managed queue fell back to local resend")
	}
	snapshot.ViewVersion = 2
	snapshot.Blocked = "storage error"
	m.applyInputQueueSnapshot(snapshot)
	if m.inputQueue.snapshot.Blocked == "" {
		t.Fatal("same revision discarded storage failure")
	}
}
func TestInputQueueConsumedMatchesInputIdentity(t *testing.T) {
	backend := &sessionControlAgent{messages: []message.Message{{Role: message.RoleUser, InputID: "first", Content: "same text"}, {Role: message.RoleUser, InputID: "second", Content: "same text"}}}
	m := NewModel(backend)
	for _, id := range []string{"first", "second", "first"} {
		m.handleTurnAgentEvent(agent.PendingDraftConsumedEvent{Target: backend.InputDeliveryTarget(), InputID: id, Revision: 3, Parts: []message.ContentPart{{Type: message.ContentPartText, Text: "same text"}}})
	}
	// The helper resolves both identities to different durable indexes, and a
	// repeated consumed event adopts the existing card at that index.
	if m.mainUserBlockForMsgIndex(backend.messages, 0) == nil || m.mainUserBlockForMsgIndex(backend.messages, 1) == nil {
		t.Fatal("identical text merged different input identities")
	}
}
func TestInputQueueLateHoldPreservesComposer(t *testing.T) {
	backend := &sessionControlAgent{}
	m := NewModel(backend)
	op := m.nextInputQueueOperation(inputQueueAction{kind: "hold", draft: queuedDraft{ID: "draft", InputID: "input", Content: "old text"}})
	m.input.SetValue("new draft")
	m.handleInputQueueResult(agent.InputQueueOperationResultEvent{Target: backend.InputDeliveryTarget(), OperationID: op, Result: agent.InputQueueCommitted, ItemRevision: 2})
	if m.input.Value() != "new draft" || m.inputQueue.editing != nil {
		t.Fatal("late hold replaced or rebound new draft")
	}
}

func TestInputQueueAfterCompletionRejectsLocalSideEffects(t *testing.T) {
	for _, text := range []string{"/history", "/context", "/rules", "/memory organize", "/new", "/skill sample", "/stats", "!echo sample"} {
		t.Run(text, func(t *testing.T) {
			backend := &sessionControlAgent{}
			m := NewModel(backend)
			m.mode = ModeInsert
			m.input.SetValue(text)
			before := m.input.Value()
			m.handleKeyMsg(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter, Mod: tea.ModAlt}))
			if m.input.Value() != before || m.mode != ModeInsert || len(m.inputQueue.pending) != 0 {
				t.Fatal("unsupported input changed draft or ran a local action")
			}
		})
	}
}

type inputQueueEditAgent struct {
	sessionControlAgent
	updates []agent.UpdateInput
}

func (a *inputQueueEditAgent) UpdateQueuedInput(input agent.UpdateInput) bool {
	a.updates = append(a.updates, input)
	return true
}

func TestInputQueueOldReceiptPreservesNewEditingSession(t *testing.T) {
	for _, kind := range []string{"submit", "release"} {
		for _, sameItem := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/same=%v", kind, sameItem), func(t *testing.T) {
				backend := &inputQueueEditAgent{}
				m := NewModel(backend)
				first := queuedDraft{ID: "draft-a", InputID: "input-a", Revision: 2, Content: "first", DeliveryManaged: true, EditHeld: true, DeliveryStage: recovery.PendingInputStageQueued}
				m.editManagedDraft(first)
				m.input.Reset()
				var op string
				if kind == "submit" {
					m.submitManagedDraft(first)
					for id := range m.inputQueue.pending {
						op = id
					}
				} else {
					op = m.nextInputQueueOperation(inputQueueAction{kind: kind, draft: first})
					m.editingQueuedDraftID = ""
				}
				newer := first
				if !sameItem {
					newer.ID = "draft-b"
					newer.InputID = "input-b"
				}
				newer.Content = "second"
				m.editManagedDraft(newer)
				editing := m.inputQueue.editing
				m.handleInputQueueResult(agent.InputQueueOperationResultEvent{Target: backend.InputDeliveryTarget(), OperationID: op, Result: agent.InputQueueCommitted})
				if m.inputQueue.editing != editing || m.editingQueuedDraftID != newer.ID || m.input.Value() != "second" {
					t.Fatalf("old receipt changed edit: current=%p expected=%p id=%q expectedID=%q text=%q", m.inputQueue.editing, editing, m.editingQueuedDraftID, newer.ID, m.input.Value())
				}
				m.input.Reset()
				m.submitManagedDraft(newer)
				if len(backend.updates) == 0 {
					t.Fatal("new edit was submitted as a new input")
				}
				update := backend.updates[len(backend.updates)-1]
				if update.InputID != newer.InputID || update.ExpectedRevision != newer.Revision {
					t.Fatalf("edit target = %+v", update)
				}

			})
		}
	}
}

func TestInputQueueSubmissionDetachesAndRejectedEditRestores(t *testing.T) {
	backend := &sessionControlAgent{}
	m := NewModel(backend)
	draft := queuedDraft{ID: "draft", InputID: "input", Revision: 2, Content: "sample", DeliveryManaged: true, EditHeld: true, DeliveryStage: recovery.PendingInputStageQueued}
	m.editManagedDraft(draft)
	editing := m.inputQueue.editing
	m.input.Reset()
	m.submitManagedDraft(draft)
	if m.inputQueue.editing != nil || m.editingQueuedDraftID != "" {
		t.Fatal("submitted edit still owns composer")
	}
	var op string
	for id := range m.inputQueue.pending {
		op = id
	}
	m.handleInputQueueResult(agent.InputQueueOperationResultEvent{Target: backend.InputDeliveryTarget(), OperationID: op, Result: agent.InputQueueRejected, Reason: "not saved"})
	if m.inputQueue.editing != editing || m.editingQueuedDraftID != draft.ID || m.input.Value() != draft.Content {
		t.Fatal("rejected edit did not restore its binding")
	}
}

func TestInputQueueReleaseClearsOnlyCurrentEdit(t *testing.T) {
	backend := &sessionControlAgent{}
	m := NewModel(backend)
	draft := queuedDraft{ID: "draft", InputID: "input", Content: "sample", DeliveryManaged: true, EditHeld: true, DeliveryStage: recovery.PendingInputStageQueued}
	m.editManagedDraft(draft)
	op := m.nextInputQueueOperation(inputQueueAction{kind: "release", draft: draft})
	m.handleInputQueueResult(agent.InputQueueOperationResultEvent{Target: backend.InputDeliveryTarget(), OperationID: op, Result: agent.InputQueueCommitted})
	if m.inputQueue.editing != nil || m.editingQueuedDraftID != "" {
		t.Fatal("release did not clear its own edit")
	}
}

func TestInputQueueStartedSnapshotDoesNotRestoreConsumedDraft(t *testing.T) {
	backend := &sessionControlAgent{messages: []message.Message{{Role: message.RoleUser, InputID: "input", Content: "sample"}}}
	m := NewModel(backend)
	snapshot := &agent.InputQueueSnapshot{Target: backend.InputDeliveryTarget(), State: &recovery.InputDeliveryState{Inputs: []recovery.PendingInput{{InputID: "input", DraftID: "draft", Revision: 1, DeliveryPolicy: recovery.InputDeliveryNextRequest, Content: "sample"}}}}
	for i, stage := range []string{recovery.PendingInputStageQueued, recovery.PendingInputStageClaimed} {
		snapshot.ViewVersion = uint64(i + 1)
		snapshot.State.Inputs[0].Stage = stage
		m.applyInputQueueSnapshot(snapshot)
		if len(m.visibleQueuedDrafts()) != 1 {
			t.Fatalf("stage %s: reservation disappeared", stage)
		}
	}
	m.handleTurnAgentEvent(agent.PendingDraftConsumedEvent{Target: backend.InputDeliveryTarget(), InputID: "input", DraftID: "draft", Revision: 3, Parts: []message.ContentPart{{Type: message.ContentPartText, Text: "sample"}}})
	if len(m.visibleQueuedDrafts()) != 0 {
		t.Fatal("consumed draft remains visible")
	}
	snapshot.ViewVersion++
	snapshot.State.Inputs[0].Stage = recovery.PendingInputStageStarted
	m.applyInputQueueSnapshot(snapshot)
	if len(m.visibleQueuedDrafts()) != 0 {
		t.Fatal("started snapshot restored consumed draft")
	}
	if m.mainUserBlockForMsgIndex(backend.messages, 0) == nil {
		t.Fatal("consumed message card missing")
	}
	if len(m.inputQueue.snapshot.State.Inputs) != 1 {
		t.Fatal("recovery state was discarded")
	}
	restored := NewModel(backend)
	restored.applyInputQueueSnapshot(snapshot)
	if len(restored.visibleQueuedDrafts()) != 0 {
		t.Fatal("restored started input appears as a draft")
	}
}

func TestAfterCompletionWaitNoticeOnlyForSettledWithoutSuccess(t *testing.T) {
	input := recovery.PendingInput{WaitWorkID: "work"}
	type want struct {
		waiting bool
		notice  string
	}
	for outcome, expected := range map[string]want{
		recovery.DeliveryWorkActive:    {true, ""},
		recovery.DeliveryWorkCompleted: {false, ""},
		recovery.DeliveryWorkFailed:    {true, "last task failed; send a message to run it next"},
		recovery.DeliveryWorkCancelled: {true, "last task cancelled; send a message to run it next"},
		recovery.DeliveryWorkUnknown:   {true, "last task outcome unknown; send a message to run it next"},
	} {
		state := &recovery.InputDeliveryState{Works: []recovery.DeliveryWork{{WorkID: "work", Outcome: outcome}}}
		waiting, notice := afterCompletionWait(state, input)
		if waiting != expected.waiting || notice != expected.notice {
			t.Errorf("outcome %q: waiting=%v notice=%q, want waiting=%v notice=%q", outcome, waiting, notice, expected.waiting, expected.notice)
		}
	}
}

func TestInputQueueAfterCompletionActiveWaitShowsOnlyPolicy(t *testing.T) {
	backend := &sessionControlAgent{}
	m := NewModel(backend)
	snapshot := &agent.InputQueueSnapshot{Target: backend.InputDeliveryTarget(), ViewVersion: 1, State: &recovery.InputDeliveryState{
		Works:  []recovery.DeliveryWork{{WorkID: "work", Origin: recovery.DeliveryWorkUser, Outcome: recovery.DeliveryWorkActive}},
		Inputs: []recovery.PendingInput{{InputID: "input", DraftID: "draft", Revision: 1, Stage: recovery.PendingInputStageQueued, DeliveryPolicy: recovery.InputDeliveryAfterCompletion, WaitWorkID: "work", Content: "sample"}}}}
	m.applyInputQueueSnapshot(snapshot)
	if len(m.queuedDrafts) != 1 {
		t.Fatalf("queued drafts = %+v", m.queuedDrafts)
	}
	if got := m.queuedDrafts[0]; got.DeliveryStatus != "After completion" || got.DeliveryRunnable {
		t.Fatalf("active wait status = %q runnable=%v, want plain policy and not runnable", got.DeliveryStatus, got.DeliveryRunnable)
	}
	snapshot.ViewVersion++
	snapshot.State.Works[0].Outcome = recovery.DeliveryWorkFailed
	m.applyInputQueueSnapshot(snapshot)
	if got := m.queuedDrafts[0].DeliveryStatus; !strings.HasPrefix(got, "After completion · last task failed") {
		t.Fatalf("failed wait status = %q, want the remedy notice", got)
	}
}

func TestInputQueueStatusInvalidatesRenderCache(t *testing.T) {
	backend := &sessionControlAgent{}
	m := NewModel(backend)
	snapshot := &agent.InputQueueSnapshot{Target: backend.InputDeliveryTarget(), ViewVersion: 1, State: &recovery.InputDeliveryState{Inputs: []recovery.PendingInput{{InputID: "input", DraftID: "draft", Revision: 1, Stage: recovery.PendingInputStageQueued, DeliveryPolicy: recovery.InputDeliveryNextRequest, Content: "sample"}}}}
	m.applyInputQueueSnapshot(snapshot)
	for _, change := range []func(){
		func() { snapshot.State.Inputs[0].DeliveryPolicy = recovery.InputDeliveryAfterCompletion },
		func() {
			snapshot.State.Inputs[0].WaitWorkID = "work"
			snapshot.State.Works = []recovery.DeliveryWork{{WorkID: "work", Origin: recovery.DeliveryWorkUser, Outcome: recovery.DeliveryWorkFailed}}
		},
		func() { snapshot.Blocked = "storage unavailable" },
		func() { snapshot.State.EditHolds = []string{"input"} },
		func() {
			snapshot.State.EditHolds = nil
			snapshot.State.Inputs[0].Stage = recovery.PendingInputStageClaimed
		},
	} {
		before := m.queuedDraftsFingerprint(80, 3)
		change()
		snapshot.ViewVersion++
		m.applyInputQueueSnapshot(snapshot)
		if after := m.queuedDraftsFingerprint(80, 3); after == before {
			t.Fatalf("status change kept stale cache: %s", m.queuedDrafts[0].DeliveryStatus)
		}
	}
}

func TestInputQueueEditResubmitFollowsSubmitKey(t *testing.T) {
	for _, tc := range []struct {
		name   string
		row    recovery.InputDeliveryPolicy
		alt    bool
		policy recovery.InputDeliveryPolicy
	}{
		{name: "enter on after completion", row: recovery.InputDeliveryAfterCompletion, policy: recovery.InputDeliveryNextRequest},
		{name: "alt+enter on next request", row: recovery.InputDeliveryNextRequest, alt: true, policy: recovery.InputDeliveryAfterCompletion},
	} {
		t.Run(tc.name, func(t *testing.T) {
			backend := &inputQueueEditAgent{}
			m := NewModel(backend)
			draft := queuedDraft{ID: "draft", InputID: "input", Revision: 1, Content: "sample", DeliveryManaged: true, EditHeld: true, DeliveryStage: recovery.PendingInputStageQueued, DeliveryPolicy: tc.row}
			m.editManagedDraft(draft)
			m.input.SetValue("edited")
			mod := tea.KeyMod(0)
			if tc.alt {
				mod = tea.ModAlt
			}
			m.handleKeyMsg(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter, Mod: mod}))
			if len(backend.updates) != 1 {
				t.Fatalf("edit resubmit updates = %+v", backend.updates)
			}
			if got := backend.updates[0].DeliveryPolicy; got != tc.policy {
				t.Fatalf("resubmit policy = %q, want %q", got, tc.policy)
			}
		})
	}
}

func TestInputQueueEditResubmitUsesSubmitKeyPolicy(t *testing.T) {
	backend := &inputQueueEditAgent{}
	m := NewModel(backend)
	draft := queuedDraft{ID: "draft", InputID: "input", Revision: 2, Content: "sample", DeliveryManaged: true, EditHeld: true, DeliveryStage: recovery.PendingInputStageQueued, DeliveryPolicy: recovery.InputDeliveryAfterCompletion}
	m.editManagedDraft(draft)
	m.submitManagedDraft(queuedDraft{ID: "draft", InputID: "input", Content: "edited", DeliveryPolicy: recovery.InputDeliveryNextRequest})
	if len(backend.updates) != 1 {
		t.Fatal("edit was not submitted as an update")
	}
	if backend.updates[0].DeliveryPolicy != recovery.InputDeliveryNextRequest {
		t.Fatalf("Enter on an after-completion edit kept its policy: %+v", backend.updates[0].DeliveryPolicy)
	}

	m.input.Reset()
	backend.updates = nil
	m.editManagedDraft(queuedDraft{ID: "draft-2", InputID: "input-2", Revision: 1, Content: "other", DeliveryManaged: true, EditHeld: true, DeliveryStage: recovery.PendingInputStageQueued, DeliveryPolicy: recovery.InputDeliveryNextRequest})
	m.submitManagedDraft(queuedDraft{ID: "draft-2", InputID: "input-2", Content: "edited", DeliveryPolicy: recovery.InputDeliveryAfterCompletion})
	if len(backend.updates) != 1 || backend.updates[0].DeliveryPolicy != recovery.InputDeliveryAfterCompletion {
		t.Fatalf("Alt+Enter on a next-request edit kept the row policy: %+v", backend.updates)
	}
}
