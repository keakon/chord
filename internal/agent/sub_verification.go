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
	coveredEpochs := make(map[uint64]struct{}, len(env.VerificationRun))
	oldestCoveredEpoch := s.workspaceMutationEpoch
	for _, declared := range env.VerificationRun {
		command := strings.TrimSpace(declared)
		var found *verificationLedgerEntry
		for i := len(s.verificationLedger) - 1; i >= 0; i-- {
			if s.verificationLedger[i].Command == command {
				entry := s.verificationLedger[i]
				found = &entry
				break
			}
		}
		if found == nil {
			return fmt.Errorf("verification command %q was not found among finalized Shell calls; run it again before Complete", command)
		}
		records = append(records, VerificationRecord{ToolCallID: found.ToolCallID, Command: found.Command, Status: found.Status, Summary: found.Summary})
		coveredEpochs[found.MutationEpoch] = struct{}{}
		if found.MutationEpoch < oldestCoveredEpoch {
			oldestCoveredEpoch = found.MutationEpoch
		}
	}
	for epoch := oldestCoveredEpoch; epoch <= s.workspaceMutationEpoch; epoch++ {
		if _, ok := coveredEpochs[epoch]; !ok {
			return fmt.Errorf("declared verification commands do not cover workspace mutation epoch %d; run them again before Complete", epoch)
		}
	}
	env.VerificationRecords = records
	return nil
}
