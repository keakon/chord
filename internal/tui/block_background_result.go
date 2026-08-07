package tui

import (
	"strconv"
	"strings"
)

const backgroundResultCardTitle = "JOB RESULT"

type parsedBackgroundResult struct {
	id          string
	description string
	status      string
	residual    []string
	output      []string
	duration    string
}

func formatBackgroundResultCardContent(raw, id, status, command, description string) (string, string) {
	sections := splitBackgroundResultSections(raw)
	if id == "" && status == "" && command == "" && description == "" && len(sections) > 1 {
		formatted := make([]string, 0, len(sections))
		firstID := ""
		for _, section := range sections {
			content, sectionID := formatSingleBackgroundResult(section, "", "", "", "")
			if firstID == "" {
				firstID = sectionID
			}
			if content != "" {
				formatted = append(formatted, content)
			}
		}
		return strings.Join(formatted, "\n\n"), firstID
	}
	return formatSingleBackgroundResult(raw, id, status, command, description)
}

func formatSingleBackgroundResult(raw, id, status, command, description string) (string, string) {
	parsed := parseBackgroundResult(raw)
	if strings.TrimSpace(id) == "" {
		id = parsed.id
	}
	if strings.TrimSpace(status) == "" {
		status = parsed.status
	}
	if strings.TrimSpace(description) == "" {
		description = parsed.description
	}
	if strings.TrimSpace(description) == "" {
		description = command
	}
	duration := parsed.duration

	glyph, statusLine := backgroundResultStatusLine(status)
	if duration != "" {
		statusLine += " · ⏱ " + duration
	}
	id = strings.TrimSpace(id)
	description = strings.TrimSpace(description)
	headline := glyph
	if id != "" {
		headline += " " + id
	}
	if description != "" {
		if id != "" {
			headline += " · "
		} else {
			headline += " "
		}
		headline += description
	}
	if headline == glyph {
		headline += " Background job"
	}

	lines := []string{headline, statusLine}
	for _, line := range parsed.residual {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.EqualFold(trimmed, "background finished") || strings.EqualFold(trimmed, "Review this result before continuing.") {
			continue
		}
		if commandDurationNote(trimmed) != "" {
			continue
		}
		lines = append(lines, line)
	}
	if len(parsed.output) > 0 {
		lines = append(lines, "Relevant output:")
		lines = append(lines, parsed.output...)
	}
	return strings.TrimSpace(strings.Join(lines, "\n")), id
}

func splitBackgroundResultSections(raw string) []string {
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	raw = strings.ReplaceAll(raw, "\r", "\n")
	lines := strings.Split(strings.TrimSpace(raw), "\n")
	var sections []string
	var current []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if len(current) > 0 && isBackgroundResultHeader(trimmed) {
			sections = append(sections, strings.TrimSpace(strings.Join(current, "\n")))
			current = nil
		}
		current = append(current, line)
	}
	if len(current) > 0 {
		sections = append(sections, strings.TrimSpace(strings.Join(current, "\n")))
	}
	if len(sections) == 0 {
		return []string{strings.TrimSpace(raw)}
	}
	return sections
}

func parseBackgroundResult(raw string) parsedBackgroundResult {
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	raw = strings.ReplaceAll(raw, "\r", "\n")
	var parsed parsedBackgroundResult
	inOutput := false
	headerChecked := false
	for line := range strings.SplitSeq(strings.TrimSpace(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		if duration := commandDurationNote(trimmed); duration != "" {
			parsed.duration = duration
			continue
		}
		if !headerChecked && trimmed != "" {
			headerChecked = true
			if isBackgroundResultHeader(trimmed) {
				parsed.id = backgroundResultIDFromHeader(trimmed)
				continue
			}
		}
		if inOutput {
			parsed.output = append(parsed.output, line)
			continue
		}
		if value, ok := cutBackgroundResultField(trimmed, "Description:"); ok {
			parsed.description = value
			continue
		}
		if value, ok := cutBackgroundResultField(trimmed, "Status:"); ok {
			parsed.status = value
			continue
		}
		if strings.EqualFold(trimmed, "Relevant output:") {
			inOutput = true
			continue
		}
		if trimmed != "" {
			parsed.residual = append(parsed.residual, line)
		}
	}
	for len(parsed.output) > 0 && strings.TrimSpace(parsed.output[len(parsed.output)-1]) == "" {
		parsed.output = parsed.output[:len(parsed.output)-1]
	}
	return parsed
}

func commandDurationNote(line string) string {
	const prefix = "(command took "
	if !strings.HasPrefix(line, prefix) || !strings.HasSuffix(line, "s)") {
		return ""
	}
	secondsText := strings.TrimSuffix(strings.TrimPrefix(line, prefix), "s)")
	seconds, err := strconv.ParseFloat(secondsText, 64)
	if err != nil || seconds < 0 {
		return ""
	}
	return strconv.Itoa(int(seconds)) + "s"
}

func cutBackgroundResultField(line, field string) (string, bool) {
	if len(line) < len(field) || !strings.EqualFold(line[:len(field)], field) {
		return "", false
	}
	return strings.TrimSpace(line[len(field):]), true
}

func isBackgroundResultHeader(line string) bool {
	if len(line) < 2 || line[0] != '[' || line[len(line)-1] != ']' {
		return false
	}
	lower := strings.ToLower(line)
	return strings.Contains(lower, "job ") || strings.Contains(lower, "service ") || strings.Contains(lower, "background ")
}

func backgroundResultIDFromHeader(line string) string {
	for field := range strings.FieldsSeq(strings.Trim(strings.TrimSpace(line), "[]")) {
		candidate := strings.Trim(field, " :,;")
		if strings.HasPrefix(candidate, "job-") || strings.HasPrefix(candidate, "svc-") {
			return candidate
		}
	}
	return ""
}

func backgroundResultStatusLine(status string) (string, string) {
	status = strings.TrimSpace(status)
	lower := strings.ToLower(status)
	if strings.Contains(lower, "cancel") {
		return "•", "Cancelled"
	}
	if strings.Contains(lower, "error") || strings.Contains(lower, "failed") || strings.Contains(lower, "timed out") || strings.Contains(lower, "exit status") || strings.Contains(lower, "exit code") {
		detail := backgroundResultErrorDetail(status)
		if detail == "" {
			detail = "Background command failed"
		}
		return "✗", "Error: " + detail
	}
	if strings.Contains(lower, "exit 0") || strings.Contains(lower, "success") || strings.Contains(lower, "completed") || lower == "finished" {
		return "✓", "Completed successfully"
	}
	if status == "" {
		return "•", "Finished"
	}
	return "•", status
}

func backgroundResultErrorDetail(status string) string {
	detail := strings.TrimSpace(status)
	lower := strings.ToLower(detail)
	const finishedError = "finished (error:"
	if strings.HasPrefix(lower, finishedError) {
		detail = strings.TrimSpace(detail[len(finishedError):])
		return strings.TrimSpace(strings.TrimSuffix(detail, ")"))
	}
	const errorPrefix = "error:"
	if strings.HasPrefix(lower, errorPrefix) {
		return strings.TrimSpace(detail[len(errorPrefix):])
	}
	return detail
}

func (b *Block) renderBackgroundResult(width int) []string {
	metrics := newToolCardMetrics(width)
	body := make([]string, 0, 8)
	contentLines := strings.Split(strings.TrimSpace(b.Content), "\n")
	for i := range len(contentLines) {
		line := contentLines[i]
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			if len(body) > 0 && body[len(body)-1] != "" {
				body = append(body, "")
			}
			continue
		}
		if isBackgroundResultHeadline(trimmed) {
			if len(body) > 0 && body[len(body)-1] != "" {
				body = append(body, "")
			}
			wrapped := wrapText(trimmed, metrics.contentWidth)
			for i, part := range wrapped {
				prefix := "    "
				if i == 0 {
					prefix = "  "
					part = styleBackgroundResultHeadline(part)
				}
				body = append(body, prefix+part)
			}
			continue
		}
		if strings.EqualFold(trimmed, "Relevant output:") {
			if len(body) > 0 && body[len(body)-1] != "" {
				body = append(body, "")
			}
			body = append(body, ToolResultExpandedStyle.Render("  ↳ Relevant output:"))
			output := strings.Join(contentLines[i+1:], "\n")
			if backgroundResultHasCodeFence(output) {
				codeLines, _, _ := renderAssistantMarkdownContent(output, output, metrics.contentWidth, 0, &b.codeHL)
				for _, codeLine := range codeLines {
					body = append(body, "    "+codeLine)
				}
				break
			}
			for outputLine := range strings.SplitSeq(output, "\n") {
				if strings.TrimSpace(outputLine) == "" {
					if len(body) > 0 && body[len(body)-1] != "" {
						body = append(body, "")
					}
					continue
				}
				for j, part := range wrapText(strings.TrimSpace(outputLine), metrics.contentWidth) {
					prefix := "    "
					if j == 0 {
						prefix = "    "
					}
					body = append(body, DimStyle.Render(prefix+part))
				}
			}
			break
		}
		style := ToolResultExpandedStyle
		prefix := "  ↳ "
		if strings.HasPrefix(strings.ToLower(trimmed), "error:") {
			style = ErrorStyle
		}
		for i, part := range wrapText(trimmed, metrics.contentWidth) {
			indent := prefix
			if i > 0 {
				indent = "    "
			}
			body = append(body, style.Render(indent+part))
		}
	}
	return renderPrewrappedToolCard(metrics.blockStyle, metrics.cardWidth, toolCardTitle(backgroundResultCardTitle, b.displayLabelID()), body, metrics.toolCardBg, railANSISeq("tool", b.Focused))
}

func backgroundResultHasCodeFence(content string) bool {
	for line := range strings.SplitSeq(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			return true
		}
	}
	return false
}

func isBackgroundResultHeadline(line string) bool {
	return strings.HasPrefix(line, "✓") || strings.HasPrefix(line, "✗") || strings.HasPrefix(line, "•")
}

func styleBackgroundResultHeadline(line string) string {
	switch {
	case strings.HasPrefix(line, "✓"):
		return ToolStatusSuccessStyle.Render("✓") + line[len("✓"):]
	case strings.HasPrefix(line, "✗"):
		return ToolStatusErrorStyle.Render("✗") + line[len("✗"):]
	case strings.HasPrefix(line, "•"):
		return ToolStatusNeutralStyle.Render("•") + line[len("•"):]
	default:
		return line
	}
}
