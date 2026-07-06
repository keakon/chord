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
	serverID    string
	version     int32
	receivedAt  time.Time
}

// Manager manages multiple LSP clients and aggregates diagnostics.
type Manager struct {
	projectRoot string
	cfg         *config.Config
	broadcast   BroadcastFunc
	clients     map[string]*Client
	clientsMu   sync.RWMutex
	starting    map[string]bool // servers currently being initialized (guarded by clientsMu)

	waiters   map[string][]chan diagnosticsEvent
	waitersMu sync.Mutex

	startFailMu sync.Mutex
	startFail   map[string]string // server name -> last start/init error

	// diagByServer tracks the latest diagnostics per server+URI for tool output,
	// broadcasts, and file-scoped review snapshots.
	diagMu         sync.RWMutex
	diagByServer   map[string]map[string]diagCounts
	reviewByServer map[string]map[string]reviewCounts

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

// NewManager creates a manager. broadcast is called when diagnostics are received.
// If cfg.LSP is nil or empty, no LSPs are started.
func NewManager(cfg *config.Config, projectRoot string, broadcast BroadcastFunc) *Manager {
	if broadcast == nil {
		broadcast = func(string, any) {}
	}
	return &Manager{
		projectRoot:    projectRoot,
		cfg:            cfg,
		broadcast:      broadcast,
		clients:        make(map[string]*Client),
		starting:       make(map[string]bool),
		waiters:        make(map[string][]chan diagnosticsEvent),
		startFail:      make(map[string]string),
		diagByServer:   make(map[string]map[string]diagCounts),
		reviewByServer: make(map[string]map[string]reviewCounts),
		touchedPaths:   make(map[string]struct{}),
	}
}

func (m *Manager) onDiagnostics(serverID string) func(uri string, _ string, diags []pnprotocol.Diagnostic, version int32) {
	return func(uri string, _ string, diags []pnprotocol.Diagnostic, version int32) {
		receivedAt := time.Now()
		chordDiags := convertDiagnostics(diags)
		path := normalizeWaiterPath(uriToPath(uri))
		payload := DiagnosticsPayload{
			URI:         uri,
			ServerID:    serverID,
			Diagnostics: chordDiags,
		}
		m.broadcast(TypeLSPDiagnostics, payload)

		// Track diagnostics for SidebarEntries().
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
			m.diagByServer = make(map[string]map[string]diagCounts)
		}
		if errs == 0 && warns == 0 {
			if byURI, ok := m.diagByServer[serverID]; ok {
				delete(byURI, uri)
				if len(byURI) == 0 {
					delete(m.diagByServer, serverID)
				}
			}
		} else {
			byURI := m.diagByServer[serverID]
			if byURI == nil {
				byURI = make(map[string]diagCounts)
				m.diagByServer[serverID] = byURI
			}
			byURI[uri] = diagCounts{errors: errs, warnings: warns}
		}
		m.diagMu.Unlock()
		m.notifySidebarChanged()

		m.waitersMu.Lock()
		for _, ch := range m.waiters[path] {
			select {
			case ch <- diagnosticsEvent{diagnostics: chordDiags, serverID: serverID, version: version, receivedAt: receivedAt}:
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
// Each server is initialized in its own goroutine so the write lock is not held
// during the potentially slow subprocess launch + LSP handshake.
func (m *Manager) Start(ctx context.Context, path string) {
	if m.cfg == nil || len(m.cfg.LSP) == 0 {
		return
	}
	if !pathUnderDir(path, m.projectRoot) {
		return
	}
	m.clientsMu.Lock()
	var toStart []struct {
		name string
		cfg  config.LSPServerConfig
	}
	for name, srvCfg := range m.cfg.LSP {
		if srvCfg.Disabled {
			continue
		}
		if !m.handles(srvCfg, path) {
			continue
		}
		if _, ok := m.clients[name]; ok {
			continue
		}
		if m.starting[name] {
			continue
		}
		m.starting[name] = true
		toStart = append(toStart, struct {
			name string
			cfg  config.LSPServerConfig
		}{name, srvCfg})
	}
	m.clientsMu.Unlock()

	for _, s := range toStart {
		name, srvCfg := s.name, s.cfg
		go m.startServer(ctx, name, srvCfg)
	}
}

func (m *Manager) startServer(ctx context.Context, name string, srvCfg config.LSPServerConfig) {
	m.startFailMu.Lock()
	if m.startFail == nil {
		m.startFail = make(map[string]string)
	}
	delete(m.startFail, name)
	m.startFailMu.Unlock()

	client, err := NewClient(ctx, name, srvCfg, m.projectRoot, false)
	if err != nil {
		log.Errorf("lsp: create client name=%v error=%v", name, err)
		m.startFailMu.Lock()
		m.startFail[name] = err.Error()
		m.startFailMu.Unlock()
		m.clientsMu.Lock()
		delete(m.starting, name)
		m.clientsMu.Unlock()
		m.notifySidebarChanged()
		return
	}
	client.SetOnDiagnostics(m.onDiagnostics(name))
	if err := client.Initialize(ctx); err != nil {
		log.Errorf("lsp: initialize client name=%v error=%v", name, err)
		_ = client.Close(ctx)
		m.startFailMu.Lock()
		m.startFail[name] = err.Error()
		m.startFailMu.Unlock()
		m.clientsMu.Lock()
		delete(m.starting, name)
		m.clientsMu.Unlock()
		m.notifySidebarChanged()
		return
	}
	// Wait for the server to be ready before exposing it (sidebar green + ClientForPath).
	// Avoids "gopls: not started" when the first call happens before init completes (see crush).
	const serverReadyTimeout = 15 * time.Second
	if err := client.WaitForServerReady(ctx, serverReadyTimeout); err != nil {
		log.Warnf("lsp: server not fully ready, continuing anyway name=%v error=%v", name, err)
		// Still add the client so later calls can succeed; first request may still fail briefly.
	}
	m.clientsMu.Lock()
	m.clients[name] = client
	delete(m.starting, name)
	m.clientsMu.Unlock()
	m.notifySidebarChanged()
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
	for n := range m.clients {
		connected[n] = true
	}
	m.clientsMu.RUnlock()

	m.startFailMu.Lock()
	failMap := make(map[string]string, len(m.startFail))
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
		} else if msg := failMap[name]; msg != "" {
			e := msg
			if len(e) > 120 {
				e = e[:117] + "..."
			}
			entry.Error = e
		} else {
			entry.Pending = true
		}
		out = append(out, entry)
	}
	return out
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
	names := make([]string, 0, len(m.clients))
	for name := range m.clients {
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

func (m *Manager) handles(srvCfg config.LSPServerConfig, path string) bool {
	if len(srvCfg.FileTypes) > 0 {
		ext := strings.ToLower(filepath.Ext(path))
		matched := false
		for _, ft := range srvCfg.FileTypes {
			e := strings.ToLower(ft)
			if e != "" && e[0] != '.' {
				e = "." + e
			}
			if ext == e {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	if len(srvCfg.RootMarkers) > 0 {
		// Check if path is under a directory containing any root marker
		dir := filepath.Dir(path)
		for {
			for _, marker := range srvCfg.RootMarkers {
				if pathExists(filepath.Join(dir, marker)) {
					return true
				}
			}
			if dir == m.projectRoot || len(dir) <= len(m.projectRoot) {
				break
			}
			dir = filepath.Dir(dir)
		}
		return false // loop above already checked projectRoot
	}
	return true
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

// clientForPathLocked returns a client that handles path; caller must hold at least RLock.
func (m *Manager) clientForPathLocked(path string) (*Client, bool) {
	for _, c := range m.clients {
		if c.HandlesFile(path) {
			return c, true
		}
	}
	return nil, false
}

// hasPendingStartForPathLocked reports whether any matching server is still starting.
// Caller must hold at least clientsMu.RLock().
func (m *Manager) hasPendingStartForPathLocked(path string) bool {
	if m.cfg == nil || len(m.cfg.LSP) == 0 {
		return false
	}
	for name := range m.starting {
		srvCfg, ok := m.cfg.LSP[name]
		if !ok || srvCfg.Disabled {
			continue
		}
		if m.handles(srvCfg, path) {
			return true
		}
	}
	return false
}

func (m *Manager) waitForClientForPath(ctx context.Context, path string, timeout time.Duration) (*Client, bool) {
	if !m.anyServerMatchesPath(path) {
		return nil, false
	}

	m.Start(ctx, path)

	check := func() (*Client, bool, bool) {
		m.clientsMu.RLock()
		defer m.clientsMu.RUnlock()
		c, ok := m.clientForPathLocked(path)
		if ok {
			return c, true, false
		}
		return nil, false, m.hasPendingStartForPathLocked(path)
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

// DidOpen sends didOpen to all clients that handle path; maintains version per client.
func (m *Manager) DidOpen(ctx context.Context, path string, content string) {
	if ctx == nil {
		ctx = context.Background()
	}
	m.clientsMu.RLock()
	defer m.clientsMu.RUnlock()
	for _, c := range m.clients {
		if c.HandlesFile(path) {
			_, _ = c.DidOpen(ctx, path, content)
		}
	}
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

// NotifyWatchedFileChanged sends workspace/didChangeWatchedFiles to all clients
// that handle path. This keeps language-server project graphs in sync
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
	for _, c := range m.clients {
		if !c.HandlesFile(path) {
			continue
		}
		if err := c.NotifyWatchedFileChange(ctx, path, changeType); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// DidChangeVersions sends didChange to all matching clients and returns the
// document versions used by each server notification. The versions are used to
// ignore stale publishDiagnostics snapshots when servers include diagnostic
// versions.
func (m *Manager) DidChangeVersions(ctx context.Context, path string, content string) (map[string]int32, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	m.clientsMu.RLock()
	defer m.clientsMu.RUnlock()
	versions := make(map[string]int32)
	var first error
	for name, c := range m.clients {
		if c.HandlesFile(path) {
			version, err := c.DidChange(ctx, path, content)
			if err == nil {
				versions[name] = version
			} else if first == nil {
				first = err
			}
		}
	}
	return versions, first
}

// DidClose sends didClose to all clients that handle path, clears cached diagnostics for that path,
// and refreshes sidebar/server counts.
func (m *Manager) DidClose(ctx context.Context, path string) {
	_ = m.DidCloseErr(ctx, path)
}

// DidCloseErr is like DidClose but returns the first notify error from any client.
func (m *Manager) DidCloseErr(ctx context.Context, path string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	path = normalizeWaiterPath(path)
	m.clientsMu.RLock()
	defer m.clientsMu.RUnlock()
	var first error
	var changed bool
	for name, c := range m.clients {
		if !c.HandlesFile(path) {
			continue
		}
		if err := c.DidClose(ctx, path); err != nil && first == nil {
			first = err
		}
		c.clearDiagnosticsForPath(path)
		uri := string(pnprotocol.URIFromPath(path))
		m.diagMu.Lock()
		if byURI, ok := m.diagByServer[name]; ok {
			if _, exists := byURI[uri]; exists {
				delete(byURI, uri)
				changed = true
				if len(byURI) == 0 {
					delete(m.diagByServer, name)
				}
			}
		}
		m.diagMu.Unlock()
		if m.broadcast != nil {
			m.broadcast(TypeLSPDiagnostics, DiagnosticsPayload{URI: uri, ServerID: name, Diagnostics: nil})
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

// Stop shuts down all LSP clients.
func (m *Manager) Stop(ctx context.Context) {
	m.clientsMu.Lock()
	defer m.clientsMu.Unlock()
	for name, c := range m.clients {
		if err := c.Close(ctx); err != nil {
			log.Warnf("lsp: stop client name=%v error=%v", name, err)
		}
		delete(m.clients, name)
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
