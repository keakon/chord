package agent

import "testing"

// /resume lists sessions before emitting the picker event, so the TUI must treat
// the payload as complete: a nil list means "no sessions", not "still loading".
func TestResumeSlashCommandEmitsPrefetchedSessionSelect(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())

	if !a.tryHandleSlashCommand("/resume") {
		t.Fatal("/resume was not handled")
	}

	for {
		select {
		case evt := <-a.outputCh:
			selectEvent, ok := evt.(SessionSelectEvent)
			if !ok {
				continue
			}
			if !selectEvent.Prefetched {
				t.Fatal("SessionSelectEvent.Prefetched = false, want true")
			}
			return
		default:
			t.Fatal("no SessionSelectEvent emitted for /resume")
		}
	}
}
