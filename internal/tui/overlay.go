package tui

import (
	"image"
	"strings"

	"charm.land/lipgloss/v2"
)

type OverlayConfig struct {
	Title          string
	Hint           string
	MinWidth       int
	MaxWidth       int
	MaxHeightRatio float64
}

func normalizeOverlayConfig(cfg OverlayConfig, area image.Rectangle) OverlayConfig {
	if cfg.MinWidth <= 0 {
		cfg.MinWidth = 40
	}
	if cfg.MaxWidth <= 0 {
		cfg.MaxWidth = 80
	}
	if cfg.MaxHeightRatio <= 0 {
		cfg.MaxHeightRatio = 0.67
	}
	maxAllowed := area.Dx()
	if maxAllowed > 0 && cfg.MaxWidth > maxAllowed {
		cfg.MaxWidth = maxAllowed
	}
	if cfg.MinWidth > cfg.MaxWidth {
		cfg.MinWidth = cfg.MaxWidth
	}
	return cfg
}

func RenderOverlay(cfg OverlayConfig, content string, contentHeight int, area image.Rectangle) (string, image.Rectangle) {
	cfg = normalizeOverlayConfig(cfg, area)
	_ = contentHeight

	bodyLines := make([]string, 0, 5)
	if cfg.Title != "" {
		bodyLines = append(bodyLines, DialogTitleStyle.Render(cfg.Title), "")
	}
	bodyLines = append(bodyLines, content)
	if cfg.Hint != "" {
		bodyLines = append(bodyLines, "", DimStyle.Render(cfg.Hint))
	}
	body := strings.Join(bodyLines, "\n")

	innerWidth := lipgloss.Width(body)
	if innerWidth+4 < cfg.MinWidth {
		innerWidth = cfg.MinWidth - 4
	}
	if innerWidth+4 > cfg.MaxWidth {
		innerWidth = cfg.MaxWidth - 4
	}
	if innerWidth < 0 {
		innerWidth = 0
	}

	box := DirectoryBorderStyle.Width(innerWidth + 4).Render(body)
	return box, centeredRect(area, box)
}
