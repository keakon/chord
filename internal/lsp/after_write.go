package lsp

import (
	"context"
	"log/slog"
	"strings"
	"time"
)

const diagnosticsWaitTimeout = 3 * time.Second
const coldStartDiagnosticsWaitTimeout = 8 * time.Second

var (
	afterWriteHasReadyClient = func(m *Manager, path string) bool {
		if m == nil {
			return false
		}
		m.clientsMu.RLock()
		defer m.clientsMu.RUnlock()
		_, ok := m.clientForPathLocked(path)
		return ok
	}
	afterWriteStart = func(m *Manager, ctx context.Context, path string) {
		m.Start(ctx, path)
	}
	afterWriteWaitForClient = func(m *Manager, ctx context.Context, path string, timeout time.Duration) (*Client, bool) {
		return m.waitForClientForPath(ctx, path, timeout)
	}
	afterWriteDidChange = func(m *Manager, path string, content string) error {
		return m.DidChangeErr(path, content)
	}
	afterWriteAwaitWaiter = func(m *Manager, ctx context.Context, path string, ch chan []Diagnostic, timeout time.Duration) ([]Diagnostic, bool) {
		return m.AwaitWaiter(ctx, path, ch, timeout)
	}
)

// AfterWriteToolResult runs the LSP pipeline after Write/Edit succeeds: Start, DidChange,
// WaitDiagnosticsNotify, logs startup/sync failures, then appends LSP error diagnostics
// (if any). includeOtherFiles is true for Write. If no LSP is configured for this file
// type, LSP is not invoked and base is returned as-is.
func (m *Manager) AfterWriteToolResult(ctx context.Context, absPath, content, base string, includeOtherFiles bool) string {
	if m == nil {
		return base
	}
	absPath = normalizeWaiterPath(absPath)
	if !pathUnderDir(absPath, m.projectRoot) {
		m.logLSPServiceNote(absPath, "File is outside project root; language servers were not notified.")
		return base
	}
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

	// Register the waiter BEFORE sending didChange so we cannot miss a fast response.
	waiterCh := m.PrepareWaiter(absPath)
	if err := afterWriteDidChange(m, absPath, content); err != nil {
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
		slog.Warn("lsp: diagnostics wait timeout", "path", absPath, "timeout", waitTimeout)
	}

	m.recordReviewSnapshot(absPath)
	return m.AppendLSPDiagnosticsToToolOutput(base, absPath, includeOtherFiles)
}

func (m *Manager) logLSPServiceNote(path, msg string) {
	if msg == "" {
		return
	}
	slog.Warn("lsp: non-actionable service note suppressed", "path", path, "detail", msg)
}

func (m *Manager) anyServerMatchesPath(path string) bool {
	if m.cfg == nil || len(m.cfg.LSP) == 0 {
		return false
	}
	if !pathUnderDir(path, m.projectRoot) {
		return false
	}
	for _, srvCfg := range m.cfg.LSP {
		if srvCfg.Disabled {
			continue
		}
		if m.handles(srvCfg, path) {
			return true
		}
	}
	return false
}

func (m *Manager) startFailuresForPath(path string) []string {
	if m.cfg == nil || len(m.cfg.LSP) == 0 {
		return nil
	}
	var missing []string
	m.clientsMu.RLock()
	for name, srvCfg := range m.cfg.LSP {
		if srvCfg.Disabled || !m.handles(srvCfg, path) {
			continue
		}
		if _, ok := m.clients[name]; !ok {
			missing = append(missing, name)
		}
	}
	m.clientsMu.RUnlock()

	m.startFailMu.Lock()
	defer m.startFailMu.Unlock()
	var out []string
	for _, name := range missing {
		if msg, ok := m.startFail[name]; ok {
			out = append(out, name+": "+msg)
		} else {
			out = append(out, name+": not started")
		}
	}
	return out
}
