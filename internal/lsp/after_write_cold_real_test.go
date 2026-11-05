package lsp

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
)

func TestRealGoplsColdTimeoutClaimsFirstPublish(t *testing.T) {
	gopls := requireRealGopls(t)
	restoreAfterWriteHooks(t)
	root := resolvedTempDir(t)
	path := writeBrokenGoCheckout(t, root, "example.com/cold", "missingSampleSymbol")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	m := NewManager(&config.Config{LSP: config.LSPConfig{
		"gopls": {Command: gopls, FileTypes: []string{"go"}, RootMarkers: []string{"go.mod"}},
	}}, root, nil)
	defer stopManager(m)
	// Hold admission waiting past the foreground deadline; the underlying
	// server and its eventual first diagnostics remain real.
	afterWriteWaitForClient = func(_ *Manager, ctx context.Context, _ string, timeout time.Duration) (*Client, bool) {
		select {
		case <-ctx.Done():
		case <-time.After(timeout):
		}
		return nil, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	began := time.Now()
	out := m.AfterFileWriteToolResult(ctx, path, string(content), "wrote main.go", true, WatchedFileChanged, "")
	if elapsed := time.Since(began); elapsed > coldStartWriteBudget+500*time.Millisecond {
		t.Fatalf("cold write took %v, exceeds foreground budget tolerance", elapsed)
	}
	if !strings.Contains(out, "diagnostics unavailable (") {
		t.Fatalf("forced cold timeout returned verified result: %q", out)
	}
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	deadline := time.NewTimer(15 * time.Second)
	defer deadline.Stop()
	for {
		items := m.PendingLateDiagnostics([]string{path}, root, "")
		for _, item := range items {
			if strings.Contains(item.Lines, "missingSampleSymbol") {
				m.AckLateDiagnostics(items)
				if !slices.Contains(m.ReviewedPaths(), path) {
					t.Fatal("claimed real publish did not create review membership")
				}
				t.Logf("real cold timeout and first-publish claim completed in %v", time.Since(began))
				return
			}
		}
		select {
		case <-tick.C:
		case <-deadline.C:
			m.clientsMu.RLock()
			t.Logf("clients=%d launches=%d tasks=%d", len(m.clients), len(m.launches), len(m.syncTasks))
			m.clientsMu.RUnlock()
			m.lateMu.Lock()
			t.Logf("late watches=%+v publish buffer=%+v", m.lateWatches, m.latePublishes)
			m.lateMu.Unlock()
			t.Fatalf("no first real publish claimed; pending=%d failures=%v diagnostics=%v output=%q", m.pendingSyncCount(), formattedStartFailures(m, path), m.Diagnostics(path), out)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
}

func TestRealTypeScriptLargeFileIncludesSemanticDiagnostic(t *testing.T) {
	if testing.Short() {
		t.Skip("real language-server round trip is disabled in short mode")
	}
	server, err := exec.LookPath("typescript-language-server")
	if err != nil {
		t.Skip("typescript-language-server not installed")
	}
	root := resolvedTempDir(t)
	if err := os.WriteFile(filepath.Join(root, "tsconfig.json"), []byte(`{"compilerOptions":{"strict":true}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "main.ts")
	var source strings.Builder
	for range 5000 {
		source.WriteString("{ const sample: number = 1; void sample; }\n")
	}
	source.WriteString("const invalidSample: number = 'text';\n")
	content := source.String()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	// This test checks semantic coverage, not the interactive wait budget.
	// Real workspace analysis can exceed the default wait under suite load.
	m := NewManager(&config.Config{
		LSP: config.LSPConfig{
			"typescript": {Command: server, Args: []string{"--stdio"}, FileTypes: []string{".ts"}},
		},
		Diagnostics: config.DiagnosticsConfig{LSP: config.LSPDiagnosticsConfig{WaitTimeoutMS: new(15000)}},
	}, root, nil)
	defer stopManager(m)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	m.Start(ctx, path)
	if _, ok := m.waitForClientForPath(ctx, path, 30*time.Second); !ok {
		t.Fatalf("TypeScript server unavailable: %v", formattedStartFailures(m, path))
	}
	m.clientsMu.Lock()
	delete(m.admittedAt, testKey(m, "typescript"))
	m.clientsMu.Unlock()
	out := m.AfterFileWriteToolResult(ctx, path, content, "wrote main.ts", true, WatchedFileChanged, "")
	if !strings.Contains(out, "not assignable") {
		t.Fatalf("large-file semantic diagnostics were omitted: %q", out)
	}
}
