package lsp

import (
	"context"
	"maps"
	"slices"
	"strings"
	"time"

	pnprotocol "github.com/keakon/x/powernap/pkg/lsp/protocol"
)

// afterWriteSync describes the notifications one post-write synchronization
// sent, which the diagnostics wait needs to recognize a fresh publish.
type afterWriteSync struct {
	// Only servers whose entire notification sequence succeeded may verify
	// this write. Failed servers are removed even if didChange succeeded.
	serverVersions map[string]int32
	// serverErrors holds the first failed notification of every server whose
	// share of the sync failed. Together with serverVersions' keys it covers
	// every server the sync reached, so a server absent from both maps was
	// never an owner.
	serverErrors map[string]error
	after        time.Time
	token        diagnosticSyncToken
}

// syncAfterWrite pushes a written file to the language servers that own it:
// the workspace file-change notification (when notifyWatched), then didChange
// and didSave. Every failure is logged and attributed to its server in the
// returned sync, because a server that missed a notification cannot publish
// diagnostics that verify this write.
func (m *Manager) syncAfterWrite(ctx context.Context, absPath, content string, changeType pnprotocol.FileChangeType, notifyWatched bool) afterWriteSync {
	sync := afterWriteSync{serverErrors: make(map[string]error)}
	if notifyWatched {
		if errs := afterWriteNotifyWatchedFileChanged(m, ctx, absPath, changeType); len(errs) > 0 {
			mergeServerErrors(sync.serverErrors, errs)
			m.logLSPServiceNote(absPath, "Failed to notify language server about workspace file change: "+formatServerErrors(errs))
		}
	}
	sync.after = time.Now()
	sync.token = m.beginDiagnosticsSync(absPath)
	versions, errs := afterWriteDidChange(m, ctx, absPath, content)
	sync.serverVersions = versions
	if len(errs) > 0 {
		mergeServerErrors(sync.serverErrors, errs)
		m.logLSPServiceNote(absPath, "Failed to sync buffer to language server: "+formatServerErrors(errs))
	}
	if errs := afterWriteDidSave(m, ctx, absPath, content); len(errs) > 0 {
		mergeServerErrors(sync.serverErrors, errs)
		m.logLSPServiceNote(absPath, "Failed to notify language server about the saved file: "+formatServerErrors(errs))
	}
	for name := range sync.serverErrors {
		delete(sync.serverVersions, name)
	}
	return sync
}

// mergeServerErrors records each server's first failed notification: the sync
// steps run in pipeline order, so an error already present is from an earlier
// step and is kept.
func mergeServerErrors(into, from map[string]error) {
	for name, err := range from {
		if _, failed := into[name]; !failed {
			into[name] = err
		}
	}
}

// formatServerErrors renders per-server notification errors for the service
// log; servers are sorted so repeated failures log identically.
func formatServerErrors(errs map[string]error) string {
	out := make([]string, 0, len(errs))
	for _, name := range slices.Sorted(maps.Keys(errs)) {
		out = append(out, name+": "+errs[name].Error())
	}
	return strings.Join(out, "; ")
}

// settleAfterWriteSyncFailure handles the servers whose share of a post-write
// synchronization failed. It reclaims the failed instances whose connection is
// gone — dropping their orphaned diagnostics so the next write relaunches them
// — and appends one degradation note per failed server, named like the
// start/exited notes. It reports whether the diagnostics wait may still run:
// only when every owning server failed must the waiter be dropped, because a
// server that holds the new content can still publish diagnostics that verify
// this write, and its result is awaited normally while the failed servers'
// share stays flagged as unverified.
func (m *Manager) settleAfterWriteSyncFailure(ctx context.Context, absPath string, waiterCh chan diagnosticsEvent, sync afterWriteSync, base string) (string, bool) {
	if len(sync.serverErrors) == 0 {
		return base, true
	}
	// syncAfterWrite excludes every server that missed any notification.
	survived := len(sync.serverVersions) > 0
	if !survived {
		m.waitersMu.Lock()
		m.removeWaiter(absPath, waiterCh)
		m.waitersMu.Unlock()
	}
	// The prune only touches instances that stopped running, so the surviving
	// owners waiting below are never reclaimed by it.
	dead := m.pruneExitedClientsForPath(ctx, absPath)
	if ctx.Err() != nil {
		return base, survived
	}
	noted := make(map[string]struct{}, len(dead)+len(sync.serverErrors))
	for _, name := range dead {
		noted[name] = struct{}{}
		base = appendLSPDegradationNote(base, m.noteLSPDegradation(lspDegradationExited, name, "connection lost; retrying on the next file write"))
	}
	for _, name := range slices.Sorted(maps.Keys(sync.serverErrors)) {
		if _, already := noted[name]; already {
			continue
		}
		base = appendLSPDegradationNote(base, m.noteLSPDegradation(lspDegradationSync, name, "file synchronization failed"))
	}
	return base, survived
}
