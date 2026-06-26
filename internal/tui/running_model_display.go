package tui

import "strings"

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
	selectedRef = strings.TrimSpace(m.agent.ProviderModelRef())
	runningRef = strings.TrimSpace(m.agent.RunningModelRef())
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

func normalizeRunningModelDisplayAgentID(agentID string) string {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" || agentID == "main" || strings.HasPrefix(agentID, "main-") {
		return "main"
	}
	return agentID
}
