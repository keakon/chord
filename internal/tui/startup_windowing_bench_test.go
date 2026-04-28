package tui

import (
	"fmt"
	"testing"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/message"
)

func benchmarkDeferredStartupModel(b *testing.B, blocks int) *Model {
	b.Helper()
	messages := make([]message.Message, 0, blocks)
	for i := 0; i < blocks; i++ {
		messages = append(messages, message.Message{Role: "assistant", Content: fmt.Sprintf("message-%04d", i)})
	}
	backend := &sessionControlAgent{resumePending: true, startupResumeID: "123", messages: messages}
	m := NewModelWithSize(backend, 120, 24)
	cmd := m.handleAgentEvent(agentEventMsg{event: agent.SessionRestoredEvent{}})
	if cmd == nil {
		b.Fatal("SessionRestoredEvent should schedule deferred rebuild")
	}
	updated, _ := m.Update(cmd())
	model, ok := updated.(*Model)
	if !ok {
		b.Fatalf("Update returned %T, want *Model", updated)
	}
	return model
}

func BenchmarkDeferredStartupTranscriptJumpOrdinalWindowSwitch(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		m := func() *Model {
			b.StopTimer()
			defer b.StartTimer()
			return benchmarkDeferredStartupModel(b, 1500)
		}()
		m.jumpToVisibleBlockOrdinal(900)
	}
}

func BenchmarkDeferredStartupTranscriptJumpTopBottomWindowSwitch(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		m := func() *Model {
			b.StopTimer()
			defer b.StartTimer()
			return benchmarkDeferredStartupModel(b, 1500)
		}()
		m.jumpToVisibleBlockOrdinal(1)
		m.handleNormalKey(modelSelectKey("G"))
	}
}
