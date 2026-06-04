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

type multiActivityObserver struct {
	observers []agent.ActivityObserver
}

func (m *multiActivityObserver) OnAgentActivity(agentID string, activity agent.ActivityType) {
	if m == nil {
		return
	}
	for _, obs := range m.observers {
		if obs == nil {
			continue
		}
		obs.OnAgentActivity(agentID, activity)
	}
}

func combineActivityObservers(observers ...agent.ActivityObserver) agent.ActivityObserver {
	filtered := make([]agent.ActivityObserver, 0, len(observers))
	for _, obs := range observers {
		if obs != nil {
			filtered = append(filtered, obs)
		}
	}
	switch len(filtered) {
	case 0:
		return nil
	case 1:
		return filtered[0]
	default:
		return &multiActivityObserver{observers: filtered}
	}
}
