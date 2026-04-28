package tui

import tea "charm.land/bubbletea/v2"

func (m *Model) handleKeyMsg(msg tea.KeyMsg) tea.Cmd {
	if m.interactionSuppressed() {
		if isCtrlC(msg) {
			return m.handleCtrlC()
		}
		return nil
	}
	// Ctrl+C is handled globally (quit or cancel) before mode-specific handlers.
	// Some terminals send Ctrl+C as rune 3 (ETX) or as Code=='c'+ModCtrl; match all.
	if isCtrlC(msg) {
		return m.handleCtrlC()
	}
	m.clearPendingQuitForKey(msg)

	// Super+C (cmd+c) – copy: in some terminals (e.g. Ghostty) the key
	// event is forwarded to the application. Copy the current content:
	// mouse selection, focused block, or input text.
	if isSuperC(msg) {
		return m.handleSuperCopy()
	}
	// Super+V (cmd+v) – smart paste, same as ctrl+v.
	if isSuperV(msg) && m.mode == ModeInsert {
		return m.pasteFromClipboard()
	}

	switch m.mode {
	case ModeInsert:
		return m.handleInsertKey(msg)
	case ModeNormal:
		return m.handleNormalKey(msg)
	case ModeDirectory:
		return m.handleDirectoryKey(msg)
	case ModeConfirm:
		return m.handleConfirmKey(msg)
	case ModeQuestion:
		return m.handleQuestionKey(msg)
	case ModeSearch:
		return m.handleSearchKey(msg)
	case ModeModelSelect:
		return m.handleModelSelectKey(msg)
	case ModeSessionSelect:
		return m.handleSessionSelectKey(msg)
	case ModeSessionDeleteConfirm:
		return m.handleSessionDeleteConfirmKey(msg)
	case ModeHandoffSelect:
		return m.handleHandoffSelectKey(msg)
	case ModeUsageStats:
		return m.handleUsageStatsKey(msg)
	case ModeHelp:
		return m.handleHelpKey(msg)
	case ModeImageViewer:
		return m.handleImageViewerKey(msg)
	case ModeRules:
		return m.handleRulesKey(msg)
	default:
		return nil
	}
}
