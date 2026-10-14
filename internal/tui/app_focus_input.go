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
		input:                m.input.SaveState(),
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
	m.input.RestoreState(state.input)
	m.composerUndo = composerUndoState{boundary: m.input.Revision().Boundary}
	m.attachments = cloneAttachments(state.attachments)
	m.editingQueuedDraftID = state.editingQueuedDraftID
}
