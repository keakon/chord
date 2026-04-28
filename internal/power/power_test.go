package power

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestManager_Basic(t *testing.T) {
	backend := &fakeBackend{}
	m := NewManager(backend)

	// Single agent goes active.
	m.UpdateActivity("main", ActivityConnecting)
	if !backend.Acquired() || !m.IsHeld() {
		t.Error("expected held after connecting")
	}

	// Idle again. The assertion should remain held until debounce expires.
	m.UpdateActivity("main", ActivityIdle)
	if !m.IsHeld() {
		t.Error("expected still held immediately after idle during debounce")
	}
	// Wait for debounce.
	backend.wait()
	if backend.Acquired() || m.IsHeld() {
		t.Error("expected released after debounce")
	}

	m.Close()
}

func TestManager_ActiveIdleActive(t *testing.T) {
	backend := &fakeBackend{}
	m := NewManager(backend)

	m.UpdateActivity("main", ActivityStreaming)
	if !m.IsHeld() {
		t.Fatal("expected held after streaming")
	}
	m.UpdateActivity("main", ActivityIdle)
	// Re-active before debounce expires.
	m.UpdateActivity("main", ActivityStreaming)
	if !m.IsHeld() {
		t.Error("expected still held after re-active")
	}
	if backend.acquireCount() != 1 {
		t.Errorf("acquireCount = %d, want 1", backend.acquireCount())
	}

	m.Close()
}

func TestManager_MultiAgent(t *testing.T) {
	backend := &fakeBackend{}
	m := NewManager(backend)

	m.UpdateActivity("main", ActivityIdle)
	m.UpdateActivity("sub-1", ActivityExecuting)
	if !m.IsHeld() {
		t.Error("expected held when subagent busy")
	}

	m.UpdateActivity("main", ActivityIdle)
	m.UpdateActivity("sub-1", ActivityIdle)
	// Wait for debounce.
	backend.wait()
	if m.IsHeld() {
		t.Error("expected released when all agents idle")
	}

	m.Close()
}

func TestManager_Idempotency(t *testing.T) {
	backend := &fakeBackend{}
	m := NewManager(backend)

	// Repeated active should not cause multiple acquires.
	for i := 0; i < 10; i++ {
		m.UpdateActivity("main", ActivityConnecting)
	}
	if backend.acquireCount() != 1 {
		t.Errorf("acquireCount = %d, want 1 with repeated active", backend.acquireCount())
	}

	m.Close()
}

func TestManager_Close(t *testing.T) {
	backend := &fakeBackend{}
	m := NewManager(backend)

	m.UpdateActivity("main", ActivityStreaming)
	m.Close()

	if !backend.released() {
		t.Error("expected released on Close")
	}

	// Update after close should be no-op.
	m.UpdateActivity("main", ActivityConnecting)
	if backend.acquireCount() != 1 {
		t.Errorf("acquireCount = %d after close, should not increment", backend.acquireCount())
	}
}

func TestIsSleepPreventingActivity(t *testing.T) {
	activeStates := []ActivityType{
		ActivityConnecting,
		ActivityWaitingHeader,
		ActivityWaitingToken,
		ActivityStreaming,
		ActivityExecuting,
		ActivityRetrying,
		ActivityRetryingKey,
		ActivityCooling,
	}
	for _, s := range activeStates {
		if !IsSleepPreventing(s) {
			t.Errorf("IsSleepPreventing(%s) = false, want true", s)
		}
	}

	idleStates := []ActivityType{ActivityIdle, ActivityCompacting}
	for _, s := range idleStates {
		if IsSleepPreventing(s) {
			t.Errorf("IsSleepPreventing(%s) = true, want false", s)
		}
	}
}

func TestManagerReleaseTimerRechecksActivity(t *testing.T) {
	backend := &fakeBackend{}
	m := NewManager(backend)
	m.releaseDelay = 20 * time.Millisecond

	m.UpdateActivity("main", ActivityStreaming)
	m.UpdateActivity("main", ActivityIdle)
	m.UpdateActivity("sub", ActivityExecuting)
	time.Sleep(40 * time.Millisecond)
	if !m.IsHeld() {
		t.Fatal("expected manager to stay held when another agent became active before release timer")
	}
	if backend.releaseCount() != 0 {
		t.Fatalf("releaseCount = %d, want 0", backend.releaseCount())
	}
	m.Close()
}

func TestManagerAcquireFailureDoesNotMarkHeld(t *testing.T) {
	backend := &fakeBackend{failAcquire: true}
	m := NewManager(backend)
	m.UpdateActivity("main", ActivityStreaming)
	if m.IsHeld() {
		t.Fatal("manager should not mark held when acquire fails")
	}
	if backend.acquireCount() != 1 {
		t.Fatalf("acquireCount = %d, want 1", backend.acquireCount())
	}
	m.Close()
}

func TestManagerCloseIdempotent(t *testing.T) {
	backend := &fakeBackend{}
	m := NewManager(backend)
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	if backend.releaseCount() != 1 {
		t.Fatalf("releaseCount = %d, want 1 from backend Close only", backend.releaseCount())
	}
}

// fakeBackend tracks Acquire/Release calls for testing.
type fakeBackend struct {
	acquireCountV atomic.Int32
	releaseCountV atomic.Int32
	acquiredV     atomic.Bool
	releasedV     atomic.Bool
	failAcquire   bool
}

func (f *fakeBackend) Acquire() error {
	f.acquireCountV.Add(1)
	if f.failAcquire {
		return errors.New("acquire failed")
	}
	f.acquiredV.Store(true)
	f.releasedV.Store(false)
	return nil
}

func (f *fakeBackend) Release() error {
	f.releaseCountV.Add(1)
	f.acquiredV.Store(false)
	f.releasedV.Store(true)
	return nil
}

func (f *fakeBackend) Close() error {
	return f.Release()
}

func (f *fakeBackend) Acquired() bool      { return f.acquiredV.Load() }
func (f *fakeBackend) released() bool      { return f.releasedV.Load() }
func (f *fakeBackend) acquireCount() int32 { return f.acquireCountV.Load() }
func (f *fakeBackend) releaseCount() int32 { return f.releaseCountV.Load() }

// wait waits until the manager's debounce releases.
func (f *fakeBackend) wait() {
	for i := 0; i < 100; i++ {
		if !f.Acquired() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}
