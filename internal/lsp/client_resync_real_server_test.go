package lsp

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
)

// TestManagerResyncFileWithRealGopls proves that a resync pushes the given
// bytes to a live server: the content it sends is deliberately different from
// the file on disk, so only the didChange notification can make gopls report
// the new symbol. gopls is optional, so the test skips when it is not
// installed.
func TestManagerResyncFileWithRealGopls(t *testing.T) {
	gopls, err := exec.LookPath("gopls")
	if err != nil {
		t.Skip("gopls not installed: skipping the real language-server round trip")
	}
	root := resolvedTempDir(t)
	path := writeBrokenGoCheckout(t, root, "example.com/resync", "missingSymbolBeforeResync")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	mgr := NewManager(&config.Config{LSP: config.LSPConfig{
		"gopls": {Command: gopls, FileTypes: []string{"go"}, RootMarkers: []string{"go.mod"}},
	}}, root, nil)
	defer stopManager(mgr)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// Warm-up write: gopls opens the document with the on-disk text.
	mgr.AfterFileWriteToolResult(ctx, path, string(content), "wrote main.go", true, WatchedFileChanged, "")
	mgr.clientsMu.RLock()
	_, ok := mgr.clientForPathLocked(path)
	mgr.clientsMu.RUnlock()
	if !ok {
		t.Fatal("no gopls client is serving the written file")
	}

	resynced := strings.Replace(string(content), "missingSymbolBeforeResync", "missingSymbolAfterResync", 1)
	if resynced == string(content) {
		t.Fatal("resync fixture does not differ from the on-disk content")
	}
	mgr.ResyncFile(ctx, path, resynced)

	deadline := time.Now().Add(30 * time.Second)
	for {
		diags := mgr.currentFileDiagnostics(path)
		if hasDiagnosticMessage(diags, "missingSymbolAfterResync") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the resynced content never reached gopls; diagnostics = %v", diags)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
