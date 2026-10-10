package tui

import (
	"image"
	"strings"

	"github.com/keakon/x/ansi"
)

// overlayLayout uses one height budget for the frame, scrollable content and
// fixed footer. Hints become compact before the content loses its required rows.
type overlayLayout struct {
	config        OverlayConfig
	hintLines     []string
	footerLines   []string
	titleGap      int
	hintGap       int
	contentHeight int
}

func layoutOverlay(cfg OverlayConfig, area image.Rectangle) overlayLayout {
	cfg = normalizeOverlayConfig(cfg, area)
	width := max(dialogContentWidth(cfg.MaxWidth), 1)
	layout := overlayLayout{config: cfg}
	if cfg.Hint != "" {
		layout.hintLines = wrapHintLines(cfg.Hint, width)
	}
	if cfg.Footer != "" {
		layout.footerLines = tuiHardwrap(strings.TrimSuffix(cfg.Footer, "\n"), width)
	}
	height := overlayHeight(area)
	titleRows := 0
	if cfg.Title != "" {
		titleRows = 1
	}
	minContent := max(cfg.MinContentHeight, 1)
	hintRows := 0
	if len(layout.hintLines) > 0 {
		hintRows = 1
	}
	available := height - DirectoryBorderStyle.GetVerticalFrameSize() - titleRows - len(layout.footerLines)
	minContent = min(minContent, max(available-hintRows, 1))
	gaps := titleRows
	if hintRows > 0 {
		gaps++
	}
	if available-minContent-hintRows >= gaps {
		layout.titleGap = titleRows
		layout.hintGap = hintRows
	}
	maxHints := max(available-minContent-layout.titleGap-layout.hintGap, hintRows)
	if len(layout.hintLines) > maxHints && cfg.CompactHint != "" {
		layout.hintLines = wrapHintLines(cfg.CompactHint, width)
		if len(layout.hintLines) > maxHints {
			// Fill the available rows and truncate only the last one, so keys
			// that would otherwise be cut with the tail stay visible.
			last := strings.Join(layout.hintLines[maxHints-1:], "  ")
			layout.hintLines = append(layout.hintLines[:maxHints-1], ansi.Truncate(last, width, "…"))
		}
	}
	if len(layout.hintLines) > maxHints && cfg.CompactHint == "" {
		tail := strings.Join(layout.hintLines[maxHints-1:], "  ")
		layout.hintLines[maxHints-1] = ansi.Truncate(tail, width, "…")
	}
	layout.hintLines = layout.hintLines[:min(len(layout.hintLines), maxHints)]
	layout.contentHeight = max(available-layout.titleGap-layout.hintGap-len(layout.hintLines), 1)
	return layout
}

func overlayHeight(area image.Rectangle) int {
	return max(area.Dy()-2, min(area.Dy(), 6))
}

func (layout overlayLayout) contentBaseRow() int {
	if layout.config.Title == "" {
		return 0
	}
	return 1 + layout.titleGap
}
