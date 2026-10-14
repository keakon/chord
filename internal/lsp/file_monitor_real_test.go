package lsp

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
)

func TestFileMaintenanceRealGopls(t *testing.T) {
	// Each test owns its workspace and server processes.
	t.Parallel()
	gopls, err := exec.LookPath("gopls")
	if err != nil {
		t.Skip("gopls unavailable")
	}
	root := resolvedTempDir(t)
	path := writeBrokenGoCheckout(t, root, "example.com/monitor", "missingBeforeChange")
	m := NewManager(&config.Config{LSP: config.LSPConfig{"gopls": {Command: gopls, FileTypes: []string{"go"}}}}, root, nil)
	defer stopManager(m)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	m.MarkTouched(path)
	m.AfterFileWriteToolResult(ctx, path, string(content), "written", true, WatchedFileChanged, "")
	if !hasDiagnosticMessage(m.Diagnostics(path), "missingBeforeChange") {
		t.Fatal("initial diagnostic missing")
	}
	// Atomic replacement must release the old overlay and publish disk diagnostics.
	replacement := path + ".new"
	if err := os.WriteFile(replacement, []byte("package main\nvar _ = missingAfterChange\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}
	tick := time.NewTicker(25 * time.Millisecond)
	defer tick.Stop()
	for !hasDiagnosticMessage(m.Diagnostics(path), "missingAfterChange") {
		select {
		case <-ctx.Done():
			t.Fatal("external replacement was not diagnosed", ctx.Err())
		case <-tick.C:
		}
	}
	if hasDiagnosticMessage(m.Diagnostics(path), "missingBeforeChange") {
		t.Fatal("old diagnostic survived replacement")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	for len(m.TouchedPaths()) != 0 {
		select {
		case <-ctx.Done():
			t.Fatal("external deletion was not reconciled", ctx.Err())
		case <-tick.C:
		}
	}
	m.diagMu.RLock()
	retained := len(m.diagState) + len(m.reviewByServer) + len(m.publishedDiagByServer)
	m.diagMu.RUnlock()
	if retained != 0 || len(m.Diagnostics(path)) != 0 {
		t.Fatal("deleted file diagnostics retained")
	}
}
