package lsp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
)

// TestRealGoplsReportsAnalyzerFindingAfterTheWriteWait drives the late path
// against a real server: gopls reports the printf analyzer finding in a later
// publish than the type-check pass, so it must be claimed after the write
// result returned rather than being lost with the wait.
func TestRealGoplsReportsAnalyzerFindingAfterTheWriteWait(t *testing.T) {
	t.Parallel()
	gopls := requireRealGopls(t)
	root := resolvedTempDir(t)
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/late\n\ngo 1.27\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "main.go")
	src := `package main

import "fmt"

func main() {
	fmt.Printf("%d\n", "x")
}
`
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	mgr := NewManager(&config.Config{LSP: config.LSPConfig{
		"gopls": {Command: gopls, FileTypes: []string{"go"}, RootMarkers: []string{"go.mod"}},
	}}, root, nil)
	defer stopManager(mgr)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	mgr.AfterFileWriteToolResult(ctx, path, src, "wrote main.go", true, WatchedFileChanged, "")
	start := time.Now()
	immediate := mgr.AfterFileWriteToolResult(ctx, path, src, "wrote main.go", true, WatchedFileChanged, "")
	t.Logf("immediate result after %v: %q", time.Since(start), immediate)

	deadline := time.Now().Add(15 * time.Second)
	for {
		items := mgr.PendingLateDiagnostics([]string{path}, root, "")
		for _, item := range items {
			t.Logf("late item after %v: %q", time.Since(start), item.Lines)
			if strings.Contains(item.Lines, "wrong type") || strings.Contains(item.Lines, "Printf") {
				mgr.AckLateDiagnostics(items)
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("no late printf diagnostic within 15s; immediate=%q", immediate)
		}
		time.Sleep(200 * time.Millisecond)
	}
}
