package tui

import (
	"strings"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/identity"
)

// contextPressureDisplayRef picks the model reference whose reminder/threshold
// lines should color the context display, mirroring exactly which model the
// focused agent's MODEL row shows: the running model while busy, otherwise the
// next-request model (a pending model switch), falling back to the selected
// model. Keeping the color tied to the displayed model means a model switch
// with a different threshold re-colors the context value, gauge and pill as
// soon as the switch is shown, instead of lingering on the previous model's
// lines until the next request boundary.
func contextPressureDisplayRef(busy bool, runningRef, selectedRef, nextRef string) string {
	ref := strings.TrimSpace(runningRef)
	if !busy {
		ref = strings.TrimSpace(nextRef)
	}
	if ref == "" {
		ref = strings.TrimSpace(selectedRef)
	}
	return ref
}

// contextPressureLinesForFocusedModel resolves the reminder/threshold lines
// for the model the focused agent currently displays. The main agent manages
// its context with usage-driven compaction lines, so it is queried per the
// displayed model ref. Focused SubAgents and parked targets keep their context
// with sliding-window compaction rather than usage lines, so they keep the
// fixed fallback lines instead (both lines 0).
func (m *Model) contextPressureLinesForFocusedModel() (reminder, threshold float64) {
	if m == nil || m.agent == nil || m.focusedAgentIDOrMain() != identity.MainAgentID {
		return 0, 0
	}
	runningRef, selectedRef := m.focusedModelRefs()
	ref := contextPressureDisplayRef(m.isFocusedAgentBusy(), runningRef, selectedRef, nextRequestModelRefForAgent(m.agent))
	if ref == "" {
		return 0, 0
	}
	return m.agent.ContextPressureLinesForModelRef(ref)
}

type runningModelDisplayState struct {
	agentID          string
	providerModelRef string
	runningModelRef  string
}

func (m *Model) noteRunningModelDisplay(agentID, providerRef, runningRef string) {
	if m == nil {
		return
	}
	runningRef = strings.TrimSpace(runningRef)
	providerRef = strings.TrimSpace(providerRef)
	if runningRef == "" && providerRef == "" {
		m.runningModelDisplay = runningModelDisplayState{}
		return
	}
	m.runningModelDisplay = runningModelDisplayState{
		agentID:          normalizeRunningModelDisplayAgentID(agentID),
		providerModelRef: providerRef,
		runningModelRef:  runningRef,
	}
}

func (m *Model) clearRunningModelDisplay(agentID string) {
	if m == nil || m.runningModelDisplay.agentID == "" {
		return
	}
	if agentID == "" || normalizeRunningModelDisplayAgentID(agentID) == m.runningModelDisplay.agentID {
		m.runningModelDisplay = runningModelDisplayState{}
	}
}

func (m *Model) focusedModelRefs() (runningRef, selectedRef string) {
	if m == nil || m.agent == nil {
		return "", ""
	}
	state := m.focusedModelState()
	selectedRef = strings.TrimSpace(state.SelectedRef)
	runningRef = strings.TrimSpace(state.RunningRef)
	if m.focusedAgentID != "" && selectedRef == "" && runningRef == "" {
		if selected, running, ok := m.sidebar.SubAgentModelRefs(m.focusedAgentID); ok {
			selectedRef = strings.TrimSpace(selected)
			runningRef = strings.TrimSpace(running)
		} else {
			selectedRef = runningRef
		}
	}
	if m.isFocusedAgentBusy() && m.runningModelDisplay.agentID == m.focusedAgentIDOrMain() {
		if ref := strings.TrimSpace(m.runningModelDisplay.providerModelRef); ref != "" {
			selectedRef = ref
		}
		if ref := strings.TrimSpace(m.runningModelDisplay.runningModelRef); ref != "" {
			runningRef = ref
		}
	}
	return runningRef, selectedRef
}

func (m *Model) focusedModelState() agent.FocusedModelState {
	if m == nil || m.agent == nil {
		return agent.FocusedModelState{}
	}
	if provider, ok := m.agent.(agent.FocusedModelStateProvider); ok {
		return provider.FocusedModelState()
	}
	if m.focusedAgentID != "" {
		if selected, running, ok := m.sidebar.SubAgentModelRefs(m.focusedAgentID); ok {
			return agent.FocusedModelState{
				SelectedRef: strings.TrimSpace(selected),
				RunningRef:  strings.TrimSpace(running),
				Variant:     strings.TrimSpace(m.agent.RunningVariant()),
				PoolName:    strings.TrimSpace(m.agent.CurrentPoolName()),
				PoolNames:   m.agent.PoolNames(),
			}
		}
	}
	return agent.FocusedModelState{
		SelectedRef: strings.TrimSpace(m.agent.ProviderModelRef()),
		RunningRef:  strings.TrimSpace(m.agent.RunningModelRef()),
		Variant:     strings.TrimSpace(m.agent.RunningVariant()),
		PoolName:    strings.TrimSpace(m.agent.CurrentPoolName()),
		PoolNames:   m.agent.PoolNames(),
	}
}

func normalizeRunningModelDisplayAgentID(agentID string) string {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" || agentID == "main" {
		return "main"
	}
	return agentID
}
