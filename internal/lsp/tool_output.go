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
	ToolOutputMaxDiagnosticsPerFile = 10
	ToolOutputMaxOtherErrorFiles    = 5
)

// AppendLSPDiagnosticsToToolOutput appends all LSP diagnostics (severity 1-4)
// to the tool result string so the model can act on them. Diagnostics for the
// edited file are listed first; optionally include diagnostics from other files.
func (m *Manager) AppendLSPDiagnosticsToToolOutput(base, editedPath string, includeOtherFiles bool) string {
	return m.appendLSPDiagnosticsToToolOutput(base, editedPath, includeOtherFiles, nil, config.DiagnosticOutputConfig{})
}

func (m *Manager) appendLSPDiagnosticsToToolOutput(base, editedPath string, includeOtherFiles bool, ranges []EditRange, output config.DiagnosticOutputConfig) string {
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

	maxTotal := output.MaxTotalDiagnostics
	if maxTotal <= 0 {
		maxTotal = ToolOutputMaxDiagnosticsPerFile
	}
	remaining := maxTotal
	selectWithinRemaining := func(diags []Diagnostic, ranges []EditRange) []Diagnostic {
		if remaining <= 0 {
			return nil
		}
		limitedOutput := output
		limitedOutput.MaxTotalDiagnostics = remaining
		selected, _ := selectDiagnosticsByOutput(diags, limitedOutput, ranges)
		remaining -= len(selected)
		return selected
	}

	primary := selectWithinRemaining(byPath[edited], ranges)
	type otherDiagnostics struct {
		path  string
		diags []Diagnostic
	}
	var others []otherDiagnostics
	if includeOtherFiles && remaining > 0 {
		otherPaths := make([]string, 0, len(byPath))
		for p, diags := range byPath {
			if p == edited || len(diags) == 0 {
				continue
			}
			otherPaths = append(otherPaths, p)
		}
		sort.Strings(otherPaths)
		for _, p := range otherPaths {
			if remaining <= 0 || len(others) >= ToolOutputMaxOtherErrorFiles {
				break
			}
			selected := selectWithinRemaining(byPath[p], nil)
			if len(selected) == 0 {
				continue
			}
			others = append(others, otherDiagnostics{path: p, diags: selected})
		}
	}
	if len(primary) == 0 && len(others) == 0 {
		return base
	}

	var b strings.Builder
	b.WriteString(base)
	b.WriteString("\n\nDiagnostics:\n")

	wroteBlock := false
	appendDiagBlock := func(file string, diags []Diagnostic, thisFile bool) {
		if len(diags) == 0 {
			return
		}
		if wroteBlock {
			b.WriteString("\n\n")
		}
		block := formatSelectedDiagnosticsBlock(file, diags, thisFile)
		b.WriteString(strings.TrimLeft(block, "\n"))
		wroteBlock = true
	}
	appendDiagBlock(edited, primary, true)

	for _, other := range others {
		appendDiagBlock(other.path, other.diags, false)
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

func diagnosticsOmittedLine(count int) string {
	return fmt.Sprintf("... %d diagnostics not shown due to output limits; they may still need fixing.", count)
}

func formatSelectedDiagnosticsBlock(file string, diags []Diagnostic, thisFile bool) string {
	if len(diags) == 0 {
		return ""
	}

	var lines []string
	for _, d := range diags {
		lines = append(lines, formatDiagLine(d))
	}

	if thisFile {
		return strings.Join(lines, "\n")
	}
	return fmt.Sprintf("\n\nLSP diagnostics in other files:\n%s\n%s", file, strings.Join(lines, "\n"))
}

func formatDiagnosticsBlockWithRanges(file string, diags []Diagnostic, output config.DiagnosticOutputConfig, ranges []EditRange, thisFile bool) string {
	selected, omitted := selectDiagnosticsByOutput(diags, output, ranges)
	if len(selected) == 0 {
		return ""
	}
	out := formatSelectedDiagnosticsBlock(file, selected, thisFile)
	if omitted > 0 {
		out += "\n" + diagnosticsOmittedLine(omitted)
	}
	return out
}

func selectDiagnosticsByOutput(diags []Diagnostic, output config.DiagnosticOutputConfig, ranges []EditRange) ([]Diagnostic, int) {
	if len(diags) == 0 {
		return nil, 0
	}
	maxTotal := output.MaxTotalDiagnostics
	if maxTotal <= 0 {
		maxTotal = ToolOutputMaxDiagnosticsPerFile
	}
	if len(ranges) == 0 {
		return selectDiagnosticsByLimit(diags, maxTotal)
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
		maxNear = 10
	}
	maxOutside := output.MaxOutsideDiagnostics
	if maxOutside <= 0 {
		maxOutside = 5
	}

	near, outside := splitDiagnosticsByRange(diags, ranges, before, after)
	sort.SliceStable(near, func(i, j int) bool { return diagnosticNearLess(near[i], near[j], ranges) })
	sort.SliceStable(outside, func(i, j int) bool { return diagnosticNearLess(outside[i], outside[j], ranges) })
	selected := make([]Diagnostic, 0, min(maxTotal, len(diags)))
	nearSelected := 0
	outsideSelected := 0
	selected, nearSelected = appendLimitedDiagnosticsBySeverity(selected, near, min(maxNear, maxTotal), true)
	remainingSlots := maxTotal - len(selected)
	if remainingSlots > 0 {
		selected, outsideSelected = appendLimitedDiagnosticsBySeverity(selected, outside, min(maxOutside, remainingSlots), true)
	}
	remainingSlots = maxTotal - len(selected)
	if remainingSlots > 0 && nearSelected < maxNear {
		var appended int
		selected, appended = appendLimitedDiagnosticsBySeverity(selected, near, min(maxNear-nearSelected, remainingSlots), false)
		nearSelected += appended
	}
	remainingSlots = maxTotal - len(selected)
	if remainingSlots > 0 && outsideSelected < maxOutside {
		selected, _ = appendLimitedDiagnosticsBySeverity(selected, outside, min(maxOutside-outsideSelected, remainingSlots), false)
	}
	return selected, len(diags) - len(selected)
}

func selectDiagnosticsByLimit(diags []Diagnostic, limit int) ([]Diagnostic, int) {
	if limit <= 0 {
		limit = ToolOutputMaxDiagnosticsPerFile
	}
	sorted := append([]Diagnostic(nil), diags...)
	sort.SliceStable(sorted, func(i, j int) bool { return diagnosticLess(sorted[i], sorted[j]) })
	selected := appendLimitedDiagnosticsByPriority(nil, sorted, limit)
	return selected, len(sorted) - len(selected)
}

func appendLimitedDiagnosticsByPriority(dst, src []Diagnostic, limit int) []Diagnostic {
	var appended int
	dst, appended = appendLimitedDiagnosticsBySeverity(dst, src, limit, true)
	if appended < limit {
		dst, _ = appendLimitedDiagnosticsBySeverity(dst, src, limit-appended, false)
	}
	return dst
}

func appendLimitedDiagnosticsBySeverity(dst, src []Diagnostic, limit int, majorOnly bool) ([]Diagnostic, int) {
	if limit <= 0 {
		return dst, 0
	}
	appended := 0
	for _, d := range src {
		if majorOnly != (d.Severity <= 2) {
			continue
		}
		dst = append(dst, d)
		appended++
		if appended >= limit {
			return dst, appended
		}
	}
	return dst, appended
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
