package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/message"
)

func mainTranscript(n int, prefix string) []message.Message {
	msgs := make([]message.Message, 0, n)
	for range n {
		msgs = append(msgs, message.Message{Role: message.RoleAssistant, Content: prefix})
	}
	return msgs
}

// newFocusAwareModel builds a model whose backend mirrors the real MainAgent
// contract the bug depends on: GetMessages returns only the focused agent's
// transcript, while GetMessagesForTarget resolves the requested conversation —
// for the main target that is the main transcript regardless of the view.
func newFocusAwareModel(t *testing.T, mainMsgs, subMsgs []message.Message) *Model {
	t.Helper()
	backend := &targetedConversationAgent{
		sessionControlAgent: sessionControlAgent{
			messagesByFocus: map[string][]message.Message{"": mainMsgs, "agent-1": subMsgs},
		},
		messagesByTask: map[string][]message.Message{"": mainMsgs},
	}
	m := NewModelWithSize(backend, 120, 24)
	m.setFocusedAgent("agent-1")
	if m.focusedAgentID != "agent-1" {
		t.Fatalf("focusedAgentID = %q, want agent-1", m.focusedAgentID)
	}
	return &m
}

func viewportThinkingBlocks(m *Model, agentID string) []*Block {
	var blocks []*Block
	for _, b := range m.viewport.blocks {
		if b != nil && b.Type == BlockThinking && b.AgentID == agentID {
			blocks = append(blocks, b)
		}
	}
	return blocks
}

// TestMainThinkingRollbackClearsCardWhileSubAgentFocused covers the reported
// symptom: while the user views a SubAgent, the main agent completes a thinking
// round and that round is rolled back. The main thinking card must be removed
// from the transcript (including its settled form), and switching back to the
// main view must not resurrect it as the trailing message.
func TestMainThinkingRollbackClearsCardWhileSubAgentFocused(t *testing.T) {
	mainMsgs := mainTranscript(3, "main answer")
	subMsgs := mainTranscript(2, "sub answer")
	m := newFocusAwareModel(t, mainMsgs, subMsgs)

	_ = m.handleAgentEvent(agentEventMsg{event: agent.RequestCycleStartedEvent{AgentID: "main", TurnID: 1}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.ThinkingStartedEvent{AgentID: ""}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.StreamThinkingDeltaEvent{AgentID: "", Text: "scratch work"}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.StreamThinkingEvent{AgentID: "", Text: "scratch work"}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.StreamRollbackEvent{AgentID: "", Reason: "retry"}})

	if got := viewportThinkingBlocks(m, ""); len(got) != 0 {
		t.Fatalf("main thinking blocks after rollback = %d, want 0: %#v", len(got), got[0])
	}

	m.setFocusedAgent("")
	if got := viewportThinkingBlocks(m, ""); len(got) != 0 {
		t.Fatalf("main thinking blocks after returning to main view = %d, want 0", len(got))
	}
	visible := m.viewport.visibleBlocks()
	if len(visible) > 0 && visible[len(visible)-1].Type == BlockThinking {
		t.Fatalf("last visible main block = thinking, want committed transcript only")
	}
}

// TestMainThinkingCardIndexTracksMainTranscriptWhileSubAgentFocused pins the
// identity of a main thinking card to the main transcript length even when the
// user views a SubAgent. A sub-agent-derived index breaks rollback matching and
// thinking-translation lookups (the agent labels translation events by the real
// main message index).
func TestMainThinkingCardIndexTracksMainTranscriptWhileSubAgentFocused(t *testing.T) {
	mainMsgs := mainTranscript(5, "main answer")
	subMsgs := mainTranscript(1, "sub answer")
	m := newFocusAwareModel(t, mainMsgs, subMsgs)

	_ = m.handleAgentEvent(agentEventMsg{event: agent.RequestCycleStartedEvent{AgentID: "main", TurnID: 1}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.ThinkingStartedEvent{AgentID: ""}})

	if m.currentThinkingBlock == nil {
		t.Fatal("expected a live main thinking block")
	}
	if got, want := m.currentThinkingBlock.MsgIndex, len(mainMsgs); got != want {
		t.Fatalf("main thinking block MsgIndex = %d, want main transcript length %d", got, want)
	}
}

// TestMainThinkingRollbackAfterSubTranscriptGrows covers the multi-round case:
// the SubAgent transcript grows between the first and second main thinking
// rounds, so a rollback boundary derived from the sub transcript no longer
// matches the first round's card. Both uncommitted main rounds must be cleared.
func TestMainThinkingRollbackAfterSubTranscriptGrows(t *testing.T) {
	mainMsgs := mainTranscript(4, "main answer")
	m := newFocusAwareModel(t, mainMsgs, mainTranscript(2, "sub answer"))

	_ = m.handleAgentEvent(agentEventMsg{event: agent.RequestCycleStartedEvent{AgentID: "main", TurnID: 1}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.ThinkingStartedEvent{AgentID: ""}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.StreamThinkingDeltaEvent{AgentID: "", Text: "first round"}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.StreamThinkingEvent{AgentID: "", Text: "first round"}})

	// The SubAgent keeps working while the user watches it, growing its
	// transcript before the main agent's next thinking round starts.
	backend := m.agent.(*targetedConversationAgent)
	backend.messagesByFocus["agent-1"] = mainTranscript(5, "sub answer later")

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ThinkingStartedEvent{AgentID: ""}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.StreamThinkingDeltaEvent{AgentID: "", Text: "second round"}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.StreamThinkingEvent{AgentID: "", Text: "second round"}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.StreamRollbackEvent{AgentID: "", Reason: "retry"}})

	if got := viewportThinkingBlocks(m, ""); len(got) != 0 {
		t.Fatalf("main thinking blocks after rollback = %d, want 0 (first round leaked)", len(got))
	}
}

// TestMainThinkingTranslationMatchesRealMainIndexWhileSubAgentFocused covers
// the second symptom of the same wrong index: the agent labels a translation
// event with the real main message index ("msgidx:<n>"), so a card carrying a
// sub-agent-derived index never matches and its translation is dropped.
func TestMainThinkingTranslationMatchesRealMainIndexWhileSubAgentFocused(t *testing.T) {
	mainMsgs := mainTranscript(5, "main answer")
	subMsgs := mainTranscript(1, "sub answer")
	m := newFocusAwareModel(t, mainMsgs, subMsgs)

	_ = m.handleAgentEvent(agentEventMsg{event: agent.RequestCycleStartedEvent{AgentID: "main", TurnID: 1}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.ThinkingStartedEvent{AgentID: ""}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.StreamThinkingDeltaEvent{AgentID: "", Text: "scratch work"}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.StreamThinkingEvent{AgentID: "", Text: "scratch work"}})

	blocks := viewportThinkingBlocks(m, "")
	if len(blocks) != 1 {
		t.Fatalf("settled main thinking blocks = %d, want 1", len(blocks))
	}
	_ = m.handleAgentEvent(agentEventMsg{event: agent.ThinkingTranslatedEvent{
		AgentID:    "",
		MessageID:  fmt.Sprintf("msgidx:%d", len(mainMsgs)),
		BlockIndex: 0,
		Translated: "translated scratch work",
	}})
	got := blocks[0].ThinkingTranslations
	if len(got) == 0 || strings.TrimSpace(got[0].Content) != "translated scratch work" {
		t.Fatalf("translations = %#v, want the translation applied to the main thinking card", got)
	}
}

// TestMainThinkingStreamNotCutByActivityIdle pins the inverse invariant behind
// the "main thinking card never settles" concern: ActivityIdle must not settle
// a main thinking card. The agent emits ActivityIdle on transitions that resume
// work (compaction continuation, handoff, queued-input drain), where the stream
// keeps flowing, so cutting the card there would split it mid-thought. The main
// agent's settling signal is IdleEvent/GlobalIdleEvent, which finalizeTurn
// handles.
func TestMainThinkingStreamNotCutByActivityIdle(t *testing.T) {
	m := NewModelWithSize(&sessionControlAgent{}, 120, 24)

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ThinkingStartedEvent{}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.StreamThinkingDeltaEvent{Text: "still streaming"}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.AgentActivityEvent{AgentID: "main", Type: agent.ActivityIdle}})

	if m.currentThinkingBlock == nil || !m.currentThinkingBlock.Streaming {
		t.Fatal("main thinking card must survive ActivityIdle; IdleEvent owns settling")
	}

	_ = m.handleAgentEvent(agentEventMsg{event: agent.IdleEvent{}})
	if m.currentThinkingBlock != nil {
		t.Fatal("IdleEvent must settle the main thinking card")
	}
}

// TestMainAssistantMsgIndexFallbackWithoutTargetedController pins the fallback
// branch used when the backend does not implement the targeted conversation
// controller: the focused transcript length is trustworthy only while the main
// agent is focused, and any other focus must report an unknown boundary instead
// of a length that belongs to the SubAgent. The two transcripts differ in
// length so a regression that keeps reporting the focused transcript cannot
// pass by coincidence.
func TestMainAssistantMsgIndexFallbackWithoutTargetedController(t *testing.T) {
	mainMsgs := mainTranscript(3, "main answer")
	subMsgs := mainTranscript(2, "sub answer")
	backend := &sessionControlAgent{
		messagesByFocus: map[string][]message.Message{"": mainMsgs, "agent-1": subMsgs},
	}
	m := NewModelWithSize(backend, 120, 24)

	if got, want := m.currentMainAssistantMsgIndex(), len(mainMsgs); got != want {
		t.Fatalf("main-focused fallback index = %d, want main transcript length %d", got, want)
	}

	m.setFocusedAgent("agent-1")
	if m.focusedAgentID != "agent-1" {
		t.Fatalf("focusedAgentID = %q, want agent-1", m.focusedAgentID)
	}
	if got := m.currentMainAssistantMsgIndex(); got != -1 {
		t.Fatalf("sub-focused fallback index = %d, want -1 (main boundary unknown)", got)
	}
}

// TestMainThinkingRollbackClearsWhenMainBoundaryUnknown pins how the unknown
// index from the fallback is consumed: a main thinking card created while a
// SubAgent is focused carries MsgIndex -1, and rollback treats that unknown
// index conservatively — clearing every settled main thinking card instead of
// matching an index it cannot trust, so a stale card cannot survive by
// accident.
func TestMainThinkingRollbackClearsWhenMainBoundaryUnknown(t *testing.T) {
	m := NewModelWithSize(&sessionControlAgent{messages: mainTranscript(3, "main answer")}, 120, 24)
	m.setFocusedAgent("agent-1")

	_ = m.handleAgentEvent(agentEventMsg{event: agent.RequestCycleStartedEvent{AgentID: "main", TurnID: 1}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.ThinkingStartedEvent{AgentID: ""}})
	if m.currentThinkingBlock == nil {
		t.Fatal("expected a live main thinking block")
	}
	if got := m.currentThinkingBlock.MsgIndex; got != -1 {
		t.Fatalf("main thinking block MsgIndex = %d, want -1 without a trusted main boundary", got)
	}

	_ = m.handleAgentEvent(agentEventMsg{event: agent.StreamThinkingDeltaEvent{AgentID: "", Text: "scratch work"}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.StreamThinkingEvent{AgentID: "", Text: "scratch work"}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.StreamRollbackEvent{AgentID: "", Reason: "retry"}})

	if got := viewportThinkingBlocks(&m, ""); len(got) != 0 {
		t.Fatalf("main thinking blocks after rollback = %d, want 0", len(got))
	}
}
