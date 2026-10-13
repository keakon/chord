package tui

import (
	"crypto/rand"
	"errors"
	"strings"
	"time"

	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/inputhistory"
)

const historyCommand = "/history"
const historyClearCommand = "/history clear"

type submittedInput struct {
	entry                               inputHistoryEntry
	projectKey, projectLabel, sessionID string
	attachments                         int
}

func (m *Model) SetInputHistory(store *inputhistory.Store, key, label string) {
	m.inputHistoryStore = store
	m.inputHistoryProjectKey = key
	m.inputHistoryProjectLabel = label
	m.inputHistoryRuntime = newHistorySearchRuntime()
}
func (m *Model) captureSubmittedInput() submittedInput {
	return submittedInput{m.input.historyEntry(), m.inputHistoryProjectKey, m.inputHistoryProjectLabel, m.currentSessionID(), len(m.attachments)}
}
func (m *Model) recordSubmittedInput(s submittedInput) tea.Cmd {
	m.input.PushHistory(s.entry)
	if m.inputHistoryStore == nil || !m.inputHistoryStore.Status().Recording {
		return nil
	}
	parts, err := historyTextParts(s.entry, inputhistory.MaxRecordBytes)
	if err != nil {
		if errors.Is(err, inputhistory.ErrTooLarge) {
			return m.enqueueToast("Input too long; not saved to history", "warn")
		}
		return m.enqueueToast("Could not record input history", "warn")
	}
	nonempty := false
	kind := inputhistory.Prompt
	for _, p := range parts {
		v := strings.TrimSpace(p.Text)
		if v != "" && !nonempty {
			nonempty = true
			if strings.HasPrefix(v, "/") {
				kind = inputhistory.SlashCommand
			}
		}
	}
	if !nonempty {
		return nil
	}
	if s.entry.BangMode {
		kind = inputhistory.ShellCommand
	}
	a := m.inputHistoryStore.TryRecord(inputhistory.Record{ID: rand.Text(), SubmittedAt: time.Now().UTC(), ProjectKey: s.projectKey, ProjectLabel: s.projectLabel, SessionID: s.sessionID, Kind: kind, TextParts: parts, AttachmentCount: s.attachments})
	if a.Outcome == inputhistory.TooLarge {
		return m.enqueueToast("Input too long; not saved to history", "warn")
	}
	if a.Outcome == inputhistory.LocalOnly || a.Outcome == inputhistory.Closed {
		msg := "Input not saved to history"
		if a.Retained {
			msg += "; searchable in this process only"
		}
		return m.enqueueToast(msg, "warn")
	}
	return nil
}

type inputHistoryStoreChangedMsg struct{}

func (m *Model) waitInputHistoryStore() tea.Cmd {
	s := m.inputHistoryStore
	if s == nil {
		return nil
	}
	return func() tea.Msg {
		select {
		case <-s.Notifications():
			return inputHistoryStoreChangedMsg{}
		case <-s.Done():
			return nil
		}
	}
}
