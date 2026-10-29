package agent

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/keakon/chord/internal/recovery"
	"github.com/keakon/chord/internal/tools"
)

const (
	eventSessionPause                 = "session_pause"
	eventSessionPausePrepared         = "session_pause_prepared"
	eventSessionPauseCheckpoint       = "session_pause_checkpoint"
	eventSessionPauseCaptured         = "session_pause_captured"
	eventSessionPauseSaved            = "session_pause_saved"
	eventSessionLLMPaused             = "session_llm_paused"
	eventSessionPauseWake             = "session_pause_wake"
	eventSessionPauseRetry            = "session_pause_retry"
	eventSessionPauseRetried          = "session_pause_retried"
	eventSessionInputDispatched       = "session_input_dispatched"
	eventSessionPauseResumeReady      = "session_pause_resume_ready"
	eventSessionOwnerInputsDispatched = "session_owner_inputs_dispatched"
)

type SessionPauseRequest struct{ Identity recovery.TransferIdentity }

func pauseError(code, detail string) error {
	return &recovery.SessionPauseError{Code: code, Cause: errors.New(detail)}
}

type SessionTransferEvent struct {
	Status recovery.SessionPauseStatus `json:"status"`
}

func (SessionTransferEvent) agentEvent() {}

type sessionPauseRuntime struct {
	inputMu         sync.Mutex
	inputClosed     bool
	inputPrefix     string
	mu              sync.Mutex
	status          recovery.SessionPauseStatus
	preparer        func(context.Context, SessionPauseRequest) error
	checkpoint      *recovery.SessionPauseCheckpoint // guarded by recoverySnapshotMu
	finalSnapshot   *recovery.SessionSnapshot        // owned by loop; reused for save-only retries
	restored        bool                             // owned by loop
	resumeSaving    bool
	resumeContinue  bool
	resumeMain      bool
	resumeFollowups []*SubAgentSendMessagePayload
	parkedInputs    map[string][]pendingUserMessage // owned by loop, durable task keyed
	initialSaved    bool                            // owned by loop
	capturing       bool                            // owned by loop
	saving          bool                            // owned by loop
	retry           *sessionPauseCommand            // owner-loop retry admission and completion identity
	sealed          atomic.Bool
}

type sessionPauseCommand struct {
	req      SessionPauseRequest
	result   chan error
	admitted bool // written by the owner loop before replying through result
}
type sessionPauseResult struct {
	command    *sessionPauseCommand
	checkpoint *recovery.SessionPauseCheckpoint
	err        error
}

func (a *MainAgent) SetSessionPausePreparer(fn func(context.Context, SessionPauseRequest) error) {
	a.pause.mu.Lock()
	a.pause.preparer = fn
	a.pause.mu.Unlock()
}

// BeginSessionPause admits a transfer independently of the caller's observation
// lifetime. Its accepted work remains owned by the source session.
func (a *MainAgent) BeginSessionPause(ctx context.Context, req SessionPauseRequest) (recovery.SessionPauseStatus, error) {
	if req.Identity.TransferID == "" || req.Identity.SessionID != filepath.Base(a.SessionDir()) {
		return recovery.SessionPauseStatus{}, pauseError("invalid_request", "transfer and current session identity are required")
	}
	cmd := &sessionPauseCommand{req: req, result: make(chan error, 1)}
	if !a.sendEventUntil(Event{Type: eventSessionPause, Payload: cmd}, ctx.Done()) {
		if err := ctx.Err(); err != nil {
			return recovery.SessionPauseStatus{}, err
		}
		return recovery.SessionPauseStatus{}, ErrAgentShutdown
	}
	select {
	case err := <-cmd.result:
		if !cmd.admitted {
			return recovery.SessionPauseStatus{TransferIdentity: req.Identity, State: recovery.SessionPausePreflight}, err
		}
		status, _ := a.SessionPauseStatus(req.Identity.TransferID)
		return status, err
	case <-ctx.Done():
		status, _ := a.SessionPauseStatus(req.Identity.TransferID)
		return status, ctx.Err()
	}
}

func (a *MainAgent) SessionPauseStatus(transferID string) (recovery.SessionPauseStatus, error) {
	a.pause.mu.Lock()
	status := a.pause.status
	status.Blockers = append([]recovery.SessionPauseBlocker(nil), status.Blockers...)
	a.pause.mu.Unlock()
	if status.TransferID == "" || status.TransferID != transferID {
		return recovery.SessionPauseStatus{}, pauseError("status_unknown", "transfer identity is unknown")
	}
	return status, nil
}

func (a *MainAgent) setSessionPauseStatus(update func(*recovery.SessionPauseStatus)) {
	a.pause.mu.Lock()
	update(&a.pause.status)
	status := a.pause.status
	status.Blockers = append([]recovery.SessionPauseBlocker(nil), status.Blockers...)
	a.pause.mu.Unlock()
	a.emitReliableToTUI(SessionTransferEvent{Status: status}, "session transfer status delivery")
}

func (a *MainAgent) dispatchSessionPause(evt Event) bool {
	switch evt.Type {
	case eventSessionTakeoverTarget:
		a.handleTakeoverTarget(evt.Payload.(sessionTakeoverTarget))
		return true
	case eventSessionPauseResumeReady:
		a.finishPausedSessionResume(evt.Payload.(sessionPauseResult))
		return true
	case eventSessionPause:
		a.prepareSessionPause(evt.Payload.(*sessionPauseCommand))
		return true
	case eventSessionPausePrepared:
		result := evt.Payload.(sessionPauseResult)
		if result.err != nil {
			a.pause.inputMu.Lock()
			a.pause.inputClosed = false
			a.pause.inputMu.Unlock()
			a.setSessionPauseStatus(func(s *recovery.SessionPauseStatus) {
				s.ErrorCode = "target_not_ready"
				s.Error = result.err.Error()
				s.State = recovery.SessionPausePrepareFailed
			})
			result.command.result <- pauseError("target_not_ready", result.err.Error())
			return true
		}
		a.freezeSessionPause(result.command)
		return true
	case eventSessionPauseCheckpoint:
		result := evt.Payload.(sessionPauseResult)
		a.pause.capturing = false
		if result.err != nil {
			a.failSessionPauseSave(result.err)
		} else {
			a.pause.initialSaved = true
			a.recoverySnapshotMu.Lock()
			a.pause.checkpoint = result.checkpoint
			a.recoverySnapshotMu.Unlock()
			a.setSessionPauseStatus(func(s *recovery.SessionPauseStatus) {
				s.PendingInputCount = len(result.checkpoint.PendingInputs) + a.managedDeliveryInputCount()
				s.CheckpointID = result.checkpoint.CheckpointID
			})
		}
		result.command.result <- result.err
		return true
	case eventSessionPauseCaptured:
		result := evt.Payload.(sessionPauseResult)
		a.pause.capturing = false
		if result.err != nil {
			a.failSessionPauseSave(result.err)
			return true
		}
		a.saveFinalSessionPause(result.checkpoint)
		return true
	case eventSessionPauseSaved:
		result := evt.Payload.(sessionPauseResult)
		a.pause.saving = false
		if a.pause.finalSnapshot != nil && result.checkpoint != nil {
			// Keep session-owned attachment references produced by a failed
			// snapshot write available for retry, after the worker has stopped.
			a.pause.finalSnapshot.Pause = result.checkpoint.Clone()
		}
		if result.err != nil {
			a.failSessionPauseSave(result.err)
			return true
		}
		a.pause.sealed.Store(true)
		a.setSessionPauseStatus(func(s *recovery.SessionPauseStatus) {
			s.State = recovery.SessionPauseSaved
			s.Blockers = nil
			s.BlockerCount = 0
			s.Error = ""
			s.ErrorCode = ""
			s.RecoverableAction = ""
		})
		return true
	case eventSessionLLMPaused:
		a.mainLLMRequestInFlight.Store(false)
		if a.turn != nil {
			a.savePartialAssistantMsgForTurn(a.turn)
		}
		return true
	case eventSessionPauseWake:
		return true
	case eventSessionInputDispatched:
		a.retireDispatchedSessionInputs(evt.Payload.(uint64))
		return true
	case eventSessionOwnerInputsDispatched:
		a.retireOwnerSessionInputs(evt.Payload.(sessionOwnerInputsDispatched))
		return true
	case eventSessionPauseRetry:
		cmd := evt.Payload.(*sessionPauseCommand)
		if a.pause.retry != nil {
			cmd.result <- pauseError("transfer_busy", "session save retry is already running")
			return true
		}
		status, err := a.SessionPauseStatus(cmd.req.Identity.TransferID)
		if err != nil || status.State != recovery.SessionPauseSaveFailed {
			cmd.result <- pauseError("invalid_request", "transfer has no failed save")
			return true
		}
		a.pause.retry = cmd
		go func() {
			err := a.retrySessionPausePersistence()
			a.sendEvent(Event{Type: eventSessionPauseRetried, Payload: sessionPauseResult{command: cmd, err: err}})
		}()
		return true
	case eventSessionPauseRetried:
		result := evt.Payload.(sessionPauseResult)
		if result.command != a.pause.retry || result.command == nil {
			return true
		}
		a.pause.retry = nil
		if result.err != nil {
			a.failSessionPauseSave(result.err)
			result.command.result <- result.err
			return true
		}
		if a.pause.finalSnapshot != nil {
			a.saveFinalSessionPause(a.pause.finalSnapshot.Pause.Clone())
			result.command.result <- nil
		} else {
			a.freezeSessionPause(result.command)
		}
		return true
	}
	a.pause.mu.Lock()
	preparing := a.pause.status.State == recovery.SessionPausePreparing
	a.pause.mu.Unlock()
	if preparing || a.sessionExecutionPaused() && !a.pause.restored {
		switch evt.Type {
		case EventSessionControl, EventMCPControl, EventModelPoolSwitch, EventExecutePlan:
			a.emitToTUI(ToastEvent{Message: "Session is transferring; finish the transfer before changing its configuration", Level: "warn"})
			return true
		}
	}
	if !a.sessionExecutionPaused() {
		return false
	}
	if a.pause.restored {
		return false
	}
	switch evt.Type {
	case EventContinue, EventLoopAssessment, EventExecutePlan, EventSubAgentLifecycleSweep:
		a.emitToTUI(ToastEvent{Message: "Session is transferring; new execution is paused", Level: "warn"})
		return true
	}
	return false
}

func (a *MainAgent) prepareSessionPause(cmd *sessionPauseCommand) {
	if cmd.req.Identity.SessionID != filepath.Base(a.SessionDir()) || cmd.req.Identity.SourceSessionEpoch != a.SessionEpoch() {
		cmd.result <- pauseError("stale_source", "source session changed before transfer admission")
		return
	}
	a.pause.mu.Lock()
	busy := a.pause.status.TransferID != "" && a.pause.status.State != recovery.SessionPausePrepareFailed && a.pause.status.State != "running" && a.pause.status.State != recovery.SessionPauseReady
	a.pause.mu.Unlock()
	if busy {
		cmd.result <- pauseError("transfer_busy", "source already has a transfer")
		return
	}
	if a.hasPauseBlockingInteraction() || a.pendingHandoff != nil || a.admissionPaused.Load() || a.mcpTransitionActive.Load() {
		cmd.result <- pauseError("interaction_blocked", "finish the current interaction or session transition first")
		return
	}
	// An external worker keeps writing its own task record and transcript
	// through the session being sealed, and pause has no way to stop it
	// durably, so refuse instead of capturing a checkpoint over a live writer.
	if len(a.liveExternalWorkers()) > 0 {
		cmd.result <- pauseError("target_not_ready", "external agents cannot be paused; stop them before pausing this session")
		return
	}
	if a.recoveryManager() == nil || a.sessionLock == nil {
		cmd.result <- pauseError("persistence_failed", "session writer and ownership lock are required")
		return
	}
	cmd.admitted = true
	a.setSessionPauseStatus(func(s *recovery.SessionPauseStatus) {
		*s = recovery.SessionPauseStatus{TransferIdentity: cmd.req.Identity, State: recovery.SessionPausePreparing}
	})
	a.pause.mu.Lock()
	preparer := a.pause.preparer
	a.pause.mu.Unlock()
	go func() {
		var err error
		if preparer != nil {
			err = preparer(a.parentCtx, cmd.req)
		}
		a.pause.inputMu.Lock()
		if err == nil {
			a.pause.inputClosed = true
		}
		a.pause.inputMu.Unlock()
		a.sendEvent(Event{Type: eventSessionPausePrepared, Payload: sessionPauseResult{command: cmd, err: err}})
	}()
}

func (a *MainAgent) freezeSessionPause(cmd *sessionPauseCommand) {
	a.pause.restored = false
	state := a.sessionExecutionGate().Freeze()
	a.suspendPendingUserDrain()
	blockers := a.sessionPauseBlockers()
	a.setSessionPauseStatus(func(s *recovery.SessionPauseStatus) {
		s.State = recovery.SessionPauseDraining
		s.StartedAt = state.FrozenAt
		s.Blockers = blockers
		s.BlockerCount = len(s.Blockers)
	})
	a.pause.capturing = true
	a.delivery.cancelGeneration.Add(1)
	if a.delivery.committed != nil || a.delivery.inflight != nil || len(a.delivery.pending) > 0 {
		// The barrier serializes this capture behind every pending delivery
		// operation, so the paused checkpoint sees a settled queue.
		op := &deliveryOperation{barrier: true}
		op.finish = func() { a.captureInitialDeliveryPause(cmd, state.FrozenAt) }
		op.failed = func(err error) { a.pause.capturing = false; a.failSessionPauseSave(err); cmd.result <- err }
		a.delivery.pending = append(a.delivery.pending, op)
		a.startNextDeliveryOperation()
		return
	}
	a.captureInitialDeliveryPause(cmd, state.FrozenAt)
}

func (a *MainAgent) failSessionPauseSave(err error) {
	a.setSessionPauseStatus(func(s *recovery.SessionPauseStatus) {
		s.State = recovery.SessionPauseSaveFailed
		s.ErrorCode = "persistence_failed"
		s.Error = err.Error()
		s.RecoverableAction = recovery.SessionPauseActionRetrySave
		if _, closed := errors.AsType[*SessionPauseWriterClosedError](err); closed {
			s.RecoverableAction = recovery.SessionPauseActionManualRecovery
		}
		if write, unknown := errors.AsType[*recovery.PersistWriteError](err); unknown && !write.RetrySafe {
			s.RecoverableAction = recovery.SessionPauseActionManualRecovery
		}
	})
}

func (a *MainAgent) advanceSessionPause() {
	if !a.pause.initialSaved || a.pause.restored || a.pause.capturing || a.pause.saving || a.pause.sealed.Load() {
		return
	}
	status, _ := a.SessionPauseStatus(a.pauseTransferID())
	if status.State != recovery.SessionPauseDraining {
		return
	}
	blockers := a.sessionPauseBlockers()
	a.publishSessionPauseBlockers(blockers)
	if len(blockers) > 0 {
		return
	}
	a.pause.capturing = true
	checkpoint := a.captureMainPauseInputs(status.TransferIdentity, status.StartedAt)
	var speculative *StreamingToolExecutor
	if a.turn != nil {
		speculative = a.turn.streamingToolExec
	}
	go func() {
		err := speculative.WaitForSessionPause(a.parentCtx)
		if err == nil {
			err = a.drainSubPauseInputs(checkpoint)
		}
		a.outputWg.Wait()
		a.toolWg.Wait()
		a.compactionWg.Wait()
		a.sendEvent(Event{Type: eventSessionPauseCaptured, Payload: sessionPauseResult{checkpoint: checkpoint, err: err}})
	}()
}

func (a *MainAgent) pauseTransferID() string {
	a.pause.mu.Lock()
	defer a.pause.mu.Unlock()
	return a.pause.status.TransferID
}

func (a *MainAgent) sessionPauseBlockers() []recovery.SessionPauseBlocker {
	var out []recovery.SessionPauseBlocker
	if a.delivery.inflight != nil || len(a.delivery.pending) > 0 {
		out = append(out, recovery.SessionPauseBlocker{Kind: "input_queue", Description: "waiting for input queue persistence"})
	}
	for _, activity := range a.sessionExecutionGate().Snapshot().Activities {
		out = append(out, recovery.SessionPauseBlocker{OwnerTaskID: activity.Owner, Kind: string(activity.Kind), StartedAt: activity.StartedAt, Description: "execution is still running"})
	}
	if a.mainLLMRequestInFlight.Load() {
		out = append(out, recovery.SessionPauseBlocker{Kind: "llm_result", Description: "waiting for model result processing"})
	}
	if turn := a.currentTurn(); turn != nil && turn.PendingToolCalls.Load() > 0 {
		out = append(out, recovery.SessionPauseBlocker{Kind: "tool_result", Description: "waiting for tool results and hooks"})
	}
	if a.compactionState.running {
		out = append(out, recovery.SessionPauseBlocker{Kind: "compaction", Description: "waiting for context checkpoint processing"})
	}
	for _, job := range tools.SnapshotJobs() {
		if job.SessionDir != "" && job.SessionDir != a.SessionDir() {
			continue
		}
		if job.Active() || job.PendingWake() {
			out = append(out, recovery.SessionPauseBlocker{OwnerTaskID: job.AgentID, Kind: "job", StartedAt: job.StartedAt, Description: job.ID})
		}
	}
	a.memoryMu.Lock()
	memoryBusy := a.memoryInflight != nil
	a.memoryMu.Unlock()
	if memoryBusy {
		out = append(out, recovery.SessionPauseBlocker{Kind: "memory", Description: "waiting for memory result persistence"})
	}
	for _, worker := range a.liveExternalWorkers() {
		out = append(out, recovery.SessionPauseBlocker{OwnerTaskID: worker.taskID, Kind: "external_agent", StartedAt: worker.StateChangedAt(), Description: "stop the external agent before pausing this session"})
	}
	sortSessionPauseBlockers(out)
	return out
}

func (a *MainAgent) RetrySessionPauseSave(ctx context.Context, transferID string) error {
	status, err := a.SessionPauseStatus(transferID)
	if err != nil {
		return err
	}
	if status.State != recovery.SessionPauseSaveFailed {
		return pauseError("invalid_request", "transfer has no failed save")
	}
	cmd := &sessionPauseCommand{req: SessionPauseRequest{Identity: status.TransferIdentity}, result: make(chan error, 1)}
	if !a.sendEventUntil(Event{Type: eventSessionPauseRetry, Payload: cmd}, ctx.Done()) {
		if err := ctx.Err(); err != nil {
			return err
		}
		return ErrAgentShutdown
	}
	select {
	case err := <-cmd.result:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// ReleasePausedSession releases ownership only after all writers closed. It
// does not write the checkpoint after releasing the lock.
func (a *MainAgent) ReleasePausedSession(transferID string) error {
	status, err := a.SessionPauseStatus(transferID)
	if err != nil {
		return err
	}
	if status.State == recovery.SessionPauseReleased {
		return nil
	}
	if !a.pause.sealed.Load() || status.State != recovery.SessionPauseSaved {
		return pauseError("invalid_request", "session is not sealed")
	}
	if a.started.Load() {
		<-a.done
	}
	if err := a.sessionLock.Release(); err != nil {
		return fmt.Errorf("release paused session lock: %w", err)
	}
	a.pause.mu.Lock()
	a.pause.status.State = recovery.SessionPauseReleased
	a.pause.mu.Unlock()
	return nil
}

func (a *MainAgent) hasPauseBlockingInteraction() bool {
	// Question waits belong to this owner loop, including waits issued by
	// workers. Async questions without a waiter remain restorable.
	if len(a.questions.waits) > 0 || a.questions.waiting {
		return true
	}
	if a.interaction == nil {
		return false
	}
	a.interaction.confirmMapMu.Lock()
	confirm := len(a.interaction.confirmStart) > 0
	a.interaction.confirmMapMu.Unlock()
	a.interaction.handoffMapMu.Lock()
	handoff := len(a.interaction.handoffStart) > 0
	a.interaction.handoffMapMu.Unlock()
	return confirm || handoff
}

func (a *MainAgent) captureInitialDeliveryPause(cmd *sessionPauseCommand, frozenAt time.Time) {
	checkpoint := a.captureMainPauseInputs(cmd.req.Identity, frozenAt)
	snapshot := a.buildRecoverySnapshot()
	go a.saveInitialSessionPause(cmd, checkpoint, snapshot)
}
