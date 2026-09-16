package tui

import (
	"strings"
	"testing"

	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/config"
)

func TestSlashCompletionTabCompletesWithoutSubmitting(t *testing.T) {
	backend := &sessionControlAgent{}
	m := NewModel(backend)
	m.mode = ModeInsert
	m.input.SetValue("/exp")

	cmd := m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyTab}))
	if cmd != nil {
		t.Fatal("Tab should not submit while slash completion is visible")
	}
	if got := m.input.Value(); got != "/export " {
		t.Fatalf("input value after Tab = %q, want /export<space>", got)
	}
	if got := len(backend.sentMessages); got != 0 {
		t.Fatalf("SendUserMessage() calls = %d, want 0", got)
	}
}

func TestSlashCompletionEnterCompletesSelectedCommandAndSubmitsIt(t *testing.T) {
	backend := &sessionControlAgent{}
	m := NewModel(backend)
	m.mode = ModeInsert
	m.input.SetValue("/r")

	matches := m.getSlashCompletions(m.input.Value())
	if len(matches) < 3 || matches[0].Cmd != "/resume" || matches[1].Cmd != "/role" || matches[2].Cmd != "/rules" {
		t.Fatalf("matches = %#v, want /resume then /role then /rules", matches)
	}

	_ = m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))

	if backend.resumeCalls != 1 {
		t.Fatalf("ResumeSession() calls = %d, want 1", backend.resumeCalls)
	}
	if got := m.input.Value(); got != "" {
		t.Fatalf("input value after Enter = %q, want empty", got)
	}
}

func TestSlashCompletionEnterSubmitsExactCommand(t *testing.T) {
	backend := &sessionControlAgent{}
	m := NewModel(backend)
	m.mode = ModeInsert
	m.input.SetValue("/resume")

	_ = m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))

	if backend.resumeCalls != 1 {
		t.Fatalf("ResumeSession() calls = %d, want 1", backend.resumeCalls)
	}
	if got := m.input.Value(); got != "" {
		t.Fatalf("input value after submit = %q, want empty", got)
	}
}

func TestSlashCompletionRendersCustomCommandScopeInline(t *testing.T) {
	m := NewModelWithSize(&sessionControlAgent{}, 100, 30)
	m.SetCustomCommands([]CustomCommand{{Cmd: "/commit", Desc: "Verify, document, and commit.", Scope: "project"}})

	plain := stripANSI(m.renderSlashCompletionDropdown("/com"))
	if !strings.Contains(plain, "/commit  [project] Verify, document, and commit.") {
		t.Fatalf("slash completion missing inline custom command scope and description:\n%s", plain)
	}
	if strings.Contains(plain, "scope: project") {
		t.Fatalf("slash completion should not render custom command scope on a separate line:\n%s", plain)
	}
}

func TestServiceTierShortcutSendsToggleCommand(t *testing.T) {
	backend := &sessionControlAgent{}
	m := NewModel(backend)
	m.mode = ModeInsert

	_ = m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: 'r', Mod: tea.ModCtrl}))
	if len(backend.sentMessages) != 1 || backend.sentMessages[0] != "/tier fast" {
		t.Fatalf("sentMessages = %#v, want [/tier fast]", backend.sentMessages)
	}

	backend.serviceTierEnabled = true
	m.mode = ModeNormal
	_ = m.handleNormalKey(tea.KeyPressMsg(tea.Key{Code: 'r', Mod: tea.ModCtrl}))
	if len(backend.sentMessages) != 2 || backend.sentMessages[1] != "/tier slow" {
		t.Fatalf("sentMessages = %#v, want second /tier slow", backend.sentMessages)
	}
}

func TestServiceTierShortcutRotatesOnlySupportedTiers(t *testing.T) {
	t.Run("standard only and already standard", func(t *testing.T) {
		backend := &sessionControlAgent{supportedServiceTiers: []config.ServiceTier{config.ServiceTierStandard}}
		m := NewModel(backend)
		m.mode = ModeInsert

		_ = m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: 'r', Mod: tea.ModCtrl}))
		if len(backend.sentMessages) != 0 {
			t.Fatalf("sentMessages = %#v, want none", backend.sentMessages)
		}
	})

	t.Run("standard only with unsupported requested tier", func(t *testing.T) {
		backend := &sessionControlAgent{
			serviceTier:           config.ServiceTierFast,
			effectiveServiceTier:  config.ServiceTierStandard,
			supportedServiceTiers: []config.ServiceTier{config.ServiceTierStandard},
		}
		m := NewModel(backend)
		m.mode = ModeInsert

		_ = m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: 'r', Mod: tea.ModCtrl}))
		if len(backend.sentMessages) != 1 || backend.sentMessages[0] != "/tier standard" {
			t.Fatalf("sentMessages = %#v, want [/tier standard]", backend.sentMessages)
		}
	})

	t.Run("standard and fast", func(t *testing.T) {
		backend := &sessionControlAgent{supportedServiceTiers: []config.ServiceTier{config.ServiceTierStandard, config.ServiceTierFast}}
		m := NewModel(backend)
		m.mode = ModeInsert

		_ = m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: 'r', Mod: tea.ModCtrl}))
		if len(backend.sentMessages) != 1 || backend.sentMessages[0] != "/tier fast" {
			t.Fatalf("sentMessages = %#v, want [/tier fast]", backend.sentMessages)
		}

		backend.serviceTier = config.ServiceTierFast
		m.mode = ModeNormal
		_ = m.handleNormalKey(tea.KeyPressMsg(tea.Key{Code: 'r', Mod: tea.ModCtrl}))
		if len(backend.sentMessages) != 2 || backend.sentMessages[1] != "/tier standard" {
			t.Fatalf("sentMessages = %#v, want second /tier standard", backend.sentMessages)
		}
	})

	t.Run("standard and slow skips unsupported fast", func(t *testing.T) {
		backend := &sessionControlAgent{supportedServiceTiers: []config.ServiceTier{config.ServiceTierStandard, config.ServiceTierSlow}}
		m := NewModel(backend)
		m.mode = ModeInsert

		_ = m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: 'r', Mod: tea.ModCtrl}))
		if len(backend.sentMessages) != 1 || backend.sentMessages[0] != "/tier slow" {
			t.Fatalf("sentMessages = %#v, want [/tier slow]", backend.sentMessages)
		}
	})
}

func TestServiceTierShortcutKeepsUnsupportedRequestedTierVisibleUntilSwitched(t *testing.T) {
	backend := &sessionControlAgent{
		serviceTier:           config.ServiceTierFast,
		effectiveServiceTier:  config.ServiceTierStandard,
		supportedServiceTiers: []config.ServiceTier{config.ServiceTierStandard},
	}
	m := NewModel(backend)
	plain := stripANSI(m.renderInfoPanelServiceTierLine(80))
	if !strings.Contains(plain, "tier: fast") {
		t.Fatalf("service tier line = %q, want unsupported requested fast visible", plain)
	}

	_ = m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: 'r', Mod: tea.ModCtrl}))
	if len(backend.sentMessages) != 1 || backend.sentMessages[0] != "/tier standard" {
		t.Fatalf("sentMessages = %#v, want [/tier standard]", backend.sentMessages)
	}
}

func TestYoloShortcutSendsToggleCommand(t *testing.T) {
	backend := &sessionControlAgent{}
	m := NewModel(backend)
	m.mode = ModeInsert

	_ = m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: 'y', Mod: tea.ModCtrl}))
	if len(backend.sentMessages) != 1 || backend.sentMessages[0] != "/yolo on" {
		t.Fatalf("sentMessages = %#v, want [/yolo on]", backend.sentMessages)
	}

	backend.yoloEnabled = true
	m.mode = ModeNormal
	_ = m.handleNormalKey(tea.KeyPressMsg(tea.Key{Code: 'y', Mod: tea.ModCtrl}))
	if len(backend.sentMessages) != 2 || backend.sentMessages[1] != "/yolo off" {
		t.Fatalf("sentMessages = %#v, want second /yolo off", backend.sentMessages)
	}
}

func TestSlashCompletionShowsTierCommandMatchingServiceTierShortcut(t *testing.T) {
	assertCompletionMatchesShortcut := func(t *testing.T, backend *sessionControlAgent, want string) {
		t.Helper()
		m := NewModel(backend)
		matches := m.getSlashCompletions("/t")
		tierMatches := matches[:0]
		for _, match := range matches {
			if strings.HasPrefix(match.Cmd, "/tier") {
				tierMatches = append(tierMatches, match)
			}
		}
		if want == "" {
			if len(tierMatches) != 0 {
				t.Fatalf("tier matches = %#v, want none", tierMatches)
			}
		} else if len(tierMatches) != 1 || tierMatches[0].Cmd != want {
			t.Fatalf("tier matches = %#v, want %s", tierMatches, want)
		}

		m.mode = ModeInsert
		_ = m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: 'r', Mod: tea.ModCtrl}))
		if want == "" {
			if len(backend.sentMessages) != 0 {
				t.Fatalf("sentMessages = %#v, want none", backend.sentMessages)
			}
		} else if len(backend.sentMessages) != 1 || backend.sentMessages[0] != want {
			t.Fatalf("sentMessages = %#v, want [%s]", backend.sentMessages, want)
		}
	}

	t.Run("default standard to fast", func(t *testing.T) {
		assertCompletionMatchesShortcut(t, &sessionControlAgent{}, "/tier fast")
	})

	t.Run("fast to slow", func(t *testing.T) {
		assertCompletionMatchesShortcut(t, &sessionControlAgent{serviceTierEnabled: true}, "/tier slow")
	})

	t.Run("standard only and already standard", func(t *testing.T) {
		assertCompletionMatchesShortcut(t, &sessionControlAgent{supportedServiceTiers: []config.ServiceTier{config.ServiceTierStandard}}, "")
	})

	t.Run("standard only with unsupported requested tier", func(t *testing.T) {
		assertCompletionMatchesShortcut(t, &sessionControlAgent{
			serviceTier:           config.ServiceTierFast,
			effectiveServiceTier:  config.ServiceTierStandard,
			supportedServiceTiers: []config.ServiceTier{config.ServiceTierStandard},
		}, "/tier standard")
	})
}

func TestSlashCompletionShowsFastCommandWhenSubAgentFocused(t *testing.T) {
	m := NewModelWithSize(&sessionControlAgent{}, 100, 30)
	m.focusedAgentID = "sub-1"
	matches := m.getSlashCompletions("/t")
	for _, match := range matches {
		if match.Cmd == "/tier fast" {
			return
		}
	}
	t.Fatalf("matches = %#v, want /tier fast when subagent is focused", matches)
}

func TestSlashCompletionHidesYoloCommandsWhenSubAgentFocused(t *testing.T) {
	m := NewModelWithSize(&sessionControlAgent{}, 100, 30)
	m.focusedAgentID = "sub-1"
	matches := m.getSlashCompletions("/y")
	joined := make([]string, 0, len(matches))
	for _, item := range matches {
		joined = append(joined, item.Cmd)
	}
	got := strings.Join(joined, "\n")
	if strings.Contains(got, "/yolo on") || strings.Contains(got, "/yolo off") {
		t.Fatalf("slash completions = %q, should not show /yolo commands when subagent is focused", got)
	}
}

func TestSlashCompletionShowsYoloCommandForCurrentState(t *testing.T) {
	backend := &sessionControlAgent{}
	m := NewModelWithSize(backend, 100, 30)
	matches := m.getSlashCompletions("/y")
	if len(matches) != 1 || matches[0].Cmd != "/yolo on" {
		t.Fatalf("matches = %#v, want only /yolo on", matches)
	}

	backend.yoloEnabled = true
	matches = m.getSlashCompletions("/y")
	if len(matches) != 1 || matches[0].Cmd != "/yolo off" {
		t.Fatalf("matches = %#v, want only /yolo off", matches)
	}
}

func TestSlashCompletionHidesLoopCommandsWhenSubAgentFocused(t *testing.T) {
	m := NewModelWithSize(&sessionControlAgent{}, 100, 30)
	m.focusedAgentID = "sub-1"
	matches := m.getSlashCompletions("/l")
	joined := make([]string, 0, len(matches))
	for _, item := range matches {
		joined = append(joined, item.Cmd)
	}
	got := strings.Join(joined, "\n")
	if strings.Contains(got, "/loop on") || strings.Contains(got, "/loop off") {
		t.Fatalf("slash completions = %q, should not show /loop commands when subagent is focused", got)
	}
}

func TestSlashCompletionHidesLoopCommandsWhenLoopUnavailable(t *testing.T) {
	backend := &sessionControlAgent{canUseLoopSet: true, canUseLoop: false}
	m := NewModel(backend)
	matches := m.getSlashCompletions("/l")
	joined := make([]string, 0, len(matches))
	for _, item := range matches {
		joined = append(joined, item.Cmd)
	}
	got := strings.Join(joined, "\n")
	if strings.Contains(got, "/loop on") || strings.Contains(got, "/loop off") {
		t.Fatalf("slash completions = %q, should hide /loop commands when Done is unavailable", got)
	}
}

func TestSlashCompletionShowsLoopCommandsForPrefixL(t *testing.T) {
	m := NewModel(nil)
	matches := m.getSlashCompletions("/l")
	joined := make([]string, 0, len(matches))
	for _, item := range matches {
		joined = append(joined, item.Cmd)
	}
	got := strings.Join(joined, "\n")
	if !strings.Contains(got, "/loop on") {
		t.Fatalf("slash completions = %q, want /loop on", got)
	}
	if strings.Contains(got, "/loop off") {
		t.Fatalf("slash completions = %q, should not show /loop off when loop is disabled", got)
	}
}

func TestSlashCompletionShowsLoopOffWhenLoopEnabled(t *testing.T) {
	backend := &sessionControlAgent{loopState: agent.LoopStateExecuting}
	m := NewModel(backend)
	matches := m.getSlashCompletions("/l")
	joined := make([]string, 0, len(matches))
	for _, item := range matches {
		joined = append(joined, item.Cmd)
	}
	got := strings.Join(joined, "\n")
	if !strings.Contains(got, "/loop off") {
		t.Fatalf("slash completions = %q, want /loop off", got)
	}
	if strings.Contains(got, "/loop on") {
		t.Fatalf("slash completions = %q, should not show /loop on when loop is enabled", got)
	}
}

func TestSlashCompletionShowsLoopOnAfterLoopStops(t *testing.T) {
	m := NewModel(&sessionControlAgent{})
	matches := m.getSlashCompletions("/l")
	joined := make([]string, 0, len(matches))
	for _, item := range matches {
		joined = append(joined, item.Cmd)
	}
	got := strings.Join(joined, "\n")
	if !strings.Contains(got, "/loop on") {
		t.Fatalf("slash completions = %q, want /loop on after loop stops", got)
	}
	if strings.Contains(got, "/loop off") {
		t.Fatalf("slash completions = %q, should not show /loop off after loop stops", got)
	}
}
