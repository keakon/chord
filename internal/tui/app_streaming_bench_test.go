package tui

import (
	"strings"
	"testing"

	"github.com/keakon/chord/internal/agent"
)

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
