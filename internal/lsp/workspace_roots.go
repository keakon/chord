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
	m.boundaries[key] = boundaryEntry{root: root, expires: now.Add(time.Second)}
	return root
}

func (m *Manager) settingsBoundary(root string) string {
	if boundary := m.workspaceBoundary(root); boundary != "" {
		return boundary
	}
	return root
}

// pruneMissingRoots releases only vanished workspaces. It runs on tool access,
// never on the UI loop; session idle unloading and the per-server LRU cap also
// reclaim clients without adding another background watcher.
func (m *Manager) pruneMissingRoots(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}
	var closed []*Client
	survivors := make(map[string][]*Client)
	m.clientsMu.Lock()
	for key, entry := range m.launches {
		if _, err := os.Stat(key.root); os.IsNotExist(err) && entry.cancel != nil {
			entry.cancel()
		}
	}
	for key, client := range m.clients {
		if _, err := os.Stat(key.root); !os.IsNotExist(err) {
			continue
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
	for _, client := range closed {
		_ = client.Close(ctx)
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
	byPath := m.allDiagnosticsByAbsPath()
	owners := make(map[clientKey]*Client)
	m.clientsMu.RLock()
	defer m.clientsMu.RUnlock()
	for _, path := range paths {
		m.forEachClientForPathLocked(path, func(key clientKey, c *Client) { owners[key] = c })
	}
	for path := range byPath {
		covered := false
		m.forEachClientForPathLocked(path, func(key clientKey, _ *Client) {
			if _, ok := owners[key]; ok {
				covered = true
			}
		})
		if !covered {
			delete(byPath, path)
		}
	}
	return byPath
}
