package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/keakon/chord/internal/tui/markdownutil"
)

type deleteDisplaySection struct {
	label string
	items []string
}

func (b *Block) renderDeleteCall(width int, spinnerFrame string) []string {
	metrics := newWideHeaderToolCardMetrics(width)
	blockStyle := metrics.blockStyle
	toolCardBg := metrics.toolCardBg
	cardWidth := metrics.cardWidth
	contentWidth := metrics.contentWidth

	_, vals := b.toolArgsParsed()
	// The subject of a delete is what it deletes, so the paths own the header
	// and the reason joins the option group beside them; the reason only takes
	// the subject slot when there is no path to name.
	paths := parseDeleteHeaderPaths(vals)
	mainPart := ""
	switch len(paths) {
	case 1:
		mainPart = b.displayToolPath(paths[0])
	case 0:
	default:
		mainPart = fmt.Sprintf("%d files", len(paths))
	}
	reason := firstDisplayLine(strings.TrimSpace(vals["reason"]))
	headerOptions := b.diagnosticHeaderOptions()
	if reason != "" {
		if mainPart == "" {
			mainPart = reason
		} else {
			headerOptions = append([]string{reason}, headerOptions...)
		}
	}
	if b.Audit != nil && b.Audit.UserModified {
		headerOptions = append([]string{"edited before approval"}, headerOptions...)
	}
	grayPart := mergeHeaderOptions("", headerOptions)
	prefix := b.renderToolPrefix(spinnerFrame)
	headerLine := renderToolHeaderLine(prefix, b.ToolName)
	headerLine = appendToolHeaderSummary(headerLine, mainPart, grayPart, "", cardWidth-4)
	headerLine = buildToolHeaderLine(headerLine, b.ToolProgress, cardWidth, b.toolExecutionIsQueued() && b.ToolQueuedByExecutionEvent, b.toolExecutionIsRunning())
	result := []string{headerLine}

	if strings.TrimSpace(b.ResultContent) == "" {
		result = appendDeleteRequestedPaths(result, b, parseDeleteHeaderPaths(vals), contentWidth)
		return b.renderToolCardWithIgnoredArgs(blockStyle, cardWidth, toolCardTitle("TOOL CALL", b.displayLabelID()), result, toolCardBg, railANSISeq("tool", b.Focused))
	}

	displayResult := b.stripResultNotes(b.ResultContent)
	if b.toolResultIsError() {
		displayResult = toolErrorDisplayContent(displayResult)
	}
	headline, sections := parseDeleteDisplayResult(displayResult)
	failureSummary := ""
	if b.toolResultIsError() {
		failureSummary = deleteFailureSummary(headline)
		if failureSummary != "" {
			// Same "↳ Error: <summary>" envelope as every other card; the
			// per-path sections below carry the detail.
			appendToolOutcomeBody(&result, toolOutcomeError, failureSummary, contentWidth, false)
		}
	}
	for _, section := range sections {
		result = appendDeleteDisplaySection(result, b, section, contentWidth)
	}
	if len(sections) == 0 && strings.TrimSpace(headline) != "" && failureSummary == "" {
		style := DimStyle
		if b.toolResultIsError() {
			style = ErrorStyle
		}
		for _, line := range wrapText(sanitizeToolDisplayText(headline), contentWidth) {
			result = append(result, toolFieldMarker(style, line))
		}
	}
	result = appendToolElapsedToHeader(result, b, cardWidth)
	return b.renderToolCardWithIgnoredArgs(blockStyle, cardWidth, toolCardTitle("TOOL CALL", b.displayLabelID()), result, toolCardBg, railANSISeq("tool", b.Focused))
}

func appendDeleteRequestedPaths(result []string, b *Block, paths []string, width int) []string {
	if len(paths) == 0 {
		return result
	}
	items := make([]string, 0, len(paths))
	for _, path := range paths {
		items = append(items, b.displayToolPath(path))
	}
	return appendDeleteDisplaySection(result, b, deleteDisplaySection{label: "Targets", items: items}, width)
}

func parseDeleteDisplayResult(content string) (string, []deleteDisplaySection) {
	content = markdownutil.NormalizeNewlines(content)
	var headline string
	var sections []deleteDisplaySection
	current := -1
	for rawLine := range strings.SplitSeq(content, "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}
		if label, ok := deleteSectionLabel(line); ok {
			sections = append(sections, deleteDisplaySection{label: label})
			current = len(sections) - 1
			continue
		}
		if current >= 0 && strings.HasPrefix(line, "- ") {
			sections[current].items = append(sections[current].items, strings.TrimSpace(strings.TrimPrefix(line, "- ")))
			continue
		}
		if headline == "" {
			headline = line
		}
	}
	return headline, sections
}

func deleteSectionLabel(line string) (string, bool) {
	for _, label := range []string{"Deleted", "Already absent", "Blocked", "Failed", "Not attempted"} {
		if line == label+":" || (strings.HasPrefix(line, label+" (") && strings.HasSuffix(line, "):")) {
			return label, true
		}
	}
	return "", false
}

func deleteFailureSummary(headline string) string {
	trimmed := strings.TrimSuffix(strings.TrimSpace(headline), ".")
	lower := strings.ToLower(trimmed)
	switch {
	case strings.Contains(lower, "no files were removed"):
		return "No files were removed"
	case strings.Contains(lower, "stopped after an execution error"):
		return "Stopped after an execution error"
	case strings.EqualFold(trimmed, "Delete completed"):
		return ""
	default:
		return trimmed
	}
}

func appendDeleteDisplaySection(result []string, b *Block, section deleteDisplaySection, width int) []string {
	if len(section.items) == 0 {
		return result
	}
	style := deleteSectionStyle(section.label)
	if len(section.items) == 1 {
		text := section.label + " " + b.deleteDisplayItem(section.items[0])
		for i, line := range wrapText(sanitizeToolDisplayText(text), width) {
			if i == 0 {
				result = append(result, toolFieldLead+ToolFieldConnectorStyle.Render(toolFieldConnector)+style.Render(line))
				continue
			}
			result = append(result, toolFieldBody(style, line))
		}
		return result
	}
	result = append(result, toolFieldSection(style, fmt.Sprintf("%s (%d)", section.label, len(section.items))))
	for _, item := range section.items {
		for i, line := range wrapText(sanitizeToolDisplayText(b.deleteDisplayItem(item)), width-2) {
			prefix := "      "
			if i == 0 {
				prefix = "    • "
			}
			result = append(result, style.Render(prefix+line))
		}
	}
	return result
}

func deleteSectionStyle(label string) lipgloss.Style {
	switch label {
	case "Blocked", "Failed":
		return ErrorStyle
	case "Deleted":
		return ToolResultStyle
	default:
		return DimStyle
	}
}

func (b *Block) deleteDisplayItem(item string) string {
	path, detail, hasDetail := strings.Cut(item, " — ")
	path = b.displayToolPath(strings.TrimSpace(path))
	if !hasDetail || strings.TrimSpace(detail) == "" {
		return path
	}
	return path + " — " + strings.TrimSpace(detail)
}
