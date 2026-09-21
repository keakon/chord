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
	// Widen the window instead of racing it. The original pacing sent the
	// deltas across most of a 16ms window, so scheduling jitter on a loaded
	// machine pushed the tail past the deadline and failed the test for a
	// timing reason rather than a real one. The guarantee is that paced
	// deltas merge into one wakeup, so give the pacing room and assert it
	// exactly.
	restore := agentEventStreamBatchWindow
	agentEventStreamBatchWindow = 500 * time.Millisecond
	t.Cleanup(func() { agentEventStreamBatchWindow = restore })

	const events = 5
	ch := make(chan agent.AgentEvent, events)
	ch <- agent.StreamTextEvent{Text: "0"}
	cmd := waitForAgentEvent(ch)

	go func() {
		for i := 1; i < events; i++ {
			time.Sleep(2 * time.Millisecond)
			ch <- agent.StreamTextEvent{Text: "x"}
		}
	}()

	msg := cmd()
	batch, ok := msg.(agentEventBatchMsg)
	if !ok {
		t.Fatalf("waitForAgentEvent() = %T, want agentEventBatchMsg", msg)
	}
	if len(batch) != events {
		t.Fatalf("batch length = %d, want all %d paced stream events merged into one wakeup", len(batch), events)
	}
}

func TestWaitForAgentEventStopsStreamTextBatchAtMax(t *testing.T) {
	ch := make(chan agent.AgentEvent, agentEventBatchMax+1)
	for range agentEventBatchMax + 1 {
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
	for b.Loop() {
		ch := make(chan agent.AgentEvent, agentEventBatchMax)
		for range agentEventBatchMax {
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
	for b.Loop() {
		completed := totalEvents
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
		for totalEvents < completed+events {
			msg := waitForAgentEvent(ch)()
			batch := msg.(agentEventBatchMsg)
			totalBatches++
			totalEvents += len(batch)
		}
	}
	b.ReportMetric(float64(totalEvents)/float64(totalBatches), "events/batch")
	b.ReportMetric(float64(totalBatches)/float64(totalEvents), "batches/event")
}
