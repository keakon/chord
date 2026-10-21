package tui

import (
	"fmt"
	"strings"

	"github.com/keakon/x/ansi"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tui/filepreview"
	"github.com/keakon/chord/internal/tui/terminaltext"
)

type fileHitKind uint8

const (
	fileHitAction fileHitKind = iota
	fileHitApplication
	fileHitEntry
)

type fileViewerHit struct {
	kind                   fileHitKind
	row, start, end, index int
}
type fileViewerPresentation struct {
	rows  []string
	hits  []fileViewerHit
	width int
}

func (p fileViewerPresentation) render() string {
	return currentStyles.DirectoryBorderStyle.Width(p.width + currentStyles.DirectoryBorderStyle.GetHorizontalFrameSize()).Render(strings.Join(p.rows, "\n"))
}
func fileLabel(s string) string {
	return strings.NewReplacer("\n", " ", "\t", " ").Replace(filepreview.SafeText(s))
}
func (m *Model) fileViewerWidth() int {
	return max(1, m.width-6-currentStyles.DirectoryBorderStyle.GetHorizontalFrameSize())
}
func (m *Model) fileViewerBodyHeight() int {
	buttons, _ := viewerActionButtons(m.fileViewer.showing)
	actions, _ := wrapActionButtons(buttons, m.fileViewerWidth())
	extra := 1 + len(actions) // status and action rows
	if m.fileViewer.inputMode != "" || m.fileViewer.showing {
		extra++
	}
	return max(2, m.height-currentStyles.DirectoryBorderStyle.GetVerticalFrameSize()-extra-2)
}
func (m *Model) fileViewerPageSize() int { return max(1, m.fileViewerBodyHeight()-1) }
func (m *Model) fileViewerSourceMode() bool {
	v := &m.fileViewer
	return len(v.files) == 0 || !(v.markdown && strings.HasPrefix(v.files[v.selected].MIME, "text/markdown"))
}
func (m *Model) fileViewerVisualLines(index int) []string {
	v := &m.fileViewer
	line := v.lines[index]
	if m.fileViewerSourceMode() {
		line = fmt.Sprintf("%5d │ ", index+1) + line
	}
	if v.wrap {
		return strings.Split(ansi.Hardwrap(line, m.fileViewerWidth(), true), "\n")
	}
	return []string{ansi.Cut(line, v.horizontal, v.horizontal+m.fileViewerWidth())}
}
func (m *Model) scrollFileViewer(delta int) {
	v := &m.fileViewer
	if !v.showing || len(v.lines) == 0 {
		return
	}
	for delta > 0 {
		available := len(m.fileViewerVisualLines(v.offset)) - 1 - v.wrapOffset
		step := min(delta, available)
		v.wrapOffset += step
		delta -= step
		if delta == 0 || v.offset+1 >= len(v.lines) {
			break
		}
		v.offset++
		v.wrapOffset = 0
		delta--
	}
	for delta < 0 {
		step := min(-delta, v.wrapOffset)
		v.wrapOffset -= step
		delta += step
		if delta == 0 || v.offset == 0 {
			break
		}
		v.offset--
		v.wrapOffset = len(m.fileViewerVisualLines(v.offset)) - 1
		delta++
	}
	m.clampFileViewerTail()
}

// clampFileViewerTail keeps the last page full: when the remaining visual
// lines from the current position cannot fill the body, it moves the start
// back just enough (or to the top for short files) so scrolling cannot strand
// the reader on a mostly-blank page. It walks at most one page of visual
// lines, so wrap-mode Hardwrap cost stays bounded.
func (m *Model) clampFileViewerTail() {
	v := &m.fileViewer
	if !v.showing || len(v.lines) == 0 {
		return
	}
	page := m.fileViewerPageSize()
	if v.offset < 0 {
		v.offset = 0
	}
	if v.offset >= len(v.lines) {
		v.offset = len(v.lines) - 1
	}
	if cur := m.fileViewerVisualLines(v.offset); len(cur) > 0 {
		if v.wrapOffset < 0 {
			v.wrapOffset = 0
		}
		if v.wrapOffset >= len(cur) {
			v.wrapOffset = len(cur) - 1
		}
	} else {
		v.wrapOffset = 0
	}
	remaining := 0
	for i := v.offset; i < len(v.lines) && remaining < page; i++ {
		lines := m.fileViewerVisualLines(i)
		start := 0
		if i == v.offset {
			start = v.wrapOffset
		}
		if start < len(lines) {
			remaining += len(lines) - start
		}
	}
	if remaining >= page {
		return
	}
	need := page - remaining
	for need > 0 && (v.offset > 0 || v.wrapOffset > 0) {
		if v.wrapOffset > 0 {
			step := min(need, v.wrapOffset)
			v.wrapOffset -= step
			need -= step
		} else {
			if v.offset == 0 {
				break
			}
			v.offset--
			if l := len(m.fileViewerVisualLines(v.offset)); l > 0 {
				v.wrapOffset = l - 1
			} else {
				v.wrapOffset = 0
			}
			need--
		}
	}
}
func (m *Model) fileViewerEnd() {
	v := &m.fileViewer
	if !v.showing || len(v.lines) == 0 {
		return
	}
	v.offset = len(v.lines) - 1
	v.wrapOffset = len(m.fileViewerVisualLines(v.offset)) - 1
	m.scrollFileViewer(-(m.fileViewerPageSize() - 1))
}
func (m *Model) renderFileViewerOverlay() string {
	if !m.fileViewer.open {
		return ""
	}
	return m.fileViewerLayout().render()
}
func (m *Model) fileViewerLayout() fileViewerPresentation {
	v := &m.fileViewer
	width, height := m.fileViewerWidth(), m.fileViewerBodyHeight()
	p := fileViewerPresentation{width: width}
	if len(v.apps) > 0 {
		p.rows = append(p.rows, "Open with · Enter select · r refresh")
		for i := fileMenuStart(v.appIndex, height, 1); i < len(v.apps) && len(p.rows) < height; i++ {
			prefix := "  "
			if i == v.appIndex {
				prefix = "> "
			}
			label := v.apps[i].Name
			if v.apps[i].Default {
				label += " (default)"
			}
			p.hits = append(p.hits, fileViewerHit{kind: fileHitApplication, row: len(p.rows), end: width, index: i})
			p.rows = append(p.rows, prefix+fileLabel(label))
		}
	} else if !v.showing {
		p.rows = append(p.rows, "Delivered files · ↑↓ select · Enter preview")
		root, paths := deliveryDisplayPaths(v.files, m.workingDir)
		if label := deliveryRootLabel(root); label != "" && len(p.rows)+1 < height {
			p.rows = append(p.rows, currentStyles.DimStyle.Render("  "+fileLabel(label)+"/"))
		}
		if len(p.rows)+1 < height {
			p.rows = append(p.rows, "")
		}
		visible := max(1, height-len(p.rows))
		start := max(0, v.selected-visible+1)
		for i := start; i < len(v.files) && len(p.rows) < height; i++ {
			f := v.files[i]
			path := paths[i]
			if path == "" {
				path = f.DisplayName
			}
			dir, name := deliveryPathParts(path)
			suffix := ""
			if dir != "" {
				suffix += "  " + dir + "/"
			}
			if label := deliveryTypeLabel(f.MIME); label != "" {
				suffix += "  " + label
			}
			if i == v.selected {
				// Truncate before Width: lipgloss wraps content that exceeds the
				// width, and an embedded newline would add a visual row while the
				// hit still counted the entry as one row.
				label := ansi.Truncate(" ▸ "+fileLabel(name)+fileLabel(suffix), width, "…")
				p.rows = append(p.rows, currentStyles.SelectedStyle.Width(width).Render(label))
			} else {
				row := "   " + currentStyles.ToolFileNameStyle.Render(fileLabel(name))
				if suffix != "" {
					row += currentStyles.DimStyle.Render(fileLabel(suffix))
				}
				p.rows = append(p.rows, row)
			}
			p.hits = append(p.hits, fileViewerHit{kind: fileHitEntry, row: len(p.rows) - 1, end: width, index: i})
		}
	} else if len(v.files) > 0 {
		f := v.files[v.selected]
		mode := "source"
		if !m.fileViewerSourceMode() {
			mode = "Markdown"
		}
		version := ""
		if f.Locator.Kind == message.FileLocatorArtifact {
			version = " · Declared version"
		}
		p.rows = append(p.rows, fileLabel(f.DisplayName)+version+" · "+mode)
		for i := v.offset; i < len(v.lines) && len(p.rows) < height; i++ {
			lines := m.fileViewerVisualLines(i)
			start := 0
			if i == v.offset {
				start = min(v.wrapOffset, len(lines)-1)
			}
			p.rows = append(p.rows, lines[start:min(len(lines), start+height-len(p.rows))]...)
		}
	}
	for len(p.rows) < height {
		p.rows = append(p.rows, "")
	}
	p.rows = append(p.rows, fileLabel(v.note))
	buttons, indexes := viewerActionButtons(v.showing)
	actions, hits := wrapActionButtons(buttons, width)
	for i := range actions {
		actions[i] = currentStyles.KeyHintStyle.Render(actions[i])
	}
	for i, h := range hits {
		p.hits = append(p.hits, fileViewerHit{kind: fileHitAction, row: len(p.rows) + h[0], start: h[1], end: min(width, h[2]), index: indexes[i]})
	}
	p.rows = append(p.rows, actions...)
	if v.inputMode != "" {
		p.rows = append(p.rows, v.inputMode+fileLabel(v.input))
	} else if v.showing {
		p.rows = append(p.rows, "l local links/images · m source · / search · n/N · g line · w wrap · y copy loaded")
	}
	for i := range p.rows {
		p.rows[i] = ansi.Truncate(p.rows[i], width, "")
	}
	// Glamour markdown rows carry per-span resets that would clear the outer
	// DirectoryBorderStyle background for the rest of the line, leaving content
	// on the terminal default while blank padding keeps DialogBg. Re-insert the
	// dialog background like other overlay paths do.
	if bg := currentStyles.Palette.DialogBg; bg != "" {
		for i, row := range p.rows {
			if row == "" || !strings.Contains(row, "\x1b[") {
				continue
			}
			p.rows[i] = terminaltext.EnsureReset(terminaltext.PreserveBackground(row, bg))
		}
	}
	return p
}
