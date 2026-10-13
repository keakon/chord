package agent

import (
	"strings"
	"unicode"
)

const awaitingOwnerReasonMaxRunes = 256

// SubAgentDisplayChangedEvent invalidates the in-process sidebar snapshot.
// It carries no task facts and is safe to consume after a session switch.
type SubAgentDisplayChangedEvent struct{}

func (SubAgentDisplayChangedEvent) agentEvent() {}

// pendingOwnerReasonsLocked requires subs.mu to be held by the caller.
func (a *MainAgent) pendingOwnerReasonsLocked() map[taskAttemptKey]string {
	reasons := make(map[taskAttemptKey]string)
	selected := make(map[taskAttemptKey]*DurableAgentRequest)
	for _, request := range a.subs.agentRequests {
		if request == nil || request.State != agentRequestStatePending {
			continue
		}
		task := a.subs.taskRecords[request.SourceTaskID]
		if task == nil || request.SourceAttempt == 0 || request.SourceAttempt != task.Attempt || request.RequestMessageID == "" || request.RequestMessageID != task.EscalationMailboxID || request.TargetTaskID != task.OwnerTaskID {
			continue
		}
		if task.OwnerTaskID == "" {
			if request.TargetAttempt != 0 {
				continue
			}
		} else {
			owner := a.subs.taskRecords[task.OwnerTaskID]
			if owner == nil || owner.Attempt != request.TargetAttempt || !isNonTerminalTaskState(owner.State) {
				continue
			}
		}
		if strings.TrimSpace(request.Message) == "" {
			continue
		}
		key := taskAttemptKey{TaskID: task.TaskID, Attempt: task.Attempt}
		if prev := selected[key]; prev != nil && (prev.CreatedAt.After(request.CreatedAt) || (prev.CreatedAt.Equal(request.CreatedAt) && prev.CorrelationID >= request.CorrelationID)) {
			continue
		}
		selected[key] = request
	}
	for key, request := range selected {
		reasons[key] = compactOwnerReason(request.Message)
	}
	return reasons
}

// compactOwnerReason folds whitespace without allocating or decoding the
// entire request once the display limit has been reached.
func compactOwnerReason(message string) string {
	var runes [awaitingOwnerReasonMaxRunes + 1]rune
	n := 0
	space := false
	for _, r := range message {
		if unicode.IsSpace(r) {
			space = n > 0
			continue
		}
		if space {
			runes[n] = ' '
			n++
			space = false
			if n == len(runes) {
				break
			}
		}
		runes[n] = r
		n++
		if n == len(runes) {
			break
		}
	}
	if n > awaitingOwnerReasonMaxRunes {
		n = awaitingOwnerReasonMaxRunes
		runes[n-1] = '…'
	}
	return string(runes[:n])
}

func applyAwaitingOwnerReason(info *SubAgentInfo, record *DurableTaskRecord, reasons map[taskAttemptKey]string) {
	info.AwaitingOwnerReason = ""
	if record == nil || info.State != string(SubAgentStateWaitingMain) || info.TaskID != record.TaskID {
		return
	}
	info.AwaitingOwnerReason = reasons[taskAttemptKey{TaskID: info.TaskID, Attempt: record.Attempt}]
}
