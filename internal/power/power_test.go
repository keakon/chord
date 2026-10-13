package power

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

type fakeBackend struct {
	acquire func(context.Context) (Lease, error)
	calls   atomic.Int32
}

func (f *fakeBackend) Acquire(ctx context.Context) (Lease, error) {
	f.calls.Add(1)
	return f.acquire(ctx)
}

type fakeLease struct {
	done      chan struct{}
	once      sync.Once
	closeOnce sync.Once
	err       error
	closeErr  error
	closing   chan struct{}
	proceed   chan struct{}
	closes    atomic.Int32
}

func newFakeLease() *fakeLease             { return &fakeLease{done: make(chan struct{})} }
func (l *fakeLease) Done() <-chan struct{} { return l.done }
func (l *fakeLease) Err() error            { return l.err }
func (l *fakeLease) lose(err error)        { l.once.Do(func() { l.err = err; close(l.done) }) }
func (l *fakeLease) Close() error {
	l.closeOnce.Do(func() {
		l.closes.Add(1)
		if l.closing != nil {
			close(l.closing)
		}
		if l.proceed != nil {
			<-l.proceed
		}
		l.once.Do(func() { close(l.done) })
	})
	return l.closeErr
}
func managerForTest(t *testing.T) (*Manager, *fakeBackend) {
	t.Helper()
	b := &fakeBackend{acquire: func(context.Context) (Lease, error) { return newFakeLease(), nil }}
	m := NewManager(b)
	t.Cleanup(func() {
		if err := m.Close(); err != nil {
			t.Error(err)
		}
	})
	return m, b
}
func TestIsSleepPreventingActivity(t *testing.T) {
	for _, state := range []ActivityType{ActivityPreparing, ActivityConnecting, ActivityWaitingHeader, ActivityWaitingToken, ActivityStreaming, ActivityExecuting, ActivityRetrying, ActivityRetryingKey, ActivityCooling} {
		if !IsSleepPreventing(state) {
			t.Errorf("expected active: %s", state)
		}
	}
	for _, state := range []ActivityType{ActivityIdle, ActivityCompacting, "unknown"} {
		if IsSleepPreventing(state) {
			t.Errorf("expected idle: %s", state)
		}
	}
}
func TestManagerAggregationAndIdleDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m, b := managerForTest(t)
		m.UpdateActivity("main", ActivityCompacting)
		synctest.Wait()
		if b.calls.Load() != 0 {
			t.Fatal("compaction acquired")
		}
		m.UpdateActivity("sub", ActivityExecuting)
		synctest.Wait()
		if !m.IsHeld() {
			t.Fatal("subagent did not acquire")
		}
		m.UpdateActivity("main", ActivityIdle)
		synctest.Wait()
		if !m.IsHeld() {
			t.Fatal("idle main released active subagent")
		}
		m.UpdateActivity("sub", ActivityIdle)
		time.Sleep(500 * time.Millisecond)
		m.UpdateActivity("sub", ActivityIdle)
		time.Sleep(500 * time.Millisecond)
		synctest.Wait()
		if m.IsHeld() {
			t.Fatal("repeated idle postponed release")
		}
		m.mu.Lock()
		remaining := len(m.activities)
		m.mu.Unlock()
		if remaining != 0 {
			t.Fatal("inactive agent retained")
		}
	})
}
func TestManagerReactivationKeepsLease(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m, b := managerForTest(t)
		m.UpdateActivity("main", ActivityStreaming)
		synctest.Wait()
		m.UpdateActivity("main", ActivityIdle)
		time.Sleep(500 * time.Millisecond)
		m.UpdateActivity("main", ActivityStreaming)
		time.Sleep(time.Second)
		synctest.Wait()
		if !m.IsHeld() || b.calls.Load() != 1 {
			t.Fatal("reactivation replaced lease")
		}
	})
}
func TestManagerObserverAndStaleAcquisition(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		entered, proceed := make(chan struct{}), make(chan struct{})
		old := newFakeLease()
		b := &fakeBackend{}
		b.acquire = func(ctx context.Context) (Lease, error) {
			if b.calls.Load() == 1 {
				close(entered)
				<-proceed
				return old, nil
			}
			return newFakeLease(), nil
		}
		m := NewManager(b)
		defer m.Close()
		m.UpdateActivity("main", ActivityStreaming)
		<-entered
		m.UpdateActivity("main", ActivityIdle) // Must return while Acquire is blocked.
		m.UpdateActivity("main", ActivityStreaming)
		close(proceed)
		synctest.Wait()
		if old.closes.Load() != 1 || b.calls.Load() != 2 || !m.IsHeld() {
			t.Fatal("cancelled success was revived")
		}
	})
}
func TestManagerConcurrentCloseWaitsForCleanup(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := newFakeLease()
		l.closing = make(chan struct{})
		l.proceed = make(chan struct{})
		l.closeErr = errors.Join(ErrCleanupFailed, errors.New("close failed"))
		b := &fakeBackend{acquire: func(context.Context) (Lease, error) { return l, nil }}
		m := NewManager(b)
		m.UpdateActivity("main", ActivityStreaming)
		synctest.Wait()
		results := make(chan error, 2)
		go func() { results <- m.Close() }()
		<-l.closing
		go func() { results <- m.Close() }()
		synctest.Wait()
		select {
		case <-results:
			t.Fatal("Close returned before cleanup")
		default:
		}
		close(l.proceed)
		for range 2 {
			if !errors.Is(<-results, ErrCleanupFailed) {
				t.Fatal("cleanup error lost")
			}
		}
		m.UpdateActivity("main", ActivityStreaming)
		synctest.Wait()
		if b.calls.Load() != 1 || l.closes.Load() != 1 {
			t.Fatal("resources reused after close")
		}
	})
}
func TestManagerCloseCancelsAcquire(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		entered := make(chan struct{})
		b := &fakeBackend{acquire: func(ctx context.Context) (Lease, error) { close(entered); <-ctx.Done(); return nil, ctx.Err() }}
		m := NewManager(b)
		m.UpdateActivity("main", ActivityStreaming)
		<-entered
		if err := m.Close(); err != nil {
			t.Fatal(err)
		}
		if m.IsHeld() {
			t.Fatal("held after cancellation")
		}
	})
}
func TestManagerLossRetryAndStableReset(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var current *fakeLease
		b := &fakeBackend{acquire: func(context.Context) (Lease, error) { current = newFakeLease(); return current, nil }}
		m := NewManager(b)
		defer m.Close()
		m.UpdateActivity("main", ActivityStreaming)
		synctest.Wait()
		old := current
		old.lose(errors.New("lost"))
		synctest.Wait()
		if m.IsHeld() {
			t.Fatal("dead lease held")
		}
		time.Sleep(time.Second)
		synctest.Wait()
		if b.calls.Load() != 2 || !m.IsHeld() {
			t.Fatal("loss did not retry")
		}
		current.lose(errors.New("lost"))
		synctest.Wait()
		time.Sleep(time.Second)
		synctest.Wait()
		if b.calls.Load() != 2 {
			t.Fatal("unstable lease reset backoff")
		}
		time.Sleep(time.Second)
		synctest.Wait()
		if b.calls.Load() != 3 {
			t.Fatal("second backoff did not finish")
		}
		time.Sleep(stablePeriod)
		synctest.Wait()
		current.lose(errors.New("lost"))
		synctest.Wait()
		time.Sleep(time.Second)
		synctest.Wait()
		if b.calls.Load() != 4 || !m.IsHeld() {
			t.Fatal("stable lease did not reset backoff")
		}
		if old.closes.Load() != 1 {
			t.Fatal("old lease cleaned twice")
		}
	})
}
func TestManagerBackoffAndActivityChurn(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b := &fakeBackend{acquire: func(context.Context) (Lease, error) { return nil, errors.New("unavailable") }}
		m := NewManager(b)
		defer m.Close()
		m.UpdateActivity("main", ActivityStreaming)
		synctest.Wait()
		for i, d := range []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 30 * time.Second, 60 * time.Second, 60 * time.Second} {
			m.UpdateActivity("main", ActivityIdle)
			m.UpdateActivity("main", ActivityStreaming)
			synctest.Wait()
			time.Sleep(d - time.Nanosecond)
			synctest.Wait()
			if got := b.calls.Load(); got != int32(i+1) {
				t.Fatalf("early retry %d: %d", i, got)
			}
			time.Sleep(time.Nanosecond)
			synctest.Wait()
			if got := b.calls.Load(); got != int32(i+2) {
				t.Fatalf("missing retry %d: %d", i, got)
			}
		}
	})
}
func TestManagerTerminalFailures(t *testing.T) {
	for _, failure := range []error{ErrUnsupported, errors.Join(ErrCleanupFailed, errors.New("rollback failed"))} {
		t.Run(failure.Error(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				b := &fakeBackend{acquire: func(context.Context) (Lease, error) { return nil, failure }}
				m := NewManager(b)
				m.UpdateActivity("main", ActivityStreaming)
				synctest.Wait()
				time.Sleep(2 * time.Minute)
				synctest.Wait()
				if b.calls.Load() != 1 || m.IsHeld() {
					t.Fatal("terminal failure retried")
				}
				err := m.Close()
				if errors.Is(failure, ErrCleanupFailed) != errors.Is(err, ErrCleanupFailed) {
					t.Fatal("cleanup failure not preserved")
				}
			})
		})
	}
}
func TestManagerReleaseBlocksReplacement(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		old := newFakeLease()
		old.closing = make(chan struct{})
		old.proceed = make(chan struct{})
		b := &fakeBackend{}
		b.acquire = func(context.Context) (Lease, error) {
			if b.calls.Load() == 1 {
				return old, nil
			}
			return newFakeLease(), nil
		}
		m := NewManager(b)
		defer m.Close()
		m.UpdateActivity("main", ActivityStreaming)
		synctest.Wait()
		m.UpdateActivity("main", ActivityIdle)
		time.Sleep(time.Second)
		<-old.closing
		m.UpdateActivity("main", ActivityStreaming)
		synctest.Wait()
		if b.calls.Load() != 1 {
			t.Fatal("acquired during cleanup")
		}
		close(old.proceed)
		synctest.Wait()
		if b.calls.Load() != 2 || !m.IsHeld() {
			t.Fatal("did not reacquire")
		}
	})
}

func TestManagerDeadlineSuccessBacksOff(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		old := newFakeLease()
		b := &fakeBackend{acquire: func(ctx context.Context) (Lease, error) { <-ctx.Done(); return old, nil }}
		m := NewManager(b)
		defer m.Close()
		m.UpdateActivity("main", ActivityStreaming)
		synctest.Wait()
		time.Sleep(acquireTimeout)
		synctest.Wait()
		if old.closes.Load() != 1 || m.IsHeld() {
			t.Fatal("timed out success not cleaned")
		}
		time.Sleep(time.Second - time.Nanosecond)
		synctest.Wait()
		if b.calls.Load() != 1 {
			t.Fatal("deadline bypassed backoff")
		}
	})
}
func TestManagerIdleCleanupFailureStopsReplacement(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		old := newFakeLease()
		old.closeErr = ErrCleanupFailed
		b := &fakeBackend{acquire: func(context.Context) (Lease, error) { return old, nil }}
		m := NewManager(b)
		m.UpdateActivity("main", ActivityStreaming)
		synctest.Wait()
		m.UpdateActivity("main", ActivityIdle)
		time.Sleep(time.Second)
		synctest.Wait()
		m.UpdateActivity("main", ActivityStreaming)
		time.Sleep(time.Minute)
		synctest.Wait()
		if b.calls.Load() != 1 || m.IsHeld() {
			t.Fatal("cleanup failure allowed replacement")
		}
		if !errors.Is(m.Close(), ErrCleanupFailed) {
			t.Fatal("cleanup error not retained")
		}
	})
}
func TestManagerCloseRejectsLateSuccess(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		entered, proceed := make(chan struct{}), make(chan struct{})
		old := newFakeLease()
		b := &fakeBackend{acquire: func(context.Context) (Lease, error) { close(entered); <-proceed; return old, nil }}
		m := NewManager(b)
		m.UpdateActivity("main", ActivityStreaming)
		<-entered
		result := make(chan error, 1)
		go func() { result <- m.Close() }()
		synctest.Wait()
		m.UpdateActivity("main", ActivityStreaming)
		select {
		case <-result:
			t.Fatal("close did not await acquisition")
		default:
		}
		close(proceed)
		if err := <-result; err != nil {
			t.Fatal(err)
		}
		if old.closes.Load() != 1 || b.calls.Load() != 1 || m.IsHeld() {
			t.Fatal("late lease escaped close")
		}
	})
}
