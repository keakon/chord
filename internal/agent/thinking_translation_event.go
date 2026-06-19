package agent

// ThinkingTranslatedEvent requests the TUI to append a translated thinking
// section to a specific settled thinking card for the specified agent.
//
// This event is emitted after the assistant message has been finalized and
// persisted. The TUI applies it only to settled thinking cards, so it does not
// interfere with streaming.
//
// The TUI applies the update best-effort; failures must not affect the main
// agent loop.
type ThinkingTranslatedEvent struct {
	AgentID      string // "" = main agent
	MessageID    string // stable transcript-local message key, e.g. "msgidx:12"
	BlockIndex   int    // thinking block index within the assistant message
	Translated   string // translated text (already normalized/validated)
	TargetLang   string // e.g. "zh-Hans" (observability only)
	OriginalHash string // hash of the untranslated thinking block, when known
}

func (ThinkingTranslatedEvent) agentEvent() {}
