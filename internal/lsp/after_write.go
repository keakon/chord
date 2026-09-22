package lsp

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/keakon/golog/log"
	pnprotocol "github.com/keakon/x/powernap/pkg/lsp/protocol"

	"github.com/keakon/chord/internal/config"
)

const diagnosticsWaitTimeout = 3 * time.Second
const coldStartDiagnosticsWaitTimeout = 8 * time.Second
const diagnosticsSettleDuration = 300 * time.Millisecond

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
	afterWriteDidChange = func(m *Manager, ctx context.Context, path string, content string) (map[string]int32, error) {
		return m.DidChangeVersions(ctx, path, content)
	}
	afterWriteNotifyWatchedFileChanged = func(m *Manager, ctx context.Context, path string, changeType pnprotocol.FileChangeType) error {
		return m.NotifyWatchedFileChanged(ctx, path, changeType)
	}
	afterWriteAwaitWaiter = func(m *Manager, ctx context.Context, path string, ch chan diagnosticsEvent, req diagnosticsWaitRequest, timeout time.Duration) ([]Diagnostic, bool) {
		return m.AwaitFreshWaiter(ctx, path, ch, req, timeout)
	}
)

// AfterFileWriteToolResult runs the LSP pipeline after Write/Edit succeeds: Start, DidChange,
// WaitDiagnosticsNotify, logs startup/sync failures, then appends LSP error diagnostics
// (if any). includeOtherFiles is true for Write. If no LSP is configured for this file
// type, LSP is not invoked and base is returned as-is.
func (m *Manager) AfterFileWriteToolResult(ctx context.Context, absPath, content, base string, includeOtherFiles bool, changeType WatchedFileChangeType, displayBaseDir string) string {
	if m == nil {
		return base
	}
	absPath = normalizeWaiterPath(absPath)
	if !pathUnderDir(absPath, m.projectRootPath()) {
		m.logLSPServiceNote(absPath, "File is outside project root; language servers were not notified.")
		return base
	}
	if isPythonPath(absPath) && config.DiagnosticsEnabled(m.cfg) {
		var ranges []EditRange
		if strings.HasPrefix(base, "Replaced ") {
			ranges = EditRangesForReplacement(content, "", "", false)
		}
		return m.afterWritePythonToolResult(ctx, absPath, content, base, includeOtherFiles, ranges, changeType, displayBaseDir)
	}
	// Unassociated file type: skip LSP entirely (no Start, no note).
	if !m.HasServerForPath(absPath) {
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
	if err := afterWriteNotifyWatchedFileChanged(m, ctx, absPath, changeType); err != nil {
		m.logLSPServiceNote(absPath, "Failed to notify language server about workspace file change: "+err.Error())
	}
	after := time.Now()
	syncToken := m.beginDiagnosticsSync(absPath)
	serverVersions, err := afterWriteDidChange(m, ctx, absPath, content)
	if err != nil {
		m.logLSPServiceNote(absPath, "Failed to sync buffer to language server: "+err.Error())
	}

	waitTimeout := diagnosticsWaitTimeout
	if coldStart {
		waitTimeout = coldStartDiagnosticsWaitTimeout
	}

	_, notified := afterWriteAwaitWaiter(m, ctx, absPath, waiterCh, diagnosticsWaitRequest{serverVersions: serverVersions, after: after}, waitTimeout)
	if err == nil && notified {
		m.confirmDiagnosticsSync(syncToken)
	}
	if !notified && ctx.Err() == nil {
		// Keep diagnostics wait timeouts out of the tool output so the model only sees
		// actionable diagnostics; log the timeout for troubleshooting instead.
		log.Warnf("lsp: diagnostics wait timeout path=%v timeout=%v", absPath, waitTimeout)
	}

	m.recordReviewSnapshot(absPath)
	return m.AppendLSPDiagnosticsToToolOutput(base, absPath, includeOtherFiles, displayBaseDir)
}

func (m *Manager) logLSPServiceNote(path, msg string) {
	if msg == "" {
		return
	}
	log.Debugf("lsp: non-actionable service note suppressed path=%v detail=%v", path, msg)
}

// HasServerForPath reports whether any configured, enabled LSP server handles
// the given path. Same matching used by AfterFileWriteToolResult and the
// Python diagnostic backends.
func (m *Manager) HasServerForPath(path string) bool {
	if m.cfg == nil || len(m.cfg.LSP) == 0 {
		return false
	}
	for name, srvCfg := range m.cfg.LSP {
		if srvCfg.Disabled {
			continue
		}
		if _, ok := m.serverRootForPath(name, srvCfg, path); ok {
			return true
		}
	}
	return false
}

func (m *Manager) startFailuresForPath(path string) []string {
	if m.cfg == nil || len(m.cfg.LSP) == 0 {
		return nil
	}
	// serverRootForPath walks the ancestor chain stat'ing root markers; resolve
	// all (name, root) candidates before taking the clients lock so no
	// filesystem work happens under it.
	matches := make([]clientKey, 0, len(m.cfg.LSP))
	for name, srvCfg := range m.cfg.LSP {
		if srvCfg.Disabled {
			continue
		}
		if root, ok := m.serverRootForPath(name, srvCfg, path); ok {
			matches = append(matches, clientKey{name: name, root: root})
		}
	}
	var missing []clientKey
	m.clientsMu.RLock()
	for _, key := range matches {
		if _, ok := m.clients[key]; !ok {
			missing = append(missing, key)
		}
	}
	m.clientsMu.RUnlock()
	// Root is part of the ordering so two instances of the same server report in
	// a stable order instead of whatever the map iteration produced.
	sort.Slice(missing, func(i, j int) bool {
		if missing[i].name != missing[j].name {
			return missing[i].name < missing[j].name
		}
		return missing[i].root < missing[j].root
	})

	m.startFailMu.Lock()
	defer m.startFailMu.Unlock()
	out := make([]string, 0, len(missing))
	for _, key := range missing {
		msg, ok := m.startFail[key]
		if !ok {
			msg = "not started"
		}
		line := key.name + ": " + msg
		// Two roots of the same server usually fail identically (missing
		// binary); reporting the same sentence twice tells the model nothing.
		if len(out) > 0 && out[len(out)-1] == line {
			continue
		}
		out = append(out, line)
	}
	return out
}
