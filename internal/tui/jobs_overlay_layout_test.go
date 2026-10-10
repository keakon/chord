package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/keakon/bubbletea/v2"
	"github.com/keakon/lipgloss/v2"
	"github.com/keakon/x/ansi"

	"github.com/keakon/chord/internal/tools"
)

func jobsOverlayLayoutModel(width, height, count int) *Model {
	m := NewModelWithSize(newInfoPanelAgent(), width, height)
	for i := range count {
		m.jobsSnapshot = append(m.jobsSnapshot, tools.JobState{
			ID: fmt.Sprintf("job-%02d", i), Description: fmt.Sprintf("J%02d", i),
			Status: jobStatusRunning, StartedAt: time.Now(),
		})
	}
	m.activeJobsSnapshot = m.jobsSnapshot
	m.jobsSnapshotValid = true
	m.jobsSnapshotFrame = m.renderFrameGeneration
	m.mode = ModeJobsOverlay
	return &m
}

func TestJobsOverlayLayoutKeepsSelectedJobVisible(t *testing.T) {
	for _, size := range [][2]int{{30, 6}, {30, 12}, {40, 12}, {80, 10}, {80, 24}} {
		t.Run(fmt.Sprintf("%dx%d", size[0], size[1]), func(t *testing.T) {
			m := jobsOverlayLayoutModel(size[0], size[1], 12)
			for i := range 12 {
				if i > 0 {
					m.handleJobsOverlayKey(tea.KeyPressMsg(tea.Key{Text: "j", Code: 'j'}))
				}
				dialog := m.renderJobsOverlayDialog()
				if lipgloss.Width(dialog) > size[0]-1 || lipgloss.Height(dialog) > size[1] {
					t.Fatalf("dialog exceeds terminal bounds: %dx%d", lipgloss.Width(dialog), lipgloss.Height(dialog))
				}
				if !strings.Contains(ansi.Strip(dialog), fmt.Sprintf("J%02d", i)) {
					t.Fatalf("selected job %d is hidden:\n%s", i, ansi.Strip(dialog))
				}
			}
			m.handleJobsOverlayKey(tea.KeyPressMsg(tea.Key{Text: "g", Code: 'g'}))
			m.handleJobsOverlayKey(tea.KeyPressMsg(tea.Key{Text: "G", Code: 'G'}))
			if !strings.Contains(ansi.Strip(m.renderJobsOverlayDialog()), "J11") {
				t.Fatal("jump to bottom hides the selected job")
			}
			m.applyTerminalSize(30, 6, false)
			if !strings.Contains(ansi.Strip(m.renderJobsOverlayDialog()), "J11") {
				t.Fatal("resize hides the selected job")
			}
		})
	}
}

func TestJobsOverlayLayoutMouseMatchesDrawnRows(t *testing.T) {
	for _, size := range [][2]int{{30, 6}, {80, 6}, {40, 12}, {80, 24}} {
		t.Run(fmt.Sprintf("%dx%d", size[0], size[1]), func(t *testing.T) {
			m := jobsOverlayLayoutModel(size[0], size[1], 12)
			m.handleJobsOverlayKey(tea.KeyPressMsg(tea.Key{Text: "G", Code: 'G'}))
			dialog := m.renderJobsOverlayDialog()
			rect := m.overlayRect(dialog)
			x := rect.Min.X + DirectoryBorderStyle.GetBorderLeftSize() + DirectoryBorderStyle.GetPaddingLeft() + m.jobsOverlayInnerWidth() - 1
			for row, line := range strings.Split(ansi.Strip(dialog), "\n") {
				want := ""
				for i := range 12 {
					if strings.Contains(line, fmt.Sprintf("J%02d", i)) {
						want = fmt.Sprintf("job-%02d", i)
						break
					}
				}
				id, stop, ok := m.jobsOverlayRowAt(x, rect.Min.Y+row)
				if want == "" {
					if ok {
						t.Fatalf("non-job row %q maps to %q", line, id)
					}
					continue
				}
				if !ok || !stop || id != want {
					t.Fatalf("row %q maps to %q, stop=%t ok=%t, want %q", line, id, stop, ok, want)
				}
			}
		})
	}
}
