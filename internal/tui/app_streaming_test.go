package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func TestScheduleStreamFlushCoalescesUntilConsumed(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	first := m.scheduleStreamFlush(0)
	if first == nil {
		t.Fatal("first scheduleStreamFlush should return a tick command")
	}
	if !m.streamFlushScheduled {
		t.Fatal("scheduleStreamFlush should mark flush as scheduled")
	}
	gen := m.streamFlushGeneration
	if second := m.scheduleStreamFlush(0); second != nil {
		t.Fatal("second scheduleStreamFlush before consume should be coalesced")
	}
	if !m.consumeStreamFlush(streamFlushTickMsg{generation: gen}) {
		t.Fatal("consumeStreamFlush should accept current generation")
	}
	if m.streamFlushScheduled {
		t.Fatal("consumeStreamFlush should clear scheduled flag")
	}
	if third := m.scheduleStreamFlush(0); third == nil {
		t.Fatal("scheduleStreamFlush should schedule again after consume")
	}
}

func TestScheduleStreamFlushUrgentDelayBypassesCadenceFloor(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	if cmd := m.scheduleStreamFlush(1 * time.Millisecond); cmd == nil {
		t.Fatal("urgent scheduleStreamFlush should return a tick command")
	}
	if !m.streamFlushScheduled {
		t.Fatal("urgent scheduleStreamFlush should mark flush as scheduled")
	}
	if m.streamFlushDelay != 1*time.Millisecond {
		t.Fatalf("streamFlushDelay = %s, want 1ms", m.streamFlushDelay)
	}
}

func TestStreamBoundaryFlushUsesForegroundBoundaryCadence(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	if cmd := m.requestStreamBoundaryFlush(); cmd == nil {
		t.Fatal("requestStreamBoundaryFlush should return a tick command")
	}
	if m.streamFlushDelay != foregroundBoundaryFlushCadence {
		t.Fatalf("streamFlushDelay = %s, want %s", m.streamFlushDelay, foregroundBoundaryFlushCadence)
	}
}

func TestStreamBoundaryFlushUsesBackgroundCadence(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.displayState = stateBackground
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityStreaming, AgentID: "main"}
	if cmd := m.requestStreamBoundaryFlush(); cmd == nil {
		t.Fatal("background-active requestStreamBoundaryFlush should return a tick command")
	}
	if m.streamFlushDelay != backgroundActiveContentFlushCadence {
		t.Fatalf("streamFlushDelay = %s, want %s", m.streamFlushDelay, backgroundActiveContentFlushCadence)
	}
}

func TestScheduleStreamFlushUrgentDelayPreemptsSlowerPendingFlush(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	if cmd := m.scheduleStreamFlush(0); cmd == nil {
		t.Fatal("default scheduleStreamFlush should return a tick command")
	}
	firstGen := m.streamFlushGeneration
	if m.streamFlushDelay != foregroundCadence.contentFlushDelay {
		t.Fatalf("streamFlushDelay = %s, want %s", m.streamFlushDelay, foregroundCadence.contentFlushDelay)
	}
	if cmd := m.scheduleStreamFlush(1 * time.Millisecond); cmd == nil {
		t.Fatal("urgent scheduleStreamFlush should preempt slower pending flush")
	}
	if !m.streamFlushScheduled {
		t.Fatal("preempted stream flush should remain scheduled")
	}
	if m.streamFlushDelay != 1*time.Millisecond {
		t.Fatalf("streamFlushDelay = %s, want 1ms", m.streamFlushDelay)
	}
	if m.streamFlushGeneration <= firstGen {
		t.Fatalf("streamFlushGeneration = %d, want > %d after preemption", m.streamFlushGeneration, firstGen)
	}
	if m.consumeStreamFlush(streamFlushTickMsg{generation: firstGen}) {
		t.Fatal("stale preempted generation should be rejected")
	}
	if !m.streamFlushScheduled {
		t.Fatal("stale preempted generation should keep urgent flush scheduled")
	}
}

func TestScheduleStreamFlushRejectsStaleGeneration(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	_ = m.scheduleStreamFlush(0)
	if m.consumeStreamFlush(streamFlushTickMsg{generation: m.streamFlushGeneration - 1}) {
		t.Fatal("consumeStreamFlush should reject stale generation")
	}
	if !m.streamFlushScheduled {
		t.Fatal("stale generation should not clear scheduled flag")
	}
}

func TestStreamDeltaNewlineUsesCoalescedFlushAfterInitialBoundary(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	assertNewlineDeltaUsesCoalescedFlushAfterInitialBoundary(t, &m, "stream text", func(text string) tea.Cmd {
		return m.handleAgentEvent(agentEventMsg{event: agent.StreamTextEvent{Text: text}})
	})
}

func TestStreamTextDeltasReuseCachedViewUntilFlush(t *testing.T) {
	m := newStreamTextRenderedModel(t, "first")

	_ = m.handleAgentEvent(agentEventMsg{event: agent.StreamTextEvent{Text: " second"}})
	if m.currentAssistantBlock == nil || m.currentAssistantBlock.Content != "first" {
		t.Fatalf("assistant block content = %q, want last flushed stream text before flush", blockContentForTest(m.currentAssistantBlock))
	}
	if got := pendingStreamingContentForTest(m.currentAssistantBlock); got != "first second" {
		t.Fatalf("pending assistant stream content = %q, want accumulated text", got)
	}
	deferred := stripANSI(m.View().Content)
	if strings.Contains(deferred, "second") {
		t.Fatalf("View should reuse cached content before stream flush, got:\n%s", deferred)
	}

	m.handleStreamFlushTick(streamFlushTickMsg{generation: m.streamFlushGeneration})
	flushed := stripANSI(m.View().Content)
	if !strings.Contains(flushed, "first second") {
		t.Fatalf("View should include accumulated stream text after flush, got:\n%s", flushed)
	}
}

func TestStreamTextPlaceholderDoesNotAppendAssistantBlock(t *testing.T) {
	m := NewModelWithSize(&sessionControlAgent{}, 120, 40)

	for _, text := range []string{"", " \n\t", "\u200b", "\u200b\u200b", "\ufeff", "\u200d", ".", "..", "...", " … \n", "\x1b[2m...\x1b[0m"} {
		_ = m.handleAgentEvent(agentEventMsg{event: agent.StreamTextEvent{Text: text}})
	}

	if m.currentAssistantBlock == nil {
		t.Fatal("placeholder stream should remain buffered for a later visible delta")
	}
	if m.assistantBlockAppended {
		t.Fatal("placeholder stream should not append an assistant block")
	}
	if got := len(m.viewport.visibleBlocks()); got != 0 {
		t.Fatalf("visible blocks = %d, want 0 before visible assistant content", got)
	}
	if got := m.currentAssistantBlock.Content; got != "" {
		t.Fatalf("placeholder content = %q, want empty pending block", got)
	}
}

func TestStreamTextVisibleContentReplacesBufferedPlaceholder(t *testing.T) {
	m := NewModelWithSize(&sessionControlAgent{}, 120, 40)

	_ = m.handleAgentEvent(agentEventMsg{event: agent.StreamTextEvent{Text: "\u200b\u200b"}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.StreamTextEvent{Text: "Answer"}})

	if m.currentAssistantBlock == nil || m.currentAssistantBlock.Content != "Answer" {
		t.Fatalf("assistant content = %q, want visible content without placeholder prefix", blockContentForTest(m.currentAssistantBlock))
	}
	if !m.assistantBlockAppended {
		t.Fatal("visible assistant content should append the buffered block")
	}
	blocks := m.viewport.visibleBlocks()
	if len(blocks) != 1 || blocks[0].Type != BlockAssistant {
		t.Fatalf("visible blocks = %#v, want one assistant block", blocks)
	}
}

func TestStreamTextSplitDotPlaceholderDoesNotPolluteVisibleContent(t *testing.T) {
	m := NewModelWithSize(&sessionControlAgent{}, 120, 40)

	for _, text := range []string{".", ".", ".", "Answer"} {
		_ = m.handleAgentEvent(agentEventMsg{event: agent.StreamTextEvent{Text: text}})
	}

	if m.currentAssistantBlock == nil || m.currentAssistantBlock.Content != "Answer" {
		t.Fatalf("assistant content = %q, want visible content without dot placeholder prefix", blockContentForTest(m.currentAssistantBlock))
	}
	if !m.assistantBlockAppended {
		t.Fatal("visible assistant content should append the buffered block")
	}
	blocks := m.viewport.visibleBlocks()
	if len(blocks) != 1 || blocks[0].Type != BlockAssistant || blocks[0].Content != "Answer" {
		t.Fatalf("visible blocks = %#v, want one assistant block with Answer", blocks)
	}
}

func TestToolCallAfterStreamPlaceholderLeavesNoAssistantCard(t *testing.T) {
	for _, deltas := range [][]string{{"..."}, {".", ".", "."}, {" … "}, {"\u200b\u200b"}, {"\u200d"}} {
		m := NewModelWithSize(&sessionControlAgent{}, 120, 40)
		for _, delta := range deltas {
			_ = m.handleAgentEvent(agentEventMsg{event: agent.StreamTextEvent{Text: delta}})
		}
		_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallStartEvent{
			ID:       "call-1",
			Name:     tools.NameShell,
			ArgsJSON: `{"command":"pwd"}`,
		}})

		if m.currentAssistantBlock != nil || m.assistantBlockAppended {
			t.Fatalf("deltas %q: tool call should clear a placeholder-only assistant stream", deltas)
		}
		blocks := m.viewport.visibleBlocks()
		if len(blocks) != 1 || blocks[0].Type != BlockToolCall {
			t.Fatalf("deltas %q: visible blocks = %#v, want only the tool call", deltas, blocks)
		}
	}
}

func TestSubAgentToolCallDoesNotSplitMainAssistantStream(t *testing.T) {
	m := NewModelWithSize(&sessionControlAgent{}, 120, 40)

	_ = m.handleAgentEvent(agentEventMsg{event: agent.StreamTextEvent{Text: "main first"}})
	mainBlock := m.currentAssistantBlock
	if mainBlock == nil {
		t.Fatal("expected active main assistant block")
	}

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallStartEvent{
		ID:       "sub-call-1",
		Name:     tools.NameRead,
		ArgsJSON: `{"path":"README.md"}`,
		AgentID:  "agent-1",
	}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.StreamTextEvent{Text: " second"}})

	if m.currentAssistantBlock != mainBlock {
		t.Fatal("subagent tool call replaced the active main assistant block")
	}
	if !mainBlock.Streaming {
		t.Fatal("subagent tool call settled the main assistant block")
	}
	if got := pendingStreamingContentForTest(mainBlock); got != "main first second" {
		t.Fatalf("main assistant content = %q, want one continuous stream", got)
	}
	mainCards := 0
	for _, block := range m.viewport.blocks {
		if block.Type == BlockAssistant && block.AgentID == "" {
			mainCards++
		}
	}
	if mainCards != 1 {
		t.Fatalf("main assistant cards = %d, want 1", mainCards)
	}
}

func TestInterleavedAgentTextStreamsKeepIndependentAssistantCards(t *testing.T) {
	m := NewModelWithSize(&sessionControlAgent{}, 120, 40)

	_ = m.handleAgentEvent(agentEventMsg{event: agent.StreamTextEvent{Text: "main first"}})
	mainBlock := m.currentAssistantBlock
	_ = m.handleAgentEvent(agentEventMsg{event: agent.StreamTextEvent{Text: "sub first", AgentID: "agent-1"}})
	subBlock := m.streamState("agent-1").assistant
	_ = m.handleAgentEvent(agentEventMsg{event: agent.StreamTextEvent{Text: " second"}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.StreamTextEvent{Text: " second", AgentID: "agent-1"}})

	if mainBlock == nil || subBlock == nil || mainBlock == subBlock {
		t.Fatalf("independent stream blocks = main:%p sub:%p", mainBlock, subBlock)
	}
	if m.currentAssistantBlock != mainBlock || m.streamState("agent-1").assistant != subBlock {
		t.Fatal("interleaved deltas replaced an agent's active assistant block")
	}
	if got := pendingStreamingContentForTest(mainBlock); got != "main first second" {
		t.Fatalf("main assistant content = %q", got)
	}
	if got := pendingStreamingContentForTest(subBlock); got != "sub first second" {
		t.Fatalf("subagent assistant content = %q", got)
	}
	for _, block := range []*Block{mainBlock, subBlock} {
		if !block.Streaming {
			t.Fatalf("block %d settled during another agent's delta", block.ID)
		}
	}
}

func TestSubAgentToolCallFinalizesOnlySubAgentAssistantStream(t *testing.T) {
	m := NewModelWithSize(&sessionControlAgent{}, 120, 40)

	_ = m.handleAgentEvent(agentEventMsg{event: agent.StreamTextEvent{Text: "main"}})
	mainBlock := m.currentAssistantBlock
	_ = m.handleAgentEvent(agentEventMsg{event: agent.StreamTextEvent{Text: "sub", AgentID: "agent-1"}})
	subBlock := m.streamState("agent-1").assistant
	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallStartEvent{
		ID:       "sub-call-1",
		Name:     tools.NameRead,
		ArgsJSON: `{"path":"README.md"}`,
		AgentID:  "agent-1",
	}})

	if !mainBlock.Streaming || m.currentAssistantBlock != mainBlock {
		t.Fatal("subagent tool call changed the main assistant stream")
	}
	if subBlock.Streaming || m.streamState("agent-1").assistant != nil {
		t.Fatal("subagent tool call did not finalize its own assistant stream")
	}
}

func TestInterleavedAgentThinkingStreamsKeepIndependentCards(t *testing.T) {
	m := NewModelWithSize(&sessionControlAgent{}, 120, 40)

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ThinkingStartedEvent{}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.StreamThinkingDeltaEvent{Text: "main thought"}})
	mainBlock := m.currentThinkingBlock
	_ = m.handleAgentEvent(agentEventMsg{event: agent.ThinkingStartedEvent{AgentID: "agent-1"}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.StreamThinkingDeltaEvent{Text: "sub thought", AgentID: "agent-1"}})
	subBlock := m.streamState("agent-1").thinking
	_ = m.handleAgentEvent(agentEventMsg{event: agent.StreamThinkingEvent{AgentID: "agent-1"}})

	if mainBlock == nil || subBlock == nil || mainBlock == subBlock {
		t.Fatalf("independent thinking blocks = main:%p sub:%p", mainBlock, subBlock)
	}
	if m.currentThinkingBlock != mainBlock || !mainBlock.Streaming {
		t.Fatal("subagent thinking completion changed the main thinking stream")
	}
	if subBlock.Streaming || m.streamState("agent-1").thinking != nil {
		t.Fatal("subagent thinking completion did not settle its own block")
	}
}

func TestStreamingAssistantPlaceholderRendersNoCard(t *testing.T) {
	for _, content := range []string{"", " \n", "\u200b", "\u200b\u200b", "\ufeff", "\u200d", ".", "..", "...", " … ", "\x1b[2m...\x1b[0m"} {
		block := &Block{ID: 1, Type: BlockAssistant, Streaming: true, Content: content}
		if lines := block.Render(120, ""); len(lines) != 0 {
			t.Fatalf("placeholder %q rendered %d lines, want none", content, len(lines))
		}
	}
}

func TestStreamThinkingDeltasStayPendingUntilFlush(t *testing.T) {
	m := newStreamThinkingRenderedModel(t, "first")

	_ = m.handleAgentEvent(agentEventMsg{event: agent.StreamThinkingDeltaEvent{Text: " second"}})
	if m.currentThinkingBlock == nil || m.currentThinkingBlock.Content != "first" {
		t.Fatalf("thinking block content = %q, want last flushed thinking text before flush", blockContentForTest(m.currentThinkingBlock))
	}
	if got := pendingStreamingContentForTest(m.currentThinkingBlock); got != "first second" {
		t.Fatalf("pending thinking stream content = %q, want accumulated text", got)
	}
	deferred := stripANSI(m.View().Content)
	if strings.Contains(deferred, "second") {
		t.Fatalf("View should reuse cached thinking content before stream flush, got:\n%s", deferred)
	}

	m.handleStreamFlushTick(streamFlushTickMsg{generation: m.streamFlushGeneration})
	flushed := stripANSI(m.View().Content)
	if !strings.Contains(flushed, "first second") {
		t.Fatalf("View should include accumulated thinking text after flush, got:\n%s", flushed)
	}
}

func TestStreamThinkingDeltaNewlineUsesCoalescedFlushAfterInitialBoundary(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	assertNewlineDeltaUsesCoalescedFlushAfterInitialBoundary(t, &m, "thinking delta", func(text string) tea.Cmd {
		return m.handleAgentEvent(agentEventMsg{event: agent.StreamThinkingDeltaEvent{Text: text}})
	})
}

func TestStreamThinkingEventFinalBoundaryKeepsUrgentFlush(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	cmd := m.handleAgentEvent(agentEventMsg{event: agent.StreamThinkingDeltaEvent{Text: "first"}})
	if cmd == nil {
		t.Fatal("initial thinking delta should schedule boundary flush")
	}
	if !m.consumeStreamFlush(streamFlushTickMsg{generation: m.streamFlushGeneration}) {
		t.Fatal("expected to consume initial stream flush")
	}

	cmd = m.handleAgentEvent(agentEventMsg{event: agent.StreamThinkingEvent{Text: "\nsecond"}})
	if cmd == nil {
		t.Fatal("stream thinking event should schedule final boundary flush")
	}
	if m.streamFlushDelay != foregroundBoundaryFlushCadence {
		t.Fatalf("stream thinking final flush delay = %s, want %s", m.streamFlushDelay, foregroundBoundaryFlushCadence)
	}
	if !m.streamRenderForceView || m.streamRenderDeferred || m.streamRenderDeferNext {
		t.Fatalf("final thinking invalidation = force:%v deferred:%v next:%v, want forced boundary render", m.streamRenderForceView, m.streamRenderDeferred, m.streamRenderDeferNext)
	}
}

func TestStreamThinkingEventFlushesPendingDeltaOnEmptyFinalEvent(t *testing.T) {
	m := newStreamThinkingRenderedModel(t, "first")

	_ = m.handleAgentEvent(agentEventMsg{event: agent.StreamThinkingDeltaEvent{Text: " second"}})
	if got := m.currentThinkingBlock.Content; got != "first" {
		t.Fatalf("thinking content before final event = %q, want last flushed content", got)
	}

	_ = m.handleAgentEvent(agentEventMsg{event: agent.StreamThinkingEvent{Text: ""}})
	blocks := m.viewport.visibleBlocks()
	if len(blocks) == 0 {
		t.Fatal("expected finalized thinking block to remain visible")
	}
	if got := blocks[len(blocks)-1].Content; got != "first second" {
		t.Fatalf("finalized thinking content = %q, want pending delta flushed", got)
	}
}

func assertNewlineDeltaUsesCoalescedFlushAfterInitialBoundary(t *testing.T, m *Model, label string, send func(string) tea.Cmd) {
	t.Helper()

	cmd := send("first")
	if cmd == nil {
		t.Fatalf("first %s should schedule initial boundary flush", label)
	}
	if m.streamFlushDelay != foregroundBoundaryFlushCadence {
		t.Fatalf("initial %s flush delay = %s, want %s", label, m.streamFlushDelay, foregroundBoundaryFlushCadence)
	}

	if !m.consumeStreamFlush(streamFlushTickMsg{generation: m.streamFlushGeneration}) {
		t.Fatal("expected to consume initial stream flush")
	}
	m.streamRenderForceView = false
	m.streamRenderDeferred = true
	m.streamRenderDeferNext = true
	cmd = send("\nsecond")
	if cmd == nil {
		t.Fatalf("newline %s should schedule coalesced flush", label)
	}
	if m.streamFlushDelay != foregroundCadence.contentFlushDelay {
		t.Fatalf("newline %s flush delay = %s, want %s", label, m.streamFlushDelay, foregroundCadence.contentFlushDelay)
	}
	if !m.streamRenderDeferred || !m.streamRenderDeferNext || m.streamRenderForceView {
		t.Fatalf("%s stream invalidation = force:%v deferred:%v next:%v, want deferred coalesced path", label, m.streamRenderForceView, m.streamRenderDeferred, m.streamRenderDeferNext)
	}
}

func BenchmarkStreamTextDeltaBurstDeferredView(b *testing.B) {
	deltas := repeatedStreamDeltas(128, "alpha beta gamma ")
	b.ReportAllocs()
	for b.Loop() {
		b.StopTimer()
		m := newStreamTextRenderedModel(b, "seed")
		b.StartTimer()
		for _, delta := range deltas {
			_ = m.handleAgentEvent(agentEventMsg{event: agent.StreamTextEvent{Text: delta}})
			_ = m.View()
		}
	}
}

func BenchmarkStreamTextDeltaBurstCadenceFlush(b *testing.B) {
	deltas := repeatedStreamDeltas(128, "alpha beta gamma ")
	b.ReportAllocs()
	for b.Loop() {
		b.StopTimer()
		m := newStreamTextRenderedModel(b, "seed")
		b.StartTimer()
		for i, delta := range deltas {
			_ = m.handleAgentEvent(agentEventMsg{event: agent.StreamTextEvent{Text: delta}})
			if (i+1)%32 == 0 {
				m.handleStreamFlushTick(streamFlushTickMsg{generation: m.streamFlushGeneration})
			}
			_ = m.View()
		}
	}
}

func BenchmarkStreamTextDeltaSteadyStateCadenceFlush(b *testing.B) {
	deltas := repeatedStreamDeltas(128, "alpha beta gamma ")
	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		b.StopTimer()
		m := newStreamTextRenderedModel(b, "seed")
		b.StartTimer()
		for i, delta := range deltas {
			_ = m.handleAgentEvent(agentEventMsg{event: agent.StreamTextEvent{Text: delta}})
			if (i+1)%32 == 0 {
				m.handleStreamFlushTick(streamFlushTickMsg{generation: m.streamFlushGeneration})
			}
			_ = m.View()
		}
	}
}

func BenchmarkStreamThinkingDeltaBurstDeferredView(b *testing.B) {
	deltas := repeatedStreamDeltas(128, "analysis detail ")
	b.ReportAllocs()
	for b.Loop() {
		b.StopTimer()
		m := newStreamThinkingRenderedModel(b, "plan")
		b.StartTimer()
		for _, delta := range deltas {
			_ = m.handleAgentEvent(agentEventMsg{event: agent.StreamThinkingDeltaEvent{Text: delta}})
			_ = m.View()
		}
	}
}

func BenchmarkToolCallUpdateArgsStreamingCadence(b *testing.B) {
	args := streamingToolArgs(128)
	b.ReportAllocs()
	for b.Loop() {
		b.StopTimer()
		m := NewModelWithSize(&sessionControlAgent{}, 120, 40)
		_ = m.View()
		b.StartTimer()
		_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallStartEvent{
			ID:       "call-stream-args",
			Name:     "shell",
			ArgsJSON: args[0],
		}})
		_ = m.View()
		for _, arg := range args[1:] {
			_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallUpdateEvent{
				ID:       "call-stream-args",
				Name:     "shell",
				ArgsJSON: arg,
			}})
			_ = m.View()
		}
	}
}

func newStreamTextRenderedModel(tb testing.TB, seed string) *Model {
	tb.Helper()
	m := NewModelWithSize(&sessionControlAgent{}, 120, 40)
	_ = m.View()
	_ = m.handleAgentEvent(agentEventMsg{event: agent.StreamTextEvent{Text: seed}})
	m.handleStreamFlushTick(streamFlushTickMsg{generation: m.streamFlushGeneration})
	rendered := stripANSI(m.View().Content)
	if !strings.Contains(rendered, seed) {
		tb.Fatalf("initial stream text was not rendered, got:\n%s", rendered)
	}
	return &m
}

func newStreamThinkingRenderedModel(tb testing.TB, seed string) *Model {
	tb.Helper()
	m := NewModelWithSize(&sessionControlAgent{}, 120, 40)
	_ = m.View()
	_ = m.handleAgentEvent(agentEventMsg{event: agent.StreamThinkingDeltaEvent{Text: seed}})
	m.handleStreamFlushTick(streamFlushTickMsg{generation: m.streamFlushGeneration})
	rendered := stripANSI(m.View().Content)
	if !strings.Contains(rendered, seed) {
		tb.Fatalf("initial thinking stream was not rendered, got:\n%s", rendered)
	}
	return &m
}

func blockContentForTest(block *Block) string {
	if block == nil {
		return ""
	}
	return block.Content
}

func pendingStreamingContentForTest(block *Block) string {
	if block == nil || block.streamContentBuilder == nil {
		return blockContentForTest(block)
	}
	return block.streamContentBuilder.String()
}

func repeatedStreamDeltas(count int, delta string) []string {
	out := make([]string, count)
	for i := range out {
		out[i] = delta
	}
	return out
}

// TestToolCallInsideThinkingBlockKeepsOneThinkingCard covers Anthropic-compatible
// gateways that splice a complete tool_use block into the middle of a single
// thinking block (thinking#0 deltas, tool_use#1 start/delta/stop, then more
// thinking#0 deltas before thinking#0 stops). The spliced tool card must not
// split that one thinking block across two cards.
func TestToolCallInsideThinkingBlockKeepsOneThinkingCard(t *testing.T) {
	m := NewModelWithSize(&sessionControlAgent{}, 120, 40)

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ThinkingStartedEvent{}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.StreamThinkingDeltaEvent{Text: "preserving the final on"}})
	thinkingBlock := m.currentThinkingBlock
	if thinkingBlock == nil {
		t.Fatal("expected an active thinking block")
	}
	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallStartEvent{
		ID:       "call-1",
		Name:     tools.NameShell,
		ArgsJSON: `{"command":"git tag -d review-backup"}`,
	}})

	if m.currentThinkingBlock != thinkingBlock {
		t.Fatal("tool call detached the thinking block that had not received thinking_end")
	}
	if !thinkingBlock.Streaming {
		t.Fatal("tool call settled a thinking block whose wire block is still open")
	}

	_ = m.handleAgentEvent(agentEventMsg{event: agent.StreamThinkingDeltaEvent{Text: "e."}})
	if m.currentThinkingBlock != thinkingBlock {
		t.Fatal("thinking delta after the tool card started a new thinking block")
	}
	if got := pendingStreamingContentForTest(thinkingBlock); got != "preserving the final one." {
		t.Fatalf("thinking content = %q, want one continuous block", got)
	}

	_ = m.handleAgentEvent(agentEventMsg{event: agent.StreamThinkingEvent{}})
	if thinkingBlock.Streaming || m.currentThinkingBlock != nil {
		t.Fatal("thinking_end did not settle the thinking block")
	}

	var thinkingCards, toolCards int
	var thinkingIdx, toolIdx int
	for i, block := range m.viewport.blocks {
		switch {
		case block.Type == BlockThinking && block.AgentID == "":
			thinkingCards++
			thinkingIdx = i
		case block.Type == BlockToolCall:
			toolCards++
			toolIdx = i
		}
	}
	if thinkingCards != 1 || toolCards != 1 {
		t.Fatalf("cards = %d thinking / %d tool, want 1 each", thinkingCards, toolCards)
	}
	if thinkingIdx > toolIdx {
		t.Fatalf("thinking card at %d should precede the tool card at %d", thinkingIdx, toolIdx)
	}
	if rendered := stripANSI(m.View().Content); !strings.Contains(rendered, "preserving the final one.") {
		t.Fatalf("view should show the reunited thinking text, got:\n%s", rendered)
	}
}

func TestSubAgentThinkingFinalBlockReplacesDeltaAfterToolCall(t *testing.T) {
	m := NewModelWithSize(&sessionControlAgent{}, 120, 40)
	const agentID = "agent-thinking-final"
	_ = m.handleAgentEvent(agentEventMsg{event: agent.StreamThinkingDeltaEvent{Text: "sub plan", AgentID: agentID}})
	thinking := m.streamState(agentID).thinking
	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallStartEvent{
		ID: "call-thinking-final", Name: tools.NameRead, ArgsJSON: `{"path":"README.md"}`, AgentID: agentID,
	}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.StreamThinkingEvent{Text: "sub plan", AgentID: agentID}})
	if thinking == nil || thinking.Content != "sub plan" {
		t.Fatalf("final thinking content = %q, want one complete block", blockContentForTest(thinking))
	}
}

func TestSubAgentEmptyFinalThinkingKeepsAccumulatedDeltas(t *testing.T) {
	m := NewModelWithSize(&sessionControlAgent{}, 120, 40)
	const agentID = "agent-thinking-empty-final"
	_ = m.handleAgentEvent(agentEventMsg{event: agent.StreamThinkingDeltaEvent{Text: "streamed reasoning", AgentID: agentID}})
	thinking := m.streamState(agentID).thinking
	// SubAgent reducers commit full text and never emit blank thinking_end
	// today; if an emitter ever does, the blank payload must not erase the
	// content that already streamed.
	_ = m.handleAgentEvent(agentEventMsg{event: agent.StreamThinkingEvent{Text: "  ", AgentID: agentID}})
	if thinking == nil || thinking.Content != "streamed reasoning" {
		t.Fatalf("thinking content after blank final event = %q, want accumulated deltas kept", blockContentForTest(thinking))
	}
}

func streamingToolArgs(count int) []string {
	out := make([]string, count)
	var payload strings.Builder
	for i := range out {
		payload.WriteString("echo sample ")
		out[i] = `{"command":"` + payload.String() + `"}`
	}
	return out
}

// newFocusSwitchBackend returns a session backend whose GetMessages follows
// SwitchFocus, so setFocusedAgent rebuilds each view from that agent's own
// transcript the way the real agent does.
func newFocusSwitchBackend() *sessionControlAgent {
	return &sessionControlAgent{
		messagesByFocus: map[string][]message.Message{
			"": {
				{Role: "user", Content: "main prompt"},
			},
			"agent-1": {
				{Role: "user", Content: "worker prompt"},
			},
		},
	}
}

func flushPendingStreamForTest(m *Model) {
	m.handleStreamFlushTick(streamFlushTickMsg{generation: m.streamFlushGeneration})
}

// TestSubAgentAssistantStreamSurvivesFocusSwitch pins the focus-switch rebuild
// carrying a live subagent assistant stream across views. The stream state
// points at its block directly, so the rebuild must keep the same instance:
// dropping it would leave deltas writing into an orphan, and cloning it under
// a fresh ID would freeze the visible copy while the stream state keeps
// updating the original.
func TestSubAgentAssistantStreamSurvivesFocusSwitch(t *testing.T) {
	m := NewModelWithSize(newFocusSwitchBackend(), 120, 40)

	// Watch the worker while its response streams.
	m.setFocusedAgent("agent-1")
	_ = m.handleAgentEvent(agentEventMsg{event: agent.StreamTextEvent{AgentID: "agent-1", Text: "analyzing files"}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.StreamTextEvent{AgentID: "agent-1", Text: " and reading"}})
	flushPendingStreamForTest(&m)
	live := m.streamState("agent-1").assistant
	if live == nil || !live.Streaming {
		t.Fatal("expected agent-1 to have a live streaming assistant block")
	}
	if got := live.Content; got != "analyzing files and reading" {
		t.Fatalf("assistant content = %q, want streamed text", got)
	}
	liveID := live.ID

	// Switch to the main view mid-stream.
	m.setFocusedAgent("")
	if got := m.viewport.GetFocusedBlock(liveID); got == nil {
		t.Fatal("focus switch dropped the streaming assistant block")
	} else if got != live {
		t.Fatal("focus switch replaced the streaming assistant block with a clone")
	}
	if m.streamState("agent-1").assistant != live {
		t.Fatal("stream state lost its reference to the live assistant block")
	}
	for _, b := range m.viewport.visibleBlocks() {
		if b.Type == BlockAssistant {
			t.Fatalf("worker streaming content leaked into the main view: %#v", b)
		}
	}

	// Deltas that arrive while the worker is unfocused update the one retained
	// block in place; nothing recreates it.
	_ = m.handleAgentEvent(agentEventMsg{event: agent.StreamTextEvent{AgentID: "agent-1", Text: " plus more"}})
	flushPendingStreamForTest(&m)
	if m.streamState("agent-1").assistant != live {
		t.Fatal("delta while unfocused replaced the live assistant block")
	}
	if got := live.Content; got != "analyzing files and reading plus more" {
		t.Fatalf("assistant content = %q, want accumulated away-delta", got)
	}

	// Switch back mid-stream: the same card is still present and the next
	// delta continues it instead of restarting from a snapshot.
	m.setFocusedAgent("agent-1")
	if got := m.viewport.GetFocusedBlock(liveID); got != live {
		t.Fatal("focus switch back did not restore the original streaming block")
	}
	_ = m.handleAgentEvent(agentEventMsg{event: agent.StreamTextEvent{AgentID: "agent-1", Text: " done"}})
	flushPendingStreamForTest(&m)
	if got := live.Content; got != "analyzing files and reading plus more done" {
		t.Fatalf("assistant content = %q, want uninterrupted stream across the switch", got)
	}
	assistantCards := 0
	for _, b := range m.viewport.visibleBlocks() {
		if b.Type == BlockAssistant && b.AgentID == "agent-1" {
			assistantCards++
		}
	}
	if assistantCards != 1 {
		t.Fatalf("agent-1 assistant cards = %d, want 1", assistantCards)
	}
}

// TestSubAgentThinkingStreamSurvivesFocusSwitch covers the same rebuild
// carry-over for a live subagent thinking card.
func TestSubAgentThinkingStreamSurvivesFocusSwitch(t *testing.T) {
	m := NewModelWithSize(newFocusSwitchBackend(), 120, 40)

	m.setFocusedAgent("agent-1")
	_ = m.handleAgentEvent(agentEventMsg{event: agent.ThinkingStartedEvent{AgentID: "agent-1"}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.StreamThinkingDeltaEvent{AgentID: "agent-1", Text: "checking imports"}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.StreamThinkingDeltaEvent{AgentID: "agent-1", Text: " and formats"}})
	flushPendingStreamForTest(&m)
	live := m.streamState("agent-1").thinking
	if live == nil || !live.Streaming {
		t.Fatal("expected agent-1 to have a live streaming thinking block")
	}
	liveID := live.ID

	m.setFocusedAgent("")
	if got := m.viewport.GetFocusedBlock(liveID); got == nil || got != live {
		t.Fatal("focus switch dropped or cloned the streaming thinking block")
	}
	if m.streamState("agent-1").thinking != live {
		t.Fatal("stream state lost its reference to the live thinking block")
	}

	m.setFocusedAgent("agent-1")
	if got := m.viewport.GetFocusedBlock(liveID); got != live {
		t.Fatal("focus switch back did not restore the original thinking block")
	}
	_ = m.handleAgentEvent(agentEventMsg{event: agent.StreamThinkingDeltaEvent{AgentID: "agent-1", Text: " more"}})
	flushPendingStreamForTest(&m)
	if got := live.Content; got != "checking imports and formats more" {
		t.Fatalf("thinking content = %q, want uninterrupted stream across the switch", got)
	}
}

// TestMainAssistantStreamSurvivesSubAgentFocus pins the same carry-over for a
// main-agent stream while the user peeks at a subagent view. Main deltas are
// not focus-gated, so without retention they would keep writing into an
// orphaned block and the text would only resurface at the next rebuild.
func TestMainAssistantStreamSurvivesSubAgentFocus(t *testing.T) {
	m := NewModelWithSize(newFocusSwitchBackend(), 120, 40)

	_ = m.handleAgentEvent(agentEventMsg{event: agent.StreamTextEvent{Text: "main response"}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.StreamTextEvent{Text: " continues"}})
	flushPendingStreamForTest(&m)
	mainBlock := m.currentAssistantBlock
	if mainBlock == nil {
		t.Fatal("expected a live main assistant block")
	}
	mainID := mainBlock.ID

	// Peek at the worker view while the main response is still streaming.
	m.setFocusedAgent("agent-1")
	if got := m.viewport.GetFocusedBlock(mainID); got != mainBlock {
		t.Fatal("subagent focus switch dropped or cloned the main streaming block")
	}
	for _, b := range m.viewport.visibleBlocks() {
		if b.Type == BlockAssistant {
			t.Fatalf("main streaming content leaked into the worker view: %#v", b)
		}
	}

	// Main deltas keep flowing into the retained block while the worker view
	// is up.
	_ = m.handleAgentEvent(agentEventMsg{event: agent.StreamTextEvent{Text: " still"}})
	flushPendingStreamForTest(&m)
	if got := mainBlock.Content; got != "main response continues still" {
		t.Fatalf("main content = %q, want hidden deltas accumulated on the same block", got)
	}

	// Back on main the same card shows the accumulated text and keeps going.
	m.setFocusedAgent("")
	if got := m.viewport.GetFocusedBlock(mainID); got != mainBlock {
		t.Fatal("returning to main did not restore the original assistant block")
	}
	_ = m.handleAgentEvent(agentEventMsg{event: agent.StreamTextEvent{Text: " end"}})
	flushPendingStreamForTest(&m)
	if got := mainBlock.Content; got != "main response continues still end" {
		t.Fatalf("main content = %q, want uninterrupted stream across the switch", got)
	}
	mainCards := 0
	for _, b := range m.viewport.visibleBlocks() {
		if b.Type == BlockAssistant && b.AgentID == "" {
			mainCards++
		}
	}
	if mainCards != 1 {
		t.Fatalf("main assistant cards = %d, want 1", mainCards)
	}
}

// TestApplyPatchArgsStreamSurvivesFocusSwitch pins the same carry-over for a
// subagent apply_patch card whose arguments are still streaming. Dropping the
// card at a focus switch would make the next args delta rebuild it from the
// full accumulated ArgsJSON — the visible jump to already-streamed content.
func TestApplyPatchArgsStreamSurvivesFocusSwitch(t *testing.T) {
	m := NewModelWithSize(newFocusSwitchBackend(), 120, 40)
	m.setFocusedAgent("agent-1")

	const callID = "call-patch-focus-1"
	partial := `{"patch":"*** Begin Patch\n*** Update File: src/demo.go\n@@\n-old`
	complete := `{"patch":"*** Begin Patch\n*** Update File: src/demo.go\n@@\n-old\n+new\n*** End Patch"}`
	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallStartEvent{
		ID: callID, Name: tools.NameApplyPatch, AgentID: "agent-1", ArgsJSON: partial,
	}})
	card, ok := m.viewport.FindBlockByToolID(callID)
	if !ok {
		t.Fatal("missing streaming apply_patch card")
	}
	cardID := card.ID
	if !strings.Contains(card.Content, "Begin Patch") {
		t.Fatalf("apply_patch preview = %q, want streamed patch text", card.Content)
	}

	// Switch to the main view while the patch is still streaming.
	m.setFocusedAgent("")
	if got, ok := m.viewport.FindBlockByToolID(callID); !ok || got.ID != cardID {
		t.Fatal("focus switch dropped the live apply_patch card")
	}
	for _, b := range m.viewport.visibleBlocks() {
		if b.Type == BlockToolCall && b.ToolID == callID {
			t.Fatal("worker apply_patch card leaked into the main view")
		}
	}

	// Deltas that arrive while the worker is unfocused keep flowing into the
	// one retained card; nothing recreates it from the accumulated args.
	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallUpdateEvent{
		ID: callID, Name: tools.NameApplyPatch, AgentID: "agent-1", ArgsJSON: complete,
	}})
	patchCards := 0
	for _, b := range m.viewport.blocks {
		if b.Type != BlockToolCall || b.ToolID != callID {
			continue
		}
		patchCards++
		if b.ID != cardID {
			t.Fatalf("delta while unfocused recreated the card under id %d, want %d", b.ID, cardID)
		}
	}
	if patchCards != 1 {
		t.Fatalf("apply_patch cards = %d, want 1", patchCards)
	}
	if card.RawArgs != complete {
		t.Fatalf("apply_patch RawArgs = %q, want the accumulated args on the retained card", card.RawArgs)
	}

	// Switch back mid-stream: same card, and the stream end settles it in
	// place instead of rebuilding a fresh card from the final ArgsJSON.
	m.setFocusedAgent("agent-1")
	if got, ok := m.viewport.FindBlockByToolID(callID); !ok || got.ID != cardID {
		t.Fatal("focus switch back did not restore the original apply_patch card")
	}
	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallUpdateEvent{
		ID: callID, Name: tools.NameApplyPatch, AgentID: "agent-1", ArgsJSON: complete, ArgsStreamingDone: true,
	}})
	if got, ok := m.viewport.FindBlockByToolID(callID); !ok || got.ID != cardID {
		t.Fatal("args end after switch back did not settle the retained card")
	}
	if card.Content != `{"paths":["src/demo.go"]}` {
		t.Fatalf("content after args complete = %q, want stable path display", card.Content)
	}
}
