package agent

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
)

// selectDeliveryInputs returns a stable selection without mutating committed
// state. A held ordinary input blocks later ordinary inputs and all followups.
func selectDeliveryInputs(state *recovery.InputDeliveryState, allowFollowup bool) []string {
	if state == nil {
		return nil
	}
	inputs := append([]recovery.PendingInput(nil), state.Inputs...)
	sort.Slice(inputs, func(i, j int) bool { return inputs[i].Sequence < inputs[j].Sequence })
	var ordinary []string
	for _, input := range inputs {
		if input.Stage != recovery.PendingInputStageQueued || input.DeliveryPolicy != recovery.InputDeliveryNextRequest {
			continue
		}
		if slices.Contains(state.EditHolds, input.InputID) {
			return ordinary
		}
		ordinary = append(ordinary, input.InputID)
	}
	if len(ordinary) > 0 {
		return ordinary
	}
	if !allowFollowup || state.Gate.Mode != recovery.FollowupGateRunning {
		return nil
	}
	if state.Gate.WaitingWorkID != "" {
		completed := false
		for _, work := range state.Works {
			if work.WorkID == state.Gate.WaitingWorkID {
				completed = work.Outcome == recovery.DeliveryWorkCompleted
			}
		}
		if !completed {
			return nil
		}
	}
	for _, input := range inputs {
		if input.Stage != recovery.PendingInputStageQueued || input.DeliveryPolicy != recovery.InputDeliveryAfterCompletion {
			continue
		}
		if slices.Contains(state.EditHolds, input.InputID) {
			return nil
		}
		return []string{input.InputID}
	}
	return nil
}

// normalizeFollowupGate clears a pause that no longer withholds anything. A
// paused gate exists to keep after-completion inputs from starting (and to
// leave edit-held ones alone), so once neither is queued the pause is dead
// state: it would keep the queue reporting itself paused and make the next
// follow-up wait for a resume that releases nothing. Explicit user pauses and
// session-level pauses record intent outside the queue and are left alone.
//
// A working-directory pause is left alone for the opposite reason: it reports
// a mismatch that only /queue resume with the original directory active can
// acknowledge, and the waiter test below cannot see it — the input it holds
// back may be an ordinary one, which bypasses the gate — so normalizing it
// here would only be undone by the next claim.
func normalizeFollowupGate(state *recovery.InputDeliveryState) {
	if state == nil || state.Gate.Mode != recovery.FollowupGatePaused {
		return
	}
	switch state.Gate.Reason {
	case recovery.FollowupPredecessorFailed, recovery.FollowupPredecessorCancelled, recovery.FollowupOutcomeUnknown, recovery.FollowupEditHold:
	default:
		return
	}
	if len(state.EditHolds) > 0 {
		return
	}
	for _, input := range state.Inputs {
		if input.Stage == recovery.PendingInputStageQueued && input.DeliveryPolicy == recovery.InputDeliveryAfterCompletion {
			return
		}
	}
	state.Gate = recovery.FollowupGate{Mode: recovery.FollowupGateRunning}
}

func deliveryInputForEdit(state *recovery.InputDeliveryState, id string, revision uint64) (*recovery.PendingInput, error) {
	if state != nil {
		for i := range state.Inputs {
			input := &state.Inputs[i]
			if input.InputID == id {
				if revision == 0 || input.Revision != revision || input.Stage != recovery.PendingInputStageQueued {
					return nil, recovery.ErrInputDeliveryConflict
				}
				return input, nil
			}
		}
	}
	return nil, fmt.Errorf("queued input %q no longer exists: %w", id, recovery.ErrInputDeliveryConflict)
}

func (a *MainAgent) managedDeliveryInputCount() int {
	if a.delivery.committed == nil {
		return 0
	}
	return len(a.delivery.committed.Inputs)
}

// IsUnsupportedAfterCompletionInput is shared by runtime and composer so local
// commands cannot run as a side effect of choosing after-completion delivery.
func IsUnsupportedAfterCompletionInput(content string) bool {
	content = strings.TrimSpace(content)
	if strings.HasPrefix(content, "!") || IsTUILocalOnlySlashCommand(content) || IsIdleOnlySlashCommand(content) {
		return true
	}
	fields := strings.Fields(content)
	if len(fields) == 0 {
		return false
	}
	switch strings.ToLower(fields[0]) {
	case "/skill", "/help", "/stats", "/status", "/rules", "/memory", "/takeover", "/history", "/context":
		return true
	}
	return false
}

// commitNextRequestInputsToTranscript writes queued next-request inputs as
// durable user messages at shutdown. A next-request input is the one the user
// expects to go out on the next send, so leaving it only in the queue file
// makes it invisible after a restart: the transcript is where the user looks
// for what they already wrote. The queue rows are then retired so the restored
// session shows the request once, in history, instead of twice.
//
// The queue is retired before the messages are written, the same order
// claimDeliveryInputs uses: the durable queue row must be gone before the
// transcript row exists. Retiring after the write would leave a failed save
// with the request in both places, and restore does not drop a queued row whose
// input already reached the transcript, so the next send would run it twice.
func (a *MainAgent) commitNextRequestInputsToTranscript() {
	state := a.delivery.committed
	if state == nil {
		return
	}
	var pending []recovery.PendingInput
	for _, input := range state.Inputs {
		if input.Stage != recovery.PendingInputStageQueued || input.DeliveryPolicy != recovery.InputDeliveryNextRequest {
			continue
		}
		if slices.Contains(state.EditHolds, input.InputID) {
			// An edit hold means the input is mid-edit in another session; writing
			// a half-finished draft into history would surface a request the user
			// never approved.
			continue
		}
		pending = append(pending, input)
	}
	if len(pending) == 0 {
		return
	}
	sort.Slice(pending, func(i, j int) bool { return pending[i].Sequence < pending[j].Sequence })
	owner := a.currentRecoveryOwnership()
	if owner == nil || owner.manager == nil {
		log.Warnf("cannot commit queued next-request inputs to history: no recovery manager")
		return
	}
	committed := state.Clone()
	for _, input := range pending {
		committed.Inputs = slices.DeleteFunc(committed.Inputs, func(candidate recovery.PendingInput) bool { return candidate.InputID == input.InputID })
	}
	committed.Revision++
	saved, err := owner.manager.SaveInputDelivery(state.Revision, committed)
	if err != nil {
		log.Warnf("failed to retire committed next-request inputs from the durable queue error=%v", err)
		return
	}
	a.delivery.committed = saved
	for _, input := range pending {
		a.recordCommittedUserMessage(message.Message{Role: message.RoleUser, InputID: input.InputID, InputRequestID: input.RequestID, Content: input.Content, Parts: cloneContentParts(input.Parts)})
	}
	log.Infof("committed %d queued next-request input(s) to session history at shutdown", len(pending))
}

// A queued request must not produce the aggregate idle that ACP uses to settle
// its prompt while that request is still waiting for the persistence barrier.
func (a *MainAgent) deliveryPreventsGlobalIdle() bool {
	if a.delivery.inflight != nil || len(a.delivery.pending) > 0 || len(a.delivery.resumeOperations) > 0 {
		return true
	}
	if a.delivery.persistenceError != nil || a.delivery.environmentError != "" || a.pendingUserDrainSuspended {
		return false
	}
	ids := selectDeliveryInputs(a.delivery.committed, true)
	if len(ids) == 0 {
		return false
	}
	if a.delivery.restoredNextInputsBlocked {
		for _, input := range a.delivery.committed.Inputs {
			if slices.Contains(ids, input.InputID) && input.DeliveryPolicy == recovery.InputDeliveryNextRequest {
				return false
			}
		}
	}
	return true
}
