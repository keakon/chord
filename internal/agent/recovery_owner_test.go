package agent

import (
	"path/filepath"
	"sync"
	"testing"

	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
)

// TestConcurrentPersistenceDuringSessionSwitchIsRaceFree covers the ownership
// protocol under its actual access pattern: tool and sub-agent goroutines
// resolve the recovery manager and write through it while the event loop
// freezes one session and installs the next. Run with -race, an unsynchronized
// a.recovery field fails here; without -race it still pins that a switch racing
// live writers neither panics nor writes through a retired manager.
func TestConcurrentPersistenceDuringSessionSwitchIsRaceFree(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	epoch := a.recoverySessionEpoch()

	const writers = 4
	const rounds = 40
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range rounds {
				select {
				case <-stop:
					return
				default:
				}
				// The three concurrent write paths that used to read the
				// recovery field without a lock.
				a.persistAsyncForEpoch(epoch, "agent-1", message.Message{Role: message.RoleAssistant, Content: "sub write"}, nil)
				a.saveRecoverySnapshot()
				if manager := a.recoveryManager(); manager != nil {
					_ = manager.AppendToolActivity(recovery.ToolActivityRecord{
						CallID:  "call-1",
						AgentID: identity.MainAgentID,
						Tool:    "Write",
						State:   recovery.ToolActivityStateStarted,
					})
				}
			}
		}()
	}

	// Freeze the live session and install a successor, exactly as
	// handleNewSessionCommand does around the runtime state reset.
	oldRecovery := a.recoveryManager()
	if oldRecovery == nil {
		t.Fatal("test agent has no recovery manager")
	}
	newSessionDir := filepath.Join(projectRoot, ".chord", "sessions", "successor")
	a.freezeCurrentSession(oldRecovery)
	a.installSessionTarget(newSessionDir)

	close(stop)
	wg.Wait()

	if a.recoveryManager() == oldRecovery {
		t.Fatal("live manager is still the frozen session's manager")
	}
	if got := a.recoverySessionEpoch(); got == epoch {
		t.Fatalf("session epoch after switch = %d, want it advanced past %d", got, epoch)
	}
}

// TestFrozenSessionEpochStopsResolvingTheSuccessorManager pins the rule that
// keeps one session's history out of another: a worker that captured an earlier
// epoch resolves nil, not the manager of the session that replaced its own.
func TestFrozenSessionEpochStopsResolvingTheSuccessorManager(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	frozenEpoch := a.recoverySessionEpoch()
	if a.recoveryManagerForEpoch(frozenEpoch) == nil {
		t.Fatal("live epoch resolves no manager")
	}

	oldRecovery := a.recoveryManager()
	a.freezeCurrentSession(oldRecovery)
	if got := a.recoveryManagerForEpoch(frozenEpoch); got != nil {
		t.Fatal("frozen epoch resolved a manager while no session is installed")
	}

	a.installSessionTarget(filepath.Join(projectRoot, ".chord", "sessions", "successor"))
	successor := a.recoveryManager()
	if successor == nil {
		t.Fatal("successor session has no recovery manager")
	}
	if got := a.recoveryManagerForEpoch(frozenEpoch); got != nil {
		t.Fatalf("frozen epoch resolved the successor manager (%p), want nil", got)
	}
	if got := a.recoveryManagerForEpoch(a.recoverySessionEpoch()); got != successor {
		t.Fatal("live epoch does not resolve the successor manager")
	}
}

// TestSubAgentFollowsRecoveryManagerReplacedWithinTheSession covers the other
// half of the same ownership rule. Compaction closes the manager and installs a
// fresh one for the same session; a sub-agent that captured the pointer at spawn
// time kept writing into the closed one, and a write after Close is reported as
// ErrClosed — which the health state machine deliberately ignores, so the
// transcript silently stopped growing. Resolving per write follows the swap.
func TestSubAgentFollowsRecoveryManagerReplacedWithinTheSession(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	spawnManager := a.recoveryManager()
	if spawnManager == nil {
		t.Fatal("test agent has no recovery manager")
	}

	sub := &SubAgent{instanceID: "agent-1", parent: a, recovery: spawnManager, sessionEpoch: a.recoverySessionEpoch()}
	if got := sub.recoveryManager(); got != spawnManager {
		t.Fatal("sub-agent does not resolve the live manager before the swap")
	}

	// Same session, new manager: what compaction's transcript rewrite does.
	replacement := recovery.NewRecoveryManager(a.SessionDir())
	t.Cleanup(replacement.Close)
	a.clearRecoveryManagerIf(spawnManager)
	spawnManager.Close()
	a.installRecoveryManager(replacement)

	if got := sub.recoveryManager(); got != replacement {
		t.Fatalf("sub-agent resolved %p after the swap, want the replacement %p", got, replacement)
	}

	sub.persistMessageAsync(message.Message{Role: message.RoleAssistant, Content: "written after the swap"}, "test message", nil)
	a.flushPersist()

	msgs, err := replacement.LoadMessages("agent-1")
	if err != nil {
		t.Fatalf("LoadMessages(agent-1): %v", err)
	}
	if len(msgs) != 1 || msgs[0].Content != "written after the swap" {
		t.Fatalf("replacement manager holds %d sub-agent messages (%v), want the one written after the swap", len(msgs), msgs)
	}
	if health := sub.PersistenceHealth(); health.State != PersistenceHealthy {
		t.Fatalf("sub-agent persistence health = %v, want healthy", health.State)
	}
}

// TestSubAgentWriteAfterSessionSwitchStaysOutOfTheNewSession pins that a
// sub-agent abandoned by a switch cannot append its frozen session's transcript
// to the successor session's files. Its goroutine is only cancelled, not joined,
// so a write can still arrive after the swap.
func TestSubAgentWriteAfterSessionSwitchStaysOutOfTheNewSession(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	sub := &SubAgent{instanceID: "agent-1", parent: a, recovery: a.recoveryManager(), sessionEpoch: a.recoverySessionEpoch()}

	oldRecovery := a.recoveryManager()
	a.freezeCurrentSession(oldRecovery)
	a.installSessionTarget(filepath.Join(projectRoot, ".chord", "sessions", "successor"))
	successor := a.recoveryManager()
	if successor == nil {
		t.Fatal("successor session has no recovery manager")
	}

	sub.persistMessageAsync(message.Message{Role: message.RoleAssistant, Content: "belongs to the frozen session"}, "test message", nil)
	a.flushPersist()

	msgs, err := successor.LoadMessages("agent-1")
	if err != nil {
		t.Fatalf("LoadMessages(agent-1): %v", err)
	}
	if len(msgs) != 0 {
		t.Fatalf("successor session holds %d messages from the frozen session's sub-agent, want 0: %v", len(msgs), msgs)
	}
	// The drop is not a durability fault the sub-agent can act on: the session
	// it was writing for is gone, so it must not be marked degraded either.
	if health := sub.PersistenceHealth(); health.State != PersistenceHealthy {
		t.Fatalf("sub-agent persistence health = %v, want healthy", health.State)
	}
}
