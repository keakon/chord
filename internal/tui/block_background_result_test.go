package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/mattn/go-runewidth"

	"github.com/keakon/chord/internal/tools"
)

func TestParseBackgroundResultKeepsElapsedAndQuietOutOfResidual(t *testing.T) {
	raw := "[Background job job-1 finished]\n\n" +
		"Status: completed (exit code 0)\n" +
		"Elapsed: 1m35s\n" +
		"Quiet: 5s\n" +
		"Purpose: build it\n" +
		"Command: make build\n" +
		"Relevant output:\nok"
	parsed := parseBackgroundResult(raw)
	if parsed.elapsed != "1m35s" {
		t.Fatalf("parsed.elapsed = %q, want 1m35s", parsed.elapsed)
	}
	if len(parsed.residual) != 0 {
		t.Fatalf("timing fields leaked into residual: %v", parsed.residual)
	}

	formatted, _ := formatSingleBackgroundResult(raw, "", "", "", "")
	if !strings.Contains(formatted, elapsedGlyph+" 1m35s") {
		t.Fatalf("formatted card dropped the elapsed field:\n%s", formatted)
	}
	// Quiet measures how long a *running* job has been silent; a finished card
	// gets nothing from it, so the parser consumes the line and the renderer
	// never sees it.
	if strings.Contains(formatted, "quiet") {
		t.Fatalf("formatted card kept the quiet field:\n%s", formatted)
	}
}

// A folded headline reserves room for the elapsed tail before it is laid out,
// so what it reserves has to be the width of the very string the renderer
// appends. Both come from one spelling of that tail; this pins the pair so a
// change to the format cannot leave the reservation measuring something else.
func TestBackgroundResultElapsedSuffixWidthMatchesTheAppendedSuffix(t *testing.T) {
	const elapsed = "17s"
	want := " · " + elapsedGlyph + " " + elapsed
	rendered := stripANSI(appendToolElapsedSuffix("✓ ▸ job-1", elapsed, 80))
	if !strings.HasSuffix(rendered, want) {
		t.Fatalf("appended suffix = %q, want it to end with %q", rendered, want)
	}
	if got := backgroundResultElapsedSuffixWidth(elapsed); got != runewidth.StringWidth(want) {
		t.Fatalf("reserved width = %d, want %d (the width of the tail the renderer appends)", got, runewidth.StringWidth(want))
	}
}

func TestJobOutputSummaryLineIgnoresWaitOutcomeNotice(t *testing.T) {
	result := "[notice] wait: exit timed out after 30s; job job-1 is still running; no output for 30s. End the turn and await the completion notification instead of polling.\n[status: running]"
	if got := jobOutputSummaryLine(result); got != "no new output" {
		t.Fatalf("jobOutputSummaryLine = %q, want the timeout notice to stay meta output", got)
	}
}

func TestOverlayRowShowsQuietDuration(t *testing.T) {
	m := newJobsTestModel(t, 160, 40)
	started := time.Now().Add(-3 * time.Minute)
	m.jobsSnapshot = []tools.JobState{{
		ID:           "job-1",
		Description:  "quiet runner",
		Status:       jobStatusRunning,
		StartedAt:    started,
		LastOutputAt: started.Add(30 * time.Second),
	}}
	m.activeJobsSnapshot = []tools.JobState{m.jobsSnapshot[0]}
	m.jobsSnapshotValid = true
	m.jobsSnapshotFrame = m.renderFrameGeneration
	m.openJobsOverlay()

	plain := stripANSI(m.renderJobsOverlayDialog())
	if !strings.Contains(plain, "quiet 2m30s") {
		t.Fatalf("overlay row missing the quiet duration:\n%s", plain)
	}

	// The info panel reserves the row for elapsed and the stop affordance; it
	// intentionally keeps the quiet field out so a long-running job cannot widen
	// it past the recorded stop zone.
	panel := stripANSI(strings.Join(jobsSectionRawLines(m, 32, 60), "\n"))
	if strings.Contains(panel, "quiet 2m30s") {
		t.Fatalf("info panel must not duplicate the quiet field:\n%s", panel)
	}
	if !strings.Contains(panel, "3m00s") {
		t.Fatalf("info panel lost the elapsed field:\n%s", panel)
	}
}

func TestRenderBackgroundResultSanitizesControlSequences(t *testing.T) {
	ApplyTheme(DefaultTheme())
	block := &Block{
		Type:        BlockStatus,
		StatusTitle: backgroundResultCardTitle,
		Content:     "✓ job\nStatus: completed\nRelevant output:\nvalue \x1b[1;1H\x00\x9b31m",
	}
	raw := strings.Join(block.Render(100, ""), "\n")
	if strings.Contains(raw, "\x1b[1;1H") || strings.ContainsRune(raw, '\x00') || strings.ContainsRune(raw, '\x9b') {
		t.Fatalf("background result leaked raw control sequence: %q", raw)
	}
	plain := stripANSI(raw)
	for _, want := range []string{`\x1b[1;1H`, `\x00`, `\x9b31m`} {
		if !strings.Contains(plain, want) {
			t.Fatalf("background result missing escaped %s in %q", want, plain)
		}
	}
}
