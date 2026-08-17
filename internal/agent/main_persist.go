package agent

import (
	"errors"
	"sync"
	"time"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
)

// persistencePump owns the ordered async-persistence channel and its drain
// goroutine's lifecycle. It was carved out of MainAgent, where the channel, the
// done signal, and the two sync.Once guards were four loose fields. The pump
// only knows how to enqueue and drain entries; the per-entry work (recovery
// writes, barriers) is supplied by the caller via start's handler, and
// MainAgent-specific concerns (shutdown gating, tool-trace timing) stay on
// MainAgent.
type persistencePump struct {
	ch        chan persistEntry
	done      chan struct{}
	mu        sync.RWMutex
	stopped   bool
	closeOnce sync.Once
	loopOnce  sync.Once
}

func newPersistencePump(buffer int) *persistencePump {
	return &persistencePump{
		ch:   make(chan persistEntry, buffer),
		done: make(chan struct{}),
	}
}

// start launches the drain loop exactly once. handle is invoked for each entry
// in arrival order; done is closed after a FIFO stop sentinel is consumed.
func (p *persistencePump) start(handle func(persistEntry)) {
	p.loopOnce.Do(func() {
		go func() {
			defer close(p.done)
			for entry := range p.ch {
				if entry.stop {
					return
				}
				handle(entry)
			}
		}()
	})
}

// close stops new enqueues and sends a FIFO stop sentinel without closing ch,
// avoiding send/close races with producers already inside enqueue.
func (p *persistencePump) close() {
	p.closeOnce.Do(func() {
		p.mu.Lock()
		p.stopped = true
		p.ch <- persistEntry{stop: true}
		p.mu.Unlock()
	})
}

// enqueue sends entry, returning false if the pump is unusable (nil channel) or
// stopping fired before the send completed.
func (p *persistencePump) enqueue(entry persistEntry, stopping <-chan struct{}) bool {
	if p == nil || p.ch == nil {
		return false
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.stopped {
		return false
	}
	select {
	case p.ch <- entry:
		return true
	case <-stopping:
		return false
	}
}

func (a *MainAgent) startPersistLoop() {
	a.persist.start(func(entry persistEntry) {
		if entry.barrier != nil {
			close(entry.barrier)
			return
		}
		var persistErr error
		if entry.recovery != nil {
			if err := entry.recovery.PersistMessage(entry.agentID, entry.msg); err != nil {
				log.Warnf("failed to persist message agent_id=%v error=%v", entry.agentID, err)
				persistErr = err
			}
		}
		if entry.after != nil {
			entry.after(persistErr)
		}
	})
}

func (a *MainAgent) closePersistLoop() {
	a.persist.close()
}

// persistAsync sends a persistence request to the ordered channel.
// Blocks if the channel is full (preferable to silently dropping
// persistence data). No-op once Shutdown has closed the channel.
func (a *MainAgent) persistAsync(agentID string, msg message.Message) {
	var after func(error)
	if agentID == identity.MainAgentID {
		// Every main-transcript write failure folds into the main persistence
		// health state machine, which degrades the session and gates tool
		// dispatch until the write path recovers.
		after = a.notePersistenceFailure
	}
	a.persistAsyncAfter(agentID, msg, after)
}

func (a *MainAgent) persistAsyncAfter(agentID string, msg message.Message, after func(error)) bool {
	if a.shuttingDown.Load() {
		return false
	}
	start := time.Now()
	if !a.persist.enqueue(persistEntry{agentID: agentID, msg: msg, recovery: a.recovery, after: after}, a.stoppingCh) {
		return false
	}
	blocked := time.Since(start)
	if blocked <= 50*time.Millisecond {
		return true
	}
	callID := ""
	if msg.Role == "assistant" && len(msg.ToolCalls) > 0 {
		for _, tc := range msg.ToolCalls {
			if tc.ID == "" {
				continue
			}
			if callID == "" {
				callID = tc.ID
			}
			a.recordToolTracePersistBlock(tc.ID, blocked)
		}
	} else if msg.Role == "tool" && msg.ToolCallID != "" {
		callID = msg.ToolCallID
		a.recordToolTracePersistBlock(callID, blocked)
	}
	log.Warnf("persistAsync enqueue blocked role=%v tool_call_id=%v blocked_ms=%v", msg.Role, callID, blocked.Milliseconds())
	return true
}

// flushPersist blocks until all persistence requests queued before this call
// have been written to disk. It is used before rewriting session files during
// context compaction or session switches.
func (a *MainAgent) flushPersist() {
	a.flushPersistUntil(nil)
}

func (a *MainAgent) flushPersistUntil(beforeWait func()) {
	if a.shuttingDown.Load() {
		return
	}
	barrier := make(chan struct{})
	if !a.persist.enqueue(persistEntry{barrier: barrier}, a.stoppingCh) {
		return
	}
	if beforeWait != nil {
		beforeWait()
	}
	<-barrier
}

// waitPersistBarrier waits for one persistence entry's write result, or fails
// closed when the entry was never enqueued (shutdown / pump stopped) or the
// agent is stopping first. A nil error means the message completed its
// f.Write (process-crash durable), which is the intent-barrier gate.
func (a *MainAgent) waitPersistBarrier(barrier <-chan error, enqueued bool) error {
	return waitPersistBarrierOn(barrier, enqueued, a.stoppingCh)
}

// waitPersistBarrierOn is the shared intent-barrier wait: it fails closed on a
// missing entry or a stopping signal, and records the wait duration for the
// barrier-latency observability metric.
func waitPersistBarrierOn(barrier <-chan error, enqueued bool, stop <-chan struct{}) error {
	if !enqueued {
		return errPersistenceQueueUnavailable
	}
	start := time.Now()
	var err error
	if stop == nil {
		err = <-barrier
	} else {
		select {
		case err = <-barrier:
		case <-stop:
			err = errPersistenceStopping
		}
	}
	d := time.Since(start)
	if d >= intentBarrierSlowThreshold {
		log.Warnf("intent barrier wait slow duration_ms=%d error=%v", d.Milliseconds(), err)
	} else {
		log.Debugf("intent barrier wait duration_us=%d error=%v", d.Microseconds(), err)
	}
	return err
}

const intentBarrierSlowThreshold = 20 * time.Millisecond

// notePersistenceFailure records a main-transcript write failure and degrades
// the session's persistence health. It is invoked from the persistence pump
// after-callbacks and the intent barrier, so it must be cheap and non-blocking
// for the event loop; TUI notification is emitted once per degradation.
func (a *MainAgent) notePersistenceFailure(err error) {
	if a == nil || err == nil {
		return
	}
	// ErrClosed only follows an intentional Close (shutdown or session
	// switch); it never indicates a disk problem, so degrading health over a
	// racing late write would be a false alarm against the next session.
	if errors.Is(err, recovery.ErrClosed) {
		log.Debugf("main persistence write after close ignored error=%v", err)
		return
	}
	if !a.persistenceHealth.markDegraded(err) {
		return
	}
	log.Errorf("main persistence degraded error=%v", err)
	msg := mainPersistenceDegradedMessage(err)
	a.emitToTUI(PersistenceHealthEvent{Degraded: true, Reason: err.Error()})
	a.emitToTUI(ToastEvent{Message: msg, Level: "error", Category: "persistence_health"})
	a.emitToTUI(InfoEvent{Message: msg})
}

func mainPersistenceDegradedMessage(err error) string {
	return "Session persistence is degraded; tool execution is paused to avoid unrecoverable repeated side effects. " +
		"Check disk space or restart the session. (" + err.Error() + ")"
}

// persistenceDegraded reports whether the main agent's persistence health is
// currently degraded. In the degraded state the session remains usable for
// Q&A turns, but tool dispatch is blocked by the intent barrier and automatic
// loop advance is paused.
func (a *MainAgent) persistenceDegraded() bool {
	return a != nil && a.persistenceHealth.snapshot().State == PersistenceDegraded
}

// markPersistenceRecoveredAfterBarrier records recovery proven by a successful
// assistant-message write. A barrier is sufficient because the persistence
// pump processes entries FIFO and reports this entry only after its Write has
// completed. Checkpoint recovery remains responsible for repairing earlier
// entries that may have been lost before the successful write.
func (a *MainAgent) markPersistenceRecoveredAfterBarrier() {
	if a == nil || !a.persistenceDegraded() {
		return
	}
	a.persistenceHealth.markRecovered()
	log.Infof("main persistence recovered after intent barrier")
	a.emitPersistenceRecovered()
}

// emitPersistenceRecovered clears the persistent degraded indicator and posts
// the transient recovery notifications.
func (a *MainAgent) emitPersistenceRecovered() {
	a.emitToTUI(PersistenceHealthEvent{Degraded: false})
	a.emitToTUI(ToastEvent{Message: "Session persistence recovered", Level: "info", Category: "persistence_health"})
	a.emitToTUI(InfoEvent{Message: "Session persistence recovered"})
}

// resetPersistenceHealthForSessionTarget starts the target session with fresh
// health state. Clearing a previous target's warning is not a recovery event,
// so only the durable TUI indicator is reset.
func (a *MainAgent) resetPersistenceHealthForSessionTarget() {
	if a == nil {
		return
	}
	wasUnhealthy := a.persistenceHealth.snapshot().State != PersistenceHealthy
	a.persistenceHealth.reset()
	if wasUnhealthy {
		a.emitToTUI(PersistenceHealthEvent{Degraded: false})
	}
}

// tryRecoverPersistenceBeforeTurn runs once at the start of a new user turn
// when degraded: drain the ordered write queue and checkpoint the full main
// transcript. Success restores healthy; failure stays degraded while the
// Q&A-only turn is still allowed to proceed.
func (a *MainAgent) tryRecoverPersistenceBeforeTurn() {
	if a == nil || a.recovery == nil || !a.persistenceDegraded() {
		return
	}
	if !a.persistenceHealth.beginRecovery() {
		return
	}
	a.flushPersist()
	if err := a.recovery.RewriteLog(identity.MainAgentID, a.ctxMgr.Snapshot()); err != nil {
		a.notePersistenceFailure(err)
		return
	}
	a.persistenceHealth.markRecovered()
	log.Infof("main persistence recovered after transcript checkpoint")
	a.emitPersistenceRecovered()
}
