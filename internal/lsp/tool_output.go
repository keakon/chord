package lsp

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/charmbracelet/x/powernap/pkg/lsp/protocol"
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

	var b strings.Builder
	b.WriteString(base)

	primary := byPath[edited]
	sort.SliceStable(primary, func(i, j int) bool {
		return diagnosticLess(primary[i], primary[j])
	})
	appendDiagBlock := func(file string, diags []Diagnostic, thisFile bool) {
		if len(diags) == 0 {
			return
		}
		sort.SliceStable(diags, func(i, j int) bool {
			return diagnosticLess(diags[i], diags[j])
		})
		b.WriteString(formatDiagnosticsBlock(file, diags, ToolOutputMaxErrorsPerFile, thisFile))
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
	return fmt.Sprintf("%s %d:%d %s", pfx, d.Line+1, d.Col+1, d.Message)
}
