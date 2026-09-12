package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// JobOutputWaitMs bounds how long one job_output call may block the turn. The
// wait is owned by the runtime rather than the model: a long in-call block is
// exactly what background promotion exists to avoid, and the completion
// notification is the primary way a result reaches the model. It is a var so
// tests can shrink it instead of waiting out the real budget.
var jobOutputWaitMs = 30_000

// jobOutputWaitSeconds is the second-granularity wait cap shown in the tool
// description, derived from jobOutputWaitMs so the two cannot drift.
func jobOutputWaitSeconds() int { return jobOutputWaitMs / 1000 }

// JobOutputTool reads output from a background job started by shell.
type JobOutputTool struct{}

const (
	jobWaitNone   = "none"
	jobWaitOutput = "output"
	jobWaitExit   = "exit"
)

type jobOutputArgs struct {
	JobID string `json:"job_id"`
	Wait  string `json:"wait,omitempty"`
}

func (JobOutputTool) Name() string { return NameJobOutput }

func (JobOutputTool) IsReadOnly() bool { return true }

func (JobOutputTool) ConcurrencySafeReadOnly(json.RawMessage) bool { return true }

func (JobOutputTool) Description() string {
	return "Read output from a background job started by shell (including a command that exceeded the foreground budget).\n" +
		"Returns the output produced since the previous read (or since the job started), then a final `[status: ...]` line.\n" +
		fmt.Sprintf("`wait` selects whether the call blocks: `none` (default) returns whatever is available now, `output` waits until the job writes more output, and `exit` waits until it reaches a terminal state. Every wait is capped at %ds by the runtime; a wait that expires is not an error — the job keeps running, the reply still carries whatever output was produced, and the status line reads `[status: running]`.\n", jobOutputWaitSeconds()) +
		"Prefer `wait: none` and end your turn when you are not blocked on the result: you are notified when the job finishes, and polling wastes calls. Use `wait: exit` only when you genuinely need the result now.\n" +
		"Repeated non-blocking reads that find no new output are flagged as polling and then rejected."
}

func (JobOutputTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"job_id": map[string]any{
				"type":        "string",
				"description": "The job id returned by shell or job_list.",
			},
			"wait": map[string]any{
				"type":        "string",
				"enum":        []string{jobWaitNone, jobWaitOutput, jobWaitExit},
				"description": fmt.Sprintf("Whether to block: `none` (default) returns immediately, `output` waits for the next output, `exit` waits for the job to finish. Each wait is capped at %ds by the runtime.", jobOutputWaitSeconds()),
			},
		},
		"required":             []string{"job_id"},
		"additionalProperties": false,
	}
}

func (JobOutputTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var a jobOutputArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	id := strings.TrimSpace(a.JobID)
	if id == "" {
		return "", fmt.Errorf("job_id is required")
	}
	wait := strings.ToLower(strings.TrimSpace(a.Wait))
	if wait == "" {
		wait = jobWaitNone
	}
	switch wait {
	case jobWaitNone, jobWaitOutput, jobWaitExit:
	default:
		return "", fmt.Errorf("wait must be one of %q, %q, or %q", jobWaitNone, jobWaitOutput, jobWaitExit)
	}
	j, ok := globalJobRegistry.get(id)
	if !ok {
		return "", fmt.Errorf("job %s not found", id)
	}
	if !j.accessibleFrom(ctx) {
		return "", fmt.Errorf("job %s is not accessible to this agent", id)
	}
	if wait == jobWaitExit {
		if !j.isFinished() {
			waitForJob(ctx, j, false)
		}
	} else if wait == jobWaitOutput && !j.isFinished() {
		// Drop a token left by an earlier write: without this, a stale signal
		// would return immediately on every call. Output that arrived before
		// the drain is still reported by the re-check below.
		select {
		case <-j.output.writeSignal():
		default:
		}
		if !j.hasUnreadOutput() {
			waitForJob(ctx, j, true)
		}
	}
	chunk, dropped := j.readIncremental()
	noNewBytes := chunk == "" && dropped == 0
	if wait != jobWaitNone {
		// A blocking wait that expired is a renewal, not polling.
		noNewBytes = false
	}
	streak := j.noteReadOutcome(noNewBytes)
	if streak >= jobOutputPollRefuseStreak {
		// Returning this as a tool error (not a successful result) makes the
		// refusal visible as an error terminal state instead of a card that
		// looks like an ordinary successful read.
		return "", errors.New(jobOutputPollRefusal(j, streak))
	}
	if j.isFinished() {
		// The caller has seen the terminal result, so the completion
		// notification would be a duplicate. Claim it through the same one-shot
		// state machine the event uses; if the event claimed it first the
		// explicit read still returns the content the caller asked for.
		globalJobRegistry.claimReported(j.ID)
	}
	note := ""
	if streak >= jobOutputPollWarnStreak {
		note = jobOutputPollNotice(streak)
	}
	return renderJobOutput(j, chunk, dropped, note), nil
}

// waitForJob blocks until the job finishes, the requested event arrives, the
// runtime wait budget expires, or the caller cancels. untilOutput additionally
// wakes on new output; the exit wait ignores output so it cannot return early
// with a job that is still running.
func waitForJob(ctx context.Context, j *job, untilOutput bool) {
	timer := time.NewTimer(time.Duration(jobOutputWaitMs) * time.Millisecond)
	defer timer.Stop()
	signal := j.output.writeSignal()
	if untilOutput {
		select {
		case <-j.done:
		case <-signal:
		case <-timer.C:
		case <-ctx.Done():
			// Cancellation must still report what the job produced.
		}
		return
	}
	select {
	case <-j.done:
	case <-timer.C:
	case <-ctx.Done():
		// Cancellation must still report what the job produced.
	}
}

// jobOutputPollWarnStreak is how many consecutive non-blocking reads with no
// new output earn a "stop polling" notice, and jobOutputPollRefuseStreak is
// where the call is refused outright. A blocking wait resets the streak, so
// repeated wait:output / wait:exit renewals are never mistaken for polling.
const (
	jobOutputPollWarnStreak   = 2
	jobOutputPollRefuseStreak = 3
)

func jobOutputPollNotice(streak int) string {
	return fmt.Sprintf("[notice] no new output across %d consecutive non-blocking reads; polling job_output will not produce a result. Do other work or end the turn — the completion notification wakes you — or block with wait: output / wait: exit.", streak)
}

func jobOutputPollRefusal(j *job, streak int) string {
	return fmt.Sprintf("Tool call rejected automatically: job %s produced no new output across %d consecutive non-blocking job_output reads. Stop polling: do other work or end the turn and let the completion notification wake you, or block with wait: output / wait: exit.\n[status: %s]", j.ID, streak, j.statusText())
}

// renderJobOutput formats one model-facing read: the cleaned chunk, the
// dropped-bytes notice, an optional anti-polling note, then the status line.
func renderJobOutput(j *job, chunk string, dropped int64, note string) string {
	chunk = cleanJobOutputText(chunk)
	var sb strings.Builder
	if chunk != "" {
		sb.WriteString(chunk)
		if !strings.HasSuffix(chunk, "\n") {
			sb.WriteString("\n")
		}
	}
	if dropped > 0 {
		if j.LogFile != "" {
			fmt.Fprintf(&sb, "(skipped %d bytes of earlier output; recent log: %s)\n", dropped, j.LogFile)
		} else {
			fmt.Fprintf(&sb, "(skipped %d bytes of earlier output)\n", dropped)
		}
	}
	if note != "" {
		sb.WriteString(note)
		sb.WriteString("\n")
	}
	fmt.Fprintf(&sb, "[status: %s]", j.statusText())
	return sb.String()
}

// JobListTool lists the background jobs the caller may act on.
type JobListTool struct{}

func (JobListTool) Name() string { return NameJobList }

func (JobListTool) IsReadOnly() bool { return true }

func (JobListTool) ConcurrencySafeReadOnly(json.RawMessage) bool { return true }

func (JobListTool) Description() string {
	return "List the background jobs you can read or stop (id, status, elapsed, label), including jobs started by the main agent and by your direct owner. Use it to see what is still running before deciding to wait, to do other work, or to end your turn."
}

func (JobListTool) Parameters() map[string]any {
	return map[string]any{
		"type":                 "object",
		"properties":           map[string]any{},
		"additionalProperties": false,
	}
}

func (JobListTool) Execute(ctx context.Context, _ json.RawMessage) (string, error) {
	states := SnapshotJobs()
	now := time.Now()
	var sb strings.Builder
	shown := 0
	for _, state := range states {
		// The list must show exactly the jobs the caller may act on: it filters
		// by the same predicate job_output and job_kill enforce per id, so the
		// model never sees a job it cannot read and never misses one it can. An
		// unowned job or a caller with no agent id is denied rather than shown.
		if !jobOwnerAccessibleFrom(ctx, state.AgentID) {
			continue
		}
		if shown > 0 {
			sb.WriteString("\n")
		}
		shown++
		elapsed := state.FinishedAt.Sub(state.StartedAt)
		if state.FinishedAt.IsZero() {
			elapsed = now.Sub(state.StartedAt)
		}
		label := state.Description
		if label == "" {
			label = state.Command
		}
		fmt.Fprintf(&sb, "%s  %s  %s  %s", state.ID, state.Status, formatJobElapsed(elapsed), label)
	}
	if shown == 0 {
		return "no background jobs", nil
	}
	return sb.String(), nil
}

func formatJobElapsed(d time.Duration) string {
	if d < time.Second {
		return "0s"
	}
	seconds := int(d.Seconds())
	if seconds < 60 {
		return fmt.Sprintf("%ds", seconds)
	}
	return fmt.Sprintf("%dm%02ds", seconds/60, seconds%60)
}

// JobKillTool stops a background job started by shell.
type JobKillTool struct{}

type jobKillArgs struct {
	JobID  string `json:"job_id"`
	Reason string `json:"reason,omitempty"`
}

func (JobKillTool) Name() string { return NameJobKill }

func (JobKillTool) IsReadOnly() bool { return false }

func (JobKillTool) Description() string {
	return "Stop a background job. Sends SIGTERM, then SIGKILL after a grace period. Use it for jobs that no longer matter or are clearly stuck; the kill does not produce a completion notification, so check job_output if you need the final output."
}

func (JobKillTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"job_id": map[string]any{
				"type":        "string",
				"description": "The job id returned by shell or job_list.",
			},
			"reason": map[string]any{
				"type":        "string",
				"description": "Why the job is being stopped (optional; recorded on the job status).",
			},
		},
		"required":             []string{"job_id"},
		"additionalProperties": false,
	}
}

func (JobKillTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var a jobKillArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	id := strings.TrimSpace(a.JobID)
	if id == "" {
		return "", fmt.Errorf("job_id is required")
	}
	reason := strings.TrimSpace(a.Reason)
	if reason == "" {
		reason = "cancelled by job_kill"
	}
	j, ok := globalJobRegistry.get(id)
	if !ok {
		return "", fmt.Errorf("job %s not found", id)
	}
	if !j.accessibleFrom(ctx) {
		return "", fmt.Errorf("job %s is not accessible to this agent", id)
	}
	if j.isFinished() {
		return fmt.Sprintf("job %s already finished\n[status: %s]", id, j.statusText()), nil
	}
	if !globalJobRegistry.kill(id, reason) {
		return fmt.Sprintf("job %s already finished\n[status: %s]", id, j.statusText()), nil
	}
	return fmt.Sprintf("job %s stopping (%s)\n[status: stopping]", id, reason), nil
}
