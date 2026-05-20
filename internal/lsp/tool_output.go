package lsp

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/keakon/x/powernap/pkg/lsp/protocol"

	"github.com/keakon/chord/internal/config"
)

// Limits for tool result text appended to the model (aligned with opencode).
const (
	ToolOutputMaxErrorsPerFile   = 20
	ToolOutputMaxOtherErrorFiles = 5
)

// AppendLSPDiagnosticsToToolOutput appends all LSP diagnostics (severity 1-4)
// to the tool result string so the model can act on them. Diagnostics for the
// edited file are listed first; optionally include diagnostics from other files.
func (m *Manager) AppendLSPDiagnosticsToToolOutput(base, editedPath string, includeOtherFiles bool) string {
	return m.appendLSPDiagnosticsToToolOutput(base, editedPath, includeOtherFiles, false, nil, config.DiagnosticOutputConfig{})
}

func (m *Manager) appendLSPDiagnosticsToToolOutput(base, editedPath string, includeOtherFiles bool, showCleanStatus bool, ranges []EditRange, output config.DiagnosticOutputConfig) string {
	if m == nil {
		return base
	}

	edited, err := filepath.Abs(editedPath)
	if err != nil {
		edited = filepath.Clean(editedPath)
	} else {
		edited = filepath.Clean(edited)
	}

	byPath := m.allDiagnosticsByAbsPath()

	primary := byPath[edited]
	hasDiagnostics := len(primary) > 0
	if !hasDiagnostics && includeOtherFiles {
		for p, diags := range byPath {
			if p != edited && len(diags) > 0 {
				hasDiagnostics = true
				break
			}
		}
	}
	if !hasDiagnostics {
		if showCleanStatus {
			return base + "\n\nDiagnostics:\nUsed LSP diagnostics.\nNo diagnostics found."
		}
		return base
	}

	var b strings.Builder
	b.WriteString(base)
	b.WriteString("\n\nDiagnostics:\nUsed LSP diagnostics.")

	sort.SliceStable(primary, func(i, j int) bool {
		return diagnosticLess(primary[i], primary[j])
	})
	appendDiagBlock := func(file string, diags []Diagnostic, thisFile bool) {
		if len(diags) == 0 {
			return
		}
		if thisFile {
			b.WriteString(formatDiagnosticsBlockWithRanges(file, diags, output, ranges, true))
			return
		}
		b.WriteString(formatDiagnosticsBlockWithRanges(file, diags, output, nil, false))
	}
	appendDiagBlock(edited, primary, true)

	if !includeOtherFiles {
		return b.String()
	}

	var others []string
	for p, diags := range byPath {
		if p == edited || len(diags) == 0 {
			continue
		}
		sort.SliceStable(diags, func(i, j int) bool {
			return diagnosticLess(diags[i], diags[j])
		})
		others = append(others, p)
	}
	sort.Strings(others)

	n := 0
	for _, p := range others {
		if n >= ToolOutputMaxOtherErrorFiles {
			break
		}
		appendDiagBlock(p, byPath[p], false)
		n++
	}

	return b.String()
}

func (m *Manager) allDiagnosticsByAbsPath() map[string][]Diagnostic {
	m.clientsMu.RLock()
	defer m.clientsMu.RUnlock()
	merged := make(map[string][]Diagnostic)
	for _, c := range m.clients {
		c.diagnosticsMu.RLock()
		for uri, diags := range c.diagnostics {
			p, err := protocol.DocumentURI(uri).Path()
			if err != nil {
				continue
			}
			absP, err := filepath.Abs(p)
			if err != nil {
				absP = filepath.Clean(p)
			} else {
				absP = filepath.Clean(absP)
			}
			for _, d := range diags {
				merged[absP] = append(merged[absP], Diagnostic{
					Severity: int(d.Severity),
					Line:     int(d.Range.Start.Line),
					Col:      int(d.Range.Start.Character),
					Code:     diagnosticCodeString(d.Code),
					Message:  d.Message,
					Source:   d.Source,
				})
			}
		}
		c.diagnosticsMu.RUnlock()
	}
	return merged
}

func formatDiagnosticsBlock(file string, diags []Diagnostic, max int, thisFile bool) string {
	if len(diags) == 0 {
		return ""
	}

	var lines []string
	lim := len(diags)
	suffix := ""
	if lim > max {
		lim = max
		suffix = fmt.Sprintf("\n... and %d more", len(diags)-max)
	}
	for i := 0; i < lim; i++ {
		lines = append(lines, formatDiagLine(diags[i]))
	}

	if thisFile {
		return fmt.Sprintf("\n\n%s%s", strings.Join(lines, "\n"), suffix)
	}
	return fmt.Sprintf("\n\nLSP diagnostics in other files:\n%s\n%s%s",
		file, strings.Join(lines, "\n"), suffix)
}

func formatHiddenDiagnosticsBlock(file string, hidden int, thisFile bool) string {
	if hidden <= 0 {
		return ""
	}
	line := fmt.Sprintf("Diagnostics hidden by output filters: %d info/hint diagnostics omitted.", hidden)
	if thisFile {
		return "\n\n" + line
	}
	return fmt.Sprintf("\n\nLSP diagnostics in other files:\n%s\n%s", file, line)
}

func formatDiagnosticsBlockWithRanges(file string, diags []Diagnostic, output config.DiagnosticOutputConfig, ranges []EditRange, thisFile bool) string {
	if len(diags) == 0 {
		return ""
	}
	if len(ranges) == 0 {
		filtered := filterDiagnosticsByOutput(diags, output)
		if len(filtered) == 0 {
			return formatHiddenDiagnosticsBlock(file, len(diags), thisFile)
		}
		max := output.MaxTotalDiagnostics
		if max <= 0 {
			max = ToolOutputMaxErrorsPerFile
		}
		sort.SliceStable(filtered, func(i, j int) bool { return diagnosticLess(filtered[i], filtered[j]) })
		return formatDiagnosticsBlock(file, filtered, max, thisFile)
	}
	before := output.NearRangeBeforeLines
	if before <= 0 {
		before = 20
	}
	after := output.NearRangeAfterLines
	if after <= 0 {
		after = 80
	}
	maxNear := output.MaxNearDiagnostics
	if maxNear <= 0 {
		maxNear = 12
	}
	maxOutside := output.MaxOutsideDiagnostics
	if maxOutside <= 0 {
		maxOutside = 5
	}
	maxTotal := output.MaxTotalDiagnostics
	if maxTotal <= 0 {
		maxTotal = ToolOutputMaxErrorsPerFile
	}

	filtered := filterDiagnosticsByOutput(diags, output)
	if len(filtered) == 0 {
		return formatHiddenDiagnosticsBlock(file, len(diags), thisFile)
	}
	near, outside := splitDiagnosticsByRange(filtered, ranges, before, after)
	sort.SliceStable(near, func(i, j int) bool { return diagnosticNearLess(near[i], near[j], ranges) })
	sort.SliceStable(outside, func(i, j int) bool { return diagnosticNearLess(outside[i], outside[j], ranges) })

	selected := make([]Diagnostic, 0, min(maxTotal, len(filtered)))
	selected = appendLimitedDiagnostics(selected, near, min(maxNear, maxTotal))
	remainingSlots := maxTotal - len(selected)
	if remainingSlots > 0 {
		selected = appendLimitedDiagnostics(selected, outside, min(maxOutside, remainingSlots))
	}
	if len(selected) == 0 {
		return ""
	}
	omitted := len(filtered) - len(selected)
	var lines []string
	for _, d := range selected {
		lines = append(lines, formatDiagLine(d))
	}
	if omitted > 0 {
		lines = append(lines, fmt.Sprintf("... and %d more diagnostics omitted.", omitted))
	}
	if thisFile {
		return "\n\n" + strings.Join(lines, "\n")
	}
	return fmt.Sprintf("\n\nLSP diagnostics in other files:\n%s\n%s", file, strings.Join(lines, "\n"))
}

func appendLimitedDiagnostics(dst, src []Diagnostic, limit int) []Diagnostic {
	for i := 0; i < len(src) && i < limit; i++ {
		dst = append(dst, src[i])
	}
	return dst
}

func filterDiagnosticsByOutput(diags []Diagnostic, output config.DiagnosticOutputConfig) []Diagnostic {
	out := make([]Diagnostic, 0, len(diags))
	for _, d := range diags {
		if d.Severity == 3 && !output.IncludeInfo {
			continue
		}
		if d.Severity == 4 && !output.IncludeHints {
			continue
		}
		out = append(out, d)
	}
	return out
}

func splitDiagnosticsByRange(diags []Diagnostic, ranges []EditRange, before, after int) (near, outside []Diagnostic) {
	for _, d := range diags {
		if diagnosticNearRanges(d, ranges, before, after) {
			near = append(near, d)
		} else {
			outside = append(outside, d)
		}
	}
	return near, outside
}

func diagnosticNearRanges(d Diagnostic, ranges []EditRange, before, after int) bool {
	for _, r := range ranges {
		if d.Line >= r.StartLine-before && d.Line <= r.EndLine+after {
			return true
		}
	}
	return false
}

func diagnosticDistanceToRanges(d Diagnostic, ranges []EditRange) int {
	best := int(^uint(0) >> 1)
	for _, r := range ranges {
		dist := 0
		if d.Line < r.StartLine {
			dist = r.StartLine - d.Line
		} else if d.Line > r.EndLine {
			dist = d.Line - r.EndLine
		}
		if dist < best {
			best = dist
		}
	}
	return best
}

func diagnosticNearLess(a, b Diagnostic, ranges []EditRange) bool {
	if a.Severity != b.Severity {
		return a.Severity < b.Severity
	}
	ad, bd := diagnosticDistanceToRanges(a, ranges), diagnosticDistanceToRanges(b, ranges)
	if ad != bd {
		return ad < bd
	}
	return diagnosticLess(a, b)
}

func diagnosticLess(a, b Diagnostic) bool {
	if a.Severity != b.Severity {
		return a.Severity < b.Severity
	}
	if a.Line != b.Line {
		return a.Line < b.Line
	}
	if a.Col != b.Col {
		return a.Col < b.Col
	}
	if a.Code != b.Code {
		return a.Code < b.Code
	}
	return a.Message < b.Message
}

func severityPrefix(severity int) string {
	switch severity {
	case 1:
		return "[E]"
	case 2:
		return "[W]"
	case 3:
		return "[I]"
	case 4:
		return "[H]"
	default:
		return "[?]"
	}
}

func formatDiagLine(d Diagnostic) string {
	pfx := severityPrefix(d.Severity)
	msg := d.Message
	if d.Code != "" && !strings.HasPrefix(msg, "["+d.Code+"]") {
		msg = fmt.Sprintf("[%s] %s", d.Code, msg)
	}
	return fmt.Sprintf("%s %d:%d %s", pfx, d.Line+1, d.Col+1, msg)
}
