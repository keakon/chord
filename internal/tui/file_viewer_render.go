package tui

import (
	"fmt"
	"strings"

	"github.com/keakon/x/ansi"

	"github.com/keakon/chord/internal/message"
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
	return DirectoryBorderStyle.Width(p.width + DirectoryBorderStyle.GetHorizontalFrameSize()).Render(strings.Join(p.rows, "\n"))
}
func fileLabel(s string) string {
	return strings.NewReplacer("\n", " ", "\t", " ").Replace(safeFileText(s))
}
func (m *Model) fileViewerWidth() int {
	return max(1, m.width-6-DirectoryBorderStyle.GetHorizontalFrameSize())
}
func (m *Model) fileViewerBodyHeight() int {
	actions, _ := fileActionRows(m.fileViewerWidth(), m.fileViewer.action)
	extra := 1 + len(actions) // status and action rows
	if m.fileViewer.inputMode != "" || m.fileViewer.showing {
		extra++
	}
	return max(2, m.height-DirectoryBorderStyle.GetVerticalFrameSize()-extra-2)
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
		for i := fileMenuStart(v.selected, height, 2); i < len(v.files) && len(p.rows) < height; i++ {
			f := v.files[i]
			prefix := "  "
			if i == v.selected {
				prefix = "> "
			}
			p.hits = append(p.hits, fileViewerHit{kind: fileHitEntry, row: len(p.rows), end: width, index: i})
			p.rows = append(p.rows, prefix+fileLabel(f.DisplayName)+" · "+fileLabel(f.MIME))
			if len(p.rows) < height {
				p.hits = append(p.hits, fileViewerHit{kind: fileHitEntry, row: len(p.rows), end: width, index: i})
				p.rows = append(p.rows, "  "+fileLabel(deliveryPath(f, m.deliverySessionDir())))
			}
		}
	} else if len(v.files) > 0 {
		f := v.files[v.selected]
		version := "Current file"
		if f.Locator.Kind == message.FileLocatorArtifact {
			version = "Declared version"
		}
		mode := "source"
		if !m.fileViewerSourceMode() {
			mode = "Markdown"
		}
		p.rows = append(p.rows, fileLabel(f.DisplayName)+" · "+version+" · "+mode)
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
	actions, hits := fileActionRows(width, v.action)
	for i, h := range hits {
		p.hits = append(p.hits, fileViewerHit{kind: fileHitAction, row: len(p.rows) + h[0], start: h[1], end: min(width, h[2]), index: i})
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
	return p
}
