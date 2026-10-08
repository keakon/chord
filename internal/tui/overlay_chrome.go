package tui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// ---------------------------------------------------------------------------
// Shared dialog chrome: key chips, tab rows and filter rows.
// ---------------------------------------------------------------------------

// hintChip is one footer item: "[Keys] action", or prose when keys is empty.
type hintChip struct {
	keys   string
	action string
}

func hint(keys, action string) hintChip { return hintChip{keys: keys, action: action} }

func hintText(text string) hintChip { return hintChip{action: text} }

func renderHintChip(c hintChip) string {
	if c.keys == "" {
		return DimStyle.Render(c.action)
	}
	return KeyHintStyle.Render("["+c.keys+"]") + " " + DimStyle.Render(c.action)
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
	for _, logical := range strings.Split(hint, "\n") {
		line := ""
		lineWidth := 0
		for _, chip := range strings.Split(logical, "  ") {
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

// renderTabRow renders a dialog tab strip with the active tab highlighted.
func renderTabRow(labels []string, active int) string {
	parts := make([]string, len(labels))
	for i, label := range labels {
		if i == active {
			parts[i] = TabActiveStyle.Render(label)
			continue
		}
		parts[i] = TabStyle.Render(label)
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
			trailingPart = DimStyle.Render(trailing)
		} else {
			trailingWidth = 0
		}
	}

	if idle {
		placeholder := label + filterPlaceholder
		if trailingWidth == 0 {
			return ansi.Truncate(DimStyle.Render(placeholder), width, "…")
		}
		// The placeholder alone can exceed the row: truncate it so the counter
		// stays inside the dialog with one separating space.
		if room := width - trailingWidth - 1; ansi.StringWidth(placeholder) > room {
			placeholder = ansi.Truncate(placeholder, room, "…")
		}
		pad := max(width-trailingWidth-ansi.StringWidth(placeholder), 1)
		return DimStyle.Render(placeholder) + strings.Repeat(" ", pad) + trailingPart
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
	line := DimStyle.Render(label) + q + caret
	if trailingWidth == 0 {
		return ansi.Truncate(line, width, "…")
	}
	pad := max(width-trailingWidth-ansi.StringWidth(label)-ansi.StringWidth(q)-len(caret), 1)
	return line + strings.Repeat(" ", pad) + trailingPart
}
