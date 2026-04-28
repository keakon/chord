// Activity observer support for MainAgent.
// This allows external components (like power manager) to subscribe to
// activity changes without competing on the outputCh.

package agent

// ActivityObserver receives notifications when agent activity changes.
// The observer is called synchronously from the event emission path,
// so implementations should be non-blocking or spawn their own goroutines.
type ActivityObserver interface {
	// OnAgentActivity is called when an agent's activity type changes.
	// agentID is "main" for the main agent, or the instance ID for subagents.
	OnAgentActivity(agentID string, activity ActivityType)
}

// SetActivityObserver registers an observer for activity events.
// Only one observer can be registered at a time; setting a new one
// replaces the previous. Pass nil to remove the observer.
func (a *MainAgent) SetActivityObserver(obs ActivityObserver) {
	a.activityObserverMu.Lock()
	defer a.activityObserverMu.Unlock()
	a.activityObserver = obs
}

// emitActivity sends an AgentActivityEvent to the TUI and notifies
// the activity observer if one is registered.
func (a *MainAgent) emitActivity(agentID string, activity ActivityType, detail string) {
	evt := AgentActivityEvent{
		AgentID: agentID,
		Type:    activity,
		Detail:  detail,
	}
	a.emitToTUI(evt)

	// Notify observer if registered (non-blocking).
	a.activityObserverMu.RLock()
	obs := a.activityObserver
	a.activityObserverMu.RUnlock()
	if obs != nil {
		obs.OnAgentActivity(agentID, activity)
	}
}
