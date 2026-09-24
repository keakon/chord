package acpagent

import (
	"strings"

	acp "github.com/coder/acp-go-sdk"

	"github.com/keakon/chord/internal/agent"
)

// eventEffects is what one Chord event means for the running turn, as opposed
// to the wire updates it produces.
type eventEffects struct {
	// busy records that the turn actually did work.
	busy bool
	// settle records global quiescence: the prompt may finish.
	settle bool
	// segment is the streaming segment a start or a segment end names, so a late
	// boundary from an older request cannot close a newer stream. Zero fields
	// mean the event carried no identity.
	segment streamSegment
	// streamStarted records that a main-agent request began streaming. Chord
	// coalesces those deltas and flushes the tail when the request goroutine
	// unwinds, so a turn can report idle before its last chunk is out.
	streamStarted bool
	// streamDrained records that such a request reported its segment end, which
	// it emits after that final flush.
	streamDrained bool
	// err is a main-agent error that aborts the turn.
	err error
	// recovered clears a previously recorded error: the agent produced a final
	// answer after it, so the turn succeeded after all.
	recovered bool
}

// streamSegment is the (TurnID, RequestSeq) identity the engine stamps on a
// streaming segment; one turn holds several requests when tools or compaction
// continue it. A zero field means unknown — events built by tests carry no
// identity — and matches any value.
type streamSegment struct {
	turnID     uint64
	requestSeq uint64
}

// segmentOf builds the identity a streaming event reports.
func segmentOf(turnID, requestSeq uint64) streamSegment {
	return streamSegment{turnID: turnID, requestSeq: requestSeq}
}

// sameSegment reports whether two identities can describe the same stream. An
// unknown field matches anything, because the engine leaves the pair at zero
// when it has no identity to report.
func sameSegment(a, b streamSegment) bool {
	if a.turnID != 0 && b.turnID != 0 && a.turnID != b.turnID {
		return false
	}
	if a.requestSeq != 0 && b.requestSeq != 0 && a.requestSeq != b.requestSeq {
		return false
	}
	return true
}

// eventMapper translates main-agent Chord events into ACP session updates.
// ACP text chunks are append-only, so only finalized AssistantMessageEvent
// text is published. Provisional deltas still drive stream-drain bookkeeping;
// thinking and tool progress remain incremental. No text buffer is needed.
type eventMapper struct{}

// Map converts one Chord event. Sub-agent events are dropped: the ACP client
// watches one conversation, and delegated work already shows up through the
// main agent's tool calls.
func (m *eventMapper) Map(ev agent.AgentEvent) ([]acp.SessionUpdate, eventEffects) {
	switch e := ev.(type) {
	case agent.StreamTextEvent:
		if e.AgentID != "" {
			return nil, eventEffects{}
		}
		return nil, eventEffects{busy: true, streamStarted: true, segment: segmentOf(e.TurnID, e.RequestSeq)}

	case agent.StreamTextCommitEvent:
		if e.AgentID != "" {
			return nil, eventEffects{}
		}
		// This confirms the provider reply before history accepts it. The
		// accepted message below owns publication, including terminal-only text.
		return nil, eventEffects{busy: true}

	case agent.StreamThinkingDeltaEvent:
		if e.AgentID != "" {
			return nil, eventEffects{}
		}
		segment := segmentOf(e.TurnID, e.RequestSeq)
		if e.Text == "" {
			return nil, eventEffects{busy: true, streamStarted: true, segment: segment}
		}
		return []acp.SessionUpdate{acp.UpdateAgentThoughtText(e.Text)}, eventEffects{busy: true, streamStarted: true, segment: segment}

	case agent.StreamThinkingEvent:
		if e.AgentID != "" {
			return nil, eventEffects{}
		}
		// An empty payload marks the end of a thinking block whose deltas were
		// already streamed; only a block that never streamed carries text here.
		segment := segmentOf(e.TurnID, e.RequestSeq)
		if e.Text == "" {
			return nil, eventEffects{busy: true, streamStarted: true, segment: segment}
		}
		return []acp.SessionUpdate{acp.UpdateAgentThoughtText(e.Text)}, eventEffects{busy: true, streamStarted: true, segment: segment}

	case agent.ThinkingStartedEvent:
		if e.AgentID != "" {
			return nil, eventEffects{}
		}
		// Thinking is flushed on a slower timer than text, so a request can be
		// mid-stream without any chunk having reached the client yet.
		return nil, eventEffects{busy: true, streamStarted: true, segment: segmentOf(e.TurnID, e.RequestSeq)}

	case agent.StreamSegmentEndedEvent:
		if e.AgentID != "" {
			return nil, eventEffects{}
		}
		// The request goroutine reports this after its final flush, which makes
		// it the only reliable "no more chunks for this turn" boundary: on a
		// cancelled turn the flush itself can land after the idle signal.
		return nil, eventEffects{busy: true, streamDrained: true, segment: segmentOf(e.TurnID, e.RequestSeq)}

	case agent.StreamRollbackEvent:
		if e.AgentID != "" {
			return nil, eventEffects{}
		}
		// Provisional text was never sent, so retries need no wire retraction.
		return nil, eventEffects{busy: true}

	case agent.ToolCallStartEvent:
		if e.AgentID != "" {
			return nil, eventEffects{}
		}
		return []acp.SessionUpdate{toolCallStart(e)}, eventEffects{busy: true}

	case agent.ToolCallUpdateEvent:
		if e.AgentID != "" {
			return nil, eventEffects{}
		}
		if update, ok := toolCallArgsUpdate(e); ok {
			return []acp.SessionUpdate{update}, eventEffects{busy: true}
		}
		return nil, eventEffects{busy: true}

	case agent.ToolCallDiscardEvent:
		if e.AgentID != "" {
			return nil, eventEffects{}
		}
		return []acp.SessionUpdate{toolCallDiscard(e)}, eventEffects{busy: true}

	case agent.ToolCallExecutionEvent:
		if e.AgentID != "" {
			return nil, eventEffects{}
		}
		if update, ok := toolCallExecutionUpdate(e); ok {
			return []acp.SessionUpdate{update}, eventEffects{busy: true}
		}
		return nil, eventEffects{busy: true}

	case agent.ToolProgressEvent:
		if e.AgentID != "" {
			return nil, eventEffects{}
		}
		if update, ok := toolCallProgressUpdate(e); ok {
			return []acp.SessionUpdate{update}, eventEffects{busy: true}
		}
		return nil, eventEffects{busy: true}

	case agent.ToolResultEvent:
		if e.AgentID != "" {
			return nil, eventEffects{}
		}
		return []acp.SessionUpdate{toolCallResult(e)}, eventEffects{busy: true}

	case agent.AssistantMessageEvent:
		if e.AgentID != "" {
			return nil, eventEffects{}
		}
		if strings.TrimSpace(e.Text) == "" {
			return nil, eventEffects{busy: true, recovered: true}
		}
		return []acp.SessionUpdate{acp.UpdateAgentMessageText(e.Text)}, eventEffects{busy: true, recovered: true}

	case agent.ErrorEvent:
		if e.AgentID != "" {
			return nil, eventEffects{}
		}
		effects := eventEffects{busy: true}
		if !e.Silent && e.Err != nil {
			effects.err = e.Err
		}
		return nil, effects

	case agent.GlobalIdleEvent:
		return nil, eventEffects{settle: true}

	case agent.IdleEvent:
		return nil, eventEffects{}

	case agent.AgentActivityEvent:
		if e.Type == agent.ActivityIdle {
			return nil, eventEffects{}
		}
		return nil, eventEffects{busy: true}

	default:
		// Loop notices, session switches, role or title changes, todos and
		// handoffs keep the turn open. None of them map onto a stable ACP
		// update, so they only feed turn bookkeeping.
		return nil, eventEffects{busy: true}
	}
}
