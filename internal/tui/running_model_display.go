package tui

import (
	"strings"

	"github.com/keakon/chord/internal/agent"
)

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
