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

const (
	mainToolOutputGuidance = "Process it with Read(path, limit, offset) or Grep(path=...) in chunks. Use the Delegate tool only when you need a separate agent for substantial multi-step work on this content."
	subToolOutputGuidance  = "Use Grep to search the full content or Read with offset/limit to view specific sections."
)

type toolExecutionPipeline struct {
	agentID      string
	eventAgentID string
	taskID       string
	sessionDir   string
	registry     *tools.Registry
	fileTrack    *filelock.FileTracker
	eventSender  tools.EventSender
	emit         func(AgentEvent)
	guidance     string
	logPrefix    string

	currentRuleset                func() permission.Ruleset
	refreshRulesetAfterRuleIntent func(toolName string, intent *ConfirmRuleIntent) permission.Ruleset
	isInternalTool                func(string) bool
	confirm                       ConfirmFunc
	currentTurnID                 func() uint64
	fireHook                      func(context.Context, string, uint64, map[string]any) (*hook.Result, error)
	updatePending                 func(PendingToolCall)
	reservedToolError             func(string) error
}

func (p toolExecutionPipeline) execute(ctx context.Context, tc message.ToolCall, fireHook bool) (ToolExecutionResult, error) {
	execResult := ToolExecutionResult{EffectiveArgsJSON: string(tc.Args)}
	if p.reservedToolError != nil {
		if err := p.reservedToolError(tc.Name); err != nil {
			return execResult, err
		}
	}

	if err := p.applyPermission(ctx, &tc, &execResult); err != nil {
		return execResult, err
	}
	if fireHook {
		if err := p.applyToolHook(ctx, &tc, &execResult); err != nil {
			return execResult, err
		}
	}
	if err := validateToolCallArguments(p.registry, tc, p.logPrefix, p.agentID); err != nil {
		return execResult, err
	}

	execResult.PreFilePath, execResult.PreContent, execResult.PreExisted = agentdiff.CapturePreWriteState(tc)
	agentCtx := buildToolExecContext(ctx, tc, p.agentID, p.taskID, p.sessionDir, p.eventSender, p.emit)
	artifactKey := toolCallArtifactKey(tc)

	trackedFilePath, deleteLocks, err := prepareTrackedToolFileAccess(p.fileTrack, p.agentID, tc)
	if err != nil {
		return execResult, err
	}
	if deleteLocks != nil {
		defer deleteLocks.Release()
	}
	if err := ensureTrackedEditPreconditions(p.fileTrack, p.agentID, trackedFilePath, tc.Name); err != nil {
		return execResult, wrapTrackedWriteError(err)
	}
	if releaseWrite, err := acquireTrackedWriteLock(p.fileTrack, p.agentID, trackedFilePath, tc.Name); err != nil {
		return execResult, err
	} else if releaseWrite != nil {
		defer releaseWrite()
	}

	result, err := p.registry.Execute(agentCtx, tc.Name, llm.UnwrapToolArgs(tc.Args))
	if err != nil {
		if result != "" {
			execResult.Result = formatToolExecutionOutput(result, p.sessionDir, artifactKey, tc.Name, err, p.guidance)
		}
		return execResult, err
	}
	if deleteLocks != nil {
		deleteLocks.Commit(result)
		execResult.FileState = buildDeleteFileStateFromResult(result)
	}
	p.applySuccessfulFileState(&execResult, tc, trackedFilePath)
	execResult.Result = formatToolExecutionOutput(result, p.sessionDir, artifactKey, tc.Name, nil, p.guidance)
	return execResult, nil
}

func (p toolExecutionPipeline) executeSpeculative(ctx context.Context, tc message.ToolCall) (ToolExecutionResult, error) {
	execResult := ToolExecutionResult{EffectiveArgsJSON: string(tc.Args)}
	if err := validateToolArgsAgainstSchema(p.registry, tc.Name, tc.Args); err != nil {
		return execResult, err
	}
	execResult.PreFilePath, execResult.PreContent, execResult.PreExisted = agentdiff.CapturePreWriteState(tc)
	if tc.Name == tools.NameEdit {
		if path := toolPathFromArgs(tc.Args); path != "" {
			if err := ensureTrackedEditPreconditions(p.fileTrack, p.agentID, path, tc.Name); err != nil {
				return execResult, wrapTrackedWriteError(err)
			}
		}
	}
	hooks, err := prepareSpeculativeToolCall(tc, p.fileTrack, p.agentID)
	if err != nil {
		return execResult, err
	}
	execResult.speculativeHooks = hooks

	agentCtx := buildToolExecContext(ctx, tc, p.agentID, p.taskID, p.sessionDir, p.eventSender, p.emit)
	artifactKey := toolCallArtifactKey(tc)
	result, err := p.registry.Execute(agentCtx, tc.Name, llm.UnwrapToolArgs(tc.Args))
	if err != nil {
		rollbackSpeculativeToolHooks(execResult)
		if result != "" {
			execResult.Result = formatToolExecutionOutput(result, p.sessionDir, artifactKey, tc.Name, err, p.guidance)
		}
		return execResult, err
	}
	applySpeculativeFileState(&execResult, p.registry, tc, result)
	execResult.Result = formatToolExecutionOutput(result, p.sessionDir, artifactKey, tc.Name, nil, p.guidance)
	return execResult, nil
}

func (p toolExecutionPipeline) applyPermission(ctx context.Context, tc *message.ToolCall, execResult *ToolExecutionResult) error {
	if p.currentRuleset == nil {
		return nil
	}
	ruleset := p.currentRuleset()
	if len(ruleset) == 0 {
		return nil
	}
	if p.isInternalTool != nil && p.isInternalTool(tc.Name) {
		return nil
	}

	decision := evaluateToolPermission(ruleset, tc.Name, tc.Args)
	switch decision.Action {
	case permission.ActionDeny:
		logToolPermissionDenied(p.logPrefix, p.agentID, tc.Name, decision.MatchArgument)
		return wrapToolPermissionDenied(tc.Name)
	case permission.ActionAsk:
		if tc.Name == tools.NameDone {
			return nil
		}
		if p.confirm == nil {
			return wrapToolRequiresConfirmation(tc.Name)
		}
		resp, err := p.confirm(ctx, tc.Name, string(tc.Args), decision.NeedsApprovalPaths, decision.AlreadyAllowedPaths)
		if err != nil {
			return wrapToolConfirmationFailed(tc.Name, err)
		}
		if !resp.Approved {
			denyReason := normalizeDenyReason(resp.DenyReason)
			logToolRejectedByUser(p.logPrefix, p.agentID, tc.Name, decision.MatchArgument, denyReason)
			return wrapToolRejectedByUser(tc.Name, denyReason)
		}
		if resp.RuleIntent != nil && p.refreshRulesetAfterRuleIntent != nil {
			ruleset = p.refreshRulesetAfterRuleIntent(tc.Name, resp.RuleIntent)
		}
		originalArgs := append(json.RawMessage(nil), tc.Args...)
		editedArgs, err := applyConfirmedArgsEdits(p.registry, ruleset, tc.Name, tc.Args, resp.FinalArgsJSON)
		if err != nil {
			return err
		}
		tc.Args = editedArgs
		execResult.EffectiveArgsJSON = string(tc.Args)
		execResult.Audit = buildToolArgsAudit(originalArgs, tc.Args, resp.EditSummary)
		p.notePendingToolCall(*tc, execResult)
	case permission.ActionAllow:
		// no-op
	}
	return nil
}

func (p toolExecutionPipeline) applyToolHook(ctx context.Context, tc *message.ToolCall, execResult *ToolExecutionResult) error {
	if p.fireHook == nil {
		return nil
	}
	turnID := uint64(0)
	if p.currentTurnID != nil {
		turnID = p.currentTurnID()
	}
	hookResult, hookErr := p.fireHook(ctx, hook.OnToolCall, turnID, buildToolHookData(*tc))
	if hookErr != nil || hookResult == nil {
		return nil
	}
	switch hookResult.Action {
	case hook.ActionBlock:
		msg := "blocked by hook"
		if hookResult.Message != "" {
			msg = hookResult.Message
		}
		return fmt.Errorf("tool %q %s", tc.Name, msg)
	case hook.ActionModify:
		modified, ok := hookResult.Data.(map[string]any)
		if !ok {
			return nil
		}
		newArgs, ok := modified["args"]
		if !ok {
			return nil
		}
		raw, err := json.Marshal(newArgs)
		if err != nil {
			return nil
		}
		originalArgs := append(json.RawMessage(nil), tc.Args...)
		tc.Args = raw
		execResult.EffectiveArgsJSON = string(tc.Args)
		execResult.Audit = syncAuditEffectiveArgs(execResult.Audit, originalArgs, tc.Args)
		p.notePendingToolCall(*tc, execResult)
	}
	return nil
}

func (p toolExecutionPipeline) notePendingToolCall(tc message.ToolCall, execResult *ToolExecutionResult) {
	pending := PendingToolCall{
		CallID:   tc.ID,
		Name:     tc.Name,
		AgentID:  p.eventAgentID,
		ArgsJSON: execResult.EffectiveArgsJSON,
		Audit:    execResult.Audit,
	}
	if p.updatePending != nil {
		p.updatePending(pending)
	}
	if p.emit != nil {
		p.emit(ToolCallUpdateEvent{ID: tc.ID, Name: tc.Name, ArgsJSON: execResult.EffectiveArgsJSON, ArgsStreamingDone: true, AgentID: p.eventAgentID})
	}
}

func (p toolExecutionPipeline) applySuccessfulFileState(execResult *ToolExecutionResult, tc message.ToolCall, trackedFilePath string) {
	if tc.Name == tools.NameRead && trackedFilePath != "" {
		execResult.FileState = buildReadFileState(trackedFilePath)
		if p.fileTrack != nil {
			if hash := firstReadHashForPath(execResult.FileState, trackedFilePath); hash != "" {
				p.fileTrack.TrackRead(trackedFilePath, p.agentID, hash)
			}
		}
		return
	}
	if (tc.Name == tools.NameWrite || tc.Name == tools.NameEdit) && trackedFilePath != "" {
		execResult.FileState = buildWriteFileState(trackedFilePath)
		execResult.LSPReviews = speculativeWriteToolLSPReviews(p.registry, tc.Name, trackedFilePath)
	}
}

func validateToolCallArguments(registry *tools.Registry, tc message.ToolCall, logPrefix, agentID string) error {
	if llm.IsMalformedArgs(tc.Args) {
		if logPrefix != "" {
			log.Warnf("%s tool call has malformed args, returning guidance error agent=%v tool=%v", logPrefix, agentID, tc.Name)
		} else {
			log.Warnf("tool call has malformed args (sentinel detected), returning guidance error tool=%v instance=%v", tc.Name, agentID)
		}
		return fmt.Errorf(
			"tool %q was called with malformed arguments (likely due to output "+
				"truncation at max_tokens). Please reduce the number of parallel "+
				"tool calls and retry with properly structured JSON arguments "+
				"matching the tool's input schema",
			tc.Name,
		)
	}
	if llm.IsEmptyArgs(tc.Args) {
		if tool, ok := registry.Get(tc.Name); ok {
			if req := llm.RequiredFields(tool.Parameters()); len(req) > 0 {
				if logPrefix != "" {
					log.Warnf("%s tool call has empty args but tool requires parameters agent=%v tool=%v required=%v", logPrefix, agentID, tc.Name, req)
				} else {
					log.Warnf("tool call has empty args but tool requires parameters tool=%v required=%v", tc.Name, req)
				}
				return fmt.Errorf(
					"tool %q was called with empty arguments {}. This typically "+
						"happens when the model's output was truncated at max_tokens. "+
						"Please reduce the number of parallel tool calls and retry "+
						"with the complete required parameters: %v",
					tc.Name, req,
				)
			}
		}
	}
	return validateToolArgsAgainstSchema(registry, tc.Name, tc.Args)
}

func prepareTrackedToolFileAccess(track *filelock.FileTracker, agentID string, tc message.ToolCall) (string, *deleteLockSet, error) {
	switch tc.Name {
	case tools.NameDelete:
		locks, err := acquireDeleteLocks(track, agentID, tc.Args)
		if err != nil {
			var ext *filelock.ExternalModificationError
			if errors.As(err, &ext) {
				return "", nil, err
			}
			return "", nil, fmt.Errorf("file conflict: %w", err)
		}
		return "", locks, nil
	case tools.NameRead, tools.NameWrite, tools.NameEdit:
		return toolPathFromArgs(tc.Args), nil, nil
	default:
		return "", nil, nil
	}
}

func acquireTrackedWriteLock(track *filelock.FileTracker, agentID, path, toolName string) (func(), error) {
	if track == nil || path == "" || (toolName != tools.NameWrite && toolName != tools.NameEdit) {
		return nil, nil
	}
	currentHash := computeFileHash(path)
	if err := track.AcquireWrite(path, agentID, currentHash); err != nil {
		return nil, wrapTrackedWriteError(err)
	}
	return func() {
		newHash := computeFileHash(path)
		track.ReleaseWrite(path, agentID, newHash)
	}, nil
}

func toolPathFromArgs(args json.RawMessage) string {
	var parsed struct {
		Path string `json:"path"`
	}
	if json.Unmarshal(llm.UnwrapToolArgs(args), &parsed) != nil {
		return ""
	}
	return parsed.Path
}

func applySpeculativeFileState(execResult *ToolExecutionResult, registry *tools.Registry, tc message.ToolCall, result string) {
	switch tc.Name {
	case tools.NameRead:
		if path := toolPathFromArgs(tc.Args); path != "" {
			execResult.FileState = buildReadFileState(path)
		}
	case tools.NameWrite, tools.NameEdit:
		if path := toolPathFromArgs(tc.Args); path != "" {
			execResult.FileState = buildWriteFileState(path)
		}
	case tools.NameDelete:
		execResult.FileState = buildDeleteFileStateFromResult(result)
	}
	if (tc.Name == tools.NameWrite || tc.Name == tools.NameEdit) && execResult.PreFilePath != "" {
		execResult.LSPReviews = speculativeWriteToolLSPReviews(registry, tc.Name, execResult.PreFilePath)
	}
}

func formatToolExecutionOutput(result, sessionDir, artifactKey, toolName string, execErr error, guidance string) string {
	truncated := tools.TruncateOutputWithOptions(result, sessionDir, tools.TruncateOptions{ArtifactKey: artifactKey})
	content := tools.NormalizeEmptySuccessOutput(toolName, truncated.Content, execErr)
	return tools.AppendArtifactGuidance(content, truncated, guidance)
}

func toolCallArtifactKey(tc message.ToolCall) string {
	artifactKey := strings.TrimSpace(tc.ID)
	if artifactKey == "" {
		artifactKey = tc.Name + "-anonymous"
	}
	return artifactKey
}

func logToolPermissionDenied(prefix, agentID, toolName, matchArgument string) {
	if prefix != "" {
		log.Warnf("%s tool call denied by permission agent=%v tool=%v argument=%v", prefix, agentID, toolName, matchArgument)
		return
	}
	log.Warnf("tool call denied by permission tool=%v argument=%v", toolName, matchArgument)
}

func logToolRejectedByUser(prefix, agentID, toolName, matchArgument, denyReason string) {
	if prefix != "" {
		log.Infof("%s tool call rejected by user agent=%v tool=%v argument=%v deny_reason=%v", prefix, agentID, toolName, matchArgument, denyReason)
		return
	}
	log.Infof("tool call rejected by user tool=%v argument=%v deny_reason=%v", toolName, matchArgument, denyReason)
}

func commitPromotedReadToolSideEffects(track *filelock.FileTracker, agentID, toolName, argsJSON string, fileState *message.ToolFileState) {
	if toolName != tools.NameRead || track == nil {
		return
	}
	var parsed struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &parsed); err != nil {
		return
	}
	if strings.TrimSpace(parsed.Path) == "" {
		return
	}
	hash := firstReadHashForPath(fileState, parsed.Path)
	if hash == "" {
		hash = computeFileHash(parsed.Path)
	}
	track.TrackRead(parsed.Path, agentID, hash)
}
