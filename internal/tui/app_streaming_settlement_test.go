package tui

import (
	"testing"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func TestLateStreamTailAfterIdleMergesIntoSettledCard(t *testing.T) {
	stubTUITicks(t)
	backend := &sessionControlAgent{}
	m := NewModelWithSize(backend, 120, 40)
	assistantCards := func() []*Block {
		var out []*Block
		for _, b := range m.viewport.visibleBlocks() {
			if b.Type == BlockAssistant {
				out = append(out, b)
			}
		}
		return out
	}

	// A normal streaming turn tagged with its turn and request sequence.
	cmd := m.handleAgentEvent(agentEventMsg{event: agent.StreamTextEvent{TurnID: 7, RequestSeq: 1, Text: "hello"}})
	applyTestCmd(t, &m, cmd)
	cmd = m.handleAgentEvent(agentEventMsg{event: agent.StreamTextEvent{TurnID: 7, RequestSeq: 1, Text: " world"}})
	applyTestCmd(t, &m, cmd)

	// ESC ends the turn, but the cancelled provider goroutine still flushes its
	// final sub-20ms batch afterwards. IdleEvent is only a scheduling signal
	// here: the card must stay streaming because its producer has not reported
	// the segment end yet.
	cmd = m.handleAgentEvent(agentEventMsg{event: agent.IdleEvent{}})
	applyTestCmd(t, &m, cmd)
	if m.currentAssistantBlock == nil || !m.currentAssistantBlock.Streaming {
		t.Fatal("idle must not settle a card whose producer has not reported its segment end")
	}

	// The flushed tail lands while the card still streams, so it appends instead
	// of opening a second card split mid-word.
	cmd = m.handleAgentEvent(agentEventMsg{event: agent.StreamTextEvent{TurnID: 7, RequestSeq: 1, Text: " tail"}})
	applyTestCmd(t, &m, cmd)
	cards := assistantCards()
	if len(cards) != 1 {
		t.Fatalf("assistant cards after idle and late tail = %d, want 1", len(cards))
	}

	// The producer reports the end of its segment: only now does the card settle.
	cmd = m.handleAgentEvent(agentEventMsg{event: agent.StreamSegmentEndedEvent{TurnID: 7, RequestSeq: 1}})
	applyTestCmd(t, &m, cmd)
	if m.currentAssistantBlock != nil {
		t.Fatal("segment end should detach the settled streaming block")
	}
	if got := cards[0].streamAccumulatedContent(); got != "hello world tail" {
		t.Fatalf("settled card content = %q, want merged tail", got)
	}

	// A batch arriving after the segment settled (reordered delivery) must fold
	// into the card that segment produced, not open a second one.
	cmd = m.handleAgentEvent(agentEventMsg{event: agent.StreamTextEvent{TurnID: 7, RequestSeq: 1, Text: " more"}})
	applyTestCmd(t, &m, cmd)
	if cards := assistantCards(); len(cards) != 1 {
		t.Fatalf("assistant cards after settled tail = %d, want 1", len(cards))
	}
	if got := cards[0].streamAccumulatedContent(); got != "hello world tail more" {
		t.Fatalf("settled card content = %q, want stale tail merged back", got)
	}

	// A new turn still opens a fresh card.
	cmd = m.handleAgentEvent(agentEventMsg{event: agent.StreamTextEvent{TurnID: 8, RequestSeq: 1, Text: "next"}})
	applyTestCmd(t, &m, cmd)
	if cards := assistantCards(); len(cards) != 2 {
		t.Fatalf("assistant cards after new turn = %d, want 2", len(cards))
	}

	// A stale tail arriving mid new-stream must not pollute the new card: it
	// merges back into its own settled card.
	cmd = m.handleAgentEvent(agentEventMsg{event: agent.StreamTextEvent{TurnID: 7, RequestSeq: 1, Text: " again"}})
	applyTestCmd(t, &m, cmd)
	cards = assistantCards()
	if len(cards) != 2 {
		t.Fatalf("assistant cards after stale tail = %d, want 2", len(cards))
	}
	for _, b := range cards {
		switch b.StreamTurnID {
		case 7:
			if got := b.streamAccumulatedContent(); got != "hello world tail more again" {
				t.Fatalf("old card content = %q, want stale tail merged back", got)
			}
		case 8:
			if got := b.streamAccumulatedContent(); got != "next" {
				t.Fatalf("new card content = %q, want untouched by stale tail", got)
			}
		default:
			t.Fatalf("unexpected card turn %d content %q", b.StreamTurnID, b.Content)
		}
	}
}

// TestStreamContinueRecordsSettledSegmentForLateTail pins that resuming a
// preserved stream interruption records the interrupted segment as settled: the
// request's error already reached the loop, so its producer is finished and the
// batch it flushed afterwards belongs to the card just settled. Without the
// record the tail opens a second card holding the last fragment of the same
// reply.
func TestStreamContinueRecordsSettledSegmentForLateTail(t *testing.T) {
	stubTUITicks(t)
	backend := &sessionControlAgent{}
	m := NewModelWithSize(backend, 120, 40)

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.StreamTextEvent{TurnID: 3, RequestSeq: 1, Text: "partial reply"}})
	applyTestCmd(t, &m, cmd)

	cmd = m.handleAgentEvent(agentEventMsg{event: agent.StreamContinueEvent{}})
	applyTestCmd(t, &m, cmd)

	// The cancelled producer's final sub-20ms batch lands after the resume
	// boundary, tagged with the segment it belongs to.
	cmd = m.handleAgentEvent(agentEventMsg{event: agent.StreamTextEvent{TurnID: 3, RequestSeq: 1, Text: " tail"}})
	applyTestCmd(t, &m, cmd)

	var cards []*Block
	for _, b := range m.viewport.visibleBlocks() {
		if b.Type == BlockAssistant {
			cards = append(cards, b)
		}
	}
	if len(cards) != 1 {
		t.Fatalf("assistant cards = %d, want the interrupted reply rendered once", len(cards))
	}
	if got := cards[0].streamAccumulatedContent(); got != "partial reply tail" {
		t.Fatalf("card content = %q, want the late tail merged into its own card", got)
	}
}

// TestLateThinkingPayloadAfterSettleKeepsOneCard pins that a thinking_end whose
// segment already settled does not open a second thinking card. The settled card
// already shows what the segment's deltas streamed, so the payload is dropped
// like any other late event with a settled owner.
func TestLateThinkingPayloadAfterSettleKeepsOneCard(t *testing.T) {
	stubTUITicks(t)
	backend := &sessionControlAgent{}
	m := NewModelWithSize(backend, 120, 40)

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.StreamThinkingDeltaEvent{TurnID: 9, RequestSeq: 1, Text: "weighing options"}})
	applyTestCmd(t, &m, cmd)
	cmd = m.handleAgentEvent(agentEventMsg{event: agent.StreamSegmentEndedEvent{TurnID: 9, RequestSeq: 1}})
	applyTestCmd(t, &m, cmd)

	// SubAgent reducers commit the full thinking block at thinking_end; a
	// thinking_end landing after the segment settled must not split it.
	cmd = m.handleAgentEvent(agentEventMsg{event: agent.StreamThinkingEvent{TurnID: 9, RequestSeq: 1, Text: "weighing options"}})
	applyTestCmd(t, &m, cmd)

	var cards []*Block
	for _, b := range m.viewport.visibleBlocks() {
		if b.Type == BlockThinking {
			cards = append(cards, b)
		}
	}
	if len(cards) != 1 {
		t.Fatalf("thinking cards = %d, want the settled block rendered once", len(cards))
	}
	if got := cards[0].streamAccumulatedContent(); got != "weighing options" {
		t.Fatalf("thinking card content = %q", got)
	}
}

// TestLateThinkingPayloadAfterSettleKeepsOneCardWhenSpilled pins that a late
// thinking_end of a settled segment still drops when that card has been
// spilled cold. The payload is the same block the card already showed; a
// second thinking card would render it twice. Late deltas of the same
// segment already drop on a cold card.
func TestLateThinkingPayloadAfterSettleKeepsOneCardWhenSpilled(t *testing.T) {
	stubTUITicks(t)
	backend := &sessionControlAgent{}
	m := NewModelWithSize(backend, 120, 40)

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.StreamThinkingDeltaEvent{TurnID: 9, RequestSeq: 1, Text: "weighing options"}})
	applyTestCmd(t, &m, cmd)
	cmd = m.handleAgentEvent(agentEventMsg{event: agent.StreamSegmentEndedEvent{TurnID: 9, RequestSeq: 1}})
	applyTestCmd(t, &m, cmd)

	spilled := false
	for _, b := range m.viewport.blocks {
		if b != nil && b.Type == BlockThinking && !b.Streaming {
			b.spillCold = true
			spilled = true
		}
	}
	if !spilled {
		t.Fatal("no settled thinking card found to mark spilled")
	}

	cmd = m.handleAgentEvent(agentEventMsg{event: agent.StreamThinkingEvent{TurnID: 9, RequestSeq: 1, Text: "weighing options"}})
	applyTestCmd(t, &m, cmd)

	var cards []*Block
	for _, b := range m.viewport.blocks {
		if b != nil && b.Type == BlockThinking {
			cards = append(cards, b)
		}
	}
	if len(cards) != 1 {
		t.Fatalf("thinking cards = %d, want the spilled settled block rendered once", len(cards))
	}
}

// TestLateThinkingPayloadGuardKeepsSecondThinkingBlock pins the boundary of the
// late-thinking_end guard: one segment can hold several thinking blocks
// (thinking -> tool call -> thinking again), and the first block's thinking_end
// settles that block without recording the segment. If it recorded the segment,
// the second block's payload would look late and be dropped.
//
// The blocks are read from the raw viewport because a SubAgent's cards are
// filtered out of the focused main transcript.
func TestLateThinkingPayloadGuardKeepsSecondThinkingBlock(t *testing.T) {
	stubTUITicks(t)
	const agentID = "agent-1"
	backend := &sessionControlAgent{}
	m := NewModelWithSize(backend, 120, 40)
	m.focusedAgentID = agentID

	thinkingBlocks := func() []*Block {
		var out []*Block
		for _, b := range m.viewport.blocks {
			if b.Type == BlockThinking {
				out = append(out, b)
			}
		}
		return out
	}

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.StreamThinkingDeltaEvent{AgentID: agentID, TurnID: 2, RequestSeq: 1, Text: "first round"}})
	applyTestCmd(t, &m, cmd)
	cmd = m.handleAgentEvent(agentEventMsg{event: agent.StreamThinkingEvent{AgentID: agentID, TurnID: 2, RequestSeq: 1, Text: "first round"}})
	applyTestCmd(t, &m, cmd)
	if got := len(thinkingBlocks()); got != 1 {
		t.Fatalf("thinking blocks after the first block = %d, want 1", got)
	}

	cmd = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallStartEvent{
		ID: "call-1", Name: tools.NameRead, ArgsJSON: `{"path":"README.md"}`, AgentID: agentID,
	}})
	applyTestCmd(t, &m, cmd)
	cmd = m.handleAgentEvent(agentEventMsg{event: agent.ThinkingStartedEvent{AgentID: agentID}})
	applyTestCmd(t, &m, cmd)
	cmd = m.handleAgentEvent(agentEventMsg{event: agent.StreamThinkingDeltaEvent{AgentID: agentID, TurnID: 2, RequestSeq: 1, Text: "second round"}})
	applyTestCmd(t, &m, cmd)
	cmd = m.handleAgentEvent(agentEventMsg{event: agent.StreamThinkingEvent{AgentID: agentID, TurnID: 2, RequestSeq: 1, Text: "second round"}})
	applyTestCmd(t, &m, cmd)

	blocks := thinkingBlocks()
	if len(blocks) != 2 {
		t.Fatalf("thinking blocks = %d, want one per thinking block of the segment", len(blocks))
	}
	if got := blocks[0].streamAccumulatedContent(); got != "first round" {
		t.Fatalf("first thinking block = %q", got)
	}
	if got := blocks[1].streamAccumulatedContent(); got != "second round" {
		t.Fatalf("second thinking block = %q, want the payload rendered rather than dropped as late", got)
	}

	// Once the producer reports its segment end, a further payload is late and
	// must not open a third card.
	cmd = m.handleAgentEvent(agentEventMsg{event: agent.StreamSegmentEndedEvent{AgentID: agentID, TurnID: 2, RequestSeq: 1}})
	applyTestCmd(t, &m, cmd)
	cmd = m.handleAgentEvent(agentEventMsg{event: agent.StreamThinkingEvent{AgentID: agentID, TurnID: 2, RequestSeq: 1, Text: "third round"}})
	applyTestCmd(t, &m, cmd)
	if got := len(thinkingBlocks()); got != 2 {
		t.Fatalf("thinking blocks after a post-segment payload = %d, want 2", got)
	}
}

// TestLateThinkingEndDoesNotSettleNewerLiveCard pins that a cancelled older
// segment's thinking_end must not settle the card a newer segment is still
// streaming into: settling it detaches the card, so the newer segment's next
// delta opens a second card holding the rest of the same reasoning.
func TestLateThinkingEndDoesNotSettleNewerLiveCard(t *testing.T) {
	stubTUITicks(t)
	backend := &sessionControlAgent{}
	m := NewModelWithSize(backend, 120, 40)

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.ThinkingStartedEvent{TurnID: 2, RequestSeq: 1}})
	applyTestCmd(t, &m, cmd)
	cmd = m.handleAgentEvent(agentEventMsg{event: agent.StreamThinkingDeltaEvent{TurnID: 2, RequestSeq: 1, Text: "newer reasoning"}})
	applyTestCmd(t, &m, cmd)
	if m.currentThinkingBlock == nil || !m.currentThinkingBlock.Streaming {
		t.Fatal("precondition: the newer segment's card should be live")
	}

	cmd = m.handleAgentEvent(agentEventMsg{event: agent.StreamThinkingEvent{TurnID: 1, RequestSeq: 1}})
	applyTestCmd(t, &m, cmd)
	if m.currentThinkingBlock == nil || !m.currentThinkingBlock.Streaming {
		t.Fatal("an older segment's thinking_end must not settle the newer live card")
	}

	cmd = m.handleAgentEvent(agentEventMsg{event: agent.StreamThinkingDeltaEvent{TurnID: 2, RequestSeq: 1, Text: " and more"}})
	applyTestCmd(t, &m, cmd)
	if got := m.currentThinkingBlock.streamAccumulatedContent(); got != "newer reasoning and more" {
		t.Fatalf("thinking content = %q, want the newer segment on one live card", got)
	}
}

// TestPostRebuildDeltaOpensItsOwnCard pins the deliberate tradeoff of clearing
// the settled record on a transcript rebuild: a rebuild can land while a producer
// is still streaming, so its remaining deltas open a card of their own instead of
// folding into a card the rebuild just dropped (which would lose them).
func TestPostRebuildDeltaOpensItsOwnCard(t *testing.T) {
	stubTUITicks(t)
	backend := &sessionControlAgent{messages: []message.Message{
		{Role: message.RoleUser, Content: "hi"},
		{Role: message.RoleAssistant, Content: "hello world"},
	}}
	m := NewModelWithSize(backend, 120, 40)

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.StreamTextEvent{TurnID: 5, RequestSeq: 1, Text: "hello world"}})
	applyTestCmd(t, &m, cmd)
	cmd = m.handleAgentEvent(agentEventMsg{event: agent.StreamSegmentEndedEvent{TurnID: 5, RequestSeq: 1}})
	applyTestCmd(t, &m, cmd)

	assistantCards := func() []*Block {
		var out []*Block
		for _, b := range m.viewport.visibleBlocks() {
			if b.Type == BlockAssistant {
				out = append(out, b)
			}
		}
		return out
	}
	before := len(assistantCards())

	m.rebuildViewportFromMessagesPreservingActivity("test", false)
	afterRebuild := len(assistantCards())

	cmd = m.handleAgentEvent(agentEventMsg{event: agent.StreamTextEvent{TurnID: 5, RequestSeq: 1, Text: " tail"}})
	applyTestCmd(t, &m, cmd)

	if got := len(assistantCards()); got != afterRebuild+1 {
		t.Fatalf("assistant cards = %d, want %d: a post-rebuild delta must be able to open its own card (before rebuild %d)",
			got, afterRebuild+1, before)
	}
}

// TestStreamSegmentEndSeparatesRequestsOfOneTurn pins that the request sequence,
// not the turn, is what identifies a streaming segment: a newer request of the
// same turn must not append its text to the previous request's card.
func TestStreamSegmentEndSeparatesRequestsOfOneTurn(t *testing.T) {
	stubTUITicks(t)
	backend := &sessionControlAgent{}
	m := NewModelWithSize(backend, 120, 40)

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.StreamTextEvent{TurnID: 5, RequestSeq: 1, Text: "first answer"}})
	applyTestCmd(t, &m, cmd)
	cmd = m.handleAgentEvent(agentEventMsg{event: agent.StreamSegmentEndedEvent{TurnID: 5, RequestSeq: 1}})
	applyTestCmd(t, &m, cmd)
	if !m.streamSegmentEnded("", m.viewport.visibleBlocks()[0]) {
		t.Fatal("precondition failed: first segment should be reported ended")
	}

	cmd = m.handleAgentEvent(agentEventMsg{event: agent.StreamTextEvent{TurnID: 5, RequestSeq: 2, Text: "second answer"}})
	applyTestCmd(t, &m, cmd)

	var cards []*Block
	for _, b := range m.viewport.visibleBlocks() {
		if b.Type == BlockAssistant {
			cards = append(cards, b)
		}
	}
	if len(cards) != 2 {
		t.Fatalf("assistant cards = %d, want one card per request segment", len(cards))
	}
	if got := cards[0].streamAccumulatedContent(); got != "first answer" {
		t.Fatalf("first card content = %q, want only the first request's text", got)
	}
	if got := cards[1].streamAccumulatedContent(); got != "second answer" {
		t.Fatalf("second card content = %q, want the second request's text", got)
	}

	// Losing the first segment's end event must not strand its card once a newer
	// segment streams: the newer request settles the stale card instead of
	// appending into it.
	cmd = m.handleAgentEvent(agentEventMsg{event: agent.StreamTextEvent{TurnID: 5, RequestSeq: 3, Text: "third answer"}})
	applyTestCmd(t, &m, cmd)
	if cards[0].Streaming {
		t.Fatal("a card left behind by an unreported segment must settle when a newer segment streams")
	}
	if cards[1].Streaming {
		t.Fatal("the previous request's card must settle when a newer request streams")
	}
}

// TestPendingDraftCommitKeepsUnendedStreamStreaming pins the second
// TUI-finalizing event of a cancellation: the pending-draft commit appends the
// queued user card but must not settle a streaming assistant card whose producer
// has not reported its segment end.
func TestPendingDraftCommitKeepsUnendedStreamStreaming(t *testing.T) {
	stubTUITicks(t)
	backend := &sessionControlAgent{}
	m := NewModelWithSize(backend, 120, 40)

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.StreamTextEvent{TurnID: 4, RequestSeq: 2, Text: "partial reply"}})
	applyTestCmd(t, &m, cmd)

	cmd = m.handleAgentEvent(agentEventMsg{event: agent.PendingDraftConsumedEvent{
		DraftID: "draft-1",
		Parts:   []message.ContentPart{{Type: message.ContentPartText, Text: "queued while streaming"}},
	}})
	applyTestCmd(t, &m, cmd)
	if m.currentAssistantBlock == nil || !m.currentAssistantBlock.Streaming {
		t.Fatal("pending-draft commit must not settle a card whose producer has not reported its segment end")
	}

	cmd = m.handleAgentEvent(agentEventMsg{event: agent.StreamSegmentEndedEvent{TurnID: 4, RequestSeq: 2}})
	applyTestCmd(t, &m, cmd)
	if m.currentAssistantBlock != nil {
		t.Fatal("segment end should settle the card after the deferred commit")
	}
}

// TestSubAgentActivityIdleKeepsUnendedStreamStreaming pins the same gating for a
// SubAgent's ActivityIdle, which is emitted before its request goroutine flushes
// the tail of the cancelled request.
func TestSubAgentActivityIdleKeepsUnendedStreamStreaming(t *testing.T) {
	stubTUITicks(t)
	backend := &sessionControlAgent{}
	m := NewModelWithSize(backend, 120, 40)

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.StreamTextEvent{AgentID: "agent-1", TurnID: 2, RequestSeq: 1, Text: "worker reply"}})
	applyTestCmd(t, &m, cmd)

	cmd = m.handleAgentEvent(agentEventMsg{event: agent.AgentActivityEvent{AgentID: "agent-1", Type: agent.ActivityIdle}})
	applyTestCmd(t, &m, cmd)
	if state := m.streamState("agent-1"); state.assistant == nil || !state.assistant.Streaming {
		t.Fatal("ActivityIdle must not settle a worker card whose producer has not reported its segment end")
	}

	cmd = m.handleAgentEvent(agentEventMsg{event: agent.StreamTextEvent{AgentID: "agent-1", TurnID: 2, RequestSeq: 1, Text: " tail"}})
	applyTestCmd(t, &m, cmd)

	cmd = m.handleAgentEvent(agentEventMsg{event: agent.StreamSegmentEndedEvent{AgentID: "agent-1", TurnID: 2, RequestSeq: 1}})
	applyTestCmd(t, &m, cmd)
	if state := m.streamState("agent-1"); state.assistant != nil {
		t.Fatal("segment end should detach the settled worker card")
	}
}

// TestStreamingReplySplitMidFenceStaysInOneCard replays the shape of the reply
// that first reported this bug: ESC cancels the request in the middle of a
// backtick-quoted word, and the cancelled request's last batch carries the rest
// of that word. The tail must land in the card that already streamed the
// opening, and the card settles only on the producer's segment end — not on the
// scheduling idle that follows the cancel.
func TestStreamingReplySplitMidFenceStaysInOneCard(t *testing.T) {
	stubTUITicks(t)
	backend := &sessionControlAgent{}
	m := NewModelWithSize(backend, 120, 40)
	assistantCards := func() []*Block {
		var out []*Block
		for _, b := range m.viewport.visibleBlocks() {
			if b.Type == BlockAssistant {
				out = append(out, b)
			}
		}
		return out
	}
	const opening = "Files touched: `cmd/chord/headless.go`, `cmd/chord/headless_event_contract_test"

	// First batch: the reply stops right before ".go".
	cmd := m.handleAgentEvent(agentEventMsg{event: agent.StreamTextEvent{TurnID: 11, RequestSeq: 1, Text: opening}})
	applyTestCmd(t, &m, cmd)

	// ESC ends the turn, but the cancelled request still holds the rest of the
	// batch. The idle is a scheduling signal, not a settle authority.
	cmd = m.handleAgentEvent(agentEventMsg{event: agent.IdleEvent{}})
	applyTestCmd(t, &m, cmd)
	if cards := assistantCards(); len(cards) != 1 || !cards[0].Streaming {
		t.Fatalf("assistant cards after idle = %d, want the still-streaming card", len(cards))
	}

	// The final batch closes the quoted word and opens the next one.
	cmd = m.handleAgentEvent(agentEventMsg{event: agent.StreamTextEvent{TurnID: 11, RequestSeq: 1, Text: ".go`, then `"}})
	applyTestCmd(t, &m, cmd)
	cmd = m.handleAgentEvent(agentEventMsg{event: agent.StreamTextEvent{TurnID: 11, RequestSeq: 1, Text: "docs/headless"}})
	applyTestCmd(t, &m, cmd)

	cards := assistantCards()
	if len(cards) != 1 {
		t.Fatalf("assistant cards after the flushed tail = %d, want 1: the tail must not open a second card", len(cards))
	}
	if !cards[0].Streaming {
		t.Fatal("the card must stay streaming until the producer reports its segment end")
	}

	// Only the producer's segment end settles the card.
	cmd = m.handleAgentEvent(agentEventMsg{event: agent.StreamSegmentEndedEvent{TurnID: 11, RequestSeq: 1}})
	applyTestCmd(t, &m, cmd)
	if m.currentAssistantBlock != nil {
		t.Fatal("segment end should detach the settled card")
	}
	if got, want := cards[0].streamAccumulatedContent(), opening+".go`, then `docs/headless"; got != want {
		t.Fatalf("card content = %q, want the split reply rejoined into one card %q", got, want)
	}
}

// TestSegmentEndSettlesThinkingCardWithoutAssistant pins the thinking side of
// the settle authority: the segment end settles a thinking card even when the
// request produced no assistant text.
func TestSegmentEndSettlesThinkingCardWithoutAssistant(t *testing.T) {
	stubTUITicks(t)
	backend := &sessionControlAgent{}
	m := NewModelWithSize(backend, 120, 40)

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.StreamThinkingDeltaEvent{TurnID: 9, RequestSeq: 1, Text: "weighing the options"}})
	applyTestCmd(t, &m, cmd)
	if m.currentThinkingBlock == nil || !m.currentThinkingBlock.Streaming {
		t.Fatal("expected a live streaming thinking card")
	}

	cmd = m.handleAgentEvent(agentEventMsg{event: agent.StreamSegmentEndedEvent{TurnID: 9, RequestSeq: 1}})
	applyTestCmd(t, &m, cmd)
	if m.currentThinkingBlock != nil {
		t.Fatal("segment end must settle a thinking card whose request streamed no assistant text")
	}
	if got := m.streamState("").thinking; got != nil {
		t.Fatal("stream state must detach the settled thinking card")
	}
}

// TestLateSegmentEndDoesNotSettleNewerThinkingPlaceholder pins the
// ThinkingStartedEvent identity: a cancelled request's segment end can drain
// after the next request has already opened a thinking placeholder. The
// placeholder carries the new segment, so the older end must not settle it —
// that would split the new reply onto a second thinking card once the first
// delta arrives.
func TestLateSegmentEndDoesNotSettleNewerThinkingPlaceholder(t *testing.T) {
	stubTUITicks(t)
	backend := &sessionControlAgent{}
	m := NewModelWithSize(backend, 120, 40)

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.ThinkingStartedEvent{TurnID: 2, RequestSeq: 1}})
	applyTestCmd(t, &m, cmd)
	if m.currentThinkingBlock == nil || !m.currentThinkingBlock.Streaming {
		t.Fatal("expected a live thinking placeholder")
	}
	if m.currentThinkingBlock.StreamTurnID != 2 || m.currentThinkingBlock.StreamRequestSeq != 1 {
		t.Fatalf("placeholder identity = (%d, %d), want (2, 1)", m.currentThinkingBlock.StreamTurnID, m.currentThinkingBlock.StreamRequestSeq)
	}

	cmd = m.handleAgentEvent(agentEventMsg{event: agent.StreamSegmentEndedEvent{TurnID: 1, RequestSeq: 1}})
	applyTestCmd(t, &m, cmd)
	if m.currentThinkingBlock == nil || !m.currentThinkingBlock.Streaming {
		t.Fatal("an older segment end must not settle the newer thinking placeholder")
	}

	cmd = m.handleAgentEvent(agentEventMsg{event: agent.StreamThinkingDeltaEvent{TurnID: 2, RequestSeq: 1, Text: "next request reasoning"}})
	applyTestCmd(t, &m, cmd)
	if m.currentThinkingBlock == nil {
		t.Fatal("the first delta must continue the same placeholder")
	}
	if got := m.currentThinkingBlock.streamAccumulatedContent(); got != "next request reasoning" {
		t.Fatalf("thinking content = %q, want the new request's delta on the same card", got)
	}

	var cards []*Block
	for _, b := range m.viewport.visibleBlocks() {
		if b.Type == BlockThinking {
			cards = append(cards, b)
		}
	}
	if len(cards) != 1 {
		t.Fatalf("thinking cards = %d, want the new request on one card", len(cards))
	}
}

// TestLateThinkingTailMergesIntoSettledThinkingCard pins the thinking counterpart
// of the late-text-tail fallback: a delta arriving after its thinking card
// settled merges back into that card instead of opening a second one.
func TestLateThinkingTailMergesIntoSettledThinkingCard(t *testing.T) {
	stubTUITicks(t)
	backend := &sessionControlAgent{}
	m := NewModelWithSize(backend, 120, 40)
	thinkingCards := func() []*Block {
		var out []*Block
		for _, b := range m.viewport.visibleBlocks() {
			if b.Type == BlockThinking {
				out = append(out, b)
			}
		}
		return out
	}

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.StreamThinkingDeltaEvent{TurnID: 6, RequestSeq: 2, Text: "checking the reducer"}})
	applyTestCmd(t, &m, cmd)
	cmd = m.handleAgentEvent(agentEventMsg{event: agent.StreamSegmentEndedEvent{TurnID: 6, RequestSeq: 2}})
	applyTestCmd(t, &m, cmd)
	if cards := thinkingCards(); len(cards) != 1 {
		t.Fatalf("thinking cards after segment end = %d, want 1", len(cards))
	}

	cmd = m.handleAgentEvent(agentEventMsg{event: agent.StreamThinkingDeltaEvent{TurnID: 6, RequestSeq: 2, Text: " once more"}})
	applyTestCmd(t, &m, cmd)
	cards := thinkingCards()
	if len(cards) != 1 {
		t.Fatalf("thinking cards after the late tail = %d, want the tail merged into the settled card", len(cards))
	}
	if got := cards[0].streamAccumulatedContent(); got != "checking the reducer once more" {
		t.Fatalf("thinking card content = %q, want the late tail merged back", got)
	}
}

// TestSameTurnNextRequestOpensNewCard pins the request granularity of late-delta
// attribution: once an idle has recorded the first request's segment as settled,
// a second request of the same turn must still open a card of its own. Turn-level
// attribution would take its text for a stale tail and drop it.
func TestSameTurnNextRequestOpensNewCard(t *testing.T) {
	stubTUITicks(t)
	backend := &sessionControlAgent{}
	m := NewModelWithSize(backend, 120, 40)

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.StreamTextEvent{TurnID: 7, RequestSeq: 1, Text: "first request"}})
	applyTestCmd(t, &m, cmd)
	cmd = m.handleAgentEvent(agentEventMsg{event: agent.StreamSegmentEndedEvent{TurnID: 7, RequestSeq: 1}})
	applyTestCmd(t, &m, cmd)
	// The turn's scheduling idle records the settled segment for the agent.
	cmd = m.handleAgentEvent(agentEventMsg{event: agent.IdleEvent{}})
	applyTestCmd(t, &m, cmd)

	cmd = m.handleAgentEvent(agentEventMsg{event: agent.StreamTextEvent{TurnID: 7, RequestSeq: 2, Text: "second request"}})
	applyTestCmd(t, &m, cmd)

	var cards []*Block
	for _, b := range m.viewport.visibleBlocks() {
		if b.Type == BlockAssistant {
			cards = append(cards, b)
		}
	}
	if len(cards) != 2 {
		t.Fatalf("assistant cards = %d, want one card per request of the turn", len(cards))
	}
	if got := cards[0].streamAccumulatedContent(); got != "first request" {
		t.Fatalf("first card content = %q, want only the first request's text", got)
	}
	if got, want := cards[1].streamAccumulatedContent(), "second request"; got != want {
		t.Fatalf("second card content = %q, want %q", got, want)
	}
}
