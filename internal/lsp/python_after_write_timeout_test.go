package lsp

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/keakon/x/powernap/pkg/lsp/protocol"

	"github.com/keakon/chord/internal/config"
)

func newPythonTimeoutTestManager(t *testing.T) (*Manager, string, *Client) {
	t.Helper()
	root := resolvedTempDir(t)
	server := config.LSPServerConfig{Command: "sample-lsp", FileTypes: []string{".py"}}
	cfg := &config.Config{
		LSP:         config.LSPConfig{"sample-lsp": server},
		Diagnostics: config.DefaultDiagnosticsConfig(),
	}
	cfg.Diagnostics.Python.SemanticBackend.Server = "sample-lsp"
	m := NewManager(cfg, root, nil)
	client := &Client{
		client:      &fakePowernapClient{},
		cwd:         root,
		cfg:         server,
		openFiles:   make(map[string]int32),
		diagnostics: make(map[protocol.DocumentURI][]protocol.Diagnostic),
	}
	m.clients[testKey(m, "sample-lsp")] = client
	oldLook := lookPath
	oldReady, oldStart, oldWait, oldAwait := afterWriteHasReadyClient, afterWriteStart, afterWriteWaitForClient, afterWriteAwaitWaiter
	t.Cleanup(func() {
		lookPath = oldLook
		afterWriteHasReadyClient, afterWriteStart, afterWriteWaitForClient, afterWriteAwaitWaiter = oldReady, oldStart, oldWait, oldAwait
	})
	lookPath = func(string) (string, error) { return "", errors.New("checker unavailable") }
	afterWriteHasReadyClient = func(*Manager, string) bool { return true }
	afterWriteStart = func(*Manager, context.Context, string) {}
	afterWriteWaitForClient = func(*Manager, context.Context, string, time.Duration) (*Client, bool) { return client, true }
	afterWriteAwaitWaiter = func(m *Manager, _ context.Context, path string, ch chan diagnosticsEvent, _ diagnosticsWaitRequest, _ time.Duration) diagnosticsWaitResult {
		m.waitersMu.Lock()
		m.removeWaiter(path, ch)
		m.waitersMu.Unlock()
		return diagnosticsWaitResult{}
	}
	return m, filepath.Join(root, "sample.py"), client
}

func TestPythonSemanticTimeoutReportsEveryWrite(t *testing.T) {
	for _, tc := range []struct {
		name              string
		base              string
		includeOtherFiles bool
	}{
		{"write", "Successfully wrote file", true},
		{"edit", "Replaced one occurrence", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, cached := range []bool{false, true} {
				name := "empty"
				if cached {
					name = "cached"
				}
				t.Run(name, func(t *testing.T) {
					m, path, client := newPythonTimeoutTestManager(t)
					want := tc.base + DiagnosticsSectionMarker + "diagnostics unavailable (wait timed out)."
					if cached {
						client.diagnostics[protocol.DocumentURI(client.pathToURI(path))] = []protocol.Diagnostic{
							{Severity: protocol.SeverityError, Message: "cached diagnostic"},
						}
					}
					for i := range 2 {
						out := m.AfterFileWriteToolResult(context.Background(), path, "value = 1", tc.base, tc.includeOtherFiles, WatchedFileChanged, filepath.Dir(path))
						if out != want {
							t.Errorf("write %d output = %q, want %q", i+1, out, want)
						}
						if notes := DegradationNotes(out); len(notes) != 0 {
							t.Errorf("write %d degradation notes = %q, want none", i+1, notes)
						}
					}
				})
			}
		})
	}
}

func TestPythonSemanticTimeoutOmitsCachedDiagnostics(t *testing.T) {
	m, path, client := newPythonTimeoutTestManager(t)
	m.cfg.Diagnostics.Python.Output = config.DiagnosticOutputConfig{
		MaxTotalDiagnostics:   1,
		MaxNearDiagnostics:    1,
		MaxOutsideDiagnostics: 1,
		NearRangeBeforeLines:  1,
		NearRangeAfterLines:   1,
	}
	client.diagnostics[protocol.DocumentURI(client.pathToURI(path))] = []protocol.Diagnostic{
		{Severity: protocol.SeverityError, Range: protocol.Range{Start: protocol.Position{Line: 0}}, Message: "outside diagnostic"},
		{Severity: protocol.SeverityError, Range: protocol.Range{Start: protocol.Position{Line: 10}}, Message: "near diagnostic"},
	}
	result := m.afterWritePythonToolResult(context.Background(), path, "value = 1", "Replaced one occurrence", false, []EditRange{{StartLine: 10, EndLine: 10}}, WatchedFileChanged, filepath.Dir(path))
	const want = "Replaced one occurrence\n\nDiagnostics:\ndiagnostics unavailable (wait timed out)."
	if result.Output != want {
		t.Fatalf("output = %q, want %q", result.Output, want)
	}
}

func TestPythonSemanticCancellationOmitsCachedDiagnostics(t *testing.T) {
	m, path, client := newPythonTimeoutTestManager(t)
	client.diagnostics[protocol.DocumentURI(client.pathToURI(path))] = []protocol.Diagnostic{
		{Severity: protocol.SeverityError, Message: "cached diagnostic"},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result := m.afterWritePythonToolResult(ctx, path, "value = 1", "Successfully wrote file", false, nil, WatchedFileChanged, filepath.Dir(path))
	if result.Status != FileWriteStatusCancelled || result.Output != "Successfully wrote file" {
		t.Fatalf("cancelled result = %+v, want base output without cached diagnostics", result)
	}
}

func TestPythonSemanticTimeoutBatchReportsOnce(t *testing.T) {
	for _, tc := range []struct {
		name   string
		count  int
		cached bool
	}{
		{"single empty", 1, false},
		{"multiple empty", 2, false},
		{"single cached", 1, true},
		{"multiple cached", 2, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, path, client := newPythonTimeoutTestManager(t)
			writes := []FileWrite{
				{Path: path, Content: "value = 1", ChangeType: WatchedFileChanged},
				{Path: filepath.Join(filepath.Dir(path), "other.py"), Content: "value = 2", ChangeType: WatchedFileChanged},
			}
			writes = writes[:tc.count]
			if tc.cached {
				for _, write := range writes {
					client.diagnostics[protocol.DocumentURI(client.pathToURI(write.Path))] = []protocol.Diagnostic{
						{Severity: protocol.SeverityError, Message: "cached diagnostic"},
					}
				}
			}
			for i := range 2 {
				results := m.AfterFileWritesToolResult(context.Background(), writes, filepath.Dir(path))
				states := make(map[string]FileWriteDiagnosticsState, len(writes))
				paths := make([]string, 0, len(writes))
				for _, write := range writes {
					result := results[write.Path]
					if result.Status != FileWriteStatusTimeout || result.Detail != "wait timed out" {
						t.Errorf("batch %d result = %+v, want timeout without a late-watch promise", i+1, result)
					}
					if notes := DegradationNotes(result.Output); len(notes) != 0 {
						t.Errorf("batch %d degradation notes = %q, want none", i+1, notes)
					}
					states[write.Path] = result.FileWriteDiagnosticsState
					paths = append(paths, write.Path)
				}
				out := m.AppendLSPDiagnosticsToToolOutputForPaths("Applied patch", paths, false, nil, nil, nil, states, filepath.Dir(path))
				const want = "Applied patch\n\nDiagnostics:\ndiagnostics unavailable (wait timed out)."
				if out != want {
					t.Errorf("batch %d output = %q, want %q", i+1, out, want)
				}
			}
		})
	}
}

func TestPythonSemanticTimeoutUsesRuffResult(t *testing.T) {
	m, path, client := newPythonTimeoutTestManager(t)
	client.diagnostics[protocol.DocumentURI(client.pathToURI(path))] = []protocol.Diagnostic{
		{Severity: protocol.SeverityError, Message: "cached diagnostic"},
	}
	lookPath = func(string) (string, error) { return "sample-checker", nil }
	oldRun := runCommandContext
	t.Cleanup(func() { runCommandContext = oldRun })
	runs := 0
	runCommandContext = func(context.Context, string, ...string) ([]byte, error) {
		runs++
		return []byte(`[{"code":"F821","message":"Undefined name","location":{"row":1,"column":1}}]`), nil
	}
	result := m.afterWritePythonToolResult(context.Background(), path, "value = 1", "Successfully wrote file", true, nil, WatchedFileChanged, filepath.Dir(path))
	const want = "Successfully wrote file\n\nDiagnostics:\n[E] 1:1 [F821] Undefined name"
	if result.Status != FileWriteStatusReported || result.Output != want {
		t.Fatalf("Ruff fallback result = %+v, want reported with output %q", result, want)
	}
	if runs != 1 || len(client.client.(*fakePowernapClient).syncedURIs()) != 1 {
		t.Fatal("expected one semantic sync followed by one Ruff fallback")
	}
}

func TestPythonSemanticChangeSummaryRequiresFreshDiagnostics(t *testing.T) {
	for _, notified := range []bool{false, true} {
		name := "timeout"
		if notified {
			name = "fresh"
		}
		t.Run(name, func(t *testing.T) {
			m, path, client := newPythonTimeoutTestManager(t)
			uri := protocol.DocumentURI(client.pathToURI(path))
			client.diagnostics[uri] = []protocol.Diagnostic{{Severity: protocol.SeverityError, Message: "previous diagnostic"}}
			await := afterWriteAwaitWaiter
			afterWriteAwaitWaiter = func(m *Manager, ctx context.Context, p string, ch chan diagnosticsEvent, req diagnosticsWaitRequest, timeout time.Duration) diagnosticsWaitResult {
				await(m, ctx, p, ch, req, timeout)
				client.diagnostics[uri] = []protocol.Diagnostic{{Severity: protocol.SeverityError, Message: "current diagnostic"}}
				if !notified {
					return diagnosticsWaitResult{}
				}
				return diagnosticsWaitResult{notified: true, diagnostics: convertDiagnostics(client.diagnostics[uri])}
			}
			result := m.afterWritePythonToolResult(context.Background(), path, "value = 1", "Successfully wrote file", false, nil, WatchedFileChanged, filepath.Dir(path))
			want := "Successfully wrote file\n\nDiagnostics:\ndiagnostics unavailable (wait timed out)."
			if notified {
				want = "Successfully wrote file\n\nDiagnostics:\n[E] 1:1 current diagnostic\nDiagnostics changed: 1 new, 1 resolved."
			}
			if result.Output != want {
				t.Fatalf("output = %q, want %q", result.Output, want)
			}
		})
	}
}
