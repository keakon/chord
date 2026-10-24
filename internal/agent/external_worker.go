package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/ctxmgr"
	"github.com/keakon/chord/internal/externalclaude"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/tools"
)

// externalSessionStopGrace bounds how long an external CLI process is given to
// exit on its own after Stop, so it can flush the session transcript a later
// --resume turn reads, before Chord escalates to SIGTERM.
const externalSessionStopGrace = 2 * time.Second

// externalShutdownWait caps how long app shutdown waits for an external
// worker's teardown before leaving the rest to its supervisor process: the
// supervisor finishes the escalation (grace, SIGTERM, SIGKILL) and leaves the
// receipt restore trusts, so waiting longer only delays the exit. A CLI that
// answers interrupt or stdin EOF settles within this budget; a slower one
// converges through the restore-time interrupted record instead.
const externalShutdownWait = 500 * time.Millisecond

// externalProgressMinInterval and externalProgressBurstItems implement the TUI
// status throttle: at most one update per interval, with a burst of distinct
// items released every N so heavy tool traffic cannot leave the status line on
// a stale summary. They never gate an owner-facing message.
const (
	externalProgressMinInterval = 5 * time.Second
	externalProgressBurstItems  = 8

	externalTerminationRetryAttempts = 10
	externalTerminationRetryDelay    = 500 * time.Millisecond
)

// externalWorker executes a subagent task on an external coding-agent process
// (Claude Code stream-json). It implements subAgentRuntime so admission, task
// records, the completion mailbox, stall scanning and shutdown stay shared
// with SubAgent. It owns no LLM client or tool registry: the external
// process executes tools itself, and Chord only decides permissions.
type externalWorker struct {
	parent          *MainAgent
	agentDef        *config.AgentConfig
	backend         config.BackendConfig
	instanceID      string
	taskID          string
	agentDefName    string
	taskDesc        string
	planTaskRef     string
	semanticTaskKey string
	color           string

	ownerMu      sync.RWMutex
	ownerAgentID string
	ownerTaskID  string
	depth        int
	joinToOwner  bool

	resultSchema              map[string]any
	resultSchemaJSON          json.RawMessage
	workDir                   string
	workDirState              WorkDirState
	sessionEpoch              uint64
	persistence               agentPersistenceHealth
	baseline                  *externalCheckoutBaseline
	inputs                    []externalTaskInput
	inputSeq                  uint64
	processEnded              bool
	terminalSettlement        *TaskSettlement
	terminalSettlementDurable bool
	// runID is the last supervisor run persisted for this task. It is written
	// before the launch so a receipt names exactly one run.
	runID string

	rulesetMu sync.RWMutex
	ruleset   permission.Ruleset

	ctx    context.Context
	cancel context.CancelFunc

	runtimeState subAgentRuntimeState

	semMu       sync.Mutex
	semHeld     bool
	semBorrowed bool
	semBypassed bool

	sessionMu          sync.Mutex
	session            *externalclaude.Session
	sessionID          string
	sessionEstablished bool
	resumeNext         bool

	started      atomic.Bool
	closing      atomic.Bool
	finished     atomic.Bool
	finishOnce   sync.Once
	closePending atomic.Bool
	// cancelRequested claims the settlement for an in-flight close request
	// (user cancel) so the run loop, which wakes on the cancelled context
	// before the request's event is dispatched, cannot synthesize a competing
	// close event with a generic reason.
	cancelRequested          atomic.Bool
	terminationRetryPending  atomic.Bool
	releaseUnproven          atomic.Bool
	terminationRetryAttempts int
	terminationRetryDelay    time.Duration
	done                     chan struct{}
	doneOnce                 sync.Once

	promptMu           sync.Mutex
	lastReplyMessageID string
	initialPrompt      string
	promptQueue        []externalTaskInput
	inputClosed        bool
	resultSeen         bool
	resultText         string
	resultError        error
	resultMu           sync.Mutex
	streamRequestSeq   uint64
	streamActive       bool

	progressMu     sync.Mutex
	lastProgressAt time.Time
	lastProgress   string
	progressItems  int

	toolMu              sync.Mutex
	toolNames           map[string]string
	toolPreWrites       map[string]externalToolPreWrite
	externalToolStateMu sync.Mutex
	toolSeen            map[string]bool
	toolBindings        map[string]*externalToolBinding
	permissionMu        sync.Mutex
	permissionWG        sync.WaitGroup
	permissionRequests  map[string]externalPermissionRequest
	permissionOutcomes  map[string]*externalPermissionOutcome
	historyMu           sync.Mutex
	ctxMgr              *ctxmgr.Manager

	lastAssistantMu sync.Mutex
	lastAssistant   string
}

type externalWorkerConfig struct {
	agentDef         *config.AgentConfig
	instanceID       string
	taskID           string
	taskDesc         string
	planTaskRef      string
	semanticTaskKey  string
	resultSchemaJSON json.RawMessage
	baseline         *externalCheckoutBaseline
	caller           delegationCaller
	workDir          string
	ctx              context.Context
	cancel           context.CancelFunc
}

func (a *MainAgent) newExternalWorker(cfg externalWorkerConfig) (*externalWorker, error) {
	def := cfg.agentDef
	if def == nil || !def.IsSubAgent() || !def.IsExternalBackend() {
		return nil, fmt.Errorf("external worker requires a backend config")
	}
	workDir := strings.TrimSpace(cfg.workDir)
	if workDir == "" {
		workDir = a.subAgentWorkDir()
	}
	defCopy := *def
	backendCopy := *def.Backend
	backendCopy.LaunchArgs = slices.Clone(backendCopy.LaunchArgs)
	backendCopy.Env = maps.Clone(backendCopy.Env)
	defCopy.Backend = &backendCopy
	// The contract is recompiled from the canonical bytes the admission path
	// persisted, exactly like a restored task recompiles it, so a worker never
	// runs against a schema the runtime validator would not accept.
	resultSchema, _, err := tools.CompileResultSchema(cfg.resultSchemaJSON)
	if err != nil {
		return nil, fmt.Errorf("invalid result_schema: %w", err)
	}
	worker := &externalWorker{
		parent:                   a,
		agentDef:                 &defCopy,
		backend:                  backendCopy,
		instanceID:               cfg.instanceID,
		taskID:                   cfg.taskID,
		agentDefName:             def.Name,
		taskDesc:                 strings.TrimSpace(cfg.taskDesc),
		planTaskRef:              strings.TrimSpace(cfg.planTaskRef),
		semanticTaskKey:          strings.TrimSpace(cfg.semanticTaskKey),
		color:                    def.Color,
		ownerAgentID:             strings.TrimSpace(cfg.caller.AgentID),
		ownerTaskID:              strings.TrimSpace(cfg.caller.TaskID),
		depth:                    cfg.caller.Depth + 1,
		joinToOwner:              !cfg.caller.IsMain && cfg.caller.Delegation.ChildJoinEnabled(),
		resultSchema:             resultSchema,
		resultSchemaJSON:         append(json.RawMessage(nil), cfg.resultSchemaJSON...),
		workDir:                  workDir,
		workDirState:             cfg.caller.WorkDirState,
		ruleset:                  a.buildSubAgentRuleset(def),
		ctx:                      cfg.ctx,
		cancel:                   cfg.cancel,
		terminationRetryAttempts: externalTerminationRetryAttempts,
		terminationRetryDelay:    externalTerminationRetryDelay,
		done:                     make(chan struct{}),
		toolNames:                make(map[string]string),
		toolPreWrites:            make(map[string]externalToolPreWrite),
		toolSeen:                 make(map[string]bool),
		toolBindings:             make(map[string]*externalToolBinding),
		ctxMgr:                   ctxmgr.NewManager(0, 0),
		permissionRequests:       make(map[string]externalPermissionRequest),
		permissionOutcomes:       make(map[string]*externalPermissionOutcome),
	}
	worker.sessionEpoch = a.recoverySessionEpoch()
	if err := worker.ensureSessionID(); err != nil {
		return nil, err
	}
	if cfg.baseline != nil {
		// A resumed attempt keeps the baseline its task started from, so the
		// completion envelope still answers "what changed since the task began".
		baseline := *cfg.baseline
		worker.baseline = &baseline
	}
	return worker, nil
}

func (w *externalWorker) InstanceID() string { return w.instanceID }

func (w *externalWorker) TaskID() string { return w.taskID }

func (w *externalWorker) AgentDefName() string { return w.agentDefName }

func (w *externalWorker) State() SubAgentState {
	state, _ := w.runtimeState.snapshot()
	if state == "" {
		return SubAgentStateIdle
	}
	return state
}

func (w *externalWorker) StateChangedAt() time.Time {
	_, changedAt := w.runtimeState.artifactSnapshot()
	return changedAt
}

func (w *externalWorker) PersistenceHealth() PersistenceHealth {
	return w.persistence.snapshot()
}

func (w *externalWorker) LastSummary() string {
	_, summary := w.runtimeState.snapshot()
	return summary
}

func (w *externalWorker) LastArtifact() tools.ArtifactRef {
	ref, _ := w.runtimeState.artifactSnapshot()
	return ref
}

func (w *externalWorker) TaskDescription() string { return w.taskDesc }

func (w *externalWorker) ColorCode() string { return w.color }

func (w *externalWorker) OwnerSnapshot() (ownerAgentID, ownerTaskID string, depth int, joinToOwner bool) {
	w.ownerMu.RLock()
	defer w.ownerMu.RUnlock()
	return w.ownerAgentID, w.ownerTaskID, w.depth, w.joinToOwner
}

func (w *externalWorker) LastReplyMessageID() string {
	w.promptMu.Lock()
	defer w.promptMu.Unlock()
	return w.lastReplyMessageID
}

func (w *externalWorker) markActivity() { w.runtimeState.markActivity() }

// stallAlertRaised/raiseStallAlert/clearStallAlert share the stall-episode
// dedup flag on subAgentRuntimeState so the lifecycle sweep's per-episode
// notification works for an external runtime exactly as it does for a
// SubAgent.
func (w *externalWorker) stallAlertRaised() bool {
	if w == nil {
		return false
	}
	return w.runtimeState.stallAlerted()
}

func (w *externalWorker) raiseStallAlert() {
	if w == nil {
		return
	}
	w.runtimeState.setStallAlerted(true)
}

func (w *externalWorker) clearStallAlert() {
	if w == nil {
		return
	}
	w.runtimeState.setStallAlerted(false)
}

func (w *externalWorker) injectInitialInput(description string) bool {
	if w.started.Load() {
		return false
	}
	description = strings.TrimSpace(description)
	w.promptMu.Lock()
	w.initialPrompt = description
	w.inputs = []externalTaskInput{{ID: w.instanceID + ":1", Content: description, State: message.ToolRecoveryStateNotStarted}}
	w.inputSeq = 1
	w.processEnded = true
	w.promptMu.Unlock()
	return true
}

func (w *externalWorker) removeInitialInput() {
	w.promptMu.Lock()
	w.initialPrompt = ""
	w.promptMu.Unlock()
}

func (w *externalWorker) takeSlot() {
	w.semMu.Lock()
	w.semHeld = true
	w.semMu.Unlock()
}

func (w *externalWorker) releaseSlot() (held, borrowed, bypassed bool) {
	w.semMu.Lock()
	defer w.semMu.Unlock()
	held, borrowed, bypassed = w.semHeld, w.semBorrowed, w.semBypassed
	w.semHeld = false
	w.semBorrowed = false
	w.semBypassed = false
	return held, borrowed, bypassed
}

func (w *externalWorker) slotHeld() bool {
	w.semMu.Lock()
	defer w.semMu.Unlock()
	return w.semHeld
}

func (w *externalWorker) startRuntime() {
	if w == nil || w.started.Swap(true) {
		return
	}
	w.runtimeState.set(SubAgentStateRunning, "external agent starting")
	// The registration record is written before the runtime starts, so its
	// state is still idle at that point. Persist the running transition now:
	// a crash before the first input write must not restore a live task as an
	// idle record that the sidebar and coordination snapshot cannot see.
	w.persistExternalLifecycleState()
	go w.runExternalWorker()
}

func (w *externalWorker) persistExternalLifecycleState() {
	if w == nil {
		return
	}
	w.promptMu.Lock()
	err := w.persistExternalSessionLocked()
	w.promptMu.Unlock()
	if err != nil {
		log.Warnf("persist external task lifecycle state task_id=%v state=%v error=%v", w.taskID, w.State(), err)
	}
}

func (w *externalWorker) abortStartup() {
	if w == nil {
		return
	}
	w.teardown()
}

func (w *externalWorker) buildTaskRecord(previous *DurableTaskRecord, closedReason string, currentTurn uint64, now time.Time) *DurableTaskRecord {
	if w == nil {
		return nil
	}
	state := w.State()
	summary := w.LastSummary()
	ownerAgentID, ownerTaskID, depth, joinToOwner := w.OwnerSnapshot()
	rec := cloneDurableTaskRecord(previous)
	if rec == nil {
		rec = &DurableTaskRecord{
			TaskID:      w.taskID,
			CreatedAt:   now,
			CreatedTurn: currentTurn,
			Attempt:     1,
		}
	}
	if rec.Attempt == 0 {
		rec.Attempt = 1
	}
	rec.LifecycleRevision++
	rec.AgentDefName = w.agentDefName
	rec.TaskDesc = w.taskDesc
	rec.PlanTaskRef = w.planTaskRef
	rec.SemanticTaskKey = w.semanticTaskKey
	rec.ResultSchema = cloneRawJSON(w.resultSchemaJSON)
	rec.OwnerAgentID = ownerAgentID
	rec.OwnerTaskID = ownerTaskID
	rec.Depth = depth
	rec.JoinToOwner = joinToOwner
	rec.State = string(state)
	rec.ResumePolicy = durableTaskResumePolicy(state)
	rec.LatestInstanceID = w.instanceID
	rec.InstanceHistory = dedupeTaskInstanceHistory(append(rec.InstanceHistory, w.instanceID))
	rec.LastSummary = summary
	rec.Persistence = w.PersistenceHealth()
	w.promptMu.Lock()
	envKeys := slices.Sorted(maps.Keys(w.backend.Env))
	rec.ExternalSession = &ExternalTaskSession{
		Backend:   config.BackendKindClaudeCode,
		SessionID: w.currentSessionID(), SessionEstablished: w.currentSessionEstablished(),
		WorkDir: w.workDir, WorkDirState: w.workDirState, Prompt: strings.TrimSpace(w.agentDef.SystemPrompt + "\n\n" + w.agentDef.PromptAppend),
		Command: w.backend.EffectiveCommand(), LaunchArgs: slices.Clone(w.backend.LaunchArgs),
		EnvKeys: envKeys, ProcessEnded: w.processEnded, RunID: w.runID, Inputs: slices.Clone(w.inputs),
		Baseline: w.baseline,
	}
	w.promptMu.Unlock()
	rec.RuntimeParked = false
	rec.LastUpdatedTurn = currentTurn
	rec.UpdatedAt = now
	if closedReason != "" {
		rec.ClosedReason = closedReason
	}
	return rec
}

func (w *externalWorker) currentSessionID() string {
	w.sessionMu.Lock()
	defer w.sessionMu.Unlock()
	return w.sessionID
}

func (w *externalWorker) currentSessionEstablished() bool {
	w.sessionMu.Lock()
	defer w.sessionMu.Unlock()
	return w.sessionEstablished
}

// markSessionEstablished records that the CLI completed initialize. Only then
// does the generated session id name a transcript that --resume can load.
func (w *externalWorker) markSessionEstablished(sessionID string) {
	w.sessionMu.Lock()
	if externalclaude.ValidSessionID(sessionID) {
		w.sessionID = sessionID
	}
	w.sessionEstablished = true
	w.sessionMu.Unlock()
	w.persistExternalLifecycleState()
}

func (w *externalWorker) persistRegistration(sessionDir string, record *DurableTaskRecord) error {
	if w == nil || w.parent == nil {
		return nil
	}
	w.parent.taskRegistryPersistMu.Lock()
	defer w.parent.taskRegistryPersistMu.Unlock()
	w.parent.subs.mu.RLock()
	records := cloneDurableTaskRecordMap(w.parent.subs.taskRecords)
	w.parent.subs.mu.RUnlock()
	if records == nil {
		records = make(map[string]*DurableTaskRecord)
	}
	records[w.taskID] = cloneDurableTaskRecord(record)
	if hook := w.parent.taskRegistryPersistHook; hook != nil {
		hook()
	}
	if err := persistDurableTaskRecords(sessionDir, records); err != nil {
		return err
	}
	return w.persistExternalMessage(message.Message{Role: message.RoleUser, Content: w.initialPrompt, InputID: w.inputs[0].ID})
}

func (w *externalWorker) removeRegistration(sessionDir, taskID string) {
	if w == nil || w.parent == nil {
		return
	}
	_ = w.parent.persistTaskRegistryRecord(sessionDir, taskID, nil)
}

// setState transitions the worker state, ignoring illegal transitions.
func (w *externalWorker) setState(state SubAgentState, summary string) bool {
	return w.runtimeState.set(state, summary)
}

func (w *externalWorker) updateProgress(summary string) {
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return
	}
	if w.runtimeState.updateProgress(summary) {
		w.markActivity()
		w.emitProgressStatus(summary)
	}
}

func (w *externalWorker) noteAssistantText(text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	w.lastAssistantMu.Lock()
	w.lastAssistant = text
	w.lastAssistantMu.Unlock()
	w.updateProgress(text)
}

func (w *externalWorker) lastAssistantText() string {
	w.lastAssistantMu.Lock()
	defer w.lastAssistantMu.Unlock()
	return w.lastAssistant
}

// emitProgressStatus refreshes the TUI status line only. The owner is not told
// about routine tool or assistant progress: like a native SubAgent, whose
// summary only advances on an explicit Notify, an external worker reaches the
// main agent only through Notify, a terminal report, or a stall alert. The
// external CLI has no Notify tool, so it notifies solely by stopping.
func (w *externalWorker) emitProgressStatus(summary string) {
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return
	}
	w.progressMu.Lock()
	now := time.Now()
	// Drop an unchanged summary so a repeated progress line never re-notifies
	// the owner, then throttle the rest to one update per interval while still
	// releasing a burst every N items so the latest state stays visible under
	// heavy tool traffic.
	if summary == w.lastProgress {
		w.progressMu.Unlock()
		return
	}
	if !w.lastProgressAt.IsZero() && now.Sub(w.lastProgressAt) < externalProgressMinInterval {
		w.progressItems++
		if w.progressItems < externalProgressBurstItems {
			w.progressMu.Unlock()
			return
		}
	}
	w.lastProgressAt = now
	w.lastProgress = summary
	w.progressItems = 0
	w.progressMu.Unlock()
	if w.parent == nil {
		return
	}
	w.parent.emitToTUI(AgentStatusEvent{AgentID: w.instanceID, Status: "running", Message: summary})
}

func (w *externalWorker) rememberToolName(id, name string) {
	w.toolMu.Lock()
	w.toolNames[id] = name
	w.toolMu.Unlock()
}

func (w *externalWorker) toolName(id string) string {
	w.toolMu.Lock()
	defer w.toolMu.Unlock()
	return w.toolNames[id]
}

func (w *externalWorker) forgetToolName(id string) {
	w.toolMu.Lock()
	delete(w.toolNames, id)
	w.toolMu.Unlock()
}

func (w *externalWorker) teardown() {
	if w == nil {
		return
	}
	w.closing.Store(true)
	if w.cancel != nil {
		w.cancel()
	}
	w.sessionMu.Lock()
	session := w.session
	w.sessionMu.Unlock()
	if session != nil {
		session.Stop(externalSessionStopGrace)
	}
}

// requestUserCancel answers the latest user cancel for a live external worker:
// it stops the external process and asks the shared close handler to settle the
// task as cancelled by the user. Settlement stays in the close handler (the
// event loop owns terminal truth), so this only signals and never mutates the
// task record inline. Reports whether a live, unsettled worker was signalled.
func (w *externalWorker) requestUserCancel(reason string) bool {
	if w == nil {
		return false
	}
	if w.finished.Load() || w.closing.Load() {
		return false
	}
	w.cancelRequested.Store(true)
	w.teardown()
	if w.parent != nil {
		w.parent.sendEvent(Event{
			Type:     EventSubAgentCloseRequested,
			SourceID: w.instanceID,
			Payload: &SubAgentCloseRequestedPayload{
				Reason:       reason,
				ClosedReason: "stopped by user",
				FinalState:   SubAgentStateCancelled,
			},
		})
	}
	return true
}

func (w *externalWorker) waitDone(ctx context.Context) error {
	select {
	case <-w.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (w *externalWorker) terminateForSessionSwitch(reason string) {
	if w == nil {
		return
	}
	// Session switches stop and settle started workers before switching, so
	// this only reaches a worker that was still starting when they checked.
	w.parent.closeExternalWorker(w, SubAgentStateCancelled, reason, reason, nil, 0, false)
}

func (w *externalWorker) finishSuccess(text string) {
	w.finishOnce.Do(func() {
		w.finished.Store(true)
		summary := strings.TrimSpace(text)
		if summary == "" {
			summary = w.lastAssistantText()
		}
		if summary == "" {
			summary = "external agent completed without a text result"
		}
		w.setState(SubAgentStateCompleted, summary)
		envelope := w.externalCompletionEnvelope(summary)
		w.persistExternalTerminalSettlement(envelope)
		result := &AgentResult{Summary: summary, Envelope: envelope}
		if w.parent != nil {
			w.parent.sendEvent(Event{Type: EventAgentDone, SourceID: w.instanceID, Payload: result})
		}
	})
}

func (w *externalWorker) finishFailure(err error) {
	w.finishOnce.Do(func() {
		w.finished.Store(true)
		message := "external agent failed"
		if err != nil {
			message = err.Error()
		}
		w.setState(SubAgentStateFailed, message)
		w.persistExternalTerminalSettlement(nil)
		if w.parent != nil {
			w.parent.sendEvent(Event{
				Type:     EventSubAgentCloseRequested,
				SourceID: w.instanceID,
				Payload: &SubAgentCloseRequestedPayload{
					Reason:       message,
					ClosedReason: "external agent failed",
					FinalState:   SubAgentStateFailed,
				},
			})
		}
	})
}

// finishCancelledUnlessRequested settles the worker as cancelled unless a
// close request from outside the run loop (user cancel, session switch) is
// already carrying the settlement. Synthesizing a second close event here
// would race that request through the event loop and could overwrite its
// closed reason.
func (w *externalWorker) finishCancelledUnlessRequested() {
	if w.closePending.Load() || w.cancelRequested.Load() {
		return
	}
	w.finishCancelled("external agent cancelled")
}

func (w *externalWorker) finishCancelled(reason string) {
	w.finishOnce.Do(func() {
		w.finished.Store(true)
		w.setState(SubAgentStateCancelled, reason)
		w.persistExternalTerminalSettlement(nil)
		if w.parent != nil {
			w.parent.sendEvent(Event{
				Type:     EventSubAgentCloseRequested,
				SourceID: w.instanceID,
				Payload: &SubAgentCloseRequestedPayload{
					Reason:       reason,
					ClosedReason: "external agent cancelled",
					FinalState:   SubAgentStateCancelled,
				},
			})
		}
	})
}

func (w *externalWorker) externalCompletionEnvelope(summary string) *CompletionEnvelope {
	return externalCheckoutCompletion(w.workDir, w.baseline, w.workDirState, summary)
}

func (w *externalWorker) tryCleanupProcess(escalate bool) bool {
	if w == nil {
		return false
	}
	session := w.currentSession()
	if session == nil {
		return false
	}
	if !session.TerminationProven() && (!escalate || !forceExternalProcessCleanup(session)) {
		return false
	}
	w.promptMu.Lock()
	w.processEnded = true
	err := w.persistExternalSessionLocked()
	w.promptMu.Unlock()
	if err != nil {
		w.persistence.markDegraded(err)
	}
	return true
}

// closeExternalWorker is the external runtime's terminal close path. It mirrors
// the SubAgent close handler's ordering: mailbox/status first, then the detached
// settlement, then runtime and slot teardown.
func (a *MainAgent) closeExternalWorker(worker *externalWorker, finalState SubAgentState, reason, closedReason string, completion *CompletionEnvelope, cleanupAttempt int, forceRelease bool) {
	if a == nil || worker == nil {
		return
	}
	if worker.sessionEpoch != a.recoverySessionEpoch() {
		select {
		case <-worker.done:
		default:
			log.Debugf("external worker close event belongs to a replaced session agent_id=%v", worker.instanceID)
			return
		}
		// The session this worker ran in was replaced (another switch won
		// while it was being stopped): its task record belongs to that
		// session's restore-time normalization, but the runtime slot must not
		// leak with the dropped settlement. An unproven termination keeps
		// ProcessEnded=false in the old record, so resume stays refused there.
		if worker.started.Load() {
			worker.promptMu.Lock()
			ended := worker.processEnded
			worker.promptMu.Unlock()
			if !ended {
				worker.releaseUnproven.Store(true)
			}
			a.releaseRuntimeSlot(worker)
		}
		log.Warnf("external worker settlement dropped for a replaced session; runtime slot released agent_id=%v task_id=%v", worker.instanceID, worker.taskID)
		return
	}
	if cleanupAttempt > 0 {
		worker.terminationRetryPending.Store(false)
	}
	if worker.started.Load() {
		select {
		case <-worker.done:
		default:
			if worker.closePending.Swap(true) {
				return
			}
			if finalState != SubAgentStateCompleted {
				worker.teardown()
			}
			go func() {
				<-worker.done
				if finalState == SubAgentStateCompleted {
					a.sendEvent(Event{Type: EventAgentDone, SourceID: worker.instanceID,
						Payload: &AgentResult{Summary: reason, Envelope: completion}})
				} else {
					a.sendEvent(Event{Type: EventSubAgentCloseRequested, SourceID: worker.instanceID,
						Payload: &SubAgentCloseRequestedPayload{FinalState: finalState, Reason: reason, ClosedReason: closedReason, worker: worker}})
				}
			}()
			return
		}
	}
	worker.promptMu.Lock()
	ended := worker.processEnded || !worker.started.Load()
	worker.promptMu.Unlock()
	cleanupRequest := func(attempt int) *SubAgentCloseRequestedPayload {
		return &SubAgentCloseRequestedPayload{
			Reason:         reason,
			ClosedReason:   closedReason,
			FinalState:     finalState,
			Completion:     completion,
			CleanupAttempt: attempt,
		}
	}
	if !ended {
		if cleanupAttempt == 0 && !forceRelease {
			// The first attempt escalates cleanup and can wait out the
			// supervisor's kill grace. Running it inline would stall every
			// task event behind one unresponsive process.
			if worker.terminationRetryPending.CompareAndSwap(false, true) {
				go func() {
					worker.tryCleanupProcess(true)
					a.sendEvent(Event{Type: EventSubAgentCloseRequested, SourceID: worker.instanceID, Payload: cleanupRequest(1)})
				}()
			} else {
				log.Debugf("external cleanup escalation already in flight agent_id=%v", worker.instanceID)
			}
			return
		}
		ended = worker.tryCleanupProcess(cleanupAttempt == 0)
	}
	if !ended && !forceRelease {
		attempts := worker.terminationRetryAttempts
		if attempts <= 0 {
			attempts = externalTerminationRetryAttempts
		}
		if cleanupAttempt < attempts {
			a.emitToTUI(AgentStatusEvent{AgentID: worker.instanceID, Status: "blocked", Message: "External process termination is not proven; retrying cleanup"})
			if worker.terminationRetryPending.CompareAndSwap(false, true) {
				payload := cleanupRequest(cleanupAttempt + 1)
				delay := worker.terminationRetryDelay
				if delay <= 0 {
					delay = externalTerminationRetryDelay
				}
				go func() {
					timer := time.NewTimer(delay)
					defer timer.Stop()
					<-timer.C
					a.sendEvent(Event{Type: EventSubAgentCloseRequested, SourceID: worker.instanceID, Payload: payload})
				}()
			} else {
				log.Debugf("external cleanup retry already scheduled agent_id=%v", worker.instanceID)
			}
			return
		}
		forceRelease = true
	}
	if !ended {
		worker.releaseUnproven.Store(true)
		worker.terminationRetryPending.Store(false)
		if finalState == SubAgentStateCompleted || finalState == "" {
			finalState = SubAgentStateFailed
		}
		reason = strings.TrimSpace(reason)
		if reason == "" {
			reason = "External process termination is not proven"
		}
		reason += "; the runtime slot was released because termination could not be proven, and this checkout must not be resumed"
		closedReason = "external process termination is not proven"
		completion = nil
		worker.promptMu.Lock()
		worker.processEnded = false
		_ = worker.persistExternalSessionLocked()
		worker.promptMu.Unlock()
		worker.teardown()
	}
	if finalState == "" {
		finalState = SubAgentStateCancelled
	}
	if !isTerminalSubAgentState(finalState) {
		finalState = SubAgentStateCancelled
	}
	worker.promptMu.Lock()
	settlement := cloneTaskSettlement(worker.terminalSettlement)
	settlementDurable := worker.terminalSettlementDurable
	worker.promptMu.Unlock()
	if settlement != nil {
		finalState = SubAgentState(settlement.Outcome)
		reason = settlement.Summary
		completion = settlement.Completion
	}
	reason = strings.TrimSpace(reason)
	closedReason = strings.TrimSpace(closedReason)
	if closedReason == "" {
		closedReason = reason
	}
	if worker.State() != finalState {
		worker.setState(finalState, reason)
	}

	a.subs.mu.RLock()
	previous := cloneDurableTaskRecord(a.subs.taskRecords[worker.taskID])
	a.subs.mu.RUnlock()
	record := worker.buildTaskRecord(previous, closedReason, a.explicitUserTurnCount.Load(), time.Now())
	if record == nil {
		return
	}
	record.State = string(finalState)
	record.RuntimeParked = true
	record.ResumePolicy = durableTaskResumePolicy(finalState)
	record.LastCompletion = normalizeCompletionEnvelope(completion)
	if settlement != nil {
		record.LatestSettlement = settlement
		record.SettlementDurable = settlementDurable
	}
	if completion != nil {
		record.LastArtifactRefs = mergeArtifactRefs(record.LastArtifactRefs, completion.Artifacts)
	}
	if finalState == SubAgentStateFailed || finalState == SubAgentStateCancelled {
		record.ClosedReason = closedReason
	}
	droppedInputs := 0
	if finalState == SubAgentStateCancelled {
		droppedInputs = discardQueuedExternalInputs(record)
	}
	a.subs.mu.Lock()
	a.subs.taskRecords[worker.taskID] = record
	a.subs.notifyTaskChangeLocked()
	a.subs.mu.Unlock()
	if err := a.persistTaskRegistry(); err != nil {
		worker.persistence.markDegraded(err)
	}

	if finalState == SubAgentStateFailed {
		a.emitExternalFailureMailbox(worker, reason)
	}
	state := a.settleDetachedTerminalTask(worker.taskID, finalState, reason, closedReason)
	if state == "" {
		state = finalState
	}
	a.subs.removeRuntime(worker.instanceID)
	a.releaseRuntimeSlot(worker)
	a.fileTrack.ReleaseAll(worker.instanceID)
	tools.StopAllJobsForAgent(worker.instanceID, closedReason)
	worker.teardown()
	a.emitActivity(worker.instanceID, ActivityIdle, "")
	status := string(state)
	switch state {
	case SubAgentStateCompleted:
		status = "completed"
	case SubAgentStateFailed:
		status = "failed"
	case SubAgentStateCancelled:
		status = "cancelled"
	}
	a.emitToTUI(AgentStatusEvent{AgentID: worker.instanceID, Status: status, Message: reason})
	if droppedInputs > 0 {
		a.emitToTUI(ToastEvent{AgentID: worker.instanceID, Message: fmt.Sprintf("Task %s was cancelled; %d queued input(s) were not run", worker.taskID, droppedInputs), Level: "warn"})
	}
	a.resumeQueuedExternalInputs(worker.taskID)
}

func (a *MainAgent) emitExternalFailureMailbox(worker *externalWorker, reason string) {
	if a == nil || worker == nil {
		return
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "external agent failed"
	}
	ownerAgentID, ownerTaskID, _, _ := worker.OwnerSnapshot()
	mailbox := &SubAgentMailboxMessage{
		AgentID:      worker.instanceID,
		TaskID:       worker.taskID,
		OwnerAgentID: ownerAgentID,
		OwnerTaskID:  ownerTaskID,
		Kind:         SubAgentMailboxKindRiskAlert,
		Subtype:      agentMessageSubtypeTaskFailure,
		Priority:     SubAgentMailboxPriorityInterrupt,
		Summary:      "External subagent failed: " + reason,
		Payload: fmt.Sprintf(
			"External subagent task terminated without completion.\n- task_id: %s\n- agent_id: %s\n- backend: %s\n- error: %s\n- required_action: inspect the failure and retry, reassign, or report the blocker; do not treat the task as completed.",
			worker.taskID, worker.instanceID, config.BackendKindClaudeCode, reason,
		),
		RequiresAck: false,
	}
	a.normalizeSubAgentMailboxMessage(mailbox)
	if err := a.prepareSubAgentMailboxMessage(mailbox); err != nil {
		log.Warnf("external failure mailbox durability degraded task_id=%v agent_id=%v error=%v", worker.taskID, worker.instanceID, err)
	} else if messageID := strings.TrimSpace(mailbox.MessageID); messageID != "" {
		a.markSubAgentMailboxSeen(messageID)
	}
	a.queueLoopEvent(Event{Type: EventSubAgentMailbox, SourceID: worker.instanceID, Payload: mailbox})
	a.emitToTUI(AgentNotifyEvent{
		AgentID:       worker.instanceID,
		TaskID:        worker.taskID,
		AgentType:     worker.agentDefName,
		ParentAgentID: controlPlaneAgentID(ownerAgentID),
		ParentTaskID:  ownerTaskID,
		TargetAgentID: controlPlaneAgentID(ownerAgentID),
		TargetTaskID:  ownerTaskID,
		Kind:          string(SubAgentMailboxKindRiskAlert),
		Message:       mailbox.Summary,
	})
}

func (w *externalWorker) enqueueExternalInput(text string, metadata *message.MailboxMetadata) (bool, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return false, fmt.Errorf("message is required")
	}
	w.promptMu.Lock()
	if w.closing.Load() {
		w.promptMu.Unlock()
		return false, fmt.Errorf("external worker is shutting down")
	}
	w.inputSeq++
	input := externalTaskInput{ID: fmt.Sprintf("%s:%d", w.instanceID, w.inputSeq), Content: text, State: message.ToolRecoveryStateNotStarted, Mailbox: metadata}
	deferred := w.inputClosed || w.finished.Load()
	if deferred {
		input.State = externalInputQueued
	}
	if err := w.persistExternalMessage(message.Message{Role: message.RoleUser, Content: text, InputID: input.ID, Mailbox: metadata}); err != nil {
		w.promptMu.Unlock()
		w.teardown()
		return false, err
	}
	w.inputs = append(w.inputs, input)
	if err := w.persistExternalSessionLocked(); err != nil {
		w.promptMu.Unlock()
		w.teardown()
		return false, err
	}
	w.promptQueue = append(w.promptQueue, input)
	w.promptMu.Unlock()
	return deferred, nil
}

func (w *externalWorker) currentSession() *externalclaude.Session {
	w.sessionMu.Lock()
	defer w.sessionMu.Unlock()
	return w.session
}
