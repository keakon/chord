package tui

import (
	"strings"

	"github.com/keakon/chord/internal/message"
)

func (b *Block) renderError(width int) []string {
	style := ErrorCardStyle
	// v2: Width() sets border-box (excl margin).
	boxWidth := max(width-style.GetHorizontalMargins(), 10)
	innerWidth := max(boxWidth-style.GetHorizontalPadding()-style.GetHorizontalBorderSize(), 10)
	lines := []string{ErrorStyle.Render(blockLabelWithID("ERROR", b.displayLabelID())), ""}
	wrapped := wrapText(sanitizeDisplayText(b.Content), min(innerWidth, maxProseWidth))
	for i, line := range wrapped {
		if i == 0 {
			lines = append(lines, ErrorStyle.Render("✗ "+line))
		} else {
			lines = append(lines, ErrorStyle.Render("  "+line))
		}
	}
	if len(wrapped) == 0 {
		lines = append(lines, ErrorStyle.Render("✗ unknown error"))
	}
	if b.errorHint != "" {
		// The error panel holds the structured details (provider, model, masked
		// key, status code, retry history) this card intentionally omits.
		lines = append(lines, "", DimStyle.Render(sanitizeDisplayText(b.errorHint)))
	}

	cardBg := currentTheme.ErrorCardBg
	lines = preserveCardBg(lines, cardBg)
	return renderPrewrappedCard(style, innerWidth, lines, cardBg, "")
}

func (b *Block) renderStatus(width int) []string {
	if b.BackgroundObjectID != "" || b.StatusTitle == backgroundResultCardTitle {
		return b.renderBackgroundResult(width)
	}
	style := CompactionSummaryCardStyle
	// Reserve a column for the conversation rail (foreground-only "│" prepended
	// outside the card width); otherwise a full-width card overflows the terminal.
	boxWidth := max((width-railWidthToReserve(style))-style.GetHorizontalMargins(), 10)
	innerWidth := max(boxWidth-style.GetHorizontalPadding()-style.GetHorizontalBorderSize(), 10)
	contentWidth := min(innerWidth-2, maxProseWidth)

	title := sanitizeDisplayText(b.StatusTitle)
	if title == "" {
		// Fallback: extract title from first line of Content (session restore).
		if idx := strings.Index(b.Content, "\n"); idx >= 0 {
			title = strings.TrimSpace(sanitizeDisplayText(b.Content[:idx]))
		}
	}
	label := ThinkingLabelStyle.Render(blockLabelWithID(title, b.displayLabelID()))

	bodyLines := renderRichMarkdownContent(strings.TrimSpace(b.Content), contentWidth, &b.richMarkdownHL)
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
	contentWidth := min(innerWidth, maxProseWidth)
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
	lines = append(lines, bodyLines...)
	cardBg := currentTheme.CompactionSummaryBg
	lines = preserveCardBg(lines, cardBg)
	return renderPrewrappedCard(style, innerWidth, lines, cardBg, railANSISeq("assistant", b.Focused))
}
