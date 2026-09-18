package tui

import (
	"testing"

	"github.com/keakon/chord/internal/agent"
)

// A focus switch rebuilds the focused transcript and carries live stream cards
// across. When the transcript is large enough to be windowed, the rebuild
// installs deferred clones rather than the live block pointers, so the stream
// state must be rebound to the cards the viewport actually renders. Otherwise
// later deltas keep updating orphaned blocks and the pane freezes at the
// content it held when the user switched away.
func TestFocusSwitchRebindKeepsLiveThinkingFollowingTail(t *testing.T) {
	mainMsgs := mainTranscript(startupTranscriptWindowMinBlocks+24, "main answer")
	m := newFocusAwareModel(t, mainMsgs, mainTranscript(2, "sub answer"))

	m.setFocusedAgent("")
	_ = m.handleAgentEvent(agentEventMsg{event: agent.ThinkingStartedEvent{AgentID: ""}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.StreamThinkingDeltaEvent{AgentID: "", Text: "partial"}})
	if m.currentThinkingBlock == nil {
		t.Fatal("expected a live main thinking block")
	}
	if !m.hasDeferredStartupTranscript() {
		t.Fatal("expected the large main transcript to be windowed")
	}
	thinkingID := m.currentThinkingBlock.ID

	m.setFocusedAgent("agent-1")
	m.setFocusedAgent("")
	if m.currentThinkingBlock == nil {
		t.Fatal("live main thinking block was dropped by the focus round-trip")
	}

	_ = m.handleAgentEvent(agentEventMsg{event: agent.StreamThinkingDeltaEvent{AgentID: "", Text: " and more"}})
	if !m.flushStreamingBlock(m.currentThinkingBlock, m.thinkingBlockAppended) {
		t.Fatal("expected the pending thinking delta to flush into the live card")
	}

	if got := m.currentThinkingBlock.Content; got != "partial and more" {
		t.Fatalf("live thinking content = %q, want %q", got, "partial and more")
	}
	block := m.viewport.GetFocusedBlock(thinkingID)
	if block == nil {
		t.Fatal("live thinking block is not in the focused viewport")
	}
	if got := block.Content; got != "partial and more" {
		t.Fatalf("viewport thinking content = %q, want %q: stream state points at a block the viewport no longer renders", got, "partial and more")
	}
}
