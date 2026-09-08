package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/keakon/chord/internal/tools"
)

func (s *SubAgent) recordVerificationToolResult(result *toolResult, contextResult string, isError bool) {
	if s == nil || result == nil || tools.NormalizeName(result.Name) != tools.NameShell {
		return
	}
	shell, err := decodeShellCallArguments(json.RawMessage(result.ArgsJSON))
	if err != nil || shell.Command == "" {
		return
	}
	command := shell.Command
	status := "passed"
	if isError {
		status = "failed"
	}
	if errors.Is(result.Error, context.Canceled) || errors.Is(result.Error, context.DeadlineExceeded) {
		status = "cancelled"
	}
	summary := strings.TrimSpace(contextResult)
	if len([]rune(summary)) > 512 {
		summary = string([]rune(summary)[:512]) + "…"
	}
	s.verificationLedger = append(s.verificationLedger, verificationLedgerEntry{ToolCallID: result.CallID, Command: command, Status: status, Summary: summary, MutationEpoch: s.workspaceMutationEpoch})
	if len(s.verificationLedger) > maxVerificationLedgerEntries {
		s.verificationLedger = s.verificationLedger[len(s.verificationLedger)-maxVerificationLedgerEntries:]
	}
}

func (s *SubAgent) validateCompletionVerification(env *CompletionEnvelope) error {
	if env == nil || len(env.VerificationRun) == 0 {
		return nil
	}
	records := make([]VerificationRecord, 0, len(env.VerificationRun))
	var latestCoveredEpoch uint64
	for _, declared := range env.VerificationRun {
		command := strings.TrimSpace(declared)
		var found *verificationLedgerEntry
		for i := len(s.verificationLedger) - 1; i >= 0; i-- {
			entry := &s.verificationLedger[i]
			if entry.Command != command {
				continue
			}
			if entry.Status != "passed" {
				return fmt.Errorf("verification command %q was finalized with status %q; run it successfully before Complete", command, entry.Status)
			}
			found = entry
			break
		}
		if found == nil {
			return fmt.Errorf("verification command %q was not found among finalized Shell calls; run it again before Complete", command)
		}
		records = append(records, VerificationRecord{ToolCallID: found.ToolCallID, Command: found.Command, Status: found.Status, Summary: found.Summary})
		if found.MutationEpoch > latestCoveredEpoch {
			latestCoveredEpoch = found.MutationEpoch
		}
	}
	// Every non-read-only tool call advances the epoch, including commands
	// that never touch a file (lint, build), so requiring the declared commands
	// to cover every epoch in between would punish honest declarations: after
	// modify → lint → build → test, declaring [lint, test] must be valid. The
	// newest declared verification is the freshness barrier: requiring every
	// declaration to share its epoch would make any multi-command verification
	// impossible because each production Shell call advances the epoch itself.
	if latestCoveredEpoch < s.workspaceMutationEpoch {
		return fmt.Errorf("the workspace mutation epoch advanced after the declared verification commands last ran; re-run them before Complete")
	}
	env.VerificationRecords = records
	return nil
}

// completionRecoveryBudgetAvailable consumes the single bounded follow-up for a
// rejected Complete call (invalid arguments or failed verification). It returns
// true when the follow-up request may proceed; once spent, any later rejection
// in the same turn fails the agent instead. The budget is deliberately separate
// from SubAgentTerminalRecoveryCount (the pure-text wrap-up nudge), so a
// text-only reply that already used its nudge still leaves the model one chance
// to repair a rejected Complete, and vice versa.
func (s *SubAgent) completionRecoveryBudgetAvailable() bool {
	if s == nil || s.turn == nil {
		return false
	}
	if s.turn.SubAgentCompletionRecoveryCount >= 1 {
		return false
	}
	s.turn.SubAgentCompletionRecoveryCount++
	return true
}

func (s *SubAgent) retryCompletionVerification(cause error) {
	if s == nil || s.turn == nil {
		return
	}
	if !s.completionRecoveryBudgetAvailable() {
		s.sendEvent(Event{Type: EventAgentError, Payload: fmt.Errorf("completion verification failed after retry: %w", cause)})
		return
	}
	s.appendPendingUserMessage(pendingUserMessage{Content: fmt.Sprintf("Completion was rejected: %v. Re-run the declared verification command(s) so they are the most recent workspace activity, then call Complete again with the exact command(s).", cause)})
	s.asyncCallLLMWithFlightMarked(s.turn, s.ctxMgr.Snapshot())
}

// rejectInvalidCompleteArguments handles a Complete call whose arguments failed
// validation — a JSON parse error, an empty summary, an artifact outside the
// session, or an invalid typed result. Within the shared rejected-completion
// budget it appends a "Completion rejected" tool result (so the transcript
// keeps its tool-call pairing) and gives the model one follow-up request to
// call Complete again with corrected arguments.
//
// degraded is the fallback delivery for the one rejection class that leaves a
// usable completion behind (an incomplete typed-result group, see
// typedResultPairingError): once the budget is spent, settling it keeps the
// summary and the rest of the structured payload instead of destroying a
// finished task over an optional metadata field. Every other rejection class
// still fails the task, because nothing dependable is left to deliver.
func (s *SubAgent) rejectInvalidCompleteArguments(callID string, cause error, degraded *AgentResult) {
	if s == nil || s.turn == nil {
		return
	}
	if s.completionRecoveryBudgetAvailable() {
		s.appendCompleteToolResult(callID, "Completion rejected: "+cause.Error())
		s.appendPendingUserMessage(pendingUserMessage{Content: fmt.Sprintf("Completion was rejected: %v. Call Complete again with corrected, valid arguments.", cause)})
		s.asyncCallLLMWithFlightMarked(s.turn, s.ctxMgr.Snapshot())
		return
	}
	if degraded != nil {
		if err := s.finishCompletion(callID, degraded); err == nil {
			return
		} else {
			// The degraded payload failed a check of its own (an unbacked
			// verification claim). Report that cause rather than the pairing
			// error, so the failure names what actually blocked delivery.
			cause = err
		}
	}
	s.appendCompleteToolResult(callID, "Completion rejected: "+cause.Error())
	s.sendEvent(Event{Type: EventAgentError, Payload: fmt.Errorf("completion was rejected after retry: %w", cause)})
}
