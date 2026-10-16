package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tui/terminaltext"
)

func TestFileViewerScrollClampsLastPage(t *testing.T) {
	m := NewModelWithSize(nil, 80, 30)
	lines := make([]string, 30)
	for i := range lines {
		lines[i] = fmt.Sprintf("line %02d content", i+1)
	}
	m.fileViewer = fileViewerState{
		open:    true,
		showing: true,
		files:   []message.DeliveredFile{{DisplayName: "sample.txt"}},
		lines:   lines,
		plain:   lines,
	}
	page := m.fileViewerPageSize()
	if page <= 0 || page >= len(lines) {
		t.Fatalf("unexpected page size %d for %d lines", page, len(lines))
	}
	m.scrollFileViewer(1000)
	wantOffset := len(lines) - page
	if m.fileViewer.offset != wantOffset || m.fileViewer.wrapOffset != 0 {
		t.Fatalf("offset=%d wrapOffset=%d, want offset=%d wrapOffset=0", m.fileViewer.offset, m.fileViewer.wrapOffset, wantOffset)
	}
	p := m.fileViewerLayout()
	bodyHeight := m.fileViewerBodyHeight()
	if len(p.rows) < bodyHeight {
		t.Fatalf("layout rows=%d, want at least body height %d", len(p.rows), bodyHeight)
	}
	for i := 1; i < bodyHeight; i++ {
		if strings.TrimSpace(terminaltext.StripANSI(p.rows[i])) == "" {
			t.Fatalf("body row %d is blank padding after clamped scroll (offset=%d): %q", i, m.fileViewer.offset, p.rows[i])
		}
	}
	// Further scrolling must not move.
	m.scrollFileViewer(5)
	if m.fileViewer.offset != wantOffset {
		t.Fatalf("overscroll moved offset to %d, want %d", m.fileViewer.offset, wantOffset)
	}
}

func TestFileViewerShortFileStaysAtTop(t *testing.T) {
	m := NewModelWithSize(nil, 80, 30)
	lines := []string{"one", "two", "three"}
	m.fileViewer = fileViewerState{
		open:    true,
		showing: true,
		files:   []message.DeliveredFile{{DisplayName: "sample.txt"}},
		lines:   lines,
		plain:   lines,
	}
	m.scrollFileViewer(1000)
	if m.fileViewer.offset != 0 || m.fileViewer.wrapOffset != 0 {
		t.Fatalf("short file scrolled to offset=%d wrapOffset=%d, want 0,0", m.fileViewer.offset, m.fileViewer.wrapOffset)
	}
}

func TestFileViewerWrappedScrollClampsLastPage(t *testing.T) {
	m := NewModelWithSize(nil, 60, 24)
	width := m.fileViewerWidth()
	lines := make([]string, 10)
	for i := range lines {
		lines[i] = fmt.Sprintf("row %02d ", i+1) + strings.Repeat("x", 3*max(width, 1))
	}
	m.fileViewer = fileViewerState{
		open:    true,
		showing: true,
		wrap:    true,
		files:   []message.DeliveredFile{{DisplayName: "sample.txt"}},
		lines:   lines,
		plain:   lines,
	}
	page := m.fileViewerPageSize()
	total := 0
	for i := range lines {
		total += len(m.fileViewerVisualLines(i))
	}
	if total <= page {
		t.Fatalf("fixture does not overflow the page: total=%d page=%d width=%d", total, page, width)
	}
	m.scrollFileViewer(total * 2)
	remaining := 0
	for i := m.fileViewer.offset; i < len(lines) && remaining < page; i++ {
		visual := m.fileViewerVisualLines(i)
		start := 0
		if i == m.fileViewer.offset {
			start = m.fileViewer.wrapOffset
		}
		remaining += len(visual) - start
	}
	if remaining != page {
		t.Fatalf("remaining=%d, want full page %d (offset=%d wrapOffset=%d total=%d)", remaining, page, m.fileViewer.offset, m.fileViewer.wrapOffset, total)
	}
	p := m.fileViewerLayout()
	bodyHeight := m.fileViewerBodyHeight()
	for i := 1; i < bodyHeight; i++ {
		if strings.TrimSpace(terminaltext.StripANSI(p.rows[i])) == "" {
			t.Fatalf("wrapped body row %d is blank padding (offset=%d wrapOffset=%d)", i, m.fileViewer.offset, m.fileViewer.wrapOffset)
		}
	}
}

func TestFileViewerMarkdownKeepsDialogBg(t *testing.T) {
	wantBg := dialogBgColor(t)
	m := NewModelWithSize(nil, 80, 30)
	line := "\x1b[38;5;252m• \x1b[m\x1b[38;5;252mtail content\x1b[m"
	m.fileViewer = fileViewerState{
		open:     true,
		showing:  true,
		markdown: true,
		files:    []message.DeliveredFile{{DisplayName: "note.md", MIME: "text/markdown"}},
		lines:    []string{line, "second line"},
		plain:    []string{"• tail content", "second line"},
	}
	out := m.renderFileViewerOverlay()
	if out == "" {
		t.Fatal("expected non-empty file viewer overlay")
	}
	rendered := findRenderedLineContaining(out, "tail content")
	if rendered == "" {
		t.Fatalf("missing markdown line: %q", terminaltext.StripANSI(out))
	}
	requireCellBg(t, rendered, "tail content", "t", wantBg)
}
