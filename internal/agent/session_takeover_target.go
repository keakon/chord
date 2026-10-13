package agent

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/keakon/chord/internal/executiongate"
	"github.com/keakon/chord/internal/recovery"
)

const eventSessionTakeoverTarget = "session_takeover_target"

type sessionTakeoverTarget struct {
	ctx         context.Context
	sessionID   string
	identity    recovery.TransferIdentity
	reply       chan error
	inputGate   *executiongate.Gate
	inputCutoff bool
}

// ResumePausedSession switches only after the target lock, checkpoint and
// ordinary restore have been validated. Failure leaves the current session.
func (a *MainAgent) ResumePausedSession(ctx context.Context, sid string, id recovery.TransferIdentity) error {
	request := sessionTakeoverTarget{ctx: ctx, sessionID: sid, identity: id, reply: make(chan error, 1)}
	// Input producers enqueue under inputMu. Closing admission here places
	// every previously accepted input before the switch event, while later
	// inputs receive an explicit rejection in their original session.
	a.pause.inputMu.Lock()
	if a.pause.inputClosed {
		a.pause.inputMu.Unlock()
		return pauseError("target_not_ready", "current session input is already closed")
	}
	request.inputGate = a.sessionExecutionGate()
	request.inputCutoff = true
	a.pause.inputClosed = true
	a.pause.inputMu.Unlock()
	if !a.sendEventUntil(Event{Type: eventSessionTakeoverTarget, Payload: request}, ctx.Done()) {
		a.releaseTakeoverInputAdmission(request)
		if err := ctx.Err(); err != nil {
			return err
		}
		return ErrAgentShutdown
	}
	select {
	case err := <-request.reply:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (a *MainAgent) handleTakeoverTarget(req sessionTakeoverTarget) {
	reply := func(err error) {
		a.releaseTakeoverInputAdmission(req)
		req.reply <- err
	}
	if req.inputCutoff && req.inputGate != a.sessionExecutionGate() {
		reply(pauseError("target_not_ready", "current session changed before target activation"))
		return
	}
	if err := req.ctx.Err(); err != nil {
		reply(err)
		return
	}
	finish, err := a.beginSessionStructural(a.parentCtx, false)
	if err != nil {
		reply(err)
		return
	}
	defer finish()
	if a.currentTurn() != nil || len(a.pendingUserMessages) > 0 || len(a.pause.parkedInputs) > 0 || a.hasPauseBlockingInteraction() || a.compactionState.running || a.mainLLMRequestInFlight.Load() || a.hasQueuedAutomaticWork() || a.loopKeepsMainBusy() || a.takeoverTargetHasActiveWork() {
		reply(pauseError("target_not_ready", "target session has active work or queued input"))
		return
	}
	if filepath.Base(req.sessionID) != req.sessionID || req.identity.SessionID != req.sessionID {
		reply(pauseError("invalid_request", "target session identity is invalid"))
		return
	}
	path, err := a.resolveResumeSessionPath(req.sessionID)
	if err != nil {
		reply(err)
		return
	}
	lock, err := recovery.AcquireSessionLock(path)
	if err != nil {
		reply(fmt.Errorf("lock_busy: %w", err))
		return
	}
	loaded, err := a.loadSessionState(path)
	if err == nil && (loaded.Pause == nil || !loaded.Pause.Paused || !loaded.Pause.CheckpointComplete || loaded.Pause.Identity != req.identity) {
		err = pauseError("restore_failed", "complete paused checkpoint does not match transfer")
	}
	if err == nil && req.identity.Workspace != a.ContentRoot() {
		canonical, canonicalErr := filepath.EvalSymlinks(a.ContentRoot())
		if canonicalErr != nil || req.identity.Workspace != canonical {
			err = pauseError("wrong_workspace", "target workspace does not match transfer")
		}
	}
	if err != nil {
		_ = lock.Release()
		reply(err)
		return
	}
	if err = a.freezeTakeoverCurrentSession(finish); err != nil {
		_ = lock.Release()
		reply(err)
		return
	}
	oldLock := a.sessionLock
	if oldLock != nil {
		if err = oldLock.Release(); err != nil {
			_ = lock.Release()
			reply(err)
			return
		}
	}
	a.emitToTUI(SessionSwitchStartedEvent{Kind: "resume", SessionID: req.sessionID})
	// No old session writer remains. The ordinary state reset is now safe;
	// active work was rejected and quiescent workers were durably parked.
	a.prepareSessionSwitch()
	a.sessionLock = lock
	a.activateLoadedSession(loaded)
	if notice := a.adoptResumedSessionCheckout(a.parentCtx, loaded.RecordedWorkDir); notice != "" {
		a.emitToTUI(ToastEvent{Message: notice, Level: "warning"})
	}
	a.llmClient.SetSessionID(req.sessionID)
	// Publish restoration only after the new binding and input admission are
	// ready, so event consumers and the caller observe the completed switch.
	a.finishSessionSwitch()
	a.emitToTUI(SessionRestoredEvent{})
	a.publishRestoredQuestions()
	a.setIdleForComposerEdit()
	reply(nil)
}

func (a *MainAgent) releaseTakeoverInputAdmission(req sessionTakeoverTarget) {
	if !req.inputCutoff || req.inputGate != a.sessionExecutionGate() || req.inputGate.Snapshot().Paused {
		return
	}
	a.pause.inputMu.Lock()
	defer a.pause.inputMu.Unlock()
	a.pause.mu.Lock()
	defer a.pause.mu.Unlock()
	switch a.pause.status.State {
	case recovery.SessionPausePreparing, recovery.SessionPauseDraining, recovery.SessionPauseSaving, recovery.SessionPauseSaved, recovery.SessionPauseReleased, recovery.SessionPauseSaveFailed:
		return
	}
	a.pause.inputClosed = false
}
