package agent

import (
	"testing"
	"testing/synctest"
	"time"

	"github.com/keakon/chord/internal/message"
)

func TestSubAgentCancelConcurrentDirectoryCandidates(t *testing.T) {
	parent := newTestMainAgent(t, t.TempDir())
	s := newPersistenceTestSubAgent(parent, "worker-1")
	r := newDirectoryInstructionRuntime(t.Context(), directoryInstructionBinding{})
	defer r.cancel()
	s.directoryInstructions.Store(r)

	start := make(chan struct{})
	done := make(chan struct{})
	go func() {
		<-start
		s.cancelCurrentTurnFromLoop()
		close(done)
	}()
	close(start)
	// This is the owner-side request preparation that can overlap a stop
	// issued by the parent. Neither side waits for the other.
	ready := s.ensureDirectoryInstructionCandidates(s.turn, r)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("turn cancellation blocked")
	}
	if !ready || s.turn.Ctx.Err() == nil {
		t.Fatal("request preparation or cancellation did not complete")
	}
}

func TestSubAgentDirectoryDiscoveryCleanupOnCancellation(t *testing.T) {
	for _, mode := range []string{"active turn", "before loop", "shutdown"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				parent := newTestMainAgent(t, t.TempDir())
				s := newControllableTestSubAgent(t, parent, "task-1")
				turn := s.newTurn()
				r := newDirectoryInstructionRuntime(t.Context(), directoryInstructionBinding{})
				defer r.cancel()
				r.pending = map[string]directoryInstructionObservation{"pkg": {Status: "present"}}
				r.attempt.Store(&directoryInstructionAttempt{})
				frozen := r.frozen.Load()
				s.directoryInstructions.Store(r)
				defer func() {
					s.cancel()
					synctest.Wait()
				}()

				if mode == "before loop" {
					s.cancelCurrentTurnFromLoop()
					if len(r.pending) != 1 || r.attempt.Load() == nil {
						t.Fatal("external cancellation changed owner-only discovery state")
					}
				}
				go s.runLoop()
				synctest.Wait()
				switch mode {
				case "active turn":
					if len(r.pending) != 1 {
						t.Fatal("active discovery was cleared")
					}
					turn.Cancel()
				case "shutdown":
					s.cancel()
				}
				synctest.Wait()
				if len(r.pending) != 0 || r.attempt.Load() != nil {
					t.Fatal("owner did not clear cancelled discovery")
				}
				if r.frozen.Load() != frozen || r.ctx.Err() != nil {
					t.Fatal("turn cleanup discarded binding snapshot")
				}
				if mode == "shutdown" {
					select {
					case <-s.done:
					default:
						t.Fatal("worker did not stop")
					}
				}
			})
		})
	}
}

func TestSubAgentCancelledDirectoryPreparationKeepsOnlyCommittedFacts(t *testing.T) {
	for _, committed := range []bool{false, true} {
		name := "uncommitted"
		if committed {
			name = "committed"
		}
		t.Run(name, func(t *testing.T) {
			parent := newTestMainAgent(t, t.TempDir())
			s := newPersistenceTestSubAgent(parent, "worker-1")
			r := newDirectoryInstructionRuntime(t.Context(), directoryInstructionBinding{})
			defer r.cancel()
			fact := directoryTestFact("pkg", "package rules")
			r.pending = map[string]directoryInstructionObservation{"pkg": {Status: "present", Body: fact.Body}}
			s.directoryInstructions.Store(r)
			prep := &directoryInstructionPreparation{turnID: s.turn.ID, binding: r.binding, dynamic: true}
			s.directoryPreparation = prep
			s.llmRequestInFlight.Store(true)
			s.cancelCurrentTurnFromLoop()

			s.handleDirectoryInstructionsPrepared(directoryInstructionsPrepared{
				preparation: prep,
				runtime:     r,
				facts:       []message.DirectoryInstructionFact{fact},
				committed:   committed,
			})

			if s.directoryPreparation != nil || s.llmRequestInFlight.Load() || len(r.pending) != 0 {
				t.Fatal("cancelled preparation retained pending work or started a request")
			}
			history := s.ctxMgr.Snapshot()
			if committed {
				if !prep.adopted || len(history) != 1 || len(history[0].DirectoryInstructions.Facts) != 1 || history[0].DirectoryInstructions.Facts[0] != fact {
					t.Fatalf("committed fact was not retained: %+v", history)
				}
			} else if prep.adopted || len(history) != 0 {
				t.Fatalf("uncommitted fact entered history: %+v", history)
			}
		})
	}
}
