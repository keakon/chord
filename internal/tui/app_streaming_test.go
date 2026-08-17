package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/agent"
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
	if mainBlock.ThinkingDuration != 0 {
		t.Fatalf("main thinking duration = %v before main completion, want 0", mainBlock.ThinkingDuration)
	}
	if subBlock.ThinkingDuration == 0 {
		t.Fatal("subagent thinking duration was not recorded")
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
	// Backdate the clock so the recorded duration must span the whole block.
	m.thinkingStartTime = time.Now().Add(-3 * time.Second)

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
	if thinkingBlock.ThinkingDuration != 0 {
		t.Fatalf("thinking duration = %v, want it frozen only on thinking_end", thinkingBlock.ThinkingDuration)
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
	if thinkingBlock.ThinkingDuration < 3*time.Second {
		t.Fatalf("thinking duration = %v, want the full block span", thinkingBlock.ThinkingDuration)
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
