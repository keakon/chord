package requesttrim

import (
	"fmt"
	"strings"

	"github.com/keakon/chord/internal/tools"
)

func reduceToolErrorOutputSummary(ctx Context) string {
	lines := summarizeImportantLines(ctx.Content, 6)
	if len(lines) == 0 {
		lines = summarizeHeadTailLines(ctx.Content, 4)
	}
	if len(lines) == 0 {
		lines = []string{"- (no preserved error details)"}
	}
	return fmt.Sprintf("[Older %s error summarized for this request to save context; bytes=%d lines=%d]\n%s", ToolNameOrUnknown(ctx.ToolName), len(ctx.Content), countMeaningfulLines(ctx.Content), strings.Join(lines, "\n"))
}

func looksLikeBuildLikeLog(ctx Context) bool {
	if ctx.ToolName != tools.NameShell && ctx.ToolName != tools.NameEdit && ctx.ToolName != tools.NameApplyPatch && ctx.ToolName != tools.NameWrite {
		return false
	}
	content := strings.TrimSpace(ctx.Content)
	if content == "" {
		return false
	}
	// A single incidental marker must not classify an ordinary listing as a
	// build log. `git log --oneline` prints one commit per line, and a subject
	// such as "fix(tools): improve match-failure recovery" contains "fail";
	// treating that listing as a build log routes it through the signal-based
	// log summary, which keeps only the marker lines and drops every other
	// commit. Require either a line that leads with a failure verdict (a real
	// runner always emits one) or at least two marker lines.
	matches := 0
	strong := false
	forEachLine(content, func(line string) bool {
		lower := strings.ToLower(strings.TrimSpace(line))
		if lower == "" {
			return true
		}
		if strings.HasPrefix(lower, "fail ") ||
			strings.HasPrefix(lower, "fail\t") ||
			strings.HasPrefix(lower, "--- fail") ||
			strings.HasPrefix(lower, "build failed") ||
			strings.HasPrefix(lower, "lint failed") ||
			strings.HasPrefix(lower, "panic:") ||
			strings.HasPrefix(lower, "traceback") ||
			strings.HasPrefix(lower, "diagnostics:") {
			strong = true
			return false
		}
		if containsAnyMarker(lower, buildLogMarkers) {
			matches++
			if matches >= 2 {
				return false
			}
		}
		return true
	})
	return strong || matches >= 2
}

func reduceLongLogOutputSummary(ctx Context) string {
	counts := summarizeLogSignalCounts(ctx.Content)
	lines := summarizeRepresentativeLogLines(ctx.Content, 6)
	preserved := len(lines)
	if preserved == 0 {
		lines = []string{"- (no preserved log lines)"}
	}
	// State how much was dropped. This summary keeps only marker lines, so
	// without a count the model cannot tell "one matching line" from "the
	// output only had one line" and may read the summary as the whole result.
	total := countMeaningfulLines(ctx.Content)
	if omitted := total - preserved; omitted > 0 {
		noun := "lines"
		if omitted == 1 {
			noun = "line"
		}
		lines = append(lines, fmt.Sprintf("- ... (%d more %s omitted) ...", omitted, noun))
	}
	return fmt.Sprintf("[Older %s log summarized for this request to save context; lines=%d errors=%d warnings=%d failed=%d]\n%s", ToolNameOrUnknown(ctx.ToolName), total, counts.Errors, counts.Warnings, counts.Failures, strings.Join(lines, "\n"))
}

type logSignalCounts struct {
	Errors   int
	Warnings int
	Failures int
}

func summarizeLogSignalCounts(content string) logSignalCounts {
	var out logSignalCounts
	forEachLine(content, func(line string) bool {
		lower := strings.ToLower(strings.TrimSpace(line))
		if lower == "" {
			return true
		}
		if strings.Contains(lower, "error") || strings.Contains(lower, "panic:") || strings.Contains(lower, "exception") || strings.Contains(lower, "traceback") {
			out.Errors++
		}
		if strings.Contains(lower, "warning") || strings.Contains(lower, "warn") {
			out.Warnings++
		}
		if strings.Contains(lower, "failed") || strings.Contains(lower, "failure") {
			out.Failures++
		}
		return true
	})
	return out
}

// summarizeLinesMatching collects up to limit de-duplicated lines for which
// keep reports true, each rendered as a single-line bullet. It backs the
// log-line and error-line summaries, which differ only in their predicate.
func summarizeLinesMatching(content string, limit int, keep func(trimmed string) bool) []string {
	if limit <= 0 {
		return nil
	}
	out := make([]string, 0, limit)
	seen := make(map[string]struct{})
	forEachLine(content, func(line string) bool {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || !keep(trimmed) {
			return true
		}
		lineKey := TextSnippet(trimmed, SummaryLineSnippetChars)
		if _, ok := seen[lineKey]; ok {
			return true
		}
		seen[lineKey] = struct{}{}
		out = append(out, "- "+strings.ReplaceAll(lineKey, "\n", " "))
		return len(out) < limit
	})
	return out
}

func summarizeRepresentativeLogLines(content string, limit int) []string {
	return summarizeLinesMatching(content, limit, func(trimmed string) bool {
		return containsAnyMarker(strings.ToLower(trimmed), logLineMarkers)
	})
}

func summarizeImportantLines(content string, limit int) []string {
	return summarizeLinesMatching(content, limit, isImportantSummaryLine)
}

func isImportantSummaryLine(line string) bool {
	if containsAnyMarker(strings.ToLower(strings.TrimSpace(line)), importantLineMarkers) {
		return true
	}
	_, _, _, ok := parseSearchResultLine(line)
	return ok
}
