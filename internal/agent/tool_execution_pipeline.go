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
	governor      *resourceGovernor
	fileTrack     *filelock.FileTracker
	fileBackups   *fileBackupManager
	eventSender   tools.EventSender
	emit          func(AgentEvent)
	guidance      string
	logPrefix     string
	projectRoot   string
	toolBaseDir   string
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

func (p toolExecutionPipeline) effectiveToolBaseDir() string {
	if strings.TrimSpace(p.toolBaseDir) != "" {
		return p.toolBaseDir
	}
	return p.projectRoot
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
	case tools.NameApplyPatch:
		targets, err := tools.ApplyPatchTargets(llm.UnwrapToolArgs(tc.Args), baseDir)
		if err != nil {
			return nil, err
		}
		paths := tools.MutationTargetPaths(targets)
		if len(paths) == 0 {
			return nil, fmt.Errorf("missing or invalid path")
		}
		return paths, nil
	case tools.NameWrite, tools.NameEdit:
		path := ""
		if tc.Name == tools.NameEdit {
			path = trackedEditPathFromArgs(tc.Args, baseDir)
		} else {
			var args struct {
				Path string `json:"path"`
			}
			_ = json.Unmarshal(llm.UnwrapToolArgs(tc.Args), &args)
			path, _ = tools.ResolveToolPathInDir(args.Path, baseDir)
		}
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
	if err := normalizeCompatibleToolCallArgs(&tc, &execResult); err != nil {
		return execResult, err
	}
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
			if err := normalizeCompatibleToolCallArgs(&tc, &execResult); err != nil {
				return execResult, err
			}
			if err := p.applyPermission(ctx, &tc, &execResult); err != nil {
				return execResult, err
			}
		}
	}
	if err := validateToolCallArguments(p.registry, tc, p.logPrefix, p.agentID); err != nil {
		return execResult, err
	}
	if err := p.validateWriteScope(tc); err != nil {
		return execResult, err
	}

	agentCtx := buildToolExecContext(ctx, tc, p.agentID, p.taskID, p.sessionDir, p.eventSender, p.emit)
	imageSink := &tools.ImageCollector{}
	agentCtx = tools.WithImageSink(agentCtx, imageSink)
	readObservationSink := &tools.ReadObservationCollector{}
	agentCtx = tools.WithReadObservationSink(agentCtx, readObservationSink)
	deleteAuditSink := &tools.DeleteAuditCollector{}
	agentCtx = tools.WithDeleteAuditSink(agentCtx, deleteAuditSink)
	var patchDiffCollector *tools.ApplyPatchDiffCollector
	var patchMutation *speculativeFileMutation
	if tc.Name == tools.NameApplyPatch {
		patchDiffCollector = &tools.ApplyPatchDiffCollector{}
		agentCtx = tools.WithApplyPatchDiffCollector(agentCtx, patchDiffCollector)
		paths, pathErr := applyPatchToolPaths(tc.Args, p.effectiveToolBaseDir())
		if pathErr != nil {
			return execResult, pathErr
		}
		mutation, mutationErr := newSpeculativeFileMutation(p.fileTrack, p.agentID, paths, "")
		if mutationErr != nil {
			return execResult, wrapTrackedWriteError(mutationErr)
		}
		patchMutation = mutation
		defer patchMutation.Abort()
	}
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
		defer releaseWrite.release()
	}
	if err := requireObservedDestructiveWrite(p.fileTrack, p.agentID, tc.Name, trackedFilePath, writeStatus, releaseWrite); err != nil {
		return execResult, err
	}
	// Acquire the workspace lease after per-file tracking locks so that same-path
	// conflicts serialize on the cheaper file lock first; a blocking tool that
	// holds a tracked write can then yield its lease slot without deadlocking
	// against a sibling waiting for the same resource ordering.
	releaseLease, err := p.acquireWorkspaceLease(ctx, tc)
	if err != nil {
		return execResult, err
	}
	defer releaseLease()
	staleWrite := writeStatus.ExternalChanged
	if deleteLocks != nil {
		staleWrite = deleteLocks.hasStalePath()
	} else if patchMutation != nil {
		staleWrite = patchMutation.stale
	}
	if tc.Name == tools.NameApplyPatch {
		plan, err := agentdiff.CapturePatchPlan(agentCtx, tc, p.effectiveToolBaseDir())
		if err != nil {
			return execResult, err
		}
		execResult.PreFilePath, execResult.PreContent, execResult.PreExisted = plan.Path, plan.Before, plan.Existed
		execResult.ModelContextNote = plan.ModelContextNote
	} else {
		execResult.PreFilePath, execResult.PreContent, execResult.PreExisted = agentdiff.CapturePreWriteState(tc, p.effectiveToolBaseDir())
	}

	var backupOutcome fileBackupOutcome
	if patchMutation != nil {
		backupOutcome = p.backupRiskyPatchState(tc, patchMutation.stale, patchMutation.backupSources())
	} else {
		backupOutcome = p.backupRiskyPreWriteState(tc, trackedFilePath, staleWrite, execResult.PreContent, execResult.PreExisted, deleteLocks)
	}

	result, err := p.registry.Execute(agentCtx, tc.Name, llm.UnwrapToolArgs(tc.Args))
	execResult.Images = imageSink.Drain()
	deleteAudit, hasDeleteAudit := deleteAuditSink.Audit()
	if deleteLocks != nil && hasDeleteAudit {
		deleteLocks.CommitAudit(deleteAudit)
	}
	if err != nil {
		if staleWrite && (tc.Name == tools.NameEdit || tc.Name == tools.NameApplyPatch) {
			err = wrapStaleEditError(err)
		}
		if result != "" {
			execResult.Result = formatToolExecutionOutput(result, p.sessionDir, artifactKey, tc.Name, err, p.guidance)
		}
		return execResult, err
	}
	if deleteLocks != nil {
		if !hasDeleteAudit {
			deleteLocks.Commit(result)
		}
		execResult.FileState = buildDeleteFileStateFromResultInDir(result, p.effectiveToolBaseDir())
	}
	if patchMutation != nil {
		execResult.Diff = patchDiffCollector.Summary()
		patchMutation.CaptureAfter()
		execResult.FileState = patchMutation.postState()
		patchMutation.Commit()
		execResult.LSPReviews = lspReviewsForFileState(p.registry, tc.Name, execResult.FileState)
	}
	execResult.Result = result
	readObservation, _ := readObservationSink.Observation()
	p.applySuccessfulFileState(&execResult, tc, trackedFilePath, readObservation, releaseWrite)
	stalePathCount := staleWritePathCount(trackedFilePath, deleteLocks)
	if patchMutation != nil {
		stalePathCount = len(patchMutation.paths)
	}
	result = appendBackupNotes(result, staleWrite, stalePathCount, backupOutcome)
	execResult.Result = formatToolExecutionOutput(result, p.sessionDir, artifactKey, tc.Name, nil, p.guidance)
	return execResult, nil
}

func (p toolExecutionPipeline) executeSpeculative(ctx context.Context, tc message.ToolCall) (ToolExecutionResult, error) {
	tc.Name = tools.NormalizeName(tc.Name)
	execResult := ToolExecutionResult{EffectiveArgsJSON: string(tc.Args)}
	if err := normalizeCompatibleToolCallArgs(&tc, &execResult); err != nil {
		return execResult, err
	}
	if err := p.validateWriteScope(tc); err != nil {
		return execResult, err
	}
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
	releaseLease, err := p.acquireWorkspaceLease(ctx, tc)
	if err != nil {
		return execResult, err
	}
	// The lease covers only the speculative execution itself. Holding it until
	// commit/rollback would let one agent's stream-length speculation block
	// other agents and creates hold-and-wait cycles; post-execution conflicts
	// are instead detected by file tracking and hash-guarded rollback.
	defer releaseLease()
	hooks, err := prepareSpeculativeToolCall(tc, p.registry, p.fileTrack, p.agentID, p.effectiveToolBaseDir())
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
	readObservationSink := &tools.ReadObservationCollector{}
	agentCtx = tools.WithReadObservationSink(agentCtx, readObservationSink)
	var patchDiffCollector *tools.ApplyPatchDiffCollector
	if tc.Name == tools.NameApplyPatch {
		patchDiffCollector = &tools.ApplyPatchDiffCollector{}
		agentCtx = tools.WithApplyPatchDiffCollector(agentCtx, patchDiffCollector)
		plan, err := agentdiff.CapturePatchPlan(agentCtx, tc, p.effectiveToolBaseDir())
		if err != nil {
			rollbackSpeculativeToolHooks(execResult)
			return execResult, err
		}
		execResult.PreFilePath, execResult.PreContent, execResult.PreExisted = plan.Path, plan.Before, plan.Existed
		execResult.ModelContextNote = plan.ModelContextNote
	} else {
		execResult.PreFilePath, execResult.PreContent, execResult.PreExisted = agentdiff.CapturePreWriteState(tc, p.effectiveToolBaseDir())
	}
	artifactKey := toolCallArtifactKey(tc)
	staleWrite := hooks != nil && hooks.stale
	trackedFilePath := speculativeTrackedFilePath(tc.Name, execResult.PreFilePath, hooks)
	var backupOutcome fileBackupOutcome
	if tc.Name == tools.NameApplyPatch && hooks != nil && hooks.backupSources != nil {
		// A patch can touch several files, so back up every snapshot the
		// speculative mutation captured instead of the single tracked path.
		backupOutcome = p.backupRiskyPatchState(tc, staleWrite, hooks.backupSources())
	} else {
		backupOutcome = p.backupRiskyPreWriteState(tc, trackedFilePath, staleWrite, execResult.PreContent, execResult.PreExisted, speculativeDeleteLocks(tc.Name, hooks))
	}
	result, err := p.registry.Execute(agentCtx, tc.Name, llm.UnwrapToolArgs(tc.Args))
	execResult.Images = imageSink.Drain()
	if err != nil {
		rollbackSpeculativeToolHooks(execResult)
		if staleWrite && (tc.Name == tools.NameEdit || tc.Name == tools.NameApplyPatch) {
			err = wrapStaleEditError(err)
		}
		if result != "" {
			execResult.Result = formatToolExecutionOutput(result, p.sessionDir, artifactKey, tc.Name, err, p.guidance)
		}
		return execResult, err
	}
	if hooks != nil && hooks.captureAfter != nil {
		hooks.captureAfter()
	}
	if patchDiffCollector != nil {
		execResult.Diff = patchDiffCollector.Summary()
	}
	readObservation, _ := readObservationSink.Observation()
	applySpeculativeFileState(&execResult, p.registry, tc, result, p.effectiveToolBaseDir(), readObservation, hooks)
	result = appendBackupNotes(result, staleWrite, speculativeStaleWritePathCount(tc.Name, trackedFilePath, hooks), backupOutcome)
	execResult.Result = formatToolExecutionOutput(result, p.sessionDir, artifactKey, tc.Name, nil, p.guidance)
	return execResult, nil
}

func toolExecutionDiff(tc message.ToolCall, result ToolExecutionResult) agentdiff.Summary {
	if tools.NormalizeName(tc.Name) == tools.NameApplyPatch {
		return agentdiff.Summary{Text: result.Diff.Text, Added: result.Diff.Added, Removed: result.Diff.Removed}
	}
	return agentdiff.GenerateToolDiff(tc, result.PreContent, result.PreFilePath, result.PreExisted)
}

func normalizeCompatibleToolCallArgs(tc *message.ToolCall, result *ToolExecutionResult) error {
	if tc == nil || tc.Name != tools.NameApplyPatch {
		return nil
	}
	normalized, err := tools.NormalizeApplyPatchArgs(tc.Args)
	if err != nil {
		return err
	}
	if string(normalized) == string(tc.Args) {
		return nil
	}
	original := append(json.RawMessage(nil), tc.Args...)
	tc.Args = normalized
	if result != nil {
		result.EffectiveArgsJSON = string(normalized)
		result.Audit = syncAuditEffectiveArgs(result.Audit, original, normalized)
	}
	return nil
}

func (p toolExecutionPipeline) acquireWorkspaceLease(ctx context.Context, tc message.ToolCall) (func(), error) {
	if p.governor == nil {
		return func() {}, nil
	}
	policy := tools.PolicyForTool(p.registry, tc.Name, llm.UnwrapToolArgs(tc.Args))
	release, err := p.governor.acquireWorkspaceLease(ctx, policy)
	if err != nil {
		return nil, fmt.Errorf("acquire workspace lease for %s: %w", tc.Name, err)
	}
	return release, nil
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
	if (toolName == tools.NameDelete || toolName == tools.NameApplyPatch) && hooks != nil {
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
	if n != tools.NameApplyPatch && n != tools.NameEdit {
		return nil
	}
	if _, ok := visible[n]; ok {
		return nil
	}
	switch n {
	case tools.NameApplyPatch:
		if _, ok := visible[tools.NameEdit]; ok {
			return fmt.Errorf("tool %q is not available for the current model. Use %q instead (the %q file-modification tool)", tools.NameApplyPatch, tools.NameEdit, tools.NameEdit)
		}
	case tools.NameEdit:
		if _, ok := visible[tools.NameApplyPatch]; ok {
			return fmt.Errorf("tool %q is not available for the current model. Use %q instead (the %q file-modification tool)", tools.NameEdit, tools.NameApplyPatch, tools.NameApplyPatch)
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
	hookResult, hookErr := p.fireHook(ctx, hook.OnToolCall, turnID, buildToolHookData(*tc, p.effectiveToolBaseDir()))
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

func (p toolExecutionPipeline) applySuccessfulFileState(execResult *ToolExecutionResult, tc message.ToolCall, trackedFilePath string, readObservation tools.ReadObservation, writeLock *trackedWriteLock) {
	if tc.Name == tools.NameRead {
		execResult.FileState = buildObservedReadFileState(readObservation)
		if p.fileTrack != nil {
			if hash := firstReadHashForPath(execResult.FileState, readObservation.Path); hash != "" {
				p.fileTrack.TrackCommittedSnapshot(readObservation.Path, p.agentID, hash)
				if readResultShowsWholeFile(execResult.Result) {
					p.fileTrack.TrackObservedSnapshot(readObservation.Path, p.agentID, hash)
				}
			}
		}
		return
	}
	if (tc.Name == tools.NameWrite || tc.Name == tools.NameEdit || tc.Name == tools.NameApplyPatch) && trackedFilePath != "" {
		if tc.Name == tools.NameEdit || tc.Name == tools.NameApplyPatch {
			execResult.FileState = buildLocalizedWriteFileState(trackedFilePath, execResult.PreContent)
		} else {
			execResult.FileState = buildWriteFileState(trackedFilePath)
		}
		if p.fileTrack != nil {
			if hash := firstWriteHashForPath(execResult.FileState, trackedFilePath); hash != "" {
				writeLock.noteResultHash(hash)
				p.fileTrack.TrackCommittedSnapshot(trackedFilePath, p.agentID, hash)
				// A whole-file write's resulting content is exactly the bytes the
				// model supplied, so it counts as an observation: a follow-up
				// write or delete of this file must not demand a redundant
				// full re-read. Edits and patches stay committed-only — the
				// model has not seen the resulting full file.
				if tc.Name == tools.NameWrite {
					p.fileTrack.TrackObservedSnapshot(trackedFilePath, p.agentID, hash)
				}
			}
		}
		execResult.LSPReviews = speculativeWriteToolLSPReviews(p.registry, tc.Name, trackedFilePath)
	}
}

// requireObservedDestructiveWrite gates whole-file replacement behind a
// current full observation of the file. Delete paths are gated per-path in
// acquireDeleteLocks; this function only sees the write tool's tracked path.
// The pre-execution state captured while acquiring the write lock is reused so
// the same file is not read and hashed twice per call.
func requireObservedDestructiveWrite(track *filelock.FileTracker, agentID, toolName, path string, writeStatus filelock.WriteStatus, lock *trackedWriteLock) error {
	if track == nil || path == "" || toolName != tools.NameWrite || lock == nil {
		return nil
	}
	action := strings.ToLower(toolName)
	if lock.preVerifyErr != nil {
		return fmt.Errorf("refusing to %s file %s because its current state cannot be verified: %w", action, path, lock.preVerifyErr)
	}
	if !lock.preExists {
		return nil
	}
	return requireCurrentFileObservation(track, agentID, path, lock.preHash, action, writeStatus.ExternalChanged)
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
		locks, err := acquireDeleteLocks(p.fileTrack, p.agentID, tc.Args, p.effectiveToolBaseDir())
		if err != nil {
			return "", nil, fmt.Errorf("file conflict: %w", err)
		}
		return "", locks, nil
	case tools.NameEdit:
		return trackedEditPathFromArgs(tc.Args, p.effectiveToolBaseDir()), nil, nil
	case tools.NameApplyPatch:
		return "", nil, nil
	case tools.NameRead, tools.NameWrite:
		path := toolPathFromArgs(tc.Args)
		if path == "" {
			return "", nil, nil
		}
		resolved, err := tools.ResolveToolPathInDir(path, p.effectiveToolBaseDir())
		if err != nil {
			return "", nil, err
		}
		return resolved, nil, nil
	default:
		return "", nil, nil
	}
}

// trackedWriteLock owns one tracked write lease acquired before tool
// execution. It carries the pre-execution content hash for the destructive
// write gate and accepts the post-execution hash captured while building the
// tool's FileState, so neither side re-reads a file that was just hashed.
type trackedWriteLock struct {
	track        *filelock.FileTracker
	agentID      string
	path         string
	preHash      string
	preExists    bool
	preVerifyErr error
	postHash     string
}

func acquireTrackedWriteLock(track *filelock.FileTracker, agentID, path, toolName string) (*trackedWriteLock, filelock.WriteStatus, error) {
	var status filelock.WriteStatus
	if track == nil || path == "" || (toolName != tools.NameWrite && toolName != tools.NameEdit && toolName != tools.NameApplyPatch) {
		return nil, status, nil
	}
	lock := &trackedWriteLock{track: track, agentID: agentID, path: path}
	// An unreadable file leaves preHash empty, matching the pre-existing
	// lease semantics for edit/patch; only the write gate turns preVerifyErr
	// into a refusal.
	lock.preHash, lock.preExists, lock.preVerifyErr = verifiedCurrentFileHash(path)
	var err error
	status, err = track.AcquireWriteStatus(path, agentID, lock.preHash)
	if err != nil {
		return nil, status, wrapTrackedWriteError(err)
	}
	return lock, status, nil
}

// noteResultHash records the post-execution content hash captured while
// building the tool's FileState, letting release skip re-hashing the file.
func (l *trackedWriteLock) noteResultHash(hash string) {
	if l != nil && hash != "" {
		l.postHash = hash
	}
}

func (l *trackedWriteLock) release() {
	if l == nil {
		return
	}
	hash := l.postHash
	if hash == "" {
		hash = computeFileHash(l.path)
	}
	l.track.ReleaseWrite(l.path, l.agentID, hash)
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
	case tools.NameEdit, tools.NameApplyPatch:
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

func (p toolExecutionPipeline) backupRiskyPatchState(tc message.ToolCall, stale bool, sources []fileBackupSource) fileBackupOutcome {
	if p.fileBackups == nil || !stale {
		return fileBackupOutcome{}
	}
	var records []fileBackupRecord
	type backupWarning struct {
		path string
		err  error
	}
	var warnings []backupWarning
	for _, source := range sources {
		backup, err := p.fileBackups.Backup(source.Path, tc.Name, source.Data)
		if err != nil {
			warnings = append(warnings, backupWarning{path: source.Path, err: err})
			continue
		}
		if backup.Path != "" {
			records = append(records, backup)
		}
	}
	var warning string
	if len(warnings) == 1 {
		warning = warnings[0].err.Error()
	} else if len(warnings) > 1 {
		parts := make([]string, 0, len(warnings))
		for _, item := range warnings {
			parts = append(parts, fmt.Sprintf("%s: %v", item.path, item.err))
		}
		warning = strings.Join(parts, "; ")
	}
	return fileBackupOutcome{Records: records, Warning: warning}
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

func applySpeculativeFileState(execResult *ToolExecutionResult, registry *tools.Registry, tc message.ToolCall, result, baseDir string, readObservation tools.ReadObservation, hooks *speculativeToolHooks) {
	switch tc.Name {
	case tools.NameRead:
		execResult.FileState = buildObservedReadFileState(readObservation)
	case tools.NameEdit:
		if path := trackedEditPathFromArgs(tc.Args, baseDir); path != "" {
			execResult.FileState = buildLocalizedWriteFileState(path, execResult.PreContent)
		}
	case tools.NameApplyPatch:
		if hooks != nil && hooks.fileState != nil {
			execResult.FileState = hooks.fileState()
		}
	case tools.NameWrite:
		if path := toolPathFromArgs(tc.Args); path != "" {
			if resolved, err := tools.ResolveToolPathInDir(path, baseDir); err == nil {
				execResult.FileState = buildWriteFileState(resolved)
			}
		}
	case tools.NameDelete:
		execResult.FileState = buildDeleteFileStateFromResultInDir(result, baseDir)
	}
	if (tc.Name == tools.NameWrite || tc.Name == tools.NameEdit || tc.Name == tools.NameApplyPatch) && execResult.PreFilePath != "" {
		execResult.LSPReviews = speculativeWriteToolLSPReviews(registry, tc.Name, execResult.PreFilePath)
	}
	if tc.Name == tools.NameApplyPatch {
		execResult.LSPReviews = lspReviewsForFileState(registry, tc.Name, execResult.FileState)
	}
}

func lspReviewsForFileState(registry *tools.Registry, toolName string, state *message.ToolFileState) []message.LSPReview {
	if state == nil {
		return nil
	}
	var reviews []message.LSPReview
	for _, write := range state.Writes {
		reviews = append(reviews, speculativeWriteToolLSPReviews(registry, toolName, write.Path)...)
	}
	return reviews
}

func trackedEditPathFromArgs(raw json.RawMessage, baseDir string) string {
	return tools.ExtractEditPathFromArgsInDir(llm.UnwrapToolArgs(raw), baseDir)
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
	return fmt.Errorf("%w: file changed on disk since it was last read; hunk may be based on stale content. Re-read the target area and retry with current contents", err)
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

func commitPromotedReadToolSideEffects(track *filelock.FileTracker, agentID, toolName, result string, fileState *message.ToolFileState) {
	if toolName != tools.NameRead || track == nil || fileState == nil || len(fileState.Reads) != 1 {
		return
	}
	read := fileState.Reads[0]
	path := strings.TrimSpace(read.Path)
	hash := strings.TrimSpace(read.SHA256)
	if path == "" || hash == "" || !read.Exists {
		return
	}
	track.TrackCommittedSnapshot(path, agentID, hash)
	if readResultShowsWholeFile(result) {
		track.TrackObservedSnapshot(path, agentID, hash)
	}
}
