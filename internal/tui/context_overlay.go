package tui

import (
	"image"
	"strings"
	"time"

	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/agent"
)

type contextOverlayTickMsg struct{ generation uint64 }
type contextOverlayState struct {
	renderScroll        int
	polling             bool
	prevMode            Mode
	generation          uint64
	page, scroll        int
	view                agent.ContextDiagnosticsView
	target              string
	cacheKey, cacheText string
	lines               []string
}

func (m *Model) readContextDiagnostics() agent.ContextDiagnosticsView {
	if provider, ok := m.agent.(agent.ContextDiagnosticsProvider); ok {
		return provider.FocusedContextDiagnostics()
	}
	return agent.ContextDiagnosticsView{UnavailableReason: "unsupported"}
}
func (m *Model) refreshContextOverlay() {
	v := agent.ContextDiagnosticsView{UnavailableReason: "not_observed"}
	if !m.sessionSwitch.active() {
		v = m.readContextDiagnostics()
	}
	target := m.focusedAgentID + "/" + v.Snapshot.ID.Binding.SessionID + "/" + v.Snapshot.ID.Binding.TaskID
	if target != m.contextOverlay.target || v.Snapshot.ID.Binding.Epoch != m.contextOverlay.view.Snapshot.ID.Binding.Epoch {
		m.contextOverlay.scroll = 0
		m.contextOverlay.cacheKey = ""
		m.contextOverlay.cacheText = ""
	}
	m.contextOverlay.target = target
	m.contextOverlay.view = v
}
func (m *Model) contextOverlayTick() tea.Cmd {
	generation := m.contextOverlay.generation
	return tea.Tick(250*time.Millisecond, func(time.Time) tea.Msg { return contextOverlayTickMsg{generation} })
}
func (m *Model) openContextOverlay() tea.Cmd {
	if m.mode == ModeContext {
		return nil
	}
	previous := m.mode
	m.clearActiveSearch()
	m.clearChordState()
	m.input.Blur()
	m.contextOverlay = contextOverlayState{prevMode: previous, generation: m.contextOverlay.generation + 1, polling: true}
	m.mode = ModeContext
	m.refreshContextOverlay()
	return m.contextOverlayTick()
}
func (m *Model) closeContextOverlay() tea.Cmd {
	previous := m.contextOverlay.prevMode
	m.contextOverlay = contextOverlayState{generation: m.contextOverlay.generation + 1}
	cmd := m.restoreModeWithIME(previous)
	if previous == ModeInsert {
		return tea.Batch(cmd, m.input.Focus())
	}
	return cmd
}
func (m *Model) handleContextOverlayKey(msg tea.KeyMsg) tea.Cmd {
	if cmd := m.maybeExportDiagnosticsShortcut(msg.String()); cmd != nil {
		return cmd
	}
	old := m.contextOverlay.page
	switch msg.String() {
	case "esc", "q":
		return m.closeContextOverlay()
	case "tab":
		m.contextOverlay.page = (old + 1) % 3
	case "shift+tab":
		m.contextOverlay.page = (old + 2) % 3
	case "1":
		m.contextOverlay.page = 0
	case "2":
		m.contextOverlay.page = 1
	case "3":
		m.contextOverlay.page = 2
	case "j", "down":
		m.contextOverlay.scroll++
	case "k", "up":
		m.contextOverlay.scroll--
	case "pgdown", "ctrl+f":
		m.contextOverlay.scroll += max(m.height-8, 1)
	case "pgup", "ctrl+b":
		m.contextOverlay.scroll -= max(m.height-8, 1)
	case "g", "home":
		m.contextOverlay.scroll = 0
	case "G", "end":
		m.contextOverlay.scroll = 1 << 20
	}
	if old != m.contextOverlay.page {
		m.contextOverlay.scroll = 0
	}
	return nil
}
func (m *Model) contextOverlayConfig() OverlayConfig {
	return OverlayConfig{Title: "Context · 上下文诊断", MaxWidth: max(min(m.width-4, 110), 1), Hint: "Tab / 1·2·3 切页 · ↑↓ 滚动 · Esc 关闭"}
}
func (m *Model) renderContextOverlayDialog() string {
	o := &m.contextOverlay
	key := contextRenderKey(o, m.width, m.height, m.theme.Name)
	if key != o.cacheKey {
		o.lines = contextDiagnosticLines(o.view, o.page, max(dialogContentWidth(m.contextOverlayConfig().MaxWidth), 1))
		o.cacheKey = key
		o.cacheText = ""
	}
	visible := max(overlayScrollContentHeight(m.contextOverlayConfig(), image.Rect(0, 0, m.width, m.height), len(o.lines)), 1)
	o.scroll = max(min(o.scroll, max(len(o.lines)-visible, 0)), 0)
	if o.cacheText != "" && o.renderScroll == o.scroll {
		return o.cacheText
	}
	content := strings.Join(o.lines[o.scroll:min(o.scroll+visible, len(o.lines))], "\n")
	dialog, _ := RenderOverlay(m.contextOverlayConfig(), content, image.Rect(0, 0, m.width, m.height))
	o.cacheText = dialog
	o.renderScroll = o.scroll
	return dialog
}
