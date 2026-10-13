package executiongate

import "time"

// TryFreeze closes admission only when the active count fits the caller's
// already admitted operation. A rejected check leaves admission unchanged.
func (g *Gate) TryFreeze(maxActive int) (FreezeState, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	state := FreezeState{FrozenAt: g.frozenAt, Active: len(g.active)}
	if g.paused || state.Active > maxActive {
		return state, false
	}
	if g.frozen == nil {
		g.frozen = make(chan struct{})
	}
	g.paused = true
	g.frozenAt = time.Now()
	close(g.frozen)
	state.FrozenAt = g.frozenAt
	return state, true
}
