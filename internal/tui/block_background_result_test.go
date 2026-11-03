package tui

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mattn/go-runewidth"

	"github.com/keakon/chord/internal/tools"
	"github.com/keakon/chord/internal/tui/terminaltext"
	uitheme "github.com/keakon/chord/internal/tui/theme"
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

	formatted := formatBackgroundResultCard(raw)
	if !strings.Contains(formatted.content, elapsedGlyph+" 1m35s") {
		t.Fatalf("formatted card dropped the elapsed field:\n%s", formatted.content)
	}
	if len(formatted.headlineLines) != 1 || formatted.headlineLines[0] != 0 {
		t.Fatalf("headline lines = %v, want [0]", formatted.headlineLines)
	}
	// Quiet measures how long a *running* job has been silent; a finished card
	// gets nothing from it, so the parser consumes the line and the renderer
	// never sees it.
	if strings.Contains(formatted.content, "quiet") {
		t.Fatalf("formatted card kept the quiet field:\n%s", formatted.content)
	}
}

// A folded headline reserves room for the elapsed tail before it is laid out,
// so what it reserves has to be the width of the very string the renderer
// appends. Both come from one spelling of that tail; this pins the pair so a
// change to the format cannot leave the reservation measuring something else.
func TestBackgroundResultElapsedSuffixWidthMatchesTheAppendedSuffix(t *testing.T) {
	const elapsed = "17s"
	want := " · " + elapsedGlyph + " " + elapsed
	rendered := terminaltext.StripANSI(appendToolElapsedSuffix("✓ ▸ job-1", elapsed, 80))
	if !strings.HasSuffix(rendered, want) {
		t.Fatalf("appended suffix = %q, want it to end with %q", rendered, want)
	}
	if got := backgroundResultElapsedSuffixWidth(elapsed); got != runewidth.StringWidth(want) {
		t.Fatalf("reserved width = %d, want %d (the width of the tail the renderer appends)", got, runewidth.StringWidth(want))
	}
}

func TestJobOutputSummaryLineIgnoresWaitOutcomeNotice(t *testing.T) {
	result := "[notice] wait: exit timed out after 30s; job job-1 is still running; no output 30s. End the turn and await the completion notification instead of polling.\n[status: running]"
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

	plain := terminaltext.StripANSI(m.renderJobsOverlayDialog())
	if !strings.Contains(plain, "quiet 2m30s") {
		t.Fatalf("overlay row missing the quiet duration:\n%s", plain)
	}

	// The info panel reserves the row for elapsed and the stop affordance; it
	// intentionally keeps the quiet field out so a long-running job cannot widen
	// it past the recorded stop zone.
	panel := terminaltext.StripANSI(strings.Join(jobsSectionRawLines(m, 32, 60), "\n"))
	if strings.Contains(panel, "quiet 2m30s") {
		t.Fatalf("info panel must not duplicate the quiet field:\n%s", panel)
	}
	if !strings.Contains(panel, "3m00s") {
		t.Fatalf("info panel lost the elapsed field:\n%s", panel)
	}
}

func TestRenderBackgroundResultSanitizesControlSequences(t *testing.T) {
	ApplyTheme(uitheme.Default())
	block := &Block{
		Type:                    BlockStatus,
		StatusTitle:             backgroundResultCardTitle,
		Content:                 "✓ job\nStatus: completed\nRelevant output:\nvalue \x1b[1;1H\x00\x9b31m",
		BackgroundHeadlineLines: []int{0},
	}
	raw := strings.Join(block.Render(100, ""), "\n")
	if strings.Contains(raw, "\x1b[1;1H") || strings.ContainsRune(raw, '\x00') || strings.ContainsRune(raw, '\x9b') {
		t.Fatalf("background result leaked raw control sequence: %q", raw)
	}
	plain := terminaltext.StripANSI(raw)
	for _, want := range []string{`\x1b[1;1H`, `\x00`, `\x9b31m`} {
		if !strings.Contains(plain, want) {
			t.Fatalf("background result missing escaped %s in %q", want, plain)
		}
	}
}

// Each job section body opens with its headline, so the recorded headline
// lines are the section starts: line 0 and, after section one's body plus the
// blank join line, the offset of the next section's first line.
func TestFormatBackgroundResultCardRecordsMultiJobHeadlineLines(t *testing.T) {
	raw := "[Background job job-1 finished]\n\n" +
		"Status: completed (exit code 0)\n" +
		"Purpose: first\n\n" +
		"Relevant output:\n" +
		"line one\n" +
		"line two\n" +
		"[Background job job-2 finished]\n\n" +
		"Status: failed (exit status 7)\n" +
		"Purpose: second"
	formatted := formatBackgroundResultCard(raw)
	// Section one renders as headline, status line, the output section header
	// and two output lines; the join adds one blank line before job-2.
	if got, want := formatted.headlineLines, []int{0, 6}; !slices.Equal(got, want) {
		t.Fatalf("headline lines = %v, want %v", got, want)
	}
	lines := strings.Split(formatted.content, "\n")
	for _, idx := range formatted.headlineLines {
		if !strings.HasPrefix(lines[idx], "✓") && !strings.HasPrefix(lines[idx], "✗") {
			t.Fatalf("line %d = %q, want a glyph headline", idx, lines[idx])
		}
	}
}

// Build tools log their own progress marks: a docs site build opens its
// completion lines with the same ✓ a job headline uses. A folded card folds to
// the job's headline alone — output lines must not be promoted into extra
// checkmark rows, and the unmarked output context stays hidden with the rest
// of the output.
func TestBackgroundResultFoldedKeepsOutputCheckmarksOutOfTheHeadlines(t *testing.T) {
	m := NewModelWithSize(nil, 120, 30)
	raw := "[Background job job-25 finished]\n\n" +
		"Status: completed (exit code 0)\n" +
		"Elapsed: 5s\n" +
		"Purpose: build the bilingual docs\n" +
		"Command: npm ci && npm run build\n\n" +
		"Relevant output:\n" +
		"npm warn allow-scripts fsevents@2.3.3\n" +
		"> vitepress build\n" +
		"- building client + server bundles...\n" +
		"✓ building client + server bundles...\n" +
		"- rendering pages...\n" +
		"✓ rendering pages...\n" +
		"build complete in 1.51s."

	_ = m.handleAgentEvent(agentEventMsg{event: backgroundResultAppended("", "mb-fold-check-output", raw)})
	block, ok := m.viewport.FindStatusBlockByBackgroundObject("mb-fold-check-output")
	if !ok {
		t.Fatal("expected background result block")
	}
	rows := foldedBackgroundResultRows(t, block, 90)
	if len(rows) != 1 {
		t.Fatalf("folded rows = %d (%v), want the job headline alone", len(rows), rows)
	}
	if !strings.Contains(rows[0], "✓ ▸ job-25 · build the bilingual docs") {
		t.Fatalf("folded headline = %q, want the job headline", rows[0])
	}
}

// Opening the card shows the output verbatim at the output indent: a ✓ line in
// the output stays an output row, so exactly one headline row carries the
// disclosure marker (the expanded ▾; the folded ▸ belongs to the folded row).
func TestBackgroundResultExpandedRendersOutputCheckmarksVerbatim(t *testing.T) {
	m := NewModelWithSize(nil, 120, 30)
	raw := "[Background job job-25 finished]\n\n" +
		"Status: completed (exit code 0)\n" +
		"Elapsed: 5s\n" +
		"Purpose: build the bilingual docs\n\n" +
		"Relevant output:\n" +
		"npm warn allow-scripts fsevents@2.3.3\n" +
		"- building client + server bundles...\n" +
		"✓ building client + server bundles...\n" +
		"build complete in 1.51s."

	_ = m.handleAgentEvent(agentEventMsg{event: backgroundResultAppended("", "mb-open-check-output", raw)})
	block, ok := m.viewport.FindStatusBlockByBackgroundObject("mb-open-check-output")
	if !ok {
		t.Fatal("expected background result block")
	}
	if !block.ToggleAtWidth(120) || block.Collapsed {
		t.Fatal("toggling must expand the card")
	}
	rendered := terminaltext.StripANSI(strings.Join(block.Render(120, ""), "\n"))
	for _, want := range []string{
		"npm warn allow-scripts fsevents@2.3.3",
		"✓ building client + server bundles...",
		"build complete in 1.51s.",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("expanded card lost output row %q:\n%s", want, rendered)
		}
	}
	if got := strings.Count(rendered, toolDisclosureExpanded); got != 1 {
		t.Fatalf("disclosure markers = %d, want the single job headline:\n%s", got, rendered)
	}
}
