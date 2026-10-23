package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/analytics"
	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/skill"
	"github.com/keakon/chord/internal/tui/terminaltext"
)

// externalDisplayAgent presents a focused external backend the way the agent
// layer does: Chord has no model identity or usage/context/timing readings for
// it, and the skill catalog belongs to the CLI.
type externalDisplayAgent struct {
	*infoPanelAgent
}

func newExternalDisplayAgent() *externalDisplayAgent {
	backend := newInfoPanelAgent()
	backend.usage = analytics.SessionStats{InputTokens: 10_000, OutputTokens: 1_000, EstimatedCost: 1}
	backend.contextCurrent = 50_000
	backend.contextBytes = 10_000
	backend.contextMessageCount = 12
	backend.walltimeStats = analytics.WalltimeStats{Model: time.Minute}
	backend.availableSkills = []*skill.Meta{{Name: "go-expert"}}
	backend.invokedSkills = []*skill.Meta{{Name: "go-expert"}}
	backend.lspRows = []agent.LSPServerDisplay{{Name: "gopls", OK: true}}
	backend.mcpRows = []agent.MCPServerDisplay{{Name: "fs", OK: true}}
	backend.keysConfirmed = 2
	backend.keysTotal = 2
	return &externalDisplayAgent{infoPanelAgent: backend}
}

func (a *externalDisplayAgent) FocusedModelState() agent.FocusedModelState {
	return agent.FocusedModelState{Backend: config.BackendKindClaudeCode}
}

func (a *externalDisplayAgent) FocusedSkills() []*skill.Meta { return nil }

func (a *externalDisplayAgent) FocusedSkillInvocationStates() []skill.InvocationState {
	return nil
}

func (a *externalDisplayAgent) InvokedSkills() []*skill.Meta { return nil }

func TestInfoPanelHidesModelAndChordUsageForExternalFocus(t *testing.T) {
	m := NewModelWithSize(newExternalDisplayAgent(), 100, 24)
	m.focusedAgentID = "external-1"

	lines := infoPanelPlainLines(m.renderInfoPanel(40, 40))
	plain := strings.Join(lines, "\n")
	for _, title := range []string{"MODEL", "USAGE", "TIME", "SKILLS", "LSP", "MCP"} {
		if got := infoPanelSectionLines(lines, title); len(got) != 0 {
			t.Fatalf("external info panel rendered %s section: %#v", title, got)
		}
	}
	backendLines := infoPanelSectionLines(lines, "BACKEND")
	if len(backendLines) != 1 || backendLines[0] != config.BackendKindClaudeCode {
		t.Fatalf("BACKEND section = %#v, want [%q]", backendLines, config.BackendKindClaudeCode)
	}
	for _, unwanted := range []string{"unknown", "Provider:", "Pool", "Keys:", "tier:", "TOKENS", "test-model", "go-expert", "gopls"} {
		if strings.Contains(plain, unwanted) {
			t.Fatalf("external info panel = %q, must not show %q", plain, unwanted)
		}
	}
}

// externalStatusAgent reports the backend through FocusedModelState while the
// raw getters still carry main-role readings, so the pill guard itself is what
// the test observes.
type externalStatusAgent struct {
	*sessionControlAgent
}

func (a *externalStatusAgent) FocusedModelState() agent.FocusedModelState {
	return agent.FocusedModelState{Backend: config.BackendKindClaudeCode}
}

func TestStatusBarHidesModelAndUsagePillsForExternalFocus(t *testing.T) {
	backend := &externalStatusAgent{sessionControlAgent: &sessionControlAgent{
		currentRole:          "builder",
		focused:              "external-1",
		runningModelRef:      "anthropic/claude-sonnet-4.5",
		tokenUsage:           message.TokenUsage{InputTokens: 10_000, OutputTokens: 1_000},
		sidebarUsage:         analytics.SessionStats{EstimatedCost: 1},
		contextCurrent:       50_000,
		contextLimit:         200_000,
		serviceTierEnabled:   true,
		serviceTier:          config.ServiceTierFast,
		effectiveServiceTier: config.ServiceTierFast,
	}}
	m := NewModelWithSize(backend, 200, 24)
	m.mode = ModeNormal
	m.rightPanelVisible = false
	m.focusedAgentID = "external-1"

	plain := terminaltext.StripANSI(m.renderStatusBar())
	for _, unwanted := range []string{"◇", "unknown", "TIER", "↑", "↓", "50k", "25%" /* context pill percent */} {
		if strings.Contains(plain, unwanted) {
			t.Fatalf("external status bar = %q, must not show %q", plain, unwanted)
		}
	}
}

func TestOpenModelSelectRefusesExternalFocus(t *testing.T) {
	backend := &externalStatusAgent{sessionControlAgent: &sessionControlAgent{focused: "external-1"}}
	m := NewModelWithSize(backend, 120, 24)
	m.mode = ModeNormal
	m.focusedAgentID = "external-1"
	backend.focused = "external-1"

	cmd := m.openModelSelectFor(agent.ModelPoolSelectorTarget{Kind: agent.ModelPoolSelectorTargetCurrentView})
	if cmd == nil {
		t.Fatal("external /models returned no toast command")
	}
	if m.mode == ModeModelSelect {
		t.Fatal("external /models entered the model selector")
	}
	if m.activeToast == nil || m.activeToast.Level != "warn" {
		t.Fatalf("external /models toast = %+v, want a warn toast", m.activeToast)
	}
	if !strings.Contains(m.activeToast.Message, "external agents") || !strings.Contains(m.activeToast.Message, config.BackendKindClaudeCode) {
		t.Fatalf("external /models toast = %q, want the backend named", m.activeToast.Message)
	}
}
