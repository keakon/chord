package lsp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/keakon/x/powernap/pkg/lsp/protocol"

	"github.com/keakon/chord/internal/config"
)

func TestLateDiagnosticsUnversionedSemanticPublish(t *testing.T) {
	for _, beforeRegistration := range []bool{false, true} {
		name := "after registration"
		if beforeRegistration {
			name = "before registration"
		}
		t.Run(name, func(t *testing.T) {
			f := newLateDiagnosticsFixture(t)
			f.mgr.cfg.LSP["ts"] = config.LSPServerConfig{Command: "typescript-language-server"}
			f.addServer("ts")
			key := clientKey{name: "ts", root: f.mgr.projectRootPath()}
			token := f.mgr.beginDiagnosticsSync(f.path)
			token.confirmable = true
			f.mgr.noteLateDocumentSync(key)
			seq := f.publish("ts", 0)
			baseline := f.baseline()
			finding := lateDiag(protocol.SeverityError, 10, "value is not assignable")
			if beforeRegistration {
				f.publish("ts", 0, finding)
			}
			versions := map[string]int32{"ts": 2}
			f.mgr.watchLateDiagnostics(f.path, versions, f.generations(versions), baseline, seq, "", token)
			if !beforeRegistration {
				f.publish("ts", 0, finding)
			}
			items := f.mgr.PendingLateDiagnostics([]string{f.path}, "", "")
			if len(items) != 1 || !strings.Contains(items[0].Lines, "not assignable") {
				t.Fatalf("pending = %+v, want the semantic finding", items)
			}
			f.mgr.AckLateDiagnostics(items)
			if got := f.mgr.PendingLateDiagnostics([]string{f.path}, "", ""); len(got) != 0 {
				t.Fatalf("pending after ack = %+v, want none", got)
			}
		})
	}
}

func TestLateDiagnosticsUnversionedRejectsStalePublishes(t *testing.T) {
	for _, scenario := range []string{"before sync", "earlier generation", "later sync", "external edit", "unknown server", "failed sync", "missing receipt boundary"} {
		t.Run(scenario, func(t *testing.T) {
			f := newLateDiagnosticsFixture(t)
			f.mgr.cfg.LSP["ts"] = config.LSPServerConfig{Command: "typescript-language-server"}
			f.addServer("ts")
			key := clientKey{name: "ts", root: f.mgr.projectRootPath()}
			f.mgr.noteLateDocumentSync(key)
			versions := map[string]int32{"ts": 2}
			token := f.mgr.beginDiagnosticsSync(f.path)
			token.confirmable = true
			at := token.after.Add(time.Millisecond)
			finding := []Diagnostic{{Severity: 1, Message: "outdated semantic finding"}}
			switch scenario {
			case "before sync":
				at = token.after.Add(-time.Millisecond)
			case "earlier generation":
				f.mgr.noteLateDiagnosticsPublish(f.path, key, 0, at, finding)
				f.mgr.noteLateDocumentSync(key)
			case "unknown server":
				f.mgr.cfg.LSP["ts"] = config.LSPServerConfig{Command: "sample-language-server"}
			case "failed sync":
				token.confirmable = false
			case "missing receipt boundary":
				token.after = time.Time{}
			}
			f.mgr.watchLateDiagnostics(f.path, versions, f.generations(versions), nil, 0, "", token)
			if scenario != "earlier generation" {
				if scenario == "later sync" {
					f.mgr.noteLateDocumentSync(key)
				}
				f.mgr.noteLateDiagnosticsPublish(f.path, key, 0, at, finding)
			}
			if scenario == "external edit" {
				changed := token.mtime.Add(time.Second)
				if err := os.Chtimes(f.path, changed, changed); err != nil {
					t.Fatal(err)
				}
			}
			if got := f.mgr.PendingLateDiagnostics([]string{f.path}, "", ""); len(got) != 0 {
				t.Fatalf("pending = %+v, want no stale finding", got)
			}
		})
	}
}

func TestAfterWriteUnversionedSemanticPublishBeyondWait(t *testing.T) {
	for _, scenario := range []string{"settled syntax", "timeout", "cancelled"} {
		t.Run(scenario, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				m := NewManager(&config.Config{LSP: config.LSPConfig{
					"typescript": {Command: "typescript-language-server", FileTypes: []string{".ts"}},
				}}, resolvedTempDir(t), nil)
				path := filepath.Join(m.projectRootPath(), "main.ts")
				content := "const value: number = 'text';\n"
				if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
					t.Fatal(err)
				}
				key := testKey(m, "typescript")
				fake := &publishOnSaveClient{}
				fake.saveOptions = &protocol.SaveOptions{}
				client := &Client{client: fake, cwd: m.projectRootPath(), cfg: m.cfg.LSP["typescript"], openFiles: make(map[string]int32), diagnostics: make(map[protocol.DocumentURI][]protocol.Diagnostic)}
				m.clients[key] = client
				publish := func(diags []protocol.Diagnostic) {
					uri := protocol.DocumentURI(client.pathToURI(path))
					client.diagnosticsMu.Lock()
					client.diagnostics[uri] = diags
					client.diagnosticsMu.Unlock()
					m.onDiagnostics(key, client)(string(uri), path, nil, "", diags, 0)
				}
				fake.publish = func() {
					if scenario == "settled syntax" {
						publish(nil)
					}
					go func() {
						time.Sleep(time.Second)
						publish([]protocol.Diagnostic{lateDiag(protocol.SeverityError, 0, "value is not assignable")})
					}()
				}
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				prepared := m.prepareReadyAfterFileWrite(ctx, path, content, "wrote main.ts", WatchedFileChanged, false, time.Now())
				prepared.waitTimeout = 500 * time.Millisecond
				if scenario == "cancelled" {
					cancel()
				}
				result := m.awaitPreparedAfterFileWrite(ctx, &prepared, false, "")
				want := FileWriteStatusReported
				switch scenario {
				case "timeout":
					want = FileWriteStatusTimeout
				case "cancelled":
					want = FileWriteStatusCancelled
				}
				if result.Status != want || strings.Contains(result.Output, "not assignable") {
					t.Fatalf("foreground result = %+v, want %v before semantic publish", result, want)
				}
				time.Sleep(time.Second)
				synctest.Wait()
				items := m.PendingLateDiagnostics([]string{path}, "", "")
				if scenario == "cancelled" {
					if len(items) != 0 {
						t.Fatalf("cancelled write registered late diagnostics: %+v", items)
					}
					return
				}
				if len(items) != 1 || !strings.Contains(items[0].Lines, "not assignable") {
					t.Fatalf("late result = %+v, want the delayed semantic finding", items)
				}
				m.AckLateDiagnostics(items)
				if got := m.PendingLateDiagnostics([]string{path}, "", ""); len(got) != 0 {
					t.Fatalf("late result repeated after ack: %+v", got)
				}
			})
		})
	}
}
