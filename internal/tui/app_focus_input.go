package tui

func cloneAttachments(src []Attachment) []Attachment {
	if len(src) == 0 {
		return nil
	}
	dst := make([]Attachment, len(src))
	copy(dst, src)
	return dst
}

func (m *Model) saveComposerStateForAgent(agentID string) {
	if m == nil {
		return
	}
	if m.agentComposerStates == nil {
		m.agentComposerStates = make(map[string]agentComposerState)
	}
	state := agentComposerState{
		draft:                m.input.draftSnapshot(),
		historyBrowsing:      m.input.histIdx < len(m.input.history),
		historyIndex:         m.input.histIdx,
		historyDraft:         m.input.draft,
		attachments:          cloneAttachments(m.attachments),
		editingQueuedDraftID: m.editingQueuedDraftID,
	}
	m.agentComposerStates[normalizeDraftAgentID(agentID)] = state
}

func (m *Model) restoreComposerStateForAgent(agentID string) {
	if m == nil {
		return
	}
	m.closeAtMention()
	m.slashCompleteSelected = 0
	state, ok := m.agentComposerStates[normalizeDraftAgentID(agentID)]
	if !ok {
		state = agentComposerState{}
	}
	m.input.applyDraftSnapshot(state.draft)
	if state.historyBrowsing {
		histIdx := state.historyIndex
		if histIdx < 0 {
			histIdx = 0
		}
		if histIdx > len(m.input.history) {
			histIdx = len(m.input.history)
		}
		m.input.histIdx = histIdx
		m.input.draft = state.historyDraft
	} else {
		m.input.histIdx = len(m.input.history)
		m.input.draft = inputDraftSnapshot{}
	}
	m.input.syncHeight()
	m.attachments = cloneAttachments(state.attachments)
	m.editingQueuedDraftID = state.editingQueuedDraftID
}
