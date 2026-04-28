package main

import (
	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/power"
)

// activityObserverAdapter bridges agent.ActivityObserver to power.Manager.
type activityObserverAdapter struct {
	mgr *power.Manager
}

func (a *activityObserverAdapter) OnAgentActivity(agentID string, activity agent.ActivityType) {
	a.mgr.UpdateActivity(agentID, power.ActivityType(activity))
}
