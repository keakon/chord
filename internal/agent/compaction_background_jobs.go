package agent

import (
	"fmt"
	"strings"
	"time"

	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/recovery"
	"github.com/keakon/chord/internal/tools"
)

// Active background job snapshot.
//
// A checkpoint that replaces the transcript must not erase the fact that
// commands are still running: after the reset the model cannot see the job
// handles it started, so a session that had live jobs would otherwise look
// idle while the jobs keep consuming resources. The snapshot is deterministic
// runtime-owned content, appended the same way the todo and sub-agent
// snapshots are, so no summarizer wording can drop it.
//
// It is rendered as an HTML comment rather than a `## ` section on purpose:
// the checkpoint parsers split sections on ATX headings
// (markdownSectionBounds, compactionHeadingSection, the anchor/claim readers),
// and a machine block must never be mistaken for model-authored prose or
// shadow a real section. Only active jobs appear — a finished job is reported
// by its own completion notification, and the retention cap would otherwise
// let a long session push live jobs out of the checkpoint with dead ones.

const (
	// activeJobsSnapshotOpenPrefix starts the block and carries the instant
	// the listed states were captured, so the elapsed and deadline numbers
	// below can never be misread as live values after the reset.
	activeJobsSnapshotOpenPrefix = "<!-- Active background jobs (snapshot_at="
	activeJobsSnapshotCloseTag   = "-->"

	// activeJobsSnapshotMaxItems bounds the rows. Like the todo snapshot cap it
	// binds unconditionally: a session that accumulated many live jobs is
	// exactly the case where an unbounded section would squeeze out the rest of
	// the checkpoint.
	activeJobsSnapshotMaxItems = 10
	// activeJobsSnapshotMaxChars bounds the rendered rows, excluding the label
	// line and the closing tag.
	activeJobsSnapshotMaxChars = 1200
	// activeJobsSnapshotItemChars bounds one row's free-text description.
	activeJobsSnapshotItemChars = 160
)

// ensureActiveBackgroundJobSnapshot replaces any existing job snapshot block
// with one rendered from the frozen states. It is idempotent, and a snapshot
// with no active jobs removes the block entirely: a stale block would read as
// the live job list after the reset.
func ensureActiveBackgroundJobSnapshot(summary string, jobs []recovery.BackgroundObjectState, snapshotAt time.Time) string {
	summary = stripActiveBackgroundJobSnapshotBlock(summary)
	block := renderActiveBackgroundJobsSnapshot(jobs, snapshotAt)
	if block == "" {
		return summary
	}
	if summary == "" {
		return block
	}
	return summary + "\n\n" + block
}

// stripActiveBackgroundJobSnapshotBlock removes the job snapshot block from a
// checkpoint body, so carrying a prior checkpoint forward (or re-ensuring the
// block) cannot leave two copies of the same block, one of them stale.
func stripActiveBackgroundJobSnapshotBlock(body string) string {
	start := strings.Index(body, activeJobsSnapshotOpenPrefix)
	if start < 0 {
		return strings.TrimSpace(body)
	}
	end := strings.Index(body[start:], activeJobsSnapshotCloseTag)
	if end < 0 {
		return strings.TrimSpace(body)
	}
	end += start + len(activeJobsSnapshotCloseTag)
	prefix := strings.TrimSpace(body[:start])
	rest := strings.TrimSpace(body[end:])
	switch {
	case prefix == "":
		return rest
	case rest == "":
		return prefix
	default:
		return prefix + "\n\n" + rest
	}
}

// renderActiveBackgroundJobsSnapshot renders the block, or "" when nothing is
// active.
func renderActiveBackgroundJobsSnapshot(jobs []recovery.BackgroundObjectState, snapshotAt time.Time) string {
	active := make([]recovery.BackgroundObjectState, 0, len(jobs))
	for _, job := range jobs {
		if (tools.JobState{Status: job.Status}).Active() {
			active = append(active, job)
		}
	}
	if len(active) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString(activeJobsSnapshotOpenPrefix)
	sb.WriteString(snapshotAt.UTC().Format(time.RFC3339))
	sb.WriteString(")\n")
	rendered := 0
	used := 0
	for _, job := range active {
		if rendered >= activeJobsSnapshotMaxItems {
			break
		}
		row := activeBackgroundJobSnapshotRow(job, snapshotAt)
		if rendered > 0 && used+1+len(row) > activeJobsSnapshotMaxChars {
			break
		}
		if rendered > 0 {
			sb.WriteByte('\n')
			used++
		}
		sb.WriteString(row)
		used += len(row)
		rendered++
	}
	if omitted := len(active) - rendered; omitted > 0 {
		fmt.Fprintf(&sb, "\n- (%d more active background jobs not shown; read job_list for the live view)", omitted)
	}
	sb.WriteByte('\n')
	sb.WriteString(activeJobsSnapshotCloseTag)
	return sb.String()
}

// activeBackgroundJobSnapshotRow renders one job on a single line. The keys
// mirror the summarizer prompt's background-objects section so a reader can
// match a row to the objects the model was shown.
func activeBackgroundJobSnapshotRow(job recovery.BackgroundObjectState, snapshotAt time.Time) string {
	state := tools.JobState{
		Status:        job.Status,
		StartedAt:     job.StartedAt,
		MaxRuntimeSec: job.MaxRuntimeSec,
		FinishedAt:    job.FinishedAt,
	}
	parts := []string{
		job.ID,
		"agent=" + backgroundObjectPromptAgent(job.AgentID),
		"status=" + job.Status,
		"elapsed=" + tools.FormatElapsed(state.Elapsed(snapshotAt)),
	}
	if deadline := state.DeadlineAt(); !deadline.IsZero() {
		if remaining := deadline.Sub(snapshotAt); remaining > 0 {
			parts = append(parts, "deadline="+tools.FormatElapsed(remaining)+" left")
		} else {
			parts = append(parts, "deadline=overdue")
		}
	}
	parts = append(parts, "desc="+singleLineSnapshotText(backgroundObjectPromptDescription(job.Description, job.Command), activeJobsSnapshotItemChars))
	return "- " + strings.Join(parts, " | ")
}

// singleLineSnapshotText collapses all whitespace and truncates, so one row can
// never wrap onto a second physical line in the machine block.
func singleLineSnapshotText(s string, maxChars int) string {
	return llm.TruncateStringRunes(strings.Join(strings.Fields(s), " "), maxChars, "...(truncated)")
}
