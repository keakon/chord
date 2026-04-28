package tools

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/lsp"
)

func TestWriteToolMarksTouchedFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "new.txt")
	mgr := lsp.NewManager(&config.Config{}, dir, nil)
	args, err := json.Marshal(map[string]any{"path": path, "content": "hello\n"})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if _, err := (WriteTool{LSP: mgr}).Execute(context.Background(), args); err != nil {
		t.Fatalf("WriteTool.Execute: %v", err)
	}
	want := []string{path}
	if got := mgr.TouchedPaths(); !reflect.DeepEqual(got, want) {
		t.Fatalf("TouchedPaths() = %#v, want %#v", got, want)
	}
}

func TestEditToolMarksTouchedFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "legacy.txt")
	if _, err := (WriteTool{}).Execute(context.Background(), mustJSON(t, map[string]any{"path": path, "content": "before\n"})); err != nil {
		t.Fatalf("seed WriteTool.Execute: %v", err)
	}
	mgr := lsp.NewManager(&config.Config{}, dir, nil)
	args, err := json.Marshal(map[string]any{"path": path, "old_string": "before", "new_string": "after"})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if _, err := (EditTool{LSP: mgr}).Execute(context.Background(), args); err != nil {
		t.Fatalf("EditTool.Execute: %v", err)
	}
	want := []string{path}
	if got := mgr.TouchedPaths(); !reflect.DeepEqual(got, want) {
		t.Fatalf("TouchedPaths() = %#v, want %#v", got, want)
	}
}

func TestWriteToolAppendsPyrightDiagnosticsForSyntaxError(t *testing.T) {
	if os.Getenv("CHORD_RUN_REAL_PYRIGHT_TESTS") != "1" {
		t.Skip("set CHORD_RUN_REAL_PYRIGHT_TESTS=1 to run real pyright integration")
	}

	pyrightPath, err := execLookPath("pyright-langserver")
	if err != nil {
		t.Skip("pyright-langserver not installed")
	}

	repoRoot := repoRootForTest(t)
	dir, err := os.MkdirTemp(repoRoot, "pyright-write-test-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatalf("Mkdir .git: %v", err)
	}

	mgr := lsp.NewManager(&config.Config{
		LSP: config.LSPConfig{
			"pyright": {
				Command:     pyrightPath,
				Args:        []string{"--stdio"},
				FileTypes:   []string{".py", ".pyi"},
				RootMarkers: []string{".git"},
			},
		},
	}, dir, nil)
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		mgr.Stop(stopCtx)
	})

	path := filepath.Join(dir, "broken.py")
	args := mustJSON(t, map[string]any{
		"path":    path,
		"content": "def broken(:\n    pass\n",
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := (WriteTool{LSP: mgr}).Execute(ctx, args)
	if err != nil {
		t.Fatalf("WriteTool.Execute: %v", err)
	}
	if !strings.Contains(out, "Successfully wrote") {
		t.Fatalf("result = %q, want write success prefix", out)
	}
	if !strings.Contains(out, "[E]") || strings.Contains(strings.ToLower(out), "[pyright]") {
		t.Fatalf("result = %q, want pyright error diagnostics", out)
	}
}

func TestDeleteToolUnmarksTouchedFileWhenAlreadyAbsent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "absent.txt")
	mgr := lsp.NewManager(&config.Config{}, dir, nil)
	mgr.MarkTouched(path)
	args, err := json.Marshal(map[string]any{"paths": []string{path}, "reason": "cleanup"})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if _, err := (DeleteTool{LSP: mgr}).Execute(context.Background(), args); err != nil {
		t.Fatalf("DeleteTool.Execute: %v", err)
	}
	if got := mgr.TouchedPaths(); len(got) != 0 {
		t.Fatalf("TouchedPaths() = %#v, want empty", got)
	}
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	return b
}

var execLookPath = func(file string) (string, error) {
	return exec.LookPath(file)
}

func repoRootForTest(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	root, err := filepath.Abs(filepath.Join(wd, "..", ".."))
	if err != nil {
		t.Fatalf("Abs repo root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".git")); err != nil {
		t.Fatalf("repo root %q missing .git: %v", root, err)
	}
	return root
}
