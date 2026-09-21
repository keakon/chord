package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/agent/agentdiff"
	"github.com/keakon/chord/internal/filelock"
	"github.com/keakon/chord/internal/hook"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/recovery"
	"github.com/keakon/chord/internal/tools"
)

const (
	mainToolOutputGuidance = tools.ArtifactReadGuidance
	subToolOutputGuidance  = tools.ArtifactReadGuidance
)

type toolExecutionPipeline struct {
	agentID          string
	journalAgentID   string
	eventAgentID     string
	taskID           string
	sessionDir       string
	registry         *tools.Registry
	governor         *resourceGovernor
	fileTrack        *filelock.FileTracker
	fileBackups      *fileBackupManager
	runtimeStartedAt time.Time // runtime start; drift warnings omit mtimes predating it
	eventSender      tools.EventSender
	emit             func(AgentEvent)
	jobAccess        tools.JobAccess
	guidance         string
	logPrefix        string
	projectRoot      string
	toolBaseDir      string
	applyPatchRetry  *applyPatchRetryGuard

	currentRuleset                func() permission.Ruleset
	refreshRulesetAfterRuleIntent func(toolName string, intent *ConfirmRuleIntent) permission.Ruleset
	isInternalTool                func(string) bool
	confirm                       ConfirmFunc
	currentTurnID                 func() uint64
	fireHook                      func(context.Context, string, uint64, map[string]any) (*hook.Result, error)
	updatePending                 func(PendingToolCall)
	reservedToolError             func(string) error
	bypassPermission              func(string) bool
	// yoloDowngradeAsk reports whether an ask decision relaxes to an implicit
	// allow because YOLO mode is on (for the main agent) or inherited from the
	// parent (for SubAgents). Deny never reaches the ask branch; done returns
	// before it and compact_context reports false so its explicit ask rules
	// still confirm. nil keeps every ask confirming.
	yoloDowngradeAsk func(string) bool
	// loopExitAuthorized reports whether loop mode is active, which authorizes
	// done against wildcard-only rules. nil means "not a loop-capable agent"
	// (SubAgents), so done keeps plain wildcard semantics there.
	loopExitAuthorized func() bool
	// preapprovedPermission consults a per-turn cache of non-interactive allow
	// decisions recorded by the speculative-reuse prefilter. It re-checks every
	// evaluation input (args, ruleset identity, cwd, pctx) before reusing the
	// recorded allow; true skips the re-evaluation in applyPermission.
	preapprovedPermission func(callID, name string, args json.RawMessage, cwd string, pctx toolPermissionContext) bool
	visibleToolNames      func() map[string]struct{}
	appendToolActivity    func(recovery.ToolActivityRecord) error
	captureWalltimeTarget func() *walltimeTarget
}

// shellCallArguments describes a Shell call the way every caller in this
// package needs to reason about it: what it runs and where. Decoding happens
// here alone so the permission/tool-card argument summary and the rest of the
// agent agree about what a given Shell call is.
type shellCallArguments struct {
	Command string `json:"command"`
	Workdir string `json:"workdir,omitempty"`
}

func decodeShellCallArguments(args json.RawMessage) (shellCallArguments, error) {
	var parsed shellCallArguments
	if err := json.Unmarshal(llm.UnwrapToolArgs(args), &parsed); err != nil {
		return shellCallArguments{}, err
	}
	parsed.Command = strings.TrimSpace(parsed.Command)
	parsed.Workdir = strings.TrimSpace(parsed.Workdir)
	return parsed, nil
}

func (p toolExecutionPipeline) effectiveToolBaseDir() string {
	if strings.TrimSpace(p.toolBaseDir) != "" {
		return p.toolBaseDir
	}
	return p.projectRoot
}

// toolActivityJournalRequired reports whether a finalized tool call needs a
// started journal record before execution. It skips calls in the read-only
// concurrency class; TodoWrite is journaled on the normal path, while its
// side-effect-free speculative preview is journaled only when promoted.
//
// A read-only tool that consumes state (see tools.IsConsumingRead) still needs a
// record: re-running it after a crash would report success while returning
// nothing, so the journal keeps that retry classified outcome_unknown instead of
// "never started, safe to retry".
func toolActivityJournalRequired(registry *tools.Registry, tc message.ToolCall) bool {
	name := tools.NormalizeName(tc.Name)
	if tools.IsConsumingRead(name) {
		return true
	}
	return tools.ConcurrencyClassForTool(registry, name, llm.UnwrapToolArgs(tc.Args)) != tools.ToolConcurrencyClassReadOnly
}

// recordToolActivityStarted appends a started journal record for non-read-only
// calls right before their execution body runs so a crash afterwards can be
// distinguished from "never started" during restore. Read-only calls are
// skipped (safe to re-run). A failed append aborts execution: running a
// mutation without the record would let restore classify it as not_started
// ("safe to retry") after its side effects already hit disk.
func (p toolExecutionPipeline) recordToolActivityStarted(tc message.ToolCall) error {
	if p.appendToolActivity == nil || !toolActivityJournalRequired(p.registry, tc) {
		return nil
	}
	turnID := uint64(0)
	if p.currentTurnID != nil {
		turnID = p.currentTurnID()
	}
	rec := recovery.ToolActivityRecord{
		CallID:  tc.ID,
		AgentID: p.toolActivityAgentID(),
		TurnID:  turnID,
		Tool:    tc.Name,
		State:   recovery.ToolActivityStateStarted,
		TS:      time.Now().UnixNano(),
	}
	if err := p.appendToolActivity(rec); err != nil {
		log.Warnf("tool-activity journal append failed agent=%v call_id=%v error=%v", p.toolActivityAgentID(), tc.ID, err)
		return fmt.Errorf("tool %q not executed: failed to record started state before execution: %w", tc.Name, err)
	}
	return nil
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

// validateToolCallArgs runs every argument check a tool call must pass before
// it executes. Malformed or empty arguments get their own truncation guidance;
// otherwise the schema check strips fields the tool does not recognize instead
// of rejecting the call, so a call whose required and known parameters are
// correct still runs when the model hallucinates an extra field. Duplicate
// object keys use JSON's last-value-wins semantics, with every shadowed value
// recorded alongside stripped fields for model and UI feedback.
//
// This runs after permission edits and hooks so arguments introduced by the
// user or a hook are held to the same contract as the model's, and it updates
// the effective arguments so downstream consumers see the parameters that
// actually ran. OriginalArgsJSON keeps meaning "what the model asked for before
// an upstream edit": sanitization never touches it and never flips
// UserModified. Values removed from a successful call are recorded in
// IgnoredArgs; fields that prevent validation are recorded in InvalidArgs.
func (p toolExecutionPipeline) validateToolCallArgs(tc *message.ToolCall, execResult *ToolExecutionResult) ([]message.IgnoredToolArg, []message.InvalidToolArg, error) {
	if err := guardAbnormalToolArgs(p.registry, *tc, p.logPrefix, p.agentID); err != nil {
		return nil, nil, err
	}
	if p.registry == nil {
		return nil, nil, nil
	}
	tool, ok := p.registry.Get(tc.Name)
	if !ok {
		return nil, nil, nil
	}
	original := tc.Args
	if execResult != nil && len(execResult.originalArgsForValidation) > 0 {
		original = execResult.originalArgsForValidation
	}
	sanitized, ignored, invalid, err := tools.SanitizeUnknownArgsWithDiagnostics(tool, llm.UnwrapToolArgs(original))
	if err != nil {
		if execResult != nil && (len(ignored) > 0 || len(invalid) > 0) {
			if execResult.Audit == nil {
				execResult.Audit = &message.ToolArgsAudit{}
			}
			execResult.Audit.IgnoredArgs = slices.Clone(ignored)
			execResult.Audit.InvalidArgs = slices.Clone(invalid)
		}
		return ignored, invalid, err
	}
	if len(ignored) == 0 && len(invalid) == 0 {
		return nil, nil, nil
	}
	if execResult != nil {
		if execResult.Audit == nil {
			execResult.Audit = &message.ToolArgsAudit{}
		}
		if len(ignored) > 0 {
			tc.Args = sanitized
			execResult.EffectiveArgsJSON = string(sanitized)
			execResult.Audit.EffectiveArgsJSON = string(sanitized)
		}
		if len(execResult.originalArgsForValidation) > 0 {
			execResult.Audit = syncAuditEffectiveArgs(execResult.Audit, execResult.originalArgsForValidation, tc.Args)
			execResult.originalArgsForValidation = nil
		}
		execResult.Audit.IgnoredArgs = slices.Clone(ignored)
		execResult.Audit.InvalidArgs = slices.Clone(invalid)
	}
	return ignored, invalid, nil
}

// maxIgnoredArgNotePathRunes caps how much of an ignored argument path is
// surfaced in a tool result, so a pathological parameter name cannot inflate
// the model context; it mirrors the 80-rune preview limit used by
// describeJSONValue in validation errors.
const maxIgnoredArgNotePathRunes = 80

func truncateIgnoredArgPath(path string) string {
	if utf8.RuneCountInString(path) <= maxIgnoredArgNotePathRunes {
		return path
	}
	runes := []rune(path)
	return string(runes[:maxIgnoredArgNotePathRunes]) + "…"
}

// maxIgnoredArgNotePaths caps how many paths one note lists. The note exists so
// the model can correct its next call, and a model that invented dozens of
// fields learns that from the first handful; listing every one would echo an
// entire hallucinated argument object back into the context.
const maxIgnoredArgNotePaths = 20

func joinIgnoredArgPaths(paths []string) string {
	if len(paths) <= maxIgnoredArgNotePaths {
		return strings.Join(paths, ", ")
	}
	return fmt.Sprintf("%s, and %d more", strings.Join(paths[:maxIgnoredArgNotePaths], ", "), len(paths)-maxIgnoredArgNotePaths)
}

// appendIgnoredArgsNote surfaces every value that did not participate in
// execution. Unrecognized fields and shadowed duplicate occurrences are kept
// separate so the model knows whether to remove a field or split a call.
func appendIgnoredArgsNote(result string, ignored []message.IgnoredToolArg) string {
	return appendNotes(result, ignoredArgsNotes(ignored))
}

// ignoredArgsNotes renders the diagnostics appendIgnoredArgsNote appends after
// the payload, as discrete lines so they can also be recorded beside the clean
// payload instead of being recovered by parsing the combined text.
func ignoredArgsNotes(ignored []message.IgnoredToolArg) []string {
	if len(ignored) == 0 {
		return nil
	}
	var unrecognized, shadowed, nullOptional []string
	for _, item := range ignored {
		path := truncateIgnoredArgPath(item.Path)
		switch item.Reason {
		case message.IgnoredToolArgReasonShadowed:
			shadowed = appendUniqueString(shadowed, path)
		case message.IgnoredToolArgReasonNull:
			nullOptional = appendUniqueString(nullOptional, path)
		default:
			unrecognized = appendUniqueString(unrecognized, path)
		}
	}
	notes := make([]string, 0, 3)
	if len(unrecognized) > 0 {
		notes = append(notes, "Note: ignored unrecognized parameter(s): "+joinIgnoredArgPaths(unrecognized))
	}
	if len(shadowed) > 0 {
		notes = append(notes, "Note: ignored earlier duplicate parameter value(s): "+joinIgnoredArgPaths(shadowed)+"; the last values were used")
	}
	if len(nullOptional) > 0 {
		notes = append(notes, "Note: ignored null parameter(s): "+joinIgnoredArgPaths(nullOptional)+"; treated as unset, so pass a value or omit the parameter")
	}
	return notes
}

// appendNotes appends diagnostic lines after a tool's payload for the model.
// The same lines are recorded on ToolExecutionResult.Notes, so the UI reads the
// payload and the notes separately instead of splitting one combined string.
func appendNotes(result string, notes []string) string {
	if len(notes) == 0 {
		return result
	}
	result = strings.TrimRight(result, "\n")
	if result == "" {
		return strings.Join(notes, "\n")
	}
	return result + "\n" + strings.Join(notes, "\n")
}

// toolPayloadIsStructured reports whether a tool's raw output is a structured
// payload the UI parses back, rather than free text shown verbatim. Only those
// tools keep a clean pre-note copy: everything else is truncated or artifacted
// into Content already, so a second copy would double large results for nothing.
func toolPayloadIsStructured(toolName string) bool {
	return toolName == tools.NameQuestion
}

func appendUniqueString(values []string, value string) []string {
	if slices.Contains(values, value) {
		return values
	}
	return append(values, value)
}

func (p toolExecutionPipeline) execute(ctx context.Context, tc message.ToolCall, fireHook bool) (ToolExecutionResult, error) {
	tc.Name = tools.NormalizeName(tc.Name)
	execResult := ToolExecutionResult{EffectiveArgsJSON: string(tc.Args)}
	if err := normalizeCompatibleToolCallArgs(&tc, &execResult); err != nil {
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
	ignored, _, err := p.validateToolCallArgs(&tc, &execResult)
	if err != nil {
		return execResult, err
	}
	if err := p.applyPatchRetry.reject(tc.Name, tc.Args, p.effectiveToolBaseDir()); err != nil {
		return execResult, err
	}

	// Wall-clock execution anchor: set after permission confirmation, hooks,
	// and argument validation all passed, before resource acquisition (file
	// locks, workspace lease), backups, and the journal write. Everything from
	// here to the registry Execute — including lock/lease waits — counts as
	// tool execution time; ask / question / done confirmation waits happened
	// before this point and are excluded.
	execResult.ExecStartedAt = time.Now()
	if p.captureWalltimeTarget != nil {
		execResult.walltimeTarget = p.captureWalltimeTarget()
	}

	agentCtx := buildToolExecContext(ctx, tc, p.agentID, p.taskID, p.sessionDir, p.eventSender, p.jobAccess, p.emit)
	if p.currentTurnID != nil {
		agentCtx = tools.WithTurnID(agentCtx, p.currentTurnID())
	}
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
	staleWrite, unobservedWrite, err := requireObservedDestructiveWrite(p.fileTrack, p.agentID, tc.Name, trackedFilePath, writeStatus, releaseWrite)
	if err != nil {
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
	if writeStatus.ExternalChanged {
		staleWrite = true
	}
	if deleteLocks != nil {
		staleWrite = deleteLocks.hasStalePath()
	} else if patchMutation != nil {
		staleWrite = patchMutation.stale
	}
	// CapturePreWriteState only handles edit/write; for apply_patch the diff
	// comes from the ApplyPatchDiffCollector and file state from the
	// patchMutation snapshots, so the pre-write fields stay zero.
	execResult.PreFilePath, execResult.PreContent, execResult.PreExisted = agentdiff.CapturePreWriteState(tc, p.effectiveToolBaseDir())

	var backupOutcome fileBackupOutcome
	if patchMutation != nil {
		backupOutcome = p.backupRiskyPatchState(tc, patchMutation.stale, patchMutation.backupSources())
	} else {
		backupOutcome = p.backupRiskyPreWriteState(tc, trackedFilePath, staleWrite, execResult.PreContent, execResult.PreExisted, deleteLocks)
	}

	// Started journal: see recordToolActivityStarted.
	if err := p.recordToolActivityStarted(tc); err != nil {
		return execResult, err
	}

	result, err := p.registry.Execute(agentCtx, tc.Name, llm.UnwrapToolArgs(tc.Args))
	execResult.Images = imageSink.Drain()
	deleteAudit, hasDeleteAudit := deleteAuditSink.Audit()
	if deleteLocks != nil && hasDeleteAudit {
		deleteLocks.CommitAudit(deleteAudit)
	}
	if err != nil && staleWrite && (tc.Name == tools.NameEdit || tc.Name == tools.NameApplyPatch) {
		err = wrapStaleEditError(err)
	}
	if !toolExecutionCommitted(err) {
		if result != "" && staleWrite && tools.ErrorDescribedInResult(err) {
			// Described-in-result errors are not appended to the model-visible
			// text, so surface the stale-read root cause in the result itself.
			result += "\nWarning: the target changed on disk since it was last read; failed hunks may be based on stale content. Re-read the failed targets and revise against current contents."
		}
		// The model must learn about stripped fields even when the call
		// went on to fail, or it cannot tell which parameters took effect.
		if toolPayloadIsStructured(tc.Name) {
			execResult.Payload = result
		}
		execResult.Notes = ignoredArgsNotes(ignored)
		result = appendIgnoredArgsNote(result, ignored)
		if result != "" {
			execResult.Result = formatToolExecutionOutput(result, p.sessionDir, artifactKey, tc.Name, err, p.guidance)
		}
		return execResult, err
	}
	if tc.Name == tools.NameDelete {
		if deleteLocks != nil && !hasDeleteAudit {
			deleteLocks.Commit(result)
		}
		if hasDeleteAudit {
			execResult.FileState = buildDeleteFileState(deleteAudit.Deleted)
		} else {
			execResult.FileState = buildDeleteFileStateFromResultInDir(result, p.effectiveToolBaseDir())
		}
	}
	if patchMutation != nil {
		execResult.Diff = patchDiffCollector.Summary()
		patchMutation.CaptureAfter()
		execResult.FileState = patchMutation.postState()
		attachApplyPatchFileChanges(execResult.FileState, patchDiffCollector.Changes())
		patchMutation.Commit()
		execResult.LSPReviews = lspReviewsForFileState(p.registry, tc.Name, execResult.FileState)
	}
	execResult.Result = result
	readObservation, _ := readObservationSink.Observation()
	p.applySuccessfulFileState(&execResult, tc, trackedFilePath, readObservation, releaseWrite)
	stalePathCount := staleWritePathCount(trackedFilePath, deleteLocks)
	var changeModTime time.Time
	switch {
	case releaseWrite != nil:
		changeModTime = releaseWrite.preModTime
	case patchMutation != nil:
		changeModTime = patchMutation.staleModTime
	case deleteLocks != nil:
		changeModTime = deleteLocks.staleModTime()
	}
	if patchMutation != nil {
		stalePathCount = len(patchMutation.paths)
	}
	// Payload is captured before anything is appended: what follows describes
	// the call, not the tool's output, and the two must stay separable.
	if toolPayloadIsStructured(tc.Name) {
		execResult.Payload = result
	}
	backup := backupNotes(tc.Name, driftReport{
		stale:      staleWrite,
		unobserved: unobservedWrite,
		// An unreadable pre-state is not a drift: the wording must not claim
		// the file changed when only its readability did, and the mtime (the
		// file's original one) must not feed an age.
		unverifiable:     releaseWrite != nil && releaseWrite.preVerifyErr != nil,
		paths:            stalePathCount,
		modTime:          changeModTime,
		runtimeStartedAt: p.runtimeStartedAt,
	}, backupOutcome)
	execResult.Notes = append(execResult.Notes, backup...)
	execResult.Notes = append(execResult.Notes, ignoredArgsNotes(ignored)...)
	result = appendNotes(result, backup)
	result = appendNotes(result, ignoredArgsNotes(ignored))
	execResult.Result = formatToolExecutionOutput(result, p.sessionDir, artifactKey, tc.Name, err, p.guidance)
	return execResult, err
}

func (p toolExecutionPipeline) toolActivityAgentID() string {
	if strings.TrimSpace(p.journalAgentID) != "" {
		return p.journalAgentID
	}
	return p.agentID
}

func (p toolExecutionPipeline) executeSpeculative(ctx context.Context, tc message.ToolCall) (ToolExecutionResult, error) {
	tc.Name = tools.NormalizeName(tc.Name)
	execResult := ToolExecutionResult{EffectiveArgsJSON: string(tc.Args)}
	if err := normalizeCompatibleToolCallArgs(&tc, &execResult); err != nil {
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
	// Mirror execute(): the speculative run has no permission edits or hooks to
	// wait for, but it owes the model the same argument contract — truncation
	// guidance for malformed/empty args, unknown fields stripped and reported.
	ignored, _, err := p.validateToolCallArgs(&tc, &execResult)
	if err != nil {
		return execResult, err
	}
	if err := p.applyPatchRetry.reject(tc.Name, tc.Args, p.effectiveToolBaseDir()); err != nil {
		return execResult, err
	}
	// Wall-clock execution anchor for the speculative run: after schema
	// validation, before workspace lease acquisition, so lease waits count as
	// tool time. Speculative execution never includes confirmation waits
	// (permission must already be Allow), so this anchor is exact.
	execResult.ExecStartedAt = time.Now()
	if p.captureWalltimeTarget != nil {
		execResult.walltimeTarget = p.captureWalltimeTarget()
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

	agentCtx := buildToolExecContext(ctx, tc, p.agentID, p.taskID, p.sessionDir, p.eventSender, p.jobAccess, p.emit)
	if p.currentTurnID != nil {
		agentCtx = tools.WithTurnID(agentCtx, p.currentTurnID())
	}
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
	}
	// See execute(): apply_patch keeps zero pre-write fields; its diff and
	// backups come from the diff collector and the speculative mutation.
	execResult.PreFilePath, execResult.PreContent, execResult.PreExisted = agentdiff.CapturePreWriteState(tc, p.effectiveToolBaseDir())
	artifactKey := toolCallArtifactKey(tc)
	staleWrite := hooks != nil && hooks.stale
	unobservedWrite := hooks != nil && hooks.unobserved
	trackedFilePath := speculativeTrackedFilePath(tc.Name, execResult.PreFilePath, hooks)
	var backupOutcome fileBackupOutcome
	if tc.Name == tools.NameApplyPatch && hooks != nil && hooks.backupSources != nil {
		// A patch can touch several files, so back up every snapshot the
		// speculative mutation captured instead of the single tracked path.
		backupOutcome = p.backupRiskyPatchState(tc, staleWrite, hooks.backupSources())
	} else {
		backupOutcome = p.backupRiskyPreWriteState(tc, trackedFilePath, staleWrite, execResult.PreContent, execResult.PreExisted, speculativeDeleteLocks(tc.Name, hooks))
	}
	// Started journal: mirror execute() so a crash after a speculative file
	// mutation is classified outcome_unknown during restore, never not_started
	// ("safe to retry"). TodoWrite stays promote-journaled — its speculative
	// run is a side-effect-free preview.
	if tc.Name != tools.NameTodoWrite {
		if err := p.recordToolActivityStarted(tc); err != nil {
			rollbackSpeculativeToolHooks(execResult)
			return execResult, err
		}
	}
	result, err := p.registry.Execute(agentCtx, tc.Name, llm.UnwrapToolArgs(tc.Args))
	execResult.Images = imageSink.Drain()
	if err != nil && staleWrite && (tc.Name == tools.NameEdit || tc.Name == tools.NameApplyPatch) {
		err = wrapStaleEditError(err)
	}
	if !toolExecutionCommitted(err) {
		rollbackSpeculativeToolHooks(execResult)
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
	if patchDiffCollector != nil {
		attachApplyPatchFileChanges(execResult.FileState, patchDiffCollector.Changes())
	}
	var changeModTime time.Time
	if hooks != nil {
		changeModTime = hooks.staleModTime
	}
	if toolPayloadIsStructured(tc.Name) {
		execResult.Payload = result
	}
	backup := backupNotes(tc.Name, driftReport{
		stale:      staleWrite,
		unobserved: unobservedWrite,
		// Writes with an unreadable pre-state never speculate (they are
		// deferred at Start), so the unverifiable wording has no speculative
		// call site.
		paths:            speculativeStaleWritePathCount(tc.Name, trackedFilePath, hooks),
		modTime:          changeModTime,
		runtimeStartedAt: p.runtimeStartedAt,
	}, backupOutcome)
	execResult.Notes = append(execResult.Notes, backup...)
	execResult.Notes = append(execResult.Notes, ignoredArgsNotes(ignored)...)
	result = appendNotes(result, backup)
	result = appendNotes(result, ignoredArgsNotes(ignored))
	execResult.Result = formatToolExecutionOutput(result, p.sessionDir, artifactKey, tc.Name, err, p.guidance)
	return execResult, err
}

func attachApplyPatchFileChanges(state *message.ToolFileState, changes []tools.ApplyPatchChange) {
	if state == nil || len(changes) == 0 {
		return
	}
	state.Changes = make([]message.ToolFileChange, 0, len(changes))
	for _, change := range changes {
		fileChange := message.ToolFileChange{
			Path:    change.SourcePath,
			Added:   change.Added,
			Removed: change.Removed,
		}
		switch change.Kind {
		case tools.MutationAdd, tools.MutationUpdate:
			fileChange.Path = change.TargetPath
		case tools.MutationDelete:
			fileChange.Deleted = true
		case tools.MutationMove:
			fileChange.TargetPath = change.TargetPath
		}
		state.Changes = append(state.Changes, fileChange)
	}
}

// toolExecutionCommitted distinguishes an ordinary failure from a tool result
// that reports an error after committing a subset of its file mutations. The
// latter still needs error handling at the model/UI boundary, but runtime
// bookkeeping must follow the actual committed FileState.
func toolExecutionCommitted(err error) bool {
	return err == nil || tools.ErrorHasCommittedChanges(err)
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
	original := append(json.RawMessage(nil), tc.Args...)
	normalized, err := tools.NormalizeApplyPatchArgs(tc.Args)
	if err != nil {
		return err
	}
	if string(normalized) == string(tc.Args) {
		return nil
	}
	tc.Args = normalized
	if result != nil {
		result.originalArgsForValidation = original
	}
	return nil
}

func toolExecDuration(toolName string, execResult ToolExecutionResult, completedAt time.Time) time.Duration {
	// Question is an interaction-only tool: its Execute method blocks for the
	// user's answers, so its wall time belongs in UserWait rather than Tools.
	if tools.NormalizeName(toolName) == tools.NameQuestion {
		return 0
	}
	start := execResult.ExecStartedAt
	if start.IsZero() || completedAt.Before(start) {
		return 0
	}
	return completedAt.Sub(start)
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
	required := map[string]bool(nil)
	if hooks.deleteBackupRequired != nil {
		required = hooks.deleteBackupRequired()
	}
	locked := make([]deleteLockedPath, 0, len(hooks.paths))
	for _, path := range hooks.paths {
		info, err := os.Lstat(path)
		locked = append(locked, deleteLockedPath{
			path:           path,
			symlink:        err == nil && info.Mode()&os.ModeSymlink != 0,
			backupRequired: required[path],
		})
	}
	return &deleteLockSet{paths: hooks.paths, locked: locked}
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

// checkVisible rejects tool calls for per-model filtered file tools (patch ↔
// edit, and write/delete hidden for patch-native models) that are not in the
// current model-appropriate visible set. This enforces the per-model file tool
// filter at execution time so a model cannot circumvent the declared tool
// surface by calling a hidden sibling tool name from conversation history.
// Other tools are governed by the existing permission/registry flow and do not
// need a secondary visibility gate.
func (p toolExecutionPipeline) checkVisible(name string) error {
	visible := p.visibleToolNames()
	if visible == nil {
		return nil
	}
	n := tools.NormalizeName(name)
	switch n {
	case tools.NameApplyPatch, tools.NameEdit, tools.NameWrite, tools.NameDelete:
	default:
		return nil
	}
	if _, ok := visible[n]; ok {
		return nil
	}
	_, patchVisible := visible[tools.NameApplyPatch]
	switch n {
	case tools.NameApplyPatch:
		if _, ok := visible[tools.NameEdit]; ok {
			return fmt.Errorf("tool %q is not available for the current model. Use %q instead (the %q file-modification tool)", tools.NameApplyPatch, tools.NameEdit, tools.NameEdit)
		}
	case tools.NameEdit:
		if patchVisible {
			return fmt.Errorf("tool %q is not available for the current model. Use %q instead (the %q file-modification tool)", tools.NameEdit, tools.NameApplyPatch, tools.NameApplyPatch)
		}
	case tools.NameWrite:
		if patchVisible {
			return fmt.Errorf("tool %q is not available for the current model. Use %q instead: create a file with `*** Add File:` or replace an existing file's contents with `*** Update File:` hunks", tools.NameWrite, tools.NameApplyPatch)
		}
	case tools.NameDelete:
		if patchVisible {
			return fmt.Errorf("tool %q is not available for the current model. Use %q instead: remove files with `*** Delete File:`", tools.NameDelete, tools.NameApplyPatch)
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

	pctx := toolPermissionContext{}
	if p.loopExitAuthorized != nil {
		pctx.LoopExitAuthorized = p.loopExitAuthorized()
	}
	if p.preapprovedPermission != nil && p.preapprovedPermission(tc.ID, tc.Name, tc.Args, p.effectiveToolBaseDir(), pctx) {
		return nil
	}
	decision := evaluateToolPermissionInDirWithContext(ruleset, tc.Name, tc.Args, p.effectiveToolBaseDir(), pctx)
	switch decision.Action {
	case permission.ActionDeny:
		logToolPermissionDenied(p.logPrefix, p.agentID, tc.Name, decision.MatchArgument)
		return wrapToolPermissionDenied(tc.Name)
	case permission.ActionAsk:
		if tc.Name == tools.NameDone {
			return nil
		}
		if p.yoloDowngradeAsk != nil && p.yoloDowngradeAsk(tc.Name) {
			// YOLO relaxes ask to an implicit allow so the user is not
			// prompted: mechanism tool asks stop confirming on the main agent,
			// and a SubAgent's ordinary asks stop confirming while the parent
			// YOLO is on. Deny decisions never reach this branch; done returns
			// before it and an explicit compact_context ask reports false, so
			// those stay as they are.
			return nil
		}
		if p.confirm == nil {
			return wrapToolRequiresConfirmation(tc.Name)
		}
		// Carry the owning agent ID into the confirmation flow so its user-wait
		// walltime is attributed to the right agent (SubAgent confirmations
		// resolve through the shared parent confirmFn).
		confirmCtx := tools.WithAgentID(ctx, p.agentID)
		if p.currentTurnID != nil {
			confirmCtx = tools.WithTurnID(confirmCtx, p.currentTurnID())
		}
		resp, err := p.confirm(confirmCtx, tc.Name, string(tc.Args), decision.NeedsApprovalPaths, decision.AlreadyAllowedPaths, decision.NeedsApprovalRules, decision.AlreadyAllowedRules)
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
		editedArgs, err := applyConfirmedArgsEdits(p.registry, ruleset, tc.Name, tc.Args, resp.FinalArgsJSON, p.effectiveToolBaseDir())
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
		p.emit(ToolCallUpdateEvent{ID: tc.ID, Name: tc.Name, ArgsJSON: modelRequestedToolArgsJSON(execResult.EffectiveArgsJSON, execResult.Audit), ArgsStreamingDone: true, AgentID: p.eventAgentID})
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
// the same file is not read and hashed twice per call. It returns stale=true
// when the file changed after the model's last read; the caller then backs up
// the current content and continues the write instead of rejecting it. An
// existing file the model has never seen returns unobserved=true alongside
// stale=true so the caller backs up its current contents and continues instead
// of refusing: write never rejects on an unobserved target. An existing
// regular file whose contents cannot be read at all (preVerifyErr) also
// proceeds: nothing can be backed up, so the caller only reminds. Non-regular
// targets (directory, socket, FIFO, device) still refuse because opening them
// for write fails or blocks.
func requireObservedDestructiveWrite(track *filelock.FileTracker, agentID, toolName, path string, writeStatus filelock.WriteStatus, lock *trackedWriteLock) (stale, unobserved bool, err error) {
	if track == nil || path == "" || toolName != tools.NameWrite || lock == nil {
		return false, false, nil
	}
	action := strings.ToLower(toolName)
	if !lock.preExists {
		return false, false, nil
	}
	if lock.preVerifyErr != nil {
		info, statErr := os.Lstat(path)
		if statErr != nil {
			return false, false, nil // vanished since locking; treat as a fresh write
		}
		if !info.Mode().IsRegular() {
			return false, false, fmt.Errorf("refusing to %s file %s because its current state cannot be verified: %w", action, path, lock.preVerifyErr)
		}
		// Regular file that cannot be read: proceed and remind instead of
		// refusing. The backup attempt inside the caller fails the same way,
		// so the result honestly shows no backup location. The empty hash
		// routes through the same observed/unobserved wording as any other
		// unread state.
		return requireCurrentFileObservation(track, agentID, path, "", action, writeStatus.ExternalChanged)
	}
	return requireCurrentFileObservation(track, agentID, path, lock.preHash, action, writeStatus.ExternalChanged)
}

// guardAbnormalToolArgs turns the two truncation signatures — the malformed-args
// sentinel and empty args for a tool that declares required fields — into
// guidance the model can act on, instead of letting them reach schema checking
// as ordinary shape errors.
func guardAbnormalToolArgs(registry *tools.Registry, tc message.ToolCall, logPrefix, agentID string) error {
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
	return nil
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
	preModTime   time.Time
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
	lock.preHash, lock.preExists, lock.preModTime, lock.preVerifyErr = verifiedCurrentFileHash(path)
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
			log.Warnf("failed to create stale file backup path=%v tool=%v error=%v", trackedPath, tc.Name, err)
			return fileBackupOutcome{}
		}
		return fileBackupOutcome{Records: nonEmptyBackupRecords(backup)}
	case tools.NameWrite:
		if !stale || trackedPath == "" {
			return fileBackupOutcome{}
		}
		data, existed, err := readPreWriteBytes(trackedPath)
		if err != nil {
			log.Warnf("failed to read stale file for backup path=%v tool=%v error=%v", trackedPath, tc.Name, err)
			return fileBackupOutcome{}
		}
		if !existed || len(data) == 0 {
			return fileBackupOutcome{}
		}
		backup, err := p.fileBackups.Backup(trackedPath, tc.Name, data)
		if err != nil {
			log.Warnf("failed to create stale file backup path=%v tool=%v error=%v", trackedPath, tc.Name, err)
			return fileBackupOutcome{}
		}
		return fileBackupOutcome{Records: nonEmptyBackupRecords(backup)}
	case tools.NameDelete:
		if deleteLocks == nil || len(deleteLocks.paths) == 0 {
			return fileBackupOutcome{}
		}
		backups := make([]fileBackupRecord, 0, len(deleteLocks.locked))
		for _, locked := range deleteLocks.locked {
			if !locked.backupRequired || locked.symlink {
				continue
			}
			path := locked.path
			data, existed, err := readPreWriteBytes(path)
			if err != nil {
				log.Warnf("failed to read deleted file for backup path=%v tool=%v error=%v", path, tc.Name, err)
				continue
			}
			if !existed {
				continue
			}
			backup, err := p.fileBackups.Backup(path, tc.Name, data)
			if err != nil {
				log.Warnf("failed to create deleted file backup path=%v tool=%v error=%v", path, tc.Name, err)
				continue
			}
			if backup.Path != "" {
				backups = append(backups, backup)
			}
		}
		return fileBackupOutcome{Records: backups}
	default:
		return fileBackupOutcome{}
	}
}

func (p toolExecutionPipeline) backupRiskyPatchState(tc message.ToolCall, stale bool, sources []fileBackupSource) fileBackupOutcome {
	if p.fileBackups == nil || !stale {
		return fileBackupOutcome{}
	}
	var records []fileBackupRecord
	for _, source := range sources {
		if len(source.Data) == 0 {
			continue
		}
		backup, err := p.fileBackups.Backup(source.Path, tc.Name, source.Data)
		if err != nil {
			log.Warnf("failed to create stale file backup path=%v tool=%v error=%v", source.Path, tc.Name, err)
			continue
		}
		if backup.Path != "" {
			records = append(records, backup)
		}
	}
	return fileBackupOutcome{Records: records}
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
	if toolName == tools.NameQuestion || toolName == tools.NameRead {
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
