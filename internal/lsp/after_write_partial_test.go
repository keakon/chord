package lsp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/keakon/x/powernap/pkg/lsp/protocol"
)

func TestAfterWritePartialSyncRemainsAttributedAfterNoteDeduplication(t *testing.T) {
	for _, semantic := range []bool{false, true} {
		name := "generic"
		if semantic {
			name = "semantic"
		}
		t.Run(name, func(t *testing.T) {
			m, path, _, _ := newAfterWriteServerPair(t, func(context.Context) error { return errors.New("notification rejected") }, nil)
			oldStart, oldAwait := afterWriteStart, afterWriteAwaitWaiter
			t.Cleanup(func() { afterWriteStart, afterWriteAwaitWaiter = oldStart, oldAwait })
			afterWriteStart = func(*Manager, context.Context, string) {}
			afterWriteAwaitWaiter = func(m *Manager, _ context.Context, p string, ch chan diagnosticsEvent, req diagnosticsWaitRequest, _ time.Duration) ([]Diagnostic, bool, uint64, map[diagnosticIdentity]struct{}) {
				if len(req.serverVersions) != 1 || req.serverVersions["samplelsp"] == 0 {
					t.Fatalf("survivors = %v", req.serverVersions)
				}
				m.waitersMu.Lock()
				m.removeWaiter(p, ch)
				m.waitersMu.Unlock()
				return nil, true, 0, nil
			}
			for i := range 2 {
				var result FileWriteResult
				if semantic {
					result = m.afterWriteLSPToolResult(context.Background(), path, "package main", "", false, nil, WatchedFileChanged, "")
				} else {
					prepared := m.prepareReadyAfterFileWrite(context.Background(), path, "package main", "", WatchedFileChanged, false, time.Now())
					result = m.awaitPreparedAfterFileWrite(context.Background(), &prepared, false, "")
				}
				if result.Status != FileWriteStatusPartial || !strings.Contains(result.Detail, "gopls: notification rejected") {
					t.Fatalf("result = %+v", result)
				}
				if i == 1 && len(DegradationNotes(result.Output)) != 0 {
					t.Fatal("expected the session note to be deduplicated")
				}
			}
		})
	}
}

func TestPartialWriteExcludesFailedServerCache(t *testing.T) {
	for _, semantic := range []bool{false, true} {
		name := "generic"
		if semantic {
			name = "semantic"
		}
		t.Run(name, func(t *testing.T) {
			m, path, clients, _ := newAfterWriteServerPair(t, func(context.Context) error { return errors.New("notification rejected") }, nil)
			if err := os.WriteFile(path, []byte("package main"), 0o600); err != nil {
				t.Fatal(err)
			}
			for name, c := range clients {
				text := "current diagnostic"
				if name == "gopls" {
					text = "stale diagnostic"
				}
				c.diagnostics[protocol.DocumentURI(c.pathToURI(path))] = []protocol.Diagnostic{{Severity: 1, Message: text}}
			}
			oldStart, oldAwait := afterWriteStart, afterWriteAwaitWaiter
			t.Cleanup(func() { afterWriteStart, afterWriteAwaitWaiter = oldStart, oldAwait })
			afterWriteStart = func(*Manager, context.Context, string) {}
			afterWriteAwaitWaiter = func(m *Manager, _ context.Context, p string, ch chan diagnosticsEvent, _ diagnosticsWaitRequest, _ time.Duration) ([]Diagnostic, bool, uint64, map[diagnosticIdentity]struct{}) {
				m.waitersMu.Lock()
				m.removeWaiter(p, ch)
				m.waitersMu.Unlock()
				return nil, true, 0, nil
			}
			var result FileWriteResult
			if semantic {
				result = m.afterWriteLSPToolResult(context.Background(), path, "package main", "written", false, nil, WatchedFileChanged, "")
			} else {
				prepared := m.prepareReadyAfterFileWrite(context.Background(), path, "package main", "written", WatchedFileChanged, false, time.Now())
				result = m.awaitPreparedAfterFileWrite(context.Background(), &prepared, false, "")
			}
			if !strings.Contains(result.Output, "current diagnostic") || strings.Contains(result.Output, "stale diagnostic") {
				t.Errorf("single-file output = %q", result.Output)
			}
			states := map[string]FileWriteDiagnosticsState{path: {Status: result.Status, Detail: result.Detail, FailedServers: result.FailedServers}}
			extras := map[string][]Diagnostic{path: ParseToolOutputDiagnostics(result.Output)}
			batch := m.AppendLSPDiagnosticsToToolOutputForPaths("written", []string{path, filepath.Join(filepath.Dir(path), "other.go")}, false, nil, nil, extras, states, "")
			if !strings.Contains(batch, "current diagnostic") || strings.Contains(batch, "stale diagnostic") {
				t.Errorf("batch output = %q", batch)
			}
			m.diagMu.Lock()
			fresh := m.diagnosticsFreshLocked(path)
			m.diagMu.Unlock()
			if fresh {
				t.Error("partially synchronized file became eligible for unrelated other-file output")
			}
		})
	}
}
