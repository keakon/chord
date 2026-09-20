package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/keakon/chord/internal/tools"
)

func TestJobRowFitsAvailableWidth(t *testing.T) {
	ApplyTheme(DefaultTheme())
	now := time.Now()
	for _, status := range []string{jobStatusRunning, jobStatusStopping, "completed"} {
		for _, duration := range []time.Duration{time.Second, time.Hour, 1000 * time.Hour} {
			for _, hasOutput := range []bool{false, true} {
				job := tools.JobState{ID: "job-1", Status: status, Description: "sample job", StartedAt: now.Add(-duration)}
				if hasOutput {
					job.LastOutputAt = job.StartedAt
				}
				for _, includeQuiet := range []bool{false, true} {
					for width := 1; width <= 80; width++ {
						row := renderJobRow(width, job, now, dialogJobRowStyles(), includeQuiet)
						if got := ansi.StringWidth(row.text); got != width {
							t.Fatalf("status=%s duration=%v output=%v quiet=%v width=%d: row width=%d text=%q", status, duration, hasOutput, includeQuiet, width, got, ansi.Strip(row.text))
						}
						if row.stoppable {
							plain := strings.TrimRight(ansi.Strip(row.text), " ")
							if !strings.HasSuffix(plain, jobStopGlyph) {
								t.Fatalf("width=%d: missing stop glyph in %q", width, plain)
							}
							glyphColumn := ansi.StringWidth(plain) - ansi.StringWidth(jobStopGlyph)
							if glyphColumn < row.stopZoneStart || glyphColumn >= row.stopZoneEnd {
								t.Fatalf("width=%d: stop glyph at %d outside [%d,%d)", width, glyphColumn, row.stopZoneStart, row.stopZoneEnd)
							}
						}
					}
				}
			}
		}
	}
}

func TestJobRowQuietColumnResponsive(t *testing.T) {
	ApplyTheme(DefaultTheme())
	now := time.Now()
	job := tools.JobState{ID: "job-1", Status: jobStatusRunning, Description: "sample job", StartedAt: now.Add(-time.Hour)}
	for _, width := range []int{24, 64} {
		row := renderJobRow(width, job, now, dialogJobRowStyles(), true)
		plain := ansi.Strip(row.text)
		if strings.Contains(plain, "no output") != (width == 64) {
			t.Fatalf("width=%d: unexpected quiet column in %q", width, plain)
		}
		if !strings.Contains(plain, job.Description) {
			t.Fatalf("width=%d: job description missing from %q", width, plain)
		}
	}
}

func TestJobsOverlayLongQuietRowStopHit(t *testing.T) {
	for _, width := range []int{40, 50, 60, 80} {
		model := newJobsTestModel(t, width, 40)
		model.mode = ModeJobsOverlay
		model.jobsSnapshot = []tools.JobState{{
			ID: "job-1", Status: jobStatusRunning, Description: "sample job", StartedAt: time.Now().Add(-time.Hour),
		}}
		model.activeJobsSnapshot = model.jobsSnapshot
		model.jobsSnapshotValid = true
		model.jobsSnapshotFrame = model.renderFrameGeneration
		dialog := model.renderJobsOverlayDialog()
		rect := centeredRect(model.ensureLayout().area, dialog)
		found := false
		for lineIndex, line := range strings.Split(dialog, "\n") {
			plain := ansi.Strip(line)
			glyphIndex := strings.Index(plain, " "+jobStopGlyph+" ")
			if glyphIndex < 0 {
				continue
			}
			found = true
			column := ansi.StringWidth(plain[:glyphIndex+1])
			jobID, inStopZone, ok := model.jobsOverlayRowAt(rect.Min.X+column, rect.Min.Y+lineIndex)
			if !ok || !inStopZone || jobID != "job-1" {
				t.Fatalf("width=%d: visible stop glyph did not hit job: id=%q stop=%v ok=%v", width, jobID, inStopZone, ok)
			}
		}
		if !found {
			t.Fatalf("width=%d: no visible stop glyph in %q", width, ansi.Strip(dialog))
		}
	}
}
