package tui

import (
	"strconv"
	"strings"
	"time"

	"github.com/keakon/chord/internal/tools"
	"github.com/keakon/chord/internal/tui/markdownutil"
)

const backgroundResultCardTitle = "JOB RESULT"

// backgroundResultSuccessStatus is the status line a successfully completed job
// renders. The ✓ headline already states it, so folded cards drop the line and
// keep only any extra detail it carries.
const backgroundResultSuccessStatus = "Completed successfully"

type parsedBackgroundResult struct {
	id          string
	description string
	command     string
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
	if strings.TrimSpace(command) == "" {
		command = parsed.command
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
		statusLine += " · " + elapsedGlyph + " " + duration
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
		if trimmed == "" {
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
	raw = markdownutil.NormalizeNewlines(raw)
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
	raw = markdownutil.NormalizeNewlines(raw)
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
		if value, ok := cutBackgroundResultField(trimmed, "Purpose:"); ok {
			parsed.description = value
			continue
		}
		// Description and Kind are the removed spawn tool's field names. The
		// header matcher no longer recognizes a legacy headline, so such a
		// record loses its id, but its fields still parse: a session archived
		// before the job registry landed keeps folding to a useful summary
		// instead of a raw "Background job" line.
		if value, ok := cutBackgroundResultField(trimmed, "Description:"); ok {
			parsed.description = value
			continue
		}
		if value, ok := cutBackgroundResultField(trimmed, "Command:"); ok {
			parsed.command = value
			continue
		}
		if _, ok := cutBackgroundResultField(trimmed, "Kind:"); ok {
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
	if err != nil || seconds < 1 {
		return ""
	}
	return tools.FormatElapsed(time.Duration(seconds * float64(time.Second)))
}

func cutBackgroundResultField(line, field string) (string, bool) {
	if len(line) < len(field) || !strings.EqualFold(line[:len(field)], field) {
		return "", false
	}
	return strings.TrimSpace(line[len(field):]), true
}

// isBackgroundResultHeader matches the headline the job registry writes: a
// single "[Background job <id> finished]" line. Anything else is ordinary
// body text, so a card can only derive its background object id from the
// current format.
func isBackgroundResultHeader(line string) bool {
	lower := strings.ToLower(strings.TrimSpace(line))
	return strings.HasPrefix(lower, "[background job ") && strings.HasSuffix(lower, " finished]")
}

func backgroundResultIDFromHeader(line string) string {
	for field := range strings.FieldsSeq(strings.Trim(strings.TrimSpace(line), "[]")) {
		candidate := strings.Trim(field, " :,;")
		if strings.HasPrefix(candidate, "job-") {
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
	if code, ok := backgroundResultExitCode(lower); ok && code == 0 {
		return "✓", backgroundResultSuccessStatus
	}
	if strings.Contains(lower, "error") || strings.Contains(lower, "failed") || strings.Contains(lower, "timed out") || strings.Contains(lower, "exit status") || strings.Contains(lower, "exit code") {
		detail := backgroundResultErrorDetail(status)
		if detail == "" {
			detail = "Background command failed"
		}
		return "✗", "Error: " + detail
	}
	if strings.Contains(lower, "exit 0") || strings.Contains(lower, "success") || strings.Contains(lower, "completed") || lower == "finished" {
		return "✓", backgroundResultSuccessStatus
	}
	if status == "" {
		return "•", "Finished"
	}
	return "•", status
}

// backgroundResultExitCode extracts the exit code from a status line such as
// "completed (exit code 0)" or "failed (exit status 7)". Success text contains
// the same "exit code" marker as the failure branch, so the code must be
// parsed and checked before generic error-substring matching.
func backgroundResultExitCode(lower string) (int, bool) {
	for _, prefix := range []string{"exit code ", "exit status ", "exit "} {
		_, rest, found := strings.Cut(lower, prefix)
		if !found {
			continue
		}
		end := 0
		for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
			end++
		}
		if end == 0 {
			continue
		}
		if code, err := strconv.Atoi(rest[:end]); err == nil {
			return code, true
		}
	}
	return 0, false
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
	// Terminal status text is "failed (...)" or "killed (...)"; the headline
	// glyph already says the job did not succeed, so show only the detail.
	for _, verb := range []string{"failed (", "killed ("} {
		if strings.HasPrefix(lower, verb) && strings.HasSuffix(detail, ")") {
			return strings.TrimSpace(detail[len(verb) : len(detail)-1])
		}
	}
	return detail
}

// isBackgroundResultCard reports whether this BlockStatus is a JOB RESULT card.
// These cards carry a durable background object id or the JOB RESULT badge, are
// collapsible, and route to renderBackgroundResult. Every other status card
// (loop notices, info cards, context-pressure notices, mailbox cards) keeps the
// generic rendering and is not collapsible.
func (b *Block) isBackgroundResultCard() bool {
	if b == nil || b.Type != BlockStatus {
		return false
	}
	return b.BackgroundObjectID != "" || b.StatusTitle == backgroundResultCardTitle
}

func (b *Block) renderBackgroundResult(width int) []string {
	metrics := newToolCardMetrics(width)
	body := make([]string, 0, 8)
	// A folded JOB RESULT card keeps each job's headline and its status line —
	// the one-line state summary a collapsed tool card also shows — and drops
	// the residual lines and relevant-output block that made the card tall.
	collapsed := b.Collapsed
	expectStatus := false
	skippingOutput := false
	contentLines := strings.Split(strings.TrimSpace(sanitizeDisplayText(b.Content)), "\n")
	for i := range len(contentLines) {
		line := contentLines[i]
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			if !skippingOutput && len(body) > 0 && body[len(body)-1] != "" {
				body = append(body, "")
			}
			continue
		}
		if isBackgroundResultHeadline(trimmed) {
			skippingOutput = false
			if len(body) > 0 && body[len(body)-1] != "" {
				body = append(body, "")
			}
			wrapped := wrapText(trimmed, metrics.contentWidth)
			for i, part := range wrapped {
				prefix := "    "
				if i == 0 {
					prefix = "  "
					part = styleBackgroundResultHeadline(part, collapsed)
				}
				body = append(body, prefix+part)
			}
			expectStatus = true
			continue
		}
		if skippingOutput {
			continue
		}
		if strings.EqualFold(trimmed, "Relevant output:") {
			if collapsed {
				// Output is the tail of its section; skip it but keep scanning
				// so a multi-job card still lists every job's headline.
				skippingOutput = true
				continue
			}
			if len(body) > 0 && body[len(body)-1] != "" {
				body = append(body, "")
			}
			body = append(body, toolFieldSection(ToolResultExpandedStyle, "Relevant output"))
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
		if collapsed && expectStatus {
			if trimmed = foldedBackgroundResultStatusLine(trimmed); trimmed == "" {
				expectStatus = false
				continue
			}
		}
		if collapsed && !expectStatus {
			continue
		}
		expectStatus = false
		style := ToolResultExpandedStyle
		if strings.HasPrefix(strings.ToLower(trimmed), "error:") {
			style = ErrorStyle
		}
		for i, part := range wrapText(trimmed, metrics.contentWidth) {
			if i > 0 {
				body = append(body, toolFieldBody(style, part))
				continue
			}
			body = append(body, toolFieldMarker(style, part))
		}
	}
	return renderPrewrappedToolCard(metrics.blockStyle, metrics.cardWidth, toolCardTitle(backgroundResultCardTitle, b.displayLabelID()), body, metrics.toolCardBg, railANSISeq("tool", b.Focused))
}

// foldedBackgroundResultStatusLine returns the status line a folded JOB RESULT
// card shows for one job. A successful job's summary is already in the ✓
// headline, so it is dropped; a duration it carried stays, and failure,
// cancellation, or unknown statuses read as-is because the glyph alone does not
// name them.
func foldedBackgroundResultStatusLine(line string) string {
	trimmed := strings.TrimSpace(line)
	if trimmed == backgroundResultSuccessStatus {
		return ""
	}
	if rest, ok := strings.CutPrefix(trimmed, backgroundResultSuccessStatus+" · "); ok {
		return rest
	}
	return line
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

// styleBackgroundResultHeadline styles the ✓/✗/• headline and appends the
// disclosure marker a collapsible card carries, so the folded state is visible
// at a glance and the space/enter toggle reads as the same affordance across
// card families.
func styleBackgroundResultHeadline(line string, collapsed bool) string {
	marker := toolDisclosureExpanded
	if collapsed {
		marker = toolDisclosureCollapsed
	}
	switch {
	case strings.HasPrefix(line, "✓"):
		return ToolStatusSuccessStyle.Render("✓") + " " + marker + line[len("✓"):]
	case strings.HasPrefix(line, "✗"):
		return ToolStatusErrorStyle.Render("✗") + " " + marker + line[len("✗"):]
	case strings.HasPrefix(line, "•"):
		return ToolStatusNeutralStyle.Render("•") + " " + marker + line[len("•"):]
	default:
		return line
	}
}
