package lsp

import (
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/keakon/x/powernap/pkg/lsp/protocol"

	"github.com/keakon/chord/internal/config"
)

// Limits for tool result text appended to the model (aligned with opencode).
const (
	ToolOutputMaxDiagnosticsPerFile  = 10
	ToolOutputMaxDiagnosticsPerBatch = 10
	ToolOutputMaxOtherErrorFiles     = 5
)

// AppendLSPDiagnosticsToToolOutput appends all LSP diagnostics (severity 1-4)
// to the tool result string so the model can act on them. Diagnostics for the
// edited file are listed first; optionally include diagnostics from other files.
func (m *Manager) AppendLSPDiagnosticsToToolOutput(base, editedPath string, includeOtherFiles bool, displayBaseDir string) string {
	return m.appendLSPDiagnosticsToToolOutput(base, editedPath, includeOtherFiles, nil, config.DiagnosticOutputConfig{}, displayBaseDir)
}

// DiagnosticOutputConfigForPath returns the configured output policy for a
// file. Batch editors use this to keep their per-file selection rules aligned
// with the single-file edit/write path.
func (m *Manager) DiagnosticOutputConfigForPath(path string) config.DiagnosticOutputConfig {
	if m == nil {
		return config.DiagnosticOutputConfig{}
	}
	return diagnosticsOutputConfig(m.cfg, path)
}

// AppendLSPDiagnosticsToToolOutputForPaths appends diagnostics for a batch of
// files changed by one tool call. All changed files are treated as primary
// files, so diagnostics are selected once with one shared output budget rather
// than being appended once per file. baselines is keyed by normalized absolute
// path and feeds the per-file "(N new, M resolved)" header counts; a nil map
// means no baseline information and omits the counts entirely.
func (m *Manager) AppendLSPDiagnosticsToToolOutputForPaths(base string, editedPaths []string, includeOtherFiles bool, baselines map[string][]Diagnostic, outputs map[string]config.DiagnosticOutputConfig, extras map[string][]Diagnostic, displayBaseDir string) string {
	if m == nil {
		return base
	}
	byPath := m.allDiagnosticsByAbsPath()
	for path, diags := range extras {
		byPath[path] = append(byPath[path], diags...)
	}
	primaryPaths := make([]string, 0, len(editedPaths))
	seen := make(map[string]struct{}, len(editedPaths))
	for _, path := range editedPaths {
		abs, err := filepath.Abs(path)
		if err != nil {
			abs = filepath.Clean(path)
		} else {
			abs = filepath.Clean(abs)
		}
		if _, ok := seen[abs]; ok {
			continue
		}
		seen[abs] = struct{}{}
		primaryPaths = append(primaryPaths, abs)
	}

	// Without LSP server coverage for any changed file (and without direct
	// backend diagnostics such as Ruff), every cached diagnostic belongs to
	// files edited earlier in the session. Attaching them would misattribute
	// unrelated errors to this tool call, so keep the base output as-is.
	if len(extras) == 0 {
		covered := slices.ContainsFunc(primaryPaths, m.HasServerForPath)
		if !covered {
			return base
		}
	}

	maxTotal := ToolOutputMaxDiagnosticsPerBatch
	remaining := maxTotal
	selectedByPath := make(map[string][]Diagnostic)
	omitted := 0
	selectWithinRemaining := func(path string, diags []Diagnostic) ([]Diagnostic, int) {
		if remaining <= 0 {
			return nil, len(diags)
		}
		output := outputs[path]
		output.MaxTotalDiagnostics = remaining
		selected, count := selectDiagnosticsByOutput(diags, output, nil)
		remaining -= len(selected)
		return selected, count
	}
	for _, path := range primaryPaths {
		selected, count := selectWithinRemaining(path, deduplicateDiagnostics(byPath[path]))
		selectedByPath[path] = selected
		omitted += count
	}

	var others []otherFileDiagnostics
	if includeOtherFiles && remaining > 0 {
		primary := make(map[string]struct{}, len(primaryPaths))
		for _, path := range primaryPaths {
			primary[path] = struct{}{}
		}
		paths := make([]string, 0, len(byPath))
		for path := range byPath {
			if _, ok := primary[path]; !ok {
				paths = append(paths, path)
			}
		}
		sort.Strings(paths)
		for _, path := range paths {
			if remaining <= 0 || len(others) >= ToolOutputMaxOtherErrorFiles {
				break
			}
			selected, count := selectWithinRemaining(path, deduplicateDiagnostics(byPath[path]))
			if len(selected) == 0 {
				continue
			}
			others = append(others, otherFileDiagnostics{path: path, diags: selected})
			omitted += count
		}
	}

	hasBlocks := len(others) > 0
	for _, path := range primaryPaths {
		if len(selectedByPath[path]) > 0 {
			hasBlocks = true
			break
		}
	}
	if !hasBlocks {
		return base
	}

	multi := len(primaryPaths) > 1
	var b strings.Builder
	b.WriteString(base)
	b.WriteString("\n\nDiagnostics:\n")
	wroteBlock := false
	writeBlock := func(block string) {
		if wroteBlock {
			b.WriteByte('\n')
		}
		b.WriteString(block)
		wroteBlock = true
	}
	for _, path := range primaryPaths {
		var counts string
		if multi && baselines != nil {
			counts = diagnosticChangeCounts(baselines[path], byPath[path])
		}
		diags := selectedByPath[path]
		switch {
		case len(diags) > 0 && multi:
			header := displayDiagnosticPath(path, displayBaseDir)
			if counts != "" {
				header += " (" + counts + ")"
			}
			writeBlock(header + ":\n" + strings.Join(formatDiagnosticLines(diags), "\n"))
		case len(diags) > 0:
			writeBlock(strings.Join(formatDiagnosticLines(diags), "\n"))
		case counts != "" && multi:
			writeBlock(displayDiagnosticPath(path, displayBaseDir) + ": " + counts + ".")
		}
	}
	appendOtherFileDiagnosticsSection(&b, others, wroteBlock, displayBaseDir)
	if omitted > 0 {
		b.WriteByte('\n')
		b.WriteString(diagnosticsOmittedLine(omitted))
	}
	if len(primaryPaths) == 1 && baselines != nil {
		if changed := diagnosticChangeSummary(baselines[primaryPaths[0]], byPath[primaryPaths[0]]); changed != "" {
			b.WriteByte('\n')
			b.WriteString(changed)
		}
	}
	return b.String()
}

// ParseToolOutputDiagnostics extracts diagnostics produced by a non-LSP
// backend (currently Ruff) from the common tool-output representation.
func ParseToolOutputDiagnostics(text string) []Diagnostic {
	var out []Diagnostic
	for raw := range strings.SplitSeq(text, "\n") {
		line := strings.TrimSpace(raw)
		if len(line) < 8 || line[0] != '[' {
			continue
		}
		end := strings.Index(line, "] ")
		if end != 2 || (line[1] != 'E' && line[1] != 'W' && line[1] != 'I' && line[1] != 'H') {
			continue
		}
		rest := line[4:]
		colon := strings.Index(rest, ":")
		space := strings.Index(rest, " ")
		if colon <= 0 || space <= colon {
			continue
		}
		lineNo, err1 := strconv.Atoi(rest[:colon])
		colNo, err2 := strconv.Atoi(rest[colon+1 : space])
		if err1 != nil || err2 != nil {
			continue
		}
		message := strings.TrimSpace(rest[space+1:])
		code := ""
		if strings.HasPrefix(message, "[") {
			if endCode := strings.Index(message, "] "); endCode > 1 {
				code = message[1:endCode]
				message = message[endCode+2:]
			}
		}
		severity := 4
		switch line[1] {
		case 'E':
			severity = 1
		case 'W':
			severity = 2
		case 'I':
			severity = 3
		}
		out = append(out, Diagnostic{Severity: severity, Line: lineNo - 1, Col: colNo - 1, Code: code, Message: message})
	}
	return out
}

func formatDiagnosticLines(diags []Diagnostic) []string {
	lines := make([]string, 0, len(diags))
	for _, d := range diags {
		lines = append(lines, formatDiagLine(d))
	}
	return lines
}

type diagnosticIdentity struct {
	severity int
	line     int
	col      int
	code     string
	message  string
}

func diagnosticIdentityKey(d Diagnostic) diagnosticIdentity {
	// Source is deliberately excluded: rendered lines never show it, and a
	// diagnostic that round-trips through rendered text loses it.
	return diagnosticIdentity{severity: d.Severity, line: d.Line, col: d.Col, code: d.Code, message: d.Message}
}

func deduplicateDiagnostics(diags []Diagnostic) []Diagnostic {
	seen := make(map[diagnosticIdentity]struct{}, len(diags))
	out := make([]Diagnostic, 0, len(diags))
	for _, d := range diags {
		key := diagnosticIdentityKey(d)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, d)
	}
	return out
}

func (m *Manager) appendLSPDiagnosticsToToolOutput(base, editedPath string, includeOtherFiles bool, ranges []EditRange, output config.DiagnosticOutputConfig, displayBaseDir string) string {
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
	var others []otherFileDiagnostics
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
			others = append(others, otherFileDiagnostics{path: p, diags: selected})
		}
	}
	if len(primary) == 0 && len(others) == 0 {
		return base
	}

	var b strings.Builder
	b.WriteString(base)
	b.WriteString("\n\nDiagnostics:\n")

	if len(primary) > 0 {
		b.WriteString(strings.Join(formatDiagnosticLines(primary), "\n"))
	}
	appendOtherFileDiagnosticsSection(&b, others, len(primary) > 0, displayBaseDir)

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

// otherFileDiagnostics pairs a non-primary file with the diagnostics selected
// for it under the shared output budget.
type otherFileDiagnostics struct {
	path  string
	diags []Diagnostic
}

// otherFilesDiagnosticsHeader labels the non-primary-file section; the
// review-state parser keys on it to stop counting primary-file lines.
const otherFilesDiagnosticsHeader = "LSP diagnostics in other files:"

// appendOtherFileDiagnosticsSection writes one shared "LSP diagnostics in
// other files:" header followed by a compact path-labelled block per file.
// afterBlock inserts a blank line so the section stays visually separate from
// preceding primary-file blocks.
func appendOtherFileDiagnosticsSection(b *strings.Builder, others []otherFileDiagnostics, afterBlock bool, displayBaseDir string) {
	for i, other := range others {
		if i == 0 {
			if afterBlock {
				b.WriteString("\n\n")
			}
			b.WriteString(otherFilesDiagnosticsHeader)
		}
		b.WriteByte('\n')
		b.WriteString(displayDiagnosticPath(other.path, displayBaseDir))
		b.WriteString(":\n")
		b.WriteString(strings.Join(formatDiagnosticLines(other.diags), "\n"))
	}
}

func displayDiagnosticPath(path, baseDir string) string {
	path = strings.TrimSpace(path)
	baseDir = strings.TrimSpace(baseDir)
	if path == "" || baseDir == "" {
		return path
	}
	path = filepath.Clean(path)
	baseDir = filepath.Clean(baseDir)
	base, baseErr := filepath.Abs(baseDir)
	target, targetErr := filepath.Abs(path)
	if baseErr != nil || targetErr != nil {
		return path
	}
	rel, err := filepath.Rel(filepath.Clean(base), filepath.Clean(target))
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return path
	}
	return filepath.ToSlash(rel)
}

func formatDiagnosticsBlockWithRanges(diags []Diagnostic, output config.DiagnosticOutputConfig, ranges []EditRange) string {
	selected, omitted := selectDiagnosticsByOutput(diags, output, ranges)
	if len(selected) == 0 {
		return ""
	}
	out := strings.Join(formatDiagnosticLines(selected), "\n")
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
