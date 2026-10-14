package lsp

import (
	"context"
	"os"
	"time"

	"github.com/keakon/x/powernap/pkg/lsp/protocol"
)

// syncedFile describes the disk snapshot corresponding to an open overlay.
// File identity catches atomic replacements even when timestamps are preserved.
type syncedFile struct {
	info os.FileInfo
	used time.Time
}

func sameDiskFile(a, b os.FileInfo) bool {
	return a != nil && b != nil && os.SameFile(a, b) && a.Size() == b.Size() && a.ModTime().Equal(b.ModTime())
}

// Versions never repeat within a client, including close followed by reopen.
// This avoids retaining an unbounded map of deleted document generations.
func (c *Client) nextDocumentVersionLocked(previous int32) int32 {
	c.documentVersion = max(c.documentVersion, previous) + 1
	return c.documentVersion
}

func (c *Client) acceptDiagnostics(path string, version int32) bool {
	_, err := os.Stat(path)
	if os.IsNotExist(err) {
		return false
	}
	c.openFilesMu.Lock()
	current, open := c.openFiles[path]
	c.openFilesMu.Unlock()
	// Versionless workspace diagnostics are valid for closed disk documents.
	return version <= 0 || (open && version >= current)
}

// clearFileState removes live diagnostics without rewriting session history.
// Modified files retain review membership, so the next publish updates counts.
func (m *Manager) clearFileState(path string, deleted bool) {
	uri := string(protocol.URIFromPath(path))
	m.diagMu.Lock()
	names := make(map[string]struct{})
	for key, byURI := range m.diagByServer {
		if _, ok := byURI[uri]; ok {
			names[key.name] = struct{}{}
		}
		delete(byURI, uri)
		if len(byURI) == 0 {
			delete(m.diagByServer, key)
		}
	}
	for key, byPath := range m.publishedDiagByServer {
		delete(byPath, path)
		if len(byPath) == 0 {
			delete(m.publishedDiagByServer, key)
		}
	}
	for name, byPath := range m.reviewByServer {
		if review, ok := byPath[path]; ok {
			names[name] = struct{}{}
			if deleted {
				delete(byPath, path)
			} else {
				review.errors, review.warnings = 0, 0
				byPath[path] = review
			}
		}
		if len(byPath) == 0 {
			delete(m.reviewByServer, name)
		}
	}
	if deleted {
		delete(m.reportedByPath, path)
	}
	delete(m.diagState, path)
	m.diagMu.Unlock()
	if deleted {
		m.UnmarkTouched(path)
		m.waitersMu.Lock()
		delete(m.waiters, path)
		m.waitersMu.Unlock()
	}
	for name := range names {
		if m.broadcast != nil {
			m.broadcast(TypeLSPDiagnostics, DiagnosticsPayload{URI: uri, ServerID: name})
		}
	}
	m.notifySidebarChanged()
}

// clearClientFileState retires one overlay without invalidating other servers'
// diagnostics or their file freshness and session suppression state.
func (m *Manager) clearClientFileState(c *Client, path string) {
	key := clientKey{name: c.name, root: c.cwd}
	uri := c.pathToURI(path)
	m.diagMu.Lock()
	m.clientsMu.RLock()
	current := m.clients[key] == c
	m.clientsMu.RUnlock()
	if !current {
		m.diagMu.Unlock()
		return
	}
	if byURI := m.diagByServer[key]; byURI != nil {
		delete(byURI, uri)
		if len(byURI) == 0 {
			delete(m.diagByServer, key)
		}
	}
	m.updateDiagnosticOutputStateLocked(key, path, nil)
	if byPath := m.reviewByServer[key.name]; byPath != nil {
		if review, ok := byPath[path]; ok {
			latest := m.reviewCountsForPathLocked(key.name, path)
			review.errors, review.warnings = latest.errors, latest.warnings
			byPath[path] = review
		}
	}
	m.diagMu.Unlock()
	if m.broadcast != nil {
		m.broadcast(TypeLSPDiagnostics, DiagnosticsPayload{URI: uri, ServerID: key.name})
	}
	m.notifySidebarChanged()
}

type overlayReleaseReason uint8

const (
	overlayReleaseEvicted overlayReleaseReason = iota
	overlayReleaseChanged
	overlayReleaseDeleted
)

// releaseOverlay gives ownership of the document back to the server's
// filesystem reader. This also handles non-UTF-8 files without introducing a
// second decoder or sending raw disk bytes as LSP text. Never reopen on a
// background event: only a user/tool edit should create an overlay.
func (m *Manager) releaseOverlay(ctx context.Context, c *Client, path string, expected syncedFile, reason overlayReleaseReason) bool {
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	if c.closed || ctx.Err() != nil {
		return false
	}
	deleted := reason == overlayReleaseDeleted
	if deleted {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			return false
		}
	}
	c.openFilesMu.Lock()
	current, exists := c.syncedFiles[path]
	if !exists || !current.used.Equal(expected.used) {
		c.openFilesMu.Unlock()
		return false
	}
	delete(c.openFiles, path)
	delete(c.syncedDigest, path)
	delete(c.syncedFiles, path)
	c.openFilesMu.Unlock()
	c.clearDiagnosticsForPath(path)
	if reason == overlayReleaseEvicted {
		m.clearClientFileState(c, path)
	} else {
		m.clearFileState(path, deleted)
	}
	_ = c.observeTransportError(c.client.NotifyDidCloseTextDocument(ctx, c.pathToURI(path)))
	change := WatchedFileChanged
	if deleted {
		change = WatchedFileDeleted
	}
	_ = c.observeTransportError(c.client.NotifyDidChangeWatchedFiles(ctx, []protocol.FileEvent{{URI: protocol.DocumentURI(c.pathToURI(path)), Type: change}}))
	return true
}

// Protocol IO must not hold the manager registry lock: background document
// invalidation and server callbacks also need access to the registry.
func (m *Manager) forEachClientForPath(path string, fn func(clientKey, *Client)) {
	m.clientsMu.RLock()
	clients := make(map[clientKey]*Client)
	m.forEachClientForPathLocked(path, func(key clientKey, c *Client) { clients[key] = c })
	m.clientsMu.RUnlock()
	for key, c := range clients {
		c.touch(time.Now().UnixNano())
		fn(key, c)
	}
}
