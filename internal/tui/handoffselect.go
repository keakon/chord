package tui

import (
	"fmt"
	"image"
	"os"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/keakon/bubbles/v2/textarea"
	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/agent"
)

// ---------------------------------------------------------------------------
// Handoff agent selector state
// ---------------------------------------------------------------------------

const handoffOverlayMaxWidth = 70

// handoffOverlayContentWidth returns the content width inside the Handoff
// overlay frame. renderHandoffSelectDialog renders through RenderOverlay with
// MaxWidth handoffOverlayMaxWidth (clamped to the terminal width), whose inner
// content width is MaxWidth-4. The deny-reason textarea must be sized with
// this same width so its already wrapped lines are not re-wrapped (and
// possibly split mid-character) by the overlay border render.
func handoffOverlayContentWidth(width int) int {
	return max(min(handoffOverlayMaxWidth, width)-4, 1)
}

// handoffOption describes a single agent entry in the Handoff selector.
type handoffOption struct {
	Name        string // agent name (e.g. "builder")
	Description string // human-readable description
	IsDefault   bool   // true if this is the default target (builder)
}

// handoffSelectState holds the transient state for the Handoff agent selector.
type handoffSelectState struct {
	options   []handoffOption
	planPath  string
	requestID string
	prevMode  Mode
	planText  string
	planErr   string
	scroll    int

	selector          overlayListSelectorState
	denyingWithReason bool
	denyReasonInput   textarea.Model
	error             string
}

// ---------------------------------------------------------------------------
// Opening the selector
// ---------------------------------------------------------------------------

// openHandoffSelect opens the Handoff agent selection dialog. requestID
// correlates the user's decision back to the pending handoff wait, allowing
// the runtime to settle the user-wait and emit the deferred tool result.
func (m *Model) openHandoffSelect(planPath, requestID string) {
	if m.agent == nil {
		return
	}

	// Build options from available agent configs. The runtime already excludes
	// the current role; when nothing remains there is no legal target, so the
	// handoff is cancelled instead of offering an ineligible fallback.
	agentNames := m.agent.AvailableAgents()
	if len(agentNames) == 0 {
		m.enqueueToast("No eligible Handoff target: the current role cannot hand off to itself", "warn")
		if requestID = strings.TrimSpace(requestID); requestID != "" {
			if hr, ok := m.agent.(agent.HandoffResolver); ok {
				hr.ResolveHandoff(requestID, "cancel", "", "")
			}
		}
		return
	}
	options := make([]handoffOption, 0, len(agentNames)+1)
	cursorIdx := 0

	for i, name := range agentNames {
		isDefault := name == "builder"
		options = append(options, handoffOption{
			Name:      name,
			IsDefault: isDefault,
		})
		if isDefault {
			cursorIdx = i
		}
	}

	m.clearChordState()
	m.clearActiveSearch()
	m.handoffSelect = handoffSelectState{
		options:   options,
		planPath:  planPath,
		requestID: requestID,
		prevMode:  m.mode,
	}
	if content, err := os.ReadFile(planPath); err == nil {
		m.handoffSelect.planText = string(content)
	} else {
		m.handoffSelect.planErr = err.Error()
	}
	m.handoffSelect.selector.list = NewOverlayList(handoffItems(options), m.handoffSelectMaxVisible())
	if m.handoffSelect.selector.list != nil {
		m.handoffSelect.selector.list.SetCursor(cursorIdx)
	}
	if m.mode == ModeInsert {
		m.input.Blur()
	}
	m.mode = ModeHandoffSelect
	m.recalcViewportSize()
}

// ---------------------------------------------------------------------------
// Key handling
// ---------------------------------------------------------------------------

// handleHandoffSelectKey processes keyboard input for the Handoff agent selector.
func (m *Model) handleHandoffSelectKey(msg tea.KeyMsg) tea.Cmd {
	if m.handoffSelect.denyingWithReason {
		return m.handleHandoffDenyReasonKey(msg)
	}
	key := msg.String()

	switch key {
	case "esc":
		return m.closeHandoffSelect()

	case "j", "down":
		if m.handoffSelect.selector.list != nil {
			m.handoffSelect.selector.list.CursorDown()
		}

	case "k", "up":
		if m.handoffSelect.selector.list != nil {
			m.handoffSelect.selector.list.CursorUp()
		}

	case "g":
		if m.handoffSelect.selector.list != nil {
			m.handoffSelect.selector.list.CursorToTop()
		}

	case "G":
		if m.handoffSelect.selector.list != nil {
			m.handoffSelect.selector.list.CursorToBottom()
		}

	case "v", "V":
		return m.openContentViewer("Handoff plan", m.handoffFullPlanContent())

	case "enter", "a", "A":
		return m.confirmHandoff()
	case "r", "R":
		m.handoffSelect.denyingWithReason = true
		m.handoffSelect.denyReasonInput = newDialogTextarea(handoffOverlayContentWidth(m.width), confirmEditMinHeight, confirmEditHeight(m.height), "")
		m.handoffSelect.error = ""
		m.recalcViewportSize()
	}

	return nil
}

func (m *Model) handleHandoffDenyReasonKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "enter":
		reason := strings.TrimSpace(m.handoffSelect.denyReasonInput.Value())
		if reason == "" {
			m.handoffSelect.error = "Handoff rejection requires a reason."
			m.recalcViewportSize()
			return nil
		}
		return m.denyHandoffWithReason(reason)
	case "esc":
		m.handoffSelect.denyingWithReason = false
		m.handoffSelect.denyReasonInput.Blur()
		m.handoffSelect.error = ""
		m.recalcViewportSize()
		return nil
	case "v", "V":
		return m.openContentViewer("Handoff plan", m.handoffFullPlanContent())
	default:
		m.handoffSelect.error = ""
		var cmd tea.Cmd
		m.handoffSelect.denyReasonInput, cmd = m.handoffSelect.denyReasonInput.Update(msg)
		return cmd
	}
}

func (m *Model) closeHandoffSelect() tea.Cmd {
	prevMode := m.handoffSelect.prevMode
	// Esc cancels the handoff without executing the plan. The pending
	// interaction is settled so the card gets a cancelled terminal result and
	// the user-wait is recorded.
	if requestID := strings.TrimSpace(m.handoffSelect.requestID); requestID != "" && m.agent != nil {
		if hr, ok := m.agent.(agent.HandoffResolver); ok {
			hr.ResolveHandoff(requestID, "cancel", "", "")
		}
	}
	cmd := m.restoreModeWithIME(prevMode)
	m.recalcViewportSize()
	if prevMode == ModeInsert {
		return tea.Batch(cmd, m.input.Focus())
	}
	return cmd
}

func (m *Model) denyHandoffWithReason(reason string) tea.Cmd {
	prevMode := m.handoffSelect.prevMode
	cmd := m.restoreModeWithIME(prevMode)
	m.recalcViewportSize()
	if requestID := strings.TrimSpace(m.handoffSelect.requestID); requestID != "" && m.agent != nil {
		if hr, ok := m.agent.(agent.HandoffResolver); ok {
			hr.ResolveHandoff(requestID, "deny", "", reason)
		}
	}
	if prevMode == ModeInsert {
		return tea.Batch(cmd, m.input.Focus())
	}
	return cmd
}

func (m *Model) confirmHandoff() tea.Cmd {
	if m.agent == nil || len(m.handoffSelect.options) == 0 {
		return nil
	}
	cursor := 0
	if m.handoffSelect.selector.list != nil {
		cursor = m.handoffSelect.selector.list.CursorAt()
	}
	if cursor < 0 || cursor >= len(m.handoffSelect.options) {
		return nil
	}
	selected := m.handoffSelect.options[cursor]
	planPath := m.handoffSelect.planPath
	requestID := strings.TrimSpace(m.handoffSelect.requestID)

	// Restore mode before triggering execution. The decision is handled by the
	// runtime (settles the user-wait and emits the deferred handoff tool result),
	// then starts plan execution on the selected agent role.
	prevMode := m.handoffSelect.prevMode
	cmd := m.restoreModeWithIME(prevMode)
	m.recalcViewportSize()

	if requestID != "" && m.agent != nil {
		if hr, ok := m.agent.(agent.HandoffResolver); ok {
			hr.ResolveHandoff(requestID, "approve", selected.Name, "")
			if prevMode == ModeInsert {
				return tea.Batch(cmd, m.input.Focus())
			}
			return cmd
		}
	}
	m.agent.ExecutePlan(planPath, selected.Name)
	if prevMode == ModeInsert {
		return tea.Batch(cmd, m.input.Focus())
	}
	return cmd
}

// ---------------------------------------------------------------------------
// Rendering
// ---------------------------------------------------------------------------

func (m *Model) handoffSelectMaxVisible() int {
	maxVisible := max(m.height/2-5, 3)
	return maxVisible
}

func (m *Model) handoffPlanVisibleLines() int {
	visible := min(max(m.height/3-4, 3), 12)
	return visible
}

func (m *Model) handoffPlanRenderedLines(width int) []string {
	if width < 10 {
		width = 10
	}
	if strings.TrimSpace(m.handoffSelect.planErr) != "" {
		return []string{ConfirmDenyStyle.Render("! Failed to read plan: ") + DimStyle.Render(m.handoffSelect.planErr)}
	}
	content := strings.TrimSpace(m.handoffSelect.planText)
	if content == "" {
		return []string{DimStyle.Render("(plan file is empty)")}
	}
	return renderRichMarkdownContent(content, width, nil)
}

func (m *Model) handoffFullPlanContent() string {
	planPath := strings.TrimSpace(m.handoffSelect.planPath)
	if strings.TrimSpace(m.handoffSelect.planErr) != "" {
		if planPath == "" {
			return "Failed to read plan: " + m.handoffSelect.planErr
		}
		return fmt.Sprintf("Plan path: %s\n\nFailed to read plan: %s", planPath, m.handoffSelect.planErr)
	}
	if planPath == "" {
		return m.handoffSelect.planText
	}
	return fmt.Sprintf("Plan path: %s\n\n---\n\n%s", planPath, strings.TrimSpace(m.handoffSelect.planText))
}

func (m *Model) handoffPlanWindow(width int) ([]string, int, int) {
	lines := m.handoffPlanRenderedLines(width)
	visible := m.handoffPlanVisibleLines()
	if visible >= len(lines) {
		m.handoffSelect.scroll = 0
		return lines, 0, len(lines)
	}
	maxScroll := len(lines) - visible
	if m.handoffSelect.scroll < 0 {
		m.handoffSelect.scroll = 0
	}
	if m.handoffSelect.scroll > maxScroll {
		m.handoffSelect.scroll = maxScroll
	}
	start := m.handoffSelect.scroll
	end := start + visible
	return lines[start:end], start, len(lines)
}

func handoffItems(options []handoffOption) []OverlayListItem {
	items := make([]OverlayListItem, 0, len(options))
	for _, opt := range options {
		label := opt.Name
		if opt.IsDefault {
			label += " (default)"
		}
		items = append(items, OverlayListItem{ID: opt.Name, Label: label})
	}
	return items
}

func (m *Model) renderHandoffSelectDialog() string {
	if m.handoffSelect.selector.list == nil {
		return ""
	}

	overlayCfg := OverlayConfig{
		Title:    "Handoff To Agent",
		Hint:     "j/k move  g/G jump  v view plan  enter/a approve  r deny reason  esc close",
		MinWidth: 30,
		MaxWidth: handoffOverlayMaxWidth,
	}
	area := image.Rect(0, 0, m.width, m.height)
	overlayCfg = normalizeOverlayConfig(overlayCfg, area)
	contentWidth := handoffOverlayContentWidth(m.width)
	planLine := DimStyle.Render(ansi.Truncate(fmt.Sprintf("Plan: %s", m.handoffSelect.planPath), contentWidth, "…"))

	planLines, planStart, planTotal := m.handoffPlanWindow(contentWidth)
	planBlock := strings.Join(planLines, "\n")
	if planTotal > len(planLines) {
		planBlock += "\n" + DimStyle.Render(fmt.Sprintf("Plan lines %d-%d of %d — mouse wheel scrolls", planStart+1, planStart+len(planLines), planTotal))
	}
	planPrefix := strings.Join([]string{
		planLine,
		"",
		ConfirmToolStyle.Render("Plan preview:"),
		planBlock,
		"",
		ConfirmEditStyle.Render("[V] View full plan"),
		ConfirmToolStyle.Render("Select model pool / target agent:"),
	}, "\n")

	if m.handoffSelect.denyingWithReason {
		lines := []string{planPrefix, "", ConfirmToolStyle.Render("Deny handoff with reason:")}
		lines = append(lines, strings.Split(m.handoffSelect.denyReasonInput.View(), "\n")...)
		if strings.TrimSpace(m.handoffSelect.error) != "" {
			lines = append(lines, "")
			for _, line := range wrapText(m.handoffSelect.error, max(10, contentWidth-2)) {
				lines = append(lines, ConfirmDenyStyle.Render("! "+line))
			}
		}
		lines = append(lines, "", ConfirmHintStyle.Render("[Enter] Deny  [Shift+Enter/Ctrl+J] New line  [Esc] Back"))
		denyCfg := overlayCfg
		denyCfg.Hint = ""
		box, _ := RenderOverlay(denyCfg, strings.Join(lines, "\n"), len(lines), area)
		return box
	}

	extraKey := fmt.Sprintf("plan=%s scroll=%d total=%d", m.handoffSelect.planPath, m.handoffSelect.scroll, planTotal)
	maxVisible := m.handoffSelectMaxVisible()
	return m.handoffSelect.selector.Render(
		m,
		overlayCfg,
		planPrefix,
		1,
		maxVisible,
		extraKey,
		nil,
		area,
	)
}
