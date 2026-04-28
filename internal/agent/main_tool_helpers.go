package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

var (
	errToolPermissionDenied     = errors.New("tool denied by permission policy")
	errToolRequiresConfirmation = errors.New("tool requires confirmation")
	errToolConfirmationFailed   = errors.New("tool confirmation failed")
	errToolRejectedByUser       = errors.New("tool rejected by user")
	errEditedArgsPermissionDeny = errors.New("edited tool arguments denied by permission policy")
)

func wrapToolPermissionDenied(toolName string) error {
	return fmt.Errorf("tool %q denied by permission policy: %w", toolName, errToolPermissionDenied)
}

func wrapToolRequiresConfirmation(toolName string) error {
	return fmt.Errorf("tool %q requires confirmation, but no confirm callback is configured: %w", toolName, errToolRequiresConfirmation)
}

func wrapToolConfirmationFailed(toolName string, err error) error {
	return fmt.Errorf("confirmation for tool %q failed: %w", toolName, errors.Join(errToolConfirmationFailed, err))
}

func wrapToolRejectedByUser(toolName, denyReason string) error {
	denyReason = strings.TrimSpace(denyReason)
	if denyReason != "" {
		return fmt.Errorf("tool %q rejected by user: %s: %w", toolName, denyReason, errToolRejectedByUser)
	}
	return fmt.Errorf("tool %q rejected by user: %w", toolName, errToolRejectedByUser)
}

func wrapEditedArgsPermissionDenied(toolName string) error {
	return fmt.Errorf("edited arguments for tool %q are denied by permission policy: %w", toolName, errEditedArgsPermissionDeny)
}

func extractToolArgument(toolName string, args []byte) string {
	switch toolName {
	case "Bash":
		var parsed struct {
			Command string `json:"command"`
		}
		if err := json.Unmarshal(args, &parsed); err == nil && parsed.Command != "" {
			return parsed.Command
		}
	case "Read", "Write", "Edit":
		var parsed struct {
			Path string `json:"path"`
		}
		if err := json.Unmarshal(args, &parsed); err == nil && parsed.Path != "" {
			return parsed.Path
		}
	case "Skill":
		var parsed struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(args, &parsed); err == nil && parsed.Name != "" {
			return parsed.Name
		}
	case "Delete":
		req, err := tools.DecodeDeleteRequest(llm.UnwrapToolArgs(args))
		if err == nil && len(req.Paths) > 0 {
			return req.Paths[0]
		}
	case "Grep", "Glob":
		var parsed struct {
			Pattern string `json:"pattern"`
		}
		if err := json.Unmarshal(args, &parsed); err == nil && parsed.Pattern != "" {
			return parsed.Pattern
		}
	}
	return "*"
}

func computeFileHash(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

func isInternalControlTool(_ string) bool { return false }

func emitToolExecutionState(emit func(AgentEvent), calls []PendingToolCall, state ToolCallExecutionState) {
	for _, call := range calls {
		if strings.TrimSpace(call.CallID) == "" {
			continue
		}
		emit(ToolCallExecutionEvent{ID: call.CallID, Name: call.Name, ArgsJSON: call.ArgsJSON, State: state, AgentID: call.AgentID})
	}
}

func emitCancelledToolResults(emit func(AgentEvent), calls []PendingToolCall) {
	for _, call := range calls {
		emit(ToolResultEvent{CallID: call.CallID, Name: call.Name, ArgsJSON: call.ArgsJSON, Audit: call.Audit.Clone(), Result: "Cancelled", Status: ToolResultStatusCancelled, AgentID: call.AgentID})
	}
}

func discardSpeculativeStreamTools(emit func(AgentEvent), t *Turn) {
	if t == nil {
		return
	}
	spec := t.drainStreamingToolCalls()
	if len(spec) > 0 {
		emitCancelledToolResults(emit, spec)
	}
}

func finalizeStreamingToolCards(emit func(AgentEvent), validCallIDs map[string]struct{}, t *Turn) {
	if t == nil {
		return
	}
	spec := t.drainStreamingToolCalls()
	const orphanMsg = "This tool call was not executed: arguments were invalid or incomplete when the model response finalized. Fix the arguments and retry if you still need this tool."
	for _, c := range spec {
		if c.CallID == "" {
			continue
		}
		if _, ok := validCallIDs[c.CallID]; ok {
			continue
		}
		emit(ToolResultEvent{CallID: c.CallID, Name: c.Name, ArgsJSON: c.ArgsJSON, Audit: c.Audit.Clone(), Result: orphanMsg, Status: ToolResultStatusError, AgentID: c.AgentID})
	}
}

func emitFailedToolResults(emit func(AgentEvent), calls []PendingToolCall, err error) {
	message := toolCallFailureMessage(err)
	for _, call := range calls {
		emit(ToolResultEvent{CallID: call.CallID, Name: call.Name, ArgsJSON: call.ArgsJSON, Audit: call.Audit.Clone(), Result: message, Status: ToolResultStatusError, AgentID: call.AgentID})
	}
}

func toolCallFailureMessage(err error) string {
	msg := "Model stopped before completing this tool call"
	if err == nil {
		return msg
	}
	cause := toolErrorSummary(err)
	if cause == "" {
		return msg
	}
	return msg + ": " + cause
}

func classifyToolError(err error) string {
	if err == nil {
		return "unknown"
	}
	if errors.Is(err, os.ErrNotExist) {
		return "not_found"
	}
	if errors.Is(err, context.Canceled) {
		return "cancelled"
	}
	if errors.Is(err, errToolPermissionDenied) || errors.Is(err, errEditedArgsPermissionDeny) {
		return "permission_denied"
	}
	if errors.Is(err, errToolRequiresConfirmation) {
		return "requires_confirmation"
	}
	if errors.Is(err, errToolConfirmationFailed) {
		return "confirmation_failed"
	}
	if errors.Is(err, errToolRejectedByUser) {
		return "rejected_by_user"
	}
	msg := strings.ToLower(strings.TrimSpace(err.Error()))
	switch {
	case strings.Contains(msg, "denied by permission policy"):
		return "permission_denied"
	case strings.Contains(msg, "requires confirmation"):
		return "requires_confirmation"
	case strings.Contains(msg, "confirmation for tool"):
		return "confirmation_failed"
	case strings.Contains(msg, "rejected by user"):
		return "rejected_by_user"
	default:
		return "unknown"
	}
}

func toolErrorSummary(err error) string {
	if err == nil {
		return ""
	}
	summary := strings.TrimSpace(err.Error())
	if summary == "" {
		return ""
	}
	return summary
}

func toolResultStatusFromError(isError bool) ToolResultStatus {
	if isError {
		return ToolResultStatusError
	}
	return ToolResultStatusSuccess
}

type toolExecutionBatch struct {
	Calls                []message.ToolCall
	AbortSiblingsOnError bool
}

func buildToolExecutionBatches(registry *tools.Registry, calls []message.ToolCall) []toolExecutionBatch {
	if len(calls) == 0 {
		return nil
	}
	pending := append([]message.ToolCall(nil), calls...)
	batches := make([]toolExecutionBatch, 0, len(calls))
	for len(pending) > 0 {
		batch := toolExecutionBatch{Calls: []message.ToolCall{pending[0]}}
		policy0 := tools.PolicyForTool(registry, pending[0].Name, llm.UnwrapToolArgs(pending[0].Args))
		batch.AbortSiblingsOnError = policy0.AbortSiblingsOnError
		remaining := make([]message.ToolCall, 0, len(pending)-1)
		usedPolicies := []tools.ConcurrencyPolicy{policy0}
		for _, tc := range pending[1:] {
			policy := tools.PolicyForTool(registry, tc.Name, llm.UnwrapToolArgs(tc.Args))
			conflict := false
			for _, existing := range usedPolicies {
				if tools.ConcurrencyConflict(existing, policy) {
					conflict = true
					break
				}
			}
			if conflict {
				remaining = append(remaining, tc)
				continue
			}
			batch.Calls = append(batch.Calls, tc)
			usedPolicies = append(usedPolicies, policy)
			if policy.AbortSiblingsOnError {
				batch.AbortSiblingsOnError = true
			}
		}
		batches = append(batches, batch)
		pending = remaining
	}
	return batches
}

func composeToolResultTexts(rawResult string, err error) (displayResult, contextResult, errorText string, isError bool) {
	displayResult = rawResult
	contextResult = rawResult
	if err == nil {
		return displayResult, contextResult, "", false
	}

	errorText = toolErrorSummary(err)
	if errorText == "" {
		errorText = "tool execution failed"
	}
	if strings.TrimSpace(rawResult) == "" {
		return errorText, fmt.Sprintf("Error: %s", errorText), errorText, true
	}

	combined := strings.TrimRight(rawResult, "\n") + "\n\nError: " + errorText
	return combined, combined, errorText, true
}

func applyToolArgsAuditToContextResult(content string, _ *message.ToolArgsAudit) string {
	return content
}
