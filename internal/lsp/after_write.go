package lsp

import (
	"context"
	"fmt"
	"slices"
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
	afterWriteDidSave = func(m *Manager, ctx context.Context, path string, content string) error {
		return m.NotifyDidSave(ctx, path, content)
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

	base, exited := m.appendExitedServerNotes(ctx, absPath, base)
	coldStart := !afterWriteHasReadyClient(m, absPath)
	afterWriteStart(m, ctx, absPath)

	// Start is asynchronous, so wait briefly for the matching client to appear
	// before treating the first post-write sync as a startup failure.
	_, ok := afterWriteWaitForClient(m, ctx, absPath, 3*time.Second)
	if !ok {
		return m.appendStartFailureNotes(ctx, absPath, base, exited)
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
	if err := afterWriteDidSave(m, ctx, absPath, content); err != nil {
		m.logLSPServiceNote(absPath, "Failed to notify language server about the saved file: "+err.Error())
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
		// A timeout is logged every time; the model hears about the first one
		// per session so an empty result is never mistaken for a clean one.
		log.Warnf("lsp: diagnostics wait timeout path=%v timeout=%v", absPath, waitTimeout)
		base = m.appendTimeoutNote(base, waitTimeout)
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

// LSP degradation kinds that get one model-facing line per server per session.
const (
	lspDegradationStart   = "start"
	lspDegradationExited  = "exited"
	lspDegradationTimeout = "timeout"
)

// lspDegradationNotePrefix opens every model-facing degradation line, so
// callers that assemble their own result from the after-write output (see
// DegradationNotes) can find the lines again.
const lspDegradationNotePrefix = "LSP diagnostics unavailable for this edit ("

// appendStartFailureNotes reports why no client could take the write. A server
// whose launch is still in flight is named on every edit until it is up
// instead of being counted as reported: it is about to serve the file, and
// spending the one start-failure line on it would hide a real failure later.
// Servers in exited were just reported as dead and restarting by this call.
func (m *Manager) appendStartFailureNotes(ctx context.Context, absPath, base string, exited []string) string {
	infos := m.startFailureInfosForPath(absPath)
	if len(infos) == 0 {
		m.logLSPServiceNote(absPath, "No language server connection is available for this file.")
		if ctx.Err() == nil {
			base = appendLSPDegradationNote(base, m.noteLSPDegradation(lspDegradationStart, "", ""))
		}
		return base
	}
	m.logLSPServiceNote(absPath, "Language server could not start: "+strings.Join(formatStartFailures(infos), "; "))
	if ctx.Err() != nil {
		return base
	}
	for _, info := range infos {
		if slices.Contains(exited, info.key.name) {
			continue
		}
		if info.starting {
			base = appendLSPDegradationNote(base, formatLSPDegradationNote(info.key.name, info.msg))
			continue
		}
		base = appendLSPDegradationNote(base, m.noteLSPDegradation(lspDegradationStart, info.key.name, info.msg))
	}
	return base
}

// appendExitedServerNotes drops the clients for absPath whose server process
// has died since it started, so the following Start relaunches them, and
// reports each dead server once per session. It returns the dead servers'
// names.
func (m *Manager) appendExitedServerNotes(ctx context.Context, absPath, base string) (string, []string) {
	exited := m.pruneExitedClientsForPath(ctx, absPath)
	for _, name := range exited {
		log.Warnf("lsp: server exited, restarting name=%v path=%v", name, absPath)
		if ctx.Err() == nil {
			base = appendLSPDegradationNote(base, m.noteLSPDegradation(lspDegradationExited, name, "server exited; restarting"))
		}
	}
	return base, exited
}

// appendTimeoutNote reports a diagnostics wait that ran out. The wait covers
// every server that owns the file, so the line names none of them and is
// reported once per session regardless of which servers stayed silent.
func (m *Manager) appendTimeoutNote(base string, waitTimeout time.Duration) string {
	return appendLSPDegradationNote(base, m.noteLSPDegradation(lspDegradationTimeout, "", fmt.Sprintf("no diagnostics within %s", waitTimeout)))
}

// DegradationNotes returns the degradation lines AfterFileWriteToolResult
// appended to out, for callers that keep only parts of that output.
func DegradationNotes(out string) []string {
	var notes []string
	for line := range strings.SplitSeq(out, "\n") {
		if strings.HasPrefix(line, lspDegradationNotePrefix) {
			notes = append(notes, line)
		}
	}
	return notes
}

// noteLSPDegradation returns the model-facing line for a language-server
// failure, or "" when this (kind, server) pair was already reported in the
// current session. Repeat failures stay in the log: a model that kept working
// after the first honest line does not need the same sentence on every edit.
func (m *Manager) noteLSPDegradation(kind, server, detail string) string {
	if m == nil {
		return ""
	}
	server = strings.TrimSpace(server)
	if server == "" {
		server = "language server"
	}
	detail = strings.TrimSpace(detail)
	if len(detail) > 160 {
		detail = truncateBytesAtRune(detail, 157, "...")
	}

	m.degradeMu.Lock()
	if m.degradeNotes == nil {
		m.degradeNotes = make(map[string]struct{})
	}
	key := kind + "\x00" + server
	if _, reported := m.degradeNotes[key]; reported {
		m.degradeMu.Unlock()
		return ""
	}
	m.degradeNotes[key] = struct{}{}
	m.degradeMu.Unlock()

	return formatLSPDegradationNote(server, detail)
}

func formatLSPDegradationNote(server, detail string) string {
	if detail == "" {
		return lspDegradationNotePrefix + server + "); do not treat this edit as verified."
	}
	return lspDegradationNotePrefix + server + ": " + detail + "); do not treat this edit as verified."
}

func appendLSPDegradationNote(base, note string) string {
	if note == "" {
		return base
	}
	return base + "\n\n" + note
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

type startFailureInfo struct {
	key clientKey
	msg string
	// starting marks a server whose launch is still in flight, not a failure.
	starting bool
}

// startFailureInfosForPath lists the servers that cover path but have no live
// client and no recorded successful start. Root is part of the ordering so two
// instances of the same server report in a stable order instead of whatever
// the map iteration produced.
func (m *Manager) startFailureInfosForPath(path string) []startFailureInfo {
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
	starting := make(map[clientKey]bool)
	m.clientsMu.RLock()
	for _, key := range matches {
		if _, ok := m.clients[key]; !ok {
			missing = append(missing, key)
			if m.starting[key] {
				starting[key] = true
			}
		}
	}
	m.clientsMu.RUnlock()
	sort.Slice(missing, func(i, j int) bool {
		if missing[i].name != missing[j].name {
			return missing[i].name < missing[j].name
		}
		return missing[i].root < missing[j].root
	})

	m.startFailMu.Lock()
	defer m.startFailMu.Unlock()
	out := make([]startFailureInfo, 0, len(missing))
	for _, key := range missing {
		msg, ok := m.startFail[key]
		isStarting := false
		if !ok {
			msg = "not started"
			if starting[key] {
				msg, isStarting = "still starting", true
			}
		}
		// Two roots of the same server usually fail identically (missing
		// binary); reporting the same sentence twice tells the model nothing.
		if len(out) > 0 && out[len(out)-1].key.name == key.name && out[len(out)-1].msg == msg {
			continue
		}
		out = append(out, startFailureInfo{key: key, msg: msg, starting: isStarting})
	}
	return out
}

func formatStartFailures(infos []startFailureInfo) []string {
	if len(infos) == 0 {
		return nil
	}
	out := make([]string, 0, len(infos))
	for _, info := range infos {
		out = append(out, info.key.name+": "+info.msg)
	}
	return out
}
