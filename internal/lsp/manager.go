package lsp

import (
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/keakon/golog/log"

	pnprotocol "github.com/keakon/x/powernap/pkg/lsp/protocol"

	"github.com/keakon/chord/internal/config"
)

// TypeLSPDiagnostics is the envelope type for LSP diagnostics (use with Hub.Broadcast).
const TypeLSPDiagnostics = "lsp.diagnostics"

// TypeLSPSidebarStatus is broadcast when LSP server connection state changes (TUI sidebar).
const TypeLSPSidebarStatus = "lsp.sidebar_status"

type WatchedFileChangeType = pnprotocol.FileChangeType

const (
	WatchedFileCreated WatchedFileChangeType = pnprotocol.Created
	WatchedFileChanged WatchedFileChangeType = pnprotocol.Changed
	WatchedFileDeleted WatchedFileChangeType = pnprotocol.Deleted
)

// Diagnostic is a single LSP diagnostic (1=Error, 2=Warning, 3=Info, 4=Hint).
// Same shape as protocol.Diagnostic for wire compatibility.
type Diagnostic struct {
	Severity int    `json:"severity"`
	Line     int    `json:"line"`
	Col      int    `json:"col"`
	Code     string `json:"code,omitempty"`
	Message  string `json:"message"`
	Source   string `json:"source,omitempty"`
}

// DiagnosticsPayload is the payload for TypeLSPDiagnostics (no session_id).
type DiagnosticsPayload struct {
	URI         string       `json:"uri"`
	ServerID    string       `json:"server_id,omitempty"`
	Diagnostics []Diagnostic `json:"diagnostics"`
}

// BroadcastFunc is called to send an LSP event (e.g. type + payload for Hub.Broadcast).
type BroadcastFunc func(eventType string, payload any)

// SidebarStatusPayload is JSON-marshaled for Hub.Broadcast (TUI sidebar).
type SidebarStatusPayload struct {
	Servers []SidebarServerEntry `json:"servers"`
}

// SidebarServerEntry is one row in the ENVIRONMENT / LSP sidebar block.
type SidebarServerEntry struct {
	Name     string `json:"name"`
	OK       bool   `json:"ok"`
	Pending  bool   `json:"pending,omitempty"`
	Error    string `json:"error,omitempty"`
	Errors   int    `json:"errors,omitempty"`
	Warnings int    `json:"warnings,omitempty"`
}

type diagnosticsEvent struct {
	diagnostics []Diagnostic
	clientKey   clientKey
	serverID    string
	version     int32
	receivedAt  time.Time
}

// clientKey identifies a language-server client by server name and workspace
// root. The same server can run once per discovered root, e.g. one
// typescript-language-server per nested frontend package of a monorepo.
type clientKey struct {
	name string
	root string
}

// startEntry tracks one in-flight server launch. ctx is the manager's start
// era at launch time: Stop cancels that era, so ctx.Err() != nil reports "a
// Stop began after this launch was spawned", which forbids registering the
// finished client into the stopped manager. done is closed when the launch
// goroutine exits, letting Stop wait for in-flight launches to settle.
type startEntry struct {
	ctx  context.Context
	done chan struct{}
}

// Manager manages multiple LSP clients and aggregates diagnostics.
type Manager struct {
	// projectRoot is the checkout every client is rooted in. It is stored
	// behind an atomic pointer because a worktree switch rebinds it while
	// tool goroutines keep reading it.
	projectRoot atomic.Pointer[string]
	cfg         *config.Config
	broadcast   BroadcastFunc
	clients     map[clientKey]*Client
	clientsMu   sync.RWMutex

	// starting marks servers whose launch goroutine is still in flight; it is
	// a pure presence predicate (guarded by clientsMu) used for start
	// deduplication and the pending-start check.
	starting map[clientKey]bool

	// launches carries the per-launch state (start era context + done
	// channel) for the goroutines tracked in starting, guarded by clientsMu.
	// It is kept separate so the two maps stay coherent under one lock while
	// starting keeps its boolean presence semantics.
	launches map[clientKey]*startEntry

	// startEra is the context era in-flight launches run under. Stop cancels
	// the current era and clears it, so every launch that began before the
	// stop aborts and is refused at registration; the next Start creates a
	// fresh era, which is what lets an idle unload be followed by a normal
	// cold start. Guarded by clientsMu.
	startEra       context.Context
	cancelStartEra context.CancelFunc

	waiters   map[string][]chan diagnosticsEvent
	waitersMu sync.Mutex

	startFailMu sync.Mutex
	startFail   map[clientKey]string // client -> last start/init error

	// diagByServer tracks the latest diagnostics per client+URI for tool output,
	// broadcasts, and file-scoped review snapshots.
	diagMu         sync.RWMutex
	diagByServer   map[clientKey]map[string]diagCounts
	reviewByServer map[string]map[string]reviewCounts

	// diagState records the on-disk freshness state for each path's published
	// diagnostics. A stale path remains suppressed until Chord synchronizes it
	// or all language servers publish it clean.
	diagState map[string]diagnosticPathState

	// publishedDiagByServer tracks the latest diagnostic identities for each
	// server and path. It lets a clean publish from one server avoid clearing
	// suppression while another server still reports the path.
	publishedDiagByServer map[clientKey]map[string]map[diagnosticIdentity]struct{}

	// reportedByPath tracks other-file diagnostics already appended to tool
	// output this session, keyed by diagnostic identity. Only unreported
	// diagnostics are attached again. An identity is removed after it disappears
	// from every server's published set, so a real regression is reported again.
	reportedByPath map[string]map[diagnosticIdentity]struct{}

	// touchedPaths tracks files modified by successful Write/Edit calls in the current
	// session. Successful Delete removes a file from this set.
	touchedMu    sync.RWMutex
	touchedPaths map[string]struct{}
}

// diagCounts tracks error/warning counts for a single URI.
type diagCounts struct {
	errors   int
	warnings int
}

type diagnosticPathState struct {
	mtime      time.Time
	stale      bool
	generation uint64
}

// NewManager creates a manager. broadcast is called when diagnostics are received.
// If cfg.LSP is nil or empty, no LSPs are started.
func NewManager(cfg *config.Config, projectRoot string, broadcast BroadcastFunc) *Manager {
	if broadcast == nil {
		broadcast = func(string, any) {}
	}
	m := &Manager{
		cfg:                   cfg,
		broadcast:             broadcast,
		clients:               make(map[clientKey]*Client),
		starting:              make(map[clientKey]bool),
		launches:              make(map[clientKey]*startEntry),
		waiters:               make(map[string][]chan diagnosticsEvent),
		startFail:             make(map[clientKey]string),
		diagByServer:          make(map[clientKey]map[string]diagCounts),
		reviewByServer:        make(map[string]map[string]reviewCounts),
		diagState:             make(map[string]diagnosticPathState),
		publishedDiagByServer: make(map[clientKey]map[string]map[diagnosticIdentity]struct{}),
		reportedByPath:        make(map[string]map[diagnosticIdentity]struct{}),
		touchedPaths:          make(map[string]struct{}),
	}
	m.projectRoot.Store(&projectRoot)
	return m
}

// projectRootPath returns the checkout the manager currently binds clients to.
func (m *Manager) projectRootPath() string {
	if m == nil {
		return ""
	}
	if p := m.projectRoot.Load(); p != nil {
		return *p
	}
	return ""
}

// RebindToProjectRoot points the manager at another checkout. Open clients are
// closed first: their workspace roots and file URIs belong to the previous
// checkout, so their diagnostics must not be attributed to the new one, and the
// diagnostic state recorded for the previous checkout is dropped along with
// them. Later requests start fresh clients under root.
func (m *Manager) RebindToProjectRoot(ctx context.Context, root string) error {
	if m == nil {
		return nil
	}
	root = strings.TrimSpace(root)
	if root == "" {
		return fmt.Errorf("lsp: empty project root")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("lsp: resolve project root %s: %w", root, err)
	}
	if m.projectRootPath() == abs {
		return nil
	}
	m.Stop(ctx)
	m.forgetCheckoutDiagnostics()
	m.projectRoot.Store(&abs)
	m.notifySidebarChanged()
	return nil
}

func (m *Manager) onDiagnostics(key clientKey) func(uri string, _ string, diags []pnprotocol.Diagnostic, version int32) {
	return func(uri string, _ string, diags []pnprotocol.Diagnostic, version int32) {
		receivedAt := time.Now()
		chordDiags := convertDiagnostics(diags)
		path := normalizeWaiterPath(uriToPath(uri))
		payload := DiagnosticsPayload{
			URI:         uri,
			ServerID:    key.name,
			Diagnostics: chordDiags,
		}
		m.broadcast(TypeLSPDiagnostics, payload)

		// Track the server's latest diagnostics. Paths already admitted to the
		// sidebar by an explicit post-write review stay live from this point on:
		// external editor saves and git operations bypass AfterFileWrite, but a
		// later publish must still replace (or clear) their stale review counts.
		var errs, warns int
		for _, d := range chordDiags {
			switch d.Severity {
			case 1:
				errs++
			case 2:
				warns++
			}
		}
		m.diagMu.Lock()
		if m.diagByServer == nil {
			m.diagByServer = make(map[clientKey]map[string]diagCounts)
		}
		if errs == 0 && warns == 0 {
			if byURI, ok := m.diagByServer[key]; ok {
				delete(byURI, uri)
				if len(byURI) == 0 {
					delete(m.diagByServer, key)
				}
			}
		} else {
			byURI := m.diagByServer[key]
			if byURI == nil {
				byURI = make(map[string]diagCounts)
				m.diagByServer[key] = byURI
			}
			byURI[uri] = diagCounts{errors: errs, warnings: warns}
		}
		m.updateDiagnosticOutputStateLocked(key, path, chordDiags)
		if byPath := m.reviewByServer[key.name]; byPath != nil {
			if reviewed, ok := byPath[path]; ok {
				latest := m.reviewCountsForPathLocked(key.name, path)
				reviewed.errors = latest.errors
				reviewed.warnings = latest.warnings
				byPath[path] = reviewed
			}
		}
		m.diagMu.Unlock()
		m.notifySidebarChanged()

		m.waitersMu.Lock()
		for _, ch := range m.waiters[path] {
			select {
			case ch <- diagnosticsEvent{diagnostics: chordDiags, clientKey: key, serverID: key.name, version: version, receivedAt: receivedAt}:
			default:
			}
		}
		m.waitersMu.Unlock()
	}
}

func convertDiagnostics(diags []pnprotocol.Diagnostic) []Diagnostic {
	out := make([]Diagnostic, 0, len(diags))
	for _, d := range diags {
		line, col := int(d.Range.Start.Line), int(d.Range.Start.Character)
		out = append(out, Diagnostic{
			Severity: int(d.Severity),
			Line:     line,
			Col:      col,
			Code:     diagnosticCodeString(d.Code),
			Message:  d.Message,
			Source:   d.Source,
		})
	}
	return out
}

func diagnosticCodeString(code any) string {
	switch v := code.(type) {
	case nil:
		return ""
	case string:
		return v
	case float64:
		if v == float64(int64(v)) {
			return fmt.Sprintf("%d", int64(v))
		}
		return fmt.Sprint(v)
	case int:
		return fmt.Sprint(v)
	case int64:
		return fmt.Sprint(v)
	default:
		return fmt.Sprint(v)
	}
}

func uriToPath(uri string) string {
	u := pnprotocol.DocumentURI(uri)
	path, _ := u.Path()
	return path
}

// Start starts LSP servers that can handle the given file path.
// Each client is initialized in its own goroutine so the write lock is not held
// during the potentially slow subprocess launch + LSP handshake. The workspace
// root is discovered per file: the nearest ancestor directory holding any
// configured root marker, falling back to the project root, so nested frontend
// packages get their own language-server instance rooted at the package.
func (m *Manager) Start(ctx context.Context, path string) {
	if m.cfg == nil || len(m.cfg.LSP) == 0 {
		return
	}
	if !pathUnderDir(path, m.projectRootPath()) {
		return
	}
	m.clientsMu.Lock()
	var toStart []struct {
		key   clientKey
		cfg   config.LSPServerConfig
		entry *startEntry
	}
	for name, srvCfg := range m.cfg.LSP {
		if srvCfg.Disabled {
			continue
		}
		root, ok := m.serverRootForPath(name, srvCfg, path)
		if !ok {
			continue
		}
		key := clientKey{name: name, root: root}
		if _, ok := m.clients[key]; ok {
			continue
		}
		if m.starting[key] {
			continue
		}
		if m.startEra == nil {
			// A Stop cleared the era; create a fresh one so launches after
			// the stop run on a live context (idle-unload reload).
			m.startEra, m.cancelStartEra = context.WithCancel(context.Background())
		}
		m.starting[key] = true
		entry := &startEntry{ctx: m.startEra, done: make(chan struct{})}
		m.launches[key] = entry
		toStart = append(toStart, struct {
			key   clientKey
			cfg   config.LSPServerConfig
			entry *startEntry
		}{key, srvCfg, entry})
	}
	m.clientsMu.Unlock()

	for _, s := range toStart {
		key, srvCfg, entry := s.key, s.cfg, s.entry
		go m.startServer(ctx, key, srvCfg, entry)
	}
}

func (m *Manager) startServer(ctx context.Context, key clientKey, srvCfg config.LSPServerConfig, entry *startEntry) {
	// The launch goroutine must never outlive its own bookkeeping: every
	// return path clears the starting markers and closes the entry's done
	// channel, so a concurrent Stop waiting on the launch can finish.
	defer func() {
		m.clientsMu.Lock()
		delete(m.starting, key)
		delete(m.launches, key)
		m.clientsMu.Unlock()
		close(entry.done)
	}()

	// Run the handshake on a context cancelled by either the requesting turn
	// or the manager: Stop cancels entry.ctx (the start era), so it never
	// waits on a launch whose server is unresponsive to the caller's ctx.
	if ctx == nil {
		ctx = context.Background()
	}
	launchCtx, cancelLaunch := context.WithCancel(entry.ctx)
	stopCallerCancel := context.AfterFunc(ctx, cancelLaunch)
	defer func() {
		stopCallerCancel()
		cancelLaunch()
	}()
	ctx = launchCtx

	m.startFailMu.Lock()
	if m.startFail == nil {
		m.startFail = make(map[clientKey]string)
	}
	delete(m.startFail, key)
	m.startFailMu.Unlock()

	client, err := newClient(ctx, key.name, srvCfg, key.root, m.projectRootPath(), false)
	if err != nil {
		log.Errorf("lsp: create client name=%v root=%v error=%v", key.name, key.root, err)
		// A Stop that cancelled this launch's start era is the cause, not the
		// server: a stopped manager must not keep a start failure for a server
		// it never started. Same gate as admitStartedClientLocked.
		if entry.ctx.Err() == nil {
			m.startFailMu.Lock()
			m.startFail[key] = err.Error()
			m.startFailMu.Unlock()
			m.notifySidebarChanged()
		}
		return
	}
	client.SetOnDiagnostics(m.onDiagnostics(key))
	if err := client.Initialize(ctx); err != nil {
		log.Errorf("lsp: initialize client name=%v root=%v error=%v", key.name, key.root, err)
		_ = client.Close(ctx)
		if entry.ctx.Err() == nil {
			m.startFailMu.Lock()
			m.startFail[key] = err.Error()
			m.startFailMu.Unlock()
			m.notifySidebarChanged()
		}
		return
	}
	// Wait for the server to be ready before exposing it (sidebar green + ClientForPath).
	// Avoids "gopls: not started" when the first call happens before init completes (see crush).
	const serverReadyTimeout = 15 * time.Second
	if err := client.WaitForServerReady(ctx, serverReadyTimeout); err != nil {
		log.Warnf("lsp: server not fully ready, continuing anyway name=%v root=%v error=%v", key.name, key.root, err)
		// Still add the client so later calls can succeed; first request may still fail briefly.
	}
	// Mark the newcomer as most-recently-used before it can be considered for
	// eviction: it was started to serve a file the caller is reading right now,
	// and an untouched client sorts as the least-recently-used one, so without
	// this the instance for the ninth root would be closed the instant it came
	// up and restarted on the next read.
	client.touch(time.Now().UnixNano())
	m.clientsMu.Lock()
	closeMe, survivors, admitted := m.admitStartedClientLocked(key, entry, client)
	m.clientsMu.Unlock()
	if !admitted {
		// A Stop began after this launch was spawned; the stopped manager
		// must not grow a client, so close the finished one instead. ctx is
		// already cancelled by the stop, so Close falls back to Kill.
		log.Infof("lsp: discarding client whose launch outlived Stop name=%v root=%v", key.name, key.root)
		if err := client.Close(ctx); err != nil {
			log.Warnf("lsp: close discarded client error=%v", err)
		}
		return
	}
	for _, victim := range closeMe {
		if err := victim.Close(ctx); err != nil {
			log.Warnf("lsp: close evicted client error=%v", err)
		}
	}
	if cleared := m.dropOrphanedDiagnostics(survivors); m.broadcast != nil {
		for _, c := range cleared {
			m.broadcast(TypeLSPDiagnostics, DiagnosticsPayload{URI: c.uri, ServerID: c.server, Diagnostics: nil})
		}
	}
	m.notifySidebarChanged()
}

// admitStartedClientLocked registers the finished client under key unless a
// Stop began after its launch, which cancels the entry's start era. On
// refusal the caller must close the client instead of registering it.
// Otherwise it returns the eviction work like evictExcessClientsLocked, for
// the caller to run outside the lock. Caller holds clientsMu.
func (m *Manager) admitStartedClientLocked(key clientKey, entry *startEntry, client *Client) (closeMe []*Client, survivors map[string][]*Client, admitted bool) {
	if entry.ctx.Err() != nil {
		return nil, nil, false
	}
	m.clients[key] = client
	closeMe, survivors = m.evictExcessClientsLocked()
	return closeMe, survivors, true
}

// discoverWorkspaceRoot walks from path's directory up to the project root and
// returns the nearest ancestor containing any of the server's root markers.
// markerMatched is false when no marker was found or none is configured, in
// which case root falls back to the project root. ok is false when path lies
// outside the project root.
//
// This is the single ancestor walk behind both "does this server cover the
// file" and "where must its client be rooted"; the two questions used to be
// answered by separate functions that stat'ed the same chain twice per call.
func (m *Manager) discoverWorkspaceRoot(name string, srvCfg config.LSPServerConfig, path string) (root string, markerMatched, ok bool) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", false, false
	}
	projectRoot, err := filepath.Abs(m.projectRootPath())
	if err != nil {
		return "", false, false
	}
	rel, err := filepath.Rel(projectRoot, absPath)
	if err != nil || relPathEscapesDir(rel) {
		return "", false, false
	}
	markers := effectiveRootMarkers(name, srvCfg)
	if len(markers) == 0 {
		return projectRoot, false, true
	}
	// absPath and projectRoot are both absolute and cleaned, so walking parents
	// from the file's directory always reaches projectRoot; the parent == dir
	// check is the filesystem-root backstop for a malformed project root.
	dir := filepath.Dir(absPath)
	// rel == "." means path is the project root itself, whose parent already
	// lies outside the project. Start at the root so it is the first candidate
	// and the dir == projectRoot backstop can stop the walk there.
	if rel == "." {
		dir = absPath
	}
	for {
		for _, marker := range markers {
			if pathExists(filepath.Join(dir, marker)) {
				return dir, true, true
			}
		}
		parent := filepath.Dir(dir)
		if dir == projectRoot || parent == dir {
			break
		}
		dir = parent
	}
	return projectRoot, false, true
}

// serverRootForPath reports whether the server covers path and, if so, the
// workspace root its client must be rooted at. A server that declares
// root_markers roots at the nearest ancestor directory containing a marker,
// falling back to the project root when none matches, so the server still
// serves files outside any marker directory.
func (m *Manager) serverRootForPath(name string, srvCfg config.LSPServerConfig, path string) (string, bool) {
	if !matchesFileType(srvCfg, path) {
		return "", false
	}
	root, _, ok := m.discoverWorkspaceRoot(name, srvCfg, path)
	if !ok {
		return "", false
	}
	return root, true
}

// SidebarEntries returns enabled LSP servers and whether each is connected, failed, or not started yet.
func (m *Manager) SidebarEntries() []SidebarServerEntry {
	if m == nil || m.cfg == nil || len(m.cfg.LSP) == 0 {
		return nil
	}
	var names []string
	for n, s := range m.cfg.LSP {
		if s.Disabled {
			continue
		}
		names = append(names, n)
	}
	if len(names) == 0 {
		return nil
	}
	sort.Strings(names)

	// Read each map under its own lock to avoid lock-ordering inversion with startServer
	// (startServer acquires startFailMu then clientsMu; holding both simultaneously
	// in the opposite order would deadlock).
	m.clientsMu.RLock()
	connected := make(map[string]bool, len(m.clients))
	for k := range m.clients {
		connected[k.name] = true
	}
	m.clientsMu.RUnlock()

	m.startFailMu.Lock()
	failMap := make(map[clientKey]string, len(m.startFail))
	maps.Copy(failMap, m.startFail)
	m.startFailMu.Unlock()

	var out []SidebarServerEntry
	touched := m.touchedSnapshot()
	for _, name := range names {
		entry := SidebarServerEntry{Name: name}
		if connected[name] {
			entry.OK = true
			m.diagMu.RLock()
			for path := range touched {
				if counts, ok := m.reviewByServer[name][path]; ok {
					entry.Errors += counts.errors
					entry.Warnings += counts.warnings
				}
			}
			m.diagMu.RUnlock()
		} else if msg := failMessage(failMap, name); msg != "" {
			if len(msg) > 120 {
				msg = msg[:117] + "..."
			}
			entry.Error = msg
		} else {
			entry.Pending = true
		}
		out = append(out, entry)
	}
	return out
}

// failMessage returns the recorded start error for the named server, choosing
// the lexicographically first workspace root so the sidebar does not flip
// between roots on every refresh. The chosen root itself doubles as the
// "no entry at all" sentinel, since a workspace root is never the empty
// string.
func failMessage(failMap map[clientKey]string, name string) string {
	var chosenRoot, chosen string
	chosenSet := false
	for k, msg := range failMap {
		if k.name != name {
			continue
		}
		if !chosenSet || k.root < chosenRoot {
			chosenRoot, chosen = k.root, msg
			chosenSet = true
		}
	}
	return chosen
}

// LoadedServerNames returns the names of currently connected LSP servers.
func (m *Manager) LoadedServerNames() []string {
	if m == nil {
		return nil
	}
	m.clientsMu.RLock()
	defer m.clientsMu.RUnlock()
	if len(m.clients) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(m.clients))
	for k := range m.clients {
		seen[k.name] = struct{}{}
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (m *Manager) notifySidebarChanged() {
	if m == nil || m.broadcast == nil {
		return
	}
	go func() {
		servers := m.SidebarEntries()
		if len(servers) == 0 {
			return
		}
		m.broadcast(TypeLSPSidebarStatus, SidebarStatusPayload{Servers: servers})
	}()
}

// maxClientsPerServer caps how many live instances a single server name may
// hold at once. Without a bound, browsing each nested package of a monorepo
// spawns its own language-server process that never gets reclaimed until
// Stop. When the cap is exceeded the least-recently-used instance is evicted.
const maxClientsPerServer = 8

// clearedDiag names a diagnostic entry dropped because the instance that
// published it is gone.
type clearedDiag struct {
	server string
	uri    string
}

// evictExcessClientsLocked finds the least-recently-used clients of any server
// whose live instance count exceeds maxClientsPerServer, removes them from
// m.clients, and returns them so the caller can Close them outside the lock.
// survivors maps each affected server name to the instances that stayed, which
// the caller feeds to dropOrphanedDiagnostics: diagByServer is keyed by server
// name, so an entry published by an evicted instance would otherwise sit in the
// sidebar and in tool output forever with no client left to ever clear it.
// startFail is intentionally untouched: an evicted instance is one that started
// successfully, so it has no start-fail entry to remove.
//
// Caller must hold clientsMu (write lock).
func (m *Manager) evictExcessClientsLocked() (closeMe []*Client, survivors map[string][]*Client) {
	byServer := make(map[string][]clientKey)
	for key := range m.clients {
		byServer[key.name] = append(byServer[key.name], key)
	}
	for name, keys := range byServer {
		if len(keys) <= maxClientsPerServer {
			continue
		}
		// Root breaks ties so eviction stays deterministic when two instances
		// were last used within the same nanosecond (or never used at all).
		sort.Slice(keys, func(i, j int) bool {
			li, lj := m.clients[keys[i]].lastUsed.Load(), m.clients[keys[j]].lastUsed.Load()
			if li != lj {
				return li < lj
			}
			return keys[i].root < keys[j].root
		})
		cut := len(keys) - maxClientsPerServer
		for _, key := range keys[:cut] {
			closeMe = append(closeMe, m.clients[key])
			delete(m.clients, key)
		}
		if survivors == nil {
			survivors = make(map[string][]*Client, 1)
		}
		for _, key := range keys[cut:] {
			survivors[name] = append(survivors[name], m.clients[key])
		}
	}
	return closeMe, survivors
}

// forgetCheckoutDiagnostics drops every diagnostic the manager recorded for the
// checkout it just left. Its clients are already closed, so no publish can ever
// clear these entries: left in place they keep the previous checkout's files in
// the sidebar aggregate (keyed by server name and filtered by the session's
// touched files) and in the review snapshots the model reads.
//
// Caller must hold neither clientsMu nor diagMu.
func (m *Manager) forgetCheckoutDiagnostics() {
	m.diagMu.Lock()
	clear(m.diagByServer)
	clear(m.publishedDiagByServer)
	clear(m.reviewByServer)
	clear(m.diagState)
	clear(m.reportedByPath)
	m.diagMu.Unlock()

	m.touchedMu.Lock()
	clear(m.touchedPaths)
	m.touchedMu.Unlock()
}

// dropOrphanedDiagnostics removes every diagnostic recorded under a server name
// whose file none of that server's surviving instances handles, and returns the
// dropped entries so the caller can broadcast the clear.
//
// Caller must hold neither clientsMu nor diagMu. Client.HandlesFile only reads
// fields fixed at construction, so the surviving instances can be inspected
// without clientsMu — which matters because recordReviewSnapshot takes
// clientsMu while holding diagMu, so grabbing the two in the other order here
// would invert the lock order.
func (m *Manager) dropOrphanedDiagnostics(survivors map[string][]*Client) (cleared []clearedDiag) {
	if len(survivors) == 0 {
		return nil
	}
	m.diagMu.Lock()
	defer m.diagMu.Unlock()
	for name, live := range survivors {
		for key := range m.diagByServer {
			if key.name != name {
				continue
			}
			byURI := m.diagByServer[key]
			for uri := range byURI {
				path := uriToPath(uri)
				served := false
				for _, c := range live {
					if c.HandlesFile(path) {
						served = true
						break
					}
				}
				if served {
					continue
				}
				delete(byURI, uri)
				m.updateDiagnosticOutputStateLocked(key, normalizeWaiterPath(path), nil)
				cleared = append(cleared, clearedDiag{server: name, uri: uri})
				if byPath := m.reviewByServer[name]; byPath != nil {
					delete(byPath, normalizeWaiterPath(path))
				}
			}
			if len(byURI) == 0 {
				delete(m.diagByServer, key)
			}
		}
		// A server whose review entries were all dropped must not leave an empty
		// map behind: ResetReviews decides whether anything needs clearing from
		// len(m.reviewByServer), so a leftover empty entry makes it report a
		// change and refresh the sidebar for nothing. This deliberately sits
		// outside the diagByServer cleanup above — reviews and diagnostics empty
		// out independently, and the common case is one orphaned file among
		// several the server still has diagnostics for.
		if byPath, ok := m.reviewByServer[name]; ok && len(byPath) == 0 {
			delete(m.reviewByServer, name)
		}
	}
	return cleared
}

// matchesFileType reports whether the server's file_types cover path. A server
// without file_types accepts every extension.
func matchesFileType(srvCfg config.LSPServerConfig, path string) bool {
	if len(srvCfg.FileTypes) == 0 {
		return true
	}
	ext := strings.ToLower(filepath.Ext(path))
	for _, ft := range srvCfg.FileTypes {
		e := strings.ToLower(ft)
		if e != "" && e[0] != '.' {
			e = "." + e
		}
		if ext == e {
			return true
		}
	}
	return false
}

func pathExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func relPathEscapesDir(rel string) bool {
	return rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func pathUnderDir(path, dir string) bool {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(absDir, absPath)
	if err != nil {
		return false
	}
	return !relPathEscapesDir(rel)
}

// forEachClientForPathLocked calls fn for every client that owns path: at most
// one instance per server name, the one whose workspace root is nearest to the
// file.
//
// Nearest-root ownership is what keeps one URI published by one instance.
// Client.HandlesFile only rejects files outside the client's own root, so an
// instance rooted at the repository root also accepts files inside a nested
// package that has its own instance. Notifying both would make two clients of
// the same server publish diagnostics for the same URI under the same server
// name, and the second publish would overwrite — or, when it is empty, clear —
// the first in diagByServer. Both roots contain the file, so they lie on one
// ancestor chain and the longer root is the deeper one.
//
// Ownership is derived from the running clients rather than re-derived from the
// config, so a client stays reachable for didClose and diagnostics cleanup even
// if its server entry is later disabled.
//
// Caller must hold at least clientsMu.RLock().
func (m *Manager) forEachClientForPathLocked(path string, fn func(clientKey, *Client)) {
	if len(m.clients) == 0 {
		return
	}
	var owners []clientKey
	for key, c := range m.clients {
		if !c.HandlesFile(path) {
			continue
		}
		seen := false
		for i, existing := range owners {
			if existing.name != key.name {
				continue
			}
			if len(key.root) > len(existing.root) {
				owners[i] = key
			}
			seen = true
			break
		}
		if !seen {
			owners = append(owners, key)
		}
	}
	for _, key := range owners {
		c := m.clients[key]
		c.touch(time.Now().UnixNano())
		fn(key, c)
	}
}

// clientForPathLocked returns the client that owns path, preferring the
// instance rooted nearest to it so nested packages route to their own server.
// Caller must hold at least RLock.
func (m *Manager) clientForPathLocked(path string) (*Client, bool) {
	var found *Client
	m.forEachClientForPathLocked(path, func(_ clientKey, c *Client) {
		if found == nil {
			found = c
		}
	})
	return found, found != nil
}

// hasPendingStartForPathLocked reports whether a launch for the workspace
// root that would serve path is still in flight and its client is not yet
// registered. A starting marker whose client already appeared counts as
// settled: the launch finished and only its bookkeeping cleanup remains, so
// readiness can use the registered client immediately. Caller must hold at
// least clientsMu.RLock().
func (m *Manager) hasPendingStartForPathLocked(path string) bool {
	if m.cfg == nil || len(m.cfg.LSP) == 0 {
		return false
	}
	for key := range m.starting {
		srvCfg, ok := m.cfg.LSP[key.name]
		if !ok || srvCfg.Disabled {
			continue
		}
		// Only a server whose configured file types and root markers cover the
		// path can be "starting for" it; otherwise a Go read would count a
		// TypeScript startup as pending for that path.
		if root, ok := m.serverRootForPath(key.name, srvCfg, path); ok && root == key.root {
			if _, ok := m.clients[key]; !ok {
				return true
			}
		}
	}
	return false
}

func (m *Manager) waitForClientForPath(ctx context.Context, path string, timeout time.Duration) (*Client, bool) {
	if !m.HasServerForPath(path) {
		return nil, false
	}

	m.Start(ctx, path)

	check := func() (*Client, bool, bool) {
		m.clientsMu.RLock()
		defer m.clientsMu.RUnlock()
		// Readiness waits for the target (name, root) launch to settle even
		// when an ancestor client already handles the file: an instance rooted
		// at the repository root also accepts files inside a nested package,
		// so returning it while the nearer instance for this path is still
		// starting would route the first navigation or the post-write sync to
		// the old environment. Once the launch settles (its client registers,
		// or the launch ends without one) the owner below is final and can be
		// served; a failed nearer launch falls back to the ancestor.
		if m.hasPendingStartForPathLocked(path) {
			return nil, false, true
		}
		c, ok := m.clientForPathLocked(path)
		if ok {
			return c, true, false
		}
		return nil, false, false
	}

	if c, ok, _ := check(); ok {
		return c, true
	}
	if timeout <= 0 {
		return nil, false
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	for {
		if c, ok, pending := check(); ok {
			return c, true
		} else if !pending {
			return nil, false
		}

		select {
		case <-ctx.Done():
			return nil, false
		case <-timer.C:
			return nil, false
		case <-ticker.C:
		}
	}
}

// ClientForPath returns a client that handles the given path, starting the server if needed.
// If the server is still starting (async), waits up to clientWaitTimeout for it to appear.
// Returns (nil, false) if no LSP is configured for this path or the server did not become ready in time.
func (m *Manager) ClientForPath(ctx context.Context, path string) (*Client, bool) {
	const clientWaitTimeout = 20 * time.Second
	return m.waitForClientForPath(ctx, path, clientWaitTimeout)
}

// DidOpen sends didOpen to the clients that own path; maintains version per client.
func (m *Manager) DidOpen(ctx context.Context, path string, content string) {
	if ctx == nil {
		ctx = context.Background()
	}
	m.clientsMu.RLock()
	defer m.clientsMu.RUnlock()
	m.forEachClientForPathLocked(path, func(_ clientKey, c *Client) {
		_, _ = c.DidOpen(ctx, path, content)
	})
}

// DidChange sends didChange to all clients that handle path.
func (m *Manager) DidChange(ctx context.Context, path string, content string) {
	_ = m.DidChangeErr(ctx, path, content)
}

// DidChangeErr is like DidChange but returns the first notify error from any client.
func (m *Manager) DidChangeErr(ctx context.Context, path string, content string) error {
	_, err := m.DidChangeVersions(ctx, path, content)
	return err
}

// NotifyWatchedFileChanged sends workspace/didChangeWatchedFiles to the clients
// that own path. This keeps language-server project graphs in sync
// for file create/change/delete events, including newly created modules that are
// imported by other files.
func (m *Manager) NotifyWatchedFileChanged(ctx context.Context, path string, changeType pnprotocol.FileChangeType) error {
	if ctx == nil {
		ctx = context.Background()
	}
	path = normalizeWaiterPath(path)
	m.clientsMu.RLock()
	defer m.clientsMu.RUnlock()
	var first error
	m.forEachClientForPathLocked(path, func(_ clientKey, c *Client) {
		if err := c.NotifyWatchedFileChange(ctx, path, changeType); err != nil && first == nil {
			first = err
		}
	})
	return first
}

// DidChangeVersions sends didChange to the clients that own path and returns the
// document versions used by each server notification. The versions are used to
// ignore stale publishDiagnostics snapshots when servers include diagnostic
// versions. Keying by server name is safe because forEachClientForPathLocked
// yields at most one instance per server for a given path.
func (m *Manager) DidChangeVersions(ctx context.Context, path string, content string) (map[string]int32, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	m.clientsMu.RLock()
	defer m.clientsMu.RUnlock()
	versions := make(map[string]int32)
	var first error
	m.forEachClientForPathLocked(path, func(key clientKey, c *Client) {
		version, err := c.DidChange(ctx, path, content)
		if err == nil {
			versions[key.name] = version
		} else if first == nil {
			first = err
		}
	})
	return versions, first
}

// DidClose sends didClose to all clients that handle path, clears cached diagnostics for that path,
// and refreshes sidebar/server counts.
func (m *Manager) DidClose(ctx context.Context, path string) {
	_ = m.DidCloseErr(ctx, path)
}

// DidCloseErr is like DidClose but returns the first notify error from any client.
//
// The clientsMu and diagMu sections run separately: forEachClientForPathLocked
// requires clientsMu, while the diagnostics cleanup needs diagMu. Taking diagMu
// while still holding clientsMu would invert the lock order against
// onDiagnostics / recordReviewSnapshot (diagMu -> clientsMu), so the client
// handles are collected under clientsMu and released before diagMu is touched.
func (m *Manager) DidCloseErr(ctx context.Context, path string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	path = normalizeWaiterPath(path)
	m.clientsMu.RLock()
	type closeEntry struct {
		key clientKey
		c   *Client
	}
	entries := make([]closeEntry, 0, 2)
	m.forEachClientForPathLocked(path, func(key clientKey, c *Client) {
		entries = append(entries, closeEntry{key: key, c: c})
	})
	m.clientsMu.RUnlock()

	var first error
	var changed bool
	for _, e := range entries {
		if err := e.c.DidClose(ctx, path); err != nil && first == nil {
			first = err
		}
		e.c.clearDiagnosticsForPath(path)
		uri := string(pnprotocol.URIFromPath(path))
		m.diagMu.Lock()
		if byURI, ok := m.diagByServer[e.key]; ok {
			if _, exists := byURI[uri]; exists {
				delete(byURI, uri)
				changed = true
				if len(byURI) == 0 {
					delete(m.diagByServer, e.key)
				}
			}
		}
		m.updateDiagnosticOutputStateLocked(e.key, path, nil)
		m.diagMu.Unlock()
		if m.broadcast != nil {
			m.broadcast(TypeLSPDiagnostics, DiagnosticsPayload{URI: uri, ServerID: e.key.name, Diagnostics: nil})
		}
	}
	m.waitersMu.Lock()
	delete(m.waiters, path)
	m.waitersMu.Unlock()
	if changed {
		m.notifySidebarChanged()
	}
	return first
}

// Diagnostics returns aggregated diagnostics for the path from all clients.
func (m *Manager) Diagnostics(path string) []Diagnostic {
	m.clientsMu.RLock()
	defer m.clientsMu.RUnlock()
	var out []Diagnostic
	for _, c := range m.clients {
		out = append(out, convertDiagnostics(c.GetDiagnostics(path))...)
	}
	return out
}

// PrepareWaiter registers a diagnostics waiter for path and returns a channel to pass to
// AwaitWaiter. Call this BEFORE sending didChange/didOpen so that notifications from
// fast LSP servers are never missed.
func (m *Manager) PrepareWaiter(path string) chan diagnosticsEvent {
	path = normalizeWaiterPath(path)
	ch := make(chan diagnosticsEvent, 8)
	m.waitersMu.Lock()
	m.waiters[path] = append(m.waiters[path], ch)
	m.waitersMu.Unlock()
	return ch
}

// AwaitWaiter waits on a channel obtained from PrepareWaiter.
// Returns (diags, true) if publishDiagnostics arrived; (cached diags, false) on timeout/cancel.
func (m *Manager) AwaitWaiter(ctx context.Context, path string, ch chan diagnosticsEvent, timeout time.Duration) ([]Diagnostic, bool) {
	return m.AwaitFreshWaiter(ctx, path, ch, diagnosticsWaitRequest{}, timeout)
}

type diagnosticsWaitRequest struct {
	serverVersions map[string]int32
	after          time.Time
	settle         time.Duration
}

// AwaitFreshWaiter waits for diagnostics that match the current edit. If an LSP
// server publishes diagnostics with a document version, stale versions are
// ignored. Otherwise, only diagnostics published after the edit notification are
// accepted. Once a fresh event arrives, the function waits briefly for additional
// diagnostics to settle so multi-phase servers such as gopls do not expose a
// transient first snapshot as final tool output.
func (m *Manager) AwaitFreshWaiter(ctx context.Context, path string, ch chan diagnosticsEvent, req diagnosticsWaitRequest, timeout time.Duration) ([]Diagnostic, bool) {
	path = normalizeWaiterPath(path)
	if req.settle <= 0 {
		req.settle = diagnosticsSettleDuration
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	defer func() {
		m.waitersMu.Lock()
		m.removeWaiter(path, ch)
		m.waitersMu.Unlock()
	}()

	var gotFresh bool
	var last []Diagnostic
	var settle <-chan time.Time
	for {
		select {
		case ev := <-ch:
			if !diagnosticsEventFresh(ev, req) {
				continue
			}
			gotFresh = true
			last = ev.diagnostics
			settleTimer := time.NewTimer(req.settle)
			defer settleTimer.Stop()
			settle = settleTimer.C
		case <-settle:
			return last, true
		case <-ctx.Done():
			return nil, false
		case <-deadline.C:
			return m.Diagnostics(path), gotFresh
		}
	}
}

func diagnosticsEventFresh(ev diagnosticsEvent, req diagnosticsWaitRequest) bool {
	if len(req.serverVersions) > 0 {
		if want, ok := req.serverVersions[ev.serverID]; ok && ev.version != 0 && ev.version != want {
			return false
		}
	}
	if !req.after.IsZero() && ev.version == 0 && ev.receivedAt.Before(req.after) {
		return false
	}
	return true
}

// WaitDiagnostics blocks until diagnostics for path are received or timeout.
func (m *Manager) WaitDiagnostics(ctx context.Context, path string, timeout time.Duration) []Diagnostic {
	diags, _ := m.WaitDiagnosticsNotify(ctx, path, timeout)
	return diags
}

// WaitDiagnosticsNotify returns diagnostics and whether publishDiagnostics arrived within timeout.
func (m *Manager) WaitDiagnosticsNotify(ctx context.Context, path string, timeout time.Duration) ([]Diagnostic, bool) {
	ch := m.PrepareWaiter(path)
	return m.AwaitWaiter(ctx, path, ch, timeout)
}

func (m *Manager) removeWaiter(path string, ch chan diagnosticsEvent) {
	for i, c := range m.waiters[path] {
		if c == ch {
			m.waiters[path] = append(m.waiters[path][:i], m.waiters[path][i+1:]...)
			break
		}
	}
	if len(m.waiters[path]) == 0 {
		delete(m.waiters, path)
	}
}

func normalizeWaiterPath(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		return filepath.Clean(p)
	}
	return filepath.Clean(abs)
}

// Stop shuts down every registered client and settles launches that were
// already in flight. It cancels the current start era (aborting those
// launches' handshakes), refuses their later registration, and waits for
// them to exit — so when Stop returns no client can appear afterwards and no
// launch goroutine predating the stop survives it. The manager stays usable:
// a later Start creates a fresh era and registers normally, which is what
// reloads language servers after an idle unload.
//
// Clients are detached from the map under clientsMu and closed without it. A
// graceful server shutdown waits for the reply, and reading that reply runs the
// connection's notification handlers, which take diagMu and then clientsMu
// (see reviewCountsForPathLocked). Holding clientsMu across the close therefore
// deadlocks the close against those handlers until the caller's context
// expires — a checkout switch would pay its whole rebind budget for it.
func (m *Manager) Stop(ctx context.Context) {
	m.clientsMu.Lock()
	if m.cancelStartEra != nil {
		m.cancelStartEra()
		m.startEra = nil
		m.cancelStartEra = nil
	}
	inflight := make([]*startEntry, 0, len(m.launches))
	for _, entry := range m.launches {
		inflight = append(inflight, entry)
	}
	type closingClient struct {
		key    clientKey
		client *Client
	}
	closing := make([]closingClient, 0, len(m.clients))
	for key, c := range m.clients {
		closing = append(closing, closingClient{key: key, client: c})
		delete(m.clients, key)
	}
	m.clientsMu.Unlock()

	for _, c := range closing {
		if err := c.client.Close(ctx); err != nil {
			log.Warnf("lsp: stop client name=%v root=%v error=%v", c.key.name, c.key.root, err)
		}
	}

	// Wait for the in-flight launches to exit (their contexts are cancelled,
	// so they settle promptly). The caller's context still bounds the wait in
	// case a launch is wedged in a call that ignores cancellation.
	for _, entry := range inflight {
		select {
		case <-entry.done:
		case <-ctx.Done():
			return
		}
	}
}

// ConfiguredServerInfo describes a configured LSP server and its handled file types.
type ConfiguredServerInfo struct {
	Name      string
	FileTypes []string
}

// ConfiguredServers returns the list of enabled LSP servers sorted by name.
// FileTypes are normalized to "*.ext" format and returned as a copy.
func (m *Manager) ConfiguredServers() []ConfiguredServerInfo {
	if m == nil || m.cfg == nil {
		return nil
	}
	var names []string
	for name, srv := range m.cfg.LSP {
		if !srv.Disabled {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return nil
	}
	sort.Strings(names)
	out := make([]ConfiguredServerInfo, 0, len(names))
	for _, name := range names {
		srv := m.cfg.LSP[name]
		var fts []string
		for _, ft := range srv.FileTypes {
			ext := strings.ToLower(ft)
			if ext == "" {
				continue
			}
			if ext[0] != '.' {
				ext = "." + ext
			}
			fts = append(fts, "*"+ext)
		}
		sort.Strings(fts)
		out = append(out, ConfiguredServerInfo{Name: name, FileTypes: fts})
	}
	return out
}
