package lsp

import (
	"context"
	"maps"
	"os"
	"sort"
	"time"

	"github.com/keakon/golog/log"
)

// clientAdmittedWithin reports whether any client currently owning path was
// admitted within d. Clients registered without going through a launch
// (tests, direct seeding) have no admission time and count as warm: the
// cold-start budget exists to wait out a first workspace load that was just
// kicked off, and a client nobody launched here is not doing that.
func (m *Manager) clientAdmittedWithin(path string, d time.Duration) bool {
	now := time.Now()
	m.clientsMu.RLock()
	defer m.clientsMu.RUnlock()
	within := false
	m.forEachClientForPathLocked(path, func(key clientKey, _ *Client) {
		if within {
			return
		}
		if admitted, ok := m.admittedAt[key]; ok && now.Sub(admitted) <= d {
			within = true
		}
	})
	return within
}

func (m *Manager) hasStartingOwner(path string) bool {
	m.clientsMu.RLock()
	defer m.clientsMu.RUnlock()
	for key := range m.starting {
		if m.clients[key] != nil {
			continue
		}
		if cfg, ok := m.cfg.LSP[key.name]; ok {
			if root, ok := m.serverRootForPath(key.name, cfg, path); ok && root == key.root {
				return true
			}
		}
	}
	return false
}

// pendingSync is one write whose language-server client did not appear in
// time. It is re-synchronized when the client is admitted.
type pendingSync struct {
	content       string
	changeType    WatchedFileChangeType
	mtime         time.Time
	writerAgentID string
	owners        map[clientKey]pendingSyncOwner
	id            *pendingSyncID
}

// A nonzero allocation gives registrations distinct pointer identities.
type pendingSyncID struct{ _ byte }

type pendingSyncOwner struct {
	client  *Client
	version int32
}

// registerPendingSync parks a write for re-synchronization at client
// admission. The file's mtime is captured now: an external change before the
// re-sync runs means the write no longer describes the disk and the entry is
// dropped instead of resurrecting stale content.
func (m *Manager) registerPendingSync(path, content string, changeType WatchedFileChangeType, writerAgentID string) {
	m.registerPendingSyncs([]FileWrite{{Path: path, Content: content, ChangeType: changeType}}, writerAgentID)
}

func (m *Manager) registerPendingSyncs(writes []FileWrite, writerAgentID string) {
	if m == nil {
		return
	}
	// Admission and failed-launch cleanup take clientsMu before pendingSyncMu
	// too. Registration either joins a pending launch or sees its settled
	// registry, so neither readiness nor failure can slip past a late entry.
	m.clientsMu.Lock()
	m.pendingSyncMu.Lock()
	if m.pendingSyncs == nil {
		m.pendingSyncs = make(map[string]pendingSync)
	}
	ready := false
	for _, write := range writes {
		path := normalizeWaiterPath(write.Path)
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		covered := false
		owners := make(map[clientKey]pendingSyncOwner)
		m.forEachClientForPathLocked(path, func(key clientKey, c *Client) {
			covered = true
			ready = true
			owners[key] = pendingSyncOwner{client: c, version: c.syncedDocumentVersion(path)}
		})
		for key := range m.starting {
			if _, owned := owners[key]; owned {
				continue
			}
			if entry := m.launches[key]; entry == nil || entry.ctx.Err() != nil {
				continue
			}
			if srvCfg, ok := m.cfg.LSP[key.name]; ok {
				if root, ok := m.serverRootForPath(key.name, srvCfg, path); ok && root == key.root {
					covered = true
					owners[key] = pendingSyncOwner{}
				}
			}
		}
		if covered {
			m.pendingSyncs[path] = pendingSync{content: write.Content, changeType: write.ChangeType, mtime: info.ModTime(), writerAgentID: writerAgentID, owners: owners, id: &pendingSyncID{}}
		}
	}
	m.pendingSyncMu.Unlock()
	if ready {
		m.startPendingResyncLocked()
	}
	m.clientsMu.Unlock()
}

// takePendingSync removes the parked write at path only when it still holds
// exactly the entry the caller saw: a write that arrived in between replaced
// the entry and will register or sync on its own.
func (m *Manager) takePendingSync(path string, entry pendingSync) bool {
	m.pendingSyncMu.Lock()
	defer m.pendingSyncMu.Unlock()
	current, ok := m.pendingSyncs[path]
	if !ok || current.id != entry.id {
		return false
	}
	delete(m.pendingSyncs, path)
	return true
}

// clearPendingSyncsForLaunch drops the parked writes the failed launch was
// going to serve. Its admission will never happen, so the entries would
// outlive any re-sync; the next write to those paths registers fresh ones.
func (m *Manager) clearPendingSyncsForLaunch(key clientKey) {
	if m == nil || m.cfg == nil {
		return
	}
	m.pendingSyncMu.Lock()
	for path, entry := range m.pendingSyncs {
		delete(entry.owners, key)
		if len(entry.owners) == 0 {
			delete(m.pendingSyncs, path)
		}
	}
	m.pendingSyncMu.Unlock()
	m.startPendingResyncLocked()
}

func (m *Manager) startPendingResyncLocked() {
	m.pendingSyncMu.Lock()
	pending := len(m.pendingSyncs) > 0
	m.pendingSyncMu.Unlock()
	if !pending {
		return
	}
	ctx, _, finish := m.newSyncTaskLocked(context.Background())
	go func() {
		defer finish()
		m.resyncPendingWrites(ctx)
	}()
}

// resyncPendingWrites re-synchronizes parked writes whose owners have all
// settled. Each path re-runs the
// after-write notification sequence and registers a late-diagnostics watch
// carrying its sync token, so the analysis the server produces lands in the
// next request. The original tool results are not revised: their notes
// already say the diagnostics follow once the server is up.
func (m *Manager) resyncPendingWrites(ctx context.Context) {
	if m == nil || m.cfg == nil {
		return
	}
	m.pendingResyncMu.Lock()
	defer m.pendingResyncMu.Unlock()
	if ctx.Err() != nil {
		return
	}
	m.clientsMu.RLock()
	m.pendingSyncMu.Lock()
	paths := make([]string, 0, len(m.pendingSyncs))
	entries := make(map[string]pendingSync, len(m.pendingSyncs))
	for path, entry := range m.pendingSyncs {
		ready := len(entry.owners) > 0
		for owner := range entry.owners {
			if m.clients[owner] == nil {
				ready = false
				break
			}
		}
		if ready {
			entry.owners = clonePendingOwners(entry.owners)
			for owner, state := range entry.owners {
				if state.client != m.clients[owner] {
					state.client = m.clients[owner]
					state.version = 0
					entry.owners[owner] = state
				}
			}
			paths = append(paths, path)
			entries[path] = entry
		}
	}
	m.pendingSyncMu.Unlock()
	m.clientsMu.RUnlock()
	if len(paths) == 0 {
		return
	}
	sort.Strings(paths)

	type pendingWatch struct {
		path     string
		entry    pendingSync
		sync     afterWriteSync
		afterSeq uint64
	}
	watches := make([]pendingWatch, 0, len(paths))
	for _, path := range paths {
		if ctx.Err() != nil {
			return
		}
		entry := entries[path]
		if !m.takePendingSync(path, entry) {
			continue
		}
		if info, err := os.Stat(path); err != nil || !info.ModTime().Equal(entry.mtime) {
			continue
		}
		// The sync has no waiter consuming publishes. Replay everything that
		// arrives while it sends notifications, including a fast first pass.
		afterSeq := m.currentLatePublishSeq()
		began := time.Now()
		sync := m.resyncAfterWait(ctx, path, entry.content, entry.changeType, entry.owners)
		log.Debugf("lsp: pending write resync path=%v confirmed=%v cancelled=%v sync_ms=%v servers=%v failed_servers=%v", path, sync.token.confirmable, ctx.Err() != nil, time.Since(began).Milliseconds(), len(sync.serverVersions), len(sync.serverErrors))
		if sync.token.confirmable && len(sync.serverVersions) > 0 {
			watches = append(watches, pendingWatch{path: path, entry: entry, sync: sync, afterSeq: afterSeq})
		}
	}
	if ctx.Err() != nil {
		return
	}
	generations := make(map[clientKey]uint64)
	for _, watch := range watches {
		maps.Copy(generations, watch.sync.generations)
	}
	for _, watch := range watches {
		watchGenerations := batchWatchGenerations(watch.sync.generations, generations)
		m.watchLateDiagnostics(watch.path, watch.sync.serverVersions, watchGenerations, nil, watch.afterSeq, watch.entry.writerAgentID, watch.sync.token)
	}
}

func clonePendingOwners(owners map[clientKey]pendingSyncOwner) map[clientKey]pendingSyncOwner {
	return maps.Clone(owners)
}

// lateSyncGenerationOf returns a client's current sync sequence, the value a
// watch must carry so its registration survives the client-generation check.
func (m *Manager) lateSyncGenerationOf(key clientKey) uint64 {
	m.lateMu.Lock()
	defer m.lateMu.Unlock()
	return m.lateSyncSeq[key]
}

// resyncAfterWait brings the clients owning path to the content a parked
// write registered and describes the result for the late watch. It mirrors
// syncAfterWrite's notification sequence (watched file change, didChange,
// didSave) with one per-client divergence: didChange is skipped if a newer
// successful sync than the one captured at registration already owns the
// buffer. Pushing the parked text over it would resurrect older content,
// and pushing identical text would only duplicate the analysis; either way
// the version that sync pushed is the one a watch can claim.
func (m *Manager) resyncAfterWait(ctx context.Context, absPath, content string, changeType WatchedFileChangeType, owners map[clientKey]pendingSyncOwner) afterWriteSync {
	sync := afterWriteSync{serverErrors: make(map[string]error)}
	m.dropLateDiagnostics(absPath)
	// The workspace file-change notification carries no content, so it is
	// always safe: it only tells the server the file changed on disk.
	if errs := afterWriteNotifyWatchedFileChanged(m, ctx, absPath, changeType); len(errs) > 0 {
		mergeServerErrors(sync.serverErrors, errs)
		m.logLSPServiceNote(absPath, "Failed to notify language server about workspace file change: "+formatServerErrors(errs))
	}
	sync.token = m.beginDiagnosticsSync(absPath)

	versions := make(map[string]int32)
	generations := make(map[clientKey]uint64)
	errs := make(map[string]error)
	m.forEachClientForPath(absPath, func(key clientKey, c *Client) {
		owner, ok := owners[key]
		if !ok || owner.client != c {
			return
		}
		c.lifecycleMu.Lock()
		// Only a successful sync newer than registration owns this buffer.
		// The comparison and change share the notification lock.
		if version := c.syncedDocumentVersion(absPath); version != owner.version {
			if version != 0 {
				versions[key.name] = version
				generations[key] = m.lateSyncGenerationOf(key)
			}
			c.lifecycleMu.Unlock()
			return
		}
		generation := m.noteLateDocumentSync(key)
		version, err := c.didChangeLocked(ctx, absPath, content)
		c.lifecycleMu.Unlock()
		if err != nil {
			errs[key.name] = err
			return
		}
		generations[key] = m.recordLateDocumentSync(key, generation)
		if err := c.NotifyDidSave(ctx, absPath, content); err != nil {
			errs[key.name] = err
			return
		}
		versions[key.name] = version
	})
	if len(errs) > 0 {
		mergeServerErrors(sync.serverErrors, errs)
		m.logLSPServiceNote(absPath, "Failed to sync buffer to language server: "+formatServerErrors(errs))
	}
	sync.serverVersions = versions
	sync.generations = generations
	finalizeAfterWriteSync(&sync)
	return sync
}

// currentLatePublishSeq returns the manager-wide publish sequence, so a watch
// registered right after a re-sync only claims publishes newer than it.
func (m *Manager) currentLatePublishSeq() uint64 {
	m.lateMu.Lock()
	defer m.lateMu.Unlock()
	return m.latePublishSeq
}
