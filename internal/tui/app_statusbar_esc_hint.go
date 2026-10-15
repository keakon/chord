package tui

// nextEscHint returns a short verb phrase describing what pressing esc would do
// in the current state, or "" when no extra esc hint should be shown.
//
// For chord-pending states it intentionally returns "": chord already has a
// nearby left-side pending buffer hint, and repeating "esc ⇢ clear <buf>"
// adds noise and layout churn.
func (m *Model) nextEscHint() string {
	switch m.mode {
	case ModeSearch:
		return "cancel search"
	case ModeInputHistorySearch, ModeInputHistoryClearConfirm:
		return "close history"
	case ModeMemoryPanel:
		return "close memory"
	case ModeFileViewer:
		return m.fileViewerEscHint()
	case ModeContentViewer:
		return "close view"
	case ModeContext:
		return "close context"
	case ModeStatus:
		return "close status"
	case ModeHelp:
		return "close help"
	case ModeUsageStats:
		return "close stats"
	case ModeErrorPanel:
		return "close errors"
	case ModeInsert:
		if keyMatches("esc", m.keyMap.InsertEscape) {
			return "normal mode"
		}
		return ""
	case ModeNormal:
		// continue below
	default:
		return ""
	}
	switch {
	case m.search.State.Active:
		return "clear search"
	case m.chord.active():
		return ""
	case m.agent != nil && m.agent.CurrentLoopState() != "":
		return "exit loop"
	case m.isAgentBusy():
		return "cancel turn"
	default:
		return ""
	}
}

// fileViewerEscHint names the single thing Esc will do. It must match
// handleFileViewerKey's routing exactly: offering "back / close" taught readers
// that Esc might only step back one layer, which is why a preview of a single
// delivery looked like it refused to close.
func (m *Model) fileViewerEscHint() string {
	v := &m.fileViewer
	switch {
	case v.inputMode != "":
		return "cancel input"
	case len(v.apps) > 0:
		return "close app list"
	case v.showing && len(v.files) > 1:
		return "back to file list"
	default:
		return "close file view"
	}
}
