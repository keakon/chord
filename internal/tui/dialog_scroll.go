package tui

import (
	"fmt"
	"image"
	"strings"

	tea "github.com/keakon/bubbletea/v2"
)

// dialogScrollState counts physical body rows. Fixed actions never scroll away.
type dialogScrollState struct {
	offset  int
	visible int
	total   int
}

func (s *dialogScrollState) move(delta int) {
	s.offset = max(0, min(s.offset+delta, s.total-s.visible))
}

func (s *dialogScrollState) handleKey(msg tea.KeyMsg) bool {
	switch msg.String() {
	case "pgup", "ctrl+b":
		s.move(-max(s.visible, 1))
	case "pgdown", "ctrl+f":
		s.move(max(s.visible, 1))
	case "home":
		s.offset = 0
	case "end":
		s.move(s.total)
	default:
		return false
	}
	return true
}

func renderScrollableDialog(cfg OverlayConfig, lines []string, area image.Rectangle, scroll *dialogScrollState) string {
	cfg = normalizeOverlayConfig(cfg, area)
	lines = wrapDialogLines(lines, max(dialogContentWidth(cfg.MaxWidth), 1))
	scroll.total = len(lines)
	scroll.visible = overlayContentHeight(cfg, area)
	if scroll.total > scroll.visible {
		cfg.Hint = appendHintChip(cfg.Hint, hint("PgUp/PgDn", "scroll"))
		scroll.visible = overlayContentHeight(cfg, area)
	}
	scroll.move(0)
	end := min(scroll.offset+scroll.visible, scroll.total)
	if scroll.total > scroll.visible {
		cfg.Title += fmt.Sprintf(" [%d-%d/%d]", scroll.offset+1, end, scroll.total)
	}
	out, _ := RenderOverlay(cfg, strings.Join(lines[scroll.offset:end], "\n"), area)
	return out
}
