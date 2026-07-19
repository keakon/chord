package tui

import (
	"fmt"
	"image"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	tea "github.com/keakon/bubbletea/v2"
	"github.com/mattn/go-runewidth"

	"github.com/keakon/chord/internal/agent"
)

// ---------------------------------------------------------------------------
// Session picker state
// ---------------------------------------------------------------------------

// sessionSelectState holds the transient state for the session picker overlay.
type sessionSelectState struct {
	options  []agent.SessionSummary // from ListSessionSummaries()
	selector overlayListSelectorState
	prevMode Mode
	loading  bool
	loadErr  string
	filter   string
	loadSeq  uint64

	filterFocused bool
	filteredIdx   []int
	searchCorpus  []string
}

// sessionSummariesLoadedMsg delivers the result of asynchronously loading
// session summaries from the agent.
type sessionSummariesLoadedMsg struct {
	options []agent.SessionSummary
	err     error
	seq     uint64
}

type sessionSummaryDetailsLoadedMsg struct {
	options []agent.SessionSummary
	seq     uint64
}

type sessionSummaryDetailsLoader interface {
	FillSessionSummaryDetails([]agent.SessionSummary) []agent.SessionSummary
}

type sessionSwitchState struct {
	kind      string
	sessionID string
	startedAt time.Time
}

const sessionSwitchOverlayDelay = 200 * time.Millisecond

const (
	// NOTE: list base row is computed by overlayListSelectorState, so hit-tests
	// should not depend on this constant.
	sessionSelectOverlayChromeRows = 7 // title/blank + filter/blank + hint/blank
)

func (s sessionSwitchState) active() bool {
	return strings.TrimSpace(s.kind) != ""
}

func sessionSwitchLabel(kind, sessionID string) string {
	switch strings.TrimSpace(kind) {
	case "resume":
		if strings.TrimSpace(sessionID) != "" {
			return fmt.Sprintf("↺ Resuming %s...", strings.TrimSpace(sessionID))
		}
		return "↺ Resuming session..."
	case "new":
		return "↺ Starting new session..."
	case "fork":
		return "↺ Forking session..."
	default:
		return "↺ Switching session..."
	}
}

func (m *Model) beginSessionSwitch(kind, sessionID string) {
	kind = strings.TrimSpace(kind)
	m.sessionSwitch = sessionSwitchState{
		kind:      kind,
		sessionID: strings.TrimSpace(sessionID),
		startedAt: time.Now(),
	}
	if kind == "new" || kind == "resume" {
		m.clearAgentErrors()
	}
	m.cachedStatusKey = ""
	m.cachedStatusRender = cachedRenderable{}
}

func (m *Model) clearSessionSwitch() {
	if !m.sessionSwitch.active() {
		return
	}
	m.sessionSwitch = sessionSwitchState{}
	m.cachedStatusKey = ""
	m.cachedStatusRender = cachedRenderable{}
}

func (m *Model) sessionSwitchStatusText(maxWidth int) string {
	if !m.sessionSwitch.active() {
		return ""
	}
	text := sessionSwitchLabel(m.sessionSwitch.kind, m.sessionSwitch.sessionID)
	if maxWidth > 0 && runewidth.StringWidth(text) > maxWidth {
		text = runewidth.Truncate(text, maxWidth, "…")
	}
	iconColor := NeonAccentColor(1800 * time.Millisecond)
	iconStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(iconColor))
	textStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(m.theme.StatusFg))
	if after, ok := strings.CutPrefix(text, "↺ "); ok {
		return iconStyle.Render("↺") + " " + textStyle.Render(after)
	}
	return textStyle.Render(text)
}

func (m *Model) interactionSuppressed() bool {
	return m.startupRestorePending || m.sessionSwitch.active()
}

func (m Model) shouldRenderSessionSwitchOverlay() bool {
	if !m.interactionSuppressed() || !m.sessionSwitch.active() {
		return false
	}
	if m.sessionSwitch.startedAt.IsZero() {
		return false
	}
	return time.Since(m.sessionSwitch.startedAt) >= sessionSwitchOverlayDelay
}

func sessionSwitchOverlayTitle(kind string) string {
	switch strings.TrimSpace(kind) {
	case "resume":
		return "Resuming session"
	case "new":
		return "Starting new session"
	case "fork":
		return "Forking session"
	default:
		return "Switching session"
	}
}

func sessionSwitchOverlaySubtitle(kind string) string {
	if strings.TrimSpace(kind) == "resume" {
		return ""
	}
	return "Please wait a moment"
}

func (m *Model) renderSessionSwitchOverlay(_ image.Rectangle) string {
	if !m.shouldRenderSessionSwitchOverlay() {
		return ""
	}
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(m.theme.StatusFg))
	subtitleStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(m.theme.DimFg))
	iconStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(NeonAccentColor(1800 * time.Millisecond)))
	sessionIDStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(m.theme.DirectoryBorderFg))
	cardStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color(m.theme.SeparatorFg)).
		Background(lipgloss.Color(m.theme.InfoPanelBg)).
		Padding(0, 2)

	headline := lipgloss.JoinHorizontal(lipgloss.Center,
		iconStyle.Render("↺"),
		" ",
		titleStyle.Render(sessionSwitchOverlayTitle(m.sessionSwitch.kind)),
	)
	if sessionID := strings.TrimSpace(m.sessionSwitch.sessionID); sessionID != "" {
		headline = lipgloss.JoinHorizontal(lipgloss.Center,
			headline,
			DimStyle.Render(" · "),
			sessionIDStyle.Render(truncateOneLine(sessionID, 24)),
		)
	}
	bodyLines := []string{headline}
	if subtitle := strings.TrimSpace(sessionSwitchOverlaySubtitle(m.sessionSwitch.kind)); subtitle != "" {
		bodyLines = append(bodyLines, subtitleStyle.Render("  "+subtitle))
	}
	body := lipgloss.JoinVertical(lipgloss.Left, bodyLines...)
	return cardStyle.Render(body)
}

func loadSessionSummariesCmd(a agent.AgentForTUI, seq uint64) tea.Cmd {
	if a == nil {
		return nil
	}
	return func() tea.Msg {
		list, err := a.ListSessionSummaries()
		return sessionSummariesLoadedMsg{options: list, err: err, seq: seq}
	}
}

func loadSessionSummaryDetailsCmd(a agent.AgentForTUI, list []agent.SessionSummary, seq uint64) tea.Cmd {
	loader, ok := a.(sessionSummaryDetailsLoader)
	if !ok || len(list) == 0 {
		return nil
	}
	copyList := append([]agent.SessionSummary(nil), list...)
	return func() tea.Msg {
		return sessionSummaryDetailsLoadedMsg{options: loader.FillSessionSummaryDetails(copyList), seq: seq}
	}
}

// ---------------------------------------------------------------------------
// Opening the picker
// ---------------------------------------------------------------------------

// openSessionSelect opens the session picker. If prefill is non-nil, that list
// is used; otherwise the list is loaded asynchronously from the agent.
func (m *Model) openSessionSelect(prefill []agent.SessionSummary) tea.Cmd {
	if m.agent == nil && prefill == nil {
		return nil
	}
	m.sessionSelect.loadSeq++
	seq := m.sessionSelect.loadSeq

	var (
		list    []agent.SessionSummary
		loading bool
	)
	if prefill != nil {
		list = prefill
		loading = false
	} else {
		list = nil
		loading = true
	}

	m.clearChordState()
	m.sessionDeleteConfirm = sessionDeleteConfirmState{}
	m.sessionSelect = sessionSelectState{
		options:  list,
		prevMode: m.mode,
		loading:  loading,
		loadSeq:  seq,
	}
	m.sessionSelect.selector.list = NewOverlayList(nil, m.sessionSelectMaxVisible())
	if !loading {
		m.rebuildSessionSelectFilteredView(false)
	}
	cmd := m.switchModeWithIME(ModeSessionSelect)
	m.recalcViewportSize()
	var cmds []tea.Cmd
	if cmd != nil {
		cmds = append(cmds, cmd)
	}
	if loading {
		cmds = append(cmds, loadSessionSummariesCmd(m.agent, seq))
	} else if cmd := loadSessionSummaryDetailsCmd(m.agent, list, seq); cmd != nil {
		cmds = append(cmds, cmd)
	}
	if len(cmds) > 0 {
		return tea.Batch(cmds...)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Key handling
// ---------------------------------------------------------------------------

func (m *Model) handleSessionSelectKey(msg tea.KeyMsg) tea.Cmd {
	key := msg.String()

	if m.sessionSelect.filterFocused {
		return m.handleSessionSelectFilterKey(msg)
	}

	if keyMatches(key, m.keyMap.InsertEscape) || key == "esc" {
		prevMode := m.sessionSelect.prevMode
		cmd := m.restoreModeWithIME(prevMode)
		m.recalcViewportSize()
		if prevMode == ModeInsert {
			return tea.Batch(cmd, m.input.Focus())
		}
		return cmd
	}

	switch key {
	case "j", "down":
		if m.sessionSelect.selector.list != nil {
			m.sessionSelect.selector.list.CursorDown()
		}
		return nil
	case "k", "up":
		if m.sessionSelect.selector.list != nil {
			m.sessionSelect.selector.list.CursorUp()
		}
		return nil
	case "enter":
		return m.selectSessionAtCursor()
	case "d":
		return m.openSessionDeleteConfirm()
	case "g":
		// "gg" to top
		if m.sessionSelect.selector.list != nil {
			m.sessionSelect.selector.list.CursorToTop()
		}
		return nil
	case "G":
		if m.sessionSelect.selector.list != nil {
			m.sessionSelect.selector.list.CursorToBottom()
		}
		return nil
	case "/":
		m.setSessionSelectFilterFocused(true)
		return nil
	}

	return nil
}

func (m *Model) handleSessionSelectFilterKey(msg tea.KeyMsg) tea.Cmd {
	key := msg.String()
	if keyMatches(key, m.keyMap.InsertEscape) || key == "esc" {
		hadFilter := m.sessionSelect.filter != ""
		m.sessionSelect.filter = ""
		if hadFilter {
			m.rebuildSessionSelectFilteredView(true)
		}
		m.setSessionSelectFilterFocused(false)
		return nil
	}
	if key == "ctrl+u" {
		if m.sessionSelect.filter == "" {
			return nil
		}
		m.sessionSelect.filter = ""
		m.rebuildSessionSelectFilteredView(true)
		return nil
	}

	switch msg.Key().Code {
	case tea.KeyEnter:
		m.setSessionSelectFilterFocused(false)
		return nil
	case tea.KeyUp:
		if m.sessionSelect.selector.list != nil {
			m.sessionSelect.selector.list.CursorUp()
		}
		return nil
	case tea.KeyDown:
		if m.sessionSelect.selector.list != nil {
			m.sessionSelect.selector.list.CursorDown()
		}
		return nil
	case tea.KeyBackspace:
		if m.sessionSelect.filter == "" {
			return nil
		}
		runes := []rune(m.sessionSelect.filter)
		m.sessionSelect.filter = string(runes[:len(runes)-1])
		m.rebuildSessionSelectFilteredView(true)
		return nil
	case tea.KeySpace:
		m.sessionSelect.filter += " "
		m.rebuildSessionSelectFilteredView(true)
		return nil
	}

	if text := msg.Key().Text; text != "" {
		m.sessionSelect.filter += text
		m.rebuildSessionSelectFilteredView(true)
	}
	return nil
}

func (m *Model) selectSessionAtCursor() tea.Cmd {
	sel, ok := m.sessionSelectCurrentOption()
	if !ok {
		return nil
	}
	prevMode := m.sessionSelect.prevMode
	cmd := m.restoreModeWithIME(prevMode)
	m.recalcViewportSize()
	if m.agent != nil {
		m.beginSessionSwitch("resume", sel.ID)
		m.agent.ResumeSessionID(sel.ID)
	}
	if prevMode == ModeInsert {
		return tea.Batch(cmd, m.input.Focus())
	}
	return cmd
}

// ---------------------------------------------------------------------------
// Render
// ---------------------------------------------------------------------------

func (m *Model) sessionSelectMaxVisible() int {
	maxVisible := max(m.viewport.height*2/3-sessionSelectOverlayChromeRows, 3)
	return maxVisible
}

func sessionSummaryPreview(summary agent.SessionSummary) string {
	if summary.Title != "" {
		return summary.Title
	}
	if summary.OriginalFirstUserMessage != "" {
		return summary.OriginalFirstUserMessage
	}
	if summary.FirstUserMessage != "" && !summary.FirstUserMessageIsCompactionSummary {
		return summary.FirstUserMessage
	}
	if summary.OriginalFirstUserMessage != "" {
		return summary.OriginalFirstUserMessage
	}
	return summary.FirstUserMessage
}

func sessionSelectPreviewText(s agent.SessionSummary) string {
	preview := sessionSummaryPreview(s)
	if preview == "" {
		preview = "(no first message)"
	}
	return strings.ReplaceAll(strings.ReplaceAll(preview, "\r\n", " "), "\n", " ")
}

func sessionSelectItemFor(s agent.SessionSummary) OverlayListItem {
	modStr := s.LastModTime.Format("2006-01-02 15:04")
	countStr := "-"
	if s.MessageCount >= 0 {
		countStr = fmt.Sprintf("%d", s.MessageCount)
	}
	preview := sessionSelectPreviewText(s)
	if s.ForkedFrom != "" {
		preview = fmt.Sprintf("↳ %s · %s", s.ForkedFrom, preview)
	}
	return OverlayListItem{
		ID:    s.ID,
		Label: fmt.Sprintf("%s  %5s  %s", modStr, countStr, preview),
	}
}

// sessionSelectColumnHeader builds the dim column-header row for the session
// picker. The 3-space lead matches OverlayList's cursor gutter and the field
// widths mirror sessionSelectItemFor so the header aligns with the rows.
func sessionSelectColumnHeader() string {
	return DimStyle.Render(fmt.Sprintf("   %-16s  %5s  %s", "Modified", "Msgs", "Preview"))
}

func buildSessionSearchCorpus(options []agent.SessionSummary) []string {
	corpus := make([]string, 0, len(options))
	for _, s := range options {
		parts := []string{strings.ToLower(s.ID), strings.ToLower(sessionSelectPreviewText(s))}
		if s.ForkedFrom != "" {
			parts = append(parts, strings.ToLower(s.ForkedFrom))
		}
		corpus = append(corpus, strings.Join(parts, " "))
	}
	return corpus
}

func filterSessionOptions(corpus []string, query string) []int {
	tokens := strings.Fields(strings.ToLower(query))
	if len(tokens) == 0 {
		indices := make([]int, 0, len(corpus))
		for i := range corpus {
			indices = append(indices, i)
		}
		return indices
	}
	indices := make([]int, 0, len(corpus))
	for i, haystack := range corpus {
		match := true
		for _, token := range tokens {
			if !strings.Contains(haystack, token) {
				match = false
				break
			}
		}
		if match {
			indices = append(indices, i)
		}
	}
	return indices
}

func (m *Model) invalidateSessionSelectDialogCache() {
	m.sessionSelect.selector.renderCacheText = ""
}

func (m *Model) setSessionSelectFilterFocused(v bool) {
	if m.sessionSelect.filterFocused == v {
		return
	}
	m.sessionSelect.filterFocused = v
	m.invalidateSessionSelectDialogCache()
}

func (m *Model) rebuildSessionSelectFilteredView(resetCursor bool) {
	opts := m.sessionSelect.options
	if len(m.sessionSelect.searchCorpus) != len(opts) {
		m.sessionSelect.searchCorpus = buildSessionSearchCorpus(opts)
	}
	m.sessionSelect.filteredIdx = filterSessionOptions(m.sessionSelect.searchCorpus, m.sessionSelect.filter)
	items := make([]OverlayListItem, 0, len(m.sessionSelect.filteredIdx))
	for _, idx := range m.sessionSelect.filteredIdx {
		items = append(items, sessionSelectItemFor(opts[idx]))
	}
	if m.sessionSelect.selector.list == nil {
		m.sessionSelect.selector.list = NewOverlayList(items, m.sessionSelectMaxVisible())
	} else {
		m.sessionSelect.selector.list.SetItems(items)
	}
	if resetCursor && m.sessionSelect.selector.list != nil && len(m.sessionSelect.filteredIdx) > 0 {
		m.sessionSelect.selector.list.SetCursor(0)
	}
	m.invalidateSessionSelectDialogCache()
}

func (m *Model) sessionSelectCurrentOptionIndex() int {
	if len(m.sessionSelect.options) == 0 || m.sessionSelect.selector.list == nil {
		return -1
	}
	cursor := m.sessionSelect.selector.list.CursorAt()
	if cursor < 0 {
		return -1
	}
	if cursor >= len(m.sessionSelect.filteredIdx) {
		// Compatibility fallback for tests that may construct picker state
		// without rebuildSessionSelectFilteredView.
		if strings.TrimSpace(m.sessionSelect.filter) == "" &&
			len(m.sessionSelect.filteredIdx) == 0 &&
			m.sessionSelect.selector.list.Len() == len(m.sessionSelect.options) &&
			cursor < len(m.sessionSelect.options) {
			return cursor
		}
		return -1
	}
	idx := m.sessionSelect.filteredIdx[cursor]
	if idx < 0 || idx >= len(m.sessionSelect.options) {
		return -1
	}
	return idx
}

func (m *Model) sessionSelectCurrentOption() (agent.SessionSummary, bool) {
	idx := m.sessionSelectCurrentOptionIndex()
	if idx < 0 {
		return agent.SessionSummary{}, false
	}
	return m.sessionSelect.options[idx], true
}

func (m *Model) renderSessionSelectFilterLine(innerWidth int) string {
	if innerWidth <= 0 {
		return ""
	}
	total := len(m.sessionSelect.options)
	filtered := len(m.sessionSelect.filteredIdx)
	if filtered == 0 && strings.TrimSpace(m.sessionSelect.filter) == "" && m.sessionSelect.selector.list != nil && m.sessionSelect.selector.list.Len() == total {
		filtered = total
	}
	count := fmt.Sprintf("%d/%d", filtered, total)
	countWidth := runewidth.StringWidth(count)
	if countWidth >= innerWidth {
		return runewidth.Truncate(count, innerWidth, "…")
	}

	filterText := m.sessionSelect.filter
	if m.sessionSelect.filterFocused {
		filterText += "_"
	}
	hintMode := !m.sessionSelect.filterFocused && strings.TrimSpace(m.sessionSelect.filter) == ""
	leftPlain := "filter: "
	if hintMode {
		leftPlain += "(press / to search)"
	} else {
		leftPlain += filterText
	}
	leftBudget := innerWidth - countWidth
	if leftBudget < 1 {
		return runewidth.Truncate(count, innerWidth, "…")
	}
	if runewidth.StringWidth(leftPlain) > leftBudget {
		leftPlain = runewidth.Truncate(leftPlain, leftBudget, "…")
	}
	leftWidth := runewidth.StringWidth(leftPlain)
	gap := max(innerWidth-countWidth-leftWidth, 0)

	leftRendered := leftPlain
	if hintMode {
		leftRendered = DimStyle.Render(leftPlain)
	} else {
		leftRendered = DimStyle.Render("filter: ") + strings.TrimPrefix(leftPlain, "filter: ")
	}
	return leftRendered + strings.Repeat(" ", gap) + DimStyle.Render(count)
}

func (m *Model) renderSessionSelectDialog() string {
	opts := m.sessionSelect.options
	overlayCfg := OverlayConfig{
		Title:    "Sessions",
		Hint:     "j/k or wheel move  g/G jump  enter resume  d delete  / filter  esc cancel",
		MinWidth: 40,
		MaxWidth: 90,
	}
	area := image.Rect(0, 0, m.width, m.height)
	overlayCfg = normalizeOverlayConfig(overlayCfg, area)
	innerWidth := overlayCfg.MaxWidth - 4

	if m.sessionSelect.loading {
		dialog, _ := RenderOverlay(overlayCfg, DimStyle.Render("Loading sessions..."), 1, area)
		return dialog
	}
	if m.sessionSelect.loadErr != "" {
		errMsg := fmt.Sprintf("Failed to load sessions: %s", m.sessionSelect.loadErr)
		dialog, _ := RenderOverlay(overlayCfg, ErrorStyle.Render(errMsg), 1, area)
		return dialog
	}

	if len(opts) == 0 {
		emptyMsg := "No previous sessions to choose from. Start a conversation to create one."
		dialog, _ := RenderOverlay(overlayCfg, DimStyle.Render(emptyMsg), 1, area)
		return dialog
	}
	if m.sessionSelect.selector.list == nil {
		return ""
	}

	maxVisible := m.sessionSelectMaxVisible()
	filterLine := m.renderSessionSelectFilterLine(innerWidth)

	// Special-case: when the filter yields no matches, we show an inline message
	// instead of the list.
	if len(m.sessionSelect.filteredIdx) == 0 && strings.TrimSpace(m.sessionSelect.filter) != "" {
		query := m.sessionSelect.filter
		maxQueryWidth := max(innerWidth-runewidth.StringWidth(`No sessions match ""`), 8)
		if runewidth.StringWidth(query) > maxQueryWidth {
			query = runewidth.Truncate(query, maxQueryWidth, "…")
		}
		listBody := DimStyle.Render(fmt.Sprintf(`No sessions match %q`, query))
		content := filterLine + "\n\n" + listBody
		dialog, _ := RenderOverlay(overlayCfg, content, lipgloss.Height(content), area)
		return dialog
	}

	header := sessionSelectColumnHeader()
	prefix := filterLine + "\n\n" + header
	extraKey := "filter=" + filterLine
	return m.sessionSelect.selector.Render(
		m,
		overlayCfg,
		prefix,
		0,
		maxVisible,
		extraKey,
		nil,
		area,
	)
}
