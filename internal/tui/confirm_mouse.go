package tui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/tools"
)

type confirmOptionSpec struct {
	action confirmDialogAction
	label  string
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
				{action: confirmDialogView, label: "[V] View"},
				{action: confirmDialogDenyReason, label: "[Esc/R] Deny+Reason required"},
			}
		}
		return []confirmOptionSpec{
			{action: confirmDialogAllow, label: "[Enter/A] Allow"},
			{action: confirmDialogView, label: "[V] View"},
			{action: confirmDialogDenyReason, label: "[Esc/R] Deny+Reason"},
		}
	}
	return []confirmOptionSpec{
		{action: confirmDialogAllow, label: "[Enter/A] Allow"},
		{action: confirmDialogDeny, label: "[Esc/D] Deny"},
		{action: confirmDialogDenyReason, label: "[R] Deny+Reason"},
		{action: confirmDialogView, label: "[V] View args"},
		{action: confirmDialogEdit, label: "[E] Modify args"},
		{action: confirmDialogAddRule, label: "[M] Add rule…"},
	}
}

func renderConfirmOption(spec confirmOptionSpec) string {
	switch spec.action {
	case confirmDialogAllow:
		return ConfirmAllowStyle.Render(spec.label)
	case confirmDialogDeny, confirmDialogDenyReason:
		return ConfirmDenyStyle.Render(spec.label)
	default:
		return ConfirmEditStyle.Render(spec.label)
	}
}

func (m *Model) confirmOptionRows() []confirmOptionRow {
	specs := m.confirmOptionSpecs()
	if len(specs) == 0 {
		return nil
	}

	maxLineWidth := confirmDialogInnerWidth(m.width) - 1
	rows := make([]confirmOptionRow, 0, 2)
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
		optionWidth := ansi.StringWidth(spec.label)
		if len(current.options) > 0 && lineWidth+2+optionWidth > maxLineWidth {
			flush()
		}
		start := lineWidth
		if len(current.options) > 0 {
			start += 2
		}
		current.options = append(current.options, confirmOptionHit{
			spec:  spec,
			start: start,
			end:   start + optionWidth,
		})
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

	req := m.confirm.request
	innerWidth := confirmDialogInnerWidth(m.width)
	summary := buildConfirmSummary(req.ToolName, req.ArgsJSON, req.NeedsApproval, req.AlreadyAllowed, req.DoneReport)
	body := m.renderConfirmSummary("⚠ Confirmation Required", summary, innerWidth)
	body = append(body, "")
	for _, row := range rows {
		parts := make([]string, len(row.options))
		for i, option := range row.options {
			parts[i] = renderConfirmOption(option.spec)
		}
		body = append(body, strings.Join(parts, "  "))
	}

	maxLines := confirmDialogMaxBodyLines(m.height)
	fitted := fitConfirmDialogLines(body, maxLines, len(rows)+1)
	visibleRows := len(rows)
	firstRow := 0
	if len(body) > maxLines {
		preserveTail := len(rows) + 1
		if preserveTail > maxLines-2 {
			preserveTail = max(0, maxLines-2)
		}
		visibleRows = min(len(rows), preserveTail)
		firstRow = len(rows) - visibleRows
	}
	if visibleRows == 0 {
		return nil
	}

	dialog := m.renderConfirmDialog()
	dialogRect := m.overlayRect(dialog)
	contentLeft := dialogRect.Min.X + DirectoryBorderStyle.GetBorderLeftSize() + DirectoryBorderStyle.GetPaddingLeft()
	contentTop := dialogRect.Min.Y + DirectoryBorderStyle.GetBorderTopSize() + DirectoryBorderStyle.GetPaddingTop()
	optionStart := len(fitted) - visibleRows

	hitboxes := make([]confirmOptionHitbox, 0, visibleRows*2)
	for rowIndex := firstRow; rowIndex < len(rows); rowIndex++ {
		row := rows[rowIndex]
		y := contentTop + optionStart + rowIndex - firstRow
		for _, option := range row.options {
			start := min(max(option.start, 0), innerWidth)
			end := min(max(option.end, start), innerWidth)
			if end <= start {
				continue
			}
			hitboxes = append(hitboxes, confirmOptionHitbox{
				action: option.spec.action,
				minX:   contentLeft + start,
				maxX:   contentLeft + end,
				minY:   y,
				maxY:   y + 1,
			})
		}
	}
	return hitboxes
}
