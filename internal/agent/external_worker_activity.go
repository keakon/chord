package agent

// emitExternalActivity publishes the worker's real loop phase to the TUI.
//
// The external process reports no activity of its own, so without these
// transitions the TUI can only show an optimistic "connecting" state anchored
// on the sent draft. A same-phase repeat with the same detail is dropped: the
// status bar anchors its elapsed timer on transitions, and per-delta or
// per-line repeats would restart it.
func (w *externalWorker) emitExternalActivity(activity ActivityType, detail string) {
	if w == nil || w.parent == nil {
		return
	}
	w.activityMu.Lock()
	if activity == w.lastActivity && detail == w.lastActivityDetail {
		w.activityMu.Unlock()
		return
	}
	w.lastActivity = activity
	w.lastActivityDetail = detail
	w.activityMu.Unlock()
	w.parent.emitActivity(w.instanceID, activity, detail)
}

// runningExternalToolsLocked counts authorized tools whose result has not
// arrived. Callers hold externalToolStateMu.
func (w *externalWorker) runningExternalToolsLocked() int {
	w.toolMu.Lock()
	defer w.toolMu.Unlock()
	running := 0
	for id := range w.toolNames {
		binding := w.toolBindings[id]
		if binding.started && !binding.settled {
			running++
		}
	}
	return running
}
