package executiongate

import "testing"

func TestTryFreezeRejectsActiveWorkWithoutClosingAdmission(t *testing.T) {
	g := &Gate{}
	lease, err := g.TryBegin(LLM, "sample")
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Finish()
	frozen := g.Frozen()
	if state, ok := g.TryFreeze(0); ok || state.Active != 1 || g.Snapshot().Paused {
		t.Fatal("active work froze admission")
	}
	select {
	case <-frozen:
		t.Fatal("rejected freeze signalled preparation cancellation")
	default:
	}
	lease.Finish()
	if state, ok := g.TryFreeze(0); !ok || state.Active != 0 || !g.Snapshot().Paused {
		t.Fatal("quiescent freeze did not close admission")
	}
	select {
	case <-frozen:
	default:
		t.Fatal("successful freeze did not signal preparation cancellation")
	}
	if state, ok := g.TryFreeze(0); ok || state.FrozenAt.IsZero() {
		t.Fatal("already frozen gate changed freeze ownership")
	}
}

func TestTryFreezeSerializesConcurrentAdmission(t *testing.T) {
	for range 100 {
		g := &Gate{}
		ready, finished := make(chan struct{}), make(chan struct{})
		var lease *Lease
		var beginErr error
		go func() {
			<-ready
			lease, beginErr = g.TryBegin(LLM, "sample")
			close(finished)
		}()
		close(ready)
		state, frozen := g.TryFreeze(0)
		<-finished
		if frozen {
			if !IsPaused(beginErr) || lease != nil || state.Active != 0 {
				t.Fatal("execution admitted after freeze won")
			}
		} else {
			if beginErr != nil || lease == nil || g.Snapshot().Paused || state.Active != 1 {
				t.Fatal("losing freeze changed admitted execution")
			}
			lease.Finish()
		}
	}
}
