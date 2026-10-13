package tui

import (
	"fmt"
	"image"
	"strings"
	"unicode/utf8"

	"github.com/keakon/chord/internal/inputhistory"

	"github.com/keakon/x/ansi"
)

type inputHistoryLayout struct {
	area                     image.Rectangle
	config                   OverlayConfig
	width, rows, previewRows int
	filters, status          bool
}

func (m *Model) inputHistoryLayout() inputHistoryLayout {
	area := image.Rect(0, 1, m.width, max(1, m.height-1))
	cfg := OverlayConfig{Title: "Input history", MaxWidth: max(1, min(100, m.width-4)), Hint: "Enter: restore · Esc: cancel · Tab: filters · Ctrl+PgUp/PgDn: preview", CompactHint: "Enter: restore · Esc: cancel", MinContentHeight: 3}
	if m.height < 12 {
		cfg.Title = ""
		cfg.Hint = ""
	}
	capacity := overlayContentHeight(cfg, area)
	layout := inputHistoryLayout{area: area, config: cfg, width: max(1, dialogContentWidth(cfg.MaxWidth)), filters: capacity >= 3, status: capacity >= 4}
	available := capacity - 1
	if layout.filters {
		available--
	}
	if layout.status {
		available--
	}
	layout.rows = max(0, min(10, available))
	layout.previewRows = min(8, max(0, available-layout.rows-1))
	return layout
}

func (m *Model) renderInputHistory() string {
	s := &m.inputHistory
	layout := m.inputHistoryLayout()
	cfg, area, width := layout.config, layout.area, layout.width
	if m.mode == ModeInputHistoryClearConfirm {
		cfg.Title = "Clear input history"
		cancel, confirm := "Cancel", "Clear"
		if s.confirm {
			confirm = "[Clear]"
		} else {
			cancel = "[Cancel]"
		}
		body := "Clear saved inputs from ALL projects and temporary records accepted in this process before this request.\nOther processes may save their pending inputs afterwards.\n\n" + cancel + "    " + confirm
		if s.clearing {
			body = "Clearing input history…"
		}
		box, _ := RenderOverlay(cfg, body, area)
		return box
	}
	s.query.SetWidth(max(1, width-8))
	lines := []string{ansi.Truncate(s.query.View(), width, "…")}
	scope, kind := "Current project", "Prompts"
	if s.allProjects {
		scope = "All projects"
	}
	if s.allKinds {
		kind = "All inputs"
	}
	if s.focus == 1 {
		scope = "[" + scope + "]"
	}
	if s.focus == 2 {
		kind = "[" + kind + "]"
	}
	filterRow, split := -1, width/2
	if layout.filters {
		filterRow = len(lines)
		left := ansi.Truncate(scope, split, "…")
		lines = append(lines, left+strings.Repeat(" ", max(0, split-ansi.StringWidth(left)))+ansi.Truncate(kind, width-split, "…"))
	}
	listIndex := len(lines)
	start := max(0, s.selected-max(1, layout.rows)+1)
	end := min(len(s.results.Hits), start+layout.rows)
	for i := start; i < end; i++ {
		h := s.results.Hits[i]
		mark := "  "
		if i == s.selected {
			mark = "> "
		}
		meta := ""
		if e, ok := s.entriesByID[h.ID]; ok {
			meta = e.Record.SubmittedAt.Local().Format("01-02 15:04") + " " + string(e.Record.Kind) + " "
			if s.allProjects {
				meta += e.Record.ProjectLabel + " "
			}
			if e.State != inputhistory.Synced {
				meta += "[" + string(e.State) + "] "
			}
		}
		lines = append(lines, ansi.Truncate(mark+sanitizeToolDisplayText(meta)+renderHistoryHighlights(h.Summary, h.Highlights), width, "…"))
	}
	// Reserve the same visible rows used by the preview worker, even with few hits.
	for len(lines)-listIndex < layout.rows {
		text := ""
		if len(lines) == listIndex {
			text = "No matching inputs"
		}
		lines = append(lines, ansi.Truncate(text, width, "…"))
	}
	if layout.status {
		var status []string
		if s.loading {
			status = append(status, "Searching…")
		}
		if s.refreshError != nil {
			status = append(status, "Unable to refresh; showing loaded history")
		}
		if s.queryLimited {
			status = append(status, "Query limited: 256 characters / 16 words")
		}
		if s.results.TotalMatched > 100 {
			status = append(status, "Showing first 100 matches")
		}
		lines = append(lines, ansi.Truncate(strings.Join(status, " · "), width, "…"))
	}
	previewIndex := len(lines)
	if layout.previewRows > 0 {
		if r, ok := m.selectedHistoryRecord(); ok {
			session := r.SessionID
			if session == "" {
				session = "No session"
			}
			lines = append(lines, ansi.Truncate(sanitizeToolDisplayText(fmt.Sprintf("%s · %s · %d attachments", session, r.ProjectKey, r.AttachmentCount)), width, "…"))
			previewIndex++
			preview := strings.Split(renderHistoryHighlights(s.preview.Text, s.preview.Highlights), "\n")
			for _, line := range preview[:min(layout.previewRows, len(preview))] {
				lines = append(lines, ansi.Truncate(line, width, "…"))
			}
			for len(lines) < previewIndex+layout.previewRows {
				lines = append(lines, "")
			}
		}
	}
	box, rect := RenderOverlay(cfg, strings.Join(lines, "\n"), area)
	chrome := layoutOverlay(cfg, area)
	x := rect.Min.X + DirectoryBorderStyle.GetBorderLeftSize() + DirectoryBorderStyle.GetPaddingLeft()
	y := rect.Min.Y + DirectoryBorderStyle.GetBorderTopSize() + DirectoryBorderStyle.GetPaddingTop() + chrome.contentBaseRow()
	s.listBase, s.listRows, s.listStart = y+listIndex, end-start, start
	s.listRect = image.Rect(x, s.listBase, x+width, s.listBase+s.listRows)
	s.filterRects = [2]image.Rectangle{}
	if filterRow >= 0 {
		s.filterRects = [2]image.Rectangle{image.Rect(x, y+filterRow, x+split, y+filterRow+1), image.Rect(x+split, y+filterRow, x+width, y+filterRow+1)}
	}
	s.previewRect = image.Rectangle{}
	if layout.previewRows > 0 {
		s.previewRect = image.Rect(x, y+previewIndex, x+width, min(y+previewIndex+layout.previewRows, y+chrome.contentHeight))
	}
	return box
}

// Restore the overlay surface after each highlighted run, not the transcript background.
func renderHistoryHighlights(text string, ranges []inputhistory.Range) string {
	if len(ranges) == 0 {
		return text
	}
	runes := utf8.RuneCountInString(text)
	marked := make([]bool, runes)
	for _, r := range ranges {
		for i := max(0, r.Start); i < min(runes, r.End); i++ {
			marked[i] = true
		}
	}
	var out, run strings.Builder
	highlight := false
	at := 0
	flush := func() {
		if run.Len() == 0 {
			return
		}
		if highlight {
			out.WriteString(SearchMatchStyle.Render(run.String()))
		} else {
			out.WriteString(run.String())
		}
		run.Reset()
	}
	for _, c := range text {
		if marked[at] != highlight {
			flush()
			highlight = marked[at]
		}
		run.WriteRune(c)
		at++
	}
	flush()
	return preserveDialogBackground(out.String())
}
