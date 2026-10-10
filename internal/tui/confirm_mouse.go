package tui

import (
	"github.com/keakon/lipgloss/v2"

	tea "github.com/keakon/bubbletea/v2"
	"github.com/keakon/x/ansi"

	"github.com/keakon/chord/internal/tools"
)

type confirmOptionSpec struct {
	action confirmDialogAction
	hint   hintChip
}

type confirmOptionHit struct {
	spec  confirmOptionSpec
	start int
	end   int
}

type confirmOptionRow struct {
	options []confirmOptionHit
}

type confirmOptionHitbox struct {
	action     confirmDialogAction
	minX, maxX int
	minY, maxY int
}

func (m *Model) confirmOptionSpecs() []confirmOptionSpec {
	if m.confirm.request == nil {
		return nil
	}
	if toolNameKey(m.confirm.request.ToolName) == tools.NameDone {
		if m.confirm.request.ForceDenyReason {
			return []confirmOptionSpec{
				{action: confirmDialogView, hint: hint("V", "View")},
				{action: confirmDialogDenyReason, hint: hint("Esc/R", "Deny+Reason")},
			}
		}
		return []confirmOptionSpec{
			{action: confirmDialogAllow, hint: hint("Enter/A", "Allow")},
			{action: confirmDialogView, hint: hint("V", "View")},
			{action: confirmDialogDenyReason, hint: hint("Esc/R", "Deny+Reason")},
		}
	}
	allow := hint("Enter/A", "Allow")
	if toolNameKey(m.confirm.request.ToolName) == tools.NameDelete {
		allow.danger = true
	}
	return []confirmOptionSpec{
		{action: confirmDialogAllow, hint: allow},
		{action: confirmDialogDeny, hint: hint("Esc/D", "Deny")},
		{action: confirmDialogDenyReason, hint: hint("R", "Deny+Reason")},
		{action: confirmDialogEdit, hint: hint("E", "Edit args")},
		{action: confirmDialogAddRule, hint: hint("M", "Remember…")},
	}
}

func renderConfirmOption(spec confirmOptionSpec) string {
	return renderHintChip(spec.hint)
}

func (m *Model) confirmOptionRows() []confirmOptionRow {
	specs := m.confirmOptionSpecs()
	width := max(confirmDialogInnerWidth(m.width), 1)
	rows := layoutConfirmOptionRows(specs, width)
	maxRows := max(overlayHeight(m.confirmDialogArea())-4, 1)
	if len(rows) <= maxRows {
		return rows
	}
	// Compact actions retain the decision and its actual exit, even when the
	// auxiliary edit/view/rule shortcuts do not fit.
	var compact []confirmOptionSpec
	hasDeny := false
	for _, spec := range specs {
		if spec.action == confirmDialogDeny {
			hasDeny = true
		}
	}
	for _, spec := range specs {
		if spec.action == confirmDialogAllow || spec.action == confirmDialogDeny || spec.action == confirmDialogDenyReason && !hasDeny {
			compact = append(compact, spec)
		}
	}
	return layoutConfirmOptionRows(compact, width)
}

func layoutConfirmOptionRows(specs []confirmOptionSpec, width int) []confirmOptionRow {
	var rows []confirmOptionRow
	current := confirmOptionRow{}
	lineWidth := 0
	flush := func() {
		if len(current.options) == 0 {
			return
		}
		rows = append(rows, current)
		current = confirmOptionRow{}
		lineWidth = 0
	}
	for _, spec := range specs {
		if spec.action == confirmDialogView || spec.action == confirmDialogEdit {
			flush()
		}
		optionWidth := ansi.StringWidth(renderConfirmOption(spec))
		if len(current.options) > 0 && lineWidth+2+optionWidth > width {
			flush()
		}
		start := lineWidth
		if len(current.options) > 0 {
			start += 2
		}
		current.options = append(current.options, confirmOptionHit{spec: spec, start: start, end: start + optionWidth})
		lineWidth = start + optionWidth
	}
	flush()
	return rows
}

func (m *Model) handleConfirmMouseClick(mouse tea.Mouse) tea.Cmd {
	action, ok := m.confirmOptionAt(mouse.X, mouse.Y)
	if !ok {
		return nil
	}
	return m.handleConfirmAction(action)
}

func (m *Model) confirmOptionAt(x, y int) (confirmDialogAction, bool) {
	for _, hitbox := range m.confirmOptionHitboxes() {
		if x >= hitbox.minX && x < hitbox.maxX && y >= hitbox.minY && y < hitbox.maxY {
			return hitbox.action, true
		}
	}
	return 0, false
}

func (m *Model) confirmOptionHitboxes() []confirmOptionHitbox {
	if m.confirm.request == nil || m.confirm.editing || m.confirm.pickingRule || m.confirm.denyingWithReason {
		return nil
	}

	rows := m.confirmOptionRows()
	if len(rows) == 0 {
		return nil
	}

	dialog := m.renderConfirmDialog()
	dialogRect := m.overlayRect(dialog)
	contentLeft := dialogRect.Min.X + DirectoryBorderStyle.GetBorderLeftSize() + DirectoryBorderStyle.GetPaddingLeft()
	contentTop := dialogRect.Min.Y + DirectoryBorderStyle.GetBorderTopSize() + DirectoryBorderStyle.GetPaddingTop()
	innerWidth := confirmDialogInnerWidth(m.width)
	// The shared layout may compact away the scroll hint; action rows themselves
	// are already selected by confirmOptionRows and stay at the end of the frame.
	cfg := OverlayConfig{Title: "⚠ Confirmation Required", MaxWidth: confirmDialogWidth(m.width), Hint: m.renderConfirmOptions(), CompactHint: m.renderConfirmOptions()}
	if m.confirm.scroll.total > m.confirm.scroll.visible {
		cfg.Hint = appendHintChip(cfg.Hint, hint("PgUp/PgDn", "scroll"))
	}
	layout := layoutOverlay(cfg, m.confirmDialogArea())
	optionStart := lipgloss.Height(dialog) - DirectoryBorderStyle.GetVerticalFrameSize() - len(layout.hintLines)
	hitboxes := make([]confirmOptionHitbox, 0, len(rows)*2)
	for rowIndex, row := range rows {
		if rowIndex >= len(layout.hintLines) {
			break
		}
		y := contentTop + optionStart + rowIndex
		for _, option := range row.options {
			start := min(max(option.start, 0), innerWidth)
			end := min(max(option.end, start), innerWidth)
			if end <= start || y >= dialogRect.Max.Y-1 {
				continue
			}
			hitboxes = append(hitboxes, confirmOptionHitbox{action: option.spec.action, minX: contentLeft + start, maxX: contentLeft + end, minY: y, maxY: y + 1})
		}
	}
	return hitboxes
}
