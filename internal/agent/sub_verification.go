package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/tools"
)

func (s *SubAgent) recordVerificationToolResult(result *toolResult, contextResult string, isError bool) {
	if s == nil || result == nil || tools.NormalizeName(result.Name) != tools.NameShell {
		return
	}
	var args struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(llm.UnwrapToolArgs(json.RawMessage(result.ArgsJSON)), &args); err != nil {
		return
	}
	command := strings.TrimSpace(args.Command)
	if command == "" {
		return
	}
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
	// that never touch a file (lint, build), so requiring the declared
	// commands to cover every epoch in between punished honest declarations:
	// after modify → lint → build → test, declaring [lint, test] failed on the
	// build gap while declaring only [test] passed. Completion needs exactly
	// one guarantee — nothing touched the workspace after the newest declared
	// verification ran — so compare only that newest epoch to the current one.
	if latestCoveredEpoch < s.workspaceMutationEpoch {
		return fmt.Errorf("the workspace mutation epoch advanced after the declared verification commands last ran; re-run them before Complete")
	}
	env.VerificationRecords = records
	return nil
}

func (s *SubAgent) retryCompletionVerification(cause error) {
	if s == nil || s.turn == nil {
		return
	}
	if s.turn.SubAgentTerminalRecoveryCount >= 1 {
		s.sendEvent(Event{Type: EventAgentError, Payload: fmt.Errorf("completion verification failed after retry: %w", cause)})
		return
	}
	s.turn.SubAgentTerminalRecoveryCount++
	s.appendPendingUserMessage(pendingUserMessage{Content: fmt.Sprintf("Completion was rejected: %v. Re-run the declared verification command(s) so they are the most recent workspace activity, then call Complete again with the exact command(s).", cause)})
	s.asyncCallLLMWithFlightMarked(s.turn, s.ctxMgr.Snapshot())
}
