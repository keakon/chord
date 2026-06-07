package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/agent"
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
	if m.streamFlushDelay != 1*time.Millisecond {
		t.Fatalf("stream thinking final flush delay = %s, want 1ms", m.streamFlushDelay)
	}
	if !m.streamRenderForceView || m.streamRenderDeferred || m.streamRenderDeferNext {
		t.Fatalf("final thinking invalidation = force:%v deferred:%v next:%v, want forced boundary render", m.streamRenderForceView, m.streamRenderDeferred, m.streamRenderDeferNext)
	}
}

func assertNewlineDeltaUsesCoalescedFlushAfterInitialBoundary(t *testing.T, m *Model, label string, send func(string) tea.Cmd) {
	t.Helper()

	cmd := send("first")
	if cmd == nil {
		t.Fatalf("first %s should schedule initial boundary flush", label)
	}
	if m.streamFlushDelay != 1*time.Millisecond {
		t.Fatalf("initial %s flush delay = %s, want 1ms", label, m.streamFlushDelay)
	}

	if !m.consumeStreamFlush(streamFlushTickMsg{generation: m.streamFlushGeneration}) {
		t.Fatal("expected to consume initial stream flush")
	}
	m.streamRenderForceView = false
	m.streamRenderDeferred = true
	m.streamRenderDeferNext = true
	m.lastHostRedrawAt = time.Now()

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
		m := newStreamTextRenderedModel(b, "seed")
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
		m := newStreamTextRenderedModel(b, "seed")
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
		m := newStreamThinkingRenderedModel(b, "plan")
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
		m := NewModelWithSize(&sessionControlAgent{}, 120, 40)
		_ = m.View()
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

func repeatedStreamDeltas(count int, delta string) []string {
	out := make([]string, count)
	for i := range out {
		out[i] = delta
	}
	return out
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
