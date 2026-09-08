package agent

import "strings"

const (
	agentMessageSubtypeTaskCompletion = "task_completion"
	agentMessageSubtypeTaskFailure    = "task_failure"
	agentMessageSubtypeWaitingExpiry  = "waiting_main_expiry"
)

func terminalMailboxOutcome(msg SubAgentMailboxMessage) SubAgentState {
	switch msg.Kind {
	case SubAgentMailboxKindCompleted:
		return SubAgentStateCompleted
	case SubAgentMailboxKindRiskAlert:
		switch msg.Subtype {
		case agentMessageSubtypeTaskFailure:
			return SubAgentStateFailed
		case agentMessageSubtypeWaitingExpiry:
			return SubAgentStateCancelled
		}
	}
	return ""
}

func terminalMailboxMatchesSettlement(msg SubAgentMailboxMessage, settlement *TaskSettlement) bool {
	outcome := terminalMailboxOutcome(msg)
	if outcome == "" || settlement == nil ||
		strings.TrimSpace(msg.TaskID) != settlement.TaskID ||
		msg.Attempt != settlement.Attempt || string(outcome) != settlement.Outcome {
		return false
	}
	// Cancellation also covers explicit stops. A prepared expiry alert is
	// valid only when that wait actually won the guarded terminal commit.
	if msg.Subtype == agentMessageSubtypeWaitingExpiry {
		return strings.HasPrefix(settlement.Summary, waitingMainExpiryClosedReasonPrefix) &&
			strings.TrimSpace(msg.Summary) == settlement.Summary
	}
	return true
}
