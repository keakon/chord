// Package markdownrender owns settled Markdown rendering, style snapshots and
// renderer reuse. Containers retain their backgrounds and streaming state.
package markdownrender

import (
	"strings"

	"github.com/keakon/glamour/v2"
	"github.com/keakon/glamour/v2/ansi"

	"github.com/keakon/chord/internal/tui/markdownutil"
	"github.com/keakon/chord/internal/tui/terminaltext"
	uitheme "github.com/keakon/chord/internal/tui/theme"
)

// Renderer owns one cached width and an immutable palette-derived style.
// Construct it with New and use it on one owner goroutine.
type Renderer struct {
	renderer *glamour.TermRenderer
	width    int
	style    ansi.StyleConfig
	render   func(*glamour.TermRenderer, string) (string, error)
}

// New captures a palette snapshot. Replacing the Renderer invalidates both its
// style and width cache without changing previously captured style configs.
func New(palette uitheme.Palette) *Renderer {
	return &Renderer{style: StyleConfig(palette), render: (*glamour.TermRenderer).Render}
}

func (r *Renderer) reset() {
	r.renderer = nil
	r.width = 0
}

// Render renders settled markdown with transparent block-level
// backgrounds; fenced code is handled separately in assistant code paths.
func (r *Renderer) Render(content string, width int) (lines []string) {
	if width <= 0 {
		width = 80
	}
	content = terminaltext.Sanitize(content)
	content = markdownutil.RepairForDisplay(content)
	// The adjacent-strong marker is glamour vocabulary: every non-glamour
	// fallback below must render the pre-normalization text, or the marker
	// would surface verbatim in the output.
	fallback := content
	content = markdownutil.NormalizeAdjacentStrong(content)
	defer func() {
		if recover() != nil {
			r.reset()
			lines = terminaltext.Wrap(fallback, width)
		}
	}()
	if r.renderer == nil || r.width != width {
		renderer, err := glamour.NewTermRenderer(
			glamour.WithStyles(r.style),
			glamour.WithWordWrap(width),
			glamour.WithInlineTableLinks(true),
		)
		if err != nil {
			return terminaltext.Wrap(fallback, width)
		}
		r.renderer = renderer
		r.width = width
	}
	out, err := r.render(r.renderer, content)
	if err != nil {
		return terminaltext.Wrap(fallback, width)
	}
	out = strings.TrimLeft(out, "\n\r")
	out = strings.TrimRight(out, "\n")
	if out == "" {
		return []string{""}
	}
	return normalizeRenderedMarkdownIndent(strings.Split(out, "\n"))
}
