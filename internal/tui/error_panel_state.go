package tui

import (
	"errors"
	"strings"
	"time"

	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/llm"
)

const maxAgentErrors = 80

type agentErrorRecord struct {
	Timestamp  time.Time
	AgentID    string
	Message    string
	StatusCode int
	ErrorCode  string
	ErrorType  string
	Provider   string
	Model      string
	KeySuffix  string
	Retry      bool // intermediate retry/fallback attempt (vs a final error)
}

type errorPanelState struct {
	scrollOffset int
	prevMode     Mode

	renderVersion     uint64
	linesCacheWidth   int
	linesCacheVer     uint64
	linesCacheLines   []string
	dialogCacheW      int
	dialogCacheH      int
	dialogCacheScroll int
	dialogCacheVer    uint64
	dialogCacheTheme  string
	dialogCacheText   string
}

// agentErrorMessage extracts the panel-facing message for err, preferring a
// non-empty structured *llm.APIError message over the wrapped error string.
func agentErrorMessage(err error) string {
	if err == nil {
		return ""
	}
	if apiErr, ok := errors.AsType[*llm.APIError](err); ok {
		if msg := strings.TrimSpace(apiErr.Message); msg != "" {
			return msg
		}
	}
	return err.Error()
}

// recordAgentError appends an error to the active conversation ring buffer, extracting
// structured fields (HTTP status, provider error code/type) from the error
// chain when an *llm.APIError is present. retry marks intermediate
// retry/fallback attempts, which is used to drop a final error that merely
// repeats the last silently-recorded retry attempt.
func (m *Model) recordAgentError(agentID string, err error, provider, model, keySuffix string, retry bool) {
	if err == nil {
		return
	}
	rec := agentErrorRecord{
		Timestamp: time.Now(),
		AgentID:   agentID,
		Message:   err.Error(),
		Provider:  provider,
		Model:     model,
		KeySuffix: keySuffix,
		Retry:     retry,
	}
	if apiErr, ok := errors.AsType[*llm.APIError](err); ok {
		rec.StatusCode = apiErr.StatusCode
		rec.ErrorCode = apiErr.Code
		rec.ErrorType = apiErr.Type
		if msg := strings.TrimSpace(apiErr.Message); msg != "" {
			rec.Message = msg
		}
	}
	idx := m.agentErrorsNext % maxAgentErrors
	m.agentErrors[idx] = rec
	m.agentErrorsNext = (idx + 1) % maxAgentErrors
	if m.agentErrorsCount < maxAgentErrors {
		m.agentErrorsCount++
	}
	m.invalidateErrorPanelCache()
}

// finalErrorDuplicatesLastRetry reports whether err repeats the most recent
// retry record. A non-retriable error with no fallback is emitted once as a
// silent retry attempt (with provider/model/key metadata) and again as the
// final error; recording both would show the same failure twice, so the final
// one is dropped in favor of the richer retry record.
func (m *Model) finalErrorDuplicatesLastRetry(err error) bool {
	if m.agentErrorsCount == 0 || err == nil {
		return false
	}
	lastIdx := (m.agentErrorsNext - 1 + maxAgentErrors) % maxAgentErrors
	last := m.agentErrors[lastIdx]
	return last.Retry && last.Message == agentErrorMessage(err)
}

func (m *Model) clearAgentErrors() {
	m.agentErrors = [maxAgentErrors]agentErrorRecord{}
	m.agentErrorsNext = 0
	m.agentErrorsCount = 0
	m.invalidateErrorPanelCache()
}

func (m *Model) openErrorPanel() {
	if m.mode == ModeErrorPanel {
		return
	}
	prevMode := m.mode
	m.clearActiveSearch()
	if prevMode == ModeInsert {
		m.input.Blur()
	}
	m.clearChordState()
	m.errorPanel = errorPanelState{
		prevMode: prevMode,
	}
	m.invalidateErrorPanelCache()
	m.mode = ModeErrorPanel
	m.recalcViewportSize()
}

func (m *Model) closeErrorPanel() tea.Cmd {
	if m.mode != ModeErrorPanel {
		return nil
	}
	prevMode := m.errorPanel.prevMode
	m.errorPanel = errorPanelState{}
	cmd := m.restoreModeWithIME(prevMode)
	m.recalcViewportSize()
	if prevMode == ModeInsert {
		return tea.Batch(cmd, m.input.Focus())
	}
	return cmd
}

func (m *Model) handleErrorPanelKey(msg tea.KeyMsg) tea.Cmd {
	key := msg.String()
	switch key {
	case "j", "down":
		m.errorPanel.scrollOffset++
	case "k", "up":
		m.errorPanel.scrollOffset--
	case "ctrl+f":
		m.errorPanel.scrollOffset += m.errorPanelVisibleLines()
	case "ctrl+b":
		m.errorPanel.scrollOffset -= m.errorPanelVisibleLines()
	case "g":
		m.errorPanel.scrollOffset = 0
	case "G":
		m.errorPanel.scrollOffset = m.errorPanelMaxScroll()
	default:
		if m.overlayCloseKeyMatches(key, m.keyMap.ErrorPanel) {
			return m.closeErrorPanel()
		}
	}
	m.clampErrorPanelScroll()
	return nil
}

func (m *Model) errorPanelVisibleLines() int {
	visible := m.height - 12
	if visible < 8 {
		visible = 8
	}
	return visible
}

func (m *Model) errorPanelMaxWidth() int {
	maxWidth := min(m.width-12, 110)
	if maxWidth < 60 {
		maxWidth = 60
	}
	return maxWidth
}

func (m *Model) errorPanelInnerWidth() int {
	return m.errorPanelMaxWidth() - 4
}

func (m *Model) errorPanelMaxScroll() int {
	lines := m.errorPanelLines(m.errorPanelInnerWidth())
	maxScroll := len(lines) - m.errorPanelVisibleLines()
	if maxScroll < 0 {
		maxScroll = 0
	}
	return maxScroll
}

func (m *Model) clampErrorPanelScroll() {
	if m.errorPanel.scrollOffset < 0 {
		m.errorPanel.scrollOffset = 0
	}
	if maxScroll := m.errorPanelMaxScroll(); m.errorPanel.scrollOffset > maxScroll {
		m.errorPanel.scrollOffset = maxScroll
	}
}

func (m *Model) invalidateErrorPanelCache() {
	m.errorPanel.renderVersion++
	m.errorPanel.linesCacheWidth = 0
	m.errorPanel.linesCacheVer = 0
	m.errorPanel.linesCacheLines = nil
	m.errorPanel.dialogCacheW = 0
	m.errorPanel.dialogCacheH = 0
	m.errorPanel.dialogCacheScroll = 0
	m.errorPanel.dialogCacheVer = 0
	m.errorPanel.dialogCacheTheme = ""
	m.errorPanel.dialogCacheText = ""
	if m.mode == ModeErrorPanel {
		m.clampErrorPanelScroll()
	}
}

// snapshotAgentErrors returns recorded errors oldest-first.
func (m *Model) snapshotAgentErrors() []agentErrorRecord {
	if m.agentErrorsCount == 0 {
		return nil
	}
	out := make([]agentErrorRecord, 0, m.agentErrorsCount)
	start := m.agentErrorsNext - m.agentErrorsCount
	if start < 0 {
		start += maxAgentErrors
	}
	for i := 0; i < m.agentErrorsCount; i++ {
		idx := (start + i) % maxAgentErrors
		out = append(out, m.agentErrors[idx])
	}
	return out
}
