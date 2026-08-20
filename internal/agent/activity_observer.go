// Activity observer support for MainAgent.
// This allows external components (like power manager) to subscribe to
// activity changes without competing on the outputCh.

package agent

import (
	"github.com/keakon/chord/internal/identity"
)

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
	if activity != ActivityIdle {
		a.globalIdle.Store(false)
	}
	evt := AgentActivityEvent{
		AgentID: agentID,
		Type:    activity,
		Detail:  detail,
	}
	a.emitToTUI(evt)

	notifyObserver := func() {
		a.activityObserverMu.RLock()
		obs := a.activityObserver
		a.activityObserverMu.RUnlock()
		if obs != nil {
			obs.OnAgentActivity(agentID, activity)
		}
	}

	// Track whether the shared main slot shows a live foreground state so
	// compaction emissions can avoid clobbering it (see emitCompactionSlotActivity).
	if agentID == identity.MainAgentID || agentID == "" {
		switch activity {
		case ActivityIdle:
			a.mainSlotForeground.Store(false)
			// Foreground work ended while background compaction is still
			// running: hand the slot back to compaction immediately instead of
			// flashing idle until the next keep-alive heartbeat. Compaction
			// completion paths clear the running state before emitting Idle,
			// so terminal idles stay idle.
			if a.compactionSlotActive.Load() {
				notifyObserver()
				a.emitActivity(agentID, ActivityCompacting, compactionActivityDetail)
				return
			}
		case ActivityCompacting:
			// Compaction never claims the slot from a foreground state; see
			// emitCompactionSlotActivity.
		default:
			a.mainSlotForeground.Store(true)
		}
	}

	// Notify observer if registered (non-blocking).
	notifyObserver()
}

// emitCompactionSlotActivity emits the compacting activity only while no live
// foreground request/tool state owns the shared main activity slot. Compaction
// start, ready-waiting, and keep-alive heartbeats all route through here so a
// parallel main-model request keeps its visible progress instead of being
// overwritten mid-stream.
func (a *MainAgent) emitCompactionSlotActivity() {
	if !a.compactionSlotActive.Load() || a.mainSlotForeground.Load() {
		return
	}
	a.emitActivity(identity.MainAgentID, ActivityCompacting, compactionActivityDetail)
}
