package tui

import (
	"strings"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/tui/modelref"
)

// contextPressureLinesForFocusedModel uses the committed budget identity even
// when MODEL already previews the next request. Workers use sliding-window
// compaction, so they keep the fixed fallback lines (both zero).
func (m *Model) contextPressureLinesForFocusedModel() (reminder, threshold float64) {
	if m == nil || m.agent == nil || m.focusedAgentIDOrMain() != identity.MainAgentID {
		return 0, 0
	}
	state := m.focusedModelState()
	ref := strings.TrimSpace(state.RunningRef)
	if ref == "" {
		ref = strings.TrimSpace(state.SelectedRef)
	}
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

// beginModelDisplayFrame captures model/provider data once for a render pass.
// Nested surfaces share the caller's snapshot, including its cache fingerprint.
func (m *Model) beginModelDisplayFrame() bool {
	if m.frameModelStateActive {
		return false
	}
	m.frameModelState = m.captureFocusedModelState()
	m.frameModelStateActive = true
	return true
}

func (m *Model) endModelDisplayFrame() {
	m.frameModelStateActive = false
	m.frameModelState = agent.FocusedModelState{}
}

func (m *Model) focusedModelState() agent.FocusedModelState {
	if m == nil {
		return agent.FocusedModelState{}
	}
	if m.frameModelStateActive {
		return m.frameModelState
	}
	return m.captureFocusedModelState()
}

func (m *Model) captureFocusedModelState() agent.FocusedModelState {
	if m.agent == nil {
		return agent.FocusedModelState{ServiceTier: config.ServiceTierStandard, EffectiveTier: config.ServiceTierStandard}
	}
	if provider, ok := m.agent.(agent.FocusedModelStateProvider); ok {
		state := provider.FocusedModelState()
		state.ServiceTier = config.NormalizeServiceTier(string(state.ServiceTier))
		state.EffectiveTier = config.NormalizeServiceTier(string(state.EffectiveTier))
		return state
	}
	state := agent.FocusedModelState{
		SelectedRef:   strings.TrimSpace(m.agent.ProviderModelRef()),
		RunningRef:    strings.TrimSpace(m.agent.RunningModelRef()),
		PoolName:      strings.TrimSpace(m.agent.CurrentPoolName()),
		PoolNames:     m.agent.PoolNames(),
		RateLimit:     m.agent.CurrentRateLimitSnapshot(),
		ServiceTier:   m.serviceTier(),
		EffectiveTier: m.effectiveServiceTier(),
	}
	state.KeysConfirmed, state.KeysTotal = m.agent.KeyStats()
	if m.focusedAgentID != "" {
		if selected, running, ok := m.sidebar.SubAgentModelRefs(m.focusedAgentID); ok {
			state.SelectedRef = strings.TrimSpace(selected)
			state.RunningRef = strings.TrimSpace(running)
		}
	}
	if m.isFocusedAgentBusy() && m.runningModelDisplay.agentID == m.focusedAgentIDOrMain() {
		state.SelectedRef = m.runningModelDisplay.providerModelRef
		state.RunningRef = m.runningModelDisplay.runningModelRef
	}
	state.DisplayRef = modelref.EnsureRefShowsProvider(state.RunningRef, state.SelectedRef)
	state.DisplayRef = modelref.EnsureRefShowsMatchingVariant(state.DisplayRef, state.SelectedRef, strings.TrimSpace(m.agent.RunningVariant()))
	if state.DisplayRef == "" {
		state.DisplayRef = state.SelectedRef
	}
	activity := m.activityForAgent(m.focusedAgentIDOrMain()).Type
	if !m.isFocusedAgentBusy() || activity == agent.ActivityPreparing || activity == agent.ActivityExecuting {
		if next := nextRequestModelRefForAgent(m.agent); next != "" {
			state.DisplayRef = next
		}
	}
	return state
}

func normalizeRunningModelDisplayAgentID(agentID string) string {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" || agentID == "main" {
		return "main"
	}
	return agentID
}
