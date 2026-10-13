package agent

import (
	"fmt"
	"slices"
	"sort"
	"strings"

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
