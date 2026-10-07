package tui

import (
	"fmt"

	tea "github.com/keakon/bubbletea/v2"
)

type composerClipboardTarget struct {
	session, boundary, version uint64
	agentID                    string
}

type composerClipboardTextMsg struct {
	target composerClipboardTarget
	text   string
	err    error
}

func (m *Model) composerClipboardTarget() composerClipboardTarget {
	return composerClipboardTarget{m.sessionTranscriptEpoch, m.input.editBoundary, m.input.interactionVersion, m.focusedAgentID}
}

func (m *Model) pasteComposerTextFromClipboard() tea.Cmd {
	target := m.composerClipboardTarget()
	return func() tea.Msg {
		text, err := clipboardReadAll()
		if err != nil {
			err = fmt.Errorf("read clipboard text: %w", err)
		}
		return composerClipboardTextMsg{target: target, text: text, err: err}
	}
}

func (m *Model) handleComposerClipboardText(msg composerClipboardTextMsg) tea.Cmd {
	current := m.composerClipboardTarget()
	if msg.target.session != current.session || msg.target.agentID != current.agentID || msg.target.boundary != current.boundary || m.mode != ModeInsert {
		return nil
	}
	if msg.err != nil {
		return m.enqueueToast(msg.err.Error(), "warn")
	}
	if msg.text == "" {
		return nil
	}
	if msg.target.version != current.version {
		return m.enqueueToast("Input changed while reading the clipboard; paste again", "info")
	}
	return m.insertComposerText(msg.text)
}
