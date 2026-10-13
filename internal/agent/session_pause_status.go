package agent

import (
	"cmp"
	"slices"
)

// Stable ordering keeps an unchanged set of active leases from publishing
// another status just because the gate's map iteration order changed.
func sortSessionPauseBlockers(blockers []SessionPauseBlocker) {
	slices.SortFunc(blockers, func(a, b SessionPauseBlocker) int {
		if order := cmp.Compare(a.Kind, b.Kind); order != 0 {
			return order
		}
		if order := cmp.Compare(a.OwnerTaskID, b.OwnerTaskID); order != 0 {
			return order
		}
		if order := a.StartedAt.Compare(b.StartedAt); order != 0 {
			return order
		}
		return cmp.Compare(a.Description, b.Description)
	})
}

// The owner loop publishes only changed blockers. Queries and the source UI
// then observe the same status without adding an idle polling worker.
func (a *MainAgent) publishSessionPauseBlockers(blockers []SessionPauseBlocker) {
	a.pause.mu.Lock()
	unchanged := a.pause.status.BlockerCount == len(blockers) && slices.Equal(a.pause.status.Blockers, blockers)
	a.pause.mu.Unlock()
	if unchanged {
		return
	}
	a.setSessionPauseStatus(func(status *SessionPauseStatus) {
		status.Blockers = blockers
		status.BlockerCount = len(blockers)
	})
}
