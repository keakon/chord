package lsp

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
)

// recordingSaveClient wraps the real powernap client so the test can observe the
// didSave notifications that reach the server.
type recordingSaveClient struct {
	lspProcessClient
	saves []string
}

func (r *recordingSaveClient) NotifyDidSaveTextDocument(ctx context.Context, uri string, text *string) error {
	r.saves = append(r.saves, uri)
	return r.lspProcessClient.NotifyDidSaveTextDocument(ctx, uri, text)
}

// TestAfterFileWriteToolResultSendsDidSaveToRealGopls drives the write path
// against a real language server: gopls must declare save support, receive
// exactly one didSave for the written file, and still publish its diagnostics.
// gopls is optional, so the test skips when it is not installed.
func TestAfterFileWriteToolResultSendsDidSaveToRealGopls(t *testing.T) {
	gopls, err := exec.LookPath("gopls")
	if err != nil {
		t.Skip("gopls not installed: skipping the real language-server round trip")
	}
	root := resolvedTempDir(t)
	path := writeBrokenGoCheckout(t, root, "example.com/didsave", "missingSymbolInDidSave")
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

	// Warm-up write: starts gopls and syncs the buffer, so the observed round
	// trip below runs against an initialized server and a hot diagnostics path.
	mgr.AfterFileWriteToolResult(ctx, path, string(content), "wrote main.go", true, WatchedFileChanged, "")

	mgr.clientsMu.RLock()
	client, ok := mgr.clientForPathLocked(path)
	mgr.clientsMu.RUnlock()
	if !ok {
		t.Fatal("no gopls client is serving the written file")
	}
	if _, requested := client.client.SaveOptions(); !requested {
		t.Fatal("gopls declared no save capability; the didSave gate would never open")
	}

	recorder := &recordingSaveClient{lspProcessClient: client.client}
	client.lifecycleMu.Lock()
	client.client = recorder
	client.lifecycleMu.Unlock()

	mgr.AfterFileWriteToolResult(ctx, path, string(content), "wrote main.go", true, WatchedFileChanged, "")
	if len(recorder.saves) != 1 || recorder.saves[0] != client.pathToURI(path) {
		t.Fatalf("didSave notifications = %v, want exactly [%s]", recorder.saves, client.pathToURI(path))
	}

	// The extra notification must not disturb the diagnostics path.
	diags := collectDiagnostics(t, ctx, mgr, path)
	if !hasDiagnosticMessage(diags, "missingSymbolInDidSave") {
		t.Fatalf("diagnostics for %s = %v, want the undefined symbol", path, diags)
	}
}
