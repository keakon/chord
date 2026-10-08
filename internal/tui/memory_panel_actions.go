package tui

import (
	"context"
	"strings"

	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/identity"
)

func (m *Model) startMemoryOrganization(all bool, instruction string) tea.Cmd {
	c, ok := m.memoryController()
	if !ok {
		return nil
	}
	if m.memoryMainBusy() {
		return m.enqueueToast("Wait until the main agent is idle before organizing memory", "info")
	}
	base, err := m.memorySelectedSnapshot(all)
	if err != nil {
		return m.enqueueToast(err.Error(), "info")
	}
	if m.memoryPanel.cancel != nil {
		m.memoryPanel.cancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.memoryPanel.cancel = cancel
	m.memoryPanel.loading = true
	m.memoryPanel.err = ""
	m.memoryPanel.instructionMode = false
	m.memoryPanel.seq++
	seq, epoch := m.memoryPanel.seq, m.memoryPanel.epoch
	return func() tea.Msg {
		draft, err := c.OrganizeMemory(ctx, base, all, instruction)
		return memoryPanelResultMsg{seq: seq, epoch: epoch, draft: draft, err: err}
	}
}

func (m *Model) memoryMainBusy() bool {
	return runtimeActivityBusy(m.activities[identity.MainAgentID]) || m.inflightDraftBelongsToAgent(identity.MainAgentID) || m.agent != nil && m.agent.IsCompactionRunning()
}

func (m *Model) applyMemoryChange(undo bool) tea.Cmd {
	c, ok := m.memoryController()
	if !ok {
		return nil
	}
	if m.memoryPanel.loading {
		return nil
	}
	draft := m.memoryPanel.preview
	if !undo && (draft == nil || draft.Empty()) {
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.memoryPanel.cancel = cancel
	m.memoryPanel.loading = true
	m.memoryPanel.seq++
	seq, epoch := m.memoryPanel.seq, m.memoryPanel.epoch
	return func() tea.Msg {
		defer cancel()
		var err error
		if undo {
			err = c.UndoMemory(ctx)
		} else {
			err = c.ApplyMemory(ctx, draft)
		}
		return memoryPanelResultMsg{seq: seq, epoch: epoch, changed: err == nil, err: err}
	}
}

// copyMemoryContent copies the selected project records when any are marked,
// otherwise the entry under the cursor.
func (m *Model) copyMemoryContent() tea.Cmd {
	p := &m.memoryPanel
	_, content, _ := m.memoryCurrentContent()
	if len(p.selected) > 0 {
		if base, err := m.memorySelectedSnapshot(false); err == nil {
			parts := make([]string, 0, len(base.Items))
			for _, item := range base.Items {
				parts = append(parts, item.Content)
			}
			content = strings.Join(parts, "\n\n---\n\n")
		}
	}
	return m.copyMemoryText(content)
}

// copyMemoryPath copies the file path of the entry under the cursor.
func (m *Model) copyMemoryPath() tea.Cmd {
	_, _, path := m.memoryCurrentContent()
	if path == "" {
		return m.enqueueToast("No file path for this entry", "info")
	}
	return writeClipboardCmd(path, "Memory path copied")
}

// copyMemoryText keeps panel copy feedback on one message instead of the
// content viewer wording.
func (m *Model) copyMemoryText(content string) tea.Cmd {
	if strings.TrimSpace(content) == "" {
		return m.enqueueToast("Nothing to copy", "info")
	}
	return writeClipboardCmd(content, "Memory copied")
}
