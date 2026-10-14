package agent

// IsGlobalIdle reports the live global-quiescence state: true only while the
// event loop has no active turn, loop, subagent work, or runnable queued work.
// Callers that wait for quiescence must consult this live state rather than
// treating a read GlobalIdleEvent as current, because the output channel can
// still hold an idle event emitted for an earlier quiescence (for example
// startup) when work has since resumed.
func (a *MainAgent) IsGlobalIdle() bool {
	return a != nil && a.globalIdle.Load()
}

// markRealWorkStarted records the beginning of work that can later produce a
// completion notification. It is safe to call from a SubAgent goroutine; the
// event loop consumes the monotonically increasing epoch when it emits the
// next GlobalIdleEvent.
func (a *MainAgent) markRealWorkStarted() {
	if a == nil {
		return
	}
	a.realWorkEpoch.Add(1)
	a.globalIdle.Store(false)
}

// controlActionCanSetBaseline reports whether a user control operation may
// establish the silent notification baseline. Real agent work — an active
// turn, loop execution, or a running subagent — must not be swallowed by the
// baseline, or that work's completion notification would be suppressed as if
// it were configuration-only quiescence.
func (a *MainAgent) controlActionCanSetBaseline() bool {
	if a == nil {
		return false
	}
	return a.currentTurn() == nil && !a.loopKeepsMainBusy() && !a.hasActiveSubAgentWork()
}

// markControlAction establishes a silent quiescence baseline for a user
// operation that does not start a real agent turn. The caller must be the
// MainAgent event-loop goroutine because lastIdleWorkEpoch is event-loop-only.
func (a *MainAgent) markControlAction() {
	if a == nil {
		return
	}
	a.lastIdleWorkEpoch = a.realWorkEpoch.Load()
	a.globalIdle.Store(false)
}
