package agent

// Keep the old recovery manager and runtime slots installed until every
// external process has exited. Navigation is retried on the event loop.
func (a *MainAgent) deferExternalSessionSwitch(resume func()) bool {
	var active []*externalWorker
	for _, rt := range a.subs.snapshotRuntimes() {
		w, ok := rt.(*externalWorker)
		if !ok || !w.started.Load() {
			continue
		}
		select {
		case <-w.done:
			w.promptMu.Lock()
			ended := w.processEnded
			w.promptMu.Unlock()
			if !ended {
				a.emitToTUI(ToastEvent{Message: "External process termination is not proven; session navigation is blocked", Level: "warn"})
				return true
			}
		default:
			w.teardown()
			active = append(active, w)
		}
	}
	if len(active) == 0 {
		return false
	}
	epoch := a.recoverySessionEpoch()
	a.admissionPaused.Store(true)
	go func() {
		for _, w := range active {
			<-w.done
		}
		a.sendEvent(Event{Type: eventExternalNavigationReady, Payload: externalNavigationReady{epoch, resume}})
	}()
	return true
}

type externalNavigationReady struct {
	epoch  uint64
	resume func()
}
