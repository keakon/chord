package agent

import "sync"

type directoryInstructionStopReason string

const (
	directoryInstructionStopRequired directoryInstructionStopReason = "directory_instructions_required"
	directoryInstructionStopBlocked  directoryInstructionStopReason = "directory_instructions_blocked"
	directoryInstructionStopReplan   directoryInstructionStopReason = "directory_instructions_replan"
)

type directoryInstructionGate struct {
	mu       sync.Mutex
	reason   directoryInstructionStopReason
	admitted map[string]struct{}
}

func (g *directoryInstructionGate) TryAdmit(callID string) bool {
	if g == nil {
		return true
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, ok := g.admitted[callID]; ok {
		return true
	}
	if g.reason != "" {
		return false
	}
	if g.admitted == nil {
		g.admitted = make(map[string]struct{})
	}
	g.admitted[callID] = struct{}{}
	return true
}
func (g *directoryInstructionGate) Close(reason directoryInstructionStopReason) {
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.reason == "" {
		if reason == "" {
			reason = directoryInstructionStopReplan
		}
		g.reason = reason
	}
}
func (g *directoryInstructionGate) Reason() directoryInstructionStopReason {
	if g == nil {
		return ""
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.reason
}
