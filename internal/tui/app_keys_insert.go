package tui

import (
	"os"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/keakon/chord/internal/message"
)

func localShellUserLine(display string) string {
	if display == "" {
		return "!"
	}
	return "!" + display
}

func localShellCommandFromParts(display string, parts []message.ContentPart) string {
	return userBlockTextFromParts(parts, display)
}

func (m *Model) handleInsertKey(msg tea.KeyMsg) tea.Cmd {
	key := msg.String()
	m.clearPendingQuit()
	if key != "" {
		m.input.ClearSelection()
	}
	if m.atMentionOpen {
		switch {
		case keyMatches(key, m.keyMap.InsertEscape):
			m.closeAtMention()
			m.recalcViewportSize()
			return nil
		case key == "tab", keyMatches(key, m.keyMap.InsertSubmit):
			if m.atMentionList != nil && m.atMentionList.Len() > 0 {
				m.insertAtMentionSelection()
				m.input.syncHeight()
				m.recalcViewportSize()
				return nil
			}
			m.closeAtMention()
			m.recalcViewportSize()
			return nil
		case key == "up" || keyMatches(key, m.keyMap.InsertHistoryUp):
			if m.atMentionList != nil {
				if m.atMentionList.CursorAt() == 0 {
					m.atMentionList.CursorToBottom()
				} else {
					m.atMentionList.CursorUp()
				}
			}
			return nil
		case key == "down" || keyMatches(key, m.keyMap.InsertHistoryDown):
			if m.atMentionList != nil {
				if m.atMentionList.CursorAt() == m.atMentionList.Len()-1 {
					m.atMentionList.CursorToTop()
				} else {
					m.atMentionList.CursorDown()
				}
			}
			return nil
		}
	}

	if m.input.ProtectInlinePastesOnKey(msg) {
		m.recalcViewportSize()
		if m.atMentionOpen {
			m.syncAtMentionQuery()
		}
		return nil
	}

	switch {
	case keyMatches(key, m.keyMap.InsertEscape):
		if m.agent != nil && m.agent.CurrentLoopState() != "" {
			m.agent.DisableLoopMode()
			m.recalcViewportSize()
			return tea.Batch(m.switchModeWithIME(ModeNormal), m.enqueueToast("Loop disabled.", "info"))
		}
		cmd := m.switchModeWithIME(ModeNormal)
		m.recalcViewportSize()
		return cmd

	case key == "backspace" || key == "ctrl+h":
		if m.input.BangMode() && m.input.Value() == "" {
			m.input.SetBangMode(false)
			m.recalcViewportSize()
			return nil
		}
		if m.input.RemoveInlinePasteAtCursor() {
			m.input.syncHeight()
			m.recalcViewportSize()
			if m.atMentionOpen {
				m.syncAtMentionQuery()
			}
			return nil
		}
		cmd := m.input.Update(msg)
		m.input.syncHeight()
		m.recalcViewportSize()
		if m.atMentionOpen {
			m.syncAtMentionQuery()
		}
		return cmd

	case key == "delete" || key == "ctrl+d":
		if m.input.RemoveInlinePasteForwardAtCursor() {
			m.input.syncHeight()
			m.recalcViewportSize()
			if m.atMentionOpen {
				m.syncAtMentionQuery()
			}
			return nil
		}
		cmd := m.input.Update(msg)
		m.input.syncHeight()
		m.recalcViewportSize()
		if m.atMentionOpen {
			m.syncAtMentionQuery()
		}
		return cmd

	case key == "!":
		if !m.input.BangMode() && m.input.Value() == "" && m.input.Line() == 0 && m.input.Column() == 0 {
			m.input.SetBangMode(true)
			m.recalcViewportSize()
			return nil
		}
		cmd := m.input.Update(msg)
		m.input.syncHeight()
		m.recalcViewportSize()
		return cmd

	case keyMatches(key, m.keyMap.InsertNewline):
		m.input.preExpandHeight()
		cmd := m.input.Update(msg)
		m.input.syncHeight()
		m.recalcViewportSize()
		return cmd

	case keyMatches(key, m.keyMap.InsertSubmit):
		raw := strings.TrimSpace(m.input.Value())
		var value string
		if m.input.BangMode() {
			value = "!" + raw
		} else {
			value = raw
		}
		if value == "" && len(m.attachments) == 0 {
			return m.tryContinue()
		}
		if len(m.attachments) == 0 && strings.EqualFold(value, "/help") {
			m.recordTUIDiagnostic("local-command", "%s", value)
			m.input.AddCurrentToHistory()
			m.input.Reset()
			m.slashCompleteSelected = 0
			return m.openHelp()
		}
		if len(m.attachments) == 0 && strings.EqualFold(value, "/stats") {
			m.recordTUIDiagnostic("local-command", "%s", value)
			m.input.AddCurrentToHistory()
			m.input.Reset()
			m.slashCompleteSelected = 0
			m.openUsageStats()
			return nil
		}
		cmdText, isDiagnostics := parseDiagnosticsBundleCommand(value)
		if len(m.attachments) == 0 && isDiagnostics {
			m.recordTUIDiagnostic("local-command", "%s", cmdText)
			m.input.AddCurrentToHistory()
			m.input.Reset()
			m.slashCompleteSelected = 0
			m.closeAtMention()
			m.recalcViewportSize()
			return m.exportDiagnosticsBundleNow(cmdText)
		}
		if m.input.BangMode() {
			if len(m.attachments) > 0 {
				return m.enqueueToast("Remove images before running !shell", "warn")
			}
			display := m.input.DisplayValue()
			contentParts := m.input.ContentParts()
			cmdStr := localShellCommandFromParts(display, contentParts)
			userLine := localShellUserLine(display)
			m.input.AddCurrentToHistory()
			m.input.Reset()
			m.finalizeTurn()
			shellID := m.nextBlockID
			m.nextBlockID++
			userBlock := &Block{ID: shellID, Type: BlockUser, Content: userLine, AgentID: m.focusedAgentID, ImageCount: 0, Collapsed: true, UserLocalShellCmd: cmdStr, UserLocalShellPending: strings.TrimSpace(cmdStr) != "", MsgIndex: -1}
			m.appendViewportBlock(userBlock)
			m.recalcViewportSize()
			if strings.TrimSpace(cmdStr) == "" {
				userBlock.UserLocalShellPending = false
				userBlock.InvalidateCache()
				m.syncStartupDeferredTranscriptBlock(userBlock)
				m.markBlockSettled(userBlock)
				return m.enqueueToast("Empty command after !", "warn")
			}
			m.localShellStartedAt = time.Now()
			wd, _ := os.Getwd()
			return tea.Batch(shellBangCmd(wd, userLine, cmdStr, m.focusedAgentID, shellID), m.startAnimTick())
		}

		inlineParts := m.input.ContentParts()
		inlinePasteTexts := m.input.InlinePasteRawContents()
		hasInlinePastes := m.input.HasInlinePastes()
		m.input.AddCurrentToHistory()
		m.input.Reset()
		trimmed := strings.TrimSpace(value)
		if m.agent != nil && len(m.attachments) == 0 && !hasInlinePastes {
			if trimmed == "/model" || strings.HasPrefix(trimmed, "/model ") ||
				trimmed == "/export" || strings.HasPrefix(trimmed, "/export ") ||
				trimmed == "/compact" || trimmed == "/loop" || trimmed == "/loop on" || strings.HasPrefix(trimmed, "/loop on ") || trimmed == "/loop off" {
				m.recordTUIDiagnostic("agent-command", "%s", trimmed)
				m.agent.SendUserMessage(value)
				return nil
			}
			switch {
			case trimmed == "/new":
				m.recordTUIDiagnostic("agent-command", "%s", trimmed)
				m.beginSessionSwitch("new", "")
				m.agent.NewSession()
				m.resetTerminalTitle()
				return nil
			case trimmed == "/resume":
				m.recordTUIDiagnostic("agent-command", "%s", trimmed)
				m.agent.ResumeSession()
				return nil
			case strings.HasPrefix(trimmed, "/resume "):
				m.recordTUIDiagnostic("agent-command", "%s", trimmed)
				targetID := strings.TrimSpace(strings.TrimPrefix(trimmed, "/resume "))
				m.beginSessionSwitch("resume", targetID)
				m.agent.ResumeSessionID(targetID)
				return nil
			case trimmed == "/rules":
				m.recordTUIDiagnostic("local-command", "%s", trimmed)
				m.input.AddCurrentToHistory()
				m.input.Reset()
				m.slashCompleteSelected = 0
				return m.openRules()
			}
		}
		if m.agent != nil && m.focusedAgentID != "" && len(m.attachments) == 0 && !hasInlinePastes {
			m.finalizeTurn()
			draft := queuedDraft{Content: value, DisplayContent: value, FileRefs: atMentionFileRefs([]string{value}, m.workingDir), QueuedAt: time.Now()}
			m.editingQueuedDraftID = ""
			return m.sendDraft(draft)
		}
		draftID := m.draftIDForSubmit()
		var draft queuedDraft
		fileRefTexts := append([]string{value}, inlinePasteTexts...)
		fileRefs := atMentionFileRefs(fileRefTexts, m.workingDir)
		fileRefParts := m.buildFileRefParts(value, inlineParts, inlinePasteTexts...)
		if fileRefParts != nil || len(m.attachments) > 0 || len(inlineParts) > 0 {
			var parts []message.ContentPart
			if fileRefParts != nil {
				parts = fileRefParts
			} else if len(inlineParts) > 0 {
				parts = append(parts, inlineParts...)
			} else if value != "" {
				parts = []message.ContentPart{{Type: "text", Text: value}}
			}
			for _, a := range m.attachments {
				parts = append(parts, message.ContentPart{Type: "image", MimeType: a.MimeType, Data: a.Data, FileName: a.FileName})
			}
			draft = queuedDraft{ID: draftID, Parts: parts, FileRefs: fileRefs, Content: value, DisplayContent: draftListDisplayText(parts, value), QueuedAt: time.Now()}
			m.attachments = nil
			m.recalcViewportSize()
		} else {
			draft = queuedDraft{ID: draftID, Content: value, DisplayContent: value, FileRefs: fileRefs, QueuedAt: time.Now()}
		}
		if m.shouldQueueMainDraft() {
			synced := m.syncQueuedDraft(draft)
			draft.Mirrored = synced
			if !synced {
				m.queueSyncEnabled = false
			}
			m.queuedDrafts = append(m.queuedDrafts, draft)
			m.recalcViewportSize()
			return nil
		}
		m.finalizeTurn()
		m.editingQueuedDraftID = ""
		return m.sendDraft(draft)

	case len(m.getSlashCompletions(m.input.Value())) > 0 &&
		(keyMatches(key, m.keyMap.InsertHistoryUp) || keyMatches(key, m.keyMap.InsertHistoryDown) || key == "j" || key == "k"):
		matches := m.getSlashCompletions(m.input.Value())
		if m.slashCompleteSelected >= len(matches) {
			m.slashCompleteSelected = len(matches) - 1
		}
		if m.slashCompleteSelected < 0 {
			m.slashCompleteSelected = 0
		}
		if key == "down" || key == "j" || keyMatches(key, m.keyMap.InsertHistoryDown) {
			m.slashCompleteSelected++
			if m.slashCompleteSelected >= len(matches) {
				m.slashCompleteSelected = 0
			}
		} else {
			m.slashCompleteSelected--
			if m.slashCompleteSelected < 0 {
				m.slashCompleteSelected = len(matches) - 1
			}
		}
		return nil

	case keyMatches(key, m.keyMap.InsertHistoryUp):
		if !m.hasComposerContent() {
			if cmd := m.loadLastUserMessageToComposer(); cmd != nil {
				return cmd
			}
			if m.hasComposerContent() {
				return nil
			}
		}
		m.input.HistoryUp()
		m.recalcViewportSize()
		return nil

	case keyMatches(key, m.keyMap.InsertHistoryDown):
		m.input.HistoryDown()
		m.recalcViewportSize()
		return nil

	case keyMatches(key, m.keyMap.SwitchModel):
		m.openModelSelect()
		return nil

	case keyMatches(key, m.keyMap.InsertAttachClipboard):
		return m.pasteFromClipboard()

	case keyMatches(key, m.keyMap.InsertAttachFile):
		return m.pickImageFile()

	case keyMatches(key, m.keyMap.InsertClearInput):
		m.input.SetDisplayValueAndPastes("", nil, 0)
		m.input.syncHeight()
		m.attachments = nil
		m.closeAtMention()
		m.recalcViewportSize()
		return nil

	default:
		value := m.input.Value()
		matches := m.getSlashCompletions(value)
		if len(matches) > 0 && (key == "tab" || key == "down" || key == "up" || key == "j" || key == "k") {
			if m.slashCompleteSelected >= len(matches) {
				m.slashCompleteSelected = len(matches) - 1
			}
			if m.slashCompleteSelected < 0 {
				m.slashCompleteSelected = 0
			}
			switch key {
			case "tab":
				sel := matches[m.slashCompleteSelected]
				m.input.SetDisplayValueAndPastes(sel.Cmd+" ", nil, 0)
				m.input.CursorEnd()
				m.input.syncHeight()
				m.slashCompleteSelected = 0
				m.recalcViewportSize()
				return nil
			case "down", "j":
				m.slashCompleteSelected++
				if m.slashCompleteSelected >= len(matches) {
					m.slashCompleteSelected = 0
				}
				return nil
			case "up", "k":
				m.slashCompleteSelected--
				if m.slashCompleteSelected < 0 {
					m.slashCompleteSelected = len(matches) - 1
				}
				return nil
			}
		}
		if key == "tab" {
			if m.focusedAgentID == "" {
				m.handleSwitchRole()
			}
			return nil
		}
		if key == "shift+tab" {
			return m.handleSwitchAgent()
		}
		if key == "@" && !m.atMentionOpen {
			col := m.input.Column()
			curLine := m.input.Line()
			row, _ := inputLineAt(m.input.Value(), curLine)
			if canTriggerAtMention(row, col) {
				cmd := m.input.Update(msg)
				m.input.syncHeight()
				m.recalcViewportSize()
				m.atMentionOpen = true
				m.atMentionLine = curLine
				m.atMentionTriggerCol = m.input.Column()
				m.atMentionQuery = ""
				m.syncAtMentionQuery()
				return cmd
			}
		}
		cmd := m.input.Update(msg)
		m.input.syncHeight()
		m.recalcViewportSize()
		if m.atMentionOpen {
			m.syncAtMentionQuery()
		} else if key == "@" {
			m.closeAtMention()
		}
		return cmd
	}
}
