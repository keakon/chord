package tui

import (
	"strings"
	"testing"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/analytics"
	"github.com/keakon/chord/internal/message"
)

func TestRenderStatusBarShowsRulesMode(t *testing.T) {
	m := NewModelWithSize(nil, 140, 24)
	m.mode = ModeRules
	m.layout = m.generateLayout(m.width, m.height)
	plain := stripANSI(m.renderStatusBar())
	if !strings.Contains(plain, "RULES") {
		t.Fatalf("status bar should include RULES pill, got %q", plain)
	}
}

func TestRenderStatusBarShowsSessionIDOnRight(t *testing.T) {
	backend := &sessionControlAgent{sessionSummary: &agent.SessionSummary{ID: "1775115074902"}}
	m := NewModelWithSize(backend, 180, 24)
	m.workingDir = "/home/user/projects/myapp"
	m.layout = m.generateLayout(m.width, m.height)

	got := stripANSI(m.renderStatusBar())
	if !strings.Contains(got, "SID 1775115074902") {
		t.Fatalf("status bar should show prefixed session id, got %q", got)
	}
	if m.statusSession.display == "" || m.statusSession.value != "1775115074902" {
		t.Fatalf("status session region = %+v, want visible session id", m.statusSession)
	}
}

func TestRenderStatusBarHidesSessionIDWhenNarrow(t *testing.T) {
	backend := &sessionControlAgent{sessionSummary: &agent.SessionSummary{ID: "1775115074902"}}
	m := NewModelWithSize(backend, 70, 24)
	m.layout = m.generateLayout(m.width, m.height)

	got := stripANSI(m.renderStatusBar())
	if strings.Contains(got, "1775115074902") {
		t.Fatalf("status bar should hide session id when narrow, got %q", got)
	}
	if m.statusSession.display != "" {
		t.Fatalf("status session region should be hidden, got %+v", m.statusSession)
	}
}

func TestRenderStatusBarUsesForegroundOnlyStatusElements(t *testing.T) {
	m := NewModel(nil)
	m.width = 140
	m.workingDir = "/home/user/projects/myapp"

	got := m.renderStatusBar()
	if m.statusPath.display == "" {
		t.Fatal("status path should be rendered")
	}

	plain := stripANSI(got)
	if !strings.Contains(plain, m.statusPath.display) {
		t.Fatalf("status bar plain text %q does not contain path %q", plain, m.statusPath.display)
	}

	pathSegment := StatusBarPathStyle.Render(m.statusPath.display)
	if !strings.Contains(got, pathSegment) {
		t.Fatalf("status bar should include styled path segment %q; got %q", pathSegment, got)
	}

	if strings.Contains(got, "48;5;") {
		t.Fatalf("status bar should not include background ANSI sequences; got %q", got)
	}
}

func TestRenderStatusBarCentersActivityAwayFromPath(t *testing.T) {
	m := NewModel(nil)
	m.width = 180
	m.workingDir = "/home/user/projects/myapp"
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityStreaming, AgentID: "main"}

	got := stripANSI(m.renderStatusBar())
	path := displayWorkingDir(m.workingDir)
	found := strings.Contains(got, path)
	if !found {
		t.Fatalf("status bar should include path %q; got %q", path, got)
	}
	if strings.Contains(got, statusBarActivityPathGap+path) {
		t.Fatalf("path should not be directly joined to activity gap %q; got %q", statusBarActivityPathGap, got)
	}
}

func TestRenderStatusBarOmitsShortHelp(t *testing.T) {
	m := NewModel(nil)
	m.width = 140

	got := stripANSI(m.renderStatusBar())
	if strings.Contains(got, "esc: normal") || strings.Contains(got, "enter: send/continue") {
		t.Fatalf("status bar should not render short help hints; got %q", got)
	}
}

func TestFormatContextPillOmitsZeroCurrent(t *testing.T) {
	if got := formatContextPill(0, 0); got != "" {
		t.Fatalf("formatContextPill(0,0) = %q, want empty", got)
	}
	if got := formatContextPill(0, 128_000); got != "" {
		t.Fatalf("formatContextPill(0,limit) = %q, want empty", got)
	}
	if got := formatContextPill(1000, 128_000); got == "" || !strings.Contains(got, "1.0k") {
		t.Fatalf("formatContextPill(1000,128000) = %q, want non-empty with token count", got)
	}
}

// When the right panel is hidden (narrow terminal), the status bar mirrors info-panel metrics;
// zero token IO and zero cost should not consume space. Scroll-position pills are not shown.
func TestNarrowStatusBarOmitsZeroUsageAndBottomLabel(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.mode = ModeNormal
	m.updateRightPanelVisible()
	if m.rightPanelVisible {
		t.Fatal("rightPanelVisible should be false at width 80")
	}

	plain := stripANSI(m.renderStatusBar())
	if strings.Contains(plain, "$0.00") {
		t.Fatalf("status bar should omit zero cost; got %q", plain)
	}
	if strings.Contains(plain, "↓0") || strings.Contains(plain, "↑0") {
		t.Fatalf("status bar should omit zero token IO; got %q", plain)
	}

	m.viewport.totalLines = 100
	m.viewport.height = 10
	m.viewport.offset = 90 // scrolled to bottom
	plain = stripANSI(m.renderStatusBar())
	if strings.Contains(plain, "BOT") {
		t.Fatalf("status bar should omit BOT at bottom; got %q", plain)
	}

}

func TestNarrowStatusBarTokenPillMatchesInfoPanelSemantics(t *testing.T) {
	backend := &sessionControlAgent{
		providerModelRef: "anthropic/claude-opus-4.7",
		tokenUsage:       message.TokenUsage{InputTokens: 29_900_000, OutputTokens: 143_100},
		sidebarUsage:     analytics.SessionStats{EstimatedCost: 1.2345},
	}
	m := NewModelWithSize(backend, 80, 24)
	m.mode = ModeNormal
	m.updateRightPanelVisible()
	if m.rightPanelVisible {
		t.Fatal("rightPanelVisible should be false at width 80")
	}

	plain := stripANSI(m.renderStatusBar())
	if !(strings.Contains(plain, "↑ 29.9M  ↓ 143.1k") || strings.Contains(plain, "↑ 29.9M")) {
		t.Fatalf("status bar token pill should keep compact client-view formatting and at least the input side visible; got %q", plain)
	}
	if !strings.Contains(plain, "$1.") {
		t.Fatalf("status bar should keep the cost pill present in narrow layouts; got %q", plain)
	}
}

func TestRenderAnimatedInputSeparatorUsesBusyColorsWhenAgentActive(t *testing.T) {
	m := NewModel(nil)
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityStreaming, AgentID: "main"}

	got := m.renderAnimatedInputSeparator(24)
	plain := stripANSI(got)
	if plain != strings.Repeat(SectionSeparator, 24) {
		t.Fatalf("separator plain text = %q, want %q", plain, strings.Repeat(SectionSeparator, 24))
	}
	if !strings.Contains(got, "38;5;") && !strings.Contains(got, "38;2;") {
		t.Fatalf("busy separator should include ANSI foreground styling; got %q", got)
	}
	if got == InputSeparatorStyle.Render(strings.Repeat(SectionSeparator, 24)) {
		t.Fatalf("busy separator should differ from static insert separator; got %q", got)
	}
}

func TestRenderAnimatedInputSeparatorUsesDimmedStyleWhenIdle(t *testing.T) {
	m := NewModel(nil)
	m.mode = ModeNormal

	got := m.renderAnimatedInputSeparator(12)
	want := InputSeparatorDimmedStyle.Render(strings.Repeat(SectionSeparator, 12))
	if got != want {
		t.Fatalf("idle separator = %q, want %q", got, want)
	}
}

func TestRenderAnimatedInputSeparatorSetThemeInvalidatesCache(t *testing.T) {
	m := NewModel(nil)
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityStreaming, AgentID: "main"}
	m.theme = DefaultTheme()

	first := m.renderAnimatedInputSeparator(24)
	if first == "" {
		t.Fatal("expected initial separator render to be non-empty")
	}
	if m.cachedSepTheme != "dark" {
		t.Fatalf("cached separator theme = %q, want dark", m.cachedSepTheme)
	}
	if m.cachedSepResult == "" {
		t.Fatal("expected cachedSepResult to be populated after render")
	}

	// SetTheme must clear the separator cache — this is the single
	// invalidation hook callers rely on whenever the palette is reloaded.
	m.SetTheme(DefaultTheme())
	if m.cachedSepTheme != "" || m.cachedSepResult != "" || m.cachedSepFrame != 0 {
		t.Fatalf("SetTheme should clear separator cache, got theme=%q frame=%d result=%q", m.cachedSepTheme, m.cachedSepFrame, m.cachedSepResult)
	}

	if second := m.renderAnimatedInputSeparator(24); second == "" {
		t.Fatal("expected separator render after SetTheme to be non-empty")
	}
	if m.cachedSepResult == "" {
		t.Fatal("expected separator render to repopulate the cache")
	}
}
