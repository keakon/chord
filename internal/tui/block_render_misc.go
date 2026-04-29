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
	lines := wrapText(b.Content, innerWidth)
	var innerLines []string
	for i, line := range lines {
		if i == 0 {
			innerLines = append(innerLines, ErrorStyle.Render("✗ "+line))
		} else {
			innerLines = append(innerLines, ErrorStyle.Render("  "+line))
		}
	}
	if len(innerLines) == 0 {
		innerLines = append(innerLines, ErrorStyle.Render("✗ unknown error"))
	}

	cardBg := currentTheme.ErrorCardBg
	innerLines = preserveCardBg(innerLines, cardBg)
	return renderPrewrappedCard(style, innerWidth, innerLines, cardBg, "")
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

	title := b.StatusTitle
	if title == "" {
		// Fallback: extract title from first line of Content (session restore).
		if idx := strings.Index(b.Content, "\n"); idx >= 0 {
			title = strings.TrimSpace(b.Content[:idx])
		}
	}
	label := ThinkingLabelStyle.Render(title)

	bodyLines := renderRichMarkdownContent(strings.TrimSpace(b.Content), innerWidth-2, &b.richMarkdownHL)
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
	label := ThinkingLabelStyle.Render("CONTEXT SUMMARY")
	bodyLines := renderRichMarkdownContent(strings.TrimSpace(b.Content), innerWidth, &b.richMarkdownHL)
	if len(bodyLines) == 0 {
		bodyLines = []string{""}
	}
	lines := make([]string, 0, len(bodyLines)+4)
	lines = append(lines, label, "")
	lines = append(lines, bodyLines...)
	if b.Collapsed {
		lines = append(lines, "", DimStyle.Render("[space] expand full preserved context"))
	} else {
		lines = append(lines, "", DimStyle.Render("[space] collapse to preview"))
	}
	cardBg := currentTheme.CompactionSummaryBg
	lines = preserveCardBg(lines, cardBg)
	return renderPrewrappedCard(style, innerWidth, lines, cardBg, railANSISeq("assistant", b.Focused))
}
