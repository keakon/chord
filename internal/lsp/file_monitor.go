package lsp

import (
	"context"
	"crypto/sha256"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/keakon/golog/log"
)

const (
	fileMaintenanceInterval = 5 * time.Second
	fileEventDelay          = 150 * time.Millisecond
	maxOpenFilesPerClient   = 256
	openFileIdleTimeout     = 10 * time.Minute
	workspaceIdleTimeout    = 20 * time.Minute
	maxMonitorDirectories   = 1024
	fileReconcileBatch      = 256
	fileDigestBudget        = 16 << 20
	workspaceCloseTimeout   = time.Second
)

type monitoredFile struct {
	client      *Client
	snapshot    syncedFile
	digest      [sha256.Size]byte
	digestKnown bool
}

// maintainFiles owns its watcher, snapshots and timers. It watches containing
// directories so atomic replacement does not detach a file watch. Polling is
// bounded and remains available when watches overflow or cannot be installed.
// Its lifetime is exactly one manager start era; Stop cancels and joins it.
func (m *Manager) maintainFiles(ctx context.Context, done chan struct{}) {
	defer close(done)
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Debugf("lsp: file watcher unavailable: %v", err)
	}
	var events <-chan fsnotify.Event
	var failures <-chan error
	if watcher != nil {
		defer watcher.Close()
		events, failures = watcher.Events, watcher.Errors
	}
	tick := time.NewTicker(fileMaintenanceInterval)
	defer tick.Stop()
	timer := time.NewTimer(fileEventDelay)
	defer timer.Stop()
	timer.Stop()
	var flush <-chan time.Time
	dirty := make(map[string]fsnotify.Op)
	observed := make(map[string]os.FileInfo)
	watching := make(map[string]struct{})
	cursor := 0
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			if event.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Remove|fsnotify.Rename) == 0 {
				continue
			}
			// Do not resolve a vanished symlink to a different identity.
			if len(dirty) < fileReconcileBatch {
				dirty[filepath.Clean(event.Name)] |= event.Op
			}
			if flush == nil {
				timer.Reset(fileEventDelay)
				flush = timer.C
			}
		case _, ok := <-failures:
			if !ok {
				failures = nil
				continue
			}
			// The periodic pass reconciles missed events; no user-facing warning.
		case <-flush:
			flush = nil
			m.notifyWorkspaceEvents(ctx, dirty)
			m.reconcileFiles(ctx, observed, dirty, &cursor, false)
			clear(dirty)
		case <-tick.C:
			m.reconcileFiles(ctx, observed, nil, &cursor, true)
			if ctx.Err() != nil {
				return
			}
			m.pruneMissingRoots(ctx)
			if watcher != nil {
				// fsnotify removes watches when a directory disappears. Re-read
				// the actual set so a recreated directory gets registered again.
				clear(watching)
				for _, dir := range watcher.WatchList() {
					watching[dir] = struct{}{}
				}
				wanted := make(map[string]struct{})
				m.clientsMu.RLock()
				for key := range m.clients {
					wanted[key.root] = struct{}{}
				}
				m.clientsMu.RUnlock()
				for path := range observed {
					if len(wanted) >= maxMonitorDirectories {
						break
					}
					wanted[filepath.Dir(path)] = struct{}{}
				}
				for dir := range watching {
					if _, ok := wanted[dir]; !ok {
						_ = watcher.Remove(dir)
						delete(watching, dir)
					}
				}
				for dir := range wanted {
					if _, ok := watching[dir]; !ok {
						if watcher.Add(dir) == nil {
							watching[dir] = struct{}{}
						}
					}
				}
			}
		}
	}
}

func (m *Manager) monitoredFiles() map[string][]monitoredFile {
	files := make(map[string][]monitoredFile)
	m.clientsMu.RLock()
	clients := make([]*Client, 0, len(m.clients))
	for _, c := range m.clients {
		clients = append(clients, c)
	}
	m.clientsMu.RUnlock()
	for _, c := range clients {
		c.openFilesMu.Lock()
		for path, state := range c.syncedFiles {
			digest, known := c.syncedDigest[path]
			files[path] = append(files[path], monitoredFile{client: c, snapshot: state, digest: digest, digestKnown: known})
		}
		c.openFilesMu.Unlock()
		c.diagnosticsMu.RLock()
		for uri := range c.diagnostics {
			path := uriToPath(string(uri))
			if _, ok := files[path]; !ok {
				files[path] = nil
			}
		}
		c.diagnosticsMu.RUnlock()
	}
	// Keep monitoring reviewed clean files after their overlays close. Otherwise
	// a clean publish would remove the last cache entry before a later deletion.
	for path := range m.touchedSnapshot() {
		for _, c := range clients {
			if pathUnderDir(path, c.cwd) && matchesFileType(c.cfg, path) {
				if _, ok := files[path]; !ok {
					files[path] = nil
				}
				break
			}
		}
	}
	return files
}

func (m *Manager) reconcileFiles(ctx context.Context, observed map[string]os.FileInfo, dirty map[string]fsnotify.Op, cursor *int, periodic bool) {
	files := m.monitoredFiles()
	for path := range observed {
		if _, ok := files[path]; !ok {
			delete(observed, path)
		}
	}
	paths := make([]string, 0, len(files))
	for path := range files {
		paths = append(paths, path)
	}
	slices.Sort(paths)
	if len(paths) == 0 {
		return
	}
	selected := make(map[string]struct{})
	// Capacity eviction selects the oldest documents, not lexical file order.
	excess := make(map[*Client]map[string]struct{})
	byClient := make(map[*Client][]string)
	for path, entries := range files {
		for _, entry := range entries {
			byClient[entry.client] = append(byClient[entry.client], path)
		}
	}
	for client, candidates := range byClient {
		if len(candidates) <= maxOpenFilesPerClient {
			continue
		}
		used := func(path string) time.Time {
			for _, e := range files[path] {
				if e.client == client {
					return e.snapshot.used
				}
			}
			return time.Time{}
		}
		slices.SortFunc(candidates, func(a, b string) int { return used(a).Compare(used(b)) })
		excess[client] = make(map[string]struct{})
		for _, path := range candidates[:len(candidates)-maxOpenFilesPerClient] {
			excess[client][path] = struct{}{}
		}
	}
	if periodic {
		for i := 0; i < min(len(paths), fileReconcileBatch); i++ {
			selected[paths[(*cursor+i)%len(paths)]] = struct{}{}
		}
		*cursor = (*cursor + min(len(paths), fileReconcileBatch)) % len(paths)
	} else {
		for path := range dirty {
			if _, ok := files[path]; ok {
				selected[path] = struct{}{}
			}
		}
	}
	now := time.Now()
	digestBudget := int64(fileDigestBudget)
	for path := range selected {
		if ctx.Err() != nil {
			return
		}
		info, err := os.Stat(path)
		deleted := os.IsNotExist(err)
		if err != nil && !deleted {
			continue
		} // Permission/IO failures are not deletion.
		previous, known := observed[path]
		entries := files[path]
		changed := deleted || (known && !sameDiskFile(previous, info))
		if !known && info != nil {
			m.diagMu.RLock()
			state, tracked := m.diagState[path]
			m.diagMu.RUnlock()
			if tracked && !state.mtime.Equal(info.ModTime()) {
				changed = true
			}
		}
		for _, entry := range entries {
			if !sameDiskFile(entry.snapshot.info, info) {
				changed = true
			}
		}
		// A write notification is evidence even when size and timestamps match.
		// Hash once per path and compare the same snapshot used for release.
		if !changed && dirty[path]&(fsnotify.Write|fsnotify.Create) != 0 {
			if len(entries) == 0 {
				changed = true
			} else {
				digest, verified := monitoredDiskDigest(path, info, &digestBudget)
				for _, entry := range entries {
					if !verified || !entry.digestKnown || digest != entry.digest {
						changed = true
					}
				}
			}
		}
		if changed {
			reason := overlayReleaseChanged
			if deleted {
				reason = overlayReleaseDeleted
			}
			for _, entry := range entries {
				m.releaseOverlay(ctx, entry.client, path, entry.snapshot, reason)
			}
			if len(entries) == 0 {
				m.clearCachedFile(ctx, path, deleted)
			}
		} else {
			for _, entry := range entries {
				_, overLimit := excess[entry.client][path]
				if now.Sub(entry.snapshot.used) > openFileIdleTimeout || overLimit {
					m.releaseOverlay(ctx, entry.client, path, entry.snapshot, overlayReleaseEvicted)
				}
			}
		}
		if deleted {
			delete(observed, path)
		} else {
			observed[path] = info
		}
	}
}

func (m *Manager) clearCachedFile(ctx context.Context, path string, deleted bool) {
	if ctx.Err() != nil {
		return
	}
	m.clientsMu.RLock()
	clients := make([]*Client, 0, len(m.clients))
	for _, c := range m.clients {
		clients = append(clients, c)
	}
	m.clientsMu.RUnlock()
	// A tool may have opened the file since monitoredFiles took its snapshot.
	// Serialize the cache-only cleanup against document notifications as well.
	slices.SortFunc(clients, func(a, b *Client) int {
		if n := strings.Compare(a.name, b.name); n != 0 {
			return n
		}
		return strings.Compare(a.cwd, b.cwd)
	})
	for _, c := range clients {
		c.lifecycleMu.Lock()
	}
	cleared := func() bool {
		defer func() {
			for _, client := range slices.Backward(clients) {
				client.lifecycleMu.Unlock()
			}
		}()
		if ctx.Err() != nil {
			return false
		}
		if deleted {
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				return false
			}
		}
		for _, c := range clients {
			c.openFilesMu.Lock()
			_, open := c.openFiles[path]
			c.openFilesMu.Unlock()
			if open {
				return false
			}
		}
		for _, c := range clients {
			c.clearDiagnosticsForPath(path)
		}
		m.clearFileState(path, deleted)
		return true
	}()
	if !cleared {
		return
	}
	change := WatchedFileChanged
	if deleted {
		change = WatchedFileDeleted
	}
	_ = m.NotifyWatchedFileChanged(ctx, path, change)
}

// Directory events also notify project-graph changes for files without an open
// overlay. Markers invalidate discovery; they never load another Chord config.
func (m *Manager) notifyWorkspaceEvents(ctx context.Context, paths map[string]fsnotify.Op) {
	m.clientsMu.RLock()
	clients := make([]*Client, 0, len(m.clients))
	for _, c := range m.clients {
		clients = append(clients, c)
	}
	m.clientsMu.RUnlock()
	for path, op := range paths {
		if ctx.Err() != nil {
			return
		}
		change := WatchedFileChanged
		if op&fsnotify.Create != 0 {
			change = WatchedFileCreated
		}
		if _, err := os.Stat(path); os.IsNotExist(err) {
			change = WatchedFileDeleted
		}
		for _, c := range clients {
			c.openFilesMu.Lock()
			_, open := c.openFiles[path]
			c.openFilesMu.Unlock()
			if open {
				continue
			} // Overlay reconciliation sends its own notification.
			marker := filepath.Base(path) == ".git"
			for _, name := range effectiveRootMarkers(c.name, c.cfg) {
				if filepath.Base(path) == name {
					marker = true
					break
				}
			}
			if marker {
				m.boundaryMu.Lock()
				clear(m.boundaries)
				m.boundaryMu.Unlock()
			}
			if pathUnderDir(path, c.cwd) && (marker || matchesFileType(c.cfg, path)) {
				_ = c.NotifyWatchedFileChange(ctx, path, change)
			}
		}
	}
}

// When verification exceeds the batch IO budget or fails, give ownership back
// to the server's disk reader instead of retaining an unverified overlay.
func monitoredDiskDigest(path string, info os.FileInfo, budget *int64) ([sha256.Size]byte, bool) {
	var digest [sha256.Size]byte
	if info == nil || !info.Mode().IsRegular() || info.Size() >= *budget {
		return digest, false
	}
	file, err := os.Open(path)
	if err != nil {
		return digest, false
	}
	defer file.Close()
	hash := sha256.New()
	limit := info.Size() + 1
	n, err := io.Copy(hash, io.LimitReader(file, limit))
	*budget -= n
	if err != nil || n != info.Size() {
		return digest, false
	}
	after, err := file.Stat()
	if err != nil || !sameDiskFile(info, after) {
		return digest, false
	}
	copy(digest[:], hash.Sum(nil))
	return digest, true
}
