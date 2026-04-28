package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/keakon/chord/internal/agent/agentdiff"
	"github.com/keakon/chord/internal/filelock"
	"github.com/keakon/chord/internal/hook"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/tools"
)

// ---------------------------------------------------------------------------
// Tool execution
// ---------------------------------------------------------------------------

// executeToolCall runs a single tool invocation with permission checks,
// repetition detection, and output truncation.
func (a *MainAgent) executeToolCall(ctx context.Context, tc message.ToolCall) (ToolExecutionResult, error) {
	execResult := ToolExecutionResult{
		EffectiveArgsJSON: string(tc.Args),
	}
	if isMainAgentReservedTool(tc.Name) {
		return execResult, fmt.Errorf("tool %q is reserved for SubAgents and unavailable to MainAgent", tc.Name)
	}
	// ----- Permission check -----
	if len(a.ruleset) > 0 && !isInternalControlTool(tc.Name) {
		ruleset := a.effectiveRuleset()
		decision := evaluateToolPermission(ruleset, tc.Name, tc.Args)

		switch decision.Action {
		case permission.ActionDeny:
			slog.Warn("tool call denied by permission",
				"tool", tc.Name,
				"argument", decision.MatchArgument,
			)
			return execResult, wrapToolPermissionDenied(tc.Name)

		case permission.ActionAsk:
			if a.confirmFn == nil {
				return execResult, wrapToolRequiresConfirmation(tc.Name)
			}
			// Tool goroutines serialize naturally on the cap=1 confirmCh;
			// the channel send can be interrupted by context cancellation
			// (e.g. turn cancelled while waiting for user response).
			resp, err := a.confirmFn(ctx, tc.Name, string(tc.Args), decision.NeedsApprovalPaths, decision.AlreadyAllowedPaths)
			if err != nil {
				return execResult, wrapToolConfirmationFailed(tc.Name, err)
			}
			if !resp.Approved {
				denyReason := normalizeDenyReason(resp.DenyReason)
				slog.Info("tool call rejected by user",
					"tool", tc.Name,
					"argument", decision.MatchArgument,
					"deny_reason", denyReason,
				)
				return execResult, wrapToolRejectedByUser(tc.Name, denyReason)
			}
			// Process rule intent if present
			if resp.RuleIntent != nil {
				a.processRuleIntent(tc.Name, resp.RuleIntent)
				ruleset = a.effectiveRuleset()
			}
			originalArgs := append(json.RawMessage(nil), tc.Args...)
			editedArgs, err := applyConfirmedArgsEdits(a.tools, ruleset, tc.Name, tc.Args, resp.FinalArgsJSON)
			if err != nil {
				return execResult, err
			}
			tc.Args = editedArgs
			execResult.EffectiveArgsJSON = string(tc.Args)
			execResult.Audit = buildToolArgsAudit(originalArgs, tc.Args, resp.EditSummary)
			if a.turn != nil {
				a.turn.updatePendingToolCall(PendingToolCall{
					CallID:   tc.ID,
					Name:     tc.Name,
					ArgsJSON: execResult.EffectiveArgsJSON,
					Audit:    execResult.Audit,
				})
			}
			a.emitToTUI(ToolCallUpdateEvent{ID: tc.ID, Name: tc.Name, ArgsJSON: execResult.EffectiveArgsJSON})

		case permission.ActionAllow:
			// Execute directly — no confirmation needed.
		}
	}

	// ----- Hook: on_tool_call (before execution) -----
	hookResult, hookErr := a.fireHook(ctx, hook.OnToolCall, a.currentTurnID(), buildToolHookData(tc))
	if hookErr == nil && hookResult != nil {
		switch hookResult.Action {
		case hook.ActionBlock:
			msg := "blocked by hook"
			if hookResult.Message != "" {
				msg = hookResult.Message
			}
			return execResult, fmt.Errorf("tool %q %s", tc.Name, msg)
		case hook.ActionModify:
			if modified, ok := hookResult.Data.(map[string]any); ok {
				if newArgs, ok := modified["args"]; ok {
					if raw, err := json.Marshal(newArgs); err == nil {
						tc.Args = raw
						execResult.EffectiveArgsJSON = string(tc.Args)
						execResult.Audit = syncAuditEffectiveArgs(execResult.Audit, tc.Args)
						if a.turn != nil {
							a.turn.updatePendingToolCall(PendingToolCall{CallID: tc.ID, Name: tc.Name, ArgsJSON: execResult.EffectiveArgsJSON, Audit: execResult.Audit})
						}
						a.emitToTUI(ToolCallUpdateEvent{ID: tc.ID, Name: tc.Name, ArgsJSON: execResult.EffectiveArgsJSON})
					}
				}
			}
		}
	}

	// ----- Repetition guard -----
	// Serialised because the detector is stateful.
	a.repMu.Lock()
	allowed := a.repetition.Check(tc.Name, tc.Args)
	a.repMu.Unlock()

	if !allowed {
		return execResult, fmt.Errorf(
			"tool %q called too many times with the same arguments (loop detected)",
			tc.Name,
		)
	}

	// ----- Malformed args guard (improvement 1) -----
	// When the LLM produces a tool call with invalid JSON, the streaming parser
	// replaces the args with the sentinel {"error":"malformed tool call arguments
	// from model"}. Detect this early and return a descriptive error that tells
	// the model exactly what happened and how to fix it, rather than letting the
	// tool fail with a generic "field required" message.
	if llm.IsMalformedArgs(tc.Args) {
		slog.Warn("tool call has malformed args (sentinel detected), returning guidance error",
			"tool", tc.Name, "instance", a.instanceID,
		)
		return execResult, fmt.Errorf(
			"tool %q was called with malformed arguments (likely due to output "+
				"truncation at max_tokens). Please reduce the number of parallel "+
				"tool calls and retry with properly structured JSON arguments "+
				"matching the tool's input schema",
			tc.Name,
		)
	}

	// ----- Empty args guard (improvement 4) -----
	// When the LLM's output is truncated near max_tokens but stop_reason is
	// still "end_turn", later tool calls in a parallel batch may receive
	// syntactically valid but empty arguments "{}". For tools that declare
	// required parameters, this is almost certainly an error — catch it early
	// with a diagnostic message rather than letting the tool return a generic
	// "field required" error that doesn't help the model self-correct.
	if llm.IsEmptyArgs(tc.Args) {
		if tool, ok := a.tools.Get(tc.Name); ok {
			if req := llm.RequiredFields(tool.Parameters()); len(req) > 0 {
				slog.Warn("tool call has empty args but tool requires parameters",
					"tool", tc.Name, "required", req,
				)
				return execResult, fmt.Errorf(
					"tool %q was called with empty arguments {}. This typically "+
						"happens when the model's output was truncated at max_tokens. "+
						"Please reduce the number of parallel tool calls and retry "+
						"with the complete required parameters: %v",
					tc.Name, req,
				)
			}
		}
	}
	if err := validateToolArgsAgainstSchema(a.tools, tc.Name, tc.Args); err != nil {
		return execResult, err
	}
	execResult.PreFilePath, execResult.PreContent, execResult.PreExisted = agentdiff.CapturePreWriteState(tc)

	// Attach execution context metadata so tools can identify the invoking
	// agent and optionally report structured progress.
	agentCtx := buildToolExecContext(ctx, tc, a.instanceID, "", a.sessionDir, a, a.emitToTUI)
	artifactKey := tc.ID
	if strings.TrimSpace(artifactKey) == "" {
		artifactKey = tc.Name + "-anonymous"
	}
	// ----- FileTracker integration -----
	var (
		trackedFilePath string
		deleteLocks     *deleteLockSet
	)
	if tc.Name == "Read" || tc.Name == "Write" || tc.Name == "Edit" || tc.Name == "MultiEdit" || tc.Name == "Delete" {
		if tc.Name == "Delete" {
			locks, err := acquireDeleteLocks(a.fileTrack, a.instanceID, tc.Args)
			if err != nil {
				if _, ok := errors.AsType[*filelock.ExternalModificationError](err); ok {
					return execResult, err
				}
				return execResult, fmt.Errorf("file conflict: %w", err)
			}
			deleteLocks = locks
			if deleteLocks != nil {
				defer deleteLocks.Release()
			}
		} else {
			var parsed struct {
				Path string `json:"path"`
			}
			if json.Unmarshal(tc.Args, &parsed) == nil {
				trackedFilePath = parsed.Path
			}
		}
	}

	// Write/Edit/MultiEdit: acquire write lock before execution, release after.
	if trackedFilePath != "" && (tc.Name == "Write" || tc.Name == "Edit" || tc.Name == "MultiEdit") {
		currentHash := computeFileHash(trackedFilePath)
		if err := a.fileTrack.AcquireWrite(trackedFilePath, a.instanceID, currentHash); err != nil {
			if _, ok := errors.AsType[*filelock.ExternalModificationError](err); ok {
				return execResult, err
			}
			return execResult, fmt.Errorf("file conflict: %w", err)
		}
		lockedPath := trackedFilePath
		defer func() {
			newHash := computeFileHash(lockedPath)
			a.fileTrack.ReleaseWrite(lockedPath, a.instanceID, newHash)
		}()
	}

	args := llm.UnwrapToolArgs(tc.Args)
	result, err := a.tools.Execute(agentCtx, tc.Name, args)
	if err != nil {
		// Preserve the tool output (e.g. Bash stdout/stderr) even on error.
		// The LLM needs this output for effective debugging.
		if result != "" {
			truncated := tools.TruncateOutputWithOptions(result, a.sessionDir, tools.TruncateOptions{ArtifactKey: artifactKey})
			content := tools.NormalizeEmptySuccessOutput(tc.Name, truncated.Content, err)
			content = tools.AppendArtifactGuidance(content, truncated,
				"Process it with Read(path, limit, offset) or Grep(path=...) in chunks. Use the Delegate tool only when you need a separate agent for substantial multi-step work on this content.")
			execResult.Result = content
			return execResult, err
		}
		return execResult, err
	}
	if deleteLocks != nil {
		deleteLocks.Commit(result)
	}

	// Read: track content hash after successful execution for optimistic locking.
	if tc.Name == "Read" && trackedFilePath != "" {
		hash := computeFileHash(trackedFilePath)
		a.fileTrack.TrackRead(trackedFilePath, a.instanceID, hash)
	}

	if (tc.Name == "Write" || tc.Name == "Edit") && trackedFilePath != "" {
		if tool, ok := a.tools.Get(tc.Name); ok {
			switch t := tool.(type) {
			case tools.WriteTool:
				if t.LSP != nil {
					execResult.LSPReviews = t.LSP.CurrentReviewSnapshots(trackedFilePath)
				}
			case tools.EditTool:
				if t.LSP != nil {
					execResult.LSPReviews = t.LSP.CurrentReviewSnapshots(trackedFilePath)
				}
			}
		}
	}

	// Apply output truncation (saves oversized output to disk).
	truncated := tools.TruncateOutputWithOptions(result, a.sessionDir, tools.TruncateOptions{ArtifactKey: artifactKey})
	content := tools.NormalizeEmptySuccessOutput(tc.Name, truncated.Content, nil)
	content = tools.AppendArtifactGuidance(content, truncated,
		"Process it with Read(path, limit, offset) or Grep(path=...) in chunks. Use the Delegate tool only when you need a separate agent for substantial multi-step work on this content.")

	execResult.Result = content
	return execResult, nil
}

// normalizeDenyReason trims, collapses newlines, and limits length of a deny reason.
func normalizeDenyReason(reason string) string {
	reason = strings.TrimSpace(reason)
	reason = strings.ReplaceAll(reason, "\n", " ")
	if len([]rune(reason)) > 200 {
		reason = string([]rune(reason)[:200])
	}
	return reason
}
