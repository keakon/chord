package agent

import "fmt"

// liveExternalWorkers returns every external worker still registered as a live
// runtime: starting, running, or waiting for its terminal settlement. A worker
// leaves this list only through the close path that persists its task record,
// so any entry here still holds a transcript writer or an unsettled task.
func (a *MainAgent) liveExternalWorkers() []*externalWorker {
	var out []*externalWorker
	for _, rt := range a.subs.snapshotRuntimes() {
		if worker, ok := rt.(*externalWorker); ok {
			out = append(out, worker)
		}
	}
	return out
}

// suspendExternalWorkersForSwitch stops every started external worker before a
// session switch so its task settles while the original session, recovery
// manager, and runtime slot are still installed. Removing those first drops the
// deferred close event, which is why the switch has to wait instead.
//
// The first result reports that the caller must return without switching.
// retry runs on the event loop once every worker exited; superseded runs there
// instead when another switch replaced the session first, so a caller waiting
// for a reply is not left hanging. err reports a worker whose process
// termination could not be proven: switching then would abandon a checkout that
// may still have a live writer.
//
// A deferred switch leaves admission paused; the retried flow clears it through
// its own switch completion, or through releaseExternalSwitchAdmission when the
// retry ends without switching.
func (a *MainAgent) suspendExternalWorkersForSwitch(retry, superseded func()) (bool, error) {
	started, err := a.pauseAdmissionAndSnapshotExternalWorkers()
	if err != nil {
		return true, err
	}
	if len(started) == 0 {
		return false, nil
	}
	epoch := a.recoverySessionEpoch()
	for _, worker := range started {
		select {
		case <-worker.done:
		default:
			worker.teardown()
		}
	}
	go func() {
		for _, worker := range started {
			<-worker.done
		}
		a.sendEvent(Event{Type: eventExternalNavigationReady, Payload: externalNavigationReady{epoch: epoch, resume: retry, superseded: superseded}})
	}()
	return true, nil
}

// pauseAdmissionAndSnapshotExternalWorkers pauses admission and returns the
// started external workers under the same admissionMu that CreateSubAgent holds
// through its registration and startRuntime, so a delegation cannot register a
// running worker between the snapshot and the pause. A worker that completes
// during the stop sequence must not rehydrate a queued input into a session
// that is about to be replaced, which is why admission pauses before teardown.
//
// An unproven worker leaves admission untouched: the caller refuses the switch
// instead of stopping anything.
func (a *MainAgent) pauseAdmissionAndSnapshotExternalWorkers() ([]*externalWorker, error) {
	a.admissionMu.Lock()
	defer a.admissionMu.Unlock()
	var started []*externalWorker
	for _, worker := range a.liveExternalWorkers() {
		if !worker.started.Load() {
			// No process exists yet; the close path settles it synchronously.
			continue
		}
		select {
		case <-worker.done:
			worker.promptMu.Lock()
			ended := worker.processEnded
			worker.promptMu.Unlock()
			if !ended {
				return nil, fmt.Errorf("external process termination is not proven for task %s; the session cannot switch until cleanup succeeds", worker.taskID)
			}
		default:
		}
		started = append(started, worker)
	}
	if len(started) > 0 {
		a.admissionPaused.Store(true)
	}
	return started, nil
}

// releaseExternalSwitchAdmission clears exactly the admission pause taken while
// external workers stopped, for a retry that ends without a session switch.
// Switching retries clear it through their own finishSessionSwitch; doing a
// full switch-finish here would also reset diagnostics and input delivery on a
// session that never switched.
func (a *MainAgent) releaseExternalSwitchAdmission() {
	a.admissionPaused.Store(false)
}

// notifyExternalSwitchSuperseded tells the user that another session switch
// won while live external agents were being stopped, so the command that
// asked for the switch was not run.
func (a *MainAgent) notifyExternalSwitchSuperseded() {
	a.emitToTUI(ToastEvent{Message: "Session changed while external agents were stopping; run the command again", Level: "warn"})
}

// deferExternalSessionSwitch keeps the old recovery manager and runtime slots
// installed until every external process has exited and settled. Navigation is
// retried on the event loop; superseded runs there instead when another switch
// replaced the session first.
func (a *MainAgent) deferExternalSessionSwitch(resume, superseded func()) bool {
	deferred, err := a.suspendExternalWorkersForSwitch(resume, superseded)
	if !deferred {
		return false
	}
	if err != nil {
		a.emitToTUI(ToastEvent{Message: err.Error(), Level: "warn"})
	}
	return true
}

type externalNavigationReady struct {
	epoch      uint64
	resume     func()
	superseded func()
}
