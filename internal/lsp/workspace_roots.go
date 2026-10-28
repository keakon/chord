package lsp

import (
	"context"
	"os"
	"path/filepath"
	"time"
)

type boundaryKey struct {
	dir  string
	base string
}

type boundaryEntry struct {
	root    string
	expires time.Time
}

// rootCacheKey identifies one ancestor marker walk. Markers are part of the
// key because callers may pass an explicit server config that differs from
// the manager's configuration.
type rootCacheKey struct {
	name    string
	path    string
	markers string
}

type rootCacheEntry struct {
	root          string
	markerMatched bool
	ok            bool
	expires       time.Time
}

const (
	workspaceBoundaryTTL  = time.Second
	workspaceRootCacheTTL = time.Second
)

// clearRootCache drops memoized marker walks. Callers must not hold rootCacheMu.
func (m *Manager) clearRootCache() {
	m.rootCacheMu.Lock()
	clear(m.rootCache)
	m.rootCacheMu.Unlock()
}

// workspaceBoundary finds the checkout containing a file. A .git file is a
// boundary just like a .git directory, so linked worktrees and independent
// repositories use the same routing. The default directory also bounds
// discovery for projects without Git. No project configuration is loaded.
func (m *Manager) workspaceBoundary(path string) string {
	path = normalizeWaiterPath(path)
	dir := filepath.Dir(path)
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		dir = path
	}
	return m.workspaceBoundaryForDir(dir)
}

func (m *Manager) workspaceBoundaryForDir(dir string) string {
	key := boundaryKey{dir: dir, base: m.projectRootPath()}
	now := time.Now()
	m.boundaryMu.Lock()
	defer m.boundaryMu.Unlock()
	if entry, ok := m.boundaries[key]; ok && now.Before(entry.expires) {
		return entry.root
	}
	root := ""
	for {
		_, declared := m.defaultRoots[dir]
		if dir == key.base || declared || pathExists(filepath.Join(dir, ".git")) {
			root = dir
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	// Bound both memory and staleness: new/deleted checkout markers must be
	// noticed, and a long session must not retain every directory it visited.
	if m.boundaries == nil || len(m.boundaries) >= 256 {
		m.boundaries = make(map[boundaryKey]boundaryEntry)
	}
	m.boundaries[key] = boundaryEntry{root: root, expires: now.Add(workspaceBoundaryTTL)}
	return root
}

func (m *Manager) settingsBoundary(root string) string {
	if boundary := m.workspaceBoundary(root); boundary != "" {
		return boundary
	}
	return root
}

// pruneMissingRoots releases vanished workspaces and idle instances without
// open documents. It runs on tool access and background maintenance, never UI.
func (m *Manager) pruneMissingRoots(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}
	if ctx.Err() != nil {
		return
	}
	var closed []*Client
	var removedRoots []string
	survivors := make(map[string][]*Client)
	m.clientsMu.Lock()
	for key, entry := range m.launches {
		if _, err := os.Stat(key.root); os.IsNotExist(err) && entry.cancel != nil {
			entry.cancel()
		}
	}
	for key, client := range m.clients {
		_, err := os.Stat(key.root)
		missing := os.IsNotExist(err)
		client.openFilesMu.Lock()
		idle := len(client.openFiles) == 0 && client.lastUsed.Load() > 0 && time.Since(time.Unix(0, client.lastUsed.Load())) > workspaceIdleTimeout
		client.openFilesMu.Unlock()
		if !missing && !idle {
			continue
		}
		if missing {
			removedRoots = append(removedRoots, key.root)
		}
		delete(m.clients, key)
		closed = append(closed, client)
		survivors[key.name] = nil
	}
	for key, client := range m.clients {
		if _, ok := survivors[key.name]; ok {
			survivors[key.name] = append(survivors[key.name], client)
		}
	}
	m.clientsMu.Unlock()
	m.startFailMu.Lock()
	for key := range m.startFail {
		if _, err := os.Stat(key.root); os.IsNotExist(err) {
			delete(m.startFail, key)
		}
	}
	m.startFailMu.Unlock()
	for _, root := range removedRoots {
		for path := range m.touchedSnapshot() {
			if pathUnderDir(path, root) {
				m.clearFileState(path, true)
			}
		}
	}
	m.boundaryMu.Lock()
	for root := range m.defaultRoots {
		if root != m.projectRootPath() {
			if _, err := os.Stat(root); os.IsNotExist(err) {
				delete(m.defaultRoots, root)
			}
		}
	}
	if len(removedRoots) > 0 {
		clear(m.boundaries)
	}
	m.boundaryMu.Unlock()
	if len(closed) > 0 {
		// One shared budget bounds a batch, even when several servers stall.
		closeCtx, cancel := context.WithTimeout(ctx, workspaceCloseTimeout)
		for _, client := range closed {
			_ = client.Close(closeCtx)
		}
		cancel()
	}
	for _, cleared := range m.dropOrphanedDiagnostics(survivors) {
		m.broadcast(TypeLSPDiagnostics, DiagnosticsPayload{URI: cleared.uri, ServerID: cleared.server})
	}
	if len(closed) > 0 {
		m.notifySidebarChanged()
	}
}

// diagnosticsForPaths limits ancillary diagnostics to the instances owning
// this edit. Another checkout's errors must not appear as fallout from it.
func (m *Manager) diagnosticsForPaths(paths []string) map[string][]Diagnostic {
	merged, _ := m.diagnosticsForPathsGrouped(paths)
	return merged
}

// diagnosticsForPathsByServer keeps the publishing server's identity so late
// diagnostics can suppress a finding until every server that reports it has
// dropped it.
func (m *Manager) diagnosticsForPathsByServer(paths []string) map[string]map[string][]Diagnostic {
	_, byServer := m.diagnosticsForPathsGrouped(paths)
	return byServer
}

func (m *Manager) diagnosticsForPathsGrouped(paths []string) (map[string][]Diagnostic, map[string]map[string][]Diagnostic) {
	owners := make(map[clientKey]*Client)
	m.clientsMu.RLock()
	for _, path := range paths {
		m.forEachClientForPathLocked(path, func(key clientKey, c *Client) { owners[key] = c })
	}
	m.clientsMu.RUnlock()
	return m.diagnosticsByAbsPath(owners)
}
