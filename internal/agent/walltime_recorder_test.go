package agent

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/keakon/chord/internal/analytics"
	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

// TestWalltimeUserWaitSettlesOnConfirmResolution verifies the user-wait
// accounting chain: a confirm wait that resolves (user approved) settles its
// wall-clock duration into the UserWait bucket of the owning agent via the
// broker's settlement hook, before the tool result event is emitted. The
// owning agent is the one captured at registration (from the tool execution
// context), so a SubAgent confirmation never lands on the main agent.
func TestWalltimeUserWaitSettlesOnConfirmResolution(t *testing.T) {
	ledger := newWalltimeTestLedger(t)
	rec := newWalltimeRecorder(ledger, nil, nil)
	b := newInteractionBroker(nil)
	b.setSettledHook(func(target *walltimeTarget, d time.Duration) {
		rec.recordTarget(target, analytics.WalltimePurposeUserWait, d)
	})

	const agentID = "worker-1"
	requestID := "req-user-wait"
	ch := b.registerConfirm(requestID, rec.captureAt(agentID, "", 0))
	go b.resolveConfirm(requestID, ConfirmResponse{Approved: true})
	resp, err := b.awaitConfirm(context.Background(), ch, 0, "read")
	if err != nil || !resp.Approved {
		t.Fatalf("awaitConfirm = (%#v, %v), want approved response", resp, err)
	}
	// The awaiting caller unregisters once the flow ends (interaction.go defers it).
	b.unregisterConfirm(requestID)

	stats := rec.statsForAgent(agentID)
	if stats.UserWait <= 0 {
		t.Fatalf("user wait for %q = %v, want > 0 after a resolved confirm", agentID, stats.UserWait)
	}
	if stats.Tool != 0 || stats.Model != 0 || stats.Cooldown != 0 {
		t.Fatalf("unexpected walltime for %q: %#v", agentID, stats)
	}
	if main := rec.statsForAgent(identity.MainAgentID); main.UserWait != 0 {
		t.Fatalf("main user wait = %v, want 0 (wait is owned by %q)", main.UserWait, agentID)
	}
}

// TestWalltimeUserWaitSettlesOnConfirmTimeout verifies that a confirm that
// times out still settles its wait into UserWait (the user did take that
// wall-clock time before the auto-deny), via the same defer-unregister path.
func TestWalltimeUserWaitSettlesOnConfirmTimeout(t *testing.T) {
	ledger := newWalltimeTestLedger(t)
	rec := newWalltimeRecorder(ledger, nil, nil)
	b := newInteractionBroker(nil)
	b.setSettledHook(func(target *walltimeTarget, d time.Duration) {
		rec.recordTarget(target, analytics.WalltimePurposeUserWait, d)
	})

	const agentID = "worker-2"
	ch := b.registerConfirm("req-timeout", rec.captureAt(agentID, "", 0))
	resp, err := b.awaitConfirm(context.Background(), ch, 2*time.Millisecond, "read")
	if err != nil || resp.Approved {
		t.Fatalf("awaitConfirm = (%#v, %v), want auto-denied timeout", resp, err)
	}
	b.unregisterConfirm("req-timeout")

	if stats := rec.statsForAgent(agentID); stats.UserWait <= 0 {
		t.Fatalf("user wait for %q = %v, want > 0 after a timed-out confirm", agentID, stats.UserWait)
	}
}

// TestWalltimeUserWaitAttribution verifies confirm/question user-wait
// attribution: MainAgent tool execution contexts carry the runtime instance ID
// ("main-N") while the TIME sidebar reads identity.MainAgentID ("main"), so
// attribution must normalize the instance ID. Bare turn contexts (loop-exit
// Done approval, repeated tool-call interception) and SubAgent contexts keep
// the sidebar-compatible ids.
func TestWalltimeUserWaitAttribution(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)

	// Bare turn context (loop-exit Done approval, repeated-call interception)
	// and the MainAgent tool execution context (which carries the runtime
	// instance ID "main-N") must both attribute to the sidebar key "main".
	if got := a.agentIDForInteraction(context.Background()); got != identity.MainAgentID {
		t.Fatalf("agentIDForInteraction(bare ctx) = %q, want %q", got, identity.MainAgentID)
	}
	if got := a.agentIDForInteraction(tools.WithAgentID(context.Background(), a.instanceID)); got != identity.MainAgentID {
		t.Fatalf("agentIDForInteraction(main instance %q) = %q, want %q", a.instanceID, got, identity.MainAgentID)
	}
	// SubAgent contexts keep their own instance ID so their waits aggregate
	// under the task's instance history.
	if got := a.agentIDForInteraction(tools.WithAgentID(context.Background(), "worker-1")); got != "worker-1" {
		t.Fatalf("agentIDForInteraction(sub ctx) = %q, want worker-1", got)
	}

	// End to end: a confirm registered from a MainAgent tool execution context
	// settles into the main agent's UserWait bucket, never under the runtime
	// instance ID (the orphan the sidebar would otherwise miss).
	if a.walltime == nil {
		t.Fatal("walltime recorder not wired")
	}
	b := newInteractionBroker(nil)
	b.setSettledHook(func(target *walltimeTarget, d time.Duration) {
		a.walltime.recordTarget(target, analytics.WalltimePurposeUserWait, d)
	})
	toolCtx := tools.WithAgentID(context.Background(), a.instanceID)
	ch := b.registerConfirm("req-main", a.walltime.captureAt(a.agentIDForInteraction(toolCtx), a.currentAgentName(), a.currentTurnID()))
	go b.resolveConfirm("req-main", ConfirmResponse{Approved: true})
	if _, err := b.awaitConfirm(context.Background(), ch, 0, "read"); err != nil {
		t.Fatalf("awaitConfirm: %v", err)
	}
	b.unregisterConfirm("req-main")

	if stats := a.walltime.statsForAgent(identity.MainAgentID); stats.UserWait <= 0 {
		t.Fatalf("main user wait = %v, want > 0 for a MainAgent tool confirm", stats.UserWait)
	}
	if stats := a.walltime.statsForAgent(a.instanceID); stats.UserWait != 0 {
		t.Fatalf("instance %q user wait = %v, want 0 (wait must not be orphaned)", a.instanceID, stats.UserWait)
	}
}

// TestWalltimeRecorderAppendsAndRepointsLedger verifies that record() appends
// walltime bookkeeping events to the current ledger and that repointLedger
// switches the append target, so session switch / resume never write TIME
// segments into the wrong session's ledger.
func TestWalltimeRecorderAppendsAndRepointsLedger(t *testing.T) {
	ledgerA := newWalltimeTestLedger(t)
	rec := newWalltimeRecorder(ledgerA, nil, nil)

	rec.recordTarget(rec.captureAt(identity.MainAgentID, "", 0), analytics.WalltimePurposeTool, 1500*time.Millisecond)
	rec.recordTarget(rec.captureAt(identity.MainAgentID, "", 0), analytics.WalltimePurposeUserWait, 250*time.Millisecond)

	_, _, _, walltime, err := ledgerA.BuildSessionEvidence()
	if err != nil {
		t.Fatalf("BuildSessionEvidence: %v", err)
	}
	got := walltime[identity.MainAgentID]
	if got.Tool != 1500*time.Millisecond || got.UserWait != 250*time.Millisecond {
		t.Fatalf("recorded walltime = %#v, want tool 1.5s + user wait 250ms", got)
	}

	// Repoint to a second session's ledger: later segments must land there.
	ledgerB := newWalltimeTestLedger(t)
	rec.repointLedger(ledgerB)
	rec.recordTarget(rec.captureAt(identity.MainAgentID, "", 0), analytics.WalltimePurposeCooldown, 300*time.Millisecond)

	otherStats := walltimeStatsFor(t, ledgerB)
	if otherStats[identity.MainAgentID].Cooldown != 300*time.Millisecond {
		t.Fatalf("repointed ledger cooldown = %v, want 300ms", otherStats[identity.MainAgentID].Cooldown)
	}
}

// TestWalltimeRecorderConcurrentRecordAndRepoint hammers record() concurrently
// with repointLedger(); under -race this detects the unlocked ledger re-read
// (the append path must use the pointer captured under mutex, not re-read the
// field after the lock is dropped). Every recorded segment must land in
// exactly one ledger, so the ledger total must equal the tracker total.
func TestWalltimeRecorderConcurrentRecordAndRepoint(t *testing.T) {
	ledgerA := newWalltimeTestLedger(t)
	ledgerB := newWalltimeTestLedger(t)
	rec := newWalltimeRecorder(ledgerA, nil, nil)

	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					rec.recordTarget(rec.captureAt(identity.MainAgentID, "", 0), analytics.WalltimePurposeTool, time.Millisecond)
				}
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			rec.repointLedger(ledgerB)
			rec.repointLedger(ledgerA)
		}
	}()
	time.Sleep(50 * time.Millisecond)
	close(stop)
	wg.Wait()

	statsA := walltimeStatsFor(t, ledgerA)
	statsB := walltimeStatsFor(t, ledgerB)
	var total time.Duration
	if s := statsA[identity.MainAgentID]; s != nil {
		total += s.Tool
	}
	if s := statsB[identity.MainAgentID]; s != nil {
		total += s.Tool
	}
	tracked := rec.statsForAgent(identity.MainAgentID).Tool
	// The tracker only reflects the current session generation; a segment
	// whose capture raced a repoint lands in its pinned ledger but not in the
	// newer tracker generation. So the tracked amount is a subset of what the
	// ledgers hold — never more, and every recorded duration is preserved in
	// exactly one ledger. Under -race this still guards the unlocked ledger
	// re-read that used to corrupt the append path.
	if total <= 0 {
		t.Fatalf("no recorded segments made it into either ledger")
	}
	if tracked > total {
		t.Fatalf("tracker tool = %v exceeds ledger total %v", tracked, total)
	}
}

// TestLateSettlementWritesPinnedLedger verifies a segment settled after a
// session switch is appended to the ledger it belongs to (the one captured at
// start) and never leaks into the current session's tracker, mirroring what
// happens when an LLM / confirm goroutine finishes after /new or /resume.
func TestLateSettlementWritesPinnedLedger(t *testing.T) {
	ledgerA := newWalltimeTestLedger(t)
	ledgerB := newWalltimeTestLedger(t)
	rec := newWalltimeRecorder(ledgerA, nil, nil)

	// A request starts while session A is active; its settlement will arrive
	// only after the switch to B.
	target := rec.captureAt(identity.MainAgentID, "", 0)

	rec.repointLedger(ledgerB)

	rec.recordTarget(target, analytics.WalltimePurposeModel, 2*time.Second)

	statsA := walltimeStatsFor(t, ledgerA)
	if s := statsA[identity.MainAgentID]; s == nil || s.Model != 2*time.Second {
		t.Fatalf("ledgerA model = %+v, want 2s in the pinned session", s)
	}
	statsB := walltimeStatsFor(t, ledgerB)
	if s := statsB[identity.MainAgentID]; s != nil && s.Model != 0 {
		t.Fatalf("ledgerB model = %v, want 0 (late segment must not land in session B)", s.Model)
	}
	if tracked := rec.statsForAgent(identity.MainAgentID).Model; tracked != 0 {
		t.Fatalf("current tracker model = %v, want 0 (late segment must not pollute session B)", tracked)
	}

	// A fresh segment in session B still accounts normally.
	rec.recordTarget(rec.captureAt(identity.MainAgentID, "", 0), analytics.WalltimePurposeTool, time.Second)
	statsB = walltimeStatsFor(t, ledgerB)
	if s := statsB[identity.MainAgentID]; s == nil || s.Tool != time.Second {
		t.Fatalf("ledgerB tool = %+v, want 1s", s)
	}
	if tracked := rec.statsForAgent(identity.MainAgentID).Tool; tracked != time.Second {
		t.Fatalf("current tracker tool = %v, want 1s", tracked)
	}
}

func newWalltimeTestLedger(t *testing.T) *analytics.UsageLedger {
	t.Helper()
	sessionDir := filepath.Join(t.TempDir(), "session")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	return analytics.NewUsageLedger(sessionDir, "")
}

// walltimeStatsFor reads one ledger's walltime buckets through the same
// single-pass scan production restore uses.
func walltimeStatsFor(t *testing.T, ledger *analytics.UsageLedger) map[string]*analytics.WalltimeStats {
	t.Helper()
	_, _, _, walltime, err := ledger.BuildSessionEvidence()
	if err != nil {
		t.Fatalf("BuildSessionEvidence: %v", err)
	}
	return walltime
}

// TestWalltimeRecorderNsPrecisionRoundTrips verifies ledger persistence is
// lossless: sub-millisecond segments and fractional-millisecond accumulation
// must rebuild to exactly the same duration as the in-memory tracker. Without
// this the TIME buckets on a resumed session drift from what the live process
// accumulated.
func TestWalltimeRecorderNsPrecisionRoundTrips(t *testing.T) {
	ledger := newWalltimeTestLedger(t)
	rec := newWalltimeRecorder(ledger, nil, nil)

	// The sum is deliberately not an integer number of milliseconds.
	segs := []time.Duration{400 * time.Nanosecond, 900 * time.Nanosecond, 1200 * time.Nanosecond, 3003 * time.Nanosecond}
	var want time.Duration
	for _, d := range segs {
		rec.recordTarget(rec.captureAt(identity.MainAgentID, "", 0), analytics.WalltimePurposeTool, d)
		want += d
	}

	stats := walltimeStatsFor(t, ledger)
	got := stats[identity.MainAgentID].Tool
	if got != want {
		t.Fatalf("ledger tool = %v, want %v (lossless round-trip)", got, want)
	}
	if tracked := rec.statsForAgent(identity.MainAgentID).Tool; tracked != want {
		t.Fatalf("tracker tool = %v, want %v", tracked, want)
	}
}

func TestWalltimeRecorderFlushesThroughPersistencePump(t *testing.T) {
	ledger := newWalltimeTestLedger(t)
	pump := newPersistencePump(4)
	pump.start(func(entry persistEntry) {
		if entry.barrier != nil {
			close(entry.barrier)
			return
		}
		if entry.walltimeLedger == nil || entry.walltimeEvent == nil {
			return
		}
		if err := entry.walltimeLedger.AppendEvent(*entry.walltimeEvent); err != nil {
			t.Errorf("AppendEvent: %v", err)
		}
	})
	defer func() {
		pump.close()
		<-pump.done
	}()

	rec := newWalltimeRecorder(ledger, pump, make(chan struct{}))
	rec.recordTarget(rec.captureAt(identity.MainAgentID, "builder", 7), analytics.WalltimePurposeTool, 3*time.Millisecond)
	rec.flush()

	stats := walltimeStatsFor(t, ledger)
	if got := stats[identity.MainAgentID].Tool; got != 3*time.Millisecond {
		t.Fatalf("pumped tool duration = %v, want 3ms", got)
	}
}

// TestRequestWallclockStreamingClosesOpenCooldown pins the cooldown/model
// segmentation contract: an interval opened by a "cooling" activity must close
// when the request resumes with a non-cooling activity, so the cooldown bucket
// never swallows the streaming span that follows it. Without the close the
// whole remaining request would be classified as Cooldown.
func TestRequestWallclockStreamingClosesOpenCooldown(t *testing.T) {
	ledger := newWalltimeTestLedger(t)
	rec := newWalltimeRecorder(ledger, nil, nil)
	req := rec.startRequestAt(identity.MainAgentID, "", 0)

	req.onActivity(ActivityCooling)
	time.Sleep(20 * time.Millisecond)
	req.onActivity(ActivityStreaming)
	time.Sleep(10 * time.Millisecond)
	req.finish()

	stats := rec.statsForAgent(identity.MainAgentID)
	total := stats.Model + stats.Cooldown
	if total <= 0 {
		t.Fatalf("no wall time recorded: %+v", stats)
	}
	if stats.Model <= 0 {
		t.Fatalf("model = %v, want the post-cooldown span to count as model time", stats.Model)
	}
	if stats.Cooldown >= total {
		t.Fatalf("cooldown = %v, want < total %v (the streaming span was misclassified)", stats.Cooldown, total)
	}
}

// TestWireStreamReducerClosesCooldownOnStreamingPromotion exercises the same
// contract through the production wiring: the reducer routes the streaming
// status and tool-use deltas through promoteStreamingActivity only, and the
// wire hook must translate that promotion into a cooldown close on the open
// request segment.
func TestWireStreamReducerClosesCooldownOnStreamingPromotion(t *testing.T) {
	ledger := newWalltimeTestLedger(t)
	rec := newWalltimeRecorder(ledger, nil, nil)
	req := rec.startRequestAt(identity.MainAgentID, "", 0)

	reducer := &llmStreamReducer{}
	var activities []ActivityType
	req.wireStreamReducer(reducer, func(a ActivityType, _ string) { activities = append(activities, a) })

	reducer.Handle(message.StreamDelta{Type: message.StreamDeltaStatus, Status: &message.StatusDelta{Type: string(ActivityCooling)}})
	time.Sleep(20 * time.Millisecond)
	reducer.Handle(message.StreamDelta{Type: message.StreamDeltaStatus, Status: &message.StatusDelta{Type: string(ActivityStreaming)}})
	time.Sleep(10 * time.Millisecond)
	req.finish()

	stats := rec.statsForAgent(identity.MainAgentID)
	total := stats.Model + stats.Cooldown
	if total <= 0 {
		t.Fatalf("no wall time recorded: %+v", stats)
	}
	if stats.Cooldown >= total {
		t.Fatalf("cooldown = %v, want < total %v after streaming promotion", stats.Cooldown, total)
	}
	if stats.Model <= 0 {
		t.Fatalf("model = %v, want the post-promotion span as model time", stats.Model)
	}
	// The streaming transition is forwarded through the promotion hook, not the
	// emit callback: only the cooling status should appear in the emit feed.
	if !slices.Contains(activities, ActivityCooling) || slices.Contains(activities, ActivityStreaming) {
		t.Fatalf("wired emit activities = %v, want cooling only (streaming goes through the promotion hook)", activities)
	}
}
