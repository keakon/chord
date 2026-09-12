package tools

import (
	"context"
	"fmt"
	"io"
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
	Command       string
	Description   string
	LogFile       string
	StartedAt     time.Time
	MaxRuntimeSec int

	cmdMu     sync.Mutex
	cmd       *exec.Cmd
	cancelCh  chan string
	startedCh chan struct{}
	logWriter *rotatingJobLog
	output    *tailWriter
	done      chan struct{}

	mu              sync.Mutex
	status          jobStatus
	detail          string
	exitErr         error
	finished        bool
	finishedAt      time.Time
	detached        bool
	reported        bool
	readOffset      int64
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

func (r *JobRegistry) start(ctx context.Context, req jobStartRequest) (*job, error) {
	// The sweep does directory I/O outside r.mu so it cannot block concurrent
	// job_output / job_list calls.
	r.maybePruneJobLogs(req.LogDir)
	r.mu.Lock()
	r.evictFinishedLocked()
	if len(r.jobs) >= maxJobs {
		r.mu.Unlock()
		return nil, fmt.Errorf("maximum number of background jobs (%d) reached; wait for or kill a job first", maxJobs)
	}
	id := fmt.Sprintf("job-%d", r.seq.Add(1))
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
		Command:     req.Command,
		Description: req.Description,
		StartedAt:   time.Now(),
		status:      jobStatusRunning,
		detached:    req.Detached,
		cancelCh:    make(chan string, 1),
		startedCh:   make(chan struct{}),
		done:        make(chan struct{}),
	}
	j.MaxRuntimeSec = req.TimeoutSec
	st := shell.ParseShellType(req.ShellType)
	binary, args := shell.GetShellCommand(st, req.Command)
	cmd := exec.Command(binary, args...)
	_, _ = configureCommandProcessGroup(cmd)
	if req.Workdir != "" {
		cmd.Dir = req.Workdir
	}
	// Jobs are intentionally non-interactive. Leaving Stdin nil makes Go
	// connect the child process to the null device instead of the TUI stdin.
	cmd.Env = appendNonInteractiveEnv(nil)
	outputBuf := newTailWriter(maxOutputBytes)
	j.output = outputBuf
	if req.LogDir != "" {
		logPath := filepath.Join(req.LogDir, id+".log")
		sessionDir := filepath.Dir(req.LogDir)
		writer, err := openRotatingJobLog(sessionDir, logPath)
		if err != nil {
			r.mu.Unlock()
			return nil, fmt.Errorf("creating log file: %w", err)
		}
		j.LogFile = logPath
		j.logWriter = writer
		cmd.Stdout = io.MultiWriter(writer, outputBuf)
		cmd.Stderr = io.MultiWriter(writer, outputBuf)
	} else {
		cmd.Stdout = outputBuf
		cmd.Stderr = outputBuf
	}
	j.cmdMu.Lock()
	j.cmd = cmd
	j.cmdMu.Unlock()
	r.jobs[id] = j
	r.mu.Unlock()

	go r.run(ctx, j)
	log.Debugf("job started id=%v command=%v max_runtime_sec=%v detached=%v agent_id=%v", id, req.Command, j.MaxRuntimeSec, req.Detached, j.AgentID)
	return j, nil
}

func (r *JobRegistry) run(ctx context.Context, j *job) {
	j.cmdMu.Lock()
	cmd := j.cmd
	j.cmdMu.Unlock()
	if cmd == nil {
		close(j.startedCh)
		r.finish(ctx, j, jobStatusFailed, "missing process handle", fmt.Errorf("starting command: missing process handle"))
		return
	}
	if err := cmd.Start(); err != nil {
		// Unblock anyone waiting for the process to exist: the job is already
		// terminal, and startedCh only gates "a process handle is available",
		// which a start failure answers just as definitively.
		close(j.startedCh)
		r.finish(ctx, j, jobStatusFailed, "failed to start", fmt.Errorf("starting command: %w", err))
		return
	}
	close(j.startedCh)

	waitCh := waitForCommand(cmd, j.done)
	var (
		status jobStatus
		detail string
		err    error
	)
	handleExit := func(rawErr error) {
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
			status, detail, err = jobStatusKilled, reason, terminateJobProcessGroup(cmd, reason, waitCh)
		case rawErr := <-waitCh:
			handleExit(rawErr)
		}
	} else {
		timer := time.NewTimer(time.Duration(j.MaxRuntimeSec) * time.Second)
		defer timer.Stop()
		select {
		case reason := <-j.cancelCh:
			status, detail, err = jobStatusKilled, reason, terminateJobProcessGroup(cmd, reason, waitCh)
		case rawErr := <-waitCh:
			handleExit(rawErr)
		case <-timer.C:
			reason := fmt.Sprintf("timed out after %ds", j.MaxRuntimeSec)
			status, detail, err = jobStatusKilled, reason, terminateJobProcessGroup(cmd, reason, waitCh)
			// A timeout otherwise invites an unchanged, equally slow re-run: the
			// model cannot see the deadline it hit, so steer the next step.
			err = fmt.Errorf("%w\n%s", err, shellTimeoutGuidance)
		}
	}
	r.finish(ctx, j, status, detail, err)
}

// finish records the terminal state, wakes any waiter, and delivers the
// completion notification when the job completed while detached. Detaching and
// finishing share j.mu, so a job cannot both complete as foreground and notify.
func (r *JobRegistry) finish(ctx context.Context, j *job, status jobStatus, detail string, exitErr error) {
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
	j.mu.Unlock()
	close(j.done)
	if j.logWriter != nil {
		_ = j.logWriter.Close()
	}
	if !notify {
		return
	}
	sender := EventSenderFromContext(ctx)
	if sender == nil {
		// A detached job that finished without a sender would silently lose its
		// result, and the model would wait forever on a notification that can
		// never arrive. Log it so the missing wiring is diagnosable.
		log.Warnf("job finished with no event sender id=%v agent_id=%v status=%v", j.ID, j.AgentID, status)
		return
	}
	statusText := j.statusText()
	sender.SendAgentEvent(
		"background_object_finished",
		j.AgentID,
		&JobFinishedPayload{
			BackgroundID:  j.ID,
			AgentID:       j.AgentID,
			SessionDir:    j.SessionDir,
			Status:        statusText,
			Command:       j.Command,
			Description:   j.Description,
			MaxRuntimeSec: j.MaxRuntimeSec,
			Message:       j.completionMessage(status, statusText),
			LogFile:       j.LogFile,
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
	j.mu.Lock()
	if j.finished {
		j.mu.Unlock()
		return false
	}
	if j.status == jobStatusRunning {
		j.status = jobStatusStopping
	}
	j.detached = notify
	j.mu.Unlock()
	select {
	case j.cancelCh <- reason:
	default:
	}
	return true
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
	return JobState{
		ID:            j.ID,
		AgentID:       j.AgentID,
		Description:   j.Description,
		Command:       j.Command,
		LogFile:       j.LogFile,
		StartedAt:     j.StartedAt,
		MaxRuntimeSec: j.MaxRuntimeSec,
		Status:        string(j.status),
		Detail:        j.detail,
		FinishedAt:    j.finishedAt,
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
	if exitErr, ok := err.(*exec.ExitError); ok {
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
	msg := fmt.Sprintf("[Background job %s finished]\n\nStatus: %s", j.ID, statusText)
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

// readIncremental returns output produced since the previous read, and how many
// bytes the reader missed because they had already fallen out of the window.
// The cursor read and commit share one critical section: job_output advertises
// itself as concurrency-safe, so two batched reads of the same job must each
// claim a disjoint window instead of replaying or dropping one.
func (j *job) readIncremental() (string, int64) {
	j.mu.Lock()
	defer j.mu.Unlock()
	chunk, next, dropped := j.output.readFrom(j.readOffset)
	j.readOffset = next
	return chunk, dropped
}

// noteReadOutcome increments the consecutive no-new-output streak and returns
// it. Callers pass false for a read that produced bytes, and for blocking waits
// (output/exit), which are legitimate renewals rather than polling.
func (j *job) noteReadOutcome(noNewBytes bool) int {
	j.mu.Lock()
	defer j.mu.Unlock()
	if noNewBytes {
		j.noProgressReads++
	} else {
		j.noProgressReads = 0
	}
	return j.noProgressReads
}

// hasUnreadOutput reports whether the retained window holds bytes this reader
// has not consumed yet.
func (j *job) hasUnreadOutput() bool {
	j.mu.Lock()
	cursor := j.readOffset
	j.mu.Unlock()
	return j.output.hasDataAfter(cursor)
}

func shortExitDetail(err error) string {
	if err == nil {
		return ""
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		if name := exitSignalName(exitErr); name != "" {
			return "signal: " + name
		}
		return fmt.Sprintf("exit code %d", exitErr.ExitCode())
	}
	return err.Error()
}

func waitForCommand(cmd *exec.Cmd, done <-chan struct{}) <-chan error {
	waitCh := make(chan error, 1)
	go func() {
		err := cmd.Wait()
		select {
		case waitCh <- err:
		case <-done:
		}
	}()
	return waitCh
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

type JobState struct {
	ID            string
	AgentID       string
	Description   string
	Command       string
	LogFile       string
	StartedAt     time.Time
	MaxRuntimeSec int
	Status        string
	Detail        string
	FinishedAt    time.Time
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

func terminateJobProcessGroup(cmd *exec.Cmd, reason string, doneCh <-chan error) error {
	if cmd == nil || cmd.Process == nil {
		return fmt.Errorf("command %s", reason)
	}
	_ = terminateCommandProcessGroup(cmd)
	select {
	case err := <-doneCh:
		if err != nil {
			return fmt.Errorf("command %s: %w", reason, err)
		}
		return fmt.Errorf("command %s", reason)
	case <-time.After(killGracePeriod):
		_ = forceTerminateCommandProcessGroup(cmd)
		select {
		case err := <-doneCh:
			if err != nil {
				return fmt.Errorf("command %s: %w", reason, err)
			}
			return fmt.Errorf("command %s", reason)
		case <-time.After(killGracePeriod):
			return fmt.Errorf("command %s", reason)
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
