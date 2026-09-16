package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/config"
)

func TestRenderStatusBarShowsServiceTierAfterModelWhenInfoPanelHidden(t *testing.T) {
	backend := &sessionControlAgent{currentRole: "builder", serviceTierEnabled: true, runningModelRef: "anthropic/claude-sonnet-4.5"}
	m := NewModelWithSize(backend, 180, 24)
	m.mode = ModeNormal
	m.rightPanelVisible = false

	plain := stripANSI(m.renderStatusBar())
	modelIdx := strings.Index(plain, "◇ anthropic/claude-sonnet-4.5")
	fastIdx := strings.Index(plain, "TIER")
	if modelIdx < 0 || fastIdx < 0 || fastIdx <= modelIdx {
		t.Fatalf("status bar = %q, want TIER after model when info panel is hidden", plain)
	}
}

func TestRenderStatusBarServiceTierChangesFingerprint(t *testing.T) {
	backend := &sessionControlAgent{currentRole: "builder"}
	m := NewModelWithSize(backend, 180, 24)
	now := time.Unix(123, 0)

	before := m.statusBarFingerprint(now)
	backend.serviceTierEnabled = true
	after := m.statusBarFingerprint(now)
	if before == after {
		t.Fatal("statusBarFingerprint did not change after service tier changed")
	}
}

func TestRenderStatusBarHidesUnsupportedServiceTier(t *testing.T) {
	backend := &sessionControlAgent{currentRole: "builder", serviceTierEnabled: true, serviceTier: config.ServiceTierFast, effectiveServiceTier: config.ServiceTierStandard, runningModelRef: "anthropic/claude-sonnet-4.5"}
	m := NewModelWithSize(backend, 180, 24)
	m.mode = ModeNormal
	m.rightPanelVisible = false

	plain := stripANSI(m.renderStatusBar())
	if strings.Contains(plain, "TIER") {
		t.Fatalf("status bar = %q, should hide unsupported tier", plain)
	}
}

func TestRenderInfoPanelMarksUnsupportedFastServiceTier(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.serviceTierEnabled = true
	backend.serviceTier = config.ServiceTierFast
	backend.effectiveServiceTier = config.ServiceTierStandard
	m := NewModelWithSize(backend, 100, 24)

	out := m.renderInfoPanel(40, 20)
	plain := stripANSI(out)
	if !strings.Contains(plain, "tier: fast") {
		t.Fatalf("info panel = %q, want unsupported fast tier shown", plain)
	}
	if idxStrike, idxTier := strings.Index(out, ";9m"), strings.Index(out, "tier: "); idxStrike < 0 || idxTier < 0 || idxStrike <= idxTier+len("tier: ") {
		t.Fatalf("info panel = %q, want only unsupported fast tier value strikethrough", out)
	}
}

func TestRenderInfoPanelShowsRunningModelWhileBusy(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.providerModelRef = "openai/gpt-5.5"
	backend.runningModelRef = "openai/gpt-5.4"
	backend.serviceTierEnabled = true
	backend.serviceTier = config.ServiceTierFast
	backend.effectiveServiceTier = config.ServiceTierStandard
	m := NewModelWithSize(backend, 100, 24)
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityStreaming, AgentID: "main"}

	out := m.renderInfoPanel(80, 20)
	plain := stripANSI(out)
	if !strings.Contains(plain, "gpt-5.4") || !strings.Contains(plain, "Provider: openai") {
		t.Fatalf("info panel = %q, want running model with provider line", plain)
	}
	if strings.Contains(plain, "gpt-5.5") || strings.Contains(plain, "->") {
		t.Fatalf("info panel = %q, should not show selected model transition while busy", plain)
	}
	if !strings.Contains(plain, "tier: fast") {
		t.Fatalf("info panel = %q, want current fast tier shown", plain)
	}
	if !strings.Contains(out, ";9m") {
		t.Fatalf("info panel = %q, unsupported fast tier value should remain struck through", out)
	}
}

func TestRenderInfoPanelShowsRunningModelWhileIdle(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.providerModelRef = "openai/gpt-5.5"
	backend.runningModelRef = "openai/gpt-5.4"
	m := NewModelWithSize(backend, 100, 24)

	plain := stripANSI(m.renderInfoPanel(80, 20))
	if !strings.Contains(plain, "gpt-5.4") || !strings.Contains(plain, "Provider: openai") {
		t.Fatalf("info panel = %q, want running model with provider line", plain)
	}
	if strings.Contains(plain, "gpt-5.5") {
		t.Fatalf("info panel = %q, should not revert to selected model while idle", plain)
	}
}

func TestRenderStatusBarShowsRunningModelWhileIdle(t *testing.T) {
	events := make(chan agent.AgentEvent, 1)
	backend := &sessionControlAgent{
		events:           events,
		providerModelRef: "prov/selected-model",
		runningModelRef:  "prov/running-fallback",
	}
	m := NewModelWithSize(backend, 220, 24)
	m.mode = ModeNormal
	m.rightPanelVisible = false
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityIdle, AgentID: "main"}

	plain := stripANSI(m.renderStatusBar())
	if !strings.Contains(plain, "prov/running-fallback") {
		t.Fatalf("status bar = %q, want running model", plain)
	}
	if strings.Contains(plain, "prov/selected-model") {
		t.Fatalf("status bar = %q, should not revert to selected model while idle", plain)
	}
}

func TestRenderInfoPanelMarksUnsupportedSlowServiceTier(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.serviceTier = config.ServiceTierSlow
	backend.effectiveServiceTier = config.ServiceTierStandard
	m := NewModelWithSize(backend, 100, 24)

	out := m.renderInfoPanel(40, 20)
	plain := stripANSI(out)
	if !strings.Contains(plain, "tier: slow") {
		t.Fatalf("info panel = %q, want unsupported slow tier shown", plain)
	}
	if idxStrike, idxTier := strings.Index(out, ";9m"), strings.Index(out, "tier: "); idxStrike < 0 || idxTier < 0 || idxStrike <= idxTier+len("tier: ") {
		t.Fatalf("info panel = %q, want only unsupported slow tier value strikethrough", out)
	}
}

func TestNarrowStatusBarShowsRunningModelRefVerbatim(t *testing.T) {
	events := make(chan agent.AgentEvent, 1)
	a := &sessionControlAgent{
		events:           events,
		providerModelRef: "sample/gpt-5.5@xhigh",
	}
	m := NewModelWithSize(a, 80, 24)
	m.mode = ModeNormal
	m.updateRightPanelVisible()
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityStreaming, AgentID: "main"}

	plain := stripANSI(m.renderStatusBar())
	want := "sample/gpt-5.5@xhigh"
	if !strings.Contains(plain, want) {
		t.Fatalf("status bar should show RunningModelRef verbatim %q; got %q", want, plain)
	}
}

func TestNarrowStatusBarDoesNotLeakVariantToFallbackModel(t *testing.T) {
	events := make(chan agent.AgentEvent, 1)
	a := &sessionControlAgent{
		events:           events,
		providerModelRef: "sample/gpt-5.5@xhigh",
		runningModelRef:  "sample/glm-5.1",
		runningVariant:   "xhigh",
	}
	m := NewModelWithSize(a, 80, 24)
	m.mode = ModeNormal
	m.updateRightPanelVisible()
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityStreaming, AgentID: "main"}

	plain := stripANSI(m.renderStatusBar())
	if !strings.Contains(plain, "sample/glm-5.1") {
		t.Fatalf("status bar should show fallback running model; got %q", plain)
	}
	if strings.Contains(plain, "sample/glm-5.1@xhigh") {
		t.Fatalf("status bar should not leak selected variant onto fallback model; got %q", plain)
	}
}

func TestNarrowStatusBarShowsFallbackModelWithoutVariantWhenPrimaryHasNoVariant(t *testing.T) {
	events := make(chan agent.AgentEvent, 1)
	a := &sessionControlAgent{
		events:           events,
		providerModelRef: "sample/gpt-5.5",
		runningModelRef:  "sample/glm-5.1",
	}
	m := NewModelWithSize(a, 80, 24)
	m.mode = ModeNormal
	m.updateRightPanelVisible()
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityStreaming, AgentID: "main"}

	plain := stripANSI(m.renderStatusBar())
	if !strings.Contains(plain, "sample/glm-5.1") {
		t.Fatalf("status bar should show fallback model name; got %q", plain)
	}
	if strings.Contains(plain, "@") {
		t.Fatalf("status bar should not show variant for fallback when primary has none; got %q", plain)
	}
}

func TestStatusBarShowsRunningModelWhileBusy(t *testing.T) {
	events := make(chan agent.AgentEvent, 1)
	a := &sessionControlAgent{
		events:               events,
		providerModelRef:     "openai/gpt-5.5",
		runningModelRef:      "openai/gpt-5.4",
		serviceTier:          config.ServiceTierFast,
		effectiveServiceTier: config.ServiceTierStandard,
	}
	m := NewModelWithSize(a, 220, 24)
	m.mode = ModeNormal
	m.rightPanelVisible = false
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityStreaming, AgentID: "main"}

	plain := stripANSI(m.renderStatusBar())
	if !strings.Contains(plain, "openai/gpt-5.4") {
		t.Fatalf("status bar should show running model; got %q", plain)
	}
	if strings.Contains(plain, "openai/gpt-5.5") || strings.Contains(plain, "->") {
		t.Fatalf("status bar should not show selected model transition while busy; got %q", plain)
	}
	if strings.Contains(plain, "TIER fast") {
		t.Fatalf("status bar should show current effective tier while busy; got %q", plain)
	}
}

func TestNarrowStatusBarShowsFallbackVariantWhenRunningModelRefIncludesVariant(t *testing.T) {
	events := make(chan agent.AgentEvent, 1)
	a := &sessionControlAgent{
		events:           events,
		providerModelRef: "sample/gpt-5.5@xhigh",
		runningModelRef:  "sample/glm-5.1@high",
		runningVariant:   "xhigh",
	}
	m := NewModelWithSize(a, 80, 24)
	m.mode = ModeNormal
	m.updateRightPanelVisible()
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityStreaming, AgentID: "main"}

	plain := stripANSI(m.renderStatusBar())
	if !strings.Contains(plain, "sample/glm-5.1@high") {
		t.Fatalf("status bar should show fallback running model variant; got %q", plain)
	}
	if strings.Contains(plain, "sample/glm-5.1@xhigh") {
		t.Fatalf("status bar should not leak selected variant onto fallback model; got %q", plain)
	}
}

func TestSidebarSubAgentModelRefs(t *testing.T) {
	s := NewSidebar(DefaultTheme())
	s.Update([]agent.SubAgentInfo{
		{InstanceID: "a1", SelectedRef: "p/m@high", RunningRef: "p/m2"},
	}, "main", "builder")
	sel, run, ok := s.SubAgentModelRefs("a1")
	if !ok || sel != "p/m@high" || run != "p/m2" {
		t.Fatalf("SubAgentModelRefs = (%q,%q,%v), want (p/m@high, p/m2, true)", sel, run, ok)
	}
	_, _, ok = s.SubAgentModelRefs("missing")
	if ok {
		t.Fatal("expected ok false for unknown agent id")
	}
}

func TestRunningModelChangedEventUpdatesSubAgentSidebarBeforeSnapshot(t *testing.T) {
	backend := &sessionControlAgent{
		events: make(chan agent.AgentEvent, 1),
		subAgents: []agent.SubAgentInfo{{
			InstanceID:   "agent-1",
			AgentDefName: "reviewer",
			TaskDesc:     "check code",
			SelectedRef:  "worker/primary",
			RunningRef:   "worker/primary",
		}},
	}
	m := NewModel(backend)
	m.refreshSidebar()

	m.handleAgentEvent(agentEventMsg{event: agent.RunningModelChangedEvent{
		AgentID:          "agent-1",
		ProviderModelRef: "worker/primary",
		RunningModelRef:  "worker/fallback",
	}})

	selected, running, ok := m.sidebar.SubAgentModelRefs("agent-1")
	if !ok {
		t.Fatal("expected sidebar model refs for agent-1")
	}
	if selected != "worker/primary" || running != "worker/fallback" {
		t.Fatalf("sidebar refs = %q/%q, want worker/primary/worker/fallback", selected, running)
	}
}

// Focused SubAgent model display uses the sidebar's selected/running snapshot
// instead of leaking the main agent's selected provider reference.
func TestNarrowStatusBarModelUsesFocusedSubAgentSnapshot(t *testing.T) {
	events := make(chan agent.AgentEvent, 1)
	a := &sessionControlAgent{
		events:           events,
		providerModelRef: "main/huge",
	}
	m := NewModelWithSize(a, 80, 24)
	m.mode = ModeNormal
	m.updateRightPanelVisible()
	m.focusedAgentID = "worker-1"
	m.sidebar.Update([]agent.SubAgentInfo{
		{InstanceID: "worker-1", SelectedRef: "sample/tiny", RunningRef: "sample/tiny"},
	}, "worker-1", "builder")

	plain := stripANSI(m.renderStatusBar())
	if !strings.Contains(plain, "sample/tiny") {
		t.Fatalf("status bar should show focused running model %q; got %q", "sample/tiny", plain)
	}
	if strings.Contains(plain, "main/huge") {
		t.Fatalf("status bar should not use main selected ref for a focused SubAgent; got %q", plain)
	}
}

func TestRunningModelChangedEventOverridesMainModelDisplayImmediately(t *testing.T) {
	backend := &sessionControlAgent{
		events:           make(chan agent.AgentEvent, 1),
		providerModelRef: "provider-a/gpt-5.5@xhigh",
		runningModelRef:  "provider-b/gpt-5.5@xhigh",
	}
	m := NewModelWithSize(backend, 220, 24)
	m.mode = ModeNormal
	m.rightPanelVisible = false
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityRetrying, AgentID: "main", Detail: "fallback"}

	m.handleAgentEvent(agentEventMsg{event: agent.RunningModelChangedEvent{
		AgentID:          "main",
		ProviderModelRef: "provider-a/gpt-5.5@xhigh",
		RunningModelRef:  "provider-c/gpt-5.5@xhigh",
	}})

	status := stripANSI(m.renderStatusBar())
	if !strings.Contains(status, "provider-c/gpt-5.5@xhigh") {
		t.Fatalf("status bar should show event running model immediately; got %q", status)
	}

	info := stripANSI(m.renderInfoPanel(40, 20))
	if !strings.Contains(info, "gpt-5.5@xhigh") || !strings.Contains(info, "Provider: provider-c") {
		t.Fatalf("info panel should show event running model immediately; got %q", info)
	}
	if strings.Contains(info, "Provider: provider-b") || strings.Contains(info, "Provider: provider-a") {
		t.Fatalf("info panel should not keep stale backend running provider after event; got %q", info)
	}
}

func TestInfoPanelFingerprintIncludesTransientRunningModelDisplay(t *testing.T) {
	backend := &sessionControlAgent{
		events:           make(chan agent.AgentEvent, 1),
		providerModelRef: "provider-a/gpt-5.5@xhigh",
		runningModelRef:  "provider-b/gpt-5.5@xhigh",
	}
	m := NewModelWithSize(backend, 220, 24)
	m.mode = ModeNormal
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityRetrying, AgentID: "main", Detail: "fallback"}

	first := stripANSI(m.renderInfoPanel(40, 20))
	if !strings.Contains(first, "gpt-5.5@xhigh") || !strings.Contains(first, "Provider: provider-b") {
		t.Fatalf("initial info panel should show backend running model; got %q", first)
	}
	cachedOut := m.cachedInfoPanelOut

	m.noteRunningModelDisplay("main", "provider-a/gpt-5.5@xhigh", "provider-c/gpt-5.5@xhigh")
	second := stripANSI(m.renderInfoPanel(40, 20))
	if !strings.Contains(second, "gpt-5.5@xhigh") || !strings.Contains(second, "Provider: provider-c") {
		t.Fatalf("info panel should include transient running model in fingerprint; got %q", second)
	}
	if strings.Contains(second, "Provider: provider-b") {
		t.Fatalf("info panel should not reuse stale cached backend model; got %q", second)
	}
	if m.cachedInfoPanelOut == cachedOut {
		t.Fatal("info panel cache output should refresh when transient running model display changes")
	}
}

func TestRunningModelChangedEventDisplayClearsOnIdle(t *testing.T) {
	backend := &sessionControlAgent{
		events:           make(chan agent.AgentEvent, 1),
		providerModelRef: "provider-a/gpt-5.5@xhigh",
		runningModelRef:  "provider-b/gpt-5.5@xhigh",
	}
	m := NewModelWithSize(backend, 220, 24)
	m.mode = ModeNormal
	m.rightPanelVisible = false
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityRetrying, AgentID: "main", Detail: "fallback"}
	m.handleAgentEvent(agentEventMsg{event: agent.RunningModelChangedEvent{
		AgentID:          "main",
		ProviderModelRef: "provider-a/gpt-5.5@xhigh",
		RunningModelRef:  "provider-c/gpt-5.5@xhigh",
	}})

	m.handleAgentEvent(agentEventMsg{event: agent.AgentActivityEvent{Type: agent.ActivityIdle, AgentID: "main"}})

	status := stripANSI(m.renderStatusBar())
	if strings.Contains(status, "provider-c/gpt-5.5@xhigh") {
		t.Fatalf("status bar should clear transient event running model on idle; got %q", status)
	}
}

func TestStatusBarCurrentAgentLabelMainWithoutAgent(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	if got := m.statusBarCurrentAgentLabel(); got != "main" {
		t.Fatalf("label = %q, want main", got)
	}
}

func TestStatusBarCurrentAgentLabelSubUsesAgentDefName(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.focusedAgentID = "s-1"
	m.sidebar.Update([]agent.SubAgentInfo{
		{InstanceID: "s-1", AgentDefName: "reviewer", TaskDesc: "check code", SelectedRef: "p/m", RunningRef: "p/m"},
	}, "s-1", "builder")
	if got := m.statusBarCurrentAgentLabel(); got != "s-1" {
		t.Fatalf("label = %q, want s-1", got)
	}
}

func TestStatusBarCurrentAgentLabelSubOmitsTaskDesc(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.focusedAgentID = "s-2"
	m.sidebar.Update([]agent.SubAgentInfo{
		{InstanceID: "s-2", TaskDesc: "do the thing", SelectedRef: "p/m", RunningRef: "p/m"},
	}, "s-2", "builder")
	got := m.statusBarCurrentAgentLabel()
	if got != "s-2" {
		t.Fatalf("without AgentDefName, label should be instance id only; got %q", got)
	}
}
