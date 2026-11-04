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
	"github.com/keakon/chord/internal/identity"
)

// diagnosticsWaitTimeout is the warm-start diagnostics wait: the configured
// default when diagnostics.lsp.wait_timeout_ms is unset (see
// configuredDiagnosticsWait).
const diagnosticsWaitTimeout = 3 * time.Second

// coldStartWriteBudget is the total wall-clock budget one cold-start write
// spends on everything after the write itself: the client wait, the content
// sync, and the diagnostics wait. It is an experiment parameter — the initial
// value exists to keep cold starts from blocking for the full first workspace
// analysis, not to maximize the chance the wait catches that analysis;
// whatever the wait misses, the late watch delivers with the next request.
const coldStartWriteBudget = 2500 * time.Millisecond

// coldStartDiagnosticsWaitTimeout is the Python semantic backend's cold-start
// diagnostics wait. It is a wait budget only, decoupled from the client wait,
// because a timeout there falls back to the Ruff quick backend instead of a
// late watch.
const coldStartDiagnosticsWaitTimeout = 8 * time.Second

// warmupGrace is how long after a client's admission a write to it still gets
// the cold-start total budget. Client readiness means the handshake finished,
// not that the first workspace load and analysis pass have; a write landing
// inside this window would otherwise draw the warm budget while the server is
// still doing the work that makes it warm.
const warmupGrace = 8 * time.Second

const diagnosticsSettleDuration = 300 * time.Millisecond

// configuredDiagnosticsWait returns the warm-start diagnostics wait: the
// configured value when valid, the built-in default otherwise. The loader
// already resets invalid values to unset; the check here keeps a hand-built
// config from turning the wait off by accident.
func configuredDiagnosticsWait(cfg *config.Config) time.Duration {
	if cfg == nil || cfg.Diagnostics.LSP.WaitTimeoutMS == nil {
		return diagnosticsWaitTimeout
	}
	if ms := *cfg.Diagnostics.LSP.WaitTimeoutMS; ms > 0 {
		return time.Duration(ms) * time.Millisecond
	}
	return diagnosticsWaitTimeout
}

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
	afterWriteDidChange = func(m *Manager, ctx context.Context, path string, content string) (map[string]int32, map[clientKey]uint64, map[string]error) {
		return m.didChangeVersions(ctx, path, content)
	}
	afterWriteDidSave = func(m *Manager, ctx context.Context, path string, content string) map[string]error {
		return m.NotifyDidSave(ctx, path, content)
	}
	afterWriteNotifyWatchedFileChanged = func(m *Manager, ctx context.Context, path string, changeType pnprotocol.FileChangeType) map[string]error {
		return m.NotifyWatchedFileChanged(ctx, path, changeType)
	}
	afterWriteAwaitWaiter = func(m *Manager, ctx context.Context, path string, ch chan diagnosticsEvent, req diagnosticsWaitRequest, timeout time.Duration) ([]Diagnostic, bool, uint64, map[diagnosticIdentity]struct{}) {
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
	path          string
	base          string
	sync          afterWriteSync
	waiter        chan diagnosticsEvent
	waitTimeout   time.Duration
	writerAgentID string
	// began is when the write's after-write pipeline started: the write's own
	// pipeline for single-file results, the batch start for batch results. The
	// cold-start total budget counts from it.
	began time.Time
	// watchGenerations is what the late watch registers with: sync.generations
	// for single-file writes, refreshed by the batch after its last sync.
	watchGenerations map[clientKey]uint64
	// coldStart and grace record which budget the wait used and why: coldStart
	// comes from beginAfterFileWrite, grace marks a warm client admitted
	// within warmupGrace that still draws the cold budget.
	coldStart bool
	grace     bool
	// clientWait and syncTime decompose the pipeline for the breakdown log.
	clientWait time.Duration
	syncTime   time.Duration
	// batchSize is set by the batch pipeline (0 for single-file writes).
	batchSize int
	afterSeq  uint64
	// wait reports whether the diagnostics wait should run: false when the
	// write has no server coverage, its server start failed, or every owning
	// server missed the content sync.
	wait bool
	// skipStatus records why wait is false. The zero value is unused when the
	// wait runs.
	skipStatus FileWriteStatus
}

// beginAfterFileWrite runs the first step of the after-write pipeline: drop
// exited clients so Start relaunches them, record whether this write needs a
// cold start, and launch the owning servers. The returned began stamps the
// start of this write's pipeline: the cold-start total budget counts from it.
// Batch callers run it for every file before any client wait, so all servers
// start (in parallel) and every cold file is recognized before a sibling's
// wait makes the server look warm.
func (m *Manager) beginAfterFileWrite(ctx context.Context, absPath, base string) (coldStart bool, baseOut string, exited []string, began time.Time) {
	began = time.Now()
	baseOut, exited = m.appendExitedServerNotes(ctx, absPath, base)
	coldStart = !afterWriteHasReadyClient(m, absPath)
	afterWriteStart(m, ctx, absPath)
	return coldStart, baseOut, exited, began
}

// prepareAfterFileWrite runs the pre-wait half of the single-file after-write
// pipeline: client wait, waiter registration, content sync, and sync-failure
// handling. coldStart and exited come from beginAfterFileWrite, which the
// caller runs first. A write whose client never appears within the budget
// registers a pending re-sync, so its diagnostics follow with the next
// request once the server is up instead of never.
func (m *Manager) prepareAfterFileWrite(ctx context.Context, absPath, content, base string, changeType WatchedFileChangeType, coldStart bool, exited []string, began time.Time) afterWritePrepared {
	// Start is asynchronous, so wait briefly for the matching client to appear
	// before treating the first post-write sync as a startup failure. A cold
	// start draws from the write's total budget, not its own timeout.
	clientWait := afterWriteClientWaitTimeout
	if coldStart {
		if remaining := coldStartWriteBudget - time.Since(began); remaining < clientWait {
			clientWait = max(remaining, 0)
		}
	}
	waitStart := time.Now()
	_, ok := afterWriteWaitForClient(m, ctx, absPath, clientWait)
	clientWaitSpent := time.Since(waitStart)
	if !ok {
		if ctx.Err() == nil {
			m.registerPendingSync(absPath, content, changeType, identity.AgentIDFromContext(ctx))
		}
		return afterWritePrepared{
			path:       absPath,
			base:       m.appendStartFailureNotes(ctx, absPath, base, exited),
			began:      began,
			clientWait: clientWaitSpent,
			coldStart:  coldStart,
			grace:      !coldStart && m.clientAdmittedWithin(absPath, warmupGrace),
			skipStatus: unavailableStatus(ctx, FileWriteStatusStartFailed),
		}
	}
	prepared := m.prepareReadyAfterFileWrite(ctx, absPath, content, base, changeType, coldStart, began)
	prepared.clientWait = clientWaitSpent
	return prepared
}

// prepareReadyAfterFileWrite runs the pre-wait half after the caller has
// confirmed a client is available. It registers the waiter BEFORE sending
// didChange so a fast server's publish is never missed; batch callers run the
// client waits concurrently and call this serially so all content syncs reach
// the server as one analysis unit.
func (m *Manager) prepareReadyAfterFileWrite(ctx context.Context, absPath, content, base string, changeType WatchedFileChangeType, coldStart bool, began time.Time) afterWritePrepared {
	if ctx.Err() == nil && afterWriteHasReadyClient(m, absPath) && m.hasStartingOwner(absPath) {
		m.registerPendingSync(absPath, content, changeType, identity.AgentIDFromContext(ctx))
	}
	prepared := afterWritePrepared{path: absPath, base: base, writerAgentID: identity.AgentIDFromContext(ctx), began: began, coldStart: coldStart}
	prepared.waiter = m.PrepareWaiter(absPath)
	prepared.afterSeq = m.currentLatePublishSeq()
	syncStart := time.Now()
	prepared.sync = m.syncAfterWrite(ctx, absPath, content, changeType, true)
	prepared.syncTime = time.Since(syncStart)
	prepared.watchGenerations = prepared.sync.generations
	prepared.base, prepared.wait = m.settleAfterWriteSyncFailure(ctx, absPath, prepared.waiter, prepared.sync, prepared.base)
	if !prepared.wait {
		prepared.skipStatus = unavailableStatus(ctx, FileWriteStatusSyncFailed)
	}
	// A client admitted within warmupGrace draws the cold total budget even
	// though the write itself is warm: the handshake may be done, but the
	// first workspace load and analysis pass it will publish against can
	// still be running.
	prepared.waitTimeout = configuredDiagnosticsWait(m.cfg)
	prepared.grace = !coldStart && m.clientAdmittedWithin(absPath, warmupGrace)
	if coldStart || prepared.grace {
		if remaining := coldStartWriteBudget - time.Since(began); remaining < prepared.waitTimeout {
			prepared.waitTimeout = max(remaining, 0)
		}
	}
	return prepared
}

// awaitPreparedAfterFileWrite runs the wait half of the after-write pipeline
// for a prepared write: fresh diagnostics, sync confirmation, timeout
// reporting in the result's diagnostics section, and the review snapshot. It
// returns the tool-result string with this file's diagnostics appended after
// base, plus whether those diagnostics describe this write. A wait that times
// out still registers the late-diagnostics watch, so the analysis the server
// finishes later is claimed by the next request instead of being lost.
func (m *Manager) awaitPreparedAfterFileWrite(ctx context.Context, prepared *afterWritePrepared, includeOtherFiles bool, displayBaseDir string) FileWriteResult {
	if !prepared.wait {
		m.logAfterWriteBreakdown(prepared, prepared.skipStatus, false, false, 0)
		return FileWriteResult{
			Output: prepared.base,
			Status: prepared.skipStatus,
			Detail: fileWriteStatusDetail(prepared.skipStatus),
		}
	}

	if prepared.coldStart || prepared.grace {
		prepared.waitTimeout = min(prepared.waitTimeout, max(coldStartWriteBudget-time.Since(prepared.began), 0))
	}
	req := diagnosticsWaitRequest{serverVersions: prepared.sync.serverVersions, after: prepared.sync.after, immediateServers: m.immediateDiagnosticsServers(prepared.sync.serverVersions), unversionedServers: m.unversionedDiagnosticsServers(prepared.sync.serverVersions)}
	diagWaitStart := time.Now()
	_, notified, lastSeq, baseline := afterWriteAwaitWaiter(m, ctx, prepared.path, prepared.waiter, req, prepared.waitTimeout)
	diagWait := time.Since(diagWaitStart)
	if notified && prepared.sync.token.confirmable {
		m.confirmDiagnosticsSync(prepared.sync.token)
	}
	lateWatch := false
	if ctx.Err() == nil && prepared.sync.token.confirmable {
		lateWatch = m.watchLateDiagnostics(prepared.path, prepared.sync.serverVersions, prepared.watchGenerations, baseline, lastSeq, prepared.writerAgentID, prepared.sync.token)
	}
	if !notified && ctx.Err() == nil {
		// A timeout is logged every time, and the result's diagnostics section
		// reports it every time, so an unverified write is never mistaken for
		// a clean one.
		log.Warnf("lsp: diagnostics wait timeout path=%v timeout=%v", prepared.path, prepared.waitTimeout)
	}
	status := unavailableStatus(ctx, FileWriteStatusTimeout)
	if notified {
		status = synchronizedFileWrite("", prepared.sync).Status
	}
	m.logAfterWriteBreakdown(prepared, status, notified, lateWatch, diagWait)

	if notified {
		m.recordReviewSnapshot(prepared.path)
	}
	result := synchronizedFileWrite("", prepared.sync)
	states := map[string]FileWriteDiagnosticsState{prepared.path: {FailedServers: result.FailedServers}}
	if !notified && ctx.Err() == nil {
		// The wait ran out, so nothing here verified this write. The section
		// says so itself — naming the reason and the late watch that still
		// claims the analysis — instead of an empty block reading as clean.
		states[prepared.path] = FileWriteDiagnosticsState{
			Status:        FileWriteStatusTimeout,
			Detail:        deferredDiagnosticsDetail(prepared.waitTimeout),
			FailedServers: result.FailedServers,
		}
	}
	out := m.AppendLSPDiagnosticsToToolOutputForPaths(prepared.base, []string{prepared.path}, includeOtherFiles, nil, nil, nil, states, displayBaseDir)
	result.Output = out
	if notified {
		return result
	}
	if ctx.Err() != nil {
		return unavailableFileWrite(ctx, out, FileWriteStatusTimeout)
	}
	return timeoutFileWrite(out, prepared.waitTimeout)
}

// logAfterWriteBreakdown records one write's after-write decomposition: the
// budget decision and the split of the total into client wait, sync, and
// diagnostics wait. These fields are the fixed metrics for budget tuning and
// real-session review, so the format stays stable.
func (m *Manager) logAfterWriteBreakdown(prepared *afterWritePrepared, status FileWriteStatus, notified, lateWatch bool, diagWait time.Duration) {
	batch := ""
	if prepared.batchSize > 0 {
		batch = fmt.Sprintf(" batch_size=%v batch_elapsed_ms=%v", prepared.batchSize, time.Since(prepared.began).Milliseconds())
	}
	totalBudget := configuredDiagnosticsWait(m.cfg)
	if prepared.coldStart || prepared.grace {
		totalBudget = coldStartWriteBudget
	}
	log.Debugf("lsp: after-write diagnostics breakdown path=%v cold=%v grace=%v status=%v client_wait_ms=%v sync_ms=%v diag_wait_ms=%v budget_ms=%v total_budget_ms=%v total_ms=%v notified=%v late_watch=%v servers=%v failed_servers=%v%v",
		prepared.path, prepared.coldStart, prepared.grace, status,
		prepared.clientWait.Milliseconds(), prepared.syncTime.Milliseconds(), diagWait.Milliseconds(),
		prepared.waitTimeout.Milliseconds(), totalBudget.Milliseconds(), time.Since(prepared.began).Milliseconds(), notified, lateWatch, len(prepared.sync.serverVersions), len(prepared.sync.serverErrors), batch)
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
		return m.afterWritePythonToolResult(ctx, absPath, content, base, includeOtherFiles, ranges, changeType, displayBaseDir).Output
	}
	// Unassociated file type: skip LSP entirely (no Start, no note).
	if !m.HasServerForPath(absPath) {
		return base
	}

	coldStart, base, exited, began := m.beginAfterFileWrite(ctx, absPath, base)
	var prepared afterWritePrepared
	bounded := coldStart || m.clientAdmittedWithin(absPath, warmupGrace)
	if !m.runWritePreparation(ctx, began, bounded, func(taskCtx context.Context) {
		prepared = m.prepareAfterFileWrite(taskCtx, absPath, content, base, changeType, coldStart, exited, began)
	}, func(taskCtx context.Context) {
		m.detachPreparedWrite(taskCtx, &prepared, identity.AgentIDFromContext(ctx))
	}) {
		return m.coldWriteBudgetResult(ctx, absPath, base, includeOtherFiles, displayBaseDir).Output
	}
	return m.awaitPreparedAfterFileWrite(ctx, &prepared, includeOtherFiles, displayBaseDir).Output
}

// publishesOncePerVersion reports whether the server publishes one final
// diagnostic set per document version, so a settle window after the first
// matching publish would only delay the result. The list is built from
// real-server probes (TestRealServerPublishesPerVersion): gopls publishes its
// full analysis only after its own internal delay, well outside any settle
// window. A server that publishes several passes per version
// must keep the settle window, because the wait cannot know its first
// snapshot is transient.
func publishesOncePerVersion(name string, cfg config.LSPServerConfig) bool {
	return isGoplsServer(name, cfg)
}

// publishesUnversioned reports servers whose publishDiagnostics never echo
// the document version. TypeScript can publish syntax and semantic results
// separately without a version, so it retains the settle window. For it the
// freshness filter — received after the
// sync — is the version check. A server that versions its publishes must
// never confirm an immediate wait on an unversioned publish: gopls emits one
// for the unopened-document snapshot while the workspace is still loading,
// and consuming it as the synced version's result would report stale
// diagnostics as fresh.
func publishesUnversioned(name string, cfg config.LSPServerConfig) bool {
	return isTypeScriptServer(name, cfg)
}

// immediateDiagnosticsServers returns the synced servers whose first publish for
// the synced version ends the diagnostics wait (see
// diagnosticsWaitRequest.immediateServers).
func (m *Manager) immediateDiagnosticsServers(serverVersions map[string]int32) map[string]bool {
	if m == nil || m.cfg == nil || len(serverVersions) == 0 {
		return nil
	}
	var out map[string]bool
	for name := range serverVersions {
		cfg, ok := m.cfg.LSP[name]
		if !ok || !publishesOncePerVersion(name, cfg) {
			continue
		}
		if out == nil {
			out = make(map[string]bool, len(serverVersions))
		}
		out[name] = true
	}
	return out
}

// unversionedDiagnosticsServers returns the synced servers whose
// publishes never carry a document version (see
// diagnosticsWaitRequest.unversionedServers).
func (m *Manager) unversionedDiagnosticsServers(serverVersions map[string]int32) map[string]bool {
	if m == nil || m.cfg == nil || len(serverVersions) == 0 {
		return nil
	}
	var out map[string]bool
	for name := range serverVersions {
		cfg, ok := m.cfg.LSP[name]
		if !ok || !publishesUnversioned(name, cfg) {
			continue
		}
		if out == nil {
			out = make(map[string]bool, len(serverVersions))
		}
		out[name] = true
	}
	return out
}

func (m *Manager) logLSPServiceNote(path, msg string) {
	if msg == "" {
		return
	}
	log.Debugf("lsp: non-actionable service note suppressed path=%v detail=%v", path, msg)
}

// LSP degradation kinds that get one model-facing line per server per session.
const (
	lspDegradationStart  = "start"
	lspDegradationExited = "exited"
	lspDegradationSync   = "sync"
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
			// The server is about to serve this file, and the write was
			// re-registered for a re-sync at admission: the diagnostics are
			// deferred, not lost.
			base = appendLSPDegradationNote(base, formatLSPDegradationNote(info.key.name, info.msg+"; diagnostics follow with the next request once the server is up"))
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
