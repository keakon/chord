package agent

import (
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/memory"
)

// The status bar's MEMORY pill has to warn once memory stops working, so a
// stalled extraction marks the region degraded and a later success clears it.
func TestSetMemoryDegradedTracksStall(t *testing.T) {
	// A ToastEvent is a reliable event: it blocks until the TUI takes it, so
	// even the flip-only assertions need a channel that can hold both events.
	a := &MainAgent{outputCh: make(chan AgentEvent, 4)}
	if a.MemoryDegraded() {
		t.Fatal("fresh agent reported degraded memory")
	}
	a.setMemoryDegraded(true, "invalid memory extraction output: unknown field \"notes\"")
	if !a.MemoryDegraded() {
		t.Fatal("a stalled extraction must mark memory degraded")
	}
	a.setMemoryDegraded(false, "")
	if a.MemoryDegraded() {
		t.Fatal("a successful commit must clear the degraded mark")
	}
}

func TestMemoryDegradedNilReceiver(t *testing.T) {
	var a *MainAgent
	if a.MemoryDegraded() {
		t.Fatal("nil receiver reported degraded memory")
	}
}

// A degradation flips the health flag, announces it, and toasts why extraction
// stopped, so the user can act on something more than a red pill. Recovery only
// flips the pill back, and an unchanged state stays silent.
func TestSetMemoryDegradedAnnouncesFlipAndReason(t *testing.T) {
	a := &MainAgent{outputCh: make(chan AgentEvent, 4)}

	// The reason is a wrapped error, so it may arrive with line breaks.
	reason := "memory extraction setup failed:\nload frozen transcript: broken"
	a.setMemoryDegraded(true, reason)

	select {
	case evt := <-a.outputCh:
		health, ok := evt.(MemoryHealthEvent)
		if !ok || !health.Degraded {
			t.Fatalf("event = %#v, want a degraded MemoryHealthEvent", evt)
		}
	default:
		t.Fatal("a stalled extraction did not announce the health change")
	}
	select {
	case evt := <-a.outputCh:
		toast, ok := evt.(ToastEvent)
		if !ok {
			t.Fatalf("event = %#v, want a ToastEvent carrying the reason", evt)
		}
		if toast.Level != "warn" {
			t.Fatalf("toast level = %q, want warn", toast.Level)
		}
		if toast.Category != "memory_health" {
			t.Fatalf("toast category = %q, want memory_health", toast.Category)
		}
		if !strings.Contains(toast.Message, "load frozen transcript: broken") {
			t.Fatalf("toast message %q must carry the failure reason", toast.Message)
		}
		if strings.ContainsAny(toast.Message, "\n\r\t") {
			t.Fatalf("toast message %q must stay on one line", toast.Message)
		}
	default:
		t.Fatal("a stalled extraction did not toast the reason")
	}

	a.setMemoryDegraded(true, reason)
	select {
	case evt := <-a.outputCh:
		t.Fatalf("an unchanged degraded state re-announced health: %#v", evt)
	default:
	}

	a.setMemoryDegraded(false, "")
	select {
	case evt := <-a.outputCh:
		health, ok := evt.(MemoryHealthEvent)
		if !ok || health.Degraded {
			t.Fatalf("event = %#v, want a healthy MemoryHealthEvent", evt)
		}
	default:
		t.Fatal("recovery did not announce the health change")
	}
	select {
	case evt := <-a.outputCh:
		t.Fatalf("recovery must not toast: %#v", evt)
	default:
	}
}

// Memory health follows project memory, not only this process's own commits: a
// session that stalled here can be covered by another Chord process, and the
// stalled pill would otherwise stay red until this process commits or restarts.
func TestMemoryHealthRecoversFromCheckpointAdvance(t *testing.T) {
	projectRoot := t.TempDir()
	writeProjectMemory(t, projectRoot, "# Project Memory\n")
	a := newTestMainAgent(t, projectRoot)

	a.setMemoryDegraded(true, "invalid memory extraction output")
	// Place the stall before every checkpoint entry this test writes.
	a.memoryDegradedAt.Store(time.Now().Add(-time.Minute).UnixNano())

	// Nothing advanced yet: a drain must leave the indicator alone.
	a.drainMemoryQueue()
	if !a.MemoryDegraded() {
		t.Fatal("a drain without any checkpoint advance must keep the degraded mark")
	}

	// A checkpoint written before the stall is not progress either.
	older := &memory.ExtractionCheckpoint{Sessions: map[string]memory.SessionCoverage{
		"stale-session": {SourceFingerprint: "fp-stale", ExtractedAt: time.Now().Add(-time.Hour)},
	}}
	if err := memory.SaveCheckpoint(a.memoryMgr.Layout(), older); err != nil {
		t.Fatalf("SaveCheckpoint: %v", err)
	}
	a.drainMemoryQueue()
	if !a.MemoryDegraded() {
		t.Fatal("a checkpoint entry older than the stall must keep the degraded mark")
	}

	// Another process extracted this session after the stall: memory moved on.
	newer := &memory.ExtractionCheckpoint{}
	newer.SetCovered("other-session", "fp-other", 1, 0)
	if err := memory.SaveCheckpoint(a.memoryMgr.Layout(), newer); err != nil {
		t.Fatalf("SaveCheckpoint: %v", err)
	}
	a.drainMemoryQueue()
	if a.MemoryDegraded() {
		t.Fatal("a checkpoint advance from another process must clear the degraded mark")
	}
}
