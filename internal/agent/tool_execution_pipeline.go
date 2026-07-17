package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
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
	mainToolOutputGuidance = "Process it with " + tools.NameRead + "(path, limit, offset) or " + tools.NameGrep + "(paths=[...]) in chunks. Use the " + tools.NameDelegate + " tool only when you need a separate agent for substantial multi-step work on this content."
	subToolOutputGuidance  = "Use " + tools.NameGrep + " to search the full content or " + tools.NameRead + " with offset/limit to view specific sections."
)

type toolExecutionPipeline struct {
	agentID       string
	eventAgentID  string
	taskID        string
	sessionDir    string
	registry      *tools.Registry
	fileTrack     *filelock.FileTracker
	fileBackups   *fileBackupManager
	eventSender   tools.EventSender
	emit          func(AgentEvent)
	guidance      string
	logPrefix     string
	projectRoot   string
	writeScope    *tools.WriteScope
	writeScopeDir string

	currentRuleset                func() permission.Ruleset
	refreshRulesetAfterRuleIntent func(toolName string, intent *ConfirmRuleIntent) permission.Ruleset
	isInternalTool                func(string) bool
	confirm                       ConfirmFunc
	currentTurnID                 func() uint64
	fireHook                      func(context.Context, string, uint64, map[string]any) (*hook.Result, error)
	updatePending                 func(PendingToolCall)
	reservedToolError             func(string) error
	bypassPermission              func(string) bool
	visibleToolNames              func() map[string]struct{}
}

func (p toolExecutionPipeline) validateWriteScope(tc message.ToolCall) error {
	if p.writeScope == nil {
		return nil
	}
	scope := p.writeScope.Normalized()
	if scope.Empty() {
		return nil
	}
	if _, ok := p.registry.Get(tc.Name); !ok {
		return nil
	}
	if tc.Name == tools.NameShell {
		return fmt.Errorf("shell is unavailable for a scoped SubAgent task because arbitrary command side effects cannot be path-validated")
	}
	if scope.ReadOnly {
		if tools.IsFileMutation(tc.Name) || tc.Name == tools.NameSpawn {
			return fmt.Errorf("tool %q is unavailable because this SubAgent task is read-only", tc.Name)
		}
		if !writeScopeKnownNonWorkspaceMutation(tc.Name) {
			tool, _ := p.registry.Get(tc.Name)
			if tool != nil && !tool.IsReadOnly() {
				return fmt.Errorf("tool %q is unavailable because this SubAgent task is read-only and the runtime cannot prove it leaves the workspace unchanged", tc.Name)
			}
		}
		return nil
	}
	if writeScopeKnownNonWorkspaceMutation(tc.Name) {
		return nil
	}
	if !tools.IsFileMutation(tc.Name) {
		if tool, _ := p.registry.Get(tc.Name); tool != nil && !tool.IsReadOnly() {
			return fmt.Errorf("tool %q is unavailable for a path-scoped SubAgent task because the runtime cannot validate its workspace mutations", tc.Name)
		}
		return nil
	}
	if len(scope.Files) == 0 && len(scope.PathPrefix) == 0 {
		return fmt.Errorf("tool %q cannot be path-validated because expected_write_scope declares only logical modules", tc.Name)
	}
	baseDir := p.writeScopeDir
	if strings.TrimSpace(baseDir) == "" {
		baseDir = p.projectRoot
	}
	paths, err := writeScopeToolPaths(tc, baseDir)
	if err != nil {
		return fmt.Errorf("validate expected_write_scope for %s: %w", tc.Name, err)
	}
	for _, path := range paths {
		if !writeScopeAllowsPath(scope, path, baseDir) {
			return fmt.Errorf("tool %q target %q is outside this SubAgent task's expected_write_scope", tc.Name, path)
		}
	}
	return nil
}

func writeScopeKnownNonWorkspaceMutation(name string) bool {
	switch name {
	case tools.NameComplete, tools.NameNotify, tools.NameEscalate, tools.NameCancel, tools.NameDelegate, tools.NameHandoff, tools.NameSaveArtifact, tools.NameReadArtifact, tools.NameSpawnStatus, tools.NameSpawnStop, tools.NameTodoWrite:
		return true
	default:
		return false
	}
}

func writeScopeToolPaths(tc message.ToolCall, baseDir string) ([]string, error) {
	switch tc.Name {
	case tools.NameWrite, tools.NameEdit, tools.NamePatch:
		path := tools.ExtractEditPathFromArgsInDir(llm.UnwrapToolArgs(tc.Args), baseDir)
		if strings.TrimSpace(path) == "" {
			return nil, fmt.Errorf("missing or invalid path")
		}
		return []string{path}, nil
	case tools.NameDelete:
		req, err := tools.DecodeDeleteRequestInDir(llm.UnwrapToolArgs(tc.Args), baseDir)
		if err != nil {
			return nil, err
		}
		return req.Paths, nil
	default:
		return nil, nil
	}
}

func writeScopeAllowsPath(scope tools.WriteScope, target, baseDir string) bool {
	target = normalizedScopeAbsPath(target, baseDir)
	for _, file := range scope.Files {
		if target == normalizedScopeAbsPath(file, baseDir) {
			return true
		}
	}
	for _, prefix := range scope.PathPrefix {
		if pathWithinScope(normalizedScopeAbsPath(prefix, baseDir), target) {
			return true
		}
	}
	return false
}

func normalizedScopeAbsPath(path, baseDir string) string {
	path = strings.TrimSpace(path)
	if !filepath.IsAbs(path) {
		path = filepath.Join(baseDir, path)
	}
	abs, err := filepath.Abs(path)
	if err == nil {
		path = abs
	}
	path = filepath.Clean(path)
	return resolveScopeSymlinks(path)
}

func resolveScopeSymlinks(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return filepath.Clean(resolved)
	}
	current := path
	var tail []string
	for {
		parent := filepath.Dir(current)
		if parent == current {
			return path
		}
		tail = append([]string{filepath.Base(current)}, tail...)
		current = parent
		resolved, err := filepath.EvalSymlinks(current)
		if err != nil {
			continue
		}
		return filepath.Clean(filepath.Join(append([]string{resolved}, tail...)...))
	}
}

func pathWithinScope(base, target string) bool {
	rel, err := filepath.Rel(base, target)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func (p toolExecutionPipeline) execute(ctx context.Context, tc message.ToolCall, fireHook bool) (ToolExecutionResult, error) {
	tc.Name = tools.NormalizeName(tc.Name)
	execResult := ToolExecutionResult{EffectiveArgsJSON: string(tc.Args)}
	if err := p.validateWriteScope(tc); err != nil {
		return execResult, err
	}
	if p.reservedToolError != nil {
		if err := p.reservedToolError(tc.Name); err != nil {
			return execResult, err
		}
	}

	if p.visibleToolNames != nil {
		if err := p.checkVisible(tc.Name); err != nil {
			return execResult, err
		}
	}
	if err := p.validateKnownTool(tc.Name); err != nil {
		return execResult, err
	}

	if err := p.applyPermission(ctx, &tc, &execResult); err != nil {
		return execResult, err
	}
	if fireHook {
		modified, err := p.applyToolHook(ctx, &tc, &execResult)
		if err != nil {
			return execResult, err
		}
		if modified {
			if tc.Name == tools.NameDelegate {
				if err := p.applyPermission(ctx, &tc, &execResult); err != nil {
					return execResult, err
				}
			}
		}
	}
	if err := validateToolCallArguments(p.registry, tc, p.logPrefix, p.agentID); err != nil {
		return execResult, err
	}

	agentCtx := buildToolExecContext(ctx, tc, p.agentID, p.taskID, p.sessionDir, p.eventSender, p.emit)
	imageSink := &tools.ImageCollector{}
	agentCtx = tools.WithImageSink(agentCtx, imageSink)
	artifactKey := toolCallArtifactKey(tc)

	trackedFilePath, deleteLocks, err := p.prepareTrackedToolFileAccess(tc)
	if err != nil {
		return execResult, err
	}
	if deleteLocks != nil {
		defer deleteLocks.Release()
	}
	releaseWrite, writeStatus, err := acquireTrackedWriteLock(p.fileTrack, p.agentID, trackedFilePath, tc.Name)
	if err != nil {
		return execResult, err
	} else if releaseWrite != nil {
		defer releaseWrite()
	}
	staleWrite := writeStatus.ExternalChanged || (deleteLocks != nil && deleteLocks.stale)
	if tc.Name == tools.NamePatch {
		plan, err := agentdiff.CapturePatchPlan(agentCtx, tc, p.projectRoot)
		if err != nil {
			return execResult, err
		}
		execResult.PreFilePath, execResult.PreContent, execResult.PreExisted = plan.Path, plan.Before, true
		execResult.ModelContextNote = plan.ModelContextNote
		agentCtx = tools.ContextWithPatchPlan(agentCtx, plan)
	} else {
		execResult.PreFilePath, execResult.PreContent, execResult.PreExisted = agentdiff.CapturePreWriteState(tc, p.projectRoot)
	}

	backupOutcome := p.backupRiskyPreWriteState(tc, trackedFilePath, staleWrite, execResult.PreContent, execResult.PreExisted, deleteLocks)

	result, err := p.registry.Execute(agentCtx, tc.Name, llm.UnwrapToolArgs(tc.Args))
	execResult.Images = imageSink.Drain()
	if err != nil {
		if staleWrite && (tc.Name == tools.NameEdit || tc.Name == tools.NamePatch) {
			err = wrapStaleEditError(err)
		}
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
	result = appendBackupNotes(result, staleWrite, staleWritePathCount(trackedFilePath, deleteLocks), backupOutcome)
	execResult.Result = formatToolExecutionOutput(result, p.sessionDir, artifactKey, tc.Name, nil, p.guidance)
	return execResult, nil
}

func (p toolExecutionPipeline) executeSpeculative(ctx context.Context, tc message.ToolCall) (ToolExecutionResult, error) {
	tc.Name = tools.NormalizeName(tc.Name)
	if err := p.validateWriteScope(tc); err != nil {
		return ToolExecutionResult{EffectiveArgsJSON: string(tc.Args)}, err
	}
	execResult := ToolExecutionResult{EffectiveArgsJSON: string(tc.Args)}
	if p.visibleToolNames != nil {
		if err := p.checkVisible(tc.Name); err != nil {
			return execResult, err
		}
	}
	if err := p.validateKnownTool(tc.Name); err != nil {
		return execResult, err
	}
	if err := validateToolArgsAgainstSchema(p.registry, tc.Name, tc.Args); err != nil {
		return execResult, err
	}
	hooks, err := prepareSpeculativeToolCall(tc, p.registry, p.fileTrack, p.agentID, p.projectRoot)
	if err != nil {
		return execResult, err
	}
	execResult.speculativeHooks = hooks

	agentCtx := buildToolExecContext(ctx, tc, p.agentID, p.taskID, p.sessionDir, p.eventSender, p.emit)
	if tc.Name == tools.NameTodoWrite {
		agentCtx = tools.WithTodoWriteSpeculativePreview(agentCtx)
	}
	imageSink := &tools.ImageCollector{}
	agentCtx = tools.WithImageSink(agentCtx, imageSink)
	if tc.Name == tools.NamePatch {
		plan, err := agentdiff.CapturePatchPlan(agentCtx, tc, p.projectRoot)
		if err != nil {
			rollbackSpeculativeToolHooks(execResult)
			return execResult, err
		}
		execResult.PreFilePath, execResult.PreContent, execResult.PreExisted = plan.Path, plan.Before, true
		execResult.ModelContextNote = plan.ModelContextNote
		agentCtx = tools.ContextWithPatchPlan(agentCtx, plan)
	} else {
		execResult.PreFilePath, execResult.PreContent, execResult.PreExisted = agentdiff.CapturePreWriteState(tc, p.projectRoot)
	}
	artifactKey := toolCallArtifactKey(tc)
	staleWrite := hooks != nil && hooks.stale
	trackedFilePath := speculativeTrackedFilePath(tc.Name, execResult.PreFilePath, hooks)
	backupOutcome := p.backupRiskyPreWriteState(tc, trackedFilePath, staleWrite, execResult.PreContent, execResult.PreExisted, speculativeDeleteLocks(tc.Name, hooks))
	result, err := p.registry.Execute(agentCtx, tc.Name, llm.UnwrapToolArgs(tc.Args))
	execResult.Images = imageSink.Drain()
	if err != nil {
		rollbackSpeculativeToolHooks(execResult)
		if staleWrite && (tc.Name == tools.NameEdit || tc.Name == tools.NamePatch) {
			err = wrapStaleEditError(err)
		}
		if result != "" {
			execResult.Result = formatToolExecutionOutput(result, p.sessionDir, artifactKey, tc.Name, err, p.guidance)
		}
		return execResult, err
	}
	applySpeculativeFileState(&execResult, p.registry, tc, result, p.projectRoot)
	result = appendBackupNotes(result, staleWrite, speculativeStaleWritePathCount(tc.Name, trackedFilePath, hooks), backupOutcome)
	execResult.Result = formatToolExecutionOutput(result, p.sessionDir, artifactKey, tc.Name, nil, p.guidance)
	return execResult, nil
}

func speculativeTrackedFilePath(toolName, preFilePath string, hooks *speculativeToolHooks) string {
	if toolName != tools.NameDelete && hooks != nil && len(hooks.paths) == 1 {
		return hooks.paths[0]
	}
	return preFilePath
}

func speculativeDeleteLocks(toolName string, hooks *speculativeToolHooks) *deleteLockSet {
	if toolName != tools.NameDelete || hooks == nil || len(hooks.paths) == 0 {
		return nil
	}
	return &deleteLockSet{paths: hooks.paths}
}

func speculativeStaleWritePathCount(toolName, trackedPath string, hooks *speculativeToolHooks) int {
	if toolName == tools.NameDelete && hooks != nil {
		return len(hooks.paths)
	}
	if strings.TrimSpace(trackedPath) != "" {
		return 1
	}
	return 0
}

func staleWritePathCount(trackedFilePath string, deleteLocks *deleteLockSet) int {
	if deleteLocks != nil {
		return len(deleteLocks.paths)
	}
	if strings.TrimSpace(trackedFilePath) != "" {
		return 1
	}
	return 0
}

// checkVisible rejects tool calls for edit-family tools (patch ↔ edit) that are
// not in the current model-appropriate visible set. This enforces the per-model
// edit tool filter at execution time so a model cannot circumvent the declared
// tool surface by calling the sibling tool name from conversation history.
// Tools outside the edit family are governed by the existing permission/registry
// flow and do not need a secondary visibility gate.
func (p toolExecutionPipeline) checkVisible(name string) error {
	visible := p.visibleToolNames()
	if visible == nil {
		return nil
	}
	n := tools.NormalizeName(name)
	if n != tools.NamePatch && n != tools.NameEdit {
		return nil
	}
	if _, ok := visible[n]; ok {
		return nil
	}
	switch n {
	case tools.NamePatch:
		if _, ok := visible[tools.NameEdit]; ok {
			return fmt.Errorf("tool %q is not available for the current model. Use %q instead (the %q file-modification tool)", tools.NamePatch, tools.NameEdit, tools.NameEdit)
		}
	case tools.NameEdit:
		if _, ok := visible[tools.NamePatch]; ok {
			return fmt.Errorf("tool %q is not available for the current model. Use %q instead (the %q file-modification tool)", tools.NameEdit, tools.NamePatch, tools.NamePatch)
		}
	}
	return fmt.Errorf("tool %q is not available for the current model", name)
}

func (p toolExecutionPipeline) validateKnownTool(name string) error {
	name = tools.NormalizeName(name)
	if name == "" {
		return fmt.Errorf("malformed tool call: missing tool name")
	}
	if p.isInternalTool != nil && p.isInternalTool(name) {
		return nil
	}
	if p.registry == nil {
		return nil
	}
	if _, ok := p.registry.Get(name); !ok {
		return fmt.Errorf("tool not found: %s", name)
	}
	return nil
}

func (p toolExecutionPipeline) applyPermission(ctx context.Context, tc *message.ToolCall, execResult *ToolExecutionResult) error {
	if p.bypassPermission != nil && p.bypassPermission(tc.Name) {
		return nil
	}
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
		resp, err := p.confirm(ctx, tc.Name, string(tc.Args), decision.NeedsApprovalPaths, decision.AlreadyAllowedPaths, decision.NeedsApprovalRules, decision.AlreadyAllowedRules)
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

func (p toolExecutionPipeline) applyToolHook(ctx context.Context, tc *message.ToolCall, execResult *ToolExecutionResult) (bool, error) {
	if p.fireHook == nil {
		return false, nil
	}
	turnID := uint64(0)
	if p.currentTurnID != nil {
		turnID = p.currentTurnID()
	}
	hookResult, hookErr := p.fireHook(ctx, hook.OnToolCall, turnID, buildToolHookData(*tc, p.projectRoot))
	if hookErr != nil || hookResult == nil {
		return false, nil
	}
	switch hookResult.Action {
	case hook.ActionBlock:
		msg := "blocked by hook"
		if hookResult.Message != "" {
			msg = hookResult.Message
		}
		return false, fmt.Errorf("tool %q %s", tc.Name, msg)
	case hook.ActionModify:
		modified, ok := hookResult.Data.(map[string]any)
		if !ok {
			return false, nil
		}
		newArgs, ok := modified["args"]
		if !ok {
			return false, nil
		}
		raw, err := json.Marshal(newArgs)
		if err != nil {
			return false, nil
		}
		originalArgs := append(json.RawMessage(nil), tc.Args...)
		tc.Args = raw
		execResult.EffectiveArgsJSON = string(tc.Args)
		execResult.Audit = syncAuditEffectiveArgs(execResult.Audit, originalArgs, tc.Args)
		p.notePendingToolCall(*tc, execResult)
		return true, nil
	}
	return false, nil
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
				p.fileTrack.TrackSnapshot(trackedFilePath, p.agentID, hash)
			}
		}
		return
	}
	if (tc.Name == tools.NameWrite || tc.Name == tools.NameEdit || tc.Name == tools.NamePatch) && trackedFilePath != "" {
		execResult.FileState = buildWriteFileState(trackedFilePath)
		if p.fileTrack != nil {
			if hash := firstWriteHashForPath(execResult.FileState, trackedFilePath); hash != "" {
				p.fileTrack.TrackSnapshot(trackedFilePath, p.agentID, hash)
			}
		}
		execResult.LSPReviews = speculativeWriteToolLSPReviews(p.registry, tc.Name, trackedFilePath)
	}
}

func validateToolCallArguments(registry *tools.Registry, tc message.ToolCall, logPrefix, agentID string) error {
	abnormality := classifyToolArgsAbnormality(registry, tc.Name, tc.Args)
	if abnormality.Malformed {
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
	if abnormality.EmptyRequired {
		if logPrefix != "" {
			log.Warnf("%s tool call has empty args but tool requires parameters agent=%v tool=%v required=%v", logPrefix, agentID, tc.Name, abnormality.RequiredFields)
		} else {
			log.Warnf("tool call has empty args but tool requires parameters tool=%v required=%v", tc.Name, abnormality.RequiredFields)
		}
		return fmt.Errorf(
			"tool %q was called with empty arguments {}. This typically "+
				"happens when the model's output was truncated at max_tokens. "+
				"Please reduce the number of parallel tool calls and retry "+
				"with the complete required parameters: %v",
			tc.Name, abnormality.RequiredFields,
		)
	}
	return validateToolArgsAgainstSchema(registry, tc.Name, tc.Args)
}

func (p toolExecutionPipeline) prepareTrackedToolFileAccess(tc message.ToolCall) (string, *deleteLockSet, error) {
	switch tc.Name {
	case tools.NameDelete:
		locks, err := acquireDeleteLocks(p.fileTrack, p.agentID, tc.Args)
		if err != nil {
			return "", nil, fmt.Errorf("file conflict: %w", err)
		}
		return "", locks, nil
	case tools.NameEdit, tools.NamePatch:
		return tools.ExtractEditPathFromArgsInDir(tc.Args, p.projectRoot), nil, nil
	case tools.NameRead, tools.NameWrite:
		return toolPathFromArgs(tc.Args), nil, nil
	default:
		return "", nil, nil
	}
}

func acquireTrackedWriteLock(track *filelock.FileTracker, agentID, path, toolName string) (func(), filelock.WriteStatus, error) {
	var status filelock.WriteStatus
	if track == nil || path == "" || (toolName != tools.NameWrite && toolName != tools.NameEdit && toolName != tools.NamePatch) {
		return nil, status, nil
	}
	currentHash := computeFileHash(path)
	var err error
	status, err = track.AcquireWriteStatus(path, agentID, currentHash)
	if err != nil {
		return nil, status, wrapTrackedWriteError(err)
	}
	return func() {
		newHash := computeFileHash(path)
		track.ReleaseWrite(path, agentID, newHash)
	}, status, nil
}

func wrapTrackedWriteError(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("file conflict: %w", err)
}

func (p toolExecutionPipeline) backupRiskyPreWriteState(tc message.ToolCall, trackedPath string, stale bool, preContent string, preExisted bool, deleteLocks *deleteLockSet) fileBackupOutcome {
	if p.fileBackups == nil || !tools.IsFileMutation(tc.Name) {
		return fileBackupOutcome{}
	}
	switch tc.Name {
	case tools.NameEdit, tools.NamePatch:
		if !stale || !preExisted || preContent == "" || trackedPath == "" {
			return fileBackupOutcome{}
		}
		backup, err := p.fileBackups.Backup(trackedPath, tc.Name, []byte(preContent))
		if err != nil {
			return fileBackupOutcome{Warning: err.Error()}
		}
		return fileBackupOutcome{Records: nonEmptyBackupRecords(backup)}
	case tools.NameWrite:
		if !stale || trackedPath == "" {
			return fileBackupOutcome{}
		}
		data, existed, err := readPreWriteBytes(trackedPath)
		if err != nil {
			return fileBackupOutcome{Warning: err.Error()}
		}
		if !existed || len(data) == 0 {
			return fileBackupOutcome{}
		}
		backup, err := p.fileBackups.Backup(trackedPath, tc.Name, data)
		if err != nil {
			return fileBackupOutcome{Warning: err.Error()}
		}
		return fileBackupOutcome{Records: nonEmptyBackupRecords(backup)}
	case tools.NameDelete:
		if deleteLocks == nil || len(deleteLocks.paths) == 0 {
			return fileBackupOutcome{}
		}
		if !stale {
			return fileBackupOutcome{}
		}
		backups := make([]fileBackupRecord, 0, len(deleteLocks.paths))
		var warnings []string
		for _, path := range deleteLocks.paths {
			data, existed, err := readPreWriteBytes(path)
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("%s: %v", path, err))
				continue
			}
			if !existed || len(data) == 0 {
				continue
			}
			backup, err := p.fileBackups.Backup(path, tc.Name, data)
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("%s: %v", path, err))
				continue
			}
			if backup.Path != "" {
				backups = append(backups, backup)
			}
		}
		return fileBackupOutcome{Records: backups, Warning: strings.Join(warnings, "; ")}
	default:
		return fileBackupOutcome{}
	}
}

func nonEmptyBackupRecords(backup fileBackupRecord) []fileBackupRecord {
	if strings.TrimSpace(backup.Path) == "" {
		return nil
	}
	return []fileBackupRecord{backup}
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

func applySpeculativeFileState(execResult *ToolExecutionResult, registry *tools.Registry, tc message.ToolCall, result, projectRoot string) {
	switch tc.Name {
	case tools.NameRead:
		if path := toolPathFromArgs(tc.Args); path != "" {
			execResult.FileState = buildReadFileState(path)
		}
	case tools.NameEdit, tools.NamePatch:
		if path := tools.ExtractEditPathFromArgsInDir(tc.Args, projectRoot); path != "" {
			execResult.FileState = buildWriteFileState(path)
		}
	case tools.NameWrite:
		if path := toolPathFromArgs(tc.Args); path != "" {
			execResult.FileState = buildWriteFileState(path)
		}
	case tools.NameDelete:
		execResult.FileState = buildDeleteFileStateFromResult(result)
	}
	if (tc.Name == tools.NameWrite || tc.Name == tools.NameEdit || tc.Name == tools.NamePatch) && execResult.PreFilePath != "" {
		execResult.LSPReviews = speculativeWriteToolLSPReviews(registry, tc.Name, execResult.PreFilePath)
	}
}

func formatToolExecutionOutput(result, sessionDir, artifactKey, toolName string, execErr error, guidance string) string {
	if toolName == tools.NameQuestion {
		return tools.NormalizeEmptySuccessOutput(toolName, result, execErr)
	}
	truncated := tools.TruncateOutputWithOptions(result, sessionDir, tools.TruncateOptions{ArtifactKey: artifactKey})
	content := tools.NormalizeEmptySuccessOutput(toolName, truncated.Content, execErr)
	return tools.AppendArtifactGuidance(content, truncated, guidance)
}

func wrapStaleEditError(err error) error {
	return fmt.Errorf("%w: file changed on disk since it was last read; hunk may be based on stale content; re-read the file and retry with current contents", err)
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
	track.TrackSnapshot(parsed.Path, agentID, hash)
}
