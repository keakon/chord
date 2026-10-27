package lsp

import (
	"context"
	"fmt"
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
	"github.com/keakon/chord/internal/pathutil"
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
	Root     string `json:"root,omitempty"`
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
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
}

// Manager manages multiple LSP clients and aggregates diagnostics.
type Manager struct {
	// projectRoot is the default directory for relative paths. Clients retain
	// their own workspace roots when the default directory changes.
	projectRoot  atomic.Pointer[string]
	boundaryMu   sync.Mutex
	boundaries   map[boundaryKey]boundaryEntry
	defaultRoots map[string]struct{}
	cfg          *config.Config
	broadcast    BroadcastFunc
	clients      map[clientKey]*Client
	clientsMu    sync.RWMutex

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
	startEra        context.Context
	cancelStartEra  context.CancelFunc
	maintenanceDone chan struct{}

	waiters   map[string][]chan diagnosticsEvent
	waitersMu sync.Mutex

	// lateWatches follows written paths after their diagnostics wait returned,
	// keyed by normalized path (see late_diagnostics.go). Guarded by lateMu,
	// which is never held while taking another Manager lock.
	lateMu      sync.Mutex
	lateWatches map[string]*lateWatch
	lateSeq     uint64
	// lateReported holds, per path, the findings already delivered as late
	// diagnostics this session, so a finding an edit merely moved or kept is
	// not delivered again.
	lateReported map[string]map[lateFindingKey]struct{}
	// lateChanged is closed (and cleared) when a watched path receives its
	// later same-version publish, waking AwaitLateDiagnostics.
	lateChanged chan struct{}

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

	// degradeMu guards degradeNotes, the per-session dedup set for model-facing
	// LSP degradation notes: losing a language server's diagnostics is worth one
	// line in a tool result, not one per failed edit.
	degradeMu    sync.Mutex
	degradeNotes map[string]struct{}

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
	projectRoot = normalizeWaiterPath(projectRoot)
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
	m.defaultRoots = map[string]struct{}{projectRoot: {}}
	m.projectRoot.Store(&projectRoot)
	return m
}

// projectRootPath returns the default directory for relative paths.
func (m *Manager) projectRootPath() string {
	if m == nil {
		return ""
	}
	if p := m.projectRoot.Load(); p != nil {
		return *p
	}
	return ""
}

// RebindToProjectRoot changes the default directory without discarding clients
// serving other checkouts. Workspace discovery remains local to each file.
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
	abs = normalizeWaiterPath(abs)
	m.boundaryMu.Lock()
	if m.defaultRoots == nil {
		m.defaultRoots = make(map[string]struct{})
	}
	m.defaultRoots[abs] = struct{}{}
	clear(m.boundaries)
	m.projectRoot.Store(&abs)
	m.boundaryMu.Unlock()
	m.notifySidebarChanged()
	return nil
}

func (m *Manager) onDiagnostics(key clientKey, origin *Client) func(uri string, _ string, diags []pnprotocol.Diagnostic, version int32) {
	return func(uri string, _ string, diags []pnprotocol.Diagnostic, version int32) {
		receivedAt := time.Now()
		// Serialize publication with diagnostic cleanup. A retired client must
		// not restore entries after a replacement or checkout removal.
		m.diagMu.Lock()
		if origin != nil {
			if !origin.acceptDiagnostics(uriToPath(uri), version) {
				origin.clearDiagnosticsForPath(uriToPath(uri))
				m.diagMu.Unlock()
				return
			}
			m.clientsMu.RLock()
			current := false
			m.forEachClientForPathLocked(uriToPath(uri), func(owner clientKey, client *Client) {
				if owner == key && client == origin {
					current = true
				}
			})
			m.clientsMu.RUnlock()
			if !current {
				m.diagMu.Unlock()
				return
			}
		}
		chordDiags := convertDiagnostics(diags)
		path := normalizeWaiterPath(uriToPath(uri))
		payload := DiagnosticsPayload{
			URI:         uri,
			ServerID:    key.name,
			Diagnostics: chordDiags,
		}

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
		m.noteLateDiagnosticsPublish(path, key.name, version, receivedAt)
		m.broadcast(TypeLSPDiagnostics, payload)
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
	path = normalizeWaiterPath(path)
	m.pruneMissingRoots(ctx)
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
			m.maintenanceDone = make(chan struct{})
			go m.maintainFiles(m.startEra, m.maintenanceDone)
		}
		m.starting[key] = true
		entryCtx, cancel := context.WithCancel(m.startEra)
		entry := &startEntry{ctx: entryCtx, cancel: cancel, done: make(chan struct{})}
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
		if entry.cancel != nil {
			entry.cancel()
		}
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

	client, err := newClient(ctx, key.name, srvCfg, key.root, m.settingsBoundary(key.root), false)
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
	client.SetOnDiagnostics(m.onDiagnostics(key, client))
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
	// Wait for the server to be ready before exposing it to the sidebar and the
	// post-write sync.
	// Avoids "gopls: not started" when the first call happens before init completes (see crush).
	const serverReadyTimeout = 15 * time.Second
	if err := client.WaitForServerReady(ctx, serverReadyTimeout); err != nil {
		log.Warnf("lsp: server not fully ready, continuing anyway name=%v root=%v error=%v", key.name, key.root, err)
		// Still add the client so later calls can succeed; first request may still fail briefly.
	}
	// Mark the newcomer as most-recently-used before it can be considered for
	// eviction: it was started to serve a file the caller is writing right now,
	// and an untouched client sorts as the least-recently-used one, so without
	// this the instance for the ninth root would be closed the instant it came
	// up and restarted on the next write.
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
	if _, err := os.Stat(key.root); os.IsNotExist(err) {
		return nil, nil, false
	}
	m.clients[key] = client
	closeMe, survivors = m.evictExcessClientsLocked()
	return closeMe, survivors, true
}

// discoverWorkspaceRoot walks toward the containing checkout or an explicitly
// bound default directory. It picks the nearest language marker and otherwise
// uses that boundary. External non-Git projects require a language marker.
//
// This is the single ancestor walk behind both "does this server cover the
// file" and "where must its client be rooted"; the two questions used to be
// answered by separate functions that stat'ed the same chain twice per call.
func (m *Manager) discoverWorkspaceRoot(name string, srvCfg config.LSPServerConfig, path string) (root string, markerMatched, ok bool) {
	absPath := normalizeWaiterPath(path)
	projectRoot := m.workspaceBoundary(absPath)
	markers := effectiveRootMarkers(name, srvCfg)
	if len(markers) == 0 {
		return projectRoot, false, projectRoot != ""
	}
	// Walk canonical parents up to the workspace boundary. External projects
	// without a boundary stop at the filesystem root.
	dir := filepath.Dir(absPath)
	// An exact root match means its parent already
	// lies outside the project. Start at the root so it is the first candidate
	// and the dir == projectRoot backstop can stop the walk there.
	if absPath == projectRoot {
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
	return projectRoot, false, projectRoot != ""
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
	affected := make(map[string]struct{})
	for name := range survivors {
		// A replacement may have started while an evicted client was closing.
		// Decide ownership from the current registry, not the removal snapshot.
		m.clientsMu.RLock()
		var live []*Client
		for key, c := range m.clients {
			if key.name == name {
				live = append(live, c)
			}
		}
		m.clientsMu.RUnlock()
		roots := make(map[string]struct{}, len(live))
		for _, c := range live {
			roots[c.cwd] = struct{}{}
		}
		for key, byURI := range m.diagByServer {
			if key.name != name {
				continue
			}
			if _, ok := roots[key.root]; ok {
				continue
			}
			for uri := range byURI {
				affected[normalizeWaiterPath(uriToPath(uri))] = struct{}{}
				cleared = append(cleared, clearedDiag{server: name, uri: uri})
			}
			delete(m.diagByServer, key)
		}
		for key, byPath := range m.publishedDiagByServer {
			if key.name != name {
				continue
			}
			if _, ok := roots[key.root]; ok {
				continue
			}
			for path := range byPath {
				affected[path] = struct{}{}
			}
			delete(m.publishedDiagByServer, key)
		}
		for path := range m.reviewByServer[name] {
			served := false
			for _, c := range live {
				if c.HandlesFile(path) {
					served = true
					break
				}
			}
			if !served {
				delete(m.reviewByServer[name], path)
				affected[path] = struct{}{}
			}
		}
		if len(m.reviewByServer[name]) == 0 {
			delete(m.reviewByServer, name)
		}
	}
	for path := range affected {
		if len(m.publishedDiagnosticIdentitiesLocked(path)) == 0 {
			delete(m.diagState, path)
			delete(m.reportedByPath, path)
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
	m.forEachClientForCanonicalPathLocked(normalizeWaiterPath(path), fn)
}

// File paths reaching this helper already have canonical filesystem identity.
// Do not resolve symlinks separately for every candidate server.
func (m *Manager) forEachClientForCanonicalPathLocked(path string, fn func(clientKey, *Client)) {
	boundary := m.workspaceBoundaryForDir(filepath.Dir(path))
	if len(m.clients) == 0 {
		return
	}
	var owners []clientKey
	for key, c := range m.clients {
		if !pathUnderDir(path, key.root) || !matchesFileType(c.cfg, path) || (boundary != "" && !pathUnderDir(key.root, boundary)) {
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
	if found != nil {
		found.touch(time.Now().UnixNano())
	}
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
		// path can be "starting for" it; otherwise a Go file would count a
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
		// starting would route the post-write sync to the old environment.
		// Once the launch settles (its client registers, or the launch ends
		// without one) the owner below is final and can be served; a failed
		// nearer launch falls back to the ancestor.
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

// DidOpen sends didOpen to the clients that own path; maintains version per client.
func (m *Manager) DidOpen(ctx context.Context, path string, content string) {
	if ctx == nil {
		ctx = context.Background()
	}
	m.forEachClientForPath(path, func(_ clientKey, c *Client) {
		_, _ = c.DidOpen(ctx, path, content)
	})
}

// DidChange sends didChange to all clients that handle path.
func (m *Manager) DidChange(ctx context.Context, path string, content string) {
	_, _ = m.DidChangeVersions(ctx, path, content)
}

// NotifyWatchedFileChanged sends workspace/didChangeWatchedFiles to the clients
// that own path. This keeps language-server project graphs in sync
// for file create/change/delete events, including newly created modules that are
// imported by other files. It returns the notification error of every failed
// server, keyed by server name.
func (m *Manager) NotifyWatchedFileChanged(ctx context.Context, path string, changeType pnprotocol.FileChangeType) map[string]error {
	if ctx == nil {
		ctx = context.Background()
	}
	path = normalizeWaiterPath(path)
	errs := make(map[string]error)
	m.forEachClientForPath(path, func(key clientKey, c *Client) {
		if err := c.NotifyWatchedFileChange(ctx, path, changeType); err != nil {
			errs[key.name] = err
		}
	})
	return errs
}

// DidChangeVersions sends didChange to the clients that own path and returns the
// document versions used by each server notification plus the notification error
// of every failed server, keyed by server name. The versions are used to
// ignore stale publishDiagnostics snapshots when servers include diagnostic
// versions. Keying by server name is safe because forEachClientForPathLocked
// yields at most one instance per server for a given path.
func (m *Manager) DidChangeVersions(ctx context.Context, path string, content string) (map[string]int32, map[string]error) {
	if ctx == nil {
		ctx = context.Background()
	}
	versions := make(map[string]int32)
	errs := make(map[string]error)
	m.forEachClientForPath(path, func(key clientKey, c *Client) {
		version, err := c.DidChange(ctx, path, content)
		if err != nil {
			errs[key.name] = err
			return
		}
		versions[key.name] = version
	})
	return versions, errs
}

// ResyncFile forwards content (the decoded file text, as write paths send it)
// to every running client that owns path and already has the document open,
// sending didChange only when the content differs from what that server last
// received. It never starts a server, so a read cannot trigger a launch and
// the workspace file watching that comes with it. Servers that never opened
// the file are skipped, because opening documents belongs to Chord's write
// paths and a server without the document has no stale copy to correct.
// Failures are logged, not returned, so a read never fails because
// language-server state could not be refreshed; a confirmed disconnect
// reclaims the instance and its orphaned diagnostics, and the next file write
// starts it again.
func (m *Manager) ResyncFile(ctx context.Context, path, content string) {
	if m == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	path = normalizeWaiterPath(path)
	disconnected := false
	m.forEachClientForPath(path, func(_ clientKey, c *Client) {
		if _, err := c.ResyncFileIfChanged(ctx, path, content); err != nil {
			log.Debugf("lsp: resync changed file failed path=%v name=%v error=%v", path, c.name, err)
			if !c.IsRunning() {
				disconnected = true
			}
		}
	})
	if disconnected {
		m.pruneExitedClientsForPath(ctx, path)
	}
}

// NotifyDidSave sends didSave to the clients that own path, skipping servers that
// did not declare save support during initialize. Returns the notification error
// of every failed server, keyed by server name; a failure is never fatal to the
// tool result.
func (m *Manager) NotifyDidSave(ctx context.Context, path string, content string) map[string]error {
	if ctx == nil {
		ctx = context.Background()
	}
	errs := make(map[string]error)
	m.forEachClientForPath(path, func(key clientKey, c *Client) {
		if err := c.NotifyDidSave(ctx, path, content); err != nil {
			errs[key.name] = err
		}
	})
	return errs
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
	m.clearFileState(path, true)
	return first
}

// Diagnostics returns aggregated diagnostics for the path from all clients.
func (m *Manager) Diagnostics(path string) []Diagnostic {
	m.clientsMu.RLock()
	defer m.clientsMu.RUnlock()
	var out []Diagnostic
	m.forEachClientForPathLocked(path, func(_ clientKey, c *Client) {
		out = append(out, convertDiagnostics(c.GetDiagnostics(path))...)
	})
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
	// A nil map accepts any server. A non-nil map limits the wait to the
	// listed servers, including when an event has no document version.
	serverVersions map[string]int32
	after          time.Time
	settle         time.Duration
	// immediateServers lists servers whose first publish for the synced
	// version is the one this wait can observe: gopls publishes its full
	// analysis only after diagnosticsDelay (1s by default), so a settle window
	// never catches it and only delays the result. The wait returns as soon as
	// every server in serverVersions has delivered a versioned, matching
	// publish and all of them are listed here; later same-version publishes
	// are picked up by the late-diagnostics watch instead.
	immediateServers map[string]bool
}

// immediateWaitComplete records ev and reports whether the wait may return
// without a settle window.
func (req diagnosticsWaitRequest) immediateWaitComplete(ev diagnosticsEvent, confirmed map[string]struct{}) bool {
	if len(req.serverVersions) == 0 || len(req.immediateServers) == 0 {
		return false
	}
	if ev.version != 0 && req.immediateServers[ev.serverID] {
		confirmed[ev.serverID] = struct{}{}
	}
	for name := range req.serverVersions {
		if _, ok := confirmed[name]; !ok {
			return false
		}
	}
	return true
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

	started := time.Now()
	var events, freshEvents int
	logReturn := func(reason string) {
		log.Debugf("lsp: diagnostics wait returned path=%v reason=%v elapsed=%v events=%v fresh_events=%v", path, reason, time.Since(started), events, freshEvents)
	}
	var gotFresh bool
	var last []Diagnostic
	var settle <-chan time.Time
	confirmed := make(map[string]struct{}, len(req.serverVersions))
	for {
		select {
		case ev := <-ch:
			events++
			if !diagnosticsEventFresh(ev, req) {
				continue
			}
			freshEvents++
			gotFresh = true
			last = ev.diagnostics
			if req.immediateWaitComplete(ev, confirmed) {
				logReturn("versioned")
				return last, true
			}
			settleTimer := time.NewTimer(req.settle)
			defer settleTimer.Stop()
			settle = settleTimer.C
		case <-settle:
			logReturn("settled")
			return last, true
		case <-ctx.Done():
			logReturn("cancelled")
			return nil, false
		case <-deadline.C:
			logReturn("timeout")
			return m.Diagnostics(path), gotFresh
		}
	}
}

func diagnosticsEventFresh(ev diagnosticsEvent, req diagnosticsWaitRequest) bool {
	if req.serverVersions != nil {
		want, ok := req.serverVersions[ev.serverID]
		if !ok || ev.version != 0 && ev.version != want {
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
	if resolved, ok := pathutil.ResolveSymlinksBestEffort(abs); ok {
		return resolved
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
	if ctx == nil {
		ctx = context.Background()
	}
	m.clientsMu.Lock()
	maintenanceDone := m.maintenanceDone
	m.maintenanceDone = nil
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

	if maintenanceDone != nil {
		select {
		case <-maintenanceDone:
		case <-ctx.Done():
		}
	}
	for _, c := range closing {
		if err := c.client.Close(ctx); err != nil {
			log.Warnf("lsp: stop client name=%v root=%v error=%v", c.key.name, c.key.root, err)
		}
	}

	survivors := make(map[string][]*Client)
	for _, c := range closing {
		survivors[c.key.name] = nil
	}
	for _, cleared := range m.dropOrphanedDiagnostics(survivors) {
		if m.broadcast != nil {
			m.broadcast(TypeLSPDiagnostics, DiagnosticsPayload{URI: cleared.uri, ServerID: cleared.server})
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
