package lsp

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/config"
)

func (m *Manager) afterWritePythonToolResult(ctx context.Context, absPath, content, base string, includeOtherFiles bool, ranges []EditRange) string {
	pyCfg := m.cfg.Diagnostics.Python
	metrics := measureContent(content)
	availability := diagnosticBackendAvailability{
		Semantic: m.pythonSemanticBackendAvailable(absPath, pyCfg),
		Quick:    m.pythonQuickBackendAvailable(pyCfg),
	}
	selection := selectPythonDiagnosticBackend(pyCfg, metrics, availability)
	switch selection.Backend {
	case pythonDiagnosticBackendQuick:
		return m.afterWritePythonQuickResult(ctx, absPath, content, pyCfg, selection, base, ranges)
	case pythonDiagnosticBackendSemantic:
		return m.afterWriteLSPToolResult(ctx, absPath, content, base, includeOtherFiles, ranges)
	default:
		return appendPythonDiagnosticsSkipped(base, selection)
	}
}

func (m *Manager) afterWriteLSPToolResult(ctx context.Context, absPath, content, base string, includeOtherFiles bool, ranges []EditRange) string {
	// Unassociated file type: skip LSP entirely (no Start, no note).
	if !m.anyServerMatchesPath(absPath) {
		return base
	}

	coldStart := !afterWriteHasReadyClient(m, absPath)
	afterWriteStart(m, ctx, absPath)

	// Start is asynchronous, so wait briefly for the matching client to appear
	// before treating the first post-write sync as a startup failure.
	if _, ok := afterWriteWaitForClient(m, ctx, absPath, 3*time.Second); !ok {
		msgs := m.startFailuresForPath(absPath)
		if len(msgs) > 0 {
			m.logLSPServiceNote(absPath, "Language server could not start: "+strings.Join(msgs, "; "))
		} else {
			m.logLSPServiceNote(absPath, "No language server connection is available for this file.")
		}
		return base
	}

	baseline := m.currentFileDiagnostics(absPath)
	// Register the waiter BEFORE sending didChange so we cannot miss a fast response.
	waiterCh := m.PrepareWaiter(absPath)
	after := time.Now()
	serverVersions, err := afterWriteDidChange(m, ctx, absPath, content)
	if err != nil {
		m.logLSPServiceNote(absPath, "Failed to sync buffer to language server: "+err.Error())
	}

	waitTimeout := diagnosticsWaitTimeout
	if coldStart {
		waitTimeout = coldStartDiagnosticsWaitTimeout
	}

	_, notified := afterWriteAwaitWaiter(m, ctx, absPath, waiterCh, diagnosticsWaitRequest{serverVersions: serverVersions, after: after}, waitTimeout)
	if !notified && ctx.Err() == nil {
		// Keep diagnostics wait timeouts out of the tool output so the model only sees
		// actionable diagnostics; log the timeout for troubleshooting instead.
		log.Warnf("lsp: diagnostics wait timeout path=%v timeout=%v", absPath, waitTimeout)
		if isPythonPath(absPath) {
			pyCfg := m.cfg.Diagnostics.Python
			if m.pythonQuickBackendAvailable(pyCfg) {
				fallbackBase := base
				selection := pythonDiagnosticSelection{Backend: pythonDiagnosticBackendQuick, Reason: "semantic-timeout", SkipFullTypeCheck: true}
				return m.afterWritePythonQuickResult(ctx, absPath, content, pyCfg, selection, fallbackBase, ranges)
			}
		}
	}

	m.recordReviewSnapshot(absPath)
	current := m.currentFileDiagnostics(absPath)
	out := m.appendLSPDiagnosticsToToolOutput(base, absPath, includeOtherFiles, ranges, diagnosticsOutputConfig(m.cfg, absPath))
	return appendDiagnosticChangeSummary(out, baseline, current)
}

func (m *Manager) afterWritePythonQuickResult(ctx context.Context, absPath, content string, pyCfg config.PythonDiagnosticsConfig, selection pythonDiagnosticSelection, base string, ranges []EditRange) string {
	diags, err := runRuffDiagnostics(ctx, absPath, pyCfg)
	if err != nil {
		if pyCfg.LargeFile.RunSemanticWhenQuickUnavailable && selection.Reason == "large-file" {
			fallbackBase := appendRuffDiagnosticsFailure(base, selection, err) + "\nFalling back to Python semantic diagnostics."
			return m.afterWriteLSPToolResult(ctx, absPath, content, fallbackBase, false, ranges)
		}
		return appendRuffDiagnosticsFailure(base, selection, err)
	}
	return appendRuffDiagnostics(base, selection, diags, pyCfg.Output, ranges)
}

func appendPythonDiagnosticsSkipped(base string, selection pythonDiagnosticSelection) string {
	switch selection.Reason {
	case "large-file-quick-unavailable":
		return base + "\n\nDiagnostics:\nPython diagnostics skipped: large file and Ruff unavailable."
	case "no-backend":
		return base + "\n\nDiagnostics:\nPython diagnostics skipped: no configured checker available."
	default:
		return base
	}
}

func appendRuffDiagnosticsFailure(base string, selection pythonDiagnosticSelection, err error) string {
	var b strings.Builder
	b.WriteString(base)
	b.WriteString("\n\nDiagnostics:\n")
	fmt.Fprintf(&b, "Ruff diagnostics failed: %v.", err)
	if selection.Reason == "large-file" {
		b.WriteString("\nPython semantic diagnostics skipped for large file.")
	} else {
		b.WriteString("\nPython semantic diagnostics unavailable.")
	}
	return b.String()
}

func diagnosticsOutputConfig(cfg *config.Config, path string) config.DiagnosticOutputConfig {
	if cfg == nil {
		return config.DiagnosticOutputConfig{}
	}
	if isPythonPath(path) {
		return cfg.Diagnostics.Python.Output
	}
	return config.DiagnosticOutputConfig{}
}

func appendDiagnosticChangeSummary(out string, baseline, current []Diagnostic) string {
	if !strings.Contains(out, "Diagnostics:") {
		return out
	}
	baseKeys := make(map[string]struct{}, len(baseline))
	for _, d := range baseline {
		baseKeys[diagnosticComparisonKey(d)] = struct{}{}
	}
	currentKeys := make(map[string]struct{}, len(current))
	for _, d := range current {
		currentKeys[diagnosticComparisonKey(d)] = struct{}{}
	}
	newCount := 0
	for key := range currentKeys {
		if _, ok := baseKeys[key]; !ok {
			newCount++
		}
	}
	resolvedCount := 0
	for key := range baseKeys {
		if _, ok := currentKeys[key]; !ok {
			resolvedCount++
		}
	}
	if newCount == 0 && resolvedCount == 0 {
		return out
	}
	return fmt.Sprintf("%s\nDiagnostics changed: %d new, %d resolved.", out, newCount, resolvedCount)
}

func (m *Manager) currentFileDiagnostics(absPath string) []Diagnostic {
	if m == nil {
		return nil
	}
	path := normalizeWaiterPath(absPath)
	diags := m.allDiagnosticsByAbsPath()[path]
	return append([]Diagnostic(nil), diags...)
}

func diagnosticComparisonKey(d Diagnostic) string {
	return fmt.Sprintf("%d:%d:%d:%s:%s:%s", d.Severity, d.Line, d.Col, d.Code, d.Source, d.Message)
}

func appendRuffDiagnostics(base string, selection pythonDiagnosticSelection, diags []Diagnostic, output config.DiagnosticOutputConfig, ranges []EditRange) string {
	var b strings.Builder
	b.WriteString(base)
	if strings.Contains(base, "Diagnostics:") {
		b.WriteByte('\n')
	} else {
		b.WriteString("\n\nDiagnostics:\n")
	}
	if output.MaxTotalDiagnostics <= 0 || output.MaxTotalDiagnostics > ruffDiagnosticsOutputMax {
		output.MaxTotalDiagnostics = ruffDiagnosticsOutputMax
	}
	selected, omitted := selectDiagnosticsByOutput(diags, output, ranges)
	if len(selected) == 0 {
		b.WriteString("No Ruff diagnostics found.")
		return b.String()
	}
	block := formatSelectedDiagnosticsBlock("", selected, true)
	if block != "" {
		b.WriteString(block)
	}
	if omitted > 0 {
		b.WriteByte('\n')
		b.WriteString(diagnosticsOmittedLine(omitted))
	}
	return b.String()
}
