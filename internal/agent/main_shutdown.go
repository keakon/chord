package agent

import (
	"context"
	"fmt"
	"time"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/hook"
	"github.com/keakon/chord/internal/recovery"
	"github.com/keakon/chord/internal/tools"
)

const sessionEndHookGrace = 300 * time.Millisecond

// Shutdown cancels any in-flight work and waits for the event loop to exit
// (up to the given timeout). The caller should cancel the context passed to
// Run as well.
func (a *MainAgent) Shutdown(timeout time.Duration) error {
	log.Infof("agent shutting down instance=%v timeout=%v", a.instanceID, timeout)
	// Cancel in-flight memory extraction and flush the usage ledger. Shutdown
	// never starts new extraction and never waits on an in-flight one.
	a.shutdownMemoryWorker()
	deadline := time.Now().Add(timeout)
	remaining := func() time.Duration {
		left := time.Until(deadline)
		if left < 0 {
			return 0
		}
		return left
	}

	if grace := remaining(); grace > 0 {
		hookBudget := min(sessionEndHookGrace, grace)
		// Run shutdown hooks under the remaining shutdown budget rather than the
		// already-cancelled run context, so on_session_end can perform best-effort
		// cleanup without hanging process exit.
		hookCtx, cancel := context.WithTimeout(context.Background(), hookBudget)
		if _, err := a.fireHook(hookCtx, hook.OnSessionEnd, 0, map[string]any{}); err != nil {
			log.Warnf("on_session_end hook error error=%v", err)
		}
		cancel()
	}

	// Flush the debounced sub-agent meta/registry writes before the flag below
	// turns the debouncer's flush into a no-op, so the final snapshot keeps the
	// last window of non-terminal worker progress.
	a.flushPendingSubPersists()

	// Mark as shutting down so UpdateTodos stops saving snapshots (the final
	// snapshot is saved below and must not be overwritten).
	a.admissionMu.Lock()
	a.shuttingDown.Store(true)
	// Unblock reliable/interactive TUI sends immediately. Waiting until Run's
	// defer is too late when the event-loop goroutine itself is blocked on a
	// full output channel: it cannot reach that defer until Shutdown releases it.
	a.signalStopping()
	a.admissionEpoch.Add(1)
	a.cancelSubAgentAdmissions()
	a.admissionMu.Unlock()

	// Stop the event loop before taking the final snapshot. signalStopping
	// releases any output-channel backpressure, while cancelling parentCtx
	// makes nextEvent return as soon as the current handler completes.
	a.cancel()
	a.cancelActiveWork()
	a.waitForSubAgents(remaining)

	// Defer SubAgent MCP cleanup across every subsequent shutdown return. The
	// helper waits for Run to finish before closing transports, so timeout paths
	// do not race an event-loop MCP call or leave cleanup behind if Run exits
	// shortly after Shutdown's budget expires.
	defer a.closeSubAgentMCPServersAfterRun()

	// Close the persistence channel and wait for the loop to drain.
	// The persist loop may be started outside Run (tests), so don't gate the wait
	// on the main event loop start flag. Closing the channel is itself an
	// ordering barrier: the drain loop processes every already-enqueued entry
	// (including walltime segments settled during cancellation) in FIFO order
	// before it exits, so no separate pre-close flush is needed.
	persistDrained := true
	if a.persist.ch != nil {
		a.closePersistLoop()
		if wait := remaining(); wait > 0 {
			select {
			case <-a.persist.done:
			case <-time.After(wait):
				persistDrained = false
			}
		} else {
			persistDrained = false
		}
	}
	if !persistDrained {
		return a.shutdownTimeoutError(timeout)
	}

	compactionDrained := true
	if wait := remaining(); wait > 0 {
		done := make(chan struct{})
		go func() {
			a.compactionWg.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(wait):
			compactionDrained = false
		}
	} else {
		compactionDrained = false
	}
	if !compactionDrained {
		return a.shutdownTimeoutError(timeout)
	}

	// The extraction worker was cancelled at the top of Shutdown; confirm it
	// actually returned before the session files are closed below.
	if !a.waitMemoryWorkerStopped(remaining()) {
		return a.shutdownTimeoutError(timeout)
	}

	// If Run was started, wait until its current handler and output producers
	// have fully stopped before reading mutable loop state or closing recovery.
	if a.started.Load() {
		if wait := remaining(); wait > 0 {
			select {
			case <-a.done:
			case <-time.After(wait):
				return a.shutdownTimeoutError(timeout)
			}
		} else {
			return a.shutdownTimeoutError(timeout)
		}
	}

	// The event loop has fully exited: settle any handoff the user never
	// decided so the transcript does not end on an unresolved tool call.
	a.settlePendingHandoffAtShutdown()

	if failed := a.checkpointDegradedSubAgents(); len(failed) > 0 {
		log.Warnf("shutdown leaving SubAgents with degraded persistence agent_ids=%v", failed)
	}

	// Save final snapshot and close recovery manager (flush JSONL file handles).
	if manager := a.recoveryManager(); manager != nil {
		if err := a.persistSnapshotLocked(a.buildShutdownSnapshot); err != nil {
			log.Warnf("failed to save final recovery snapshot error=%v", err)
		}

		// Unpublish before closing so a straggling worker resolves nil instead
		// of a closed manager it would report as a persistence failure.
		a.clearRecoveryManagerIf(manager)
		manager.Close()
	}

	if a.sessionLock != nil {
		if err := a.sessionLock.Release(); err != nil {
			log.Warnf("failed to release session lock on shutdown error=%v", err)
		}
	}

	return nil
}

// shutdownTimeoutError persists a best-effort snapshot before Shutdown aborts
// with workers still live. persistSnapshotLocked serializes the snapshot build
// and write against the concurrent TodoWrite / SubAgent persistence writers,
// and SaveSnapshot writes atomically via temp+rename, so racing a wedged loop
// can only yield a slightly stale snapshot — never a corrupt one. The recovery
// manager stays open: the stuck persist loop may still be writing JSONL, and
// Close would turn those writes into silent no-ops.
func (a *MainAgent) shutdownTimeoutError(timeout time.Duration) error {
	if a.recoveryManager() != nil {
		if err := a.persistSnapshotLocked(a.buildShutdownSnapshot); err != nil {
			log.Warnf("failed to save best-effort recovery snapshot on shutdown timeout error=%v", err)
		}
	}
	return fmt.Errorf("agent shutdown timed out after %v", timeout)
}

func (a *MainAgent) waitForSubAgents(remaining func() time.Duration) {
	subs := a.subs.snapshotSubAgents()
	for _, sub := range subs {
		wait := remaining()
		if wait <= 0 {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), wait)
		if err := sub.waitDone(ctx); err != nil {
			log.Warnf("SubAgent did not stop within shutdown budget agent_id=%v error=%v", sub.instanceID, err)
		}
		cancel()
	}
}

func (a *MainAgent) checkpointDegradedSubAgents() []string {
	if a == nil {
		return nil
	}
	var failed []string
	for _, sub := range a.subs.snapshotSubAgents() {
		if sub == nil || sub.transcriptPersistenceHealthy() {
			continue
		}
		if err := sub.checkpointTranscript(); err != nil {
			failed = append(failed, sub.instanceID)
		}
	}
	return failed
}

// cancelActiveWork aborts the active turn (if any), cancels every live
// SubAgent, and stops orphaned background shell jobs. It is the first phase of [MainAgent.Shutdown] and runs synchronously so tool
// executions and LLM calls observe cancellation before snapshot/persist work
// begins.
func (a *MainAgent) cancelActiveWork() {
	a.turnMu.Lock()
	if a.turn != nil {
		a.turn.Cancel()
	}
	a.turnMu.Unlock()
	a.llmMu.RLock()
	mainClient := a.llmClient
	a.llmMu.RUnlock()
	if mainClient != nil {
		mainClient.Close()
	}

	a.subs.mu.RLock()
	for _, sub := range a.subs.subAgents {
		tools.StopAllJobsForAgent(sub.instanceID, "terminated on client exit")
		sub.cancel()
		sub.closeLLMClient()
	}
	a.subs.mu.RUnlock()

	if stoppedBackground := tools.StopAllJobsForShutdown(); stoppedBackground > 0 {
		log.Infof("terminated background objects for shutdown count=%v instance=%v", stoppedBackground, a.instanceID)
	}
}

// closeSubAgentMCPServers tears down SubAgent-exclusive MCP managers. Sentinel
// entries (Mgr==nil) point at main-agent servers which are owned by AppContext
// and closed elsewhere. Resets the cache so post-shutdown lookups fail
// explicitly.
func (a *MainAgent) closeSubAgentMCPServers() {
	a.mcpServerCacheMu.Lock()
	defer a.mcpServerCacheMu.Unlock()
	for name, entry := range a.mcpServerCache {
		if entry.Mgr != nil {
			log.Infof("closing subagent MCP server server=%v", name)
			entry.Mgr.Close()
		}
	}
	a.mcpServerCache = nil
}

func (a *MainAgent) closeSubAgentMCPServersAfterRun() {
	if !a.started.Load() {
		a.closeSubAgentMCPServers()
		return
	}
	select {
	case <-a.done:
		a.closeSubAgentMCPServers()
	default:
		go func() {
			<-a.done
			a.closeSubAgentMCPServers()
		}()
	}
}

// buildShutdownSnapshot collects todos, sub-agent states, and current usage
// totals into a [recovery.SessionSnapshot] suitable for the final shutdown
// snapshot.
func (a *MainAgent) buildShutdownSnapshot() *recovery.SessionSnapshot {
	return a.buildRecoverySnapshot()
}
