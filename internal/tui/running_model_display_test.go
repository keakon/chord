package tui

import (
	"strings"
	"testing"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/ratelimit"
)

// attemptRefAgent is the smallest stub that can present a distinct in-flight
// attempt target next to the confirmed running identity: the sidebar must name
// the attempt while the Context percentage keeps measuring the confirmed model.
type attemptRefAgent struct {
	*infoPanelAgent
	state agent.FocusedModelState
}

func (a *attemptRefAgent) FocusedModelState() agent.FocusedModelState { return a.state }

func newAttemptRefAgent(runningRef, attemptRef string) *attemptRefAgent {
	return &attemptRefAgent{
		infoPanelAgent: newInfoPanelAgent(),
		state: agent.FocusedModelState{
			SelectedRef: "prov/selected-model",
			RunningRef:  runningRef,
			DisplayRef:  attemptRef,
		},
	}
}

// TestFocusedModelStateFollowsRequestTarget pins the display rule: while a
// request is in flight the MODEL row names the attempt target (a fallback
// attempt included, before it has produced output), and it shows the next request target once the request returns.
func TestFocusedModelStateFollowsRequestTarget(t *testing.T) {
	backend := newAttemptRefAgent("primary-prov/primary-model", "fallback-prov/fallback-model")
	m := NewModelWithSize(backend, 100, 24)

	state := m.focusedModelState()
	running, selected := state.DisplayRef, state.SelectedRef
	if running != "fallback-prov/fallback-model" {
		t.Fatalf("display ref = %q, want the in-flight attempt target", running)
	}
	if selected != "prov/selected-model" {
		t.Fatalf("selected ref = %q, want prov/selected-model", selected)
	}

	// The backend supplies the next target after the request returns.
	backend.state.DisplayRef = backend.state.RunningRef
	running = m.focusedModelState().DisplayRef
	if running != "primary-prov/primary-model" {
		t.Fatalf("display ref after the request returned = %q, want the next target", running)
	}
}

// TestInfoPanelModelRowShowsAttemptTarget pins the user-visible outcome: the
// rendered MODEL block names the model the request was actually dispatched to,
// not the last confirmed one.
func TestInfoPanelModelRowShowsAttemptTarget(t *testing.T) {
	backend := newAttemptRefAgent("primary-prov/primary-model", "fallback-prov/fallback-model")
	m := NewModelWithSize(backend, 100, 24)

	plain := stripANSI(m.renderInfoPanel(80, 20))
	if !strings.Contains(plain, "fallback-model") {
		t.Fatalf("info panel = %q, want the in-flight attempt model", plain)
	}
	if strings.Contains(plain, "primary-model") {
		t.Fatalf("info panel = %q, must not show the stale confirmed model while an attempt is in flight", plain)
	}
}

// TestContextPressureLinesFollowConfirmedIdentityNotAttempt pins the budget
// split: the Context percentage is measured against the admitted model's fixed
// compaction budget, so its reminder/threshold lines must come from the
// confirmed identity even while the MODEL row names a different attempt target.
// Reading the display refs here would color the percentage against another
// model's lines.
func TestContextPressureLinesFollowConfirmedIdentityNotAttempt(t *testing.T) {
	backend := newAttemptRefAgent("primary-prov/primary-model", "fallback-prov/fallback-model")
	backend.contextLinesForRef = func(ref string) (float64, float64) {
		switch ref {
		case "primary-prov/primary-model":
			return 0.9, 0.7
		case "fallback-prov/fallback-model":
			return 0.1, 0.2
		}
		return 0, 0
	}
	m := NewModelWithSize(backend, 100, 24)
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityStreaming, AgentID: "main"}

	reminder, threshold := m.contextPressureLinesForFocusedModel()
	if reminder != 0.9 || threshold != 0.7 {
		t.Fatalf("context lines = (%v, %v), want the confirmed model's (0.9, 0.7)", reminder, threshold)
	}

	// Guard the guard: the same fixture must resolve the attempt model when the
	// display refs are consulted, so this test cannot pass by accident.
	if running := m.focusedModelState().DisplayRef; running != "fallback-prov/fallback-model" {
		t.Fatalf("display ref = %q, want the attempt target", running)
	}
}

func TestModelSurfacesUseCompleteNextTargetWhileIdleAndPreparing(t *testing.T) {
	for _, activity := range []agent.ActivityType{agent.ActivityIdle, agent.ActivityPreparing, agent.ActivityExecuting, agent.ActivityRetryingKey} {
		t.Run(string(activity), func(t *testing.T) {
			backend := newAttemptRefAgent("sample/model@high", "sample/model")
			backend.state.SelectedRef = "sample/model@high"
			m := NewModelWithSize(backend, 180, 30)
			m.activities["main"] = agent.AgentActivityEvent{Type: activity, AgentID: "main"}
			m.mode = ModeNormal
			m.rightPanelVisible = false
			panel := stripANSI(m.renderInfoPanel(80, 25))
			bar := stripANSI(m.renderStatusBar())
			if strings.Contains(panel, "@high") || strings.Contains(bar, "@high") || !strings.Contains(bar, "sample/model") {
				t.Fatalf("next target inherited a variant: panel=%q bar=%q", panel, bar)
			}
		})
	}
}

func TestModelSurfacesTierFollowsDisplayTarget(t *testing.T) {
	backend := newAttemptRefAgent("provider-a/model-a", "provider-b/model-b")
	backend.state.ServiceTier = config.ServiceTierFast
	backend.state.EffectiveTier = config.ServiceTierStandard
	m := NewModelWithSize(backend, 180, 30)
	m.mode = ModeNormal
	m.rightPanelVisible = false
	panel := m.renderInfoPanel(80, 25)
	if !strings.Contains(stripANSI(panel), "tier: fast") || !strings.Contains(panel, ";9m") {
		t.Fatalf("unsupported target tier lacks strikethrough: %q", panel)
	}
	if bar := stripANSI(m.renderStatusBar()); strings.Contains(bar, "TIER") {
		t.Fatalf("status bar claims unsupported tier: %q", bar)
	}
	backend.state.DisplayRef = "provider-c/model-c"
	backend.state.EffectiveTier = config.ServiceTierFast
	bar := stripANSI(m.renderStatusBar())
	if !strings.Contains(bar, "provider-c/model-c") || !strings.Contains(bar, "TIER fast") {
		t.Fatalf("next target did not refresh cached status: %q", bar)
	}
}

type changingModelSnapshotAgent struct {
	*infoPanelAgent
	reads int
}

func (a *changingModelSnapshotAgent) FocusedModelState() agent.FocusedModelState {
	a.reads++
	ref, keys, pct := "provider-a/model-a", 2, 17.0
	if a.reads > 1 {
		ref, keys, pct = "provider-b/model-b", 5, 63
	}
	return agent.FocusedModelState{
		SelectedRef: ref, RunningRef: ref, DisplayRef: ref,
		KeysConfirmed: keys, KeysTotal: keys,
		RateLimit: &ratelimit.KeyRateLimitSnapshot{Primary: &ratelimit.RateLimitWindow{UsedPct: pct}},
	}
}

func TestInfoPanelModelDataAndFingerprintShareSnapshot(t *testing.T) {
	backend := &changingModelSnapshotAgent{infoPanelAgent: newInfoPanelAgent()}
	m := NewModelWithSize(backend, 180, 40)
	first := stripANSI(m.renderInfoPanel(80, 35))
	if backend.reads != 1 || !strings.Contains(first, "model-a") || !strings.Contains(first, "Keys: 2/2") || !strings.Contains(first, "17%") {
		t.Fatalf("first snapshot reads=%d, panel=%q", backend.reads, first)
	}
	second := stripANSI(m.renderInfoPanel(80, 35))
	if backend.reads != 2 || !strings.Contains(second, "model-b") || !strings.Contains(second, "Keys: 5/5") || !strings.Contains(second, "63%") {
		t.Fatalf("second snapshot reads=%d, panel=%q", backend.reads, second)
	}
}

func TestDrawSharesModelSnapshotAcrossSidebarAndStatusBar(t *testing.T) {
	backend := &changingModelSnapshotAgent{infoPanelAgent: newInfoPanelAgent()}
	m := NewModelWithSize(backend, 180, 40)
	screen := newScreenBuffer(180, 40)
	m.Draw(screen, screen.Bounds())
	if backend.reads != 1 || m.statusBarAgentSnapshot.modelRef != "provider-a/model-a" {
		t.Fatalf("draw reads=%d, status ref=%q", backend.reads, m.statusBarAgentSnapshot.modelRef)
	}
	m.Draw(screen, screen.Bounds())
	if backend.reads != 2 || m.statusBarAgentSnapshot.modelRef != "provider-b/model-b" {
		t.Fatalf("next draw reads=%d, status ref=%q", backend.reads, m.statusBarAgentSnapshot.modelRef)
	}
}
