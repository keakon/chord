package tui

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/mattn/go-runewidth"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
	"github.com/keakon/chord/internal/tui/terminaltext"
)

func (b *Block) renderUserLocalShell(width int, spinnerFrame string) []string {
	const toolName = tools.NameShell
	style := currentStyles.UserCardStyle
	// Reserve a column for the conversation rail (foreground-only "│" prepended
	// outside the card width); otherwise a full-width card overflows the terminal.
	boxWidth := max((width-railWidthToReserve(style))-style.GetHorizontalMargins(), 10)
	innerWidth := max(boxWidth-style.GetHorizontalPadding()-style.GetHorizontalBorderSize(), 10)
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
		ResultStatus:           message.ToolStatusSuccess,
		ResultDone:             !b.UserLocalShellPending,
		ToolCallDetailExpanded: !b.UserLocalShellPending && !b.Collapsed,
	}
	if b.UserLocalShellFailed {
		pseudo.ResultStatus = message.ToolStatusError
	}
	prefix := pseudo.renderToolPrefix(spinnerFrame)
	// The disclosure marker advertises toggling; only show it when the card
	// can actually toggle (the same predicate ToggleAtWidth applies).
	if userLocalShellToggleable(b) {
		prefix = renderToolDisclosurePrefix(prefix, !b.Collapsed)
	}
	isActive := b.UserLocalShellPending && spinnerFrame != ""
	displayResult := strings.TrimSuffix(sanitizeToolDisplayText(b.UserLocalShellResult), "\n")

	var bashLines []string
	addHeader := func() {
		if isActive {
			headerLine := "  " + prefix + " " + currentStyles.ToolCallStyle.Render(toolName)
			if mainPart != "" || grayPart != "" {
				headerLine += " " + mainPart
				if grayPart != "" {
					headerLine += " " + currentStyles.DimStyle.Render(grayPart)
				}
			} else if paramSummary != "" {
				headerLine += " " + currentStyles.DimStyle.Render(paramSummary)
			}
			bashLines = append(bashLines, headerLine)
		} else {
			headerLine := fmt.Sprintf("  %s %s", prefix, toolName)
			if mainPart != "" || grayPart != "" {
				headerLine += " " + mainPart
				if grayPart != "" {
					headerLine += " " + currentStyles.DimStyle.Render(grayPart)
				}
			} else if paramSummary != "" {
				headerLine += " " + currentStyles.DimStyle.Render(paramSummary)
			}
			bashLines = append(bashLines, currentStyles.ToolCallStyle.Render(headerLine))
		}
	}

	switch {
	case b.UserLocalShellPending:
		addHeader()
	case !b.Collapsed && strings.TrimSpace(displayResult) != "":
		addHeader()
		appendBashCommandBlock(&bashLines, b.UserLocalShellCmd, contentWidth, true)
		if b.UserLocalShellFailed {
			bashLines = append(bashLines, toolFieldSection(currentStyles.ErrorStyle, "Error"))
		}
		for _, line := range terminaltext.Wrap(displayResult, contentWidth) {
			bashLines = append(bashLines, currentStyles.DimStyle.Render("    "+line))
		}
	default:
		addHeader()
		appendBashCommandBlock(&bashLines, b.UserLocalShellCmd, contentWidth, false)
		if displayResult != "" {
			summary := terminaltext.TruncateOneLine(displayResult, innerWidth-26)
			if b.UserLocalShellFailed {
				bashLines = append(bashLines, toolFieldMarker(currentStyles.ErrorStyle, summary))
			} else {
				bashLines = append(bashLines, toolFieldMarker(currentStyles.ToolResultStyle, summary))
			}
		}
	}

	var finalLines []string
	label := "TERMINAL"
	finalLines = append(finalLines, currentStyles.UserLabelStyle.Render(blockLabelWithID(label, b.displayLabelID())))
	finalLines = append(finalLines, "")
	finalLines = append(finalLines, bashLines...)

	cardBg := currentStyles.Palette.UserCardBg
	finalLines = preserveCardBg(finalLines, cardBg)
	return renderPrewrappedCard(style, innerWidth, finalLines, cardBg, cardRailPrefix("user", b.Focused))
}

func (b *Block) renderUser(width int, spinnerFrame string) []string {
	if b.UserLocalShellCmd != "" {
		return b.renderUserLocalShell(width, spinnerFrame)
	}
	return b.renderUserPlain(width)
}

func (b *Block) renderUserPlain(width int) []string {
	style := currentStyles.UserCardStyle
	// Reserve a column for the conversation rail (foreground-only "│" prepended
	// outside the card width); otherwise a full-width card overflows the terminal.
	boxWidth := max((width-railWidthToReserve(style))-style.GetHorizontalMargins(), 10)
	innerWidth := max(boxWidth-style.GetHorizontalPadding()-style.GetHorizontalBorderSize(), 10)
	contentWidth := min(innerWidth-2, maxProseWidth)

	if strings.TrimSpace(b.Content) == "" && b.ImageCount == 0 && len(b.FileRefs) == 0 && len(b.PDFNames) == 0 {
		return nil
	}

	var finalLines []string
	label := "USER"
	if b.LoopAnchor {
		label = "USER · LOOP"
	}
	finalLines = append(finalLines, currentStyles.UserLabelStyle.Render(blockLabelWithID(label, b.displayLabelID())))
	finalLines = append(finalLines, "")
	if strings.TrimSpace(b.Content) != "" {
		for _, l := range renderUserText(b.Content, contentWidth) {
			finalLines = append(finalLines, "  "+l)
		}
	}
	imagesRendered := b.appendImagePreviewLines(&finalLines, contentWidth, currentStyles.Palette.UserCardBg, style.GetPaddingTop(), strings.TrimSpace(b.Content) != "")

	if b.ImageCount > 0 && !imagesRendered {
		if strings.TrimSpace(b.Content) != "" {
			finalLines = append(finalLines, "")
		}
		imageLabel := "  📎"
		if b.ImageCount > 1 {
			imageLabel = fmt.Sprintf("  📎 %d", b.ImageCount)
		}
		finalLines = append(finalLines, currentStyles.DimStyle.Render(imageLabel))
	}
	if len(b.PDFNames) > 0 {
		if strings.TrimSpace(b.Content) != "" || b.ImageCount > 0 || imagesRendered {
			finalLines = append(finalLines, "")
		}
		for _, name := range b.PDFNames {
			finalLines = append(finalLines, currentStyles.DimStyle.Render("  📄 "+name))
		}
	}
	if len(b.FileRefs) > 0 {
		if strings.TrimSpace(b.Content) != "" || b.ImageCount > 0 || imagesRendered {
			finalLines = append(finalLines, "")
		}
		for _, ref := range b.FileRefs {
			finalLines = append(finalLines, currentStyles.DimStyle.Render("  ⎿  @"+ref))
		}
	}

	cardBg := currentStyles.Palette.UserCardBg
	finalLines = preserveCardBg(finalLines, cardBg)
	return renderPrewrappedCard(style, innerWidth, finalLines, cardBg, cardRailPrefix("user", b.Focused))
}
