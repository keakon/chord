package agent

import (
	"crypto/rand"
	"path/filepath"
	"sync"
	"sync/atomic"

	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
)

const (
	eventInputDeliveryOperation          = "input_delivery_operation"
	eventInputDeliveryCommitted          = "input_delivery_committed"
	eventInputDeliveryAdvance            = "input_delivery_advance"
	eventInputDeliveryEnvironmentChanged = "input_delivery_environment_changed"
	InputQueueCommitted                  = "committed"
	InputQueueRejected                   = "rejected"
	InputQueueConflict                   = "conflict"
	InputQueueUnknown                    = "unknown"
	InputQueueClear                      = "clear"
	InputQueueHoldEdit                   = "hold_edit"
	InputQueueReleaseEdit                = "release_edit"
)

type InputTarget struct {
	RuntimeID         string `json:"runtime_id"`
	SessionID         string `json:"session_id"`
	SessionEpoch      uint64 `json:"session_epoch"`
	OwnerTaskID       string `json:"owner_task_id"`
	WorkDirGeneration uint64 `json:"work_dir_generation"`
}

type SubmitInput struct {
	ExpectedViewVersion uint64
	OperationID         string
	RequestID           string
	DraftID             string
	Target              InputTarget
	DeliveryPolicy      recovery.InputDeliveryPolicy
	Content             string
	Parts               []message.ContentPart
}

type UpdateInput struct {
	ExpectedViewVersion uint64
	OperationID         string
	Target              InputTarget
	InputID             string
	ExpectedRevision    uint64
	DeliveryPolicy      recovery.InputDeliveryPolicy
	// Nil preserves the current content and attachments (policy-only update).
	Content *string
	Parts   []message.ContentPart
}

type DeleteInput struct {
	ExpectedViewVersion uint64
	OperationID         string
	Target              InputTarget
	InputID             string
	ExpectedRevision    uint64
}

type QueueControl struct {
	ExpectedViewVersion   uint64
	OperationID           string
	Target                InputTarget
	Action                string
	ExpectedQueueRevision uint64
	InputID               string
	ExpectedRevision      uint64
}

type InputQueueSnapshot struct {
	Target      InputTarget                  `json:"target"`
	ViewVersion uint64                       `json:"view_version"`
	State       *recovery.InputDeliveryState `json:"state"`
	Blocked     string                       `json:"blocked,omitempty"`
	// RestoredHeld reports that inputs restored with the session wait for the
	// user to continue or send a message before any of them starts.
	RestoredHeld bool `json:"restored_held,omitempty"`
}

func (s *InputQueueSnapshot) Clone() *InputQueueSnapshot {
	if s == nil {
		return nil
	}
	out := *s
	out.State = s.State.Clone()
	return &out
}

type InputQueueOperationResultEvent struct {
	Target        InputTarget `json:"target"`
	OperationID   string      `json:"operation_id"`
	InputID       string      `json:"input_id,omitempty"`
	ItemRevision  uint64      `json:"item_revision,omitempty"`
	QueueRevision uint64      `json:"queue_revision"`
	Result        string      `json:"result"`
	Reason        string      `json:"reason,omitempty"`
}

func (InputQueueOperationResultEvent) agentEvent() {}

type InputQueueChangedEvent struct{ Snapshot *InputQueueSnapshot }

func (InputQueueChangedEvent) agentEvent() {}

type inputDeliveryRuntime struct {
	loading          bool
	restoredHeld     bool
	environmentError string
	resumeOperations []*deliveryOperation
	navigation       bool
	navigationReady  bool
	acceptedOrders   map[string]int64
	permit           *deliveryOperation
	request          *deliveryRequest
	cancelGeneration atomic.Uint64
	once             sync.Once
	runtimeID        string
	snapshot         atomic.Pointer[InputQueueSnapshot]
	committed        *recovery.InputDeliveryState
	pending          []*deliveryOperation
	inflight         *deliveryOperation
	owner            *recoveryOwnership
	viewVersion      uint64
	persistenceError error
	currentWorkID    string
	currentOrigin    string
}

func (a *MainAgent) InputDeliveryTarget() InputTarget {
	a.delivery.once.Do(func() { a.delivery.runtimeID = rand.Text() })
	target := InputTarget{RuntimeID: a.delivery.runtimeID, OwnerTaskID: identity.MainAgentID, WorkDirGeneration: a.WorkDirSnapshot().Generation}
	if owner := a.currentRecoveryOwnership(); owner != nil {
		target.SessionID, target.SessionEpoch = filepath.Base(owner.dir), owner.epoch
	}
	return target
}

func (a *MainAgent) InputQueueSnapshot() *InputQueueSnapshot {
	return a.delivery.snapshot.Load().Clone()
}

func (a *MainAgent) SubmitUserInput(input SubmitInput) bool {
	input.Parts = cloneContentParts(input.Parts)
	return a.sendEvent(Event{Type: eventInputDeliveryOperation, Payload: &deliveryOperation{submit: &input, acceptedOrder: a.acceptedRawMessages.Add(1)}})
}
func (a *MainAgent) UpdateQueuedInput(input UpdateInput) bool {
	input.Parts = cloneContentParts(input.Parts)
	if input.Content != nil {
		input.Content = new(*input.Content)
	}
	return a.sendEvent(Event{Type: eventInputDeliveryOperation, Payload: &deliveryOperation{update: &input}})
}
func (a *MainAgent) DeleteQueuedInput(input DeleteInput) bool {
	return a.sendEvent(Event{Type: eventInputDeliveryOperation, Payload: &deliveryOperation{delete: &input}})
}
func (a *MainAgent) ControlInputQueue(input QueueControl) bool {
	return a.sendEvent(Event{Type: eventInputDeliveryOperation, Payload: &deliveryOperation{control: &input}})
}
