package tui

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/mattn/go-runewidth"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/tools"
)

func (b *Block) renderUserLocalShell(width int, spinnerFrame string) []string {
	const toolName = tools.NameShell
	style := UserCardStyle
	boxWidth := width - style.GetHorizontalMargins()
	if boxWidth < 10 {
		boxWidth = 10
	}
	innerWidth := boxWidth - style.GetHorizontalPadding() - style.GetHorizontalBorderSize()
	if innerWidth < 10 {
		innerWidth = 10
	}
	innerWidth = clampCardInnerWidth(innerWidth, style, maxTextWidth)
	contentWidth := min(innerWidth-4, maxTextWidth)

	argsJSON, _ := json.Marshal(map[string]string{"command": b.UserLocalShellCmd})
	argsStr := string(argsJSON)
	mainPart := firstDisplayLine(b.UserLocalShellCmd)
	grayPart := ""
	paramSummary := formatToolHeaderParams(toolName, argsStr)
	if paramSummary == "" {
		paramSummary = extractToolParams(argsStr, innerWidth-16)
	} else if runewidth.StringWidth(paramSummary) > innerWidth-16 {
		paramSummary = runewidth.Truncate(paramSummary, innerWidth-16, "…")
	}

	pseudo := &Block{
		ToolName:               toolName,
		ResultContent:          b.UserLocalShellResult,
		ResultStatus:           agent.ToolResultStatusSuccess,
		ResultDone:             !b.UserLocalShellPending,
		ToolCallDetailExpanded: !b.UserLocalShellPending && !b.Collapsed,
	}
	if b.UserLocalShellFailed {
		pseudo.ResultStatus = agent.ToolResultStatusError
	}
	prefix := pseudo.renderToolPrefix(spinnerFrame)
	isActive := b.UserLocalShellPending && spinnerFrame != ""

	var bashLines []string
	addHeader := func() {
		if isActive {
			headerLine := "  " + prefix + " " + ToolCallStyle.Render(toolName)
			if mainPart != "" || grayPart != "" {
				headerLine += " " + mainPart
				if grayPart != "" {
					headerLine += " " + DimStyle.Render(grayPart)
				}
			} else if paramSummary != "" {
				headerLine += " " + DimStyle.Render(paramSummary)
			}
			bashLines = append(bashLines, headerLine)
		} else {
			headerLine := fmt.Sprintf("  %s %s", prefix, toolName)
			if mainPart != "" || grayPart != "" {
				headerLine += " " + mainPart
				if grayPart != "" {
					headerLine += " " + DimStyle.Render(grayPart)
				}
			} else if paramSummary != "" {
				headerLine += " " + DimStyle.Render(paramSummary)
			}
			bashLines = append(bashLines, ToolCallStyle.Render(headerLine))
		}
	}

	switch {
	case b.UserLocalShellPending:
		addHeader()
	case !b.Collapsed && strings.TrimSpace(b.UserLocalShellResult) != "":
		addHeader()
		appendBashCommandBlock(&bashLines, b.UserLocalShellCmd, contentWidth, true, false)
		if b.UserLocalShellFailed {
			bashLines = append(bashLines, ErrorStyle.Render("  ↳ Error:"))
		}
		for _, line := range wrapText(sanitizeToolDisplayText(b.UserLocalShellResult), contentWidth) {
			bashLines = append(bashLines, DimStyle.Render("    "+line))
		}
	default:
		addHeader()
		appendBashCommandBlock(&bashLines, b.UserLocalShellCmd, contentWidth, false, true)
		if b.UserLocalShellResult != "" {
			lineCount := strings.Count(b.UserLocalShellResult, "\n") + 1
			summary := truncateOneLine(sanitizeToolDisplayText(b.UserLocalShellResult), innerWidth-26)
			if b.UserLocalShellFailed {
				bashLines = append(bashLines, ErrorStyle.Render(fmt.Sprintf("  ↳ %s (%d lines)", summary, lineCount)))
			} else {
				bashLines = append(bashLines, ToolResultStyle.Render(fmt.Sprintf("  ↳ %s (%d lines)", summary, lineCount)))
			}
			if hidden := len(wrapText(sanitizeToolDisplayText(b.UserLocalShellResult), contentWidth)) - 1; hidden > 0 {
				bashLines = append(bashLines, renderToolExpandHint("  ", hidden))
			}
		}
	}

	var finalLines []string
	label := "TERMINAL"
	finalLines = append(finalLines, UserLabelStyle.Render(blockLabelWithID(label, b.ID)))
	finalLines = append(finalLines, "")
	finalLines = append(finalLines, bashLines...)

	cardBg := currentTheme.UserCardBg
	finalLines = preserveCardBg(finalLines, cardBg)
	return renderPrewrappedCard(style, innerWidth, finalLines, cardBg, railANSISeq("user", b.Focused))
}

func (b *Block) renderUser(width int, spinnerFrame string) []string {
	if b.UserLocalShellCmd != "" {
		return b.renderUserLocalShell(width, spinnerFrame)
	}
	return b.renderUserPlain(width)
}

func (b *Block) renderUserPlain(width int) []string {
	style := UserCardStyle
	boxWidth := width - style.GetHorizontalMargins()
	if boxWidth < 10 {
		boxWidth = 10
	}
	innerWidth := boxWidth - style.GetHorizontalPadding() - style.GetHorizontalBorderSize()
	if innerWidth < 10 {
		innerWidth = 10
	}
	innerWidth = clampCardInnerWidth(innerWidth, style, maxTextWidth)
	contentWidth := min(innerWidth-2, maxTextWidth)

	if strings.TrimSpace(b.Content) == "" && b.ImageCount == 0 && len(b.FileRefs) == 0 && len(b.PDFNames) == 0 {
		return nil
	}

	var finalLines []string
	label := "USER"
	if b.LoopAnchor {
		label = "USER · LOOP"
	}
	finalLines = append(finalLines, UserLabelStyle.Render(blockLabelWithID(label, b.ID)))
	finalLines = append(finalLines, "")
	if strings.TrimSpace(b.Content) != "" {
		for _, l := range renderUserText(b.Content, contentWidth) {
			finalLines = append(finalLines, "  "+l)
		}
	}
	imagesRendered := b.appendImagePreviewLines(&finalLines, contentWidth, currentTheme.UserCardBg, style.GetPaddingTop(), strings.TrimSpace(b.Content) != "")

	if b.ImageCount > 0 && !imagesRendered {
		if strings.TrimSpace(b.Content) != "" {
			finalLines = append(finalLines, "")
		}
		imageLabel := "  📎"
		if b.ImageCount > 1 {
			imageLabel = fmt.Sprintf("  📎 %d", b.ImageCount)
		}
		finalLines = append(finalLines, DimStyle.Render(imageLabel))
	}
	if len(b.PDFNames) > 0 {
		if strings.TrimSpace(b.Content) != "" || b.ImageCount > 0 || imagesRendered {
			finalLines = append(finalLines, "")
		}
		for _, name := range b.PDFNames {
			finalLines = append(finalLines, DimStyle.Render("  📄 "+name))
		}
	}
	if len(b.FileRefs) > 0 {
		if strings.TrimSpace(b.Content) != "" || b.ImageCount > 0 || imagesRendered {
			finalLines = append(finalLines, "")
		}
		for _, ref := range b.FileRefs {
			finalLines = append(finalLines, DimStyle.Render("  ⎿  @"+ref))
		}
	}

	cardBg := currentTheme.UserCardBg
	finalLines = preserveCardBg(finalLines, cardBg)
	return renderPrewrappedCard(style, innerWidth, finalLines, cardBg, railANSISeq("user", b.Focused))
}
