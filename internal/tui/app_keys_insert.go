package tui

import (
	"fmt"
	"os"
	"strings"
	"time"

	tea "github.com/keakon/bubbletea/v2"

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

func (m *Model) applySelectedSlashCompletion(matches []slashCommand) bool {
	if len(matches) == 0 {
		return false
	}
	if m.slashCompleteSelected >= len(matches) {
		m.slashCompleteSelected = len(matches) - 1
	}
	if m.slashCompleteSelected < 0 {
		m.slashCompleteSelected = 0
	}
	sel := matches[m.slashCompleteSelected]
	m.input.SetDisplayValueAndPastes(sel.Cmd+" ", nil, 0)
	m.input.CursorEnd()
	m.input.syncHeight()
	m.slashCompleteSelected = 0
	m.recalcViewportSize()
	return true
}

func (m *Model) maybeExportDiagnosticsShortcut(key string) tea.Cmd {
	if !keyMatches(key, m.keyMap.Diagnostics) {
		return nil
	}
	trigger := key
	if strings.TrimSpace(trigger) == "" {
		trigger = "diagnostics"
	}
	m.recordTUIDiagnostic("local-command", "shortcut:%s", trigger)
	return m.exportDiagnosticsBundleNow("shortcut:" + trigger)
}

func (m *Model) maybeMCPShortcut(key string) bool {
	if !keyMatches(key, m.keyMap.MCP) {
		return false
	}
	if m.agent == nil {
		return true
	}
	trigger := key
	if strings.TrimSpace(trigger) == "" {
		trigger = "mcp"
	}
	m.recordTUIDiagnostic("local-command", "shortcut:%s", trigger)
	m.openMCPSelect()
	return true
}

// sendSlashShortcut binds a key to a slash command that is routed to the agent
// (e.g. `/tier fast`, `/yolo on`). Returns true once the binding consumed the
// key, regardless of whether the agent was ready to receive the command, so
// callers can short-circuit further key handling.
func (m *Model) sendSlashShortcut(key string, binding []string, command string) bool {
	if !keyMatches(key, binding) {
		return false
	}
	if m.agent == nil {
		return true
	}
	m.recordTUIDiagnostic("agent-command", "shortcut:%s %s", key, command)
	m.agent.SendUserMessage(command)
	return true
}

func (m *Model) handleInsertKey(msg tea.KeyMsg) tea.Cmd {
	key := msg.String()
	if cmd := m.maybeExportDiagnosticsShortcut(key); cmd != nil {
		return cmd
	}
	if m.maybeServiceTierShortcut(key) {
		return nil
	}
	if m.maybeYoloShortcut(key) {
		return nil
	}
	if m.maybeMCPShortcut(key) {
		return nil
	}
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
				cmd := m.insertAtMentionSelection()
				m.input.syncHeight()
				m.recalcViewportSize()
				return cmd
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
		return m.syncAtMentionIfOpen()
	}

	switch {
	case keyMatches(key, m.keyMap.InsertEscape):
		cmd := m.switchModeWithIME(ModeNormal)
		m.recalcViewportSize()
		return cmd

	case key == "backspace" || key == "ctrl+h":
		if m.input.BangMode() && m.input.Value() == "" {
			m.input.SetBangMode(false)
			m.recalcViewportSize()
			return nil
		}
		if paste, ok := m.input.RemoveInlinePasteAtCursor(); ok {
			m.removeAttachmentForInlinePaste(paste)
			m.input.syncHeight()
			m.recalcViewportSize()
			if m.atMentionOpen {
				return m.syncAtMentionQuery()
			}
			return nil
		}
		cmd := m.input.Update(msg)
		m.input.syncHeight()
		m.recalcViewportSize()
		if m.atMentionOpen {
			return tea.Batch(cmd, m.syncAtMentionQuery())
		}
		return cmd

	case key == "delete" || key == "ctrl+d":
		if paste, ok := m.input.RemoveInlinePasteForwardAtCursor(); ok {
			m.removeAttachmentForInlinePaste(paste)
			m.input.syncHeight()
			m.recalcViewportSize()
			if m.atMentionOpen {
				return m.syncAtMentionQuery()
			}
			return nil
		}
		cmd := m.input.Update(msg)
		m.input.syncHeight()
		m.recalcViewportSize()
		if m.atMentionOpen {
			return tea.Batch(cmd, m.syncAtMentionQuery())
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
		currentValue := m.input.Value()
		matches := m.getSlashCompletions(currentValue)
		if len(matches) > 0 {
			trimmedInput := strings.TrimSpace(currentValue)
			selected := matches[0]
			if m.slashCompleteSelected >= 0 && m.slashCompleteSelected < len(matches) {
				selected = matches[m.slashCompleteSelected]
			}
			if !strings.EqualFold(trimmedInput, strings.TrimSpace(selected.Cmd)) {
				m.input.SetDisplayValueAndPastes(selected.Cmd, nil, 0)
				currentValue = m.input.Value()
			}
		}
		raw := strings.TrimSpace(currentValue)
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
		if m.input.BangMode() {
			if len(m.attachments) > 0 {
				return m.enqueueToast("Remove images before running terminal commands", "warn")
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
			if userBlock.UserLocalShellPending {
				userBlock.StartedAt = time.Now()
			}
			m.appendViewportBlock(userBlock)
			m.recalcViewportSize()
			if strings.TrimSpace(cmdStr) == "" {
				userBlock.UserLocalShellPending = false
				userBlock.InvalidateCache()
				m.syncStartupDeferredTranscriptBlock(userBlock)
				m.markBlockSettled(userBlock)
				return m.enqueueToast("Empty command after !", "warn")
			}
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
			if trimmed == "/models" || strings.HasPrefix(trimmed, "/models ") ||
				trimmed == "/export" || strings.HasPrefix(trimmed, "/export ") ||
				trimmed == "/tier" || strings.HasPrefix(trimmed, "/tier ") ||
				trimmed == "/yolo" || strings.HasPrefix(trimmed, "/yolo ") ||
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
		if m.agent != nil && m.focusedAgentID != "" && len(m.attachments) == 0 && !hasInlinePastes && (trimmed == "/models" || strings.HasPrefix(trimmed, "/models ")) {
			m.recordTUIDiagnostic("agent-command", "%s", trimmed)
			m.agent.SendUserMessage(value)
			return nil
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

			if len(m.attachments) > 0 {
				loaded := make([]Attachment, len(m.attachments))
				for i, a := range m.attachments {
					loaded[i] = a
					if len(loaded[i].Data) == 0 && strings.TrimSpace(loaded[i].ImagePath) != "" {
						data, err := os.ReadFile(loaded[i].ImagePath)
						if err != nil {
							return m.enqueueToast(fmt.Sprintf("Failed to read image %s: %v", loaded[i].FileName, err), "error")
						}
						loaded[i].Data = data
					}
				}
				parts = interleaveImageAttachments(parts, loaded)
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
		if m.shouldSuppressDuplicateImagePasteAction("key") {
			return nil
		}
		if cmd := m.tryPasteImageIntoComposer("key", ""); cmd != nil {
			return cmd
		}
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
				if m.applySelectedSlashCompletion(matches) {
					return nil
				}
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
				return tea.Batch(cmd, m.syncAtMentionQuery())
			}
		}
		cmd := m.input.Update(msg)
		m.input.syncHeight()
		m.recalcViewportSize()
		if m.atMentionOpen {
			cmd = tea.Batch(cmd, m.syncAtMentionQuery())
		} else if key == "@" {
			m.closeAtMention()
		}
		return cmd
	}
}
