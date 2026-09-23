package agent

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/memory"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
	"github.com/keakon/chord/internal/sessionview"
)

// memoryExtractionBudget bounds the sanitized transcript sent to the extraction
// model. It is intentionally conservative relative to the model input budget so
// extraction never competes for a huge input slice.
const (
	memoryExtractionTokens    = 24000
	memoryExtractionItemBytes = 32 * 1024
	// memoryExtractionAgentsBytes bounds repository guidance in the extraction
	// input; the last memoryExtractionAgentsTail bytes of it stay reserved for
	// the end of the file, where guidance concentrates commit/review
	// discipline. Oversized guidance keeps both ends and marks the cut.
	memoryExtractionAgentsBytes   = 48 * 1024
	memoryExtractionAgentsTail    = 8 * 1024
	memoryPendingPromotionsView   = 12
	memoryExtractionActiveBytes   = 64 * 1024
	memoryExtractionActiveRecords = 128
	memoryJobRetainBackoff        = 2 * time.Second
	memoryMaxRetryBackoff         = 30 * time.Second
	memoryMaxExtractionAttempts   = 5
	// memoryMaxOutputAttempts bounds how often one job may fail on an unusable
	// output shape (bad JSON, an unknown field, an unknown enum) before memory
	// is declared stalled: the first response plus one resample. A shape failure
	// describes the sample, not the job, so it gets its own small budget instead
	// of consuming the transient-failure attempts.
	memoryMaxOutputAttempts    = 2
	memoryStartupBackfillLimit = 2
	memoryJobQueueLimit        = 64
	// memoryHealthToastRunes bounds the reason carried by the degradation toast
	// so a long model or protocol error cannot wrap the notification.
	memoryHealthToastRunes = 200
)

// memoryJob is one queued extraction attempt. retryAt gates the next attempt
// after a failure; attempts bounds transient retries and outputAttempts bounds
// unusable-output-shape retries, so a persistently failing job stops burning
// model tokens.
//
// A job with review set and no sessionDir audits the whole active index instead
// of one transcript: that is the only view from which memory written by an
// earlier, weaker pass can be judged and removed.
type memoryJob struct {
	sessionDir string
	review     bool
	attempts   int
	// outputAttempts counts responses that were unusable in shape. The retry
	// starts from the next model in the pool, so the same sample is not drawn
	// twice from the model that produced it.
	outputAttempts int
	retryAt        time.Time
}

// memoryInflight tracks one in-flight extraction so a new foreground turn can
// cancel it (foreground preemption). The job is retained so a panic that
// aborts the drain can requeue exactly the work that was interrupted.
type memoryInflight struct {
	sessionDir string
	cancel     context.CancelFunc
	job        memoryJob
}

// initMemory wires the memory manager, resolves the effective auto-extraction
// config (project overrides user), refreshes the session-head reminder, and
// starts the background worker. Called from NewMainAgent before the final
// refreshSystemPrompt so the stable prompt can include the fixed Memory
// discipline when a MEMORY.md exists.
func (a *MainAgent) initMemory(contentRoot string) {
	a.memoryMu.Lock()
	defer a.memoryMu.Unlock()
	if a.memoryMgr != nil {
		return
	}
	m, err := memory.NewManager(contentRoot, a.pathLocator)
	if err != nil {
		a.memoryErr = err
		a.memoryDegraded.Store(true)
		a.memoryDegradedAt.Store(time.Now().UnixNano())
		log.Warnf("memory: init error=%v", err)
		return
	}
	a.memoryMgr = m
	a.memoryWake = make(chan struct{}, 1)
	a.memoryExtractEnabled.Store(a.effectiveMemoryExtractEnabled())
	a.refreshMemoryReminder()
	// Project/process lifetime worker: canceled by parentCtx on shutdown, which
	// also waits for this goroutine to return so nothing touches the project
	// files after Shutdown reports the agent stopped.
	a.memoryWorkerDone = make(chan struct{})
	go func() {
		defer close(a.memoryWorkerDone)
		a.memoryWorkerLoop()
	}()
}

// effectiveMemoryExtractEnabled resolves memory.enabled with project-level
// values overriding user-level ones, matching every other config key. Unset
// everywhere means automatic extraction is off.
func (a *MainAgent) effectiveMemoryExtractEnabled() bool {
	if a.projectConfig != nil && a.projectConfig.Memory.Enabled != nil {
		return *a.projectConfig.Memory.Enabled
	}
	if a.globalConfig != nil && a.globalConfig.Memory.Enabled != nil {
		return *a.globalConfig.Memory.Enabled
	}
	return false
}

// MemoryEnabled reports whether automatic memory extraction is enabled for this
// project (status bar MEMORY pill).
func (a *MainAgent) MemoryEnabled() bool {
	if a == nil {
		return false
	}
	return a.memoryExtractEnabled.Load()
}

// MemoryDegraded reports whether memory extraction is stalled: setup failed, or
// a commit or extraction cannot proceed without external intervention. The
// MEMORY pill reads it so it can warn instead of implying a healthy region.
func (a *MainAgent) MemoryDegraded() bool {
	if a == nil {
		return false
	}
	return a.memoryDegraded.Load()
}

// setMemoryDegraded records background memory health. Only a real flip is
// announced, so a live TUI repaints the MEMORY pill without a redundant event
// on every successful commit. A flip also stamps when the degradation started,
// which is what lets a later drain recognize that another process has moved
// memory on (see recoverMemoryHealthFromCheckpoint).
//
// A degradation also carries the reason as a toast: the pill alone says memory
// stopped, not what to look at, and the cause is usually a single line of the
// last failure. Recovery stays pill-only — the region is working again, and a
// toast would only add noise.
func (a *MainAgent) setMemoryDegraded(degraded bool, reason string) {
	if a.memoryDegraded.Swap(degraded) == degraded {
		return
	}
	if degraded {
		a.memoryDegradedAt.Store(time.Now().UnixNano())
	} else {
		a.memoryDegradedAt.Store(0)
	}
	a.emitToTUI(MemoryHealthEvent{Degraded: degraded})
	if !degraded {
		return
	}
	msg := "Project memory extraction stopped"
	// Collapse to a single line: the reason is a wrapped error, and the toast
	// renders one row.
	if reason = strings.TrimSpace(reason); reason != "" {
		msg += ": " + strings.Join(strings.Fields(reason), " ")
	}
	a.emitToTUI(ToastEvent{
		Message:  truncateString(msg, memoryHealthToastRunes),
		Level:    "warn",
		Category: "memory_health",
	})
}

// refreshMemoryReminderBlock reloads the bounded MEMORY.md summary into the
// cached reminder block and updates the activation state. It is safe to call
// from any goroutine: every touched field is atomic, and the per-request
// reminder is rebuilt from the cached block by ensureSessionBuilt at the next
// request boundary, so a background extraction commit lands on the next
// request. Called at init and after every successful background commit.
func (a *MainAgent) refreshMemoryReminderBlock() {
	if a.memoryMgr == nil {
		return
	}
	summary, active, err := a.memoryMgr.BoundedSummary()
	if err != nil {
		log.Warnf("memory: refresh summary error=%v", err)
		return
	}
	prevActive := a.memoryActive.Load()
	a.memoryActive.Store(active)
	if !active {
		a.cachedMemoryReminder.Store(nil)
	} else {
		block := renderMemoryReminder(summary)
		if block == "" {
			a.memoryActive.Store(false)
			a.cachedMemoryReminder.Store(nil)
		} else {
			a.cachedMemoryReminder.Store(&block)
		}
	}
	if a.memoryActive.Load() != prevActive {
		// Load activation flip changes the stable-prompt Memory discipline
		// block; mark the surface dirty so the next request reinstalls it.
		a.markRuntimeSurfaceDirty()
	}
	// Invalidate the per-request reminder regardless of activation: a content
	// change without a flip (e.g. a new record indexed) must still land on the
	// next request. ensureSessionBuilt rebuilds the reminder when the version
	// moves.
	a.memoryReminderVersion.Add(1)
}

// refreshMemoryReminder refreshes the cached memory block and immediately
// rebuilds the per-request session reminder so the current session sees the
// update. Event-loop callers only (init, tests): the background worker calls
// refreshMemoryReminderBlock and lets the next request boundary rebuild.
func (a *MainAgent) refreshMemoryReminder() {
	a.refreshMemoryReminderBlock()
	a.refreshSessionContextReminder()
}

// memoryReminderBlock returns the cached untrusted Memory block (nil when
// inactive).
func (a *MainAgent) memoryReminderBlock() *string {
	return a.cachedMemoryReminder.Load()
}

// memoryIsActive reports whether a bounded Memory summary is injected into the
// session context (which also enables the fixed load discipline in the stable
// prompt).
func (a *MainAgent) memoryIsActive() bool {
	return a.memoryActive.Load()
}

// scheduleMemoryExtraction queues extraction for a frozen session directory. It
// is called only after freezeCurrentSession has flushed the old persistence and
// closed the old recovery. A session already queued (not yet attempted) is not
// enqueued twice.
func (a *MainAgent) scheduleMemoryExtraction(sessionDir string) {
	if sessionDir == "" || a.memoryMgr == nil || a.memoryErr != nil {
		return
	}
	a.memoryMu.Lock()
	if len(a.memoryPending) < memoryJobQueueLimit && !memoryQueueHasSession(a.memoryPending, sessionDir) {
		a.memoryPending = append(a.memoryPending, memoryJob{sessionDir: sessionDir})
	}
	a.memoryMu.Unlock()
	a.signalMemoryWake()
}

func memoryQueueHasSession(queue []memoryJob, sessionDir string) bool {
	for _, job := range queue {
		if job.sessionDir == sessionDir {
			return true
		}
	}
	return false
}

// scheduleMemoryIndexReview queues a whole-index audit. It is triggered when the
// active index has reached its soft limit, which is where consolidation stops
// being optional: past that point the reminder budget truncates the tail, so
// anything not consolidated is simply never injected.
//
// Only one review is queued at a time. The commit's own checkpoint keeps a
// review whose index state has not changed from running again, so a pass that
// decides nothing needs to go does not re-run on every later extraction.
func (a *MainAgent) scheduleMemoryIndexReview() {
	if a.memoryMgr == nil || a.memoryErr != nil {
		return
	}
	a.memoryMu.Lock()
	queued := false
	for _, job := range a.memoryPending {
		if job.review {
			queued = true
			break
		}
	}
	if !queued && len(a.memoryPending) < memoryJobQueueLimit {
		a.memoryPending = append(a.memoryPending, memoryJob{review: true})
	}
	a.memoryMu.Unlock()
	a.signalMemoryWake()
}

// signalMemoryWake pokes the worker without blocking (coalescing).
func (a *MainAgent) signalMemoryWake() {
	select {
	case a.memoryWake <- struct{}{}:
	default:
	}
}

// memoryWorkerLoop dispatches pending extraction jobs when the foreground is
// idle and auto-extraction is enabled. Errors that do not advance the commit
// retain the job for a later trigger; a foreground cancel requeues the job
// (the fingerprint stays uncovered) and waits for the next wake so it is
// retried later. Shutdown never waits here.
func (a *MainAgent) memoryWorkerLoop() {
	defer func() {
		if r := recover(); r != nil {
			log.Errorf("memory worker panic error=%v stack=%v", r, string(debug.Stack()))
		}
	}()
	for {
		select {
		case <-a.parentCtx.Done():
			return
		case <-a.memoryWake:
		}
		a.drainMemoryQueueSafely()
	}
}

// drainMemoryQueueSafely runs one drain pass under its own recover so a panic
// in a single extraction cannot stop the worker permanently. The interrupted
// job is requeued with a backoff (see recoverMemoryDrainPanic) so the same
// work is retried without a hot loop.
func (a *MainAgent) drainMemoryQueueSafely() {
	defer a.recoverMemoryDrainPanic()
	a.drainMemoryQueue()
}

// recoverMemoryDrainPanic turns a panic inside one drain pass into a bounded
// retry. A panic aborts the pass before it can clear memoryInflight, so the
// interrupted job is still recorded there: release it, charge one attempt, and
// put it back at the head of the queue with a backoff, scheduling a wake for
// when the backoff expires. Once the attempt cap is reached the job is dropped
// and logged so a deterministic panic cannot hot-loop on model tokens.
func (a *MainAgent) recoverMemoryDrainPanic() {
	r := recover()
	if r == nil {
		return
	}
	log.Errorf("memory worker panic error=%v stack=%v", r, string(debug.Stack()))
	a.memoryMu.Lock()
	inflight := a.memoryInflight
	a.memoryInflight = nil
	a.memoryMu.Unlock()
	if inflight == nil {
		return
	}
	if inflight.cancel != nil {
		inflight.cancel()
	}
	job := inflight.job
	job.attempts++
	if job.attempts > memoryMaxExtractionAttempts {
		log.Warnf("memory: dropping job after repeated panics session=%v attempts=%d", memoryJobSessionID(job), job.attempts)
		return
	}
	delay := memoryRetryBackoff(job.attempts)
	job.retryAt = time.Now().Add(delay)
	a.memoryMu.Lock()
	a.memoryPending = append([]memoryJob{job}, a.memoryPending...)
	a.memoryMu.Unlock()
	time.AfterFunc(delay, a.signalMemoryWake)
}

// memoryWakeIdle pokes the worker when the foreground becomes idle, so jobs
// that were deferred while a turn was active are retried promptly.
func (a *MainAgent) memoryWakeIdle() {
	if a.memoryExtractEnabled.Load() {
		a.signalMemoryWake()
	}
}

// drainMemoryQueue pops admissible pending jobs one at a time.
func (a *MainAgent) drainMemoryQueue() {
	for {
		a.memoryMu.Lock()
		if len(a.memoryPending) == 0 {
			a.memoryMu.Unlock()
			// Nothing of our own is left to run, which is the only moment the
			// worker reads the checkpoint for health without competing with its
			// own jobs.
			a.recoverMemoryHealthFromCheckpoint()
			return
		}
		if a.memoryInflight != nil {
			a.memoryMu.Unlock()
			return
		}
		job := a.memoryPending[0]
		if job.retryAt.After(time.Now()) {
			// Backoff window not reached yet; the next wake (idle transition)
			// re-checks. Never spin here.
			a.memoryMu.Unlock()
			return
		}
		a.memoryPending = a.memoryPending[1:]
		a.memoryMu.Unlock()

		if !a.memoryAdmission() {
			// Not admissible: return the job to the head and wait for a later
			// wake (a new turn finishing, startup backfill, config reload).
			a.memoryMu.Lock()
			a.memoryPending = append([]memoryJob{job}, a.memoryPending...)
			a.memoryMu.Unlock()
			return
		}

		ctx, cancel := context.WithCancel(a.parentCtx)
		a.memoryMu.Lock()
		a.memoryInflight = &memoryInflight{sessionDir: job.sessionDir, cancel: cancel, job: job}
		a.memoryMu.Unlock()
		var err error
		if job.review {
			err = a.runMemoryIndexReview(ctx, job.outputAttempts)
		} else {
			err = a.runMemoryExtraction(ctx, job.sessionDir, job.outputAttempts)
		}
		// Read the cancellation state before releasing the context: cancel()
		// sets ctx.Err() itself, so checking it afterwards would classify every
		// outcome — success included — as a foreground preemption.
		preempted := ctx.Err() != nil
		cancel()
		a.memoryMu.Lock()
		a.memoryInflight = nil
		a.memoryMu.Unlock()

		switch {
		case preempted:
			// Cancelled by a new foreground turn (or shutdown): keep the job
			// for a later idle pass instead of losing it; the fingerprint
			// stays uncovered. Attempts are not consumed by a foreground
			// preemption.
			a.memoryMu.Lock()
			a.memoryPending = append([]memoryJob{job}, a.memoryPending...)
			a.memoryMu.Unlock()
			return
		case err != nil:
			sessionID := memoryJobSessionID(job)
			log.Warnf("memory: extraction failed session=%v attempts=%d/%d output_attempts=%d/%d error=%v",
				sessionID, job.attempts, memoryMaxExtractionAttempts, job.outputAttempts, memoryMaxOutputAttempts, err)
			if m := a.memoryMgr; m != nil {
				memory.SaveFailure(m.Layout(), sessionID, err)
			}
			switch chargeMemoryFailure(&job, err) {
			case memoryFailureStalled:
				// Broken managed markers, unusable setup, or an output shape
				// that survived the resample: stop retrying and say why once.
				// The fingerprint stays uncovered, so a later startup backfill
				// retries after the cause is fixed.
				log.Warnf("memory: extraction stalled session=%v attempts=%d output_attempts=%d", sessionID, job.attempts, job.outputAttempts)
				a.setMemoryDegraded(true, err.Error())
				continue
			case memoryFailureDrop:
				// Transient retries used up: stop this job so the worker never
				// hot-loops or burns model tokens, but leave memory health
				// alone — another session or a later backfill can still
				// succeed. The fingerprint stays uncovered.
				log.Warnf("memory: dropping job after %d failures session=%v", job.attempts, sessionID)
				continue
			}
			job.retryAt = time.Now().Add(memoryRetryBackoff(job.attempts + job.outputAttempts))
			a.memoryMu.Lock()
			a.memoryPending = append(a.memoryPending, job)
			a.memoryMu.Unlock()
			// Wait out the backoff without spinning, while staying cancellable
			// on shutdown.
			a.memorySleepUntil(job.retryAt)
		default:
			// Committed: reload the bounded summary into the cached reminder
			// block. The per-request reminder is rebuilt by ensureSessionBuilt
			// at the next request boundary (this goroutine must not touch it).
			a.setMemoryDegraded(false, "")
			a.refreshMemoryReminderBlock()
		}
	}
}

// recoverMemoryHealthFromCheckpoint clears a stall that another process already
// resolved. memoryDegraded only tracks this process's own commits, but
// extraction is shared project state: a second window (or a later startup
// backfill) can cover the session that failed here, so the pill would otherwise
// stay red until this process commits or restarts. A checkpoint entry extracted
// after the local stall means memory moved on and the indicator follows the
// project rather than the process.
//
// Called on the worker goroutine only, and only once the local queue has
// drained. A read failure just logs: the next idle drain retries, and a stale
// red pill is safer than a green one that claims memory is healthy.
func (a *MainAgent) recoverMemoryHealthFromCheckpoint() {
	if a == nil || a.memoryMgr == nil || !a.memoryDegraded.Load() {
		return
	}
	stalledAt := a.memoryDegradedAt.Load()
	if stalledAt == 0 {
		return
	}
	cp, err := memory.LoadCheckpoint(a.memoryMgr.Layout())
	if err != nil {
		log.Warnf("memory: health recovery checkpoint read failed: %v", err)
		return
	}
	newest := cp.NewestExtractionAt()
	if !newest.After(time.Unix(0, stalledAt)) {
		return
	}
	log.Infof("memory: health recovered from checkpoint: extraction advanced at %v after the local stall", newest.UTC().Format(time.RFC3339))
	a.setMemoryDegraded(false, "")
}

// memoryRetryBackoff grows exponentially from memoryJobRetainBackoff up to
// memoryMaxRetryBackoff.
func memoryRetryBackoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	shift := min(attempt-1, 4)
	d := memoryJobRetainBackoff * time.Duration(1<<shift)
	if d > memoryMaxRetryBackoff {
		return memoryMaxRetryBackoff
	}
	return d
}

// memoryJobSessionID returns the stable identifier a failed extraction is
// recorded under. A session job keeps its session dir basename; an index review
// (which has no session) is recorded under the constant synthetic key used for
// its commits, so the failure surfaces against the review rather than a
// meaningless "." (filepath.Base of an empty string).
func memoryJobSessionID(job memoryJob) string {
	if job.review {
		return memory.ReviewSessionID
	}
	return filepath.Base(job.sessionDir)
}

// memorySleepUntil blocks until retryAt or shutdown, whichever comes first.
func (a *MainAgent) memorySleepUntil(retryAt time.Time) {
	delay := time.Until(retryAt)
	if delay <= 0 {
		return
	}
	select {
	case <-a.parentCtx.Done():
	case <-time.After(delay):
	}
}

// memoryPermanentFailure reports whether an extraction failure cannot succeed
// without external intervention (config or file changes). Permanent jobs are
// dropped and stall memory; their fingerprint stays uncovered for a later
// startup backfill.
func memoryPermanentFailure(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, memory.ErrManagedMarkers) ||
		errors.Is(err, errMemorySetupFailed)
}

// memoryFailureOutcome is how a failed extraction job proceeds.
type memoryFailureOutcome int

const (
	// memoryFailureRetry requeues the job after a backoff.
	memoryFailureRetry memoryFailureOutcome = iota
	// memoryFailureDrop stops the job without touching memory health: the
	// transient retry budget is used up, and another session or a later
	// backfill can still succeed.
	memoryFailureDrop
	// memoryFailureStalled stops the job and marks memory degraded: the failure
	// cannot succeed without external intervention.
	memoryFailureStalled
)

// chargeMemoryFailure books one failure against the job's budget and reports how
// the job proceeds.
//
// Unusable output shape (bad JSON, an unknown field, an unknown enum) is
// charged to outputAttempts, not to attempts: it is one model's sampling
// accident, so the retry starts from the next pool model and the job only
// stalls once that resample fails too. Transient failures keep the exponential
// backoff budget; permanent failures stall immediately.
func chargeMemoryFailure(job *memoryJob, err error) memoryFailureOutcome {
	switch {
	case memoryPermanentFailure(err):
		return memoryFailureStalled
	case errors.Is(err, memory.ErrInvalidExtraction):
		if job.outputAttempts+1 >= memoryMaxOutputAttempts {
			return memoryFailureStalled
		}
		job.outputAttempts++
		return memoryFailureRetry
	default:
		if job.attempts+1 > memoryMaxExtractionAttempts {
			return memoryFailureDrop
		}
		job.attempts++
		return memoryFailureRetry
	}
}

// errMemorySetupFailed marks the extraction failures that come from the setup
// around the model call — no usable pool, unreadable transcript, unreadable
// active memory, unresolvable sessions dir. None of them can succeed on a retry
// without external intervention. It is a sentinel rather than a message match
// so rewording any of the wrapping errors cannot silently turn a permanent
// failure back into a retried one.
var errMemorySetupFailed = errors.New("memory extraction setup failed")

// memoryAdmission reports whether a background extraction may dispatch: enabled,
// no shutdown, foreground idle, and no compaction running. Rate/quota are
// handled by the governor at request time; a new turn cancels the in-flight
// request and requeues its job.
func (a *MainAgent) memoryAdmission() bool {
	if a.memoryMgr == nil || !a.memoryExtractEnabled.Load() {
		return false
	}
	if a.shuttingDown.Load() {
		return false
	}
	if a.currentTurn() != nil {
		return false
	}
	if a.IsCompactionRunning() {
		return false
	}
	return true
}

// cancelInFlightMemoryExtraction cancels the current extraction request so a
// new foreground turn is never starved. The job is left for a later trigger.
// Called from newTurn.
func (a *MainAgent) cancelInFlightMemoryExtraction() {
	a.memoryMu.Lock()
	inflight := a.memoryInflight
	a.memoryMu.Unlock()
	if inflight != nil && inflight.cancel != nil {
		inflight.cancel()
	}
}

// shutdownMemoryWorker cancels the in-flight extraction. It does not wait for
// the worker or any LLM request; shutdown never blocks on extraction.
func (a *MainAgent) shutdownMemoryWorker() {
	a.cancelInFlightMemoryExtraction()
}

// waitMemoryWorkerStopped waits, within the caller's remaining shutdown budget,
// for the extraction worker goroutine to return. Cancellation is what unblocks
// it — the LLM request, the retry backoff, and the idle wait all select on the
// same context — so this normally returns at once. It matters because the
// worker resolves project paths through the process-wide path locator: a
// goroutine still running after Shutdown reported the agent stopped can create
// or write files under whatever project the locator resolves to next.
func (a *MainAgent) waitMemoryWorkerStopped(wait time.Duration) bool {
	if a.memoryWorkerDone == nil {
		return true
	}
	if wait <= 0 {
		select {
		case <-a.memoryWorkerDone:
			return true
		default:
			return false
		}
	}
	select {
	case <-a.memoryWorkerDone:
		return true
	case <-time.After(wait):
		return false
	}
}

// runMemoryExtraction projects, sanitizes, bounds, fingerprints, extracts via
// the shared model pool (full round fallback semantics), and commits
// candidates. It advances no checkpoint on any failure. rotation picks the pool
// model the retry of an unusable output shape starts from.
func (a *MainAgent) runMemoryExtraction(ctx context.Context, sessionDir string, rotation int) error {
	if a.memoryMgr == nil {
		return fmt.Errorf("memory is not initialized")
	}
	sessionsDir, err := a.projectSessionsDir()
	if err != nil {
		return fmt.Errorf("%w: resolve sessions dir: %w", errMemorySetupFailed, err)
	}
	loader := recovery.NewReadOnlyTranscriptLoader(sessionsDir)
	msgs, err := loader.LoadDir(sessionDir)
	if err != nil {
		return fmt.Errorf("%w: load frozen transcript: %w", errMemorySetupFailed, err)
	}

	projected := projectForMemory(msgs)
	projected = sanitizeProjected(projected)
	kept, _ := sessionview.Retain(projected, memoryExtractionTokens, memoryExtractionItemBytes)
	sessionID := filepath.Base(sessionDir)
	fingerprint := sessionview.Fingerprint(kept)
	// Skip sessions whose current fingerprint is already covered: the model
	// call is the expensive part, and the commit would no-op anyway.
	if a.memoryCheckpointCovers(sessionID, fingerprint) {
		return nil
	}
	if len(kept) == 0 {
		// Nothing projectable: a legal no-op that still advances the
		// checkpoint so we do not re-scan an empty/irrelevant session forever.
		res, err := a.memoryMgr.CommitExtractionCtx(ctx, sessionID, fingerprint, 0, 0, nil)
		logMemoryCommitWarnings(sessionID, res)
		return err
	}

	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-a.agentsMDReady:
	case <-ctx.Done():
		return ctx.Err()
	}
	active, warnings, err := a.memoryMgr.ActiveSnapshot()
	if err != nil {
		return fmt.Errorf("%w: load active memory for extraction: %w", errMemorySetupFailed, err)
	}
	if len(warnings) > 0 {
		log.Warnf("memory: active snapshot warnings=%v", warnings)
	}
	agentsMD := a.boundedAgentsMDSnapshot()
	prompt := buildMemoryExtractionPrompt(kept, agentsMD, active, a.pendingPromotionView())
	out, err := a.callMemoryExtraction(ctx, prompt, memory.MaxRetirePerSessionRun, rotation)
	if err != nil {
		return err
	}
	if len(out.Dropped) > 0 {
		log.Warnf("memory: dropped %d item(s) from extraction session=%v reasons=%v", len(out.Dropped), sessionID, out.Dropped)
	}
	generation := a.memoryCompactionGeneration(sessionDir)
	res, err := a.memoryMgr.CommitExtractionCtx(ctx, sessionID, fingerprint, len(kept), generation, out)
	logMemoryCommitWarnings(sessionID, res)
	logMemoryCommitGovernance(sessionID, res)
	if err == nil {
		a.maybeScheduleMemoryIndexReview(res)
	}
	return err
}

// agentsMDTruncationMarker marks the cut inside a truncated repository
// guidance snapshot, so the extraction model knows rules near the end of the
// file may be missing from the cut rather than absent from the repository.
const agentsMDTruncationMarker = "\n\n[... repository instructions truncated in the middle to fit the extraction budget; rules near the end may be missing ...]\n\n"

// boundedAgentsMDSnapshot returns the cached AGENTS.md snapshot bounded to the
// extraction budget. Oversized guidance keeps its head and its tail: guidance
// files concentrate commit/review/push discipline near their end, and a blind
// head-only cut hides exactly the rules the model needs when judging whether a
// conclusion is already expressed.
func (a *MainAgent) boundedAgentsMDSnapshot() string {
	return boundedAgentsSnapshot(a.cachedAgentsMDSnapshot(), memoryExtractionAgentsBytes, memoryExtractionAgentsTail)
}

// boundedAgentsSnapshot keeps the first budget-tailReserve bytes and the last
// tailReserve bytes of md, joined by the truncation marker, without splitting a
// UTF-8 rune at either cut. When the budget cannot hold a separate tail
// reserve, it degrades to a rune-safe head-only cut.
func boundedAgentsSnapshot(md string, budget, tailReserve int) string {
	if len(md) <= budget {
		return md
	}
	if tailReserve <= 0 || tailReserve >= budget {
		md = md[:budget]
		for len(md) > 0 && !utf8.ValidString(md) {
			md = md[:len(md)-1]
		}
		return md
	}
	head := md[:budget-tailReserve]
	for len(head) > 0 && !utf8.ValidString(head) {
		head = head[:len(head)-1]
	}
	tailStart := len(md) - tailReserve
	for tailStart < len(md) {
		r, size := utf8.DecodeRuneInString(md[tailStart:])
		if r != utf8.RuneError || size > 1 {
			break
		}
		tailStart++
	}
	return head + agentsMDTruncationMarker + md[tailStart:]
}

// pendingPromotionView returns the titles of pending human-review suggestions
// for the extraction prompt, newest first. A read failure degrades to an empty
// view: the pass still runs, it just cannot avoid re-suggesting a conclusion
// that is already pending for a human.
func (a *MainAgent) pendingPromotionView() []string {
	pending, err := a.memoryMgr.PendingPromotionSummaries(memoryPendingPromotionsView)
	if err != nil {
		log.Warnf("memory: pending promotion view unavailable: %v", err)
		return nil
	}
	return pending
}

// maybeScheduleMemoryIndexReview queues an index audit once the active index
// has reached its soft limit. Checked after a commit because that is the only
// place the index changes size; commits that could not have changed the index
// report -1 and are skipped, since only an entry-writing commit can cross the
// threshold.
func (a *MainAgent) maybeScheduleMemoryIndexReview(res *memory.CommitResult) {
	if res == nil || res.ActiveEntries < memory.ActiveIndexSoftLimit {
		return
	}
	a.scheduleMemoryIndexReview()
}

// runMemoryIndexReview audits the whole active index instead of one transcript.
//
// A session-scoped pass only sees the memory adjacent to the session it came
// from, so index-wide problems — near-duplicates across subsystems, entries that
// never belonged, material that outgrew memory and belongs in project docs —
// are invisible to it. This pass sees only the index and the repository
// instructions, so it judges the collection on its own terms.
//
// It commits under a synthetic session key whose fingerprint is the index state,
// so an unchanged index is already covered and the review does not repeat.
// rotation picks the pool model the retry of an unusable output shape starts
// from.
func (a *MainAgent) runMemoryIndexReview(ctx context.Context, rotation int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-a.agentsMDReady:
	case <-ctx.Done():
		return ctx.Err()
	}
	active, warnings, err := a.memoryMgr.ActiveSnapshot()
	if err != nil {
		return fmt.Errorf("%w: load active memory for review: %w", errMemorySetupFailed, err)
	}
	if len(warnings) > 0 {
		log.Warnf("memory: active snapshot warnings=%v", warnings)
	}
	if len(active.Entries) == 0 {
		return nil
	}
	fingerprint := active.IndexFingerprint()
	if a.memoryCheckpointCovers(memory.ReviewSessionID, fingerprint) {
		// This exact index was already reviewed; re-running would spend tokens to
		// reach the same conclusion.
		return nil
	}
	agentsMD := a.boundedAgentsMDSnapshot()
	prompt := buildMemoryIndexReviewPrompt(agentsMD, active, a.pendingPromotionView())
	out, err := a.callMemoryExtraction(ctx, prompt, memory.MaxRetirePerReviewRun, rotation)
	if err != nil {
		return err
	}
	if len(out.Dropped) > 0 {
		log.Warnf("memory: dropped %d item(s) from index review reasons=%v", len(out.Dropped), out.Dropped)
	}
	res, err := a.memoryMgr.CommitExtractionCtx(ctx, memory.ReviewSessionID, fingerprint, len(active.Entries), 0, out)
	logMemoryCommitWarnings(memory.ReviewSessionID, res)
	logMemoryCommitGovernance(memory.ReviewSessionID, res)
	return err
}

// logMemoryCommitGovernance reports index shrinkage and pending promotions.
// Retirements and promotions remove entries from the injected index, so they
// must be visible in the log even though they are not failures.
func logMemoryCommitGovernance(sessionID string, res *memory.CommitResult) {
	if res == nil {
		return
	}
	if len(res.Retired) > 0 {
		log.Infof("memory: retired %d record(s) from the active index session=%v ids=%v", len(res.Retired), sessionID, res.Retired)
	}
	if res.Promoted > 0 {
		log.Infof("memory: wrote %d promotion suggestion(s) to %v for review session=%v", res.Promoted, memory.ProjectPromotionsDir, sessionID)
	}
}

// logMemoryCommitWarnings surfaces machine-state problems the commit recovered
// from on its own (e.g. a rebuilt checkpoint), which are not failures but must
// not stay invisible when they repeat.
func logMemoryCommitWarnings(sessionID string, res *memory.CommitResult) {
	if res == nil || len(res.Warnings) == 0 {
		return
	}
	log.Warnf("memory: commit warnings session=%v warnings=%v", sessionID, res.Warnings)
}

// projectForMemory projects a transcript into the shared detached surface.
func projectForMemory(msgs []message.Message) []sessionview.Projected {
	var projected []sessionview.Projected
	for _, msg := range msgs {
		if p, ok := sessionview.Project(msg); ok {
			projected = append(projected, p)
		}
		if p, ok := sessionview.SummaryProject(msg); ok {
			projected = append(projected, p)
		}
	}
	return projected
}

// sanitizeProjected redacts secret shapes per item and drops items that stay
// high-risk. The returned slice is a detached surface; the source is never
// mutated.
func sanitizeProjected(projected []sessionview.Projected) []sessionview.Projected {
	out := make([]sessionview.Projected, 0, len(projected))
	for _, p := range projected {
		if memory.HighRisk(p.Text) {
			continue
		}
		p.Text = memory.SanitizeText(p.Text)
		out = append(out, p)
	}
	return out
}

// callMemoryExtraction runs the structured extraction request under the
// governor and parses the response. Malformed or unknown output is a failure
// (does not advance the checkpoint); per-item drops are returned inside the
// output and never block the surviving items from being committed. maxRetire
// bounds how many active records this run may retire, and rotation picks the
// pool model the request starts from.
func (a *MainAgent) callMemoryExtraction(ctx context.Context, prompt string, maxRetire int, rotation int) (*memory.ExtractionOutput, error) {
	client := a.newMemoryExtractionClient(rotation)
	if client == nil {
		return nil, fmt.Errorf("%w: no model pool available for memory extraction", errMemorySetupFailed)
	}
	client.SetSystemPrompt(memoryExtractionSystemPrompt)
	reqCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	release, err := a.governor.acquireLLM(reqCtx, client.PrimaryModelRef())
	if err != nil {
		return nil, fmt.Errorf("acquire memory extraction LLM capacity: %w", err)
	}
	defer release()

	resp, err := client.CompleteStream(reqCtx, []message.Message{{Role: "user", Content: prompt}}, nil, nil)
	if err != nil {
		return nil, err
	}
	return memory.ParseExtractionOutput(extractionJSONBytes(resp.Content), maxRetire)
}

// newMemoryExtractionClient builds the extraction client from the main model
// pool snapshot (sticky cursor + full round fallback), never a single model.
// It reuses the auxiliary-client construction (timeout/retry profile) but is
// owned by this extraction: independent cancel and stale-result checks.
//
// rotation shifts the starting position for a retry after an unusable output
// shape, so the resample is not drawn from the model that produced it. A
// single-model pool has nowhere to rotate to, which is the only option.
func (a *MainAgent) newMemoryExtractionClient(rotation int) *llm.Client {
	a.llmMu.RLock()
	mainClient := a.llmClient
	a.llmMu.RUnlock()
	if mainClient == nil {
		return nil
	}
	pool, selectedIdx := mainClient.ModelPoolSnapshot()
	if len(pool) == 0 {
		return nil
	}
	if rotation > 0 {
		selectedIdx = (selectedIdx + rotation) % len(pool)
	}
	client := newAuxClientFromPool(pool, selectedIdx, 0, a.ServiceTier())
	client.SetStreamRetryRounds(1)
	return client
}

// extractionJSONBytes extracts the largest balanced JSON object from a response
// that may be wrapped in code fences, prose, or a leading think block.
func extractionJSONBytes(content string) []byte {
	start := strings.IndexByte(content, '{')
	end := strings.LastIndexByte(content, '}')
	if start < 0 || end <= start {
		return []byte(content)
	}
	return []byte(content[start : end+1])
}

// memoryCompactionGeneration reads the frozen session's last compaction
// generation for the checkpoint (best-effort; 0 when unavailable).
func (a *MainAgent) memoryCompactionGeneration(sessionDir string) uint64 {
	rm := recovery.NewRecoveryManager(sessionDir)
	defer rm.Close()
	snap, err := rm.Recover()
	if err != nil || snap == nil {
		return 0
	}
	return snap.CompactionGeneration
}

// memoryCheckpointCovers reports whether the session's projected fingerprint is
// already covered by the extraction checkpoint. Best-effort: any failure
// returns false so the session can be safely re-extracted.
func (a *MainAgent) memoryCheckpointCovers(sessionID, fingerprint string) bool {
	if a == nil || a.memoryMgr == nil {
		return false
	}
	cp, err := memory.LoadCheckpoint(a.memoryMgr.Layout())
	if err != nil || cp == nil {
		return false
	}
	return cp.Covered(sessionID, fingerprint)
}

// maybeScheduleStartupBackfill enqueues extraction for at most
// memoryStartupBackfillLimit unlocked, non-imported root sessions whose
// fingerprint is not yet covered. It skips the current active session. Called
// once after the session head is built at startup. Fingerprint checks happen
// in the background worker, so startup never scans full transcripts here.
func (a *MainAgent) maybeScheduleStartupBackfill() {
	if a.memoryMgr == nil || !a.memoryExtractEnabled.Load() {
		return
	}
	sessionsDir, err := a.projectSessionsDir()
	if err != nil {
		return
	}
	loader := recovery.NewReadOnlyTranscriptLoader(sessionsDir)
	candidates, err := loader.ListRecentCandidates(a.SessionDir(), memoryStartupBackfillLimit)
	if err != nil {
		log.Warnf("memory: startup backfill list error=%v", err)
		return
	}
	for _, s := range candidates {
		if meta, err := recovery.LoadSessionMeta(s.Path); err == nil && meta != nil && meta.ImportedFrom != nil {
			// Imported sessions are not backfilled at startup. Freezing one
			// (switching away from it) queues it like any other session, and
			// the uncovered fingerprint keeps it eligible until then.
			continue
		}
		a.scheduleMemoryExtraction(s.Path)
	}
}
