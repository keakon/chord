package tui

import (
	"regexp"
	"strings"

	"charm.land/lipgloss/v2"
)

func isAssistantSummaryFieldLine(line string) bool {
	return assistantSummaryFieldRE.MatchString(strings.TrimSpace(line))
}

func countLeadingAssistantSummaryFieldLines(lines []string) int {
	count := 0
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			if count > 0 {
				break
			}
			continue
		}
		if !isAssistantSummaryFieldLine(trimmed) {
			break
		}
		count++
	}
	return count
}

func (s assistantSummary) empty() bool {
	return strings.TrimSpace(s.TasksCompleted) == "" &&
		strings.TrimSpace(s.FilesModified) == "" &&
		strings.TrimSpace(s.Summary) == "" &&
		strings.TrimSpace(s.Issues) == ""
}

func (s assistantSummary) fieldCount() int {
	count := 0
	if strings.TrimSpace(s.TasksCompleted) != "" {
		count++
	}
	if strings.TrimSpace(s.FilesModified) != "" {
		count++
	}
	if strings.TrimSpace(s.Summary) != "" {
		count++
	}
	if strings.TrimSpace(s.Issues) != "" {
		count++
	}
	return count
}

// assistantSummaryFields 定义了 assistant 摘要块的固定四字段
var assistantSummaryFields = []string{
	"Tasks completed:",
	"Files modified:",
	"Summary:",
	"Issues:",
}

// assistantSummaryFieldRE 匹配摘要字段行（字段名 + 可选内容）
var assistantSummaryFieldRE = regexp.MustCompile(`^(` + regexp.QuoteMeta(assistantSummaryFields[0]) + `|` +
	regexp.QuoteMeta(assistantSummaryFields[1]) + `|` +
	regexp.QuoteMeta(assistantSummaryFields[2]) + `|` +
	regexp.QuoteMeta(assistantSummaryFields[3]) + `)\s*(.*)$`)

// assistantSummary holds the parsed summary fields from an assistant message.
type assistantSummary struct {
	TasksCompleted string
	FilesModified  string
	Summary        string
	Issues         string
	// HasMeta indicates whether any summary field was found
	HasMeta bool
}

// parseAssistantSummary extracts the four standard summary fields from content.
// It only recognizes a dedicated summary block that starts at the beginning of
// the content and contains at least two consecutive summary-field lines.
func parseAssistantSummary(content string) assistantSummary {
	content = strings.TrimSpace(content)
	if content == "" {
		return assistantSummary{}
	}

	lines := strings.Split(content, "\n")
	if countLeadingAssistantSummaryFieldLines(lines) < 2 {
		return assistantSummary{}
	}

	var summary assistantSummary
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			if summary.HasMeta {
				break
			}
			continue
		}
		matches := assistantSummaryFieldRE.FindStringSubmatch(trimmed)
		if matches == nil {
			break
		}
		fieldValue := strings.TrimSpace(matches[2])
		switch matches[1] {
		case "Tasks completed:":
			summary.TasksCompleted = fieldValue
		case "Files modified:":
			summary.FilesModified = fieldValue
		case "Summary:":
			summary.Summary = fieldValue
		case "Issues:":
			summary.Issues = fieldValue
		}
		summary.HasMeta = true
	}
	if summary.empty() {
		return assistantSummary{}
	}
	return summary
}

// stripAssistantSummary returns the content with the summary header removed.
// If no summary was found, returns the original content.
func stripAssistantSummary(content string, summary assistantSummary) string {
	if !summary.HasMeta || summary.fieldCount() < 2 {
		return content
	}

	lines := strings.Split(content, "\n")
	var result []string
	skipping := true
	foundAny := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			if foundAny {
				skipping = false
				continue
			}
			if skipping {
				continue
			}
		}

		if skipping {
			matches := assistantSummaryFieldRE.FindStringSubmatch(trimmed)
			if matches != nil {
				foundAny = true
				continue
			}
			skipping = false
		}

		result = append(result, line)
	}

	out := strings.Join(result, "\n")
	out = strings.TrimSpace(out)
	return out
}

// renderAssistantSummaryLines renders the summary as weak/dim lines.
// Returns nil if no summary fields were found.
func renderAssistantSummaryLines(summary assistantSummary, width int) []string {
	if !summary.HasMeta {
		return nil
	}

	var lines []string
	style := lipgloss.NewStyle().Foreground(lipgloss.Color(currentTheme.DimFg))

	if summary.TasksCompleted != "" {
		line := "Tasks completed: " + summary.TasksCompleted
		lines = append(lines, style.Render(truncateOneLine(line, width)))
	}
	if summary.FilesModified != "" {
		line := "Files modified: " + summary.FilesModified
		lines = append(lines, style.Render(truncateOneLine(line, width)))
	}
	if summary.Summary != "" {
		line := "Summary: " + summary.Summary
		lines = append(lines, style.Render(truncateOneLine(line, width)))
	}
	if summary.Issues != "" {
		line := "Issues: " + summary.Issues
		lines = append(lines, style.Render(truncateOneLine(line, width)))
	}

	return lines
}
