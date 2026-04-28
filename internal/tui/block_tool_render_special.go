package tui

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/mattn/go-runewidth"

	"github.com/keakon/chord/internal/tools"
)

func (b *Block) renderTaskCall(width int, spinnerFrame string) []string {
	blockStyle := ToolBlockStyle
	toolCardBg := currentTheme.ToolCallBg
	boxWidth := width - blockStyle.GetHorizontalMargins()
	if boxWidth < 10 {
		boxWidth = 10
	}
	cardWidth := boxWidth - blockStyle.GetHorizontalPadding() - blockStyle.GetHorizontalBorderSize()
	if cardWidth < 10 {
		cardWidth = 10
	}
	contentWidth := cardWidth - 4
	if contentWidth < 10 {
		contentWidth = 10
	}

	args := parseTaskToolArgs(b.Content)
	_, hasHandle := parseTaskToolHandle(b.ResultContent)
	hasResultText := strings.TrimSpace(b.ResultContent) != ""
	title := taskToolHeaderTitle(b.Content)
	subType := strings.TrimSpace(args.AgentType)
	isActive := b.toolExecutionIsRunning() && spinnerFrame != ""
	prefix := b.renderToolPrefix(spinnerFrame)

	headerLine := fmt.Sprintf("  %s %s", prefix, b.ToolName)
	if isActive {
		headerLine = "  " + prefix + " " + ToolCallStyle.Render(b.ToolName)
	} else {
		headerLine = ToolCallStyle.Render(headerLine)
	}
	if title != "" {
		headerLine += " " + title
	}
	if subType != "" {
		headerLine += " " + DimStyle.Render("("+subType+")")
	}
	headerLine = appendToolProgressSuffix(headerLine, b.ToolProgress, cardWidth-4)
	if b.toolExecutionIsQueued() && !isActive {
		headerLine = renderQueuedToolHeaderBadge(headerLine, cardWidth)
	}

	result := []string{headerLine}
	if b.Collapsed {
		descLines, hiddenDesc := taskToolCollapsedDescriptionLines(b.Content, contentWidth-4)
		if len(descLines) > 0 {
			for _, line := range descLines {
				result = append(result, "    "+line)
			}
		}
		if hiddenDesc > 0 {
			result = append(result, renderToolExpandHint(toolHintIndent, hiddenDesc))
		}
		switch {
		case strings.TrimSpace(b.DoneSummary) != "":
			appendCollapsedSummaryLines(&result, b.DoneSummary, width-30, ToolResultStyle)
		case hasResultText:
			summary := truncateOneLine(taskToolCollapsedHandleSummary(b.ResultContent), width-24)
			result = append(result, ToolResultStyle.Render("  ▸ ↳ "+summary))
		case b.toolResultIsError() && strings.TrimSpace(b.ResultContent) != "":
			appendCollapsedSummaryLines(&result, b.ResultContent, width-30, ErrorStyle)
		case b.toolResultIsCancelled() && strings.TrimSpace(b.ResultContent) != "":
			appendCollapsedSummaryLines(&result, b.ResultContent, width-30, DimStyle)
		}
	} else {
		if subType != "" {
			result = append(result, DimStyle.Render("    type: "+subType))
		}
		descLines := taskToolExpandedDescriptionLines(b.Content, contentWidth-4)
		if len(descLines) > 0 {
			result = append(result, ToolResultExpandedStyle.Render("  ↳ Description:"))
			for _, line := range descLines {
				result = append(result, "    "+line)
			}
		}
		if hasHandle || (hasResultText && !b.toolResultIsError() && !b.toolResultIsCancelled()) {
			result = append(result, ToolResultExpandedStyle.Render("  ↳ Worker:"))
			for _, line := range taskToolExpandedHandleLines(b.ResultContent) {
				for _, wrapped := range wrapText(line, contentWidth) {
					result = append(result, DimStyle.Render("    "+wrapped))
				}
			}
		} else if b.toolResultIsError() && strings.TrimSpace(b.ResultContent) != "" {
			result = append(result, ErrorStyle.Render("  ↳ Error:"))
			for _, line := range wrapText(strings.TrimSpace(b.ResultContent), contentWidth) {
				result = append(result, ErrorStyle.Render("    "+line))
			}
		} else if b.toolResultIsCancelled() && strings.TrimSpace(b.ResultContent) != "" {
			result = append(result, DimStyle.Render("  ↳ Cancelled:"))
			for _, line := range wrapText(strings.TrimSpace(b.ResultContent), contentWidth) {
				result = append(result, DimStyle.Render("    "+line))
			}
		}
		if strings.TrimSpace(b.DoneSummary) != "" {
			result = append(result, ToolResultExpandedStyle.Render("  ↳ Completed:"))
			for _, line := range toolExpandedTextLines(b.DoneSummary, contentWidth) {
				result = append(result, "    "+line)
			}
		}
	}
	result = appendToolElapsedFooter(result, b)

	return renderPrewrappedToolCard(blockStyle, cardWidth, ToolLabelStyle.Render("TOOL CALL"), result, toolCardBg, railANSISeq("tool", b.Focused))
}

// renderTodoCall renders a TodoWrite tool call as a todo list with status markers.
func (b *Block) renderTodoCall(width int, spinnerFrame string) []string {
	blockStyle := ToolBlockStyle
	toolCardBg := currentTheme.ToolCallBg
	boxWidth := width - blockStyle.GetHorizontalMargins()
	if boxWidth < 10 {
		boxWidth = 10
	}
	cardWidth := boxWidth - blockStyle.GetHorizontalPadding() - blockStyle.GetHorizontalBorderSize()
	if cardWidth < 10 {
		cardWidth = 10
	}
	contentWidth := cardWidth - 6
	if contentWidth < 10 {
		contentWidth = 10
	}

	type todoArgs struct {
		Todos []todoCallArgItem `json:"todos"`
	}
	var args todoArgs
	_ = json.Unmarshal([]byte(b.Content), &args)

	prefix := b.renderToolPrefix(spinnerFrame)
	isActive := b.toolExecutionIsRunning() && spinnerFrame != ""

	var result []string
	if isActive {
		headerLine := "  " + prefix + " " + ToolCallStyle.Render(b.ToolName)
		headerLine = appendToolProgressSuffix(headerLine, b.ToolProgress, cardWidth-4)
		result = append(result, headerLine)
	} else {
		headerLine := fmt.Sprintf("  %s %s", prefix, b.ToolName)
		headerLine = appendToolProgressSuffix(headerLine, b.ToolProgress, cardWidth-4)
		styledHeader := ToolCallStyle.Render(headerLine)
		if b.toolExecutionIsQueued() {
			styledHeader = renderQueuedToolHeaderBadge(styledHeader, cardWidth)
		}
		result = append(result, styledHeader)
	}

	if b.toolResultIsError() && b.ResultContent != "" {
		result = appendErrorResultLines(result, b.ResultContent, contentWidth)
	} else if b.toolResultIsCancelled() && b.ResultContent != "" {
		result = appendCancelledResultLines(result, b.ResultContent, contentWidth)
	} else if len(args.Todos) > 0 {
		if len(args.Todos) > 1 {
			result = append(result, QuestionSeparatorStyle.Render(fmt.Sprintf("  ▸ %d tasks", len(args.Todos))))
		}
		for _, item := range args.Todos {
			appendTodoCallItemLines(&result, item, contentWidth)
		}
	}
	// Empty list: don't show "(no items)" prominently; just omit the list body

	return renderPrewrappedToolCard(blockStyle, cardWidth, ToolLabelStyle.Render("TOOL CALL"), result, toolCardBg, railANSISeq("tool", b.Focused))
}

// renderQuestionCall renders a Question tool call showing the question text and options.
func (b *Block) renderQuestionCall(width int, spinnerFrame string) []string {
	blockStyle := ToolBlockStyle
	toolCardBg := currentTheme.ToolCallBg
	boxWidth := width - blockStyle.GetHorizontalMargins()
	if boxWidth < 10 {
		boxWidth = 10
	}
	cardWidth := boxWidth - blockStyle.GetHorizontalPadding() - blockStyle.GetHorizontalBorderSize()
	if cardWidth < 10 {
		cardWidth = 10
	}
	contentWidth := cardWidth - 6
	if contentWidth < 10 {
		contentWidth = 10
	}

	type questionArgs struct {
		Questions []tools.QuestionItem `json:"questions"`
	}
	var args questionArgs
	_ = json.Unmarshal([]byte(b.Content), &args)

	var answers []tools.QuestionAnswer
	hasStructuredAnswers := false
	if !b.toolResultIsError() && !b.toolResultIsCancelled() && strings.TrimSpace(b.ResultContent) != "" {
		if err := json.Unmarshal([]byte(b.ResultContent), &answers); err == nil && len(answers) > 0 {
			hasStructuredAnswers = true
		}
	}

	prefix := b.renderToolPrefix(spinnerFrame)
	isActive := b.toolExecutionIsRunning() && spinnerFrame != ""

	var result []string
	if isActive {
		headerLine := "  " + prefix + " " + ToolCallStyle.Render(b.ToolName)
		headerLine = appendToolProgressSuffix(headerLine, b.ToolProgress, cardWidth-4)
		result = append(result, headerLine)
	} else {
		headerLine := fmt.Sprintf("  %s %s", prefix, b.ToolName)
		headerLine = appendToolProgressSuffix(headerLine, b.ToolProgress, cardWidth-4)
		styledHeader := ToolCallStyle.Render(headerLine)
		if b.toolExecutionIsQueued() {
			styledHeader = renderQueuedToolHeaderBadge(styledHeader, cardWidth)
		}
		result = append(result, styledHeader)
	}

	for i, q := range args.Questions {
		answer, hasAnswer := questionAnswerForRender(answers, i, q.Header)
		selectedOptions, customAnswers := splitQuestionSelections(q, answer)
		if q.Header != "" {
			result = append(result, QuestionSeparatorStyle.Render("  ▸ "+q.Header))
		}
		if q.Question != "" {
			qText := strings.ReplaceAll(q.Question, "<br>", "\n")
			for _, line := range strings.Split(qText, "\n") {
				for _, wl := range wrapText("    "+line, contentWidth) {
					result = append(result, paramValStyle.Render(wl))
				}
			}
		}
		if len(q.Options) > 0 {
			mode := "Single-select"
			if q.Multiple {
				mode = "Multi-select"
			}
			result = append(result, DimStyle.Render("    Mode: "+mode))
			result = append(result, DimStyle.Render("    Options:"))
			for i, opt := range q.Options {
				marker := " "
				if _, ok := selectedOptions[opt.Label]; ok {
					marker = "✓"
				}
				optPrefix := fmt.Sprintf("      %s %d. %s", marker, i+1, opt.Label)
				optLine := optPrefix
				if opt.Description != "" {
					descWidth := contentWidth - runewidth.StringWidth(optPrefix)
					if descWidth < 0 {
						descWidth = 0
					}
					optLine += DimStyle.Render(" — " + truncateOneLine(opt.Description, descWidth))
				}
				result = append(result, optLine)
			}
		}
		switch {
		case hasAnswer && len(q.Options) == 0:
			for _, sel := range answer.Selected {
				result = appendQuestionAnswerLines(result, sel, "    Answer: ", "            ", contentWidth)
			}
		case len(customAnswers) == 1:
			result = appendQuestionAnswerLines(result, customAnswers[0], "    Custom: ", "            ", contentWidth)
		case len(customAnswers) > 1:
			result = append(result, DimStyle.Render("    Custom:"))
			for _, sel := range customAnswers {
				result = appendQuestionAnswerLines(result, sel, "      • ", "        ", contentWidth)
			}
		case len(q.Options) > 0 && !hasStructuredAnswers:
			result = append(result, DimStyle.Render("    Custom: Enabled"))
		}
	}

	if b.toolResultIsError() && b.ResultContent != "" {
		result = appendErrorResultLines(result, b.ResultContent, contentWidth)
	} else if b.toolResultIsCancelled() && b.ResultContent != "" {
		result = appendCancelledResultLines(result, b.ResultContent, contentWidth)
	} else if strings.TrimSpace(b.ResultContent) != "" && !hasStructuredAnswers {
		if b.ResultContent != "" {
			result = append(result, ToolResultStyle.Render("  ↳ ✓"))
			for _, line := range wrapText(b.ResultContent, contentWidth) {
				result = append(result, ToolResultStyle.Render("    "+line))
			}
		}
	}
	result = appendToolElapsedFooter(result, b)

	return renderPrewrappedToolCard(blockStyle, cardWidth, ToolLabelStyle.Render("TOOL CALL"), result, toolCardBg, railANSISeq("tool", b.Focused))
}

func questionAnswerForRender(answers []tools.QuestionAnswer, index int, header string) (tools.QuestionAnswer, bool) {
	if index >= 0 && index < len(answers) {
		return answers[index], true
	}
	if strings.TrimSpace(header) == "" {
		return tools.QuestionAnswer{}, false
	}
	for _, answer := range answers {
		if answer.Header == header {
			return answer, true
		}
	}
	return tools.QuestionAnswer{}, false
}

func splitQuestionSelections(question tools.QuestionItem, answer tools.QuestionAnswer) (map[string]struct{}, []string) {
	selectedOptions := make(map[string]struct{}, len(answer.Selected))
	if len(answer.Selected) == 0 {
		return selectedOptions, nil
	}

	labels := make(map[string]struct{}, len(question.Options))
	for _, opt := range question.Options {
		labels[opt.Label] = struct{}{}
	}

	customAnswers := make([]string, 0, len(answer.Selected))
	for _, sel := range answer.Selected {
		if _, ok := labels[sel]; ok {
			selectedOptions[sel] = struct{}{}
			continue
		}
		customAnswers = append(customAnswers, sel)
	}
	return selectedOptions, customAnswers
}

// renderCancelCall renders a Cancel tool call with semantic display.
// Collapsed view shows: target (readable), reason (if any), result status.
// Expanded view shows more structured details but avoids raw JSON.
func (b *Block) renderCancelCall(width int, spinnerFrame string) []string {
	blockStyle := ToolBlockStyle
	toolCardBg := currentTheme.ToolCallBg
	boxWidth := width - blockStyle.GetHorizontalMargins()
	if boxWidth < 10 {
		boxWidth = 10
	}
	cardWidth := boxWidth - blockStyle.GetHorizontalPadding() - blockStyle.GetHorizontalBorderSize()
	if cardWidth < 10 {
		cardWidth = 10
	}
	contentWidth := cardWidth - 4
	if contentWidth < 10 {
		contentWidth = 10
	}

	args := parseCancelToolArgs(b.Content)
	prefix := b.renderToolPrefix(spinnerFrame)
	isActive := b.toolExecutionIsRunning() && spinnerFrame != ""

	target := extractReadableTarget(args.TargetTaskID)
	if target == "" {
		target = "unknown"
	}

	var result []string
	if isActive {
		headerLine := "  " + prefix + " " + ToolCallStyle.Render(b.ToolName) + " " + target
		headerLine = appendToolProgressSuffix(headerLine, b.ToolProgress, cardWidth-4)
		result = append(result, headerLine)
	} else {
		headerLine := fmt.Sprintf("  %s %s", prefix, b.ToolName) + " " + target
		headerLine = appendToolProgressSuffix(headerLine, b.ToolProgress, cardWidth-4)
		styledHeader := ToolCallStyle.Render(headerLine)
		if b.toolExecutionIsQueued() {
			styledHeader = renderQueuedToolHeaderBadge(styledHeader, cardWidth)
		}
		result = append(result, styledHeader)
	}

	if b.Collapsed {
		if args.Reason != "" {
			reasonSummary := truncateOneLine(args.Reason, contentWidth-10)
			result = append(result, DimStyle.Render("    reason: "+reasonSummary))
		}
		if summary := formatToolResultSummaryLine(b); summary != "" {
			result = append(result, toolSummaryLine(summary))
		}
		if b.toolResultIsError() && strings.TrimSpace(b.ResultContent) != "" {
			summary := truncateOneLine(strings.TrimSpace(b.ResultContent), contentWidth-10)
			result = append(result, ErrorStyle.Render("    error: "+summary))
		}
	} else {
		if args.Reason != "" {
			result = append(result, DimStyle.Render("    reason:"))
			for _, line := range wrapText(args.Reason, contentWidth-4) {
				result = append(result, DimStyle.Render("      "+line))
			}
		}
		if summary := formatToolResultSummaryLine(b); summary != "" {
			result = append(result, toolSummaryLine(summary))
		}
		if b.ResultContent != "" {
			handle, ok := parseTaskToolHandle(b.ResultContent)
			if ok {
				result = append(result, ToolResultExpandedStyle.Render("  ↳ Result:"))
				if handle.Status != "" {
					result = append(result, DimStyle.Render("    status: "+handle.Status))
				}
				if handle.TaskID != "" && !b.Collapsed {
					result = append(result, DimStyle.Render("    task_id: "+handle.TaskID))
				}
				if handle.AgentID != "" {
					result = append(result, DimStyle.Render("    agent_id: "+handle.AgentID))
				}
				if handle.Message != "" {
					result = append(result, DimStyle.Render("    message: "+handle.Message))
				}
			} else if !b.toolResultIsError() && !b.toolResultIsCancelled() {
				result = append(result, ToolResultExpandedStyle.Render("  ↳ Result:"))
				for _, line := range wrapText(strings.TrimSpace(b.ResultContent), contentWidth) {
					result = append(result, DimStyle.Render("    "+line))
				}
			}
		}
		if b.toolResultIsError() && strings.TrimSpace(b.ResultContent) != "" {
			result = append(result, ErrorStyle.Render("  ↳ Error:"))
			for _, line := range wrapText(strings.TrimSpace(b.ResultContent), contentWidth) {
				result = append(result, ErrorStyle.Render("    "+line))
			}
		} else if b.toolResultIsCancelled() && strings.TrimSpace(b.ResultContent) != "" {
			result = append(result, DimStyle.Render("  ↳ Cancelled:"))
			for _, line := range wrapText(strings.TrimSpace(b.ResultContent), contentWidth) {
				result = append(result, DimStyle.Render("    "+line))
			}
		}
		if strings.TrimSpace(b.DoneSummary) != "" {
			result = append(result, ToolResultExpandedStyle.Render("  ↳ Completed:"))
			for _, line := range wrapText(b.DoneSummary, contentWidth) {
				result = append(result, DimStyle.Render("    "+line))
			}
		}
	}
	result = appendToolElapsedFooter(result, b)

	return renderPrewrappedToolCard(blockStyle, cardWidth, ToolLabelStyle.Render("TOOL CALL"), result, toolCardBg, railANSISeq("tool", b.Focused))
}

// renderNotifyCall renders a Notify tool call with semantic display.
// Collapsed view shows: target (readable), kind (if any), message summary, result status.
// Expanded view shows more structured details but avoids raw JSON.
func (b *Block) renderNotifyCall(width int, spinnerFrame string) []string {
	blockStyle := ToolBlockStyle
	toolCardBg := currentTheme.ToolCallBg
	boxWidth := width - blockStyle.GetHorizontalMargins()
	if boxWidth < 10 {
		boxWidth = 10
	}
	cardWidth := boxWidth - blockStyle.GetHorizontalPadding() - blockStyle.GetHorizontalBorderSize()
	if cardWidth < 10 {
		cardWidth = 10
	}
	contentWidth := cardWidth - 4
	if contentWidth < 10 {
		contentWidth = 10
	}

	args := parseNotifyToolArgs(b.Content)
	prefix := b.renderToolPrefix(spinnerFrame)
	isActive := b.toolExecutionIsRunning() && spinnerFrame != ""

	target := extractReadableTarget(args.TargetTaskID)

	var result []string
	if isActive {
		headerLine := "  " + prefix + " " + ToolCallStyle.Render(b.ToolName)
		if target != "" {
			headerLine += " " + target
		}
		if args.Kind != "" {
			headerLine += " " + DimStyle.Render("("+args.Kind+")")
		}
		headerLine = appendToolProgressSuffix(headerLine, b.ToolProgress, cardWidth-4)
		result = append(result, headerLine)
	} else {
		headerLine := fmt.Sprintf("  %s %s", prefix, b.ToolName)
		if target != "" {
			headerLine += " " + target
		}
		if args.Kind != "" {
			headerLine += " " + DimStyle.Render("("+args.Kind+")")
		}
		headerLine = appendToolProgressSuffix(headerLine, b.ToolProgress, cardWidth-4)
		styledHeader := ToolCallStyle.Render(headerLine)
		if b.toolExecutionIsQueued() {
			styledHeader = renderQueuedToolHeaderBadge(styledHeader, cardWidth)
		}
		result = append(result, styledHeader)
	}

	if b.Collapsed {
		if args.Message != "" {
			msgSummary := truncateOneLine(firstDisplayLine(args.Message), contentWidth-10)
			result = append(result, DimStyle.Render("    message: "+msgSummary))
		}
		if summary := formatToolResultSummaryLine(b); summary != "" {
			result = append(result, toolSummaryLine(summary))
		}
		if b.toolResultIsError() && strings.TrimSpace(b.ResultContent) != "" {
			summary := truncateOneLine(strings.TrimSpace(b.ResultContent), contentWidth-10)
			result = append(result, ErrorStyle.Render("    error: "+summary))
		}
	} else {
		if args.Message != "" {
			result = append(result, DimStyle.Render("    message:"))
			for _, line := range wrapText(args.Message, contentWidth-4) {
				result = append(result, DimStyle.Render("      "+line))
			}
		}
		if summary := formatToolResultSummaryLine(b); summary != "" {
			result = append(result, toolSummaryLine(summary))
		}
		if b.ResultContent != "" {
			handle, ok := parseTaskToolHandle(b.ResultContent)
			if ok {
				result = append(result, ToolResultExpandedStyle.Render("  ↳ Result:"))
				if handle.Status != "" {
					result = append(result, DimStyle.Render("    status: "+handle.Status))
				}
				if handle.TaskID != "" && !b.Collapsed {
					result = append(result, DimStyle.Render("    task_id: "+handle.TaskID))
				}
				if handle.AgentID != "" {
					result = append(result, DimStyle.Render("    agent_id: "+handle.AgentID))
				}
				if handle.Message != "" {
					result = append(result, DimStyle.Render("    message: "+handle.Message))
				}
			} else if !b.toolResultIsError() && !b.toolResultIsCancelled() {
				result = append(result, ToolResultExpandedStyle.Render("  ↳ Result:"))
				for _, line := range wrapText(strings.TrimSpace(b.ResultContent), contentWidth) {
					result = append(result, DimStyle.Render("    "+line))
				}
			}
		}
		if b.toolResultIsError() && strings.TrimSpace(b.ResultContent) != "" {
			result = append(result, ErrorStyle.Render("  ↳ Error:"))
			for _, line := range wrapText(strings.TrimSpace(b.ResultContent), contentWidth) {
				result = append(result, ErrorStyle.Render("    "+line))
			}
		} else if b.toolResultIsCancelled() && strings.TrimSpace(b.ResultContent) != "" {
			result = append(result, DimStyle.Render("  ↳ Cancelled:"))
			for _, line := range wrapText(strings.TrimSpace(b.ResultContent), contentWidth) {
				result = append(result, DimStyle.Render("    "+line))
			}
		}
		if strings.TrimSpace(b.DoneSummary) != "" {
			result = append(result, ToolResultExpandedStyle.Render("  ↳ Completed:"))
			for _, line := range wrapText(b.DoneSummary, contentWidth) {
				result = append(result, DimStyle.Render("    "+line))
			}
		}
	}
	result = appendToolElapsedFooter(result, b)

	return renderPrewrappedToolCard(blockStyle, cardWidth, ToolLabelStyle.Render("TOOL CALL"), result, toolCardBg, railANSISeq("tool", b.Focused))
}
