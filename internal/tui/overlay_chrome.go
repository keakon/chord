package tui

import (
	"strings"

	"github.com/keakon/x/ansi"
)

// ---------------------------------------------------------------------------
// Shared dialog chrome: key chips, tab rows and filter rows.
// ---------------------------------------------------------------------------

// hintChip is one footer item: "[Keys] action", or prose when keys is empty.
type hintChip struct {
	keys    string
	action  string
	primary bool
	danger  bool
}

// Importance and destructive intent are independent of focus and key dispatch.
func primaryHint(keys, action string) hintChip {
	return hintChip{keys: keys, action: action, primary: true}
}

func dangerHint(keys, action string) hintChip {
	return hintChip{keys: keys, action: action, danger: true}
}

func hint(keys, action string) hintChip { return hintChip{keys: keys, action: action} }

func hintText(text string) hintChip { return hintChip{action: text} }

func renderHintChip(c hintChip) string {
	if c.keys == "" {
		return currentStyles.DimStyle.Render(c.action)
	}
	text := "[" + c.keys + "] " + c.action
	if c.danger {
		return currentStyles.DialogDangerStyle.Bold(c.primary).Render(text)
	}
	if c.primary {
		return currentStyles.DialogPrimaryStyle.Render(text)
	}
	return currentStyles.KeyHintStyle.Render("["+c.keys+"]") + " " + currentStyles.DimStyle.Render(c.action)
}

// hintLine renders one footer line: chips joined by two spaces. Callers build
// hints while rendering, never in package-level initializers, because the chip
// styles only exist after ApplyTheme.
func hintLine(chips ...hintChip) string {
	parts := make([]string, len(chips))
	for i, c := range chips {
		parts[i] = renderHintChip(c)
	}
	return strings.Join(parts, "  ")
}

// appendHintText appends a prose chip (for example a scroll counter) to a hint.
func appendHintText(hint string, text string) string {
	if text == "" {
		return hint
	}
	return appendHintChip(hint, hintText(text))
}

// appendHintChip appends one chip to an existing hint line.
func appendHintChip(hint string, chip hintChip) string {
	if hint == "" {
		return renderHintChip(chip)
	}
	return hint + "  " + renderHintChip(chip)
}

// wrapHintLines wraps rendered hint chips to width. Chips are atomic, so a key
// never separates from its action, and an oversized chip is truncated with
// ANSI-safe resets instead of splitting escape sequences.
func wrapHintLines(hint string, width int) []string {
	if hint == "" || width <= 0 {
		return nil
	}
	var out []string
	for logical := range strings.SplitSeq(hint, "\n") {
		line := ""
		lineWidth := 0
		for chip := range strings.SplitSeq(logical, "  ") {
			if chip == "" {
				continue
			}
			chipWidth := ansi.StringWidth(chip)
			if chipWidth > width {
				chip = ansi.Truncate(chip, width, "…")
				chipWidth = ansi.StringWidth(chip)
			}
			switch {
			case line == "":
				line, lineWidth = chip, chipWidth
			case lineWidth+2+chipWidth <= width:
				line += "  " + chip
				lineWidth += 2 + chipWidth
			default:
				out = append(out, line)
				line, lineWidth = chip, chipWidth
			}
		}
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

// splitHintChips flattens wrapped hint lines back into their chips.
func splitHintChips(lines []string) []string {
	chips := make([]string, 0, len(lines))
	for _, line := range lines {
		for chip := range strings.SplitSeq(line, "  ") {
			if chip != "" {
				chips = append(chips, chip)
			}
		}
	}
	return chips
}

// hintKeyOnly keeps just the key of a rendered chip, so a hint that runs out of
// room can still name the action's binding.
func hintKeyOnly(chip string) string {
	plain := ansi.Strip(chip)
	if !strings.HasPrefix(plain, "[") {
		return ""
	}
	end := strings.IndexByte(plain, ']')
	if end < 0 {
		return ""
	}
	return currentStyles.KeyHintStyle.Render(plain[:end+1])
}

// packHintChips fits chips into at most maxLines rows of width. A chip that
// does not fit is moved to the next row; the trailing chip falls back to its
// key plus "…" and any remaining chips are dropped whole. Cutting a chip
// mid-word leaves a fragment such as "[M…" that names no action and owns no
// hit target, while dropping the exit key entirely hides how to back out.
func packHintChips(chips []string, width, maxLines int) []string {
	if width <= 0 || maxLines <= 0 {
		return nil
	}
	lines := make([]string, 0, maxLines)
	current := ""
	currentWidth := 0
	dropped := false
	for i, chip := range chips {
		chipWidth := ansi.StringWidth(chip)
		if chipWidth > width {
			chip = ansi.Truncate(chip, width, "…")
			chipWidth = ansi.StringWidth(chip)
		}
		if current == "" {
			if len(lines) == maxLines {
				dropped = true
				break
			}
			current, currentWidth = chip, chipWidth
			continue
		}
		if currentWidth+2+chipWidth <= width {
			current += "  " + chip
			currentWidth += 2 + chipWidth
			continue
		}
		if i == len(chips)-1 {
			if key := hintKeyOnly(chip); key != "" && currentWidth+2+ansi.StringWidth(key)+1 <= width {
				lines = append(lines, current+"  "+key+currentStyles.DimStyle.Render("…"))
				return lines
			}
		}
		lines = append(lines, current)
		if len(lines) == maxLines {
			dropped = true
			break
		}
		current, currentWidth = chip, chipWidth
	}
	if current != "" && len(lines) < maxLines {
		lines = append(lines, current)
	}
	if dropped && len(lines) > 0 {
		last := lines[len(lines)-1]
		switch {
		case ansi.StringWidth(last)+3 <= width:
			lines[len(lines)-1] = last + "  …"
		case ansi.StringWidth(last)+1 <= width:
			lines[len(lines)-1] = last + "…"
		default:
			lines[len(lines)-1] = ansi.Truncate(last, width, "…")
		}
	}
	return lines
}

// renderTabRow renders a dialog tab strip with the active tab highlighted.
func renderTabRow(labels []string, active int) string {
	parts := make([]string, len(labels))
	for i, label := range labels {
		if i == active {
			parts[i] = currentStyles.TabActiveStyle.Render(label)
			continue
		}
		parts[i] = currentStyles.TabStyle.Render(label)
	}
	return strings.Join(parts, " ")
}

const filterPlaceholder = "(press / to search)"

// renderFilterLine renders the filter row shared by list dialogs: a dim
// "filter: " label, the query in body colour with a caret while the input is
// focused, an optional dim right-aligned counter, and the idle placeholder.
// A long query keeps its tail visible, because the caret sits at the end.
func renderFilterLine(query string, focused bool, trailing string, width int) string {
	if width <= 0 {
		return ""
	}
	const label = "filter: "
	idle := !focused && strings.TrimSpace(query) == ""

	trailingPart, trailingWidth := "", 0
	if trailing != "" {
		trailingWidth = ansi.StringWidth(trailing)
		// Keep the counter only while it leaves room for a readable query.
		if width >= ansi.StringWidth(label)+trailingWidth+8 {
			trailingPart = currentStyles.DimStyle.Render(trailing)
		} else {
			trailingWidth = 0
		}
	}

	if idle {
		placeholder := label + filterPlaceholder
		if trailingWidth == 0 {
			return ansi.Truncate(currentStyles.DimStyle.Render(placeholder), width, "…")
		}
		// The placeholder alone can exceed the row: truncate it so the counter
		// stays inside the dialog with one separating space.
		if room := width - trailingWidth - 1; ansi.StringWidth(placeholder) > room {
			placeholder = ansi.Truncate(placeholder, room, "…")
		}
		pad := max(width-trailingWidth-ansi.StringWidth(placeholder), 1)
		return currentStyles.DimStyle.Render(placeholder) + strings.Repeat(" ", pad) + trailingPart
	}

	caret := ""
	if focused {
		caret = "_"
	}
	avail := width - trailingWidth
	if trailingWidth > 0 {
		avail -= 2
	}
	if avail < ansi.StringWidth(label)+len(caret)+1 {
		trailingPart, trailingWidth, avail = "", 0, width
	}
	queryWidth := max(avail-ansi.StringWidth(label)-len(caret), 1)
	q := query
	if queryWidth := min(queryWidth, max(width-ansi.StringWidth(label)-len(caret), 1)); ansi.StringWidth(q) > queryWidth {
		q = ansi.TruncateLeft(q, ansi.StringWidth(q)-queryWidth+1, "…")
	}
	line := currentStyles.DimStyle.Render(label) + q + caret
	if trailingWidth == 0 {
		return ansi.Truncate(line, width, "…")
	}
	pad := max(width-trailingWidth-ansi.StringWidth(label)-ansi.StringWidth(q)-len(caret), 1)
	return line + strings.Repeat(" ", pad) + trailingPart
}
