package tui

import (
	"strings"

	"github.com/keakon/bubbles/v2/textarea"
)

func (m *Model) renderConfirmEditor(width int, title string, input *textarea.Model, submit, exit hintChip) string {
	compact := []hintChip{submit}
	if exit.keys != "" {
		compact = append(compact, exit)
	}
	full := append(append([]hintChip(nil), compact...), hint("Shift+Enter/Ctrl+J", "new line"))
	cfg := OverlayConfig{
		Title: title, MaxWidth: width,
		Hint: hintLine(full...), CompactHint: hintLine(compact...),
	}
	if m.confirm.editError != "" {
		cfg.Footer = DialogDangerStyle.Render(truncateOneLine(m.confirm.editError, max(dialogContentWidth(width), 1)))
	}
	area := m.confirmDialogArea()
	configureDialogTextarea(input, max(dialogContentWidth(width), 1), 1, min(confirmEditHeight(m.height), overlayContentHeight(cfg, area)))
	out, _ := RenderOverlay(cfg, strings.TrimSuffix(input.View(), "\n"), area)
	return out
}
