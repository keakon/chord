package agent

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/analytics"
	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
)

// persistEntry is a queued persistence request for ordered JSONL writes.
type persistEntry struct {
	durable        bool
	agentID        string
	msg            message.Message
	recovery       *recovery.RecoveryManager // manager resolved at enqueue time; nil means the write's session is gone
	after          func(error)
	walltimeLedger *analytics.UsageLedger
	walltimeEvent  *analytics.UsageEvent
	barrier        chan struct{}
}

// persistencePump owns ordered async persistence and its drain lifecycle.
// The handler supplies per-entry writes and barriers; MainAgent owns shutdown
// gating and tool-trace timing.
type persistencePump struct {
	ch            chan persistEntry
	done          chan struct{}
	admission     chan struct{}
	stopping      chan struct{}
	stopOnce      sync.Once
	closed        chan struct{}
	loopOnce      sync.Once
	failureMu     sync.Mutex
	failures      []persistFailure
	pauseCloseErr error
}

type persistFailure struct {
	entry persistEntry
	err   error
}

func (p *persistencePump) rememberFailure(entry persistEntry, err error) {
	if p == nil || err == nil {
		return
	}
	p.failureMu.Lock()
	p.failures = append(p.failures, persistFailure{entry, err})
	p.failureMu.Unlock()
}

func (p *persistencePump) strictError(manager *recovery.RecoveryManager) error {
	if p == nil {
		return errPersistenceQueueUnavailable
	}
	p.failureMu.Lock()
	defer p.failureMu.Unlock()
	var errs []error
	for _, failure := range p.failures {
		if failure.entry.recovery == nil || failure.entry.recovery == manager {
			errs = append(errs, failure.err)
		}
	}
	return errors.Join(errs...)
}

func (p *persistencePump) managerFailed(manager *recovery.RecoveryManager) bool {
	p.failureMu.Lock()
	defer p.failureMu.Unlock()
	for _, failure := range p.failures {
		if failure.entry.recovery == manager {
			return true
		}
	}
	return false
}

func (p *persistencePump) clearCheckpointFailures(manager *recovery.RecoveryManager, agentID string) {
	if p == nil {
		return
	}
	p.failureMu.Lock()
	defer p.failureMu.Unlock()
	retained := p.failures[:0]
	for _, failure := range p.failures {
		if failure.entry.recovery != manager || failure.entry.agentID != agentID {
			retained = append(retained, failure)
		}
	}
	p.failures = retained
}

func (p *persistencePump) retryFailures(manager *recovery.RecoveryManager) error {
	p.failureMu.Lock()
	for _, failure := range p.failures {
		if failure.entry.recovery == manager {
			writeErr, ok := errors.AsType[*recovery.PersistWriteError](failure.err)
			if !ok || !writeErr.RetrySafe {
				p.failureMu.Unlock()
				return p.strictError(manager)
			}
		}
	}
	failures := p.failures
	p.failures = nil
	p.failureMu.Unlock()
	blocked := false
	for _, failure := range failures {
		if blocked && failure.entry.recovery == manager {
			p.rememberFailure(failure.entry, failure.err)
			continue
		}
		if failure.entry.recovery != manager || manager == nil {
			p.rememberFailure(failure.entry, failure.err)
			continue
		}
		writeErr, ok := errors.AsType[*recovery.PersistWriteError](failure.err)
		if !ok || !writeErr.RetrySafe {
			p.rememberFailure(failure.entry, failure.err)
			continue
		}
		write := manager.PersistMessage
		if failure.entry.durable {
			write = manager.PersistMessageDurable
		}
		if err := write(failure.entry.agentID, failure.entry.msg); err != nil {
			p.rememberFailure(failure.entry, err)
			blocked = true
		}
	}
	return p.strictError(manager)
}

func newPersistencePump(buffer int) *persistencePump {
	return &persistencePump{
		ch:        make(chan persistEntry, buffer),
		done:      make(chan struct{}),
		admission: make(chan struct{}, 1),
		stopping:  make(chan struct{}),
		closed:    make(chan struct{}),
	}
}

// start launches the drain loop exactly once. handle is invoked for each entry
// in arrival order; done is closed after the closed queue is drained.
func (p *persistencePump) start(handle func(persistEntry)) {
	p.loopOnce.Do(func() {
		go func() {
			defer close(p.done)
			for entry := range p.ch {
				handle(entry)
			}
		}()
	})
}

// closeUntil stops admission and closes the queue after the last admitted
// sender leaves. Closure continues even if the caller's deadline expires;
// the drain loop owns completion of every accepted entry.
func (p *persistencePump) closeUntil(deadline <-chan time.Time) bool {
	if p == nil || p.ch == nil {
		return true
	}
	p.stopOnce.Do(func() {
		close(p.stopping)
		go func() {
			p.admission <- struct{}{}
			defer func() { <-p.admission }()
			close(p.ch)
			close(p.closed)
		}()
	})
	select {
	case <-p.closed:
		return true
	case <-deadline:
		return false
	}
}

// enqueue sends entry, returning false if the pump is unusable (nil channel) or
// stopping fired before the send completed.
func (p *persistencePump) enqueue(entry persistEntry, stopping <-chan struct{}) bool {
	return p.enqueueUntil(entry, stopping, nil)
}

func (p *persistencePump) enqueueUntil(entry persistEntry, stopping <-chan struct{}, deadline <-chan time.Time) bool {
	if p == nil || p.ch == nil {
		return false
	}
	select {
	case p.admission <- struct{}{}:
	case <-p.stopping:
		return false
	case <-stopping:
		return false
	case <-deadline:
		return false
	}
	defer func() { <-p.admission }()
	select {
	case <-p.stopping:
		return false
	default:
	}
	select {
	case p.ch <- entry:
		return true
	case <-p.stopping:
		return false
	case <-stopping:
		return false
	case <-deadline:
		return false
	}
}

// flush waits until all entries queued before this call have been handled.
func (p *persistencePump) flush() bool {
	return p.flushUntil(nil)
}

// flushUntil applies the same ordering barrier but also bounds both admission
// to a full queue and waiting for the barrier handler. A nil deadline channel
// preserves the unbounded session-switch behavior.
func (p *persistencePump) flushUntil(deadline <-chan time.Time) bool {
	if p == nil || p.ch == nil {
		return false
	}
	barrier := make(chan struct{})
	if !p.enqueueUntil(persistEntry{barrier: barrier}, nil, deadline) {
		return false
	}
	if deadline == nil {
		<-barrier
		return true
	}
	select {
	case <-barrier:
		return true
	case <-deadline:
		return false
	}
}

func (a *MainAgent) startPersistLoop() {
	a.persist.start(func(entry persistEntry) {
		if entry.barrier != nil {
			close(entry.barrier)
			return
		}
		if entry.walltimeLedger != nil && entry.walltimeEvent != nil {
			if err := entry.walltimeLedger.AppendEvent(*entry.walltimeEvent); err != nil {
				log.Warnf("failed to append walltime ledger event agent_id=%v purpose=%v error=%v", entry.walltimeEvent.AgentID, entry.walltimeEvent.Purpose, err)
			}
			return
		}
		var persistErr error
		if entry.durable && entry.recovery == nil {
			persistErr = fmt.Errorf("durable message has no recovery manager")
		}
		if entry.recovery != nil && a.persist.managerFailed(entry.recovery) {
			persistErr = &recovery.PersistWriteError{RetrySafe: true, Cause: fmt.Errorf("message append blocked by earlier persistence failure")}
		} else if entry.recovery != nil {
			write := entry.recovery.PersistMessage
			if entry.durable {
				write = entry.recovery.PersistMessageDurable
			}
			if err := write(entry.agentID, entry.msg); err != nil {
				log.Warnf("failed to persist message agent_id=%v error=%v", entry.agentID, err)
				persistErr = err
			}
		}
		strictErr := persistErr
		if entry.recovery == nil && strictErr == nil {
			strictErr = fmt.Errorf("message has no recovery manager")
		}
		a.persist.rememberFailure(entry, strictErr)
		if entry.after != nil {
			entry.after(persistErr)
		}
	})
}

func (a *MainAgent) closePersistLoopUntil(deadline <-chan time.Time) bool {
	return a.persist.closeUntil(deadline)
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

// persistAsyncAfter enqueues a write for the live session. Callers that belong
// to a specific session epoch must use persistAsyncForEpoch instead.
func (a *MainAgent) persistAsyncAfter(agentID string, msg message.Message, after func(error)) bool {
	return a.persistAsyncThrough(a.recoveryManager(), agentID, msg, after)
}

// persistAsyncForEpoch enqueues a write on behalf of a worker that belongs to
// sessionEpoch — a sub-agent, whose goroutine outlives the switch that froze
// its session. Resolving the manager here rather than at spawn time is what
// makes the write follow a compaction that replaced the manager, and the epoch
// check is what keeps a frozen agent's message out of the successor session's
// transcript.
func (a *MainAgent) persistAsyncForEpoch(sessionEpoch uint64, agentID string, msg message.Message, after func(error)) bool {
	return a.persistAsyncThrough(a.recoveryManagerForEpoch(sessionEpoch), agentID, msg, after)
}

func requiresDurableMessage(msg message.Message) bool {
	// Journal acknowledgements must never survive the canonical receipt they
	// retire, including receipts that record an unknown execution outcome.
	return len(msg.Question) > 0 || msg.NativeTools != nil
}

// persistAsyncThrough queues one write against an already-resolved manager. A
// nil manager still queues. Ordinary messages need no session file; durable
// messages fail closed in the writer when there is no recovery manager.
func (a *MainAgent) persistAsyncThrough(manager *recovery.RecoveryManager, agentID string, msg message.Message, after func(error)) bool {
	if a.shuttingDown.Load() {
		a.persist.rememberFailure(persistEntry{recovery: manager}, errPersistenceQueueUnavailable)
		return false
	}
	start := time.Now()
	entry := persistEntry{agentID: agentID, msg: msg, recovery: manager, after: after, durable: requiresDurableMessage(msg)}
	if !a.persist.enqueue(entry, a.stoppingCh) {
		a.persist.rememberFailure(entry, errPersistenceQueueUnavailable)
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
	// A successful write proves the path is healthy again, so the cards whose
	// backing appends failed can be surfaced.
	a.flushPendingOverlayAppends()
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
	if a == nil || a.questions.failed {
		return
	}
	manager := a.recoveryManager()
	if manager == nil || !a.persistenceDegraded() {
		return
	}
	if !a.persistenceHealth.beginRecovery() {
		return
	}
	a.flushPersist()
	if err := manager.RewriteLog(identity.MainAgentID, a.ctxMgr.Snapshot()); err != nil {
		a.notePersistenceFailure(err)
		return
	}
	a.persist.clearCheckpointFailures(manager, identity.MainAgentID)
	a.persistenceHealth.markRecovered()
	log.Infof("main persistence recovered after transcript checkpoint")
	a.emitPersistenceRecovered()
	// The checkpoint rewrote the transcript from ctxmgr, so every deferred
	// card's message is durable again and its card can be surfaced.
	a.flushPendingOverlayAppends()
}

// tryEnqueue reserves admission without blocking the event loop.
func (p *persistencePump) tryEnqueue(entry persistEntry) bool {
	if p == nil || p.ch == nil {
		return false
	}
	select {
	case p.admission <- struct{}{}:
	default:
		return false
	}
	defer func() { <-p.admission }()
	select {
	case <-p.stopping:
		return false
	default:
	}
	select {
	case p.ch <- entry:
		return true
	default:
		return false
	}
}

// flushSessionPausePersistence proves that every admitted transcript and usage
// write completed. An ordinary ordering barrier does not repair failed writes.
func (a *MainAgent) flushSessionPausePersistence() error {
	manager := a.recoveryManager()
	if manager == nil {
		return fmt.Errorf("session pause has no recovery manager")
	}
	if err := a.persist.sessionPauseBarrier(); err != nil {
		return err
	}
	errs := []error{a.persist.strictError(manager)}
	if a.usageLedger != nil {
		a.usageLedger.Seal()
		errs = append(errs, a.usageLedger.Sync())
	}
	return errors.Join(errs...)
}

// retrySessionPausePersistence rewrites only records proven not to have been
// appended. It never reruns a tool or a request, or repeats result callbacks.
func (a *MainAgent) retrySessionPausePersistence() error {
	if err := a.persist.sessionPauseCloseError(); err != nil {
		return err
	}
	manager := a.recoveryManager()
	if manager == nil {
		return fmt.Errorf("session pause has no recovery manager")
	}
	if err := a.persist.sessionPauseBarrier(); err != nil {
		return err
	}
	errs := []error{a.persist.retryFailures(manager), a.retrySessionPauseProducerWrites()}
	if a.usageLedger != nil {
		errs = append(errs, a.usageLedger.RetryWrites())
	}
	return errors.Join(errs...)
}

// SessionPauseWriterClosedError preserves a failed close outcome. Reopening
// writers cannot prove whether the old writers synchronized every record.
type SessionPauseWriterClosedError struct{ Cause error }

func (e *SessionPauseWriterClosedError) Error() string {
	return "session writer closed with an unknown persistence outcome; retry-save cannot safely repair it: " + e.Cause.Error()
}
func (e *SessionPauseWriterClosedError) Unwrap() error { return e.Cause }

func (p *persistencePump) sessionPauseCloseError() error {
	if p == nil {
		return errPersistenceQueueUnavailable
	}
	p.failureMu.Lock()
	defer p.failureMu.Unlock()
	return p.pauseCloseErr
}

func (p *persistencePump) rememberSessionPauseCloseFailure(cause error) error {
	if cause == nil {
		return nil
	}
	p.failureMu.Lock()
	defer p.failureMu.Unlock()
	p.pauseCloseErr = &SessionPauseWriterClosedError{Cause: cause}
	return p.pauseCloseErr
}

// A completed drain is an ordering barrier even after queue admission closed.
// This allows safe record repair without restarting producers or the pump.
func (p *persistencePump) sessionPauseBarrier() error {
	if err := p.sessionPauseCloseError(); err != nil {
		return err
	}
	if p.flush() {
		return nil
	}
	select {
	case <-p.done:
		return nil
	default:
		return errPersistenceQueueUnavailable
	}
}
