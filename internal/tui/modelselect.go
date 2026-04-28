package tui

import (
	"image"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/config"
)

// ---------------------------------------------------------------------------
// Model selector state and option types
// ---------------------------------------------------------------------------

// ModelSelectOption describes a single model entry in the selector dialog.
type ModelSelectOption struct {
	Label     string // display text (e.g. "claude-opus-4.7")
	Value     string // provider/model reference (e.g. "anthropic-main/claude-opus-4.7")
	Provider  string // provider name/group label
	ModelID   string // model identifier for searching/display
	Context   int    // context window limit
	Output    int    // max output tokens
	IsCurrent bool   // true if this is the currently active model
	Header    bool   // true for non-selectable provider group header rows
}

// modelSelectState holds the transient state for the model selector overlay.
type modelSelectState struct {
	allOptions  []ModelSelectOption
	options     []ModelSelectOption
	table       *OverlayTable
	current     string // currently active model (provider/model)
	prevMode    Mode   // mode to restore on cancel
	searchInput string

	renderCacheWidth       int
	renderCacheHeight      int
	renderCacheMaxVisible  int
	renderCacheSearchInput string
	renderCacheTableVer    uint64
	renderCacheText        string
}

type modelSwitchResultMsg struct {
	err error
}

// ---------------------------------------------------------------------------
// Opening the selector
// ---------------------------------------------------------------------------

// openModelSelect populates the model selector state from the agent's
// available models and enters ModeModelSelect.
func (m *Model) openModelSelect() {
	if m.agent == nil {
		return
	}

	models := m.agent.AvailableModels()
	currentRef := m.agent.ProviderModelRef()
	allOptions, cursorRef := buildModelSelectOptions(models, currentRef, "")
	table := newModelSelectTable(allOptions, m.modelSelectMaxVisible())
	if table != nil {
		table.list.SetCursor(modelSelectCursorIndex(allOptions, cursorRef))
	}

	prevMode := m.mode
	if prevMode == ModeModelSelect {
		// Keep the original non-overlay mode when ModelSelectEvent is emitted repeatedly.
		prevMode = m.modelSelect.prevMode
	}
	m.clearActiveSearch()
	m.clearChordState()
	m.modelSelect = modelSelectState{
		allOptions: allOptions,
		options:    allOptions,
		table:      table,
		current:    currentRef,
		prevMode:   prevMode,
	}
	if m.mode == ModeInsert {
		m.input.Blur()
	}
	m.mode = ModeModelSelect
	m.recalcViewportSize()
}

// ---------------------------------------------------------------------------
// Key handling
// ---------------------------------------------------------------------------

// handleModelSelectKey processes keyboard input while the model selector is
// open. j/k or up/down navigate, enter selects, esc cancels.
func (m *Model) handleModelSelectKey(msg tea.KeyMsg) tea.Cmd {
	key := msg.String()

	if keyMatches(key, m.keyMap.SwitchModel) || key == "esc" {
		// Cancel — restore previous mode.
		prevMode := m.modelSelect.prevMode
		cmd := m.restoreModeWithIME(prevMode)
		m.recalcViewportSize()
		if prevMode == ModeInsert {
			return tea.Batch(cmd, m.input.Focus())
		}
		return cmd
	}
	if key == "ctrl+d" {
		prevMode := m.modelSelect.prevMode
		cmd := m.restoreModeWithIME(prevMode)
		m.recalcViewportSize()
		if prevMode == ModeInsert {
			return tea.Batch(cmd, m.input.Focus())
		}
		return cmd
	}

	switch key {
	case "j", "down":
		if m.modelSelect.table != nil {
			m.modelSelect.table.CursorDown()
		}

	case "k", "up":
		if m.modelSelect.table != nil {
			m.modelSelect.table.CursorUp()
		}

	case "g":
		if m.modelSelect.table != nil {
			m.modelSelect.table.CursorToTop()
		}

	case "G":
		if m.modelSelect.table != nil {
			m.modelSelect.table.CursorToBottom()
		}

	case "enter":
		return m.selectModelAtCursor()
	default:
		if m.handleModelSelectSearchKey(msg.Key()) {
			return nil
		}
	}

	return nil
}

func (m *Model) selectModelAtCursor() tea.Cmd {
	cursor := 0
	if m.modelSelect.table != nil {
		cursor = m.modelSelect.table.CursorAt()
	}
	var switchCmd tea.Cmd
	if cursor >= 0 && cursor < len(m.modelSelect.options) {
		selected := m.modelSelect.options[cursor]
		if !selected.Header && !selected.IsCurrent && m.agent != nil {
			if m.isAgentBusy() {
				block := &Block{
					ID:      m.nextBlockID,
					Type:    BlockStatus,
					Content: "Agent busy, cancel current turn before switching model",
				}
				m.nextBlockID++
				m.appendViewportBlock(block)
				m.markBlockSettled(block)
			} else {
				switchCmd = m.switchModelCmd(selected.Value)
			}
		}
	}
	// Restore previous mode.
	prevMode := m.modelSelect.prevMode
	cmd := m.restoreModeWithIME(prevMode)
	m.recalcViewportSize()
	if prevMode == ModeInsert {
		if switchCmd != nil {
			return tea.Batch(cmd, m.input.Focus(), switchCmd)
		}
		return tea.Batch(cmd, m.input.Focus())
	}
	if switchCmd != nil {
		return tea.Batch(cmd, switchCmd)
	}
	return cmd
}

func (m *Model) switchModelCmd(value string) tea.Cmd {
	ag := m.agent
	return func() tea.Msg {
		if ag == nil {
			return nil
		}
		return modelSwitchResultMsg{err: ag.SwitchModel(value)}
	}
}

// modelOptionMatchesCurrent reports whether mo should be treated as the current
// selection. Exact provider/model@variant matches win; a base-ref fallback is
// allowed only when no exact variant option exists in the list.
func modelOptionMatchesCurrent(mo agent.ModelOption, currentRef string) bool {
	if mo.ProviderModel == currentRef {
		return true
	}
	mb, mv := config.ParseModelRef(mo.ProviderModel)
	cb, cv := config.ParseModelRef(currentRef)
	if mb != cb {
		return false
	}
	if mv == cv {
		return true
	}
	return false
}

func preferredCurrentProvider(models []agent.ModelOption, currentRef string) string {
	for _, mo := range models {
		if mo.ProviderModel == currentRef {
			return mo.ProviderName
		}
	}
	for _, mo := range models {
		mb, _ := config.ParseModelRef(mo.ProviderModel)
		cb, _ := config.ParseModelRef(currentRef)
		if mb == cb {
			return mo.ProviderName
		}
	}
	return ""
}

func buildModelSelectLabel(mo agent.ModelOption) string {
	if _, variant := config.ParseModelRef(mo.ProviderModel); variant != "" {
		return mo.ModelID + "@" + variant
	}
	return mo.ModelID
}

func buildModelSelectOptions(models []agent.ModelOption, currentRef, query string) ([]ModelSelectOption, string) {
	if len(models) == 0 {
		return nil, ""
	}
	query = strings.TrimSpace(strings.ToLower(query))

	currentProvider := preferredCurrentProvider(models, currentRef)
	hasExactCurrent := false
	currentBaseRef, _ := config.ParseModelRef(currentRef)
	for _, mo := range models {
		if mo.ProviderModel == currentRef {
			hasExactCurrent = true
			break
		}
	}

	options := make([]ModelSelectOption, 0, len(models)+4)
	cursorRef := ""
	hasVisibleCurrentProvider := false

	lastProvider := ""
	for _, mo := range models {
		if !modelSelectMatchesQuery(mo, query) {
			continue
		}
		if mo.ProviderName != lastProvider {
			headerLabel := mo.ProviderName
			options = append(options, ModelSelectOption{
				Label:    headerLabel,
				Provider: mo.ProviderName,
				Header:   true,
			})
			lastProvider = mo.ProviderName
		}
		isCurrent := modelOptionMatchesCurrent(mo, currentRef)
		if !isCurrent && !hasExactCurrent {
			mb, _ := config.ParseModelRef(mo.ProviderModel)
			if mb == currentBaseRef {
				isCurrent = true
			}
		}
		if isCurrent {
			hasVisibleCurrentProvider = true
		}
		opt := ModelSelectOption{
			Label:     buildModelSelectLabel(mo),
			Value:     mo.ProviderModel,
			Provider:  mo.ProviderName,
			ModelID:   mo.ModelID,
			Context:   mo.ContextLimit,
			Output:    mo.OutputLimit,
			IsCurrent: isCurrent,
		}
		options = append(options, opt)
		if isCurrent && cursorRef == "" {
			cursorRef = mo.ProviderModel
		}
	}

	if cursorRef == "" && currentProvider != "" && !hasVisibleCurrentProvider {
		for _, opt := range options {
			if !opt.Header && opt.Provider == currentProvider {
				cursorRef = opt.Value
				break
			}
		}
	}
	return options, cursorRef
}

func modelSelectMatchesQuery(mo agent.ModelOption, query string) bool {
	if query == "" {
		return true
	}
	haystacks := []string{
		strings.ToLower(mo.ModelID),
		strings.ToLower(mo.ProviderName),
		strings.ToLower(mo.ProviderModel),
	}
	for _, h := range haystacks {
		if strings.Contains(h, query) {
			return true
		}
	}
	return false
}

func modelSelectCursorIndex(options []ModelSelectOption, preferredRef string) int {
	firstSelectable := -1
	preferredIdx := -1
	for i, opt := range options {
		if opt.Header {
			continue
		}
		if firstSelectable == -1 {
			firstSelectable = i
		}
		if preferredRef != "" && opt.Value == preferredRef {
			preferredIdx = i
			break
		}
	}
	if preferredIdx >= 0 {
		return preferredIdx
	}
	if firstSelectable >= 0 {
		return firstSelectable
	}
	return 0
}

func newModelSelectTable(options []ModelSelectOption, maxVisible int) *OverlayTable {
	tableItems := make([]OverlayTableItem, 0, len(options))
	for _, opt := range options {
		if opt.Header {
			tableItems = append(tableItems, OverlayTableItem{
				OverlayListItem: OverlayListItem{
					Label:    opt.Label,
					Disabled: true,
					Header:   true,
				},
				Cells: []string{opt.Label},
			})
			continue
		}
		status := ""
		if opt.IsCurrent {
			status = "✓"
		}
		tableItems = append(tableItems, OverlayTableItem{
			OverlayListItem: OverlayListItem{
				ID:       opt.Value,
				Label:    opt.Label,
				Selected: opt.IsCurrent,
			},
			Cells: []string{
				opt.Label,
				formatTokens(opt.Context),
				formatTokens(opt.Output),
				status,
			},
		})
	}
	return NewOverlayTable([]TableColumn{
		{Title: "Model", Width: 0, Align: 0},
		{Title: "Context", Width: 8, Align: 1},
		{Title: "Output", Width: 8, Align: 1},
		{Title: "Use", Width: 3, Align: 2},
	}, tableItems, maxVisible)
}

func (m *Model) refreshModelSelectOptions() {
	options, cursorRef := buildModelSelectOptions(m.agent.AvailableModels(), m.modelSelect.current, m.modelSelect.searchInput)
	m.modelSelect.allOptions = options
	m.modelSelect.options = options
	m.modelSelect.table = newModelSelectTable(options, m.modelSelectMaxVisible())
	if m.modelSelect.table != nil {
		m.modelSelect.table.list.SetCursor(modelSelectCursorIndex(options, cursorRef))
	}
}

func (m *Model) handleModelSelectSearchKey(key tea.Key) bool {
	switch key.Code {
	case tea.KeyBackspace:
		if m.modelSelect.searchInput == "" {
			return false
		}
		runes := []rune(m.modelSelect.searchInput)
		m.modelSelect.searchInput = string(runes[:len(runes)-1])
		m.refreshModelSelectOptions()
		return true
	case tea.KeySpace:
		m.modelSelect.searchInput += " "
		m.refreshModelSelectOptions()
		return true
	}
	if key.Text == "" {
		return false
	}
	m.modelSelect.searchInput += key.Text
	m.refreshModelSelectOptions()
	return true
}

func (m *Model) modelSelectMaxVisible() int {
	maxVisible := m.height/2 - 6
	if maxVisible < 3 {
		maxVisible = 3
	}
	return maxVisible
}

// renderModelSelectDialog renders the model selector as a bordered overlay
// in the input area, following the pattern of the directory overlay.
func (m *Model) renderModelSelectDialog() string {
	if len(m.modelSelect.allOptions) == 0 {
		dialog, _ := RenderOverlay(OverlayConfig{
			Title:    "Model Selector",
			Hint:     "type to search  g/G jump  enter select  esc cancel",
			MinWidth: 40,
			MaxWidth: 90,
		}, DimStyle.Render("(no models configured)"), 1, image.Rect(0, 0, m.width, m.height))
		return dialog
	}

	if m.modelSelect.table == nil {
		return ""
	}
	maxVisible := m.modelSelectMaxVisible()
	m.modelSelect.table.SetMaxVisible(maxVisible)
	tableVersion := m.modelSelect.table.RenderVersion()
	if m.modelSelect.renderCacheText != "" &&
		m.modelSelect.renderCacheWidth == m.width &&
		m.modelSelect.renderCacheHeight == m.height &&
		m.modelSelect.renderCacheMaxVisible == maxVisible &&
		m.modelSelect.renderCacheSearchInput == m.modelSelect.searchInput &&
		m.modelSelect.renderCacheTableVer == tableVersion {
		return m.modelSelect.renderCacheText
	}
	overlayCfg := OverlayConfig{
		Title:    "Model Selector",
		Hint:     "type to search  j/k move  g/G jump  enter select  esc cancel",
		MinWidth: 56,
		MaxWidth: 110,
	}
	contentWidth := overlayCfg.MaxWidth - 4
	searchLine := DimStyle.Render("Search: ")
	if m.modelSelect.searchInput != "" {
		searchLine += m.modelSelect.searchInput
	} else {
		searchLine += DimStyle.Render("(type to filter provider/model)")
	}
	content := lipgloss.JoinVertical(lipgloss.Left,
		ansi.Truncate(searchLine, contentWidth, "…"),
		"",
		m.modelSelect.table.Render(contentWidth),
	)
	dialog, _ := RenderOverlay(overlayCfg, content, lipgloss.Height(content), image.Rect(0, 0, m.width, m.height))
	m.modelSelect.renderCacheWidth = m.width
	m.modelSelect.renderCacheHeight = m.height
	m.modelSelect.renderCacheMaxVisible = maxVisible
	m.modelSelect.renderCacheSearchInput = m.modelSelect.searchInput
	m.modelSelect.renderCacheTableVer = tableVersion
	m.modelSelect.renderCacheText = dialog
	return dialog
}
