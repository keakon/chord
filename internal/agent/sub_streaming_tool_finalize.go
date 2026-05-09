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

func (s *SubAgent) commitPromotedToolSideEffects(tc message.ToolCall, result *toolResult) {
	if s == nil || result == nil || result.Error != nil {
		return
	}
	if result.speculativeHooks != nil && result.speculativeHooks.commit != nil {
		result.speculativeHooks.commit()
	}
	if tc.Name != tools.NameRead {
		return
	}
	if s.parent == nil || s.parent.fileTrack == nil {
		return
	}
	var parsed struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(result.ArgsJSON), &parsed); err != nil {
		return
	}
	if strings.TrimSpace(parsed.Path) == "" {
		return
	}
	hash := computeFileHash(parsed.Path)
	s.parent.fileTrack.TrackRead(parsed.Path, s.instanceID, hash)
}

func (s *SubAgent) executeToolCallWithHook(ctx context.Context, tc message.ToolCall, fireHook bool) (ToolExecutionResult, error) {
	execResult := ToolExecutionResult{EffectiveArgsJSON: string(tc.Args)}
	// ----- Permission check -----
	if len(s.ruleset) > 0 && !isSubAgentInternalTool(tc.Name) {
		decision := evaluateToolPermission(s.ruleset, tc.Name, tc.Args)
		switch decision.Action {
		case permission.ActionDeny:
			log.Warnf("SubAgent: tool call denied by permission agent=%v tool=%v argument=%v", s.instanceID, tc.Name, decision.MatchArgument)
			return execResult, wrapToolPermissionDenied(tc.Name)
		case permission.ActionAsk:
			if s.parent.confirmFn == nil {
				return execResult, wrapToolRequiresConfirmation(tc.Name)
			}
			resp, err := s.parent.confirmFn(ctx, tc.Name, string(tc.Args), decision.NeedsApprovalPaths, decision.AlreadyAllowedPaths)
			if err != nil {
				return execResult, wrapToolConfirmationFailed(tc.Name, err)
			}
			if !resp.Approved {
				denyReason := normalizeDenyReason(resp.DenyReason)
				log.Infof("SubAgent: tool call rejected by user agent=%v tool=%v argument=%v deny_reason=%v", s.instanceID, tc.Name, decision.MatchArgument, denyReason)
				return execResult, wrapToolRejectedByUser(tc.Name, denyReason)
			}
			if resp.RuleIntent != nil {
				s.parent.processRuleIntent(tc.Name, resp.RuleIntent)
				s.ruleset = s.parent.effectiveRuleset()
			}
			originalArgs := append(json.RawMessage(nil), tc.Args...)
			editedArgs, err := applyConfirmedArgsEdits(s.tools, s.ruleset, tc.Name, tc.Args, resp.FinalArgsJSON)
			if err != nil {
				return execResult, err
			}
			tc.Args = editedArgs
			execResult.EffectiveArgsJSON = string(tc.Args)
			execResult.Audit = buildToolArgsAudit(originalArgs, tc.Args, resp.EditSummary)
			if s.turn != nil {
				s.turn.updatePendingToolCall(PendingToolCall{CallID: tc.ID, Name: tc.Name, AgentID: s.instanceID, ArgsJSON: execResult.EffectiveArgsJSON, Audit: execResult.Audit})
			}
			s.parent.emitToTUI(ToolCallUpdateEvent{ID: tc.ID, Name: tc.Name, ArgsJSON: execResult.EffectiveArgsJSON, ArgsStreamingDone: true, AgentID: s.instanceID})
		case permission.ActionAllow:
			// no-op
		}
	}

	// ----- Hook: on_tool_call -----
	if fireHook {
		hookResult, hookErr := s.fireHook(ctx, hook.OnToolCall, s.currentTurnID(), buildToolHookData(tc))
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
							if s.turn != nil {
								s.turn.updatePendingToolCall(PendingToolCall{CallID: tc.ID, Name: tc.Name, AgentID: s.instanceID, ArgsJSON: execResult.EffectiveArgsJSON, Audit: execResult.Audit})
							}
							s.parent.emitToTUI(ToolCallUpdateEvent{ID: tc.ID, Name: tc.Name, ArgsJSON: execResult.EffectiveArgsJSON, ArgsStreamingDone: true, AgentID: s.instanceID})
						}
					}
				}
			}
		}
	}

	// ----- Repetition guard -----
	s.repMu.Lock()
	allowed := s.repetition.Check(tc.Name, tc.Args)
	s.repMu.Unlock()
	if !allowed {
		return execResult, fmt.Errorf("tool %q called too many times with the same arguments (loop detected)", tc.Name)
	}

	if llm.IsMalformedArgs(tc.Args) {
		log.Warnf("SubAgent: tool call has malformed args, returning guidance error agent=%v tool=%v", s.instanceID, tc.Name)
		return execResult, fmt.Errorf(
			"tool %q was called with malformed arguments (likely due to output "+
				"truncation at max_tokens). Please reduce the number of parallel "+
				"tool calls and retry with properly structured JSON arguments "+
				"matching the tool's input schema",
			tc.Name,
		)
	}
	if llm.IsEmptyArgs(tc.Args) {
		if tool, ok := s.tools.Get(tc.Name); ok {
			if req := llm.RequiredFields(tool.Parameters()); len(req) > 0 {
				log.Warnf("SubAgent: tool call has empty args but tool requires parameters agent=%v tool=%v required=%v", s.instanceID, tc.Name, req)
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
	if err := validateToolArgsAgainstSchema(s.tools, tc.Name, tc.Args); err != nil {
		return execResult, err
	}
	execResult.PreFilePath, execResult.PreContent, execResult.PreExisted = agentdiff.CapturePreWriteState(tc)

	agentCtx := buildToolExecContext(ctx, tc, s.instanceID, s.taskID, s.sessionDir, s.parent, s.parent.emitToTUI)
	artifactKey := tc.ID
	if strings.TrimSpace(artifactKey) == "" {
		artifactKey = tc.Name + "-anonymous"
	}

	var (
		trackedFilePath string
		deleteLocks     *deleteLockSet
	)
	if tc.Name == tools.NameRead || tc.Name == tools.NameWrite || tc.Name == tools.NameEdit || tc.Name == tools.NameDelete {
		if tc.Name == tools.NameDelete {
			locks, err := acquireDeleteLocks(s.parent.fileTrack, s.instanceID, tc.Args)
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
			if json.Unmarshal(llm.UnwrapToolArgs(tc.Args), &parsed) == nil {
				trackedFilePath = parsed.Path
			}
		}
	}
	if err := ensureTrackedEditPreconditions(s.parent.fileTrack, s.instanceID, trackedFilePath, tc.Name); err != nil {
		return execResult, wrapTrackedWriteError(err)
	}
	if trackedFilePath != "" && (tc.Name == tools.NameWrite || tc.Name == tools.NameEdit) {
		currentHash := computeFileHash(trackedFilePath)
		if err := s.parent.fileTrack.AcquireWrite(trackedFilePath, s.instanceID, currentHash); err != nil {
			return execResult, wrapTrackedWriteError(err)
		}
		defer func() {
			newHash := computeFileHash(trackedFilePath)
			s.parent.fileTrack.ReleaseWrite(trackedFilePath, s.instanceID, newHash)
		}()
	}

	result, err := s.tools.Execute(agentCtx, tc.Name, llm.UnwrapToolArgs(tc.Args))
	if err != nil {
		if result != "" {
			truncated := tools.TruncateOutputWithOptions(result, s.sessionDir, tools.TruncateOptions{ArtifactKey: artifactKey})
			content := tools.NormalizeEmptySuccessOutput(tc.Name, truncated.Content, err)
			content = tools.AppendArtifactGuidance(content, truncated,
				"Use Grep to search the full content or Read with offset/limit to view specific sections.")
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
		s.parent.fileTrack.TrackRead(trackedFilePath, s.instanceID, hash)
	}
	truncated := tools.TruncateOutputWithOptions(result, s.sessionDir, tools.TruncateOptions{ArtifactKey: artifactKey})
	content := tools.NormalizeEmptySuccessOutput(tc.Name, truncated.Content, nil)
	content = tools.AppendArtifactGuidance(content, truncated,
		"Use Grep to search the full content or Read with offset/limit to view specific sections.")
	execResult.Result = content
	return execResult, nil
}

func (s *SubAgent) executeToolCallSpeculative(ctx context.Context, tc message.ToolCall) (ToolExecutionResult, error) {
	execResult := ToolExecutionResult{EffectiveArgsJSON: string(tc.Args)}
	if err := validateToolArgsAgainstSchema(s.tools, tc.Name, tc.Args); err != nil {
		return execResult, err
	}
	execResult.PreFilePath, execResult.PreContent, execResult.PreExisted = agentdiff.CapturePreWriteState(tc)
	if tc.Name == tools.NameEdit {
		var parsed struct {
			Path string `json:"path"`
		}
		if json.Unmarshal(llm.UnwrapToolArgs(tc.Args), &parsed) == nil {
			if err := ensureTrackedEditPreconditions(s.parent.fileTrack, s.instanceID, parsed.Path, tc.Name); err != nil {
				return execResult, wrapTrackedWriteError(err)
			}
		}
	}
	hooks, err := prepareSpeculativeToolCall(tc, s.parent.fileTrack, s.instanceID)
	if err != nil {
		return execResult, err
	}
	execResult.speculativeHooks = hooks
	agentCtx := buildToolExecContext(ctx, tc, s.instanceID, s.taskID, s.sessionDir, s.parent, s.parent.emitToTUI)
	artifactKey := tc.ID
	if strings.TrimSpace(artifactKey) == "" {
		artifactKey = tc.Name + "-anonymous"
	}
	result, err := s.tools.Execute(agentCtx, tc.Name, llm.UnwrapToolArgs(tc.Args))
	if err != nil {
		rollbackSpeculativeToolHooks(execResult)
		if result != "" {
			truncated := tools.TruncateOutputWithOptions(result, s.sessionDir, tools.TruncateOptions{ArtifactKey: artifactKey})
			content := tools.NormalizeEmptySuccessOutput(tc.Name, truncated.Content, err)
			content = tools.AppendArtifactGuidance(content, truncated,
				"Use Grep to search the full content or Read with offset/limit to view specific sections.")
			execResult.Result = content
			return execResult, err
		}
		return execResult, err
	}
	if (tc.Name == tools.NameWrite || tc.Name == tools.NameEdit) && execResult.PreFilePath != "" {
		execResult.LSPReviews = speculativeWriteToolLSPReviews(s.tools, tc.Name, execResult.PreFilePath)
	}
	truncated := tools.TruncateOutputWithOptions(result, s.sessionDir, tools.TruncateOptions{ArtifactKey: artifactKey})
	content := tools.NormalizeEmptySuccessOutput(tc.Name, truncated.Content, nil)
	content = tools.AppendArtifactGuidance(content, truncated,
		"Use Grep to search the full content or Read with offset/limit to view specific sections.")
	execResult.Result = content
	return execResult, nil
}
