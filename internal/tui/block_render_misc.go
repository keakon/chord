package tui

import (
	"strconv"
	"strings"

	"github.com/keakon/chord/internal/message"
)

// maxCollapsedStatusBodyLines caps the body a folded runtime status or error
// card shows. The opening lines are the summary; the tail hides behind the same
// disclosure marker a tool card uses.
const maxCollapsedStatusBodyLines = 3

// statusCardIsFoldable reports whether a BlockStatus card may fold. Sub-agent
// mailbox cards are excluded because their body is the worker model's own
// message — model output that must stay fully visible. Every other status card
// carries harness/runtime text (loop notices, context-pressure notices, info
// notices, export/diagnostics, JOB RESULT).
func (b *Block) statusCardIsFoldable() bool {
	return b != nil && b.Type == BlockStatus && b.StatusFrom == "" && b.StatusKind == ""
}

// statusDisclosureMarker returns the disclosure glyph for a folded or expanded
// card, matching the tool card markers.
func statusDisclosureMarker(collapsed bool) string {
	if collapsed {
		return toolDisclosureCollapsed
	}
	return toolDisclosureExpanded
}

// foldStatusBodyLines keeps the first maxCollapsedStatusBodyLines rendered lines
// and reports how many lines are hidden. A body that already fits the summary is
// returned unchanged with hidden == 0, so the caller adds no disclosure marker
// to a card with nothing behind it.
func foldStatusBodyLines(lines []string) (kept []string, hidden int) {
	if len(lines) <= maxCollapsedStatusBodyLines {
		return lines, 0
	}
	kept = append([]string(nil), lines[:maxCollapsedStatusBodyLines]...)
	for len(kept) > 0 && strings.TrimSpace(stripANSI(kept[len(kept)-1])) == "" {
		kept = kept[:len(kept)-1]
	}
	return kept, len(lines) - len(kept)
}

func (b *Block) renderError(width int) []string {
	style := ErrorCardStyle
	// Reserve a column for the conversation rail (foreground-only "│" prepended
	// outside the card width); otherwise a full-width card overflows the
	// terminal. v2: Width() sets border-box (excl margin).
	boxWidth := max((width-railWidthToReserve(style))-style.GetHorizontalMargins(), 10)
	innerWidth := max(boxWidth-style.GetHorizontalPadding()-style.GetHorizontalBorderSize(), 10)
	contentWidth := min(innerWidth-2, maxProseWidth)
	// Same shape as the status and checkpoint cards: badge, blank line, body
	// indented by two. The badge, the error background and the error rail
	// already say this is a failure, so the body carries no extra glyph, and the
	// body stays fully visible: an error is short and must be readable without
	// expanding a card.
	lines := []string{ErrorLabelStyle.Render(blockLabelWithID("ERROR", b.displayLabelID())), ""}
	wrapped := wrapText(sanitizeDisplayText(b.Content), contentWidth)
	if len(wrapped) == 0 {
		wrapped = []string{"unknown error"}
	}
	for _, line := range wrapped {
		lines = append(lines, ErrorStyle.Render("  "+line))
	}
	if b.errorHint != "" {
		// The error panel holds the structured details (provider, model, masked
		// key, status code, retry history) this card intentionally omits.
		lines = append(lines, "")
		for _, line := range wrapText(sanitizeDisplayText(b.errorHint), contentWidth) {
			lines = append(lines, DimStyle.Render("  "+line))
		}
	}

	cardBg := currentTheme.ErrorCardBg
	lines = preserveCardBg(lines, cardBg)
	return renderPrewrappedCard(style, innerWidth, lines, cardBg, railANSISeq("error", b.Focused))
}

func (b *Block) renderStatus(width int) []string {
	if b.isBackgroundResultCard() {
		return b.renderBackgroundResult(width)
	}
	style := CompactionSummaryCardStyle
	// Reserve a column for the conversation rail (foreground-only "│" prepended
	// outside the card width); otherwise a full-width card overflows the terminal.
	boxWidth := max((width-railWidthToReserve(style))-style.GetHorizontalMargins(), 10)
	innerWidth := max(boxWidth-style.GetHorizontalPadding()-style.GetHorizontalBorderSize(), 10)
	contentWidth := min(innerWidth-2, maxProseWidth)

	title := strings.TrimSpace(sanitizeDisplayText(b.StatusTitle))
	if title == "" {
		// An untitled card used to borrow its first body line as the badge,
		// which both duplicated that line and left a blank badge whenever the
		// body was a single line. Name it instead.
		title = infoCardTitle
	}
	label := ThinkingLabelStyle.Render(blockLabelWithID(title, b.displayLabelID()))

	// A sub-agent mailbox card names its sender and kind as field rows rather
	// than flattening them into "[agent] kind: …" inside the prose: the rows
	// then read with the same grammar as a tool card's, and the body is left
	// as the message itself. Every other status card leaves both empty.
	metaLines := make([]string, 0, 2)
	if b.StatusFrom != "" {
		metaLines = append(metaLines, toolFieldInline(ToolResultExpandedStyle, "From", sanitizeDisplayText(b.StatusFrom)))
	}
	if b.StatusKind != "" {
		// The kind is a value, not a key, so it keeps its raw spelling: labels
		// get humanized, enum values stay greppable.
		metaLines = append(metaLines, toolFieldInline(ToolResultExpandedStyle, "Kind", sanitizeDisplayText(b.StatusKind)))
	}

	// The From/Kind field rows above the body use a 2-space lead plus a "↳ "
	// connector, which puts their labels at visual column 4. The body must
	// nest under that header rather than sit at the lead/connector column,
	// so when the field rows are present it is indented 2 more and rendered
	// 2 columns narrower. Without the field rows (the info card), the body
	// keeps the original 2-space indent.
	bodyIndent := "  "
	bodyWidth := contentWidth
	if len(metaLines) > 0 {
		bodyIndent = "    "
		bodyWidth = max(contentWidth-2, 10)
	}
	bodyLines := renderRichMarkdownContent(strings.TrimSpace(b.Content), bodyWidth, &b.richMarkdownHL)
	if len(bodyLines) == 0 {
		bodyLines = []string{""}
	}
	// Runtime status cards carry harness text, not model output, so they start
	// folded to their opening lines. A sub-agent mailbox card carries a worker
	// model's message and stays fully visible; a card whose body already fits
	// the summary grows no disclosure marker.
	if b.statusCardIsFoldable() {
		if folded, hidden := foldStatusBodyLines(bodyLines); hidden > 0 {
			label += " " + statusDisclosureMarker(b.Collapsed)
			if b.Collapsed {
				bodyLines = append(folded, DimStyle.Render("  ... "+strconv.Itoa(hidden)+" more lines hidden."))
			}
		}
	}
	lines := make([]string, 0, len(metaLines)+len(bodyLines)+2)
	lines = append(lines, label, "")
	lines = append(lines, metaLines...)
	for _, line := range bodyLines {
		lines = append(lines, bodyIndent+line)
	}

	cardBg := currentTheme.CompactionSummaryBg
	lines = preserveCardBg(lines, cardBg)
	return renderPrewrappedCard(style, innerWidth, lines, cardBg, railANSISeq("thinking", b.Focused))
}

func (b *Block) renderBoundaryMarker(width int) []string {
	if width <= 0 {
		width = 80
	}
	content := strings.TrimSpace(b.Content)
	if content == "" {
		content = "History truncated"
	}
	marker := "··· " + sanitizeDisplayText(content) + " ···"
	lines := wrapText(marker, width)
	for i, line := range lines {
		styled := DimStyle.Render(line)
		if b.Focused {
			styled = FocusedCardStyle.Render(line)
		}
		lines[i] = styled
	}
	return lines
}

// compactionSummaryModeLabel names the compaction mode on the card label. The
// four modes preserve very different amounts of the archived history, and that
// is exactly what a reader scrolling back needs in order to decide whether the
// checkpoint can be trusted or the archives have to be re-read: a model-driven
// checkpoint is built from runtime facts, while a truncate-only one carries no
// summary at all.
func compactionSummaryModeLabel(mode string) (string, bool) {
	switch mode {
	case message.CompactionSummaryModeModelDriven:
		return "MODEL-DRIVEN", false
	case message.CompactionSummaryModeModelSummary:
		return "AUTO", false
	case message.CompactionSummaryModeStructuredFallback:
		return "FALLBACK", true
	case message.CompactionSummaryModeTruncateOnly:
		return "TRUNCATED", true
	}
	// Unknown or unrecorded (pre-mode sessions whose body no longer carries the
	// generated sentence): claim nothing rather than guess.
	return "", false
}

func renderCompactionSummaryLabel(b *Block) string {
	label := blockLabelWithID("CONTEXT SUMMARY", b.displayLabelID())
	mode, degraded := compactionSummaryModeLabel(b.CompactionSummaryMode)
	switch {
	case mode == "":
		return ThinkingLabelStyle.Render(label)
	case degraded:
		// A degraded checkpoint (a fallback digest, or no summary at all) is a
		// warning about the history behind it, so its mode is styled apart
		// from the badge. The badge's own padding separates the two.
		return ThinkingLabelStyle.Render(label) + LSPWarnStyle.Render("· "+mode)
	default:
		// One badge, so the label does not carry two lots of badge padding.
		return ThinkingLabelStyle.Render(label + " · " + mode)
	}
}

func (b *Block) renderCompactionSummary(width int) []string {
	style := CompactionSummaryCardStyle
	// Reserve a column for the conversation rail (foreground-only "│" prepended
	// outside the card width); otherwise a full-width card overflows the terminal.
	boxWidth := max((width-railWidthToReserve(style))-style.GetHorizontalMargins(), 10)
	innerWidth := max(boxWidth-style.GetHorizontalPadding()-style.GetHorizontalBorderSize(), 10)
	contentWidth := min(innerWidth-2, maxProseWidth)
	label := renderCompactionSummaryLabel(b)
	// Compaction summaries are always fully expanded (see Block.Toggle); the
	// complete raw content including any [Context compressed] archive section
	// stays visible, so no [space] hints are rendered.
	//
	// Each protocol region renders as its own Markdown document: a bare
	// "[Session Anchors]" line has no block-level Markdown meaning, so rendering
	// the checkpoint as one document would merge every marker into the paragraph
	// that follows it.
	var bodyLines []string
	sections := splitCompactionSections(b.Content)
	for len(b.compactionSectionHL) < len(sections) {
		b.compactionSectionHL = append(b.compactionSectionHL, nil)
	}
	for i, section := range sections {
		if len(bodyLines) > 0 {
			bodyLines = append(bodyLines, "")
		}
		if section.label != "" {
			bodyLines = append(bodyLines, CompactionSectionLabelStyle.Render(section.label))
		}
		bodyLines = append(bodyLines, renderRichMarkdownContent(section.body, contentWidth, &b.compactionSectionHL[i])...)
	}
	if len(bodyLines) == 0 {
		bodyLines = []string{""}
	}
	lines := make([]string, 0, len(bodyLines)+2)
	lines = append(lines, label, "")
	for _, line := range bodyLines {
		lines = append(lines, "  "+line)
	}
	cardBg := currentTheme.CompactionSummaryBg
	lines = preserveCardBg(lines, cardBg)
	return renderPrewrappedCard(style, innerWidth, lines, cardBg, railANSISeq("assistant", b.Focused))
}
