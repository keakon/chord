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
	afterWriteDidChange = func(m *Manager, ctx context.Context, path string, content string) (map[string]int32, map[string]error) {
		return m.DidChangeVersions(ctx, path, content)
	}
	afterWriteDidSave = func(m *Manager, ctx context.Context, path string, content string) map[string]error {
		return m.NotifyDidSave(ctx, path, content)
	}
	afterWriteNotifyWatchedFileChanged = func(m *Manager, ctx context.Context, path string, changeType pnprotocol.FileChangeType) map[string]error {
		return m.NotifyWatchedFileChanged(ctx, path, changeType)
	}
	afterWriteAwaitWaiter = func(m *Manager, ctx context.Context, path string, ch chan diagnosticsEvent, req diagnosticsWaitRequest, timeout time.Duration) ([]Diagnostic, bool) {
		return m.AwaitFreshWaiter(ctx, path, ch, req, timeout)
	}
)

// afterWriteClientWaitTimeout bounds how long one write waits for its
// language server client to appear after Start. Batch callers run these waits
// concurrently, so the budget applies per file rather than per batch.
const afterWriteClientWaitTimeout = 3 * time.Second

// afterWritePrepared carries one write through the two phases of the
// after-write pipeline: preparation (client wait, waiter registration, content
// sync) and the diagnostics wait that follows.
type afterWritePrepared struct {
	path        string
	base        string
	sync        afterWriteSync
	waiter      chan diagnosticsEvent
	waitTimeout time.Duration
	// wait reports whether the diagnostics wait should run: false when the
	// write has no server coverage, its server start failed, or every owning
	// server missed the content sync.
	wait bool
}

// beginAfterFileWrite runs the first step of the after-write pipeline: drop
// exited clients so Start relaunches them, record whether this write needs a
// cold start, and launch the owning servers. Batch callers run it for every
// file before any client wait, so all servers start (in parallel) and every
// cold file is recognized before a sibling's wait makes the server look warm.
func (m *Manager) beginAfterFileWrite(ctx context.Context, absPath, base string) (coldStart bool, baseOut string, exited []string) {
	baseOut, exited = m.appendExitedServerNotes(ctx, absPath, base)
	coldStart = !afterWriteHasReadyClient(m, absPath)
	afterWriteStart(m, ctx, absPath)
	return coldStart, baseOut, exited
}

// prepareAfterFileWrite runs the pre-wait half of the single-file after-write
// pipeline: client wait, waiter registration, content sync, and sync-failure
// handling. coldStart and exited come from beginAfterFileWrite, which the
// caller runs first.
func (m *Manager) prepareAfterFileWrite(ctx context.Context, absPath, content, base string, changeType WatchedFileChangeType, coldStart bool, exited []string) afterWritePrepared {
	// Start is asynchronous, so wait briefly for the matching client to appear
	// before treating the first post-write sync as a startup failure.
	if _, ok := afterWriteWaitForClient(m, ctx, absPath, afterWriteClientWaitTimeout); !ok {
		return afterWritePrepared{path: absPath, base: m.appendStartFailureNotes(ctx, absPath, base, exited)}
	}
	return m.prepareReadyAfterFileWrite(ctx, absPath, content, base, changeType, coldStart)
}

// prepareReadyAfterFileWrite runs the pre-wait half after the caller has
// confirmed a client is available. It registers the waiter BEFORE sending
// didChange so a fast server's publish is never missed; batch callers run the
// client waits concurrently and call this serially so all content syncs reach
// the server as one analysis unit.
func (m *Manager) prepareReadyAfterFileWrite(ctx context.Context, absPath, content, base string, changeType WatchedFileChangeType, coldStart bool) afterWritePrepared {
	prepared := afterWritePrepared{path: absPath, base: base}
	prepared.waiter = m.PrepareWaiter(absPath)
	prepared.sync = m.syncAfterWrite(ctx, absPath, content, changeType, true)
	prepared.base, prepared.wait = m.settleAfterWriteSyncFailure(ctx, absPath, prepared.waiter, prepared.sync, prepared.base)
	prepared.waitTimeout = diagnosticsWaitTimeout
	if coldStart {
		prepared.waitTimeout = coldStartDiagnosticsWaitTimeout
	}
	return prepared
}

// awaitPreparedAfterFileWrite runs the wait half of the after-write pipeline
// for a prepared write: fresh diagnostics, sync confirmation, timeout notes,
// and the review snapshot. It returns the tool-result string with this file's
// diagnostics appended after base.
func (m *Manager) awaitPreparedAfterFileWrite(ctx context.Context, prepared *afterWritePrepared, includeOtherFiles bool, displayBaseDir string) string {
	if !prepared.wait {
		return prepared.base
	}

	_, notified := afterWriteAwaitWaiter(m, ctx, prepared.path, prepared.waiter, diagnosticsWaitRequest{serverVersions: prepared.sync.serverVersions, after: prepared.sync.after}, prepared.waitTimeout)
	if notified {
		m.confirmDiagnosticsSync(prepared.sync.token)
	}
	if !notified && ctx.Err() == nil {
		// A timeout is logged every time; the model hears about the first one
		// per session so an empty result is never mistaken for a clean one.
		log.Warnf("lsp: diagnostics wait timeout path=%v timeout=%v", prepared.path, prepared.waitTimeout)
		prepared.base = m.appendTimeoutNote(prepared.path, prepared.base, prepared.waitTimeout)
	}

	m.recordReviewSnapshot(prepared.path)
	return m.AppendLSPDiagnosticsToToolOutput(prepared.base, prepared.path, includeOtherFiles, displayBaseDir)
}

// AfterFileWriteToolResult runs the LSP pipeline after Write/Edit succeeds: Start, DidChange,
// WaitDiagnosticsNotify, logs startup/sync failures, then appends LSP error diagnostics
// (if any). includeOtherFiles is true for Write. If no LSP is configured for this file
// type, LSP is not invoked and base is returned as-is.
func (m *Manager) AfterFileWriteToolResult(ctx context.Context, absPath, content, base string, includeOtherFiles bool, changeType WatchedFileChangeType, displayBaseDir string) string {
	if m == nil {
		return base
	}
	absPath = normalizeWaiterPath(absPath)
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

	coldStart, base, exited := m.beginAfterFileWrite(ctx, absPath, base)
	prepared := m.prepareAfterFileWrite(ctx, absPath, content, base, changeType, coldStart, exited)
	return m.awaitPreparedAfterFileWrite(ctx, &prepared, includeOtherFiles, displayBaseDir)
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
	lspDegradationSync    = "sync"
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
			base = appendLSPDegradationNote(base, m.noteLSPDegradationForPath(absPath, lspDegradationStart, "", ""))
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
		base = appendLSPDegradationNote(base, m.noteLSPDegradationForPath(absPath, lspDegradationStart, info.key.name, info.msg))
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
			base = appendLSPDegradationNote(base, m.noteLSPDegradationForPath(absPath, lspDegradationExited, name, "server exited; restarting"))
		}
	}
	return base, exited
}

// appendTimeoutNote reports a diagnostics wait that ran out. The wait covers
// every server that owns the file, so the line names none of them and is
// reported once per workspace per session.
func (m *Manager) appendTimeoutNote(path, base string, waitTimeout time.Duration) string {
	return appendLSPDegradationNote(base, m.noteLSPDegradationForPath(path, lspDegradationTimeout, "", fmt.Sprintf("no diagnostics within %s", waitTimeout)))
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

// noteLSPDegradationForPath returns the model-facing line for a language-server
// failure, or "" when this (kind, server, root) was already reported in the
// current session. Repeat failures stay in the log: a model that kept working
// after the first honest line does not need the same sentence on every edit.
func (m *Manager) noteLSPDegradationForPath(path, kind, server, detail string) string {
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
	root := ""
	if path != "" {
		root = m.workspaceBoundary(path)
		if m.cfg != nil {
			for name, cfg := range m.cfg.LSP {
				if cfg.Disabled || (server != "language server" && server != name) {
					continue
				}
				if found, covered := m.serverRootForPath(name, cfg, path); covered && (server != "language server" || root == "") {
					root = found
				}
			}
		}
	}
	key := kind + "\x00" + server + "\x00" + root
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
