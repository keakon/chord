package agent

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
