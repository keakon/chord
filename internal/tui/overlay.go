package tui

import (
	"fmt"
	"image"

	"github.com/keakon/x/ansi"
)

type OverlayConfig struct {
	Title            string
	Hint             string
	CompactHint      string
	Footer           string
	MinContentHeight int

	MaxWidth int
}

func normalizeOverlayConfig(cfg OverlayConfig, area image.Rectangle) OverlayConfig {
	if cfg.MaxWidth <= 0 {
		cfg.MaxWidth = 80
	}
	// Keep the last physical column unwritten to avoid terminal autowrap.
	cfg.MaxWidth = min(cfg.MaxWidth, max(area.Dx()-1, 1))
	return cfg
}

// overlayContentHeight is the scroll window after the fixed frame and footer.
func overlayContentHeight(cfg OverlayConfig, area image.Rectangle) int {
	return layoutOverlay(cfg, area).contentHeight
}

func RenderOverlay(cfg OverlayConfig, content string, area image.Rectangle) (string, image.Rectangle) {
	layout := layoutOverlay(cfg, area)
	cfg = layout.config
	width := max(dialogContentWidth(cfg.MaxWidth), 1)
	lines := make([]string, 0, 8)
	if cfg.Title != "" {
		lines = append(lines, DialogTitleStyle.Render(ansi.Truncate(cfg.Title, width, "…")))
		for range layout.titleGap {
			lines = append(lines, "")
		}
	}
	body := tuiHardwrap(content, width)
	lines = append(lines, body[:min(len(body), layout.contentHeight)]...)
	lines = append(lines, layout.footerLines...)
	for range layout.hintGap {
		lines = append(lines, "")
	}
	lines = append(lines, layout.hintLines...)
	box := renderDialogBox(cfg.MaxWidth, lines)
	return box, centeredRect(area, box)
}

func overlayScrollContentHeight(cfg OverlayConfig, area image.Rectangle, total int) int {
	visible := overlayContentHeight(cfg, area)
	if total <= visible {
		return visible
	}
	cfg.Hint = appendHintText(cfg.Hint, fmt.Sprintf("%d/%d", total, total))
	return overlayContentHeight(cfg, area)
}
