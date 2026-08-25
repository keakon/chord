package lsp

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/keakon/x/powernap/pkg/lsp/protocol"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
)

func newDiagnosticOutputTestManager(t *testing.T) (*Manager, string, string, clientKey, protocol.DocumentURI, *Client) {
	t.Helper()
	root := t.TempDir()
	mgr := NewManager(&config.Config{LSP: config.LSPConfig{
		"gopls": {Command: "gopls", FileTypes: []string{".go"}},
	}}, root, nil)
	edited := filepath.Join(root, "edited.go")
	other := filepath.Join(root, "other.go")
	if err := os.WriteFile(other, []byte("package sample\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	key := testKey(mgr, "gopls")
	uri := protocol.DocumentURI("file://" + filepath.ToSlash(other))
	client := &Client{diagnostics: make(map[protocol.DocumentURI][]protocol.Diagnostic)}
	mgr.clients[key] = client
	return mgr, edited, other, key, uri, client
}

func publishTestDiagnostics(mgr *Manager, key clientKey, uri protocol.DocumentURI, client *Client, version int32, diags ...protocol.Diagnostic) {
	client.diagnosticsMu.Lock()
	client.diagnostics[uri] = append([]protocol.Diagnostic(nil), diags...)
	client.diagnosticsMu.Unlock()
	mgr.onDiagnostics(key)(string(uri), "", diags, version)
}

func testProtocolDiagnostic(message string, line uint32) protocol.Diagnostic {
	return protocol.Diagnostic{
		Severity: protocol.SeverityError,
		Range:    protocol.Range{Start: protocol.Position{Line: line, Character: 0}},
		Message:  message,
	}
}

func TestResetReportedDiagnosticsStartsNewSessionWindow(t *testing.T) {
	mgr, edited, _, key, uri, client := newDiagnosticOutputTestManager(t)
	diag := testProtocolDiagnostic("cached error", 0)
	publishTestDiagnostics(mgr, key, uri, client, 1, diag)

	first := mgr.AppendLSPDiagnosticsToToolOutputForPaths("Applied patch", []string{edited}, true, nil, nil, nil, "")
	second := mgr.AppendLSPDiagnosticsToToolOutputForPaths("Applied patch", []string{edited}, true, nil, nil, nil, "")
	if !strings.Contains(first, diag.Message) || strings.Contains(second, diag.Message) {
		t.Fatalf("first = %q, second = %q; want one report per session", first, second)
	}

	mgr.ResetReportedDiagnostics()
	third := mgr.AppendLSPDiagnosticsToToolOutputForPaths("Applied patch", []string{edited}, true, nil, nil, nil, "")
	if !strings.Contains(third, diag.Message) {
		t.Fatalf("third = %q, want diagnostic reported in new session window", third)
	}
}

func TestResolvedDiagnosticCanReappearWhileAnotherRemains(t *testing.T) {
	mgr, edited, _, key, uri, client := newDiagnosticOutputTestManager(t)
	a := testProtocolDiagnostic("error a", 0)
	b := testProtocolDiagnostic("error b", 1)
	publishTestDiagnostics(mgr, key, uri, client, 1, a, b)
	first := mgr.AppendLSPDiagnosticsToToolOutputForPaths("Applied patch", []string{edited}, true, nil, nil, nil, "")
	if !strings.Contains(first, a.Message) || !strings.Contains(first, b.Message) {
		t.Fatalf("first = %q, want both diagnostics", first)
	}

	publishTestDiagnostics(mgr, key, uri, client, 2, b)
	publishTestDiagnostics(mgr, key, uri, client, 3, a, b)
	second := mgr.AppendLSPDiagnosticsToToolOutputForPaths("Applied patch", []string{edited}, true, nil, nil, nil, "")
	if !strings.Contains(second, a.Message) || strings.Contains(second, b.Message) {
		t.Fatalf("second = %q, want only reappeared diagnostic a", second)
	}
}

func TestCleanPublishFromOneServerDoesNotClearAnotherServer(t *testing.T) {
	mgr, edited, _, goplsKey, uri, client := newDiagnosticOutputTestManager(t)
	otherKey := testKey(mgr, "sample-lsp")
	otherClient := &Client{diagnostics: make(map[protocol.DocumentURI][]protocol.Diagnostic)}
	mgr.clients[otherKey] = otherClient
	goplsDiag := testProtocolDiagnostic("gopls error", 0)
	otherDiag := testProtocolDiagnostic("other error", 1)
	publishTestDiagnostics(mgr, goplsKey, uri, client, 1, goplsDiag)
	publishTestDiagnostics(mgr, otherKey, uri, otherClient, 1, otherDiag)
	_ = mgr.AppendLSPDiagnosticsToToolOutputForPaths("Applied patch", []string{edited}, true, nil, nil, nil, "")

	publishTestDiagnostics(mgr, goplsKey, uri, client, 2)
	mgr.ResetReportedDiagnostics()
	out := mgr.AppendLSPDiagnosticsToToolOutputForPaths("Applied patch", []string{edited}, true, nil, nil, nil, "")
	if strings.Contains(out, goplsDiag.Message) || !strings.Contains(out, otherDiag.Message) {
		t.Fatalf("output = %q, want only surviving server diagnostic", out)
	}
}

func TestDeletedOtherFileDiagnosticsStaySuppressed(t *testing.T) {
	mgr, edited, other, key, uri, client := newDiagnosticOutputTestManager(t)
	diag := testProtocolDiagnostic("cached error", 0)
	publishTestDiagnostics(mgr, key, uri, client, 1, diag)
	if err := os.Remove(other); err != nil {
		t.Fatal(err)
	}

	out := mgr.AppendLSPDiagnosticsToToolOutputForPaths("Applied patch", []string{edited}, true, nil, nil, nil, "")
	if strings.Contains(out, diag.Message) {
		t.Fatalf("output = %q, want diagnostic for deleted file suppressed", out)
	}
}

func TestConcurrentOtherFileReservationReportsOnce(t *testing.T) {
	mgr, edited, _, key, uri, client := newDiagnosticOutputTestManager(t)
	diag := testProtocolDiagnostic("cached error", 0)
	publishTestDiagnostics(mgr, key, uri, client, 1, diag)

	start := make(chan struct{})
	outputs := make(chan string, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			<-start
			outputs <- mgr.AppendLSPDiagnosticsToToolOutputForPaths("Applied patch", []string{edited}, true, nil, nil, nil, "")
		})
	}
	close(start)
	wg.Wait()
	close(outputs)

	reports := 0
	for out := range outputs {
		if strings.Contains(out, diag.Message) {
			reports++
		}
	}
	if reports != 1 {
		t.Fatalf("diagnostic reports = %d, want exactly 1", reports)
	}
}

func TestStaleDiagnosticsRequireFreshSyncConfirmation(t *testing.T) {
	mgr, edited, other, key, uri, client := newDiagnosticOutputTestManager(t)
	diag := testProtocolDiagnostic("cached error", 0)
	publishTestDiagnostics(mgr, key, uri, client, 1, diag)

	oldTime := time.Now().Add(-2 * time.Second)
	if err := os.Chtimes(other, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	stale := mgr.AppendLSPDiagnosticsToToolOutputForPaths("Applied patch", []string{edited}, true, nil, nil, nil, "")
	if strings.Contains(stale, diag.Message) {
		t.Fatalf("stale output = %q, want external change suppressed", stale)
	}

	// A delayed publish by itself cannot bless stale diagnostics.
	publishTestDiagnostics(mgr, key, uri, client, 2, diag)
	delayed := mgr.AppendLSPDiagnosticsToToolOutputForPaths("Applied patch", []string{edited}, true, nil, nil, nil, "")
	if strings.Contains(delayed, diag.Message) {
		t.Fatalf("delayed output = %q, want stale path still suppressed", delayed)
	}

	token := mgr.beginDiagnosticsSync(other)
	publishTestDiagnostics(mgr, key, uri, client, 3, diag)
	mgr.confirmDiagnosticsSync(token)
	fresh := mgr.AppendLSPDiagnosticsToToolOutputForPaths("Applied patch", []string{edited}, true, nil, nil, nil, "")
	if !strings.Contains(fresh, diag.Message) {
		t.Fatalf("fresh output = %q, want diagnostic after confirmed sync", fresh)
	}
}

func TestCleanPublishAfterExternalChangeDoesNotBlessLaterStaleDiagnostic(t *testing.T) {
	mgr, edited, other, key, uri, client := newDiagnosticOutputTestManager(t)
	diag := testProtocolDiagnostic("cached error", 0)
	publishTestDiagnostics(mgr, key, uri, client, 1, diag)

	changedTime := time.Now().Add(-2 * time.Second)
	if err := os.Chtimes(other, changedTime, changedTime); err != nil {
		t.Fatal(err)
	}
	publishTestDiagnostics(mgr, key, uri, client, 2)
	publishTestDiagnostics(mgr, key, uri, client, 3, diag)
	out := mgr.AppendLSPDiagnosticsToToolOutputForPaths("Applied patch", []string{edited}, true, nil, nil, nil, "")
	if strings.Contains(out, diag.Message) {
		t.Fatalf("output = %q, want stale diagnostic suppressed after unconfirmed clean publish", out)
	}
}

// TestEditDoesNotRepeatAlreadyReportedDiagnostic pins the context-cost rule: a
// problem the model was already told about must not be re-announced by every
// later edit. Only what changed is worth spending context on.
func TestEditDoesNotRepeatAlreadyReportedDiagnostic(t *testing.T) {
	mgr, edited, other, key, uri, client := newDiagnosticOutputTestManager(t)
	diag := testProtocolDiagnostic("still broken", 0)
	publishTestDiagnostics(mgr, key, uri, client, 1, diag)

	first := mgr.AppendLSPDiagnosticsToToolOutputForPaths("Applied patch", []string{edited}, true, nil, nil, nil, "")
	second := mgr.AppendLSPDiagnosticsToToolOutputForPaths("Applied patch", []string{edited}, true, nil, nil, nil, "")
	if !strings.Contains(first, diag.Message) || strings.Contains(second, diag.Message) {
		t.Fatalf("first = %q, second = %q; want one report while the file is unchanged", first, second)
	}

	// Chord edits the diagnosed file and the same problem survives. It is not
	// news: the model already has it. beginDiagnosticsSync is an after-write
	// hook, so the new bytes are already on disk when it captures the mtime.
	time.Sleep(10 * time.Millisecond)
	if err := os.WriteFile(other, []byte("package sample\n\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	token := mgr.beginDiagnosticsSync(other)
	publishTestDiagnostics(mgr, key, uri, client, 2, diag)
	mgr.confirmDiagnosticsSync(token)

	third := mgr.AppendLSPDiagnosticsToToolOutputForPaths("Applied patch", []string{edited}, true, nil, nil, nil, "")
	if strings.Contains(third, diag.Message) {
		t.Fatalf("third = %q, want no repeat of a surviving problem after an edit", third)
	}
}

// TestFixedDiagnosticStopsBeingReported covers the other half: a problem fixed
// by another agent, a copy, or a git checkout must stop being reported instead
// of being served from cache forever.
func TestFixedDiagnosticStopsBeingReported(t *testing.T) {
	mgr, edited, other, key, uri, client := newDiagnosticOutputTestManager(t)
	diag := testProtocolDiagnostic("broken", 0)
	publishTestDiagnostics(mgr, key, uri, client, 1, diag)
	if out := mgr.AppendLSPDiagnosticsToToolOutputForPaths("Applied patch", []string{edited}, true, nil, nil, nil, ""); !strings.Contains(out, diag.Message) {
		t.Fatalf("out = %q, want the problem reported once", out)
	}

	// Something outside Chord restores/fixes the file. The cached diagnostic no
	// longer describes it, so it must not be surfaced again on any later edit,
	// with or without a follow-up publish.
	time.Sleep(10 * time.Millisecond)
	if err := os.WriteFile(other, []byte("package sample\n// fixed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out := mgr.AppendLSPDiagnosticsToToolOutputForPaths("Applied patch", []string{edited}, true, nil, nil, nil, ""); strings.Contains(out, diag.Message) {
		t.Fatalf("out = %q, want a stale cached diagnostic withheld after an external change", out)
	}

	// The server confirms the fix by publishing an empty set.
	publishTestDiagnostics(mgr, key, uri, client, 2)
	if out := mgr.AppendLSPDiagnosticsToToolOutputForPaths("Applied patch", []string{edited}, true, nil, nil, nil, ""); strings.Contains(out, diag.Message) {
		t.Fatalf("out = %q, want the fixed problem gone for good", out)
	}
}

func TestRestoreReportedDiagnosticsSuppressesAlreadyShownLines(t *testing.T) {
	mgr, edited, other, key, uri, client := newDiagnosticOutputTestManager(t)
	diag := testProtocolDiagnostic("cached error", 0)
	publishTestDiagnostics(mgr, key, uri, client, 1, diag)

	shown := mgr.AppendLSPDiagnosticsToToolOutputForPaths("Applied patch", []string{edited}, true, nil, nil, nil, "")
	if !strings.Contains(shown, diag.Message) {
		t.Fatalf("shown = %q, want the diagnostic rendered once", shown)
	}

	// Resume: the transcript already contains the rendered section, so the
	// restored window must keep it suppressed instead of re-announcing it.
	msgs := []message.Message{{Role: "tool", Content: shown}}
	reported := RebuildReportedDiagnosticsFromMessages(msgs, filepath.Dir(other))
	if len(reported) == 0 {
		t.Fatalf("rebuild found no reported diagnostics in %q", shown)
	}
	mgr.RestoreReportedDiagnostics(reported)

	after := mgr.AppendLSPDiagnosticsToToolOutputForPaths("Applied patch", []string{edited}, true, nil, nil, nil, "")
	if strings.Contains(after, diag.Message) {
		t.Fatalf("after = %q, want no repeat of a diagnostic the transcript already showed", after)
	}
}
