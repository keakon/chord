package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEditToolErrorUsesSnakeCaseForIdenticalOldNew(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "demo.txt")
	if err := os.WriteFile(path, []byte("hello"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	args, err := json.Marshal(map[string]any{
		"path":       path,
		"old_string": "hello",
		"new_string": "hello",
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	_, err = (EditTool{}).Execute(context.Background(), args)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "old_string and new_string are identical") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestEditToolErrorUsesSnakeCaseForReplaceAllGuidance(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "demo.txt")
	if err := os.WriteFile(path, []byte("foo\nfoo\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	args, err := json.Marshal(map[string]any{
		"path":       path,
		"old_string": "foo",
		"new_string": "bar",
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	_, err = (EditTool{}).Execute(context.Background(), args)
	if err == nil {
		t.Fatal("expected error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "old_string found") || !strings.Contains(msg, "replace_all") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestEditToolTrailingNewlineToleranceEditsWhenUnique(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "demo.txt")
	if err := os.WriteFile(path, []byte("hello"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	args, err := json.Marshal(map[string]any{
		"path":       path,
		"old_string": "hello\n",
		"new_string": "world\n",
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	out, err := (EditTool{}).Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("EditTool.Execute: %v", err)
	}
	if !strings.Contains(out, "Replaced 1 occurrence") {
		t.Fatalf("unexpected output: %q", out)
	}
	data, rerr := os.ReadFile(path)
	if rerr != nil {
		t.Fatalf("ReadFile: %v", rerr)
	}
	if got := string(data); got != "world" {
		t.Fatalf("file content = %q, want world", got)
	}
}
