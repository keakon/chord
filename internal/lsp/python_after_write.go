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
		return m.afterWritePythonQuickResult(ctx, absPath, content, pyCfg, metrics, selection, base, ranges)
	case pythonDiagnosticBackendSemantic:
		return m.afterWriteLSPToolResult(ctx, absPath, content, base, includeOtherFiles, ranges)
	default:
		return appendPythonDiagnosticsSkipped(base, metrics, selection)
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
	if err := afterWriteDidChange(m, ctx, absPath, content); err != nil {
		m.logLSPServiceNote(absPath, "Failed to sync buffer to language server: "+err.Error())
	}

	waitTimeout := diagnosticsWaitTimeout
	if coldStart {
		waitTimeout = coldStartDiagnosticsWaitTimeout
	}

	_, notified := afterWriteAwaitWaiter(m, ctx, absPath, waiterCh, waitTimeout)
	if !notified && ctx.Err() == nil {
		// Keep diagnostics wait timeouts out of the tool output so the model only sees
		// actionable diagnostics; log the timeout for troubleshooting instead.
		log.Warnf("lsp: diagnostics wait timeout path=%v timeout=%v", absPath, waitTimeout)
		if isPythonPath(absPath) {
			pyCfg := m.cfg.Diagnostics.Python
			if m.pythonQuickBackendAvailable(pyCfg) {
				fallbackBase := base + "\n\nDiagnostics:\nPython semantic diagnostics did not complete successfully; showing Ruff quick diagnostics instead."
				selection := pythonDiagnosticSelection{Backend: pythonDiagnosticBackendQuick, Reason: "semantic-timeout", SkipFullTypeCheck: true}
				return m.afterWritePythonQuickResult(ctx, absPath, content, pyCfg, measureContent(content), selection, fallbackBase, ranges)
			}
		}
	}

	m.recordReviewSnapshot(absPath)
	current := m.currentFileDiagnostics(absPath)
	out := m.appendLSPDiagnosticsToToolOutput(base, absPath, includeOtherFiles, notified, ranges, diagnosticsOutputConfig(m.cfg, absPath))
	return appendDiagnosticComparisonStatus(out, baseline, current, "LSP")
}

func (m *Manager) afterWritePythonQuickResult(ctx context.Context, absPath, content string, pyCfg config.PythonDiagnosticsConfig, metrics fileMetrics, selection pythonDiagnosticSelection, base string, ranges []EditRange) string {
	diags, err := runRuffDiagnostics(ctx, absPath, pyCfg)
	if err != nil {
		if pyCfg.LargeFile.RunSemanticWhenQuickUnavailable && selection.Reason == "large-file" {
			fallbackBase := appendRuffDiagnosticsFailure(base, metrics, selection, err) + "\nFalling back to Python semantic diagnostics because run_semantic_when_quick_unavailable=true."
			return m.afterWriteLSPToolResult(ctx, absPath, content, fallbackBase, false, ranges)
		}
		return appendRuffDiagnosticsFailure(base, metrics, selection, err)
	}
	return appendRuffDiagnostics(base, metrics, selection, diags, pyCfg.Output, ranges)
}

func appendPythonDiagnosticsSkipped(base string, metrics fileMetrics, selection pythonDiagnosticSelection) string {
	switch selection.Reason {
	case "large-file-quick-unavailable":
		return fmt.Sprintf("%s\n\nDiagnostics:\nPython diagnostics skipped: this file exceeds the configured large-file threshold (lines=%d, bytes=%d) and Ruff quick diagnostics are unavailable.\nFull Python semantic diagnostics were not run to avoid blocking Edit/Write. Set run_semantic_when_quick_unavailable=true to force semantic diagnostics on large files.", base, metrics.Lines, metrics.Bytes)
	case "no-backend":
		return base + "\n\nDiagnostics:\nPython diagnostics skipped: no configured checker available."
	default:
		return base
	}
}

func appendRuffDiagnosticsFailure(base string, metrics fileMetrics, selection pythonDiagnosticSelection, err error) string {
	var b strings.Builder
	b.WriteString(base)
	b.WriteString("\n\nDiagnostics:\n")
	fmt.Fprintf(&b, "Ruff quick diagnostics failed: %v.\n", err)
	if selection.Reason == "large-file" {
		fmt.Fprintf(&b, "Full Python semantic diagnostics were skipped because this file exceeds the configured large-file threshold (lines=%d, bytes=%d).\n", metrics.Lines, metrics.Bytes)
		b.WriteString("Fix Ruff, or set run_semantic_when_quick_unavailable=true to force semantic diagnostics on large files.")
	} else {
		b.WriteString("Full Python semantic diagnostics are unavailable.")
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

func diagnosticErrorWarningCounts(diags []Diagnostic) (errors, warnings int) {
	for _, d := range diags {
		switch d.Severity {
		case 1:
			errors++
		case 2:
			warnings++
		}
	}
	return errors, warnings
}

func (m *Manager) currentFileDiagnostics(absPath string) []Diagnostic {
	if m == nil {
		return nil
	}
	path := normalizeWaiterPath(absPath)
	diags := m.allDiagnosticsByAbsPath()[path]
	return append([]Diagnostic(nil), diags...)
}

func appendDiagnosticComparisonStatus(out string, baseline, current []Diagnostic, backend string) string {
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
	errs, warns := diagnosticErrorWarningCounts(current)
	return fmt.Sprintf("%s\nDiagnostics status: backend=%s, new=%d, resolved=%d, current=%d errors, %d warnings (best effort).", out, backend, newCount, resolvedCount, errs, warns)
}

func diagnosticComparisonKey(d Diagnostic) string {
	return fmt.Sprintf("%d:%d:%d:%s:%s:%s", d.Severity, d.Line, d.Col, d.Code, d.Source, d.Message)
}

func appendRuffDiagnostics(base string, metrics fileMetrics, selection pythonDiagnosticSelection, diags []Diagnostic, output config.DiagnosticOutputConfig, ranges []EditRange) string {
	var b strings.Builder
	b.WriteString(base)
	if strings.Contains(base, "Diagnostics:") {
		b.WriteByte('\n')
	} else {
		b.WriteString("\n\nDiagnostics:\n")
	}
	if selection.Reason == "large-file" {
		fmt.Fprintf(&b, "Used Ruff quick diagnostics because this Python file exceeds the configured threshold (lines=%d, bytes=%d).\n", metrics.Lines, metrics.Bytes)
	} else if selection.Reason == "semantic-timeout" {
		b.WriteString("Used Ruff quick diagnostics because Python semantic diagnostics did not complete successfully.\n")
	} else {
		b.WriteString("Used Ruff quick diagnostics because Python semantic diagnostics are unavailable.\n")
	}
	if len(diags) == 0 {
		b.WriteString("No Ruff diagnostics found.\n")
		b.WriteString("Diagnostics status: current: 0 errors, 0 warnings.\n")
	} else {
		block := strings.TrimPrefix(formatDiagnosticsBlockWithRanges("", diags, output, ranges, true), "\n\n")
		if block != "" {
			b.WriteString(block)
			b.WriteByte('\n')
		}
		errs, warns := diagnosticErrorWarningCounts(diags)
		fmt.Fprintf(&b, "Diagnostics status: current Ruff diagnostics: %d errors, %d warnings.\n", errs, warns)
	}
	b.WriteString("Full Python semantic diagnostics were skipped.")
	return b.String()
}
