package agent

import (
	"fmt"
	"strings"

	"github.com/keakon/chord/internal/executiongate"
	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/recovery"
)

func (a *MainAgent) installPausedSession(loaded *loadedSessionState) {
	gate := &executiongate.Gate{}
	a.stateMu.Lock()
	a.executionGate = gate
	a.stateMu.Unlock()
	a.pause.inputMu.Lock()
	a.pause.inputClosed = false
	a.pause.drafts = nil
	a.pause.inputMu.Unlock()
	a.pause.restored = loaded.Pause != nil && (loaded.Pause.Paused || len(loaded.Pause.PendingInputs) > 0)
	a.pause.initialSaved = false
	a.pause.capturing = false
	a.pause.saving = false
	a.pause.resumeSaving = false
	a.pause.resumeContinue = false
	a.pause.resumeMain = false
	a.pause.resumeFollowups = nil
	a.pause.finalSnapshot = nil
	a.pause.sealed.Store(false)
	a.pause.parkedInputs = make(map[string][]pendingUserMessage)
	a.stagedMainInputs = nil
	a.requestStarts.Store(0)
	a.recoverySnapshotMu.Lock()
	a.pause.checkpoint = loaded.Pause.Clone()
	a.recoverySnapshotMu.Unlock()
	a.pause.mu.Lock()
	a.pause.status = SessionPauseStatus{}
	a.pause.mu.Unlock()
	if !a.pause.restored {
		return
	}
	gate.Freeze()
	a.suspendPendingUserDrain()
	for _, input := range loaded.Pause.PendingInputs {
		p := restorePauseInput(input)
		// Draft IDs are UI-local. The restored identity uses its durable InputID
		// so the new composer's counter cannot collide with the old runtime.
		p.DraftID = "restored:" + input.InputID
		if input.OwnerTaskID == identity.MainAgentID {
			a.pendingUserMessages = enqueuePendingUserMessage(a.pendingUserMessages, p)
		} else {
			a.pause.parkedInputs[input.OwnerTaskID] = append(a.pause.parkedInputs[input.OwnerTaskID], p)
		}
	}
	a.pause.mu.Lock()
	a.pause.status = SessionPauseStatus{TransferIdentity: loaded.Pause.Identity, State: SessionPauseReady, StartedAt: loaded.Pause.FrozenAt, CheckpointID: loaded.Pause.CheckpointID, PendingInputCount: len(loaded.Pause.PendingInputs)}
	a.pause.mu.Unlock()
}

func (a *MainAgent) resumePausedSessionForUser() bool {
	if !a.pause.restored {
		a.emitToTUI(ToastEvent{Message: "Session is transferring; execution remains paused", Level: "warn"})
		return false
	}
	manager := a.recoveryManager()
	if manager == nil {
		a.emitToTUI(ToastEvent{Message: "Could not save the paused session", Level: "warn"})
		return false
	}
	if a.pause.resumeSaving {
		return false
	}
	a.pause.resumeSaving = true
	// Restored local control commands require a fresh explicit action. They
	// remain visible and cannot silently switch a session/worktree or deploy.
	var runnable, controls []pendingUserMessage
	for _, p := range a.pendingUserMessages {
		if strings.HasPrefix(strings.TrimSpace(p.Content), "/") {
			controls = append(controls, p)
		} else {
			runnable = append(runnable, p)
		}
	}
	a.pendingUserMessages = runnable
	if len(controls) > 0 {
		a.pause.parkedInputs[identity.MainAgentID] = append(a.pause.parkedInputs[identity.MainAgentID], controls...)
		a.emitToTUI(ToastEvent{Message: "Queued commands remain paused; run them explicitly to continue", Level: "info"})
	}
	a.recoverySnapshotMu.Lock()
	checkpoint := a.pause.checkpoint.Clone()
	a.recoverySnapshotMu.Unlock()
	checkpoint.Paused = false
	// Capture the owner queue before disk I/O, including the explicit new
	// request. Additional accepted messages stay parked until this ACK returns.
	checkpoint.PendingInputs = a.captureMainPauseInputs(checkpoint.Identity, checkpoint.FrozenAt).PendingInputs
	snapshot := a.buildRecoverySnapshot()
	snapshot.Pause = checkpoint.Clone()
	a.outputWg.Go(func() {
		inputs, err := manager.PersistPendingInputs(checkpoint.PendingInputs)
		if err == nil {
			checkpoint.PendingInputs = inputs
			snapshot.Pause = checkpoint.Clone()
			a.recoverySnapshotMu.Lock()
			err = manager.SaveSnapshot(snapshot)
			if err == nil {
				a.pause.checkpoint = checkpoint.Clone()
			}
			a.recoverySnapshotMu.Unlock()
		}
		a.sendEvent(Event{Type: eventSessionPauseResumeReady, Payload: sessionPauseResult{checkpoint: checkpoint, err: err}})
	})
	return false
}

func (a *MainAgent) finishPausedSessionResume(result sessionPauseResult) {
	a.pause.resumeSaving = false
	err := result.err
	if err == nil {
		current := a.captureMainPauseInputs(result.checkpoint.Identity, result.checkpoint.FrozenAt)
		if !samePauseInputRevisions(current.PendingInputs, result.checkpoint.PendingInputs) {
			a.resumePausedSessionForUser()
			return
		}
	}
	if err == nil {
		a.adoptPauseInputAttachments(result.checkpoint.PendingInputs)
		err = a.sessionExecutionGate().Resume()
	}
	if err != nil {
		a.finishPausedFollowups(err)
		a.emitToTUI(ToastEvent{Message: fmt.Sprintf("Could not resume the paused session: %v", err), Level: "warn"})
		return
	}
	a.pause.restored = false
	a.setSessionPauseStatus(func(s *SessionPauseStatus) { s.State = "running" })
	a.finishPausedFollowups(nil)
	if a.pause.resumeContinue {
		a.pause.resumeContinue = false
		a.pause.resumeMain = false
		a.handleContinueFromContext()
		return
	}
	if !a.pause.resumeMain {
		return
	}
	a.pause.resumeMain = false
	a.resumePendingUserDrain()
	a.drainPendingUserMessages()
}

func samePauseInputRevisions(a, b []recovery.PendingInput) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].InputID != b[i].InputID || a[i].OwnerTaskID != b[i].OwnerTaskID || a[i].Revision != b[i].Revision || a[i].Stage != b[i].Stage || a[i].Content != b[i].Content {
			return false
		}
	}
	return true
}

func (a *MainAgent) adoptPauseInputAttachments(inputs []recovery.PendingInput) {
	durable := make(map[string]recovery.PendingInput, len(inputs))
	for _, p := range inputs {
		durable[p.InputID] = p
	}
	adopt := func(queue []pendingUserMessage) {
		for i := range queue {
			if p, ok := durable[queue[i].InputID]; ok && p.Revision == queue[i].Revision {
				queue[i].Parts = cloneContentParts(p.Parts)
			}
		}
	}
	adopt(a.pendingUserMessages)
	for _, queue := range a.pause.parkedInputs {
		adopt(queue)
	}
}
