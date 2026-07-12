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
	for i := range blocks {
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
	m := benchmarkDeferredStartupModel(b, 1500)
	ordinals := [...]int{900, 300}
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; b.Loop(); i++ {
		m.jumpToVisibleBlockOrdinal(ordinals[i%len(ordinals)])
	}
}

func BenchmarkDeferredStartupTranscriptJumpTopBottomWindowSwitch(b *testing.B) {
	m := benchmarkDeferredStartupModel(b, 1500)
	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		m.jumpToVisibleBlockOrdinal(1)
		m.handleNormalKey(modelSelectKey("G"))
	}
}

func BenchmarkDeferredStartupModelBuild(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		_ = benchmarkDeferredStartupModel(b, 1500)
	}
}
