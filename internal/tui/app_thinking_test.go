package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
)

func TestThinkingStreamEndsOnStreamThinkingEvent(t *testing.T) {
	// When StreamThinkingEvent (thinking_end) arrives, the current thinking
	// card is settled and detached so the next round starts a fresh card.
	m := NewModelWithSize(nil, 80, 12)

	// Start thinking.
	_ = m.handleAgentEvent(agentEventMsg{event: agent.ThinkingStartedEvent{}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.StreamThinkingDeltaEvent{Text: "I need to analyze this carefully.", AgentID: ""}})

	// thinking_end settles the block and detaches it.
	_ = m.handleAgentEvent(agentEventMsg{event: agent.StreamThinkingEvent{AgentID: ""}})

	if m.currentThinkingBlock != nil {
		t.Fatal("expected currentThinkingBlock to be detached after StreamThinkingEvent so the next round starts a fresh card")
	}
	thinkingBlock := findThinkingBlockInViewport(m.viewport)
	if thinkingBlock == nil {
		t.Fatal("expected a thinking block in viewport after thinking_end")
	}
	if thinkingBlock.Streaming {
		t.Fatal("expected settled thinking block to have Streaming=false")
	}

	// A subsequent tool call must not alter the already-settled block.
	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallStartEvent{
		ID:   "call-1",
		Name: "read",
	}})

	thinkingBlock = findThinkingBlockInViewport(m.viewport)
	if thinkingBlock == nil {
		t.Fatal("expected a thinking block in viewport")
	}
}

func TestSubAgentThinkingStreamEndsOnStreamThinkingEvent(t *testing.T) {
	// SubAgent reducers commit the full thinking block at thinking_end.
	m := NewModelWithSize(nil, 80, 12)
	_ = m.handleAgentEvent(agentEventMsg{event: agent.StreamThinkingDeltaEvent{Text: "Planning safe branch update preserving changes", AgentID: "sub-1"}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.StreamThinkingEvent{Text: "Planning safe branch update preserving changes", AgentID: "sub-1"}})

	thinkingBlock := findThinkingBlockInViewport(m.viewport)
	if thinkingBlock == nil {
		// SubAgent blocks are filtered out of visibleBlocks() by the default
		// main-agent filter, so fall back to the raw block list.
		for _, b := range m.viewport.blocks {
			if b.Type == BlockThinking && b.AgentID == "sub-1" {
				thinkingBlock = b
				break
			}
		}
	}
	if thinkingBlock == nil {
		t.Fatal("expected a sub-agent thinking block after thinking_end")
	}
	if thinkingBlock.Streaming {
		t.Fatal("expected settled sub-agent thinking block to have Streaming=false")
	}
	rendered := stripANSI(strings.Join(thinkingBlock.Render(80, ""), "\n"))
	if !strings.Contains(rendered, "Planning safe branch update preserving changes") {
		t.Fatalf("expected rendered sub-agent thinking to keep content; got:\n%s", rendered)
	}
}

func TestThinkingTranslatedEventTargetsExactThinkingBlock(t *testing.T) {
	m := NewModelWithSize(nil, 80, 12)
	first := &Block{ID: 1, Type: BlockThinking, Content: "first", MsgIndex: 2, ThinkingBlockIndex: 0}
	second := &Block{ID: 2, Type: BlockThinking, Content: "second", MsgIndex: 3, ThinkingBlockIndex: 0}
	m.viewport.AppendBlock(first)
	m.viewport.AppendBlock(second)

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ThinkingTranslatedEvent{MessageID: "msgidx:2", BlockIndex: 0, Translated: "bonjour", TargetLang: "fr"}})

	if got := strings.TrimSpace(first.ThinkingTranslations[0].Content); got != "bonjour" {
		t.Fatalf("first translated content = %q, want bonjour", got)
	}
	if len(second.ThinkingTranslations) != 0 {
		t.Fatalf("second block should remain untranslated, got %#v", second.ThinkingTranslations)
	}
}

func TestThinkingTranslatedEventDoesNotFallbackToNearestThinkingBlock(t *testing.T) {
	m := NewModelWithSize(nil, 80, 12)
	block := &Block{ID: 1, Type: BlockThinking, Content: "first", MsgIndex: 7, ThinkingBlockIndex: 0}
	m.viewport.AppendBlock(block)

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ThinkingTranslatedEvent{MessageID: "msgidx:99", BlockIndex: 0, Translated: "bonjour", TargetLang: "fr"}})

	if len(block.ThinkingTranslations) != 0 {
		t.Fatalf("translation should not apply on mismatched message id, got %#v", block.ThinkingTranslations)
	}
}

func TestThinkingTranslatedEventRequiresMatchingOriginalHash(t *testing.T) {
	m := NewModelWithSize(nil, 80, 12)
	block := &Block{ID: 1, Type: BlockThinking, Content: "retry thought", MsgIndex: 7, ThinkingBlockIndex: 0}
	m.viewport.AppendBlock(block)

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ThinkingTranslatedEvent{
		MessageID:    "msgidx:7",
		BlockIndex:   0,
		Translated:   "stale",
		TargetLang:   "zh-Hans",
		OriginalHash: recovery.ThinkingTranslationOriginalHash("failed thought"),
	}})
	if len(block.ThinkingTranslations) != 0 {
		t.Fatalf("translation should not apply on mismatched original hash, got %#v", block.ThinkingTranslations)
	}

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ThinkingTranslatedEvent{
		MessageID:    "msgidx:7",
		BlockIndex:   0,
		Translated:   "重试",
		TargetLang:   "zh-Hans",
		OriginalHash: recovery.ThinkingTranslationOriginalHash("retry thought"),
	}})
	if got := strings.TrimSpace(block.ThinkingTranslations[0].Content); got != "重试" {
		t.Fatalf("translated content = %q, want 重试", got)
	}
}

func TestThinkingTranslatedEventDeduplicatesStructuredTranslation(t *testing.T) {
	m := NewModelWithSize(nil, 80, 12)
	block := &Block{ID: 1, Type: BlockThinking, Content: "first", MsgIndex: 2, ThinkingBlockIndex: 0}
	m.viewport.AppendBlock(block)
	evt := agent.ThinkingTranslatedEvent{MessageID: "msgidx:2", BlockIndex: 0, Translated: "bonjour", TargetLang: "fr"}

	_ = m.handleAgentEvent(agentEventMsg{event: evt})
	_ = m.handleAgentEvent(agentEventMsg{event: evt})

	if len(block.ThinkingTranslations) != 1 {
		t.Fatalf("translations len = %d, want 1", len(block.ThinkingTranslations))
	}
	if got := block.ThinkingTranslations[0].Content; got != "bonjour" {
		t.Fatalf("translated content = %q, want bonjour", got)
	}
}

func TestThinkingTranslatedEventAppliesToStreamingThinkingBlockWithMessageIndex(t *testing.T) {
	backend := &sessionControlAgent{messages: []message.Message{{Role: "user", Content: "hello"}}}
	m := NewModelWithSize(backend, 80, 12)

	_ = m.handleAgentEvent(agentEventMsg{event: agent.RequestCycleStartedEvent{TurnID: 1}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.ThinkingStartedEvent{}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.StreamThinkingDeltaEvent{Text: "Plan", AgentID: ""}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.StreamThinkingEvent{Text: "", AgentID: ""}})

	blocks := m.viewport.visibleBlocks()
	var thinkingBlock *Block
	for _, b := range blocks {
		if b.Type == BlockThinking {
			thinkingBlock = b
			break
		}
	}
	if thinkingBlock == nil {
		t.Fatal("expected thinking block")
	}
	if thinkingBlock.Streaming {
		t.Fatal("thinking block should be settled")
	}
	if thinkingBlock.MsgIndex != 1 {
		t.Fatalf("MsgIndex = %d, want next assistant message index 1", thinkingBlock.MsgIndex)
	}
	if thinkingBlock.ThinkingBlockIndex != 0 {
		t.Fatalf("ThinkingBlockIndex = %d, want 0", thinkingBlock.ThinkingBlockIndex)
	}

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ThinkingTranslatedEvent{MessageID: "msgidx:1", BlockIndex: 0, Translated: "计划", TargetLang: "zh-Hans"}})
	if got := strings.TrimSpace(thinkingBlock.ThinkingTranslations[0].Content); got != "计划" {
		t.Fatalf("translated content = %q, want 计划", got)
	}
}

func TestStreamingThinkingMsgIndexCatchesUpWhenMessagesAdvanceBeforeEvent(t *testing.T) {
	backend := &sessionControlAgent{messages: []message.Message{{Role: "user", Content: "hello"}}}
	m := NewModelWithSize(backend, 80, 12)

	_ = m.handleAgentEvent(agentEventMsg{event: agent.RequestCycleStartedEvent{TurnID: 1}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.StreamThinkingDeltaEvent{Text: "first", AgentID: ""}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.StreamThinkingEvent{Text: "", AgentID: ""}})

	backend.messages = append(backend.messages,
		message.Message{Role: "assistant", Content: "", ToolCalls: []message.ToolCall{{ID: "call-1", Name: "shell"}}},
		message.Message{Role: "tool", Content: "result", ToolCallID: "call-1"},
		message.Message{Role: "tool", Content: "result", ToolCallID: "call-2"},
	)

	_ = m.handleAgentEvent(agentEventMsg{event: agent.StreamThinkingDeltaEvent{Text: "second", AgentID: ""}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.StreamThinkingEvent{Text: "", AgentID: ""}})

	var thinkingBlocks []*Block
	for _, b := range m.viewport.visibleBlocks() {
		if b.Type == BlockThinking {
			thinkingBlocks = append(thinkingBlocks, b)
		}
	}
	if len(thinkingBlocks) != 2 {
		t.Fatalf("thinking block count = %d, want 2", len(thinkingBlocks))
	}
	if thinkingBlocks[0].MsgIndex != 1 || thinkingBlocks[1].MsgIndex != 4 {
		t.Fatalf("MsgIndex = [%d,%d], want [1,4]", thinkingBlocks[0].MsgIndex, thinkingBlocks[1].MsgIndex)
	}
	if thinkingBlocks[0].ThinkingBlockIndex != 0 || thinkingBlocks[1].ThinkingBlockIndex != 0 {
		t.Fatalf("ThinkingBlockIndex = [%d,%d], want [0,0]", thinkingBlocks[0].ThinkingBlockIndex, thinkingBlocks[1].ThinkingBlockIndex)
	}

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ThinkingTranslatedEvent{MessageID: "msgidx:4", BlockIndex: 0, Translated: "第二", TargetLang: "zh-Hans"}})
	if len(thinkingBlocks[0].ThinkingTranslations) != 0 {
		t.Fatalf("first block should not receive second assistant translation, got %#v", thinkingBlocks[0].ThinkingTranslations)
	}
	if got := strings.TrimSpace(thinkingBlocks[1].ThinkingTranslations[0].Content); got != "第二" {
		t.Fatalf("second translated content = %q, want 第二", got)
	}
}

func TestStreamingThinkingBlocksIncrementThinkingBlockIndex(t *testing.T) {
	backend := &sessionControlAgent{messages: []message.Message{{Role: "user", Content: "hello"}}}
	m := NewModelWithSize(backend, 80, 12)

	_ = m.handleAgentEvent(agentEventMsg{event: agent.RequestCycleStartedEvent{TurnID: 1}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.StreamThinkingDeltaEvent{Text: "first", AgentID: ""}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.StreamThinkingEvent{Text: "", AgentID: ""}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.StreamThinkingDeltaEvent{Text: "second", AgentID: ""}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.StreamThinkingEvent{Text: "", AgentID: ""}})

	var thinkingBlocks []*Block
	for _, b := range m.viewport.visibleBlocks() {
		if b.Type == BlockThinking {
			thinkingBlocks = append(thinkingBlocks, b)
		}
	}
	if len(thinkingBlocks) != 2 {
		t.Fatalf("thinking block count = %d, want 2", len(thinkingBlocks))
	}
	if thinkingBlocks[0].MsgIndex != 1 || thinkingBlocks[1].MsgIndex != 1 {
		t.Fatalf("MsgIndex = [%d,%d], want [1,1]", thinkingBlocks[0].MsgIndex, thinkingBlocks[1].MsgIndex)
	}
	if thinkingBlocks[0].ThinkingBlockIndex != 0 || thinkingBlocks[1].ThinkingBlockIndex != 1 {
		t.Fatalf("ThinkingBlockIndex = [%d,%d], want [0,1]", thinkingBlocks[0].ThinkingBlockIndex, thinkingBlocks[1].ThinkingBlockIndex)
	}

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ThinkingTranslatedEvent{MessageID: "msgidx:1", BlockIndex: 1, Translated: "第二", TargetLang: "zh-Hans"}})
	if len(thinkingBlocks[0].ThinkingTranslations) != 0 {
		t.Fatalf("first block should not receive second translation, got %#v", thinkingBlocks[0].ThinkingTranslations)
	}
	if got := strings.TrimSpace(thinkingBlocks[1].ThinkingTranslations[1].Content); got != "第二" {
		t.Fatalf("second translated content = %q, want 第二", got)
	}
}

func TestThinkingFinalizeWithoutThinkingEnd(t *testing.T) {
	// When thinking_end is never received (e.g. cancellation), finalizing the
	// turn should still settle the thinking block.
	//
	// A tool call is deliberately not such a terminal point: gateways may
	// splice a tool_use block into the middle of one thinking block, so the
	// completion stays owned by thinking_end there. See
	// TestToolCallInsideThinkingBlockKeepsOneThinkingCard.
	m := NewModelWithSize(nil, 80, 12)

	// Start thinking but never send StreamThinkingEvent (thinking_end).
	_ = m.handleAgentEvent(agentEventMsg{event: agent.ThinkingStartedEvent{}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.StreamThinkingDeltaEvent{Text: "Analyzing...", AgentID: ""}})

	m.finalizeTurn()

	// The thinking block should be settled by the fallback.
	blocks := m.viewport.visibleBlocks()
	var thinkingBlock *Block
	for _, b := range blocks {
		if b.Type == BlockThinking {
			thinkingBlock = b
			break
		}
	}
	if thinkingBlock == nil {
		t.Fatal("expected a thinking block in viewport")
	}
	if thinkingBlock.Streaming {
		t.Fatal("expected thinking block to be settled by finalizeAssistantBlock")
	}
}

func TestMultipleThinkingRoundsProduceIndependentCards(t *testing.T) {
	// After thinking_end, subsequent thinking deltas must start a fresh
	// card instead of appending to the already-settled block.
	m := NewModelWithSize(nil, 80, 12)

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ThinkingStartedEvent{}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.StreamThinkingDeltaEvent{Text: "first round", AgentID: ""}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.StreamThinkingEvent{AgentID: ""}})

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ThinkingStartedEvent{}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.StreamThinkingDeltaEvent{Text: "second round", AgentID: ""}})

	var thinkingBlocks []*Block
	for _, b := range m.viewport.visibleBlocks() {
		if b.Type == BlockThinking {
			thinkingBlocks = append(thinkingBlocks, b)
		}
	}
	if len(thinkingBlocks) != 2 {
		t.Fatalf("expected 2 thinking blocks after a second round started, got %d", len(thinkingBlocks))
	}
	if thinkingBlocks[0].Streaming {
		t.Fatal("first thinking block should be settled")
	}
	if !thinkingBlocks[1].Streaming {
		t.Fatal("second thinking block should still be streaming")
	}
	if !strings.Contains(thinkingBlocks[1].Content, "second round") {
		t.Fatalf("second thinking block content = %q, want to include 'second round'", thinkingBlocks[1].Content)
	}
}

func TestStreamingStaleUsesLastDeltaNotActivityStart(t *testing.T) {
	// streamingStale should check when the last streaming delta arrived,
	// not when ActivityStreaming began. A model that has been thinking for
	// >5 minutes but still sending deltas should NOT be considered stale.
	m := NewModelWithSize(nil, 80, 24)

	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityStreaming, AgentID: "main"}
	m.activityStartTime["main"] = time.Now().Add(-6 * time.Minute)

	// Simulate a recent delta received 10 seconds ago.
	m.streamLastDeltaAt["main"] = time.Now().Add(-10 * time.Second)

	if m.streamingStale() {
		t.Fatal("streamingStale should return false when a recent delta was received, even if activity started >5 min ago")
	}
}

func TestStreamingStaleTriggersWhenNoDeltaForFiveMinutes(t *testing.T) {
	// When no streaming delta has been received for >5 minutes, the
	// connection is likely lost and streamingStale should return true.
	m := NewModelWithSize(nil, 80, 24)

	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityStreaming, AgentID: "main"}
	m.activityStartTime["main"] = time.Now().Add(-10 * time.Minute)
	m.streamLastDeltaAt["main"] = time.Now().Add(-6 * time.Minute)

	if !m.streamingStale() {
		t.Fatal("streamingStale should return true when last delta was received >5 min ago")
	}
}

func TestStreamingStaleWithoutDeltaFallsBackToActivityStart(t *testing.T) {
	// When no delta timestamp is recorded (e.g. streaming started but
	// no visible output yet), fall back to activityStartTime.
	m := NewModelWithSize(nil, 80, 24)

	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityStreaming, AgentID: "main"}
	m.activityStartTime["main"] = time.Now().Add(-6 * time.Minute)

	if !m.streamingStale() {
		t.Fatal("streamingStale should fall back to activityStartTime when no delta recorded")
	}

	// Recent activity start should not trigger stale.
	m.activityStartTime["main"] = time.Now().Add(-30 * time.Second)
	if m.streamingStale() {
		t.Fatal("streamingStale should return false when activity started recently and no delta recorded")
	}
}

func TestStreamingDeltaTouchPreventsThinkingSplit(t *testing.T) {
	// Simulate long-running thinking that continuously sends deltas.
	// The thinking block should NOT be split by streamingStale.
	m := NewModelWithSize(nil, 80, 24)

	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityStreaming, AgentID: "main"}
	m.activityStartTime["main"] = time.Now().Add(-6 * time.Minute)

	// Start thinking.
	_ = m.handleAgentEvent(agentEventMsg{event: agent.ThinkingStartedEvent{}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.StreamThinkingDeltaEvent{Text: "initial analysis", AgentID: ""}})

	// StreamThinkingDeltaEvent should have touched the delta timestamp.
	if _, ok := m.streamLastDeltaAt["main"]; !ok {
		t.Fatal("expected streamLastDeltaAt[main] to be set after StreamThinkingDeltaEvent")
	}

	// Even though activity started >5 min ago, last delta is recent.
	if m.streamingStale() {
		t.Fatal("streamingStale should not trigger when thinking deltas are still arriving")
	}

	// The thinking block should remain a single card.
	var thinkingBlocks []*Block
	for _, b := range m.viewport.visibleBlocks() {
		if b.Type == BlockThinking {
			thinkingBlocks = append(thinkingBlocks, b)
		}
	}
	if len(thinkingBlocks) != 1 {
		t.Fatalf("expected 1 thinking block, got %d", len(thinkingBlocks))
	}
	if !thinkingBlocks[0].Streaming {
		t.Fatal("thinking block should still be streaming")
	}
}

func TestTouchStreamDeltaFromTextEvent(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityStreaming, AgentID: "main"}

	_ = m.handleAgentEvent(agentEventMsg{event: agent.StreamTextEvent{Text: "hello", AgentID: ""}})

	if _, ok := m.streamLastDeltaAt["main"]; !ok {
		t.Fatal("expected streamLastDeltaAt[main] to be set after StreamTextEvent")
	}
}

func TestTouchStreamDeltaFromToolCallStart(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityStreaming, AgentID: "main"}

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallStartEvent{
		ID:   "call-1",
		Name: "read",
	}})

	if _, ok := m.streamLastDeltaAt["main"]; !ok {
		t.Fatal("expected streamLastDeltaAt[main] to be set after ToolCallStartEvent")
	}
}

func TestTouchStreamDeltaFromRequestProgress(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityStreaming, AgentID: "main"}

	_ = m.handleAgentEvent(agentEventMsg{event: agent.RequestProgressEvent{
		AgentID: "main",
		Bytes:   1024,
		Events:  5,
	}})

	if _, ok := m.streamLastDeltaAt["main"]; !ok {
		t.Fatal("expected streamLastDeltaAt[main] to be set after RequestProgressEvent")
	}
}

func TestTouchStreamDeltaNotSetOnRequestProgressDone(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityStreaming, AgentID: "main"}

	_ = m.handleAgentEvent(agentEventMsg{event: agent.RequestProgressEvent{
		AgentID: "main",
		Bytes:   2048,
		Events:  10,
		Done:    true,
	}})

	if _, ok := m.streamLastDeltaAt["main"]; ok {
		t.Fatal("streamLastDeltaAt should not be set when RequestProgressEvent.Done is true")
	}
}

func TestMarkAgentIdleClearsStreamLastDelta(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.streamLastDeltaAt["main"] = time.Now()

	m.markAgentIdle("main")

	if _, ok := m.streamLastDeltaAt["main"]; ok {
		t.Fatal("expected streamLastDeltaAt[main] to be cleared after markAgentIdle")
	}
}
