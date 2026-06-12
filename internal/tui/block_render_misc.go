package tui

import "strings"

func (b *Block) renderError(width int) []string {
	style := ErrorCardStyle
	// v2: Width() sets border-box (excl margin).
	boxWidth := width - style.GetHorizontalMargins()
	if boxWidth < 10 {
		boxWidth = 10
	}
	innerWidth := boxWidth - style.GetHorizontalPadding() - style.GetHorizontalBorderSize()
	if innerWidth < 10 {
		innerWidth = 10
	}
	innerWidth = clampCardInnerWidth(innerWidth, style, maxProseWidth)
	lines := []string{ErrorStyle.Render(blockLabelWithID("ERROR", b.ID)), ""}
	wrapped := wrapText(b.Content, innerWidth)
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

	cardBg := currentTheme.ErrorCardBg
	lines = preserveCardBg(lines, cardBg)
	return renderPrewrappedCard(style, innerWidth, lines, cardBg, "")
}

func (b *Block) renderStatus(width int) []string {
	style := CompactionSummaryCardStyle
	boxWidth := width - style.GetHorizontalMargins()
	if boxWidth < 10 {
		boxWidth = 10
	}
	innerWidth := boxWidth - style.GetHorizontalPadding() - style.GetHorizontalBorderSize()
	if innerWidth < 10 {
		innerWidth = 10
	}
	innerWidth = clampCardInnerWidth(innerWidth, style, maxProseWidth)
	contentWidth := min(innerWidth-2, maxProseWidth)

	title := b.StatusTitle
	if title == "" {
		// Fallback: extract title from first line of Content (session restore).
		if idx := strings.Index(b.Content, "\n"); idx >= 0 {
			title = strings.TrimSpace(b.Content[:idx])
		}
	}
	label := ThinkingLabelStyle.Render(blockLabelWithID(title, b.ID))

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
	marker := "··· " + content + " ···"
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

func (b *Block) renderCompactionSummary(width int) []string {
	style := CompactionSummaryCardStyle
	boxWidth := width - style.GetHorizontalMargins()
	if boxWidth < 10 {
		boxWidth = 10
	}
	innerWidth := boxWidth - style.GetHorizontalPadding() - style.GetHorizontalBorderSize()
	if innerWidth < 10 {
		innerWidth = 10
	}
	innerWidth = clampCardInnerWidth(innerWidth, style, maxProseWidth)
	contentWidth := min(innerWidth, maxProseWidth)
	label := ThinkingLabelStyle.Render(blockLabelWithID("CONTEXT SUMMARY", b.ID))
	bodyLines := renderRichMarkdownContent(strings.TrimSpace(b.Content), contentWidth, &b.richMarkdownHL)
	if len(bodyLines) == 0 {
		bodyLines = []string{""}
	}
	lines := make([]string, 0, len(bodyLines)+4)
	lines = append(lines, label, "")
	lines = append(lines, bodyLines...)
	if b.Collapsed {
		lines = append(lines, "", DimStyle.Render("[space] toggle expand/collapse"))
	} else {
		lines = append(lines, "", DimStyle.Render("[space] collapse to preview"))
	}
	cardBg := currentTheme.CompactionSummaryBg
	lines = preserveCardBg(lines, cardBg)
	return renderPrewrappedCard(style, innerWidth, lines, cardBg, railANSISeq("assistant", b.Focused))
}
