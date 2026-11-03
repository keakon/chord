package tui

import (
	"runtime"
	"testing"
	"time"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/tools"
)

type idleReclaimAgent struct {
	sessionControlAgent
	loopBusy       bool
	compactionBusy bool
}

func (a *idleReclaimAgent) LoopKeepsMainBusy() bool   { return a.loopBusy }
func (a *idleReclaimAgent) IsCompactionRunning() bool { return a.compactionBusy }

func TestHeapReclaimableBytes(t *testing.T) {
	tests := []struct {
		name string
		mem  runtime.MemStats
		want uint64
	}{
		{name: "idle above released", mem: runtime.MemStats{HeapIdle: 10 << 20, HeapReleased: 3 << 20}, want: 7 << 20},
		{name: "equal", mem: runtime.MemStats{HeapIdle: 5 << 20, HeapReleased: 5 << 20}, want: 0},
		{name: "released above idle", mem: runtime.MemStats{HeapIdle: 3 << 20, HeapReleased: 5 << 20}, want: 0},
		{name: "zero", mem: runtime.MemStats{}, want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := heapReclaimableBytes(&tt.mem); got != tt.want {
				t.Fatalf("heapReclaimableBytes = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestShouldAttemptFreeOSMemory(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name        string
		idleSince   time.Time
		lastAttempt time.Time
		pending     bool
		want        bool
	}{
		{name: "no idle window", idleSince: time.Time{}, want: false},
		{name: "below idle delay", idleSince: now.Add(-freeOSMemoryIdleDelay + time.Second), want: false},
		{name: "reached idle delay", idleSince: now.Add(-freeOSMemoryIdleDelay), want: true},
		{name: "recent attempt", idleSince: now.Add(-10 * time.Minute), lastAttempt: now.Add(-time.Minute), want: false},
		{name: "attempt at min interval", idleSince: now.Add(-10 * time.Minute), lastAttempt: now.Add(-freeOSMemoryMinInterval), want: true},
		{name: "pending", idleSince: now.Add(-10 * time.Minute), pending: true, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldAttemptFreeOSMemory(now, tt.idleSince, tt.lastAttempt, tt.pending); got != tt.want {
				t.Fatalf("shouldAttemptFreeOSMemory = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestHasActiveWorkForIdleReclaim(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, m *Model)
		want  bool
	}{
		{name: "globally idle", setup: func(*testing.T, *Model) {}, want: false},
		{
			name: "queued draft",
			setup: func(_ *testing.T, m *Model) {
				m.queuedDrafts = []queuedDraft{{ID: "draft-1"}}
			},
			want: true,
		},
		{
			name: "mailbox queue",
			setup: func(_ *testing.T, m *Model) {
				m.mailboxQueue = []queuedMailbox{{MessageID: "msg-1"}}
			},
			want: true,
		},
		{
			name: "inflight draft",
			setup: func(_ *testing.T, m *Model) {
				m.inflightDraft = &queuedDraft{ID: "draft-1"}
			},
			want: true,
		},
		{
			name: "other composer draft",
			setup: func(_ *testing.T, m *Model) {
				m.input.SetValue("draft text")
				state := m.input.SaveState()
				m.input.Reset()
				m.agentComposerStates = map[string]agentComposerState{"worker": {input: state}}
			},
			want: true,
		},
		{
			name: "active background job",
			setup: func(_ *testing.T, m *Model) {
				m.jobsSnapshotValid = true
				m.jobsSnapshotFrame = m.renderFrameGeneration
				m.activeJobsSnapshot = append(m.activeJobsSnapshot, tools.JobState{ID: "job-1", Status: jobStatusRunning})
			},
			want: true,
		},
		{
			name: "loop keeps main busy",
			setup: func(_ *testing.T, m *Model) {
				m.agent = &idleReclaimAgent{loopBusy: true}
			},
			want: true,
		},
		{
			name: "compaction running",
			setup: func(_ *testing.T, m *Model) {
				m.agent = &idleReclaimAgent{compactionBusy: true}
			},
			want: true,
		},
		{
			name: "compaction status active",
			setup: func(_ *testing.T, m *Model) {
				m.compactionBgStatus.Active = true
			},
			want: true,
		},
		{
			name: "session switch",
			setup: func(_ *testing.T, m *Model) {
				m.sessionSwitch = sessionSwitchState{kind: "resume", sessionID: "session-1"}
			},
			want: true,
		},
		{
			name: "takeover input frozen",
			setup: func(_ *testing.T, m *Model) {
				m.takeover.sourceFrozen = true
			},
			want: true,
		},
		{
			name: "confirm dialog",
			setup: func(_ *testing.T, m *Model) {
				m.confirm.request = &ConfirmRequest{RequestID: "req-1"}
			},
			want: true,
		},
		{
			name: "pending tool work",
			setup: func(_ *testing.T, m *Model) {
				m.viewport.AppendBlock(&Block{ID: 1, Type: BlockToolCall, ToolName: "read", ResultDone: false})
			},
			want: true,
		},
		{
			name: "pending local shell",
			setup: func(_ *testing.T, m *Model) {
				m.viewport.AppendBlock(&Block{ID: 1, Type: BlockUser, UserLocalShellCmd: "echo hi", UserLocalShellPending: true})
			},
			want: true,
		},
		{
			name: "other agent runtime activity",
			setup: func(_ *testing.T, m *Model) {
				m.activities["worker"] = agent.AgentActivityEvent{Type: agent.ActivityStreaming, AgentID: "worker"}
			},
			want: true,
		},
		{
			name: "running subagent",
			setup: func(_ *testing.T, m *Model) {
				m.agent = &idleReclaimAgent{subAgents: []agent.SubAgentInfo{
					{State: string(agent.SubAgentStateRunning)},
				}}
			},
			want: true,
		},
		{
			name: "idle subagent",
			setup: func(_ *testing.T, m *Model) {
				m.agent = &idleReclaimAgent{subAgents: []agent.SubAgentInfo{
					{State: string(agent.SubAgentStateIdle)},
				}}
			},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resetJobsRegistry(t)
			m := NewModelWithSize(nil, 80, 24)
			tt.setup(t, &m)
			if got := m.hasActiveWorkForIdleReclaim(); got != tt.want {
				t.Fatalf("hasActiveWorkForIdleReclaim = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestIdleTickDispatchesFreeOSMemoryAtThirtySeconds(t *testing.T) {
	resetJobsRegistry(t)
	m := NewModelWithSize(nil, 80, 24)
	m.displayState = stateBackground
	m.backgroundIdleSince = time.Now().Add(-freeOSMemoryIdleDelay)
	m.idleSweepGeneration = 3

	cmd := m.handleIdleSweepTick(idleSweepTickMsg{generation: 3})
	if cmd == nil {
		t.Fatal("idle tick should schedule the next maintenance check")
	}
	if !m.freeOSMemoryPending {
		t.Fatal("idle tick should dispatch one heap reclaim attempt")
	}
	if m.freeOSMemoryGeneration != 1 {
		t.Fatalf("freeOSMemoryGeneration = %d, want 1", m.freeOSMemoryGeneration)
	}
	if !m.lastCacheSweepAt.IsZero() {
		t.Fatal("30-second reclaim must not run the cache sweep")
	}
	if !m.idleSweepScheduled {
		t.Fatal("next maintenance tick should be scheduled")
	}
}

func TestIdleTickBeforeThirtySecondsDoesNotReclaim(t *testing.T) {
	resetJobsRegistry(t)
	m := NewModelWithSize(nil, 80, 24)
	m.displayState = stateBackground
	m.backgroundIdleSince = time.Now().Add(-10 * time.Second)
	m.idleSweepGeneration = 3

	cmd := m.handleIdleSweepTick(idleSweepTickMsg{generation: 3})
	if cmd == nil {
		t.Fatal("idle tick should reschedule while the idle window is still short")
	}
	if m.freeOSMemoryPending {
		t.Fatal("no reclaim attempt should be pending before the idle delay")
	}
	if !m.lastCacheSweepAt.IsZero() {
		t.Fatal("no cache sweep should run before the idle delay")
	}
}

func TestIdleTickCacheSweepRunsOnceAfterFiveMinutes(t *testing.T) {
	resetJobsRegistry(t)
	m := NewModelWithSize(nil, 80, 24)
	m.displayState = stateBackground
	m.backgroundIdleSince = time.Now().Add(-cacheSweepIdleDelay)
	// Keep the reclaim interval closed so this test isolates the cache sweep.
	m.lastFreeOSMemoryAt = time.Now()
	m.idleSweepGeneration = 5

	m.handleIdleSweepTick(idleSweepTickMsg{generation: 5})
	if m.lastCacheSweepAt.IsZero() {
		t.Fatal("cache sweep should run once the idle window reaches its delay")
	}
	if m.freeOSMemoryPending {
		t.Fatal("cache sweep must not dispatch a heap reclaim on its own")
	}
	firstSweep := m.lastCacheSweepAt

	m.handleIdleSweepTick(idleSweepTickMsg{generation: m.idleSweepGeneration})
	if !m.lastCacheSweepAt.Equal(firstSweep) {
		t.Fatalf("cache sweep ran again before its interval: first=%v second=%v", firstSweep, m.lastCacheSweepAt)
	}
}

func TestIdleTickClearsWindowWhenWorkResumes(t *testing.T) {
	resetJobsRegistry(t)
	m := NewModelWithSize(nil, 80, 24)
	m.displayState = stateBackground
	m.backgroundIdleSince = time.Now().Add(-time.Minute)
	m.queuedDrafts = []queuedDraft{{ID: "draft-1"}}
	m.idleSweepGeneration = 6

	m.handleIdleSweepTick(idleSweepTickMsg{generation: 6})
	if !m.backgroundIdleSince.IsZero() {
		t.Fatal("busy background must clear the idle window")
	}
	if m.freeOSMemoryPending {
		t.Fatal("busy background must not dispatch a heap reclaim")
	}
	if m.idleSweepScheduled {
		t.Fatal("busy background must not keep a maintenance tick scheduled")
	}
}

func TestIdleTickInForegroundClearsWindow(t *testing.T) {
	resetJobsRegistry(t)
	m := NewModelWithSize(nil, 80, 24)
	m.displayState = stateForeground
	m.backgroundIdleSince = time.Now().Add(-time.Minute)
	m.idleSweepGeneration = 6

	m.handleIdleSweepTick(idleSweepTickMsg{generation: 6})
	if !m.backgroundIdleSince.IsZero() {
		t.Fatal("foreground tick must clear the idle window")
	}
	if m.freeOSMemoryPending {
		t.Fatal("foreground tick must not dispatch a heap reclaim")
	}
}

func TestIdleTickPendingReclaimIsNotDispatchedTwice(t *testing.T) {
	resetJobsRegistry(t)
	m := NewModelWithSize(nil, 80, 24)
	m.displayState = stateBackground
	m.backgroundIdleSince = time.Now().Add(-time.Minute)
	m.freeOSMemoryPending = true
	m.freeOSMemoryGeneration = 4
	m.idleSweepGeneration = 7

	m.handleIdleSweepTick(idleSweepTickMsg{generation: 7})
	if !m.freeOSMemoryPending {
		t.Fatal("in-flight reclaim must stay pending")
	}
	if m.freeOSMemoryGeneration != 4 {
		t.Fatalf("freeOSMemoryGeneration = %d, want 4 (no new dispatch)", m.freeOSMemoryGeneration)
	}
}

func TestFreeOSMemoryResultRecordsAttempt(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.freeOSMemoryPending = true
	m.freeOSMemoryGeneration = 2

	m.handleFreeOSMemoryResult(freeOSMemoryResultMsg{generation: 2, reclaimable: 12 << 20, ran: true})

	if m.freeOSMemoryPending {
		t.Fatal("result should clear the pending flag")
	}
	if m.lastFreeOSMemoryAt.IsZero() {
		t.Fatal("result should record the attempt time")
	}
	if m.freeOSMemoryLastReclaimable != 12<<20 {
		t.Fatalf("freeOSMemoryLastReclaimable = %d, want %d", m.freeOSMemoryLastReclaimable, 12<<20)
	}
	if !m.freeOSMemoryLastRan {
		t.Fatal("freeOSMemoryLastRan should record a forced reclaim")
	}
}

func TestFreeOSMemoryResultIgnoresStaleGeneration(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.freeOSMemoryPending = true
	m.freeOSMemoryGeneration = 3

	m.handleFreeOSMemoryResult(freeOSMemoryResultMsg{generation: 2, reclaimable: 12 << 20, ran: true})

	if !m.freeOSMemoryPending {
		t.Fatal("stale result must not clear the pending flag")
	}
	if !m.lastFreeOSMemoryAt.IsZero() || m.freeOSMemoryLastReclaimable != 0 || m.freeOSMemoryLastRan {
		t.Fatal("stale result must not update attempt diagnostics")
	}
}

func TestRunFreeOSMemoryCmdHonorsReclaimableThreshold(t *testing.T) {
	tests := []struct {
		name        string
		heapIdle    uint64
		wantRan     bool
		wantReclaim uint64
	}{
		{name: "below threshold", heapIdle: freeOSMemoryReclaimableThreshold - 1, wantRan: false, wantReclaim: freeOSMemoryReclaimableThreshold - 1},
		{name: "at threshold", heapIdle: freeOSMemoryReclaimableThreshold, wantRan: true, wantReclaim: freeOSMemoryReclaimableThreshold},
		{name: "above threshold", heapIdle: 12 << 20, wantRan: true, wantReclaim: 12 << 20},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var freed bool
			msg := runFreeOSMemoryCmd(9, new(freeOSMemoryGate), func(mem *runtime.MemStats) {
				mem.HeapIdle = tt.heapIdle
			}, func() { freed = true })()
			result, ok := msg.(freeOSMemoryResultMsg)
			if !ok {
				t.Fatalf("command message = %T, want freeOSMemoryResultMsg", msg)
			}
			if result.generation != 9 {
				t.Fatalf("generation = %d, want 9", result.generation)
			}
			if result.reclaimable != tt.wantReclaim {
				t.Fatalf("reclaimable = %d, want %d", result.reclaimable, tt.wantReclaim)
			}
			if result.ran != tt.wantRan {
				t.Fatalf("ran = %t, want %t", result.ran, tt.wantRan)
			}
			if freed != tt.wantRan {
				t.Fatalf("FreeOSMemory called = %t, want %t", freed, tt.wantRan)
			}
		})
	}
}

func TestResumedActivityCancelsPendingReclaim(t *testing.T) {
	resetJobsRegistry(t)
	m := NewModelWithSize(nil, 80, 24)
	m.displayState = stateBackground
	m.backgroundIdleSince = time.Now().Add(-time.Minute)
	m.freeOSMemoryPending = true
	m.freeOSMemoryGeneration = 4
	gate := new(freeOSMemoryGate)
	m.freeOSMemoryGate = gate
	m.queuedDrafts = []queuedDraft{{ID: "draft-1"}}

	m.updateBackgroundIdleSweepState()

	if m.freeOSMemoryPending {
		t.Fatal("resumed activity must clear the pending reclaim")
	}
	if m.freeOSMemoryGate != nil {
		t.Fatal("resumed activity must detach the reclaim gate")
	}
	freed := false
	if gate.free(func() { freed = true }) {
		t.Fatal("cancelled gate still allowed FreeOSMemory")
	}
	if freed {
		t.Fatal("cancelled gate invoked FreeOSMemory")
	}
}

func TestForegroundFocusCancelsPendingReclaim(t *testing.T) {
	resetJobsRegistry(t)
	m := NewModelWithSize(nil, 80, 24)
	m.displayState = stateBackground
	m.backgroundIdleSince = time.Now().Add(-time.Minute)
	m.freeOSMemoryPending = true
	m.freeOSMemoryGeneration = 7
	gate := new(freeOSMemoryGate)
	m.freeOSMemoryGate = gate

	_ = m.handleFocusMsg()

	if m.freeOSMemoryPending {
		t.Fatal("focus must clear the pending reclaim")
	}
	if m.freeOSMemoryGate != nil {
		t.Fatal("focus must detach the reclaim gate")
	}
	freed := false
	if gate.free(func() { freed = true }) {
		t.Fatal("focus-cancelled gate still allowed FreeOSMemory")
	}
	if freed {
		t.Fatal("focus-cancelled gate invoked FreeOSMemory")
	}
}

func TestForegroundFocusDoesNotWaitForRunningReclaim(t *testing.T) {
	resetJobsRegistry(t)
	m := NewModelWithSize(nil, 80, 24)
	m.displayState = stateBackground
	m.freeOSMemoryPending = true
	gate := new(freeOSMemoryGate)
	m.freeOSMemoryGate = gate

	freeStarted := make(chan struct{})
	releaseFree := make(chan struct{})
	freeDone := make(chan struct{})
	go func() {
		defer close(freeDone)
		gate.free(func() {
			close(freeStarted)
			<-releaseFree
		})
	}()

	select {
	case <-freeStarted:
	case <-time.After(time.Second):
		close(releaseFree)
		t.Fatal("reclaim did not start")
	}

	focusDone := make(chan struct{})
	go func() {
		defer close(focusDone)
		_ = m.handleFocusMsg()
	}()
	select {
	case <-focusDone:
	case <-time.After(time.Second):
		close(releaseFree)
		t.Fatal("focus waited for the running reclaim")
	}
	if m.freeOSMemoryPending {
		t.Fatal("focus must clear the pending reclaim")
	}
	if m.freeOSMemoryGate != nil {
		t.Fatal("focus must detach the reclaim gate")
	}

	close(releaseFree)
	select {
	case <-freeDone:
	case <-time.After(time.Second):
		t.Fatal("reclaim did not finish after release")
	}
}

func TestRunFreeOSMemoryCmdSkipsGateCancelledDuringRead(t *testing.T) {
	gate := new(freeOSMemoryGate)
	readStarted := make(chan struct{})
	releaseRead := make(chan struct{})
	freed := make(chan struct{}, 1)
	cmd := runFreeOSMemoryCmd(5, gate, func(mem *runtime.MemStats) {
		close(readStarted)
		<-releaseRead
		mem.HeapIdle = 16 << 20
	}, func() { freed <- struct{}{} })
	resultCh := make(chan freeOSMemoryResultMsg, 1)
	go func() {
		resultCh <- cmd().(freeOSMemoryResultMsg)
	}()

	select {
	case <-readStarted:
	case <-time.After(time.Second):
		close(releaseRead)
		t.Fatal("freeOSMemory command did not start its heap read")
	}
	gate.cancel()
	close(releaseRead)

	select {
	case result := <-resultCh:
		if result.ran {
			t.Fatal("cancelled reclaim reported a forced GC")
		}
		if result.reclaimable != 16<<20 {
			t.Fatalf("reclaimable = %d, want 16 MiB", result.reclaimable)
		}
	case <-time.After(time.Second):
		t.Fatal("freeOSMemory command did not finish")
	}
	select {
	case <-freed:
		t.Fatal("cancelled reclaim invoked FreeOSMemory")
	default:
	}
}

func TestNextIdleMaintenanceDelay(t *testing.T) {
	now := time.Now()
	m := NewModelWithSize(nil, 80, 24)

	m.backgroundIdleSince = now
	if got := m.nextIdleMaintenanceDelay(now); got != freeOSMemoryIdleDelay {
		t.Fatalf("fresh idle delay = %v, want %v", got, freeOSMemoryIdleDelay)
	}

	m.backgroundIdleSince = now.Add(-cacheSweepIdleDelay)
	if got := m.nextIdleMaintenanceDelay(now); got != time.Millisecond {
		t.Fatalf("overdue delay = %v, want 1ms", got)
	}

	m.backgroundIdleSince = now.Add(-time.Minute)
	m.lastFreeOSMemoryAt = now.Add(-time.Minute)
	m.lastCacheSweepAt = now.Add(-30 * time.Second)
	if got := m.nextIdleMaintenanceDelay(now); got != 4*time.Minute {
		t.Fatalf("reclaim delay = %v, want 4m", got)
	}

	m.freeOSMemoryPending = true
	if got := m.nextIdleMaintenanceDelay(now); got != 4*time.Minute+30*time.Second {
		t.Fatalf("pending reclaim delay = %v, want 4m30s", got)
	}
}
