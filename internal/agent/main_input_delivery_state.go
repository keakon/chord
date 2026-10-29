package agent

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
)

// selectDeliveryInputs returns a stable selection without mutating committed
// state. A held input blocks later inputs of the same policy class, and
// after-completion inputs fire one per idle in sequence order — each only once
// the work it was queued behind has settled successfully (or nothing was
// running when it was queued).
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
	if len(ordinary) > 0 || !allowFollowup {
		return ordinary
	}
	for _, input := range inputs {
		if input.Stage != recovery.PendingInputStageQueued || input.DeliveryPolicy != recovery.InputDeliveryAfterCompletion {
			continue
		}
		if slices.Contains(state.EditHolds, input.InputID) {
			return nil
		}
		if !afterCompletionSettled(state, input) {
			continue
		}
		return []string{input.InputID}
	}
	return nil
}

func deliveryHasQueuedInput(state *recovery.InputDeliveryState) bool {
	if state == nil {
		return false
	}
	for _, input := range state.Inputs {
		if input.Stage == recovery.PendingInputStageQueued {
			return true
		}
	}
	return false
}

// runnableDeliveryInputs is selectDeliveryInputs behind the restore hold: a
// restored session is idle until the user continues or sends a message, so
// queued inputs it carried never start a turn or join a request on their own.
func (a *MainAgent) runnableDeliveryInputs(allowFollowup bool) []string {
	if a.delivery.restoredHeld {
		return nil
	}
	return selectDeliveryInputs(a.delivery.committed, allowFollowup)
}

// releaseRestoredHold records the user's explicit re-engagement after a
// restore. Only the user's own message, submission or continue calls it.
func (a *MainAgent) releaseRestoredHold() {
	if !a.delivery.restoredHeld {
		return
	}
	a.delivery.restoredHeld = false
	a.publishInputDelivery()
}

// afterCompletionSettled reports whether a queued after-completion input may
// start: either nothing was running when it was queued, or the work it waits
// behind has settled successfully. A failed, cancelled or unknown wait is not
// sticky — newTurn re-points the wait at whatever runs next, so the input
// rides the first successful settle after the user re-engages.
func afterCompletionSettled(state *recovery.InputDeliveryState, input recovery.PendingInput) bool {
	switch state.AfterCompletionWait(input) {
	case "", recovery.DeliveryWorkCompleted:
		return true
	}
	return false
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

// commitNextRequestInputsToTranscriptUntil writes queued next-request inputs as
// durable user messages at shutdown. A next-request input is the one the user
// expects to go out on the next send, so leaving it only in the queue file
// makes it invisible after a restart: the transcript is where the user looks
// for what they already wrote. The queue rows are then retired so the restored
// session shows the request once, in history, instead of twice.
//
// Transcript writes happen before queue retirement. A crash between the two
// therefore leaves a queued row whose InputID is already in the transcript,
// which restore retires, instead of losing a row whose queue record is gone.
func (a *MainAgent) commitNextRequestInputsToTranscriptUntil(persistDeadline <-chan time.Time) {
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

	// Barrier first: the direct durable appends below must land after every
	// message the persistence pump accepted before shutdown.
	if a.persist.ch != nil && !a.persist.flushUntil(persistDeadline) {
		log.Warnf("cannot commit queued next-request inputs to history: persistence barrier unavailable")
		return
	}

	type committedInput struct {
		user      message.Message
		skillName string
		skillPair []message.Message
	}
	committed := make([]committedInput, 0, len(pending))
	for _, input := range pending {
		userMsg := message.Message{Role: message.RoleUser, InputID: input.InputID, InputRequestID: input.RequestID, Content: input.Content, Parts: cloneContentParts(input.Parts)}
		if err := owner.manager.PersistMessageDurable(identity.MainAgentID, userMsg); err != nil {
			log.Warnf("failed to persist committed next-request input error=%v", err)
			return
		}
		skillName, skillPair := a.buildUserSkillInvocation(userMsg)
		for _, msg := range skillPair {
			if err := owner.manager.PersistMessageDurable(identity.MainAgentID, msg); err != nil {
				log.Warnf("failed to persist committed next-request skill invocation error=%v", err)
				skillName, skillPair = "", nil
				break
			}
		}
		committed = append(committed, committedInput{user: userMsg, skillName: skillName, skillPair: skillPair})
	}

	committedState := state.Clone()
	for _, input := range pending {
		committedState.Inputs = slices.DeleteFunc(committedState.Inputs, func(candidate recovery.PendingInput) bool { return candidate.InputID == input.InputID })
	}
	committedState.Revision++
	saved, err := owner.manager.SaveInputDelivery(state.Revision, committedState)
	if err != nil {
		// The transcript rows are already durable. Restore retires the queued
		// rows by InputID, so they cannot execute a second time.
		log.Warnf("failed to retire committed next-request inputs from the durable queue error=%v", err)
		return
	}
	a.delivery.committed = saved
	for _, input := range committed {
		a.recordCommittedUserMessageInMemory(input.user)
		if len(input.skillPair) > 0 {
			a.applyUserSkillPairInMemory(input.skillName, input.skillPair)
		}
	}
	log.Infof("committed %d queued next-request input(s) to session history at shutdown", len(committed))
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
	return len(a.runnableDeliveryInputs(true)) > 0
}
