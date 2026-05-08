package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/agent/agentdiff"
	"github.com/keakon/chord/internal/filelock"
	"github.com/keakon/chord/internal/hook"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/tools"
)

// commitPromotedToolSideEffects applies the minimal post-execution side effects
// that speculative execution intentionally skipped, but that must still happen
// once a tool call is validated and committed to context.
func (a *MainAgent) commitPromotedToolSideEffects(tc message.ToolCall, payload *ToolResultPayload) {
	if a == nil || payload == nil || payload.Error != nil {
		return
	}
	if payload.speculativeHooks != nil && payload.speculativeHooks.commit != nil {
		payload.speculativeHooks.commit()
	}
	if tc.Name != tools.NameRead {
		return
	}
	if a.fileTrack == nil {
		return
	}
	var parsed struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(payload.ArgsJSON), &parsed); err != nil {
		return
	}
	if strings.TrimSpace(parsed.Path) == "" {
		return
	}
	hash := computeFileHash(parsed.Path)
	a.fileTrack.TrackRead(parsed.Path, a.instanceID, hash)
}

// executeToolCallWithHook is a small wrapper used by the streaming-tool finalize
// path: we sometimes need to run a tool call after running the on_tool_call hook
// ourselves (e.g. hook-induced args drift) and must avoid firing the hook twice.
func (a *MainAgent) executeToolCallWithHook(ctx context.Context, tc message.ToolCall, fireHook bool) (ToolExecutionResult, error) {
	execResult := ToolExecutionResult{EffectiveArgsJSON: string(tc.Args)}
	if isMainAgentReservedTool(tc.Name) {
		return execResult, fmt.Errorf("tool %q is reserved for SubAgents and unavailable to MainAgent", tc.Name)
	}
	// ----- Permission check -----
	if len(a.ruleset) > 0 && !isInternalControlTool(tc.Name) {
		ruleset := a.effectiveRuleset()
		decision := evaluateToolPermission(ruleset, tc.Name, tc.Args)

		switch decision.Action {
		case permission.ActionDeny:
			log.Warnf("tool call denied by permission tool=%v argument=%v", tc.Name, decision.MatchArgument)
			return execResult, wrapToolPermissionDenied(tc.Name)
		case permission.ActionAsk:
			if a.confirmFn == nil {
				return execResult, wrapToolRequiresConfirmation(tc.Name)
			}
			resp, err := a.confirmFn(ctx, tc.Name, string(tc.Args), decision.NeedsApprovalPaths, decision.AlreadyAllowedPaths)
			if err != nil {
				return execResult, wrapToolConfirmationFailed(tc.Name, err)
			}
			if !resp.Approved {
				denyReason := normalizeDenyReason(resp.DenyReason)
				log.Infof("tool call rejected by user tool=%v argument=%v deny_reason=%v", tc.Name, decision.MatchArgument, denyReason)
				return execResult, wrapToolRejectedByUser(tc.Name, denyReason)
			}
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
				a.turn.updatePendingToolCall(PendingToolCall{CallID: tc.ID, Name: tc.Name, ArgsJSON: execResult.EffectiveArgsJSON, Audit: execResult.Audit})
			}
			a.emitToTUI(ToolCallUpdateEvent{ID: tc.ID, Name: tc.Name, ArgsJSON: execResult.EffectiveArgsJSON, ArgsStreamingDone: true})
		case permission.ActionAllow:
			// no-op
		}
	}

	// ----- Hook: on_tool_call (before execution) -----
	if fireHook {
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
							a.emitToTUI(ToolCallUpdateEvent{ID: tc.ID, Name: tc.Name, ArgsJSON: execResult.EffectiveArgsJSON, ArgsStreamingDone: true})
						}
					}
				}
			}
		}
	}

	// ----- Repetition guard -----
	a.repMu.Lock()
	allowed := a.repetition.Check(tc.Name, tc.Args)
	a.repMu.Unlock()
	if !allowed {
		return execResult, fmt.Errorf("tool %q called too many times with the same arguments (loop detected)", tc.Name)
	}

	// ----- Malformed args guard -----
	if llm.IsMalformedArgs(tc.Args) {
		log.Warnf("tool call has malformed args (sentinel detected), returning guidance error tool=%v instance=%v", tc.Name, a.instanceID)
		return execResult, fmt.Errorf(
			"tool %q was called with malformed arguments (likely due to output "+
				"truncation at max_tokens). Please reduce the number of parallel "+
				"tool calls and retry with properly structured JSON arguments "+
				"matching the tool's input schema",
			tc.Name,
		)
	}
	if llm.IsEmptyArgs(tc.Args) {
		if tool, ok := a.tools.Get(tc.Name); ok {
			if req := llm.RequiredFields(tool.Parameters()); len(req) > 0 {
				log.Warnf("tool call has empty args but tool requires parameters tool=%v required=%v", tc.Name, req)
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
	if tc.Name == tools.NameRead || tc.Name == tools.NameWrite || tc.Name == tools.NameEdit || tc.Name == tools.NameDelete {
		if tc.Name == tools.NameDelete {
			locks, err := acquireDeleteLocks(a.fileTrack, a.instanceID, tc.Args)
			if err != nil {
				var ext *filelock.ExternalModificationError
				if errors.As(err, &ext) {
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

	if trackedFilePath != "" && (tc.Name == tools.NameWrite || tc.Name == tools.NameEdit) {
		currentHash := computeFileHash(trackedFilePath)
		if err := a.fileTrack.AcquireWrite(trackedFilePath, a.instanceID, currentHash); err != nil {
			var ext *filelock.ExternalModificationError
			if errors.As(err, &ext) {
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

	result, err := a.tools.Execute(agentCtx, tc.Name, llm.UnwrapToolArgs(tc.Args))
	if err != nil {
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
	if tc.Name == tools.NameRead && trackedFilePath != "" {
		hash := computeFileHash(trackedFilePath)
		a.fileTrack.TrackRead(trackedFilePath, a.instanceID, hash)
	}

	// Apply output truncation.
	truncated := tools.TruncateOutputWithOptions(result, a.sessionDir, tools.TruncateOptions{ArtifactKey: artifactKey})
	content := tools.NormalizeEmptySuccessOutput(tc.Name, truncated.Content, nil)
	content = tools.AppendArtifactGuidance(content, truncated,
		"Process it with Read(path, limit, offset) or Grep(path=...) in chunks. Use the Delegate tool only when you need a separate agent for substantial multi-step work on this content.")
	execResult.Result = content
	return execResult, nil
}
