package agent

import (
	"strconv"
	"sync"
	"time"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/analytics"
	"github.com/keakon/chord/internal/identity"
)

// walltimeRecorder records settled wall-clock time segments per agent and
// appends them to the usage ledger as zero-usage diagnostic events. It is the
// agent-side fact source for the TIME sidebar section: segments are recorded
// at event terminal points by the LLM stream reducers, tool execution
// adoption points, compaction workers, and the interaction broker.
//
// The recorder never starts or ends timers itself; callers settle a segment
// (start..end wall clock) and hand it over. All methods are goroutine-safe:
// LLM stream callbacks, tool goroutines, compaction workers, and the broker
// paths can call concurrently. Failed ledger appends are logged and dropped —
// walltime is bookkeeping, never a reason to fail a tool or request.
type walltimeRecorder struct {
	mu         sync.Mutex
	tracker    *analytics.WalltimeTracker
	ledger     *analytics.UsageLedger
	persist    *persistencePump
	stopping   <-chan struct{}
	generation uint64
	compaction map[uint64]walltimeSegment
}

type walltimeTarget struct {
	recorder   *walltimeRecorder
	ledger     *analytics.UsageLedger
	generation uint64
	agentID    string
	agentName  string
	turnID     uint64
}

type walltimeSegment struct {
	target *walltimeTarget
	start  time.Time
}

func newWalltimeRecorder(ledger *analytics.UsageLedger, persist *persistencePump, stopping <-chan struct{}) *walltimeRecorder {
	return &walltimeRecorder{
		tracker:    analytics.NewWalltimeTracker(),
		ledger:     ledger,
		persist:    persist,
		stopping:   stopping,
		generation: 1,
		compaction: make(map[uint64]walltimeSegment),
	}
}

// captureAt pins both bookkeeping metadata and the current session ledger to
// a target. Late settlements therefore remain attributable to the segment's
// original turn and session after a switch.
func (r *walltimeRecorder) captureAt(agentID, agentName string, turnID uint64) *walltimeTarget {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	target := r.captureLocked(agentID, agentName, turnID)
	r.mu.Unlock()
	return target
}

// captureLocked builds a target pinned to the recorder's current ledger and
// generation. Callers must hold r.mu. The target stays valid after a session
// switch without acquiring the new ledger, so a late settlement is appended
// to the session it belongs to.
func (r *walltimeRecorder) captureLocked(agentID, agentName string, turnID uint64) *walltimeTarget {
	target := &walltimeTarget{
		recorder:   r,
		ledger:     r.ledger,
		generation: r.generation,
		agentID:    agentID,
		agentName:  agentName,
		turnID:     turnID,
	}
	return target
}

func (r *walltimeRecorder) recordTarget(target *walltimeTarget, purpose string, d time.Duration) {
	if target == nil || d <= 0 || !analytics.IsWalltimePurpose(purpose) {
		return
	}
	r.mu.Lock()
	if target.recorder == r && target.generation == r.generation {
		r.tracker.Record(target.agentID, purpose, d)
	}
	r.mu.Unlock()
	r.appendLedgerEvent(target, purpose, d)
}

func (r *walltimeRecorder) appendLedgerEvent(target *walltimeTarget, purpose string, d time.Duration) {
	if target == nil || target.ledger == nil {
		return
	}
	event := analytics.UsageEvent{
		EventID:    makeRequestID(),
		OccurredAt: time.Now(),
		AgentID:    target.agentID,
		AgentKind:  "walltime",
		AgentName:  target.agentName,
		Purpose:    purpose,
		TurnID:     target.turnID,
		Diagnostic: map[string]string{analytics.WalltimeDiagnosticNsKey: strconv.FormatInt(d.Nanoseconds(), 10)},
	}
	if r.persist != nil && r.persist.enqueue(persistEntry{
		walltimeLedger: target.ledger,
		walltimeEvent:  &event,
	}, r.stopping) {
		return
	}
	if err := target.ledger.AppendEvent(event); err != nil {
		log.Warnf("failed to append walltime ledger event agent_id=%v purpose=%v error=%v", target.agentID, purpose, err)
	}
}

// flush waits for walltime events already admitted to the shared persistence
// pump. It is called before a session ledger is replaced or the pump stops.
func (r *walltimeRecorder) flush() {
	if r == nil || r.persist == nil {
		return
	}
	r.persist.flush()
}

// statsForAgent returns the tracked snapshot for one agent.
func (r *walltimeRecorder) statsForAgent(agentID string) analytics.WalltimeStats {
	if r == nil || r.tracker == nil {
		return analytics.WalltimeStats{}
	}
	return r.tracker.StatsForAgent(agentID)
}

// statsForTask aggregates across a task's instance history, mirroring the
// token/cost aggregation in GetSidebarUsageStats so parked/rehydrated
// SubAgents keep their historical walltime under the new instance ID.
func (r *walltimeRecorder) statsForTask(rec *DurableTaskRecord) analytics.WalltimeStats {
	if r == nil || r.tracker == nil || rec == nil {
		return analytics.WalltimeStats{}
	}
	var out analytics.WalltimeStats
	for _, instanceID := range dedupeTaskInstanceHistory(rec.InstanceHistory) {
		out.Add(r.tracker.StatsForAgent(instanceID))
	}
	return out
}

// restoreStats replaces the tracked per-agent snapshot (session switch /
// resume baseline).
func (r *walltimeRecorder) restoreStats(snapshot map[string]*analytics.WalltimeStats) {
	if r == nil || r.tracker == nil {
		return
	}
	r.tracker.RestoreStats(snapshot)
}

// repointLedger switches the ledger this recorder appends to (session switch /
// resume). The tracker itself is reset separately via restoreStats.
func (r *walltimeRecorder) repointLedger(ledger *analytics.UsageLedger) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.ledger = ledger
	r.generation++
	r.mu.Unlock()
}

// startCompactionAt opens a draft-production segment for a compaction plan.
func (r *walltimeRecorder) startCompactionAt(planID uint64, agentName string, turnID uint64) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.compaction[planID] = walltimeSegment{target: r.captureLocked(identity.MainAgentID, agentName, turnID), start: time.Now()}
	r.mu.Unlock()
}

// finishCompaction closes the plan's segment on its first terminal event
// (Ready / Failed, including the synthetic watchdog failure). Late duplicates
// are no-ops, so a real event arriving after the watchdog never double-counts.
func (r *walltimeRecorder) finishCompaction(planID uint64) {
	if r == nil {
		return
	}
	r.mu.Lock()
	segment, ok := r.compaction[planID]
	if ok {
		delete(r.compaction, planID)
	}
	r.mu.Unlock()
	if !ok {
		return
	}
	r.recordTarget(segment.target, analytics.WalltimePurposeCompaction, time.Since(segment.start))
}

// requestWallclock tracks one LLM request's model/cooldown split. Model time
// is the whole request wall clock minus accumulated cooldown intervals, so a
// cooldown sleep is never counted as model time; cooldown intervals are
// opened by the first "cooling" status and closed by any later non-cooling
// event or the request terminal.
type requestWallclock struct {
	recorder      *walltimeRecorder
	target        *walltimeTarget
	started       time.Time
	cooldownStart time.Time
	cooldownTotal time.Duration
}

// startRequest opens a request segment. Nil-safe: a nil recorder yields a
// segment whose methods are all no-ops, so test harnesses without walltime
// wiring keep working.
func (r *walltimeRecorder) startRequestAt(agentID, agentName string, turnID uint64) *requestWallclock {
	if r == nil {
		return nil
	}
	return &requestWallclock{recorder: r, target: r.captureAt(agentID, agentName, turnID), started: time.Now()}
}

// wireStreamReducer feeds the stream reducer's activity events into this open
// request segment. The reducer routes ActivityStreaming through
// promoteStreamingActivity only and never calls emitActivity for that
// transition, so without this hook a cooldown interval opened by a "cooling"
// status would stay open until the request terminal — inflating Cooldown and
// shrinking Model. Wrapping the promotion path closes an open cooldown window
// on the first visible text, thinking, or tool-use delta as well.
func (w *requestWallclock) wireStreamReducer(r *llmStreamReducer, emit func(ActivityType, string)) {
	if w == nil || r == nil {
		return
	}
	r.emitActivity = func(activity ActivityType, detail string) {
		if emit != nil {
			emit(activity, detail)
		}
		w.onActivity(activity)
	}
	promote := r.promoteStreamingActivity
	r.promoteStreamingActivity = func(source string) {
		if promote != nil {
			promote(source)
		}
		w.onActivity(ActivityStreaming)
	}
}

// onActivity feeds activity transitions into the segment. The first
// ActivityCooling opens a cooldown interval; any other activity closes one.
func (w *requestWallclock) onActivity(activity ActivityType) {
	if w == nil || w.started.IsZero() {
		return
	}
	now := time.Now()
	if activity == ActivityCooling {
		if w.cooldownStart.IsZero() {
			w.cooldownStart = now
		}
		return
	}
	if !w.cooldownStart.IsZero() {
		w.cooldownTotal += now.Sub(w.cooldownStart)
		w.cooldownStart = time.Time{}
	}
}

// finish settles the segment: closes any open cooldown interval, then records
// model = total - cooldown and the cooldown total. The terminal close happens
// right after CompleteStream returns (success or error), so requests that
// never produced a delta still settle.
func (w *requestWallclock) finish() {
	if w == nil || w.started.IsZero() || w.recorder == nil {
		return
	}
	now := time.Now()
	if !w.cooldownStart.IsZero() {
		w.cooldownTotal += now.Sub(w.cooldownStart)
		w.cooldownStart = time.Time{}
	}
	total := now.Sub(w.started)
	model := max(total-w.cooldownTotal, 0)
	w.recorder.recordTarget(w.target, analytics.WalltimePurposeModel, model)
	if w.cooldownTotal > 0 {
		w.recorder.recordTarget(w.target, analytics.WalltimePurposeCooldown, w.cooldownTotal)
	}
	w.started = time.Time{}
}
