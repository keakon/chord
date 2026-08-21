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
	memoryExtractionTokens        = 24000
	memoryExtractionItemBytes     = 32 * 1024
	memoryExtractionAgentsBytes   = 32 * 1024
	memoryExtractionActiveBytes   = 64 * 1024
	memoryExtractionActiveRecords = 128
	memoryJobRetainBackoff        = 2 * time.Second
	memoryMaxRetryBackoff         = 30 * time.Second
	memoryMaxExtractionAttempts   = 5
	memoryStartupBackfillLimit    = 2
	memoryJobQueueLimit           = 64
)

// memoryJob is one queued extraction attempt. retryAt gates the next attempt
// after a transient failure; attempts bounds retries so a persistently failing
// job stops burning model tokens.
type memoryJob struct {
	sessionDir string
	attempts   int
	retryAt    time.Time
}

// memoryInflight tracks one in-flight extraction so a new foreground turn can
// cancel it (foreground preemption).
type memoryInflight struct {
	sessionDir string
	cancel     context.CancelFunc
}

// initMemory wires the memory manager, resolves the effective auto-extraction
// config (project overrides user), refreshes the session-head reminder, and
// starts the background worker. Called from NewMainAgent before the final
// refreshSystemPrompt so the stable prompt can include the fixed Memory
// discipline when a MEMORY.md exists.
func (a *MainAgent) initMemory(projectRoot string) {
	a.memoryMu.Lock()
	defer a.memoryMu.Unlock()
	if a.memoryMgr != nil {
		return
	}
	m, err := memory.NewManager(projectRoot, a.pathLocator)
	if err != nil {
		a.memoryErr = err
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
		a.drainMemoryQueue()
	}
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
		if len(a.memoryPending) == 0 || a.memoryInflight != nil {
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
		a.memoryInflight = &memoryInflight{sessionDir: job.sessionDir, cancel: cancel}
		a.memoryMu.Unlock()
		err := a.runMemoryExtraction(ctx, job.sessionDir)
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
			log.Warnf("memory: extraction failed session=%v error=%v", filepath.Base(job.sessionDir), err)
			if m := a.memoryMgr; m != nil {
				memory.SaveFailure(m.Layout(), filepath.Base(job.sessionDir), err)
			}
			if memoryPermanentFailure(err) || job.attempts >= memoryMaxExtractionAttempts {
				// Permanent or repeatedly failing job: stop retrying so the
				// worker never hot-loops or burns model tokens. The fingerprint
				// stays uncovered, so a future startup backfill or explicit
				// request retries after external intervention.
				continue
			}
			job.attempts++
			job.retryAt = time.Now().Add(memoryRetryBackoff(job.attempts))
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
			a.refreshMemoryReminderBlock()
		}
	}
}

// memoryRetryBackoff grows exponentially from memoryJobRetainBackoff up to
// memoryMaxRetryBackoff.
func memoryRetryBackoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	shift := attempt - 1
	if shift > 4 {
		shift = 4
	}
	d := memoryJobRetainBackoff * time.Duration(1<<shift)
	if d > memoryMaxRetryBackoff {
		return memoryMaxRetryBackoff
	}
	return d
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
// without external intervention (config, file, or schema changes). Permanent
// jobs are dropped after logging; their fingerprint stays uncovered for a
// later startup backfill or explicit request.
func memoryPermanentFailure(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, memory.ErrManagedMarkers) ||
		errors.Is(err, memory.ErrInvalidExtraction) ||
		errors.Is(err, errMemorySetupFailed)
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
// candidates. It advances no checkpoint on any failure.
func (a *MainAgent) runMemoryExtraction(ctx context.Context, sessionDir string) error {
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
	agentsMD := a.cachedAgentsMDSnapshot()
	if len(agentsMD) > memoryExtractionAgentsBytes {
		agentsMD = agentsMD[:memoryExtractionAgentsBytes]
		for len(agentsMD) > 0 && !utf8.ValidString(agentsMD) {
			agentsMD = agentsMD[:len(agentsMD)-1]
		}
	}
	prompt := buildMemoryExtractionPrompt(kept, agentsMD, active)
	candidates, dropped, err := a.callMemoryExtraction(ctx, prompt)
	if err != nil {
		return err
	}
	if len(dropped) > 0 {
		log.Warnf("memory: dropped %d candidate(s) from extraction session=%v reasons=%v", len(dropped), sessionID, dropped)
	}
	generation := a.memoryCompactionGeneration(sessionDir)
	res, err := a.memoryMgr.CommitExtractionCtx(ctx, sessionID, fingerprint, len(kept), generation, candidates)
	logMemoryCommitWarnings(sessionID, res)
	return err
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
// governor and parses the candidate list. Malformed or unknown output is a
// failure (does not advance the checkpoint); per-candidate drops are returned
// separately and never block the surviving candidates from being committed.
func (a *MainAgent) callMemoryExtraction(ctx context.Context, prompt string) ([]memory.Candidate, []string, error) {
	client := a.newMemoryExtractionClient()
	if client == nil {
		return nil, nil, fmt.Errorf("%w: no model pool available for memory extraction", errMemorySetupFailed)
	}
	client.SetSystemPrompt(memoryExtractionSystemPrompt)
	reqCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	release, err := a.governor.acquireLLM(reqCtx, client.PrimaryModelRef())
	if err != nil {
		return nil, nil, fmt.Errorf("acquire memory extraction LLM capacity: %w", err)
	}
	defer release()

	resp, err := client.CompleteStream(reqCtx, []message.Message{{Role: "user", Content: prompt}}, nil, nil)
	if err != nil {
		return nil, nil, err
	}
	return memory.ParseExtractionOutput(extractionJSONBytes(resp.Content))
}

// newMemoryExtractionClient builds the extraction client from the main model
// pool snapshot (sticky cursor + full round fallback), never a single model.
// It reuses the auxiliary-client construction (timeout/retry profile) but is
// owned by this extraction: independent cancel and stale-result checks.
func (a *MainAgent) newMemoryExtractionClient() *llm.Client {
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
			// Imported sessions are not auto-backfilled unless the user later
			// asks; the fingerprint stays uncovered, so an explicit request
			// still works.
			continue
		}
		a.scheduleMemoryExtraction(s.Path)
	}
}
