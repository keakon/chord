package lsp

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/keakon/x/powernap/pkg/lsp/protocol"

	"github.com/keakon/chord/internal/config"
)

// TestRebindToProjectRootDropsPreviousCheckoutDiagnostics pins the invariant
// behind RebindToProjectRoot: once the working directory moves to another
// checkout, nothing recorded for the old one may survive. The sidebar aggregate
// and the review snapshots are keyed by server name and path, so a surviving
// entry would present the previous checkout's diagnostics as the new
// checkout's.
func TestRebindToProjectRootDropsPreviousCheckoutDiagnostics(t *testing.T) {
	previous := t.TempDir()
	next := t.TempDir()
	path := filepath.Join(previous, "main.go")
	tracked := normalizeWaiterPath(path)
	identity := diagnosticIdentity{severity: 1, line: 2, message: "boom"}

	mgr := NewManager(&config.Config{}, previous, nil)
	mgr.diagByServer[clientKey{name: "gopls", root: previous}] = map[string]diagCounts{
		string(protocol.URIFromPath(path)): {errors: 2},
	}
	mgr.publishedDiagByServer[clientKey{name: "gopls", root: previous}] = map[string]map[diagnosticIdentity]struct{}{
		tracked: {identity: {}},
	}
	mgr.reviewByServer["gopls"] = map[string]reviewCounts{tracked: {errors: 2}}
	mgr.diagState[tracked] = diagnosticPathState{stale: true}
	mgr.reportedByPath[tracked] = map[diagnosticIdentity]struct{}{identity: {}}
	mgr.MarkTouched(path)

	if err := mgr.RebindToProjectRoot(context.Background(), next); err != nil {
		t.Fatalf("RebindToProjectRoot() error = %v", err)
	}

	if got := mgr.TouchedPaths(); len(got) != 0 {
		t.Fatalf("TouchedPaths() = %v, want empty after the checkout switched", got)
	}
	if got := mgr.ReviewedPaths(); len(got) != 0 {
		t.Fatalf("ReviewedPaths() = %v, want empty after the checkout switched", got)
	}
	if got := mgr.CurrentReviewSnapshots(path); len(got) != 0 {
		t.Fatalf("CurrentReviewSnapshots(previous checkout) = %v, want none", got)
	}
	if got := mgr.diagByServer; len(got) != 0 {
		t.Fatalf("diagByServer = %v, want empty after the checkout switched", got)
	}
	if got := mgr.publishedDiagByServer; len(got) != 0 {
		t.Fatalf("publishedDiagByServer = %v, want empty after the checkout switched", got)
	}
	if got := mgr.diagState; len(got) != 0 {
		t.Fatalf("diagState = %v, want empty after the checkout switched", got)
	}
	if got := mgr.reportedByPath; len(got) != 0 {
		t.Fatalf("reportedByPath = %v, want empty after the checkout switched", got)
	}
}

// TestRebindToProjectRootKeepsStateForUnchangedRoot makes sure the reset is tied
// to a real checkout change: rebinding to the current root is a no-op.
func TestRebindToProjectRootKeepsStateForUnchangedRoot(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "main.go")

	mgr := NewManager(&config.Config{}, root, nil)
	mgr.MarkTouched(path)
	mgr.reviewByServer["gopls"] = map[string]reviewCounts{normalizeWaiterPath(path): {errors: 1}}

	if err := mgr.RebindToProjectRoot(context.Background(), root); err != nil {
		t.Fatalf("RebindToProjectRoot() error = %v", err)
	}

	if got := mgr.TouchedPaths(); len(got) != 1 {
		t.Fatalf("TouchedPaths() = %v, want the touched file kept for an unchanged root", got)
	}
	if got := mgr.ReviewedPaths(); len(got) != 1 {
		t.Fatalf("ReviewedPaths() = %v, want the reviewed file kept for an unchanged root", got)
	}
}

// TestRebindToProjectRootRerootsRealGopls drives the switch against a real
// language server: after the working directory moves, the new checkout gets
// diagnostics and the previous one is no longer served. gopls is optional, so
// the test skips when it is not installed.
func TestRebindToProjectRootRerootsRealGopls(t *testing.T) {
	gopls, err := exec.LookPath("gopls")
	if err != nil {
		t.Skip("gopls not installed: skipping the real language-server round trip")
	}
	previous := resolvedTempDir(t)
	next := resolvedTempDir(t)
	previousFile := writeBrokenGoCheckout(t, previous, "example.com/checkout-previous", "missingSymbolInPreviousCheckout")
	nextFile := writeBrokenGoCheckout(t, next, "example.com/checkout-next", "missingSymbolInNextCheckout")

	mgr := NewManager(&config.Config{LSP: config.LSPConfig{
		"gopls": {Command: gopls, FileTypes: []string{"go"}, RootMarkers: []string{"go.mod"}},
	}}, previous, nil)
	defer stopManager(mgr)

	// Each checkout gets its own budget: one slow server must not consume the
	// time the other phase needs.
	firstCtx, cancelFirst := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancelFirst()
	secondCtx, cancelSecond := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancelSecond()

	// A write in the first checkout starts a server rooted there and puts the
	// file in the session's touched set, which is what the sidebar aggregates.
	mgr.MarkTouched(previousFile)
	if diags := collectDiagnostics(t, firstCtx, mgr, previousFile); !hasDiagnosticMessage(diags, "missingSymbolInPreviousCheckout") {
		t.Fatalf("diagnostics for %s = %v, want the undefined symbol", previousFile, diags)
	}

	// The runtime bounds a switch to a few seconds so a wedged server cannot
	// stall it; the test does the same instead of handing Stop both phases'
	// remaining budget. The switch must also finish well inside it: Stop must
	// not hold clientsMu while waiting for the servers' shutdown replies, or
	// the close deadlocks against the notification handlers (diagMu ->
	// clientsMu) until this context expires.
	const rebindBudget = 20 * time.Second
	rebindCtx, cancelRebind := context.WithTimeout(context.Background(), rebindBudget)
	rebindStart := time.Now()
	rebindErr := mgr.RebindToProjectRoot(rebindCtx, next)
	rebindElapsed := time.Since(rebindStart)
	cancelRebind()
	if rebindErr != nil {
		t.Fatalf("RebindToProjectRoot() error = %v", rebindErr)
	}
	if rebindElapsed >= rebindBudget/2 {
		t.Fatalf("RebindToProjectRoot() took %v of a %v budget, want it to close the old checkout's servers promptly", rebindElapsed, rebindBudget)
	}
	if mgr.HasServerForPath(previousFile) {
		t.Fatalf("HasServerForPath(%s) = true after the checkout switched", previousFile)
	}
	if diags := mgr.Diagnostics(previousFile); len(diags) != 0 {
		t.Fatalf("Diagnostics(%s) = %v, want none after the checkout switched", previousFile, diags)
	}

	mgr.MarkTouched(nextFile)
	if diags := collectDiagnostics(t, secondCtx, mgr, nextFile); !hasDiagnosticMessage(diags, "missingSymbolInNextCheckout") {
		t.Fatalf("diagnostics for %s = %v, want the undefined symbol", nextFile, diags)
	}
	// The sidebar aggregate and the review snapshots must describe the current
	// checkout only.
	if got := mgr.TouchedPaths(); len(got) != 1 || got[0] != normalizeWaiterPath(nextFile) {
		t.Fatalf("TouchedPaths() = %v, want only the new checkout's file", got)
	}
	if got := mgr.ReviewedPaths(); len(got) != 1 || got[0] != normalizeWaiterPath(nextFile) {
		t.Fatalf("ReviewedPaths() = %v, want only the new checkout's file", got)
	}
	if got := mgr.CurrentReviewSnapshots(previousFile); len(got) != 0 {
		t.Fatalf("CurrentReviewSnapshots(previous checkout) = %v, want none", got)
	}
}

// stopManager tears down every server the test started so it does not leave
// language-server processes behind, bounded so a wedged server cannot hang the
// test binary.
func stopManager(mgr *Manager) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	mgr.Stop(ctx)
}

func resolvedTempDir(t *testing.T) string {
	t.Helper()
	// Resolve symlinks (/var -> /private/var on macOS) so the paths the server
	// reports back match the ones the manager tracks.
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve temp dir: %v", err)
	}
	return dir
}

func writeBrokenGoCheckout(t *testing.T, dir, module, symbol string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module "+module+"\n\ngo 1.27\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	path := filepath.Join(dir, "main.go")
	src := "package main\n\nfunc main() {\n\t" + symbol + "()\n}\n"
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatalf("write main.go: %v", err)
	}
	return path
}

// collectDiagnostics starts the servers for path if needed and returns the
// diagnostics published after the buffer was synced. Unlike
// AfterFileWriteToolResult it waits on the explicit timeout, so a cold server
// on a slow machine does not turn into a spurious failure.
func collectDiagnostics(t *testing.T, ctx context.Context, mgr *Manager, path string) []Diagnostic {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	mgr.Start(ctx, path)
	if _, ok := mgr.waitForClientForPath(ctx, path, time.Minute); !ok {
		t.Fatalf("no language server became available for %s: %v", path, mgr.startFailuresForPath(path))
	}
	ch := mgr.PrepareWaiter(path)
	after := time.Now()
	versions, err := mgr.DidChangeVersions(ctx, path, string(content))
	if err != nil {
		t.Fatalf("sync %s: %v", path, err)
	}
	diags, notified := mgr.AwaitFreshWaiter(ctx, path, ch, diagnosticsWaitRequest{serverVersions: versions, after: after}, 30*time.Second)
	if !notified && len(diags) == 0 {
		t.Fatalf("no diagnostics were published for %s within 30s", path)
	}
	// AfterFileWriteToolResult records a review snapshot once the publish is
	// settled; the sidebar aggregate and the model read that, not the raw
	// diagnostics, so the helper must do the same.
	mgr.recordReviewSnapshot(path)
	return diags
}

func hasDiagnosticMessage(diags []Diagnostic, message string) bool {
	for _, d := range diags {
		if strings.Contains(d.Message, message) {
			return true
		}
	}
	return false
}
