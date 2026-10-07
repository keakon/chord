package tui

import (
	"image"
	"strings"
)

func (m *Model) renderHandoffDenyReasonDialog(cfg OverlayConfig, plan string, area image.Rectangle) string {
	cfg.Title = "Deny handoff with reason"
	cfg.Hint = "[Enter] Deny  [Shift+Enter/Ctrl+J] New line  [Esc] Back"
	cfg.CompactHint = "Enter deny  Esc back"
	width := max(dialogContentWidth(cfg.MaxWidth), 1)
	var errorLines []string
	if errText := strings.TrimSpace(m.handoffSelect.error); errText != "" {
		// Reserve the title, one preview row, one editor row and one hint row.
		errorRows := max(overlayHeight(area)-DirectoryBorderStyle.GetVerticalFrameSize()-4, 0)
		if errorRows == 0 {
			cfg.Title = "Reason required"
		} else {
			for _, line := range wrapText(errText, width) {
				errorLines = append(errorLines, ConfirmDenyStyle.Render(line))
			}
			errorLines = errorLines[:min(len(errorLines), errorRows)]
			cfg.Footer = strings.Join(errorLines, "\n")
		}
	}
	editorHeight := max(min(confirmEditHeight(m.height), overlayContentHeight(cfg, area)-1), 1)
	configureDialogTextarea(&m.handoffSelect.denyReasonInput, width, confirmEditMinHeight, editorHeight)
	inputLines := strings.Split(strings.TrimSuffix(m.handoffSelect.denyReasonInput.View(), "\n"), "\n")
	inputLines = append(inputLines, errorLines...)
	cfg.Footer = strings.Join(inputLines, "\n")
	dialog, _ := RenderOverlay(cfg, plan, area)
	return dialog
}
