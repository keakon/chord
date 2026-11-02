package lsp

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/keakon/golog/log"
	pnprotocol "github.com/keakon/x/powernap/pkg/lsp/protocol"

	"github.com/keakon/chord/internal/config"
)

func (m *Manager) afterWritePythonToolResult(ctx context.Context, absPath, content, base string, includeOtherFiles bool, ranges []EditRange, changeType pnprotocol.FileChangeType, displayBaseDir string) FileWriteResult {
	pyCfg := m.cfg.Diagnostics.Python
	metrics := measureContent(content)
	availability := diagnosticBackendAvailability{
		Semantic: m.pythonSemanticBackendAvailable(absPath, pyCfg),
		Quick:    m.pythonQuickBackendAvailable(pyCfg),
	}
	selection := selectPythonDiagnosticBackend(pyCfg, metrics, availability)
	switch selection.Backend {
	case pythonDiagnosticBackendQuick:
		return m.afterWritePythonQuickResult(ctx, absPath, content, pyCfg, selection, base, ranges, changeType, true, displayBaseDir)
	case pythonDiagnosticBackendSemantic:
		return m.afterWriteLSPToolResult(ctx, absPath, content, base, includeOtherFiles, ranges, changeType, displayBaseDir)
	default:
		result := unavailableFileWrite(ctx, appendPythonDiagnosticsSkipped(base, selection), FileWriteStatusBackendUnavailable)
		if result.Status != FileWriteStatusCancelled {
			result.Detail = "no Python diagnostics backend available"
			if selection.Reason == "large-file-quick-unavailable" {
				result.Detail = "large file; Ruff unavailable and semantic diagnostics skipped"
			}
		}
		return result
	}
}

func (m *Manager) afterWriteLSPToolResult(ctx context.Context, absPath, content, base string, includeOtherFiles bool, ranges []EditRange, changeType pnprotocol.FileChangeType, displayBaseDir string) FileWriteResult {
	return m.afterWriteLSPToolResultWithWatchedNotification(ctx, absPath, content, base, includeOtherFiles, ranges, changeType, true, displayBaseDir)
}

func (m *Manager) afterWriteLSPToolResultWithWatchedNotification(ctx context.Context, absPath, content, base string, includeOtherFiles bool, ranges []EditRange, changeType pnprotocol.FileChangeType, notifyWatched bool, displayBaseDir string) FileWriteResult {
	// Unassociated file type: skip LSP entirely (no Start, no note).
	if !m.HasServerForPath(absPath) {
		return FileWriteResult{Output: base, Status: FileWriteStatusNoServer}
	}

	base, exited := m.appendExitedServerNotes(ctx, absPath, base)
	coldStart := !afterWriteHasReadyClient(m, absPath)
	afterWriteStart(m, ctx, absPath)

	// Start is asynchronous, so wait briefly for the matching client to appear
	// before treating the first post-write sync as a startup failure.
	_, ok := afterWriteWaitForClient(m, ctx, absPath, 3*time.Second)
	if !ok {
		return unavailableFileWrite(ctx, m.appendStartFailureNotes(ctx, absPath, base, exited), FileWriteStatusStartFailed)
	}

	baseline := m.currentFileDiagnostics(absPath)
	// Register the waiter BEFORE sending didChange so we cannot miss a fast response.
	waiterCh := m.PrepareWaiter(absPath)
	sync := m.syncAfterWrite(ctx, absPath, content, changeType, notifyWatched)
	base, wait := m.settleAfterWriteSyncFailure(ctx, absPath, waiterCh, sync, base)
	if !wait {
		return unavailableFileWrite(ctx, base, FileWriteStatusSyncFailed)
	}

	waitTimeout := diagnosticsWaitTimeout
	if coldStart {
		waitTimeout = coldStartDiagnosticsWaitTimeout
	}

	_, notified, _, _ := afterWriteAwaitWaiter(m, ctx, absPath, waiterCh, diagnosticsWaitRequest{serverVersions: sync.serverVersions, after: sync.after}, waitTimeout)
	if notified && len(sync.serverErrors) == 0 {
		m.confirmDiagnosticsSync(sync.token)
	}
	if !notified && ctx.Err() == nil {
		// A timeout is logged every time; the model hears about the first one
		// per session so an empty result is never mistaken for a clean one. The
		// quick-backend fallback below replaces the semantic result, so only the
		// no-fallback case carries the note.
		log.Warnf("lsp: diagnostics wait timeout path=%v timeout=%v", absPath, waitTimeout)
		if isPythonPath(absPath) {
			pyCfg := m.cfg.Diagnostics.Python
			if m.pythonQuickBackendAvailable(pyCfg) {
				fallbackBase := base
				selection := pythonDiagnosticSelection{Backend: pythonDiagnosticBackendQuick, Reason: "semantic-timeout", SkipFullTypeCheck: true}
				return m.afterWritePythonQuickResult(ctx, absPath, content, pyCfg, selection, fallbackBase, ranges, changeType, false, displayBaseDir)
			}
		}
		base = m.appendTimeoutNote(absPath, base, waitTimeout)
	}

	m.recordReviewSnapshot(absPath)
	result := synchronizedFileWrite("", sync)
	states := map[string]FileWriteDiagnosticsState{absPath: {FailedServers: result.FailedServers}}
	byPath := m.diagnosticsForWritePaths([]string{absPath}, states)
	out := m.appendFileDiagnosticsToToolOutput(base, absPath, includeOtherFiles, ranges, diagnosticsOutputConfig(m.cfg, absPath), displayBaseDir, byPath)
	if len(sync.serverErrors) == 0 {
		out = appendDiagnosticChangeSummary(out, baseline, byPath[absPath])
	}
	if notified {
		result.Output = out
		return result
	}
	if ctx.Err() != nil {
		return unavailableFileWrite(ctx, out, FileWriteStatusTimeout)
	}
	return timeoutFileWrite(out, waitTimeout)
}

func (m *Manager) afterWritePythonQuickResult(ctx context.Context, absPath, content string, pyCfg config.PythonDiagnosticsConfig, selection pythonDiagnosticSelection, base string, ranges []EditRange, changeType pnprotocol.FileChangeType, notifyWatched bool, displayBaseDir string) FileWriteResult {
	watchedNotified := false
	if notifyWatched && afterWriteHasReadyClient(m, absPath) {
		if errs := afterWriteNotifyWatchedFileChanged(m, ctx, absPath, changeType); len(errs) > 0 {
			m.logLSPServiceNote(absPath, "Failed to notify language server about workspace file change: "+formatServerErrors(errs))
		} else {
			watchedNotified = true
		}
	}
	diags, err := runRuffDiagnostics(ctx, absPath, pyCfg)
	if err != nil {
		if pyCfg.LargeFile.RunSemanticWhenQuickUnavailable && selection.Reason == "large-file" {
			fallbackBase := appendRuffDiagnosticsFailure(base, selection, err) + "\nFalling back to Python semantic diagnostics."
			return m.afterWriteLSPToolResultWithWatchedNotification(ctx, absPath, content, fallbackBase, false, ranges, changeType, !watchedNotified, displayBaseDir)
		}
		result := unavailableFileWrite(ctx, appendRuffDiagnosticsFailure(base, selection, err), FileWriteStatusBackendUnavailable)
		if result.Status != FileWriteStatusCancelled {
			result.Detail = "Ruff diagnostics failed: " + err.Error()
		}
		return result
	}
	return reportedFileWrite(appendRuffDiagnostics(base, diags, pyCfg.Output, ranges))
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
	if changed := diagnosticChangeSummary(baseline, current); changed != "" {
		return out + "\n" + changed
	}
	return out
}

func diagnosticChangeSummary(baseline, current []Diagnostic) string {
	counts := diagnosticChangeCounts(baseline, current)
	if counts == "" {
		return ""
	}
	return "Diagnostics changed: " + counts + "."
}

// diagnosticChangeCounts returns "N new, M resolved" relative to baseline, or
// "" when the diagnostic set is unchanged.
func diagnosticChangeCounts(baseline, current []Diagnostic) string {
	baseKeys := make(map[diagnosticIdentity]struct{}, len(baseline))
	for _, d := range baseline {
		baseKeys[diagnosticIdentityKey(d)] = struct{}{}
	}
	currentKeys := make(map[diagnosticIdentity]struct{}, len(current))
	for _, d := range current {
		currentKeys[diagnosticIdentityKey(d)] = struct{}{}
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
		return ""
	}
	return fmt.Sprintf("%d new, %d resolved", newCount, resolvedCount)
}

func (m *Manager) currentFileDiagnostics(absPath string) []Diagnostic {
	if m == nil {
		return nil
	}
	path := normalizeWaiterPath(absPath)
	diags := m.diagnosticsForPaths([]string{path})[path]
	return append([]Diagnostic(nil), diags...)
}

func appendRuffDiagnostics(base string, diags []Diagnostic, output config.DiagnosticOutputConfig, ranges []EditRange) string {
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
	if block := formatDiagnosticsBlockWithRanges(diags, output, ranges); block != "" {
		b.WriteString(block)
	} else {
		b.WriteString("No Ruff diagnostics found.")
	}
	return b.String()
}
