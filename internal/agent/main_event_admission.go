package agent

// trySendEventLocked never waits for capacity. The caller holds eventMu.
func (a *MainAgent) trySendEventLocked(evt Event) bool {
	if len(a.deferredEvents) == 0 && len(a.loopEvents) == 0 {
		if len(a.eventCh) < cap(a.eventCh) {
			evt = a.sequenceEvent(evt)
			a.eventCh <- evt
			return true
		}
	}
	if a.coalesceQueuedEventLocked(a.deferredEvents, evt) {
		a.eventCoalesced.Add(1)
		return true
	}
	if len(a.deferredEvents) < a.eventOverflowLimit {
		evt = a.sequenceEvent(evt)
		a.deferredEvents = append(a.deferredEvents, evt)
		a.updateEventOverflowPeakLocked()
		a.wakeDeferredEvents()
		return true
	}
	return false
}
