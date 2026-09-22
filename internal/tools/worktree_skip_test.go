package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorktreeSkipRel(t *testing.T) {
	root := filepath.Join(string(filepath.Separator), "repo")

	if _, ok := worktreeSkipRel(root, ""); ok {
		t.Error("empty worktree root must not prune")
	}
	if _, ok := worktreeSkipRel(root, root); ok {
		t.Error("the walk root itself must not be pruned: searching a worktree explicitly must work")
	}
	if rel, ok := worktreeSkipRel(root, filepath.Join(root, "sub")); !ok || rel != "sub" {
		t.Errorf("worktreeSkipRel = %q, %v; want sub, true", rel, ok)
	}
	if _, ok := worktreeSkipRel(filepath.Join(root, "sub"), root); ok {
		t.Error("a worktree root above the walk root must not be pruned")
	}
	if _, ok := worktreeSkipRel(root, filepath.Join(string(filepath.Separator), "elsewhere")); ok {
		t.Error("an unrelated directory must not be pruned")
	}
}

// TestGrepSkipsWorktreeContainer covers the D4 search rule: a search from the
// repository root must not report another checkout's copies, while explicitly
// searching the worktree still works.
func TestGrepSkipsWorktreeContainer(t *testing.T) {
	root := t.TempDir()
	wt := filepath.Join(root, "wt")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("needle\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, "b.txt"), []byte("needle\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := GrepTool{BaseDir: root, WorktreeRoot: wt}

	args, _ := json.Marshal(map[string]any{"pattern": "needle"})
	out, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "a.txt:1:") {
		t.Fatalf("root search missed the repository file, output:\n%s", out)
	}
	if strings.Contains(out, "b.txt") {
		t.Fatalf("root search reported a copy inside the worktree container, output:\n%s", out)
	}

	args, _ = json.Marshal(map[string]any{"pattern": "needle", "paths": []string{"wt"}})
	out, err = tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute(path=wt): %v", err)
	}
	if !strings.Contains(out, "b.txt:1:") {
		t.Fatalf("explicit search of the worktree missed its file, output:\n%s", out)
	}
}

// TestGlobSkipsWorktreeContainer is the Glob counterpart of
// TestGrepSkipsWorktreeContainer.
func TestGlobSkipsWorktreeContainer(t *testing.T) {
	root := t.TempDir()
	wt := filepath.Join(root, "wt")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, "b.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := GlobTool{BaseDir: root, WorktreeRoot: wt}

	args, _ := json.Marshal(map[string]any{"patterns": []string{"**/*.txt"}})
	out, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "a.txt") {
		t.Fatalf("root glob missed the repository file, output:\n%s", out)
	}
	if strings.Contains(out, "b.txt") {
		t.Fatalf("root glob reported a copy inside the worktree container, output:\n%s", out)
	}

	args, _ = json.Marshal(map[string]any{"patterns": []string{"**/*.txt"}, "path": "wt"})
	out, err = tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute(path=wt): %v", err)
	}
	if !strings.Contains(out, "b.txt") {
		t.Fatalf("explicit glob of the worktree missed its file, output:\n%s", out)
	}
}
