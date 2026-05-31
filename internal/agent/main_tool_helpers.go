package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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

type toolRejectedByUserError struct {
	toolName   string
	denyReason string
}

func (e toolRejectedByUserError) Error() string {
	base := fmt.Sprintf("tool %q rejected by user", e.toolName)
	if strings.TrimSpace(e.denyReason) == "" {
		return base
	}
	return base + ": " + strings.TrimSpace(e.denyReason)
}

func (e toolRejectedByUserError) Unwrap() error { return errToolRejectedByUser }

func wrapToolRejectedByUser(toolName, denyReason string) error {
	return toolRejectedByUserError{toolName: toolName, denyReason: denyReason}
}

func wrapEditedArgsPermissionDenied(toolName string) error {
	return fmt.Errorf("edited arguments for tool %q are denied by permission policy: %w", toolName, errEditedArgsPermissionDeny)
}

func extractToolArgument(toolName string, args []byte) string {
	return extractToolArgumentInDir(toolName, args, "")
}

func extractToolArgumentInDir(toolName string, args []byte, projectRoot string) string {
	switch toolName {
	case "Shell":
		var parsed struct {
			Command string `json:"command"`
		}
		if err := json.Unmarshal(args, &parsed); err == nil && parsed.Command != "" {
			return parsed.Command
		}
	case tools.NameApplyPatch:
		if path := tools.ExtractApplyPatchPathFromArgsInDir(args, projectRoot); path != "" {
			return path
		}
	case "Read", "Write":
		var parsed struct {
			Path string `json:"path"`
		}
		if err := json.Unmarshal(args, &parsed); err == nil && parsed.Path != "" {
			return parsed.Path
		}
	case "WebFetch":
		var parsed struct {
			URL string `json:"url"`
		}
		if err := json.Unmarshal(args, &parsed); err == nil && parsed.URL != "" {
			return parsed.URL
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
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))
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

func finalizeStreamingToolCards(emit func(AgentEvent), validCallIDs map[string]struct{}, discardInfo map[string]StreamingToolDiscardInfo, t *Turn) {
	if t == nil {
		return
	}
	spec := t.drainStreamingToolCalls()
	const notExecutedMsg = "This tool call was not executed: arguments were invalid or incomplete when the model response finalized. Fix the arguments and retry if you still need this tool."
	const discardedMsg = "Speculative tool execution was discarded during finalize (not part of conversation context)."
	for _, c := range spec {
		if c.CallID == "" {
			continue
		}
		if _, ok := validCallIDs[c.CallID]; ok {
			continue
		}
		msg := notExecutedMsg
		if discardInfo != nil {
			if info, ok := discardInfo[c.CallID]; ok {
				// If speculative execution had started, we must not claim "not executed".
				if info.Started {
					msg = discardedMsg
					if info.Reason != "" {
						msg += " reason=" + info.Reason
					}
				} else if info.Reason != "" {
					// Speculative was registered but never started (deferred/cancelled/etc.).
					msg = notExecutedMsg + " reason=" + info.Reason
				}
			}
		}
		emit(ToolResultEvent{CallID: c.CallID, Name: c.Name, ArgsJSON: c.ArgsJSON, Audit: c.Audit.Clone(), Result: msg, Status: ToolResultStatusError, AgentID: c.AgentID})
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
	batches := make([]toolExecutionBatch, 0, len(calls))
	for i := 0; i < len(calls); {
		call := calls[i]
		args := llm.UnwrapToolArgs(call.Args)
		class := tools.ConcurrencyClassForTool(registry, call.Name, args)
		policy := tools.PolicyForTool(registry, call.Name, args)
		batch := toolExecutionBatch{Calls: []message.ToolCall{call}, AbortSiblingsOnError: policy.AbortSiblingsOnError}
		if class != tools.ToolConcurrencyClassReadOnly {
			batches = append(batches, batch)
			i++
			continue
		}
		usedPolicies := []tools.ConcurrencyPolicy{policy}
		j := i + 1
		for j < len(calls) {
			next := calls[j]
			nextArgs := llm.UnwrapToolArgs(next.Args)
			if tools.ConcurrencyClassForTool(registry, next.Name, nextArgs) != tools.ToolConcurrencyClassReadOnly {
				break
			}
			nextPolicy := tools.PolicyForTool(registry, next.Name, nextArgs)
			conflict := false
			for _, existing := range usedPolicies {
				if tools.ConcurrencyConflict(existing, nextPolicy) {
					conflict = true
					break
				}
			}
			if conflict {
				break
			}
			batch.Calls = append(batch.Calls, next)
			usedPolicies = append(usedPolicies, nextPolicy)
			if nextPolicy.AbortSiblingsOnError {
				batch.AbortSiblingsOnError = true
			}
			j++
		}
		batches = append(batches, batch)
		i = j
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

// contextCancelledError returns the error from ctx.Err(), or context.Canceled
// if the context has no error set. This helper ensures a cancelled tool batch
// always surfaces a non-nil error for tool result payloads.
func contextCancelledError(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return context.Canceled
}
