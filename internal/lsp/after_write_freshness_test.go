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
	"github.com/keakon/chord/internal/message"
)

func TestGoplsUnversionedPublishDoesNotVerifyWrite(t *testing.T) {
	for _, delay := range []time.Duration{10 * time.Millisecond, time.Second} {
		t.Run(delay.String(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				m, path, _ := newBatchTestManager(t)
				c := m.clients[testKey(m, "gopls")]
				afterWriteDidSave = func(*Manager, context.Context, string, string) map[string]error {
					go func() {
						time.Sleep(delay)
						m.onDiagnostics(testKey(m, "gopls"), c)(c.pathToURI(path), path, nil, "", nil, 0)
					}()
					return nil
				}
				result := m.AfterFileWritesToolResult(context.Background(), []FileWrite{{Path: path, Content: "package main\n", ChangeType: WatchedFileChanged}}, "")[path]
				if result.Status != FileWriteStatusTimeout {
					t.Fatalf("unversioned publish verified write: %+v", result)
				}
			})
		})
	}
}

func TestTypeScriptWaitIncludesDelayedSemanticDiagnostics(t *testing.T) {
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
			publish(nil)
			go func() {
				time.Sleep(150 * time.Millisecond)
				publish([]protocol.Diagnostic{{Severity: protocol.SeverityError, Message: "Type 'string' is not assignable to type 'number'."}})
			}()
		}
		prepared := m.prepareReadyAfterFileWrite(context.Background(), path, content, "", WatchedFileChanged, false, time.Now())
		result := m.awaitPreparedAfterFileWrite(context.Background(), &prepared, false, "")
		if result.Status != FileWriteStatusReported || !strings.Contains(result.Output, "not assignable") {
			t.Fatalf("semantic diagnostics were lost: %+v", result)
		}
	})
}

func TestCleanLateReviewIsRestorableWithoutDiagnosticText(t *testing.T) {
	f := newLateDiagnosticsFixture(t)
	f.publish("gopls", 1, lateDiag(protocol.SeverityError, 0, "undefined: sample"))
	f.mgr.recordReviewSnapshot(f.path)
	history := []message.Message{{Role: message.RoleTool, LSPReviews: f.mgr.CurrentReviewSnapshots(f.path)}}
	token := f.mgr.beginDiagnosticsSync(f.path)
	token.confirmable = true
	versions := map[string]int32{"gopls": 2}
	f.mgr.watchLateDiagnostics(f.path, versions, f.generations(versions), nil, 0, "", token)
	f.publish("gopls", 2)
	items := f.mgr.PendingLateDiagnostics([]string{f.path}, "", "")
	if len(items) != 1 || items[0].Lines != "" || len(items[0].Reviews) != 1 {
		t.Fatalf("clean late result = %+v", items)
	}
	history = append(history, message.Message{Role: message.RoleSystem, Kind: message.KindLSPReview, LSPReviews: items[0].Reviews})
	f.mgr.AckLateDiagnostics(items)
	restored := RebuildReviewSnapshotsFromMessages(history)
	if len(restored) != 1 || restored[0].Errors != 0 || len(f.mgr.ReviewedPaths()) != 1 {
		t.Fatalf("restored = %+v, reviewed paths = %v", restored, f.mgr.ReviewedPaths())
	}
	if again := f.mgr.PendingLateDiagnostics([]string{f.path}, "", ""); len(again) != 0 {
		t.Fatalf("clean result repeated after acknowledgement: %+v", again)
	}
}
