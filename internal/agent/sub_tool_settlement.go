package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/keakon/chord/internal/analytics"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

// collectToolSettlement closes admissions and snapshots every result that
// already finished, so a SubAgent turn closeout can preserve completed results
// and distinguish admitted-but-unresolved calls from ones that never ran.
func (s *SubAgent) collectToolSettlement(turn *Turn) toolSettlementEvidence {
	if turn == nil {
		return toolSettlementEvidence{}
	}
	turn.closeToolAdmissions()
	evidence := turn.takeToolSettlement()
	if turn.streamingToolExec != nil {
		for callID, result := range turn.streamingToolExec.DrainCompletedResults() {
			evidence.addReadyResult(callID, subToolResultFromPayload(result))
		}
	}
	return evidence
}

// settleInterruptedSubToolCalls settles the given pending calls exactly once,
// preserving results that finished before the closeout. The returned calls still
// need a synthetic terminal result.
func (s *SubAgent) settleInterruptedSubToolCalls(turn *Turn, evidence toolSettlementEvidence, calls []PendingToolCall) (synthetic []PendingToolCall, preserved int) {
	return closeInterruptedToolCalls(turn, evidence, calls, func(call PendingToolCall, ready any) bool {
		result, ok := ready.(*toolResult)
		if !ok {
			return false
		}
		return s.handleCompletedInterruptedToolResult(call, result)
	})
}

func (s *SubAgent) handleCompletedInterruptedToolResult(call PendingToolCall, result *toolResult) bool {
	if s == nil || result == nil {
		return false
	}
	if s.ctxMgr != nil && !s.ctxMgr.AnyAssistantDeclaresToolCallID(call.CallID) {
		if s.parent != nil {
			emitToolCallDiscards(s.parent.emitToTUI, []PendingToolCall{call}, "not_in_context")
		}
		return false
	}
	tc := message.ToolCall{ID: call.CallID, Name: call.Name, Args: json.RawMessage(call.ArgsJSON)}
	if err := s.commitPromotedToolSideEffects(tc, result); err != nil {
		result.Error = fmt.Errorf("commit completed speculative tool side effects: %w", err)
	}
	if s.parent != nil && s.parent.walltime != nil {
		s.parent.walltime.recordTarget(result.walltimeTarget, analytics.WalltimePurposeTool, result.Duration)
	}
	s.appendCompletedInterruptedToolResult(result)
	return true
}

// appendCompletedInterruptedToolResult persists and emits a tool result whose
// execution finished before the turn was cancelled or replaced, without driving
// normal turn continuation.
func (s *SubAgent) appendCompletedInterruptedToolResult(result *toolResult) {
	if s == nil || result == nil {
		return
	}
	if len(result.DeliveredFiles) > 0 && result.deliveryCommit == nil {
		result.DeliveredFiles = nil
		result.Error = context.Canceled
		result.Result = "File delivery canceled before durable submission"
	}
	if result.Name == tools.NameGenerateImage && result.RecoveryState == "" {
		result.RecoveryState = imageToolRecoveryState(result.Error)
	}
	rawResult := result.Result
	displayResult, contextResult, _, isError := composeToolResultTexts(rawResult, result.Error)
	contextResult = applyToolArgsAuditToContextResult(contextResult, result.Audit)
	contextResult = appendExternalOutcomeNote(contextResult, result.Error)
	parts := s.toolResultParts(contextResult, result.Images)

	event := ToolResultEvent{
		CallID:         result.CallID,
		Name:           result.Name,
		ArgsJSON:       result.ArgsJSON,
		Audit:          result.Audit.Clone(),
		Result:         displayResult,
		Payload:        result.Payload,
		Notes:          append([]string(nil), result.Notes...),
		Status:         toolResultStatus(isError),
		AgentID:        s.instanceID,
		Parts:          parts,
		Diff:           result.Diff,
		DiffAdded:      result.DiffAdded,
		DiffRemoved:    result.DiffRemoved,
		FileCreated:    result.FileCreated,
		FileState:      result.FileState.Clone(),
		Duration:       result.Duration,
		RecoveryState:  result.RecoveryState,
		DeliveredFiles: successfulDeliveredFiles(result.DeliveredFiles, isError),
	}
	if result.Name == tools.NameGenerateImage {
		event.Diagnostic = imageToolErrorDiagnostic(result.Error)
	} else if s.parent != nil {
		s.parent.emitToTUI(event)
	}

	toolChangedPaths, fileAttributionIncomplete := s.recordTaskToolChanges(result, isError)
	toolMsg := s.buildToolResultMessage(result, contextResult, parts, isError, toolChangedPaths, fileAttributionIncomplete)
	s.ctxMgr.Append(toolMsg)
	if result.Name == tools.NameGenerateImage && s.parent != nil {
		s.parent.persistImageResult(s.recoveryManager(), s.instanceID, toolMsg, event, s.notePersistenceFailure)
	} else {
		s.persistMessageAsync(toolMsg, "tool result", nil)
	}
	s.directoryInstructions.Load().collectDiscovery(result.DirectoryDiscovery)
}
