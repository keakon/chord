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
	case ModeInsert:
		return "normal mode"
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
		return ""
	case m.isAgentBusy():
		return "cancel turn"
	default:
		return ""
	}
}
