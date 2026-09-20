package tools

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/shell"
)

const (
	// maxJobs also bounds the registry's in-memory output: each job retains at
	// most 1.5x maxOutputBytes of live bytes (see tailWriter), so the
	// pathological ceiling is roughly 750 MB when every job produces megabytes
	// of output. The tailWriter's backing capacity can sit above that 1.5x mark
	// after append growth, so peak memory is higher still.
	maxJobs = 50
	// maxRetainedFinishedJobs bounds how many terminal jobs keep their captured
	// output in memory so a later job_output can still read the final result.
	// Terminal jobs are evicted oldest-first once this many accumulate.
	maxRetainedFinishedJobs = 20
	// maxClaimedJobIDs bounds the dedup memory for results already surfaced. It
	// must outlive the job entry: eviction reclaims the output buffer, but the
	// "already delivered" answer still has to survive it or a completion
	// notification replayed after a resume would be delivered twice.
	maxClaimedJobIDs = 256
)

type jobStatus string

const (
	jobStatusRunning   jobStatus = "running"
	jobStatusStopping  jobStatus = "stopping"
	jobStatusCompleted jobStatus = "completed"
	jobStatusKilled    jobStatus = "killed"
	jobStatusFailed    jobStatus = "failed"
)

// jobStopOrigin records who requested a job's cancellation. A user-initiated
// stop is the only origin that is surfaced in the completion event so the model
// can tell "the operator stopped it" apart from a deadline or session switch.
type jobStopOrigin string

const jobStopOriginUser jobStopOrigin = "user"

type jobStartRequest struct {
	Command     string
	Description string
	Workdir     string
	// TimeoutSec is the hard deadline in seconds; 0 means no hard deadline.
	TimeoutSec int
	ShellType  string
	LogDir     string
	// Detached starts the job as background-owned: no caller waits on it and
	// its completion is delivered as an asynchronous notification.
	Detached bool
}

type job struct {
	ID            string
	AgentID       string
	SessionDir    string
	eventSender   EventSender
	Command       string
	Description   string
	LogFile       string
	StartedAt     time.Time
	MaxRuntimeSec int

	// cmd is written once by start before the run goroutine is launched; the
	// goroutine start gives run its happens-before edge, so no extra locking
	// guards it.
	cmd       *exec.Cmd
	cancelCh  chan string
	logWriter *rotatingJobLog
	output    *tailWriter
	done      chan struct{}

	mu         sync.Mutex
	status     jobStatus
	detail     string
	exitErr    error
	finished   bool
	finishedAt time.Time
	detached   bool
	reported   bool
	// stopOrigin records who cancelled the job; empty when it was not stopped.
	// It travels with the completion event so a user stop is distinguishable
	// from a deadline, a job_kill, or a session-switch teardown.
	stopOrigin jobStopOrigin
	// readers holds one cursor and anti-polling streak per reading agent. A
	// job is deliberately readable by more than its owner - job_list offers the
	// main agent's jobs and the caller's owner's jobs - and the incremental read
	// consumes what it returns, so a single shared cursor would let one reader
	// swallow another's output and drive it into the polling refusal without it
	// ever seeing a byte. Keyed by agent id; the set is bounded by the agents
	// that can reach the job.
	readers map[string]*jobReaderState
}

// jobReaderState is one reader's view of a job's output.
type jobReaderState struct {
	offset          int64
	noProgressReads int
}

// JobRegistry owns every background or foreground shell execution. A job's
// lifecycle outlives the tool call that started it once it is detached, so
// turn cancellation never kills a detached job by itself.
type JobRegistry struct {
	mu   sync.RWMutex
	jobs map[string]*job
	seq  atomic.Uint64

	// claimed remembers ids whose result was already surfaced *and* whose job
	// entry has since been evicted (see evictFinishedLocked). While the entry
	// lives it carries its own reported flag, so only eviction needs this — and
	// without it a notification replayed after a resume is delivered twice.
	claimed      map[string]struct{}
	claimedOrder []string

	// pruneMu guards the rate-limit state of the job-log retention sweep.
	pruneMu      sync.Mutex
	lastPruneAt  time.Time
	lastPruneDir string
}

var globalJobRegistry = &JobRegistry{jobs: make(map[string]*job)}

// jobLogPruneInterval rate-limits the retention sweep. The sweep walks the
// whole log directory and stats every entry, so running it on each job start
// would make every shell call in a long session pay a growing scan; the 7-day
// retention only needs it occasionally.
const jobLogPruneInterval = 10 * time.Minute

// maybePruneJobLogs runs the retention sweep for dir at most once per interval.
// A different directory prunes immediately, so switching sessions still
// reclaims that session's stale logs without waiting out another dir's window.
func (r *JobRegistry) maybePruneJobLogs(dir string) {
	if dir == "" {
		return
	}
	now := time.Now()
	r.pruneMu.Lock()
	if r.lastPruneDir == dir && now.Sub(r.lastPruneAt) < jobLogPruneInterval {
		r.pruneMu.Unlock()
		return
	}
	r.lastPruneDir = dir
	r.lastPruneAt = now
	r.pruneMu.Unlock()
	// Kept synchronous: the rate limit above already reduces this to one
	// directory scan per interval, and running it in the background would make
	// the sweep unobservable to tests and to shutdown ordering for no measurable
	// gain on a path that is hit at most once per interval.
	pruneJobLogs(dir)
}

// commandWaitDelay bounds how long cmd.Wait keeps draining a command's I/O
// after the command's own process has exited (see cmd.WaitDelay in start and
// RunLocalShellCapture).
const commandWaitDelay = 3 * time.Second

func (r *JobRegistry) start(ctx context.Context, req jobStartRequest) (*job, error) {
	// The sweep does directory I/O outside r.mu so it cannot block concurrent
	// job_output / job_list calls.
	r.maybePruneJobLogs(req.LogDir)
	// The id comes from an atomic counter, so the diagnostic log can be opened
	// outside r.mu as well: file creation is real I/O and the registry lock
	// must not serialize behind a slow filesystem. A capacity rejection below
	// merely skips one id and leaves an empty log file for the retention sweep
	// to reclaim.
	id := fmt.Sprintf("job-%d", r.seq.Add(1))
	var (
		logWriter *rotatingJobLog
		logPath   string
	)
	if req.LogDir != "" {
		logPath = jobLogFilePath(req.LogDir, id)
		sessionDir := filepath.Dir(req.LogDir)
		writer, err := openRotatingJobLog(sessionDir, logPath)
		if err != nil {
			return nil, fmt.Errorf("creating log file: %w", err)
		}
		logWriter = writer
	}
	r.mu.Lock()
	r.evictFinishedLocked()
	if len(r.jobs) >= maxJobs {
		r.mu.Unlock()
		if logWriter != nil {
			_ = logWriter.Close()
			_ = os.Remove(logPath)
		}
		return nil, fmt.Errorf("maximum number of background jobs (%d) reached; wait for or kill a job first", maxJobs)
	}
	// The session directory the job belongs to travels with the completion
	// event so a job that finishes across a session switch is not delivered
	// into the new session's transcript.
	jobSessionDir := SessionDirFromContext(ctx)
	if jobSessionDir == "" && req.LogDir != "" {
		jobSessionDir = filepath.Dir(req.LogDir)
	}
	j := &job{
		ID:          id,
		AgentID:     AgentIDFromContext(ctx),
		SessionDir:  jobSessionDir,
		eventSender: EventSenderFromContext(ctx),
		Command:     req.Command,
		Description: req.Description,
		LogFile:     logPath,
		StartedAt:   time.Now(),
		status:      jobStatusRunning,
		detached:    req.Detached,
		cancelCh:    make(chan string, 1),
		done:        make(chan struct{}),
		logWriter:   logWriter,
	}
	j.MaxRuntimeSec = req.TimeoutSec
	st := shell.ParseShellType(req.ShellType)
	binary, args := shell.GetShellCommand(st, req.Command)
	cmd := exec.Command(binary, args...)
	_, _ = configureCommandProcessGroup(cmd)
	// A job's own process exiting does not guarantee its pipes close: a
	// daemonized descendant that outlives the process group can hold stdout or
	// stderr open forever, and cmd.Wait would block on their EOF past every
	// grace period. WaitDelay bounds that wait to an already-terminal process;
	// run maps the abandoned-I/O error back to the recorded exit status.
	cmd.WaitDelay = commandWaitDelay
	if req.Workdir != "" {
		cmd.Dir = req.Workdir
	}
	// Jobs are intentionally non-interactive. Leaving Stdin nil makes Go
	// connect the child process to the null device instead of the TUI stdin.
	cmd.Env = appendNonInteractiveEnv(nil)
	outputBuf := newTailWriter(maxOutputBytes)
	j.output = outputBuf
	if logWriter != nil {
		cmd.Stdout = io.MultiWriter(logWriter, outputBuf)
		cmd.Stderr = io.MultiWriter(logWriter, outputBuf)
	} else {
		cmd.Stdout = outputBuf
		cmd.Stderr = outputBuf
	}
	j.cmd = cmd
	r.jobs[id] = j
	r.mu.Unlock()

	go r.run(j)
	log.Debugf("job started id=%v command=%v max_runtime_sec=%v detached=%v agent_id=%v", id, req.Command, j.MaxRuntimeSec, req.Detached, j.AgentID)
	return j, nil
}

func (r *JobRegistry) run(j *job) {
	cmd := j.cmd
	if err := cmd.Start(); err != nil {
		r.finish(j, jobStatusFailed, "failed to start", fmt.Errorf("starting command: %w", err))
		return
	}

	waitCh := waitForCommand(cmd)
	var (
		status jobStatus
		detail string
		err    error
	)
	handleExit := func(rawErr error) {
		// WaitDelay expiry means the process exited successfully but descendants
		// kept the pipes open: the recorded exit status is authoritative, and
		// the abandoned I/O only fed the in-memory window and the diagnostic
		// log, both expendable. Report success instead of the I/O cutoff. A
		// non-zero exit surfaces as *exec.ExitError and never carries
		// ErrWaitDelay, so the exit status needs no reconstruction here.
		if errors.Is(rawErr, exec.ErrWaitDelay) {
			rawErr = nil
		}
		if rawErr == nil {
			status, detail, err = jobStatusCompleted, "exit code 0", nil
			return
		}
		status = jobStatusFailed
		detail = shortExitDetail(rawErr)
		err = j.formatRuntimeError(rawErr)
	}

	if j.MaxRuntimeSec <= 0 {
		select {
		case reason := <-j.cancelCh:
			if rawErr, natural := terminateJobProcessGroup(cmd, reason, waitCh); natural {
				handleExit(rawErr)
			} else {
				status, detail, err = jobStatusKilled, reason, rawErr
			}
		case <-waitCh.done:
			rawErr, _ := waitCh.result()
			handleExit(rawErr)
		}
	} else {
		timer := time.NewTimer(time.Duration(j.MaxRuntimeSec) * time.Second)
		defer timer.Stop()
		select {
		case reason := <-j.cancelCh:
			if rawErr, natural := terminateJobProcessGroup(cmd, reason, waitCh); natural {
				handleExit(rawErr)
			} else {
				status, detail, err = jobStatusKilled, reason, rawErr
			}
		case <-waitCh.done:
			rawErr, _ := waitCh.result()
			handleExit(rawErr)
		case <-timer.C:
			reason := fmt.Sprintf("timed out after %ds", j.MaxRuntimeSec)
			if rawErr, natural := terminateJobProcessGroup(cmd, reason, waitCh); natural {
				handleExit(rawErr)
			} else {
				status, detail, err = jobStatusKilled, reason, rawErr
				// A timeout otherwise invites an unchanged, equally slow
				// re-run: the model cannot see the deadline it hit, so
				// steer the next step.
				err = fmt.Errorf("%w\n%s", err, shellTimeoutGuidance)
			}
		}
	}
	r.finish(j, status, detail, err)
}

// finish records the terminal state, wakes any waiter, and delivers the
// completion notification when the job completed while detached. Detaching and
// finishing share j.mu, so a job cannot both complete as foreground and notify.
func (r *JobRegistry) finish(j *job, status jobStatus, detail string, exitErr error) {
	j.mu.Lock()
	if j.finished {
		j.mu.Unlock()
		return
	}
	j.finished = true
	j.finishedAt = time.Now()
	j.status = status
	j.detail = detail
	j.exitErr = exitErr
	notify := j.detached
	// A recorded user stop only counts when the kill actually took effect: a
	// process that exited on its own between the stop request and the cancel
	// being consumed finishes as completed, and must neither be reported to the
	// model as a user stop nor lose its completion toast.
	userStopped := j.stopOrigin == jobStopOriginUser && status == jobStatusKilled
	j.mu.Unlock()
	close(j.done)
	if j.logWriter != nil {
		_ = j.logWriter.Close()
	}
	if !notify {
		return
	}
	sender := j.eventSender
	if sender == nil {
		// A detached job that finished without a sender would silently lose its
		// result, and the model would wait forever on a notification that can
		// never arrive. Log it so the missing wiring is diagnosable.
		log.Warnf("job finished with no event sender id=%v agent_id=%v status=%v", j.ID, j.AgentID, status)
		return
	}
	statusText := j.statusText()
	sender.SendAgentEvent(
		EventBackgroundObjectFinished,
		j.AgentID,
		&JobFinishedPayload{
			BackgroundID: j.ID,
			AgentID:      j.AgentID,
			SessionDir:   j.SessionDir,
			Status:       statusText,
			Message:      j.completionMessage(status, statusText),
			UserStopped:  userStopped,
		},
	)
}

// detach converts a still-running foreground job into a background one and
// reports whether the caller should hand back a job handle. It returns false
// when the job already finished, in which case the caller must render the
// terminal result instead.
func (j *job) detach() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.finished {
		return false
	}
	j.detached = true
	return true
}

// beginStop is the shared core of requestCancel and stopByUser: it marks a
// live job as stopping and delivers the cancel reason without waiting. origin
// records who asked for the stop; empty leaves the recorded origin untouched.
// setDetached overwrites j.detached in the same critical section when
// non-nil, so the canceller goroutine can never observe a half-applied stop.
func (j *job) beginStop(reason string, origin jobStopOrigin, setDetached *bool) bool {
	j.mu.Lock()
	if j.finished {
		j.mu.Unlock()
		return false
	}
	if j.status == jobStatusRunning {
		j.status = jobStatusStopping
	}
	if origin != "" {
		j.stopOrigin = origin
	}
	if setDetached != nil {
		j.detached = *setDetached
	}
	j.mu.Unlock()
	select {
	case j.cancelCh <- reason:
	default:
	}
	return true
}

// requestCancel signals cancellation without waiting. notify controls whether
// the eventual terminal state is delivered as an asynchronous notification:
// an explicit job_kill already tells the model the outcome, so it suppresses
// the wakeup.
//
// It assigns j.detached outright rather than only clearing it, so a cancel that
// suppresses the notification also cancels an earlier promotion's promise to
// notify. That is deliberate last-writer-wins: a session switch or an agent
// stop cancels jobs whose completion would be delivered into a transcript that
// is going away, and job_kill reports the outcome itself. Either way the
// terminal state is delivered as a foreground result or not at all, never
// twice, and never to a session that no longer owns the job.
func (j *job) requestCancel(reason string, notify bool) bool {
	return j.beginStop(reason, "", &notify)
}

// stopByUser is the operator-initiated stop. Unlike requestCancel it never
// touches j.detached: only an already-detached job needs an asynchronous
// completion notification, while a job still owned by a waiting foreground
// shell call is reported to the model through that tool result. Setting the
// user stop origin lets finish() mark the completion event so the agent can
// suppress the redundant toast.
func (j *job) stopByUser(reason string) bool {
	return j.beginStop(reason, jobStopOriginUser, nil)
}

// finishedTime reports when a terminal job finished. The finished flag and the
// timestamp come from one critical section, so eviction ordering never mixes a
// finished flag from one instant with a zero timestamp from another.
func (j *job) finishedTime() (time.Time, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if !j.finished {
		return time.Time{}, false
	}
	return j.finishedAt, true
}

func (j *job) isFinished() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.finished
}

// isKilled reports whether the job's terminal state is a kill (deadline or
// explicit cancellation), so callers can skip notes that would contradict the
// kill error without reading the mutex-guarded status directly.
func (j *job) isKilled() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.status == jobStatusKilled
}

// statusText is the model-facing terminal status of the job.
func (j *job) statusText() string {
	j.mu.Lock()
	defer j.mu.Unlock()
	switch j.status {
	case jobStatusCompleted:
		return "completed (exit code 0)"
	case jobStatusFailed:
		if j.detail != "" {
			return "failed (" + j.detail + ")"
		}
		return "failed"
	case jobStatusKilled:
		if j.detail != "" {
			return "killed (" + j.detail + ")"
		}
		return "killed"
	default:
		return string(j.status)
	}
}

func (j *job) state() JobState {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.stateLocked()
}

// stateLocked snapshots the job while j.mu is held. completionMessage builds
// on it instead of hand-rolling its own JobState so new fields cannot drift.
func (j *job) stateLocked() JobState {
	lastOutputAt := time.Time{}
	if j.output != nil {
		// Keep the lock order consistent with displayPeek and the incremental
		// reader: job state first, then the output buffer.
		lastOutputAt = j.output.lastOutputTime()
	}
	return JobState{
		ID:            j.ID,
		AgentID:       j.AgentID,
		Description:   j.Description,
		Command:       j.Command,
		StartedAt:     j.StartedAt,
		MaxRuntimeSec: j.MaxRuntimeSec,
		Status:        string(j.status),
		FinishedAt:    j.finishedAt,
		LastOutputAt:  lastOutputAt,
	}
}

func (j *job) formatRuntimeError(err error) error {
	if err == nil {
		return nil
	}
	output := j.rawOutput()
	if ClassifyNonInteractiveRuntimeFailure(j.Command, err, output) != nil {
		return FormatNonInteractiveRuntimeError(NameShell, j.Command, err, output)
	}
	if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
		return shellExitErrorForCommand(j.Command, exitErr, output)
	}
	return fmt.Errorf("command error: %w", err)
}

func (j *job) outputString() string {
	if j == nil || j.output == nil {
		return ""
	}
	return j.output.String()
}

// rawOutput returns the retained window exactly as the process wrote it.
// Classification and error formatting must read this rather than outputString:
// the truncation notice in outputString is model-facing decoration and must
// never be matched against by build-failure or runtime-failure sniffing.
func (j *job) rawOutput() string {
	if j == nil || j.output == nil {
		return ""
	}
	return j.output.raw()
}

func (j *job) completionMessage(status jobStatus, statusText string) string {
	if j == nil {
		return ""
	}
	j.mu.Lock()
	state := j.stateLocked()
	j.mu.Unlock()
	elapsed := state.Elapsed(state.FinishedAt)
	quiet := jobQuietFieldValue(state, state.FinishedAt)
	log.Debugf("job finished id=%v status=%v elapsed=%v quiet=%v has_output=%v", j.ID, status, FormatElapsed(elapsed), FormatElapsed(state.QuietDuration(state.FinishedAt)), state.HasOutput())
	msg := fmt.Sprintf("[Background job %s finished]\n\nStatus: %s\nElapsed: %s\nQuiet: %s", j.ID, statusText, FormatElapsed(elapsed), quiet)
	if purpose := strings.TrimSpace(j.Description); purpose != "" {
		msg += "\nPurpose: " + purpose
	}
	if command := summarizeJobCommand(j.Command); command != "" {
		msg += "\nCommand: " + command
	}
	if snippet := j.relevantOutputForCompletion(); snippet != "" {
		msg += "\n\nRelevant output:\n" + snippet
	}
	// A successful completion is informational: the model wakes with the result
	// already in hand, so pointing it at another tool only invites a redundant
	// call. A failure or kill keeps the pointer so the model can inspect the
	// diagnostic output before deciding what to do.
	if status != jobStatusCompleted {
		msg += fmt.Sprintf("\n\nRead its output with job_output(%s).", j.ID)
	}
	return msg
}

// summarizeJobCommand renders a command for a completion notification: the
// first line, length-capped so a heredoc or long pipeline cannot dominate the
// message the model wakes up to.
func summarizeJobCommand(command string) string {
	command = strings.TrimSpace(command)
	if command == "" {
		return ""
	}
	if idx := strings.IndexByte(command, '\n'); idx >= 0 {
		command = strings.TrimSpace(command[:idx])
	}
	return truncateForError(command, 200)
}

// relevantOutputForCompletion renders the end of a job's output for the
// completion notification: failures and summaries land at the tail, while the
// head is usually setup noise the model has already seen.
func (j *job) relevantOutputForCompletion() string {
	if j == nil || j.output == nil {
		return ""
	}
	const maxLen = 500
	snippet, dropped, truncated := j.output.tail(maxLen)
	snippet = strings.TrimSpace(cleanJobOutputText(snippet))
	if snippet == "" {
		return ""
	}
	var prefix string
	if dropped > 0 {
		prefix += fmt.Sprintf("(earlier output dropped from the buffer: %d bytes)\n", dropped)
	}
	if truncated {
		prefix += fmt.Sprintf("...(showing the last %d bytes)\n", maxLen)
	}
	return prefix + snippet
}

// readerStateLocked returns the reader's cursor and streak, creating it on
// first read. Callers must hold j.mu.
func (j *job) readerStateLocked(reader string) *jobReaderState {
	if j.readers == nil {
		j.readers = make(map[string]*jobReaderState, 2)
	}
	state, ok := j.readers[reader]
	if !ok {
		state = &jobReaderState{}
		j.readers[reader] = state
	}
	return state
}

// readIncremental returns output produced since this reader's previous read,
// and how many bytes it missed because they had already fallen out of the
// window. The cursor read and commit share one critical section: job_output
// advertises itself as concurrency-safe, so two batched reads by the same
// reader must each claim a disjoint window instead of replaying or dropping
// one. Readers do not consume from each other: each has its own cursor.
func (j *job) readIncremental(reader string) (string, int64) {
	j.mu.Lock()
	defer j.mu.Unlock()
	state := j.readerStateLocked(reader)
	chunk, next, dropped := j.output.readFrom(state.offset)
	state.offset = next
	return chunk, dropped
}

// noteReadOutcome increments this reader's consecutive no-new-output streak and
// returns it. Callers pass false for a read that produced bytes, and for
// blocking waits (output/exit), which are legitimate renewals rather than
// polling. The streak is per reader, so one agent's polling cannot refuse
// another's first read.
func (j *job) noteReadOutcome(reader string, noNewBytes bool) int {
	j.mu.Lock()
	defer j.mu.Unlock()
	state := j.readerStateLocked(reader)
	if noNewBytes {
		state.noProgressReads++
	} else {
		state.noProgressReads = 0
	}
	return state.noProgressReads
}

// outputWaitState returns the current output generation and whether this reader
// already has unread bytes. The cursor and notification generation are sampled
// as one wait predicate, so a write cannot slip between the check and select.
func (j *job) outputWaitState(reader string) (<-chan struct{}, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	cursor := j.readerStateLocked(reader).offset
	return j.output.waitSignalAfter(cursor)
}

func shortExitDetail(err error) string {
	if err == nil {
		return ""
	}
	if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
		if name := exitSignalName(exitErr); name != "" {
			return "signal: " + name
		}
		return fmt.Sprintf("exit code %d", exitErr.ExitCode())
	}
	return err.Error()
}

type commandWait struct {
	mu       sync.Mutex
	done     chan struct{}
	err      error
	finished bool
}

func waitForCommand(cmd *exec.Cmd) *commandWait {
	waitCh := &commandWait{done: make(chan struct{})}
	go func() {
		err := cmd.Wait()
		waitCh.mu.Lock()
		waitCh.err = err
		waitCh.finished = true
		close(waitCh.done)
		waitCh.mu.Unlock()
	}()
	return waitCh
}

func (w *commandWait) result() (error, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.err, w.finished
}

// cancelAll signals every id in one pass, then waits for them under a single
// shared deadline. Waiting per job would multiply the SIGTERM→SIGKILL grace
// period by the number of stuck jobs, blocking session switch and shutdown for
// tens of seconds.
func (r *JobRegistry) cancelAll(ids []string, reason string) {
	targets := make([]*job, 0, len(ids))
	for _, id := range ids {
		if j, ok := r.get(id); ok && j.requestCancel(reason, false) {
			targets = append(targets, j)
		}
	}
	if len(targets) == 0 {
		return
	}
	deadline := time.NewTimer(2*killGracePeriod + time.Second)
	defer deadline.Stop()
	for _, j := range targets {
		select {
		case <-j.done:
		case <-deadline.C:
			return
		}
	}
}

func (r *JobRegistry) kill(id string, reason string) bool {
	r.mu.RLock()
	j, ok := r.jobs[id]
	r.mu.RUnlock()
	if !ok {
		return false
	}
	return j.requestCancel(reason, false)
}

func (r *JobRegistry) get(id string) (*job, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	j, ok := r.jobs[id]
	return j, ok
}

// accessibleFrom reports whether the caller identified by ctx may read or stop
// this job.
func (j *job) accessibleFrom(ctx context.Context) bool {
	if j == nil {
		return false
	}
	return jobOwnerAccessibleFrom(ctx, j.AgentID)
}

// jobOwnerAccessibleFrom reports whether the caller identified by ctx may reach
// a job owned by owner. Identity is required on both sides: a caller with no
// agent id or a job with no recorded owner is denied rather than treated as
// public. Besides its own jobs, a caller may reach the main agent's jobs and
// jobs started by its direct owner (a worker reading a job its owner launched).
// Job ownership is immutable after start, so reading it here needs no lock.
//
// It is the single source of truth for job visibility: job_output and job_kill
// gate one id with it, and job_list filters its snapshot with it, so the list
// can neither reveal a job the caller cannot act on nor hide one it can.
func jobOwnerAccessibleFrom(ctx context.Context, owner string) bool {
	caller := strings.TrimSpace(AgentIDFromContext(ctx))
	owner = strings.TrimSpace(owner)
	if caller == "" || owner == "" {
		return false
	}
	if caller == owner {
		return true
	}
	access := JobAccessFromContext(ctx)
	if access.MainAgentID != "" && (caller == access.MainAgentID || owner == access.MainAgentID) {
		return true
	}
	return access.OwnerAgentID != "" && owner == access.OwnerAgentID
}

// claimReported atomically marks a job reported and reports whether this call
// was the one that claimed it. An unknown id is claimed only when the registry
// has no record of the result reaching the model: a job whose entry was evicted
// *after* it was surfaced is still recorded as delivered, so eviction cannot
// resurrect a notification, while an id the registry never owned (a replayed
// notification for a job from an earlier process) is still delivered rather
// than silently dropped. The lookup, the reported flag, and the eviction sweep
// all share r.mu -> j.mu ordering so a claim and an eviction cannot interleave.
func (r *JobRegistry) claimReported(id string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	j, ok := r.jobs[id]
	if !ok {
		_, dup := r.claimed[id]
		return !dup
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.reported {
		return false
	}
	j.reported = true
	return true
}

// isReported reports whether the terminal result was already surfaced.
func (j *job) isReported() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.reported
}

// noteClaimedLocked remembers an evicted job as already delivered. The caller
// must hold r.mu. Only eviction needs this: while the job entry is alive it
// carries its own reported flag.
func (r *JobRegistry) noteClaimedLocked(id string) {
	if r.claimed == nil {
		r.claimed = make(map[string]struct{})
	}
	if _, ok := r.claimed[id]; ok {
		return
	}
	r.claimed[id] = struct{}{}
	r.claimedOrder = append(r.claimedOrder, id)
	// Bounded FIFO: the oldest record is the least likely to be replayed, and
	// losing it only risks one duplicate for a job finished long ago.
	if len(r.claimedOrder) > maxClaimedJobIDs {
		delete(r.claimed, r.claimedOrder[0])
		r.claimedOrder = append(r.claimedOrder[:0], r.claimedOrder[1:]...)
	}
}

// ClaimJobReported reports whether a job completion notification still needs to
// be delivered, atomically claiming it when so. It is false only when the
// terminal result was already surfaced (a terminal job_output read, or a
// foreground result), never because the job is unknown.
func ClaimJobReported(id string) bool {
	return globalJobRegistry.claimReported(id)
}

// StopJobByUser stops a running job on the operator's behalf and reports
// whether a live job was found. It is the interface-facing stop entry point:
// a detached job still delivers an asynchronous completion notification marked
// UserStopped, while a foreground job is told through its waiting shell result.
func StopJobByUser(id, reason string) bool {
	j, ok := globalJobRegistry.get(id)
	if !ok {
		return false
	}
	return j.stopByUser(reason)
}

type JobState struct {
	ID            string
	AgentID       string
	Description   string
	Command       string
	StartedAt     time.Time
	MaxRuntimeSec int
	Status        string
	FinishedAt    time.Time
	LastOutputAt  time.Time
}

// Elapsed returns the job's terminal duration, or its live duration when it is
// still running. StartedAt is the baseline in both cases.
func (s JobState) Elapsed(now time.Time) time.Duration {
	if s.StartedAt.IsZero() {
		return 0
	}
	end := s.FinishedAt
	if end.IsZero() {
		end = now
	}
	return max(end.Sub(s.StartedAt), 0)
}

// QuietDuration returns how long the job has gone without output at now. A
// job that has not produced output yet measures from its start, so callers can
// show a useful quiet duration without inventing a last-output timestamp.
func (s JobState) QuietDuration(now time.Time) time.Duration {
	since := s.LastOutputAt
	if since.IsZero() {
		since = s.StartedAt
	}
	if since.IsZero() {
		return 0
	}
	end := s.FinishedAt
	if end.IsZero() {
		end = now
	}
	return max(end.Sub(since), 0)
}

// HasOutput reports whether the job has produced any output.
func (s JobState) HasOutput() bool { return !s.LastOutputAt.IsZero() }

// QuietWarning reports whether the running job has crossed the fixed
// observation threshold. It is intentionally a display/observation signal,
// not a claim that the runner is stuck.
func (s JobState) QuietWarning(now time.Time) bool {
	return s.Status == string(jobStatusRunning) && s.QuietDuration(now) >= quietWarnAfter
}

// quietWarnAfter is the duration after which a running job's quiet period is
// worth calling out in observation surfaces. It does not create a notification
// or change the job lifecycle.
const quietWarnAfter = 5 * time.Minute

// jobQuietFieldValue renders the value of the model-facing quiet field, the
// way a labeled line reads it: "Quiet: 5s". The metric's name already sits in
// the label, so repeating it in the value would be noise — only the case that
// the number alone cannot carry gets words.
func jobQuietFieldValue(state JobState, now time.Time) string {
	if !state.HasOutput() {
		return "no output " + FormatElapsed(state.QuietDuration(now))
	}
	return FormatElapsed(state.QuietDuration(now))
}

// JobQuietLabel renders how long a job has been silent as the compact label a
// one-line surface shows: "quiet 5s", or "no output 5s" before the job has
// printed anything. A job that has not printed yet was never quiet after
// output, so the two cases read differently rather than one implying the other.
//
// jobQuietPhrase builds the sentence form on top of it, so both spell the same
// measurement instead of one surface drifting away from the other.
func JobQuietLabel(state JobState, now time.Time) string {
	if state.HasOutput() {
		return "quiet " + jobQuietFieldValue(state, now)
	}
	return jobQuietFieldValue(state, now)
}

// JobQuietLabelPrefixWidth is the widest prefix JobQuietLabel can put before the
// duration. A one-line surface clamps the rendered label to this width plus its
// own elapsed clamp, so the wording and the column it renders into cannot drift
// apart silently.
const JobQuietLabelPrefixWidth = len("no output ")

// jobQuietPhrase renders JobQuietLabel as wording a sentence can quote: the
// completion message's siblings aside, job_list and a wait notice both
// describe this one measurement. Past quietWarnAfter the phrase carries the
// observation that the runner may still be working — a long silence is
// evidence, not proof it hung, and only a running job can reach it.
func jobQuietPhrase(state JobState, now time.Time) string {
	phrase := JobQuietLabel(state, now)
	if state.QuietWarning(now) {
		phrase += "; the runner may still be working"
	}
	return phrase
}

func (r *JobRegistry) snapshotStates() []JobState {
	r.mu.RLock()
	jobs := make([]*job, 0, len(r.jobs))
	for _, j := range r.jobs {
		jobs = append(jobs, j)
	}
	r.mu.RUnlock()
	states := make([]JobState, 0, len(jobs))
	for _, j := range jobs {
		states = append(states, j.state())
	}
	slices.SortFunc(states, func(a, b JobState) int {
		if c := a.StartedAt.Compare(b.StartedAt); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	return states
}

// SnapshotJobs returns the current lifecycle state of every tracked job.
func SnapshotJobs() []JobState {
	return globalJobRegistry.snapshotStates()
}

// JobDisplayPeek is a read-only projection of one job for the interface. It
// carries no cursor and no reader state, so rendering it can never consume
// output or advance an agent's job_output position.
type JobDisplayPeek struct {
	ID           string
	Label        string
	Command      string
	Owner        string
	Status       string
	StartedAt    time.Time
	LastOutputAt time.Time
	LogFile      string
	Tail         string
	DroppedBytes int64
	Truncated    bool
}

// PeekJobForDisplay returns a non-consuming view of one job's state and tail
// output for the operator interface, or false for an unknown id. Unlike
// job_output it neither advances a reader cursor nor claims the completion
// notification, and it cleans the tail the same way the model-facing reads do,
// since the interface cannot reach cleanJobOutputText.
func PeekJobForDisplay(id string, maxTailBytes int) (JobDisplayPeek, bool) {
	j, ok := globalJobRegistry.get(id)
	if !ok {
		return JobDisplayPeek{}, false
	}
	return j.displayPeek(maxTailBytes), true
}

func (j *job) displayPeek(maxTailBytes int) JobDisplayPeek {
	j.mu.Lock()
	defer j.mu.Unlock()
	peek := JobDisplayPeek{
		ID:        j.ID,
		Label:     j.Description,
		Command:   j.Command,
		Owner:     j.AgentID,
		Status:    string(j.status),
		StartedAt: j.StartedAt,
		LogFile:   j.LogFile,
	}
	if peek.Label == "" {
		peek.Label = j.Command
	}
	if j.output != nil {
		snippet, dropped, truncated := j.output.tail(maxTailBytes)
		peek.Tail = cleanJobOutputText(snippet)
		peek.DroppedBytes = dropped
		peek.Truncated = truncated
		peek.LastOutputAt = j.output.lastOutputTime()
	}
	return peek
}

func StopAllJobsForAgent(agentID string, reason string) int {
	return globalJobRegistry.stopForAgent(agentID, reason)
}

func StopAllJobsForSessionSwitch() int {
	return globalJobRegistry.stopAll("terminated on session switch")
}

func StopAllJobsForShutdown() int {
	return globalJobRegistry.stopAll("terminated on client exit")
}

func (r *JobRegistry) stopForAgent(agentID string, reason string) int {
	r.mu.Lock()
	ids := make([]string, 0)
	for id, j := range r.jobs {
		if j.AgentID == agentID && !j.isFinished() {
			ids = append(ids, id)
		}
	}
	r.mu.Unlock()
	r.cancelAll(ids, reason)
	return len(ids)
}

func (r *JobRegistry) stopAll(reason string) int {
	r.mu.Lock()
	ids := make([]string, 0, len(r.jobs))
	for id, j := range r.jobs {
		if !j.isFinished() {
			ids = append(ids, id)
		}
	}
	r.mu.Unlock()
	r.cancelAll(ids, reason)
	return len(ids)
}

// evictFinishedLocked drops the oldest terminal jobs once their retained count
// exceeds the cap. The caller must hold r.mu.
func (r *JobRegistry) evictFinishedLocked() {
	type finishedJob struct {
		j          *job
		finishedAt time.Time
	}
	finished := make([]finishedJob, 0)
	for _, j := range r.jobs {
		if at, ok := j.finishedTime(); ok {
			finished = append(finished, finishedJob{j: j, finishedAt: at})
		}
	}
	if len(finished) <= maxRetainedFinishedJobs {
		return
	}
	slices.SortFunc(finished, func(a, b finishedJob) int { return a.finishedAt.Compare(b.finishedAt) })
	for _, c := range finished[:len(finished)-maxRetainedFinishedJobs] {
		// Eviction reclaims the output buffer but must not reclaim the knowledge
		// that the result already reached the model, or a notification replayed
		// after a resume would be delivered a second time.
		if c.j.isReported() {
			r.noteClaimedLocked(c.j.ID)
		}
		delete(r.jobs, c.j.ID)
	}
}

func terminateJobProcessGroup(cmd *exec.Cmd, reason string, waitCh *commandWait) (error, bool) {
	if cmd == nil || cmd.Process == nil {
		return fmt.Errorf("command %s", reason), false
	}
	// Hold the same lock the wait goroutine publishes under, so the "already
	// finished" test and the stop signal cannot interleave with publication.
	waitCh.mu.Lock()
	if waitCh.finished {
		err := waitCh.err
		waitCh.mu.Unlock()
		return err, true
	}
	signalErr := terminateCommandProcessGroup(cmd)
	waitCh.mu.Unlock()
	if processGroupAlreadyGone(signalErr) {
		// The group no longer existed when the stop arrived: the command exited
		// on its own, so its result wins even though the wait goroutine had not
		// published it yet.
		<-waitCh.done
		err, _ := waitCh.result()
		return err, true
	}
	select {
	case <-waitCh.done:
		err, _ := waitCh.result()
		if err != nil {
			return fmt.Errorf("command %s: %w", reason, err), false
		}
		return fmt.Errorf("command %s", reason), false
	case <-time.After(killGracePeriod):
		_ = forceTerminateCommandProcessGroup(cmd)
		select {
		case <-waitCh.done:
			err, _ := waitCh.result()
			if err != nil {
				return fmt.Errorf("command %s: %w", reason, err), false
			}
			return fmt.Errorf("command %s", reason), false
		case <-time.After(killGracePeriod):
			return fmt.Errorf("command %s", reason), false
		}
	}
}

// ExecuteJobForTest starts a detached bash job for cross-package tests.
func ExecuteJobForTest(ctx context.Context, command, description string, timeoutSec *int) (string, error) {
	timeout := 0
	if timeoutSec != nil && *timeoutSec > 0 {
		timeout = *timeoutSec
	}
	j, err := globalJobRegistry.start(ctx, jobStartRequest{
		Command:     command,
		Description: description,
		TimeoutSec:  timeout,
		Detached:    true,
	})
	if err != nil {
		return "", err
	}
	return j.ID, nil
}

func ResetJobRegistryForTest() func() {
	old := globalJobRegistry
	globalJobRegistry = &JobRegistry{jobs: make(map[string]*job)}
	return func() {
		globalJobRegistry = old
	}
}
