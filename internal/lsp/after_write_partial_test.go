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

	"github.com/keakon/chord/internal/config"
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
			afterWriteAwaitWaiter = func(m *Manager, _ context.Context, p string, ch chan diagnosticsEvent, req diagnosticsWaitRequest, _ time.Duration) diagnosticsWaitResult {
				if len(req.serverVersions) != 1 || req.serverVersions["samplelsp"] == 0 {
					t.Fatalf("survivors = %v", req.serverVersions)
				}
				m.waitersMu.Lock()
				m.removeWaiter(p, ch)
				m.waitersMu.Unlock()
				return diagnosticsWaitResult{notified: true}
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

func TestPartialWriteUsesOnlyVerifiedPublishes(t *testing.T) {
	for _, semantic := range []bool{false, true} {
		name := "generic"
		if semantic {
			name = "semantic"
		}
		t.Run(name, func(t *testing.T) {
			m, path, clients, _ := newAfterWriteServerPair(t, func(context.Context) error { return errors.New("notification rejected") }, nil)
			const silent = "silent-lsp"
			m.cfg.LSP[silent] = config.LSPServerConfig{Command: silent, FileTypes: []string{".go"}}
			quiet := newResyncTestClient(&fakePowernapClient{})
			quiet.name, quiet.cwd, quiet.cfg = silent, m.projectRootPath(), m.cfg.LSP[silent]
			quiet.diagnostics = make(map[protocol.DocumentURI][]protocol.Diagnostic)
			m.clients[testKey(m, silent)] = quiet
			clients[silent] = quiet
			if err := os.WriteFile(path, []byte("package main"), 0o600); err != nil {
				t.Fatal(err)
			}
			for name, c := range clients {
				c.diagnostics[protocol.DocumentURI(c.pathToURI(path))] = []protocol.Diagnostic{{Severity: 1, Message: "cached diagnostic from " + name}}
			}
			oldStart, oldAwait := afterWriteStart, afterWriteAwaitWaiter
			t.Cleanup(func() { afterWriteStart, afterWriteAwaitWaiter = oldStart, oldAwait })
			afterWriteStart = func(*Manager, context.Context, string) {}
			afterWriteAwaitWaiter = func(m *Manager, ctx context.Context, p string, ch chan diagnosticsEvent, req diagnosticsWaitRequest, timeout time.Duration) diagnosticsWaitResult {
				if req.serverVersions[silent] == 0 || req.serverVersions["samplelsp"] == 0 {
					t.Fatalf("synchronized owners = %v", req.serverVersions)
				}
				ch <- diagnosticsEvent{serverID: silent, version: req.serverVersions[silent] + 1, diagnostics: []Diagnostic{{Severity: 1, Message: "different write"}}}
				ch <- diagnosticsEvent{serverID: "samplelsp", version: req.serverVersions["samplelsp"], diagnostics: []Diagnostic{{Severity: 1, Message: "current diagnostic"}}}
				req.settle = time.Millisecond
				return m.AwaitFreshWaiter(ctx, p, ch, req, timeout)
			}
			var result FileWriteResult
			if semantic {
				result = m.afterWriteLSPToolResult(context.Background(), path, "package main", "written", false, nil, WatchedFileChanged, "")
			} else {
				prepared := m.prepareReadyAfterFileWrite(context.Background(), path, "package main", "written", WatchedFileChanged, false, time.Now())
				result = m.awaitPreparedAfterFileWrite(context.Background(), &prepared, false, "")
			}
			if result.Status != FileWriteStatusPartial || !strings.Contains(result.Detail, "no fresh publish from "+silent) {
				t.Fatalf("write result = %+v, want the missing server identified", result)
			}
			states := map[string]FileWriteDiagnosticsState{path: result.FileWriteDiagnosticsState}
			snapshots := map[string][]Diagnostic{path: result.Diagnostics}
			baselines := map[string][]Diagnostic{path: {{Severity: 1, Message: "cached diagnostic"}}}
			batch := m.AppendLSPDiagnosticsToToolOutputForPaths("written", []string{path, filepath.Join(filepath.Dir(path), "other.go")}, false, baselines, nil, snapshots, states, "")
			for name, output := range map[string]string{"single": result.Output, "batch": batch} {
				if !strings.Contains(output, "current diagnostic") || strings.Contains(output, "cached diagnostic") ||
					strings.Contains(output, "different write") || strings.Contains(output, "resolved") {
					t.Errorf("%s output = %q", name, output)
				}
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

func TestWriteCancellationKeepsOnlyConfirmedDiagnostics(t *testing.T) {
	for _, pipeline := range []string{"generic", "semantic"} {
		t.Run(pipeline, func(t *testing.T) {
			for _, confirmed := range []bool{false, true} {
				name := "before confirmation"
				if confirmed {
					name = "after confirmation"
				}
				t.Run(name, func(t *testing.T) {
					m, path, clients, _ := newAfterWriteServerPair(t, nil, nil)
					restoreAfterWriteHooks(t)
					afterWriteStart = func(*Manager, context.Context, string) {}
					client := clients["gopls"]
					uri := protocol.DocumentURI(client.pathToURI(path))
					client.diagnostics[uri] = []protocol.Diagnostic{{Severity: 1, Message: "cached diagnostic"}}

					ctx, cancel := context.WithCancel(t.Context())
					defer cancel()
					afterWriteAwaitWaiter = func(m *Manager, ctx context.Context, p string, ch chan diagnosticsEvent, req diagnosticsWaitRequest, timeout time.Duration) diagnosticsWaitResult {
						ch <- diagnosticsEvent{serverID: "gopls", version: req.serverVersions["gopls"], diagnostics: []Diagnostic{{Severity: 1, Message: "verified diagnostic"}}}
						ch <- diagnosticsEvent{serverID: "samplelsp", version: req.serverVersions["samplelsp"]}
						if !confirmed {
							cancel()
						}
						req.settle = time.Millisecond
						got := m.AwaitFreshWaiter(ctx, p, ch, req, timeout)
						cancel()
						return got
					}
					const base, content = "written", "package main"
					var result FileWriteResult
					if pipeline == "semantic" {
						result = m.afterWriteLSPToolResult(ctx, path, content, base, false, nil, WatchedFileChanged, "")
					} else {
						prepared := m.prepareReadyAfterFileWrite(ctx, path, content, base, WatchedFileChanged, false, time.Now())
						result = m.awaitPreparedAfterFileWrite(ctx, &prepared, false, "")
					}
					if confirmed {
						if result.Status != FileWriteStatusReported || !strings.Contains(result.Output, "verified diagnostic") || strings.Contains(result.Output, "cached diagnostic") ||
							len(result.Diagnostics) != 1 {
							t.Fatalf("cancelled after verification: %+v", result)
						}
					} else if result.Status != FileWriteStatusCancelled || result.Output != base || len(result.Diagnostics) != 0 {
						t.Fatalf("cancelled before verification: %+v", result)
					}
					m.diagMu.RLock()
					_, reviewed := m.reviewByServer["gopls"][path]
					m.diagMu.RUnlock()
					if reviewed != confirmed {
						t.Fatalf("review recorded = %v; confirmed = %v", reviewed, confirmed)
					}
				})
			}
		})
	}
}
