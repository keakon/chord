package tui

import (
	"testing"
	"time"

	"github.com/keakon/chord/internal/agent"
)

func TestWaitForAgentEventMicroBatchesStreamText(t *testing.T) {
	ch := make(chan agent.AgentEvent, agentEventBatchMax)
	ch <- agent.StreamTextEvent{Text: "a"}
	cmd := waitForAgentEvent(ch)

	go func() {
		time.Sleep(agentEventStreamBatchWindow / 2)
		ch <- agent.StreamTextEvent{Text: "b"}
	}()

	msg := cmd()
	batch, ok := msg.(agentEventBatchMsg)
	if !ok {
		t.Fatalf("waitForAgentEvent() = %T, want agentEventBatchMsg", msg)
	}
	if len(batch) != 2 {
		t.Fatalf("batch length = %d, want 2", len(batch))
	}
}

func TestWaitForAgentEventReducesPacedStreamTextWakeups(t *testing.T) {
	const events = 5
	ch := make(chan agent.AgentEvent, events)
	ch <- agent.StreamTextEvent{Text: "0"}
	cmd := waitForAgentEvent(ch)

	go func() {
		for i := 1; i < events; i++ {
			time.Sleep(agentEventStreamBatchWindow / events)
			ch <- agent.StreamTextEvent{Text: "x"}
		}
	}()

	msg := cmd()
	batch, ok := msg.(agentEventBatchMsg)
	if !ok {
		t.Fatalf("waitForAgentEvent() = %T, want agentEventBatchMsg", msg)
	}
	if len(batch) <= 1 {
		t.Fatalf("batch length = %d, want more than 1 paced stream event", len(batch))
	}
	if len(batch) >= events {
		return
	}
	// Scheduling jitter can leave the final event just outside the micro-batch
	// window, but the batch must still reduce wakeups for paced stream output.
	if len(batch) < events-1 {
		t.Fatalf("batch length = %d, want at least %d", len(batch), events-1)
	}
}

func TestWaitForAgentEventStopsStreamTextBatchAtMax(t *testing.T) {
	ch := make(chan agent.AgentEvent, agentEventBatchMax+1)
	for i := 0; i < agentEventBatchMax+1; i++ {
		ch <- agent.StreamTextEvent{Text: "x"}
	}
	msg := waitForAgentEvent(ch)()
	batch, ok := msg.(agentEventBatchMsg)
	if !ok {
		t.Fatalf("waitForAgentEvent() = %T, want agentEventBatchMsg", msg)
	}
	if len(batch) != agentEventBatchMax {
		t.Fatalf("batch length = %d, want %d", len(batch), agentEventBatchMax)
	}
}

func TestWaitForAgentEventDoesNotDelayNonStreamingEvent(t *testing.T) {
	ch := make(chan agent.AgentEvent, agentEventBatchMax)
	ch <- agent.IdleEvent{}
	cmd := waitForAgentEvent(ch)

	start := time.Now()
	msg := cmd()
	if elapsed := time.Since(start); elapsed >= agentEventStreamBatchWindow/2 {
		t.Fatalf("waitForAgentEvent delayed non-streaming event by %s", elapsed)
	}
	batch, ok := msg.(agentEventBatchMsg)
	if !ok {
		t.Fatalf("waitForAgentEvent() = %T, want agentEventBatchMsg", msg)
	}
	if len(batch) != 1 {
		t.Fatalf("batch length = %d, want 1", len(batch))
	}
}

func BenchmarkWaitForAgentEventStreamTextMicroBatch(b *testing.B) {
	for i := 0; i < b.N; i++ {
		ch := make(chan agent.AgentEvent, agentEventBatchMax)
		for j := 0; j < agentEventBatchMax; j++ {
			ch <- agent.StreamTextEvent{Text: "x"}
		}
		msg := waitForAgentEvent(ch)()
		batch := msg.(agentEventBatchMsg)
		if len(batch) != agentEventBatchMax {
			b.Fatalf("batch length = %d, want %d", len(batch), agentEventBatchMax)
		}
	}
}

func BenchmarkWaitForAgentEventPacedStreamTextMicroBatch(b *testing.B) {
	const events = 5
	var totalBatches int
	var totalEvents int
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ch := make(chan agent.AgentEvent, events)
		ch <- agent.StreamTextEvent{Text: "0"}
		go func() {
			for j := 1; j < events; j++ {
				time.Sleep(agentEventStreamBatchWindow / events)
				ch <- agent.StreamTextEvent{Text: "x"}
			}
		}()
		msg := waitForAgentEvent(ch)()
		batch := msg.(agentEventBatchMsg)
		totalBatches++
		totalEvents += len(batch)
		for totalEvents < (i+1)*events {
			msg := waitForAgentEvent(ch)()
			batch := msg.(agentEventBatchMsg)
			totalBatches++
			totalEvents += len(batch)
		}
	}
	b.StopTimer()
	b.ReportMetric(float64(totalEvents)/float64(totalBatches), "events/batch")
	b.ReportMetric(float64(totalBatches)/float64(totalEvents), "batches/event")
}
