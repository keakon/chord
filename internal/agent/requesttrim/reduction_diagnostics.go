package requesttrim

import (
	"fmt"
	"strings"

	"github.com/keakon/chord/internal/lsp"
)

// DiagnosticsSectionLabel matches the LSP diagnostics section header anywhere
// in a tool result. Producers emit it after a blank line, but the preceding
// output may already end with newlines, so detection stays deliberately loose.
const DiagnosticsSectionLabel = "Diagnostics:"

func reduceDiagnosticsToolOutput(content string, superseded bool) (string, bool) {
	idx := strings.Index(content, lsp.DiagnosticsSectionMarker)
	sepLen := len(lsp.DiagnosticsSectionMarker)
	if idx < 0 {
		idx = strings.Index(content, "\nDiagnostics:\n")
		sepLen = len("\nDiagnostics:\n")
	}
	if idx < 0 {
		return content, false
	}
	prefix := strings.TrimRight(content[:idx], "\n")
	diagnostics := strings.TrimSpace(content[idx+sepLen:])
	if diagnostics == "" {
		return content, false
	}
	lines := strings.Split(diagnostics, "\n")
	if superseded {
		summary := preferredDiagnosticsSummaryLine(lines)
		if summary == "" {
			summary = "diagnostics were present"
		}
		return prefix + "\n\nDiagnostics summary:\n[Older diagnostics details omitted; a newer diagnostics output appears later in this conversation; prefer that tool result.]\n" + summary, true
	}
	return prefix + "\n\nDiagnostics summary:\n" + renderDiagnosticsSummaryLines(diagnostics), true
}

// renderDiagnosticsSummaryLines renders a bounded full location list from a
// diagnostics block, dropping only the explanatory status/change-summary prose
// around the locations. Diagnostics lines are short location records (severity,
// line:col, code, message)); the list cap is a safety net well above what the
// LSP/Ruff output configs can emit. Keeping every location (rather than one
// preferred line) preserves the edit-diagnostics iteration feedback signal.
func renderDiagnosticsSummaryLines(diagnostics string) string {
	const maxLines = 40
	lines := make([]string, 0)
	omitted := 0
	for line := range strings.SplitSeq(diagnostics, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || isNoisyDiagnosticsSummaryLine(trimmed) {
			continue
		}
		if len(lines) >= maxLines {
			omitted++
			continue
		}
		lines = append(lines, trimmed)
	}
	if len(lines) == 0 {
		lines = append(lines, "no actionable diagnostics preserved")
	}
	if omitted > 0 {
		lines = append(lines, fmt.Sprintf("- ... (%d more diagnostics omitted) ...", omitted))
	}
	return strings.Join(lines, "\n")
}

func preferredDiagnosticsSummaryLine(lines []string) string {
	preferredPrefixes := []string{
		"Python diagnostics skipped:",
		"Ruff diagnostics failed:",
		"Ruff quick diagnostics failed:",
	}
	for _, prefix := range preferredPrefixes {
		for _, line := range lines {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, prefix) {
				return trimmed
			}
		}
	}
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" && !isNoisyDiagnosticsSummaryLine(trimmed) {
			return trimmed
		}
	}
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func isNoisyDiagnosticsSummaryLine(line string) bool {
	return strings.HasPrefix(line, "Diagnostics status:") ||
		strings.HasPrefix(line, "Used LSP diagnostics") ||
		strings.HasPrefix(line, "Used Ruff quick diagnostics") ||
		strings.HasPrefix(line, "Full Python semantic diagnostics")
}
