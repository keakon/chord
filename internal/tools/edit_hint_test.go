package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEditToolNotFoundErrorIncludesIndentHintForUniqueTrimmedMatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "main.go")
	// Use tabs in file; spaces in old_string.
	content := "\tif true {\n\t\tfmt.Println(\"hi\")\n\t}\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	args, err := json.Marshal(map[string]any{
		"path":       path,
		"old_string": "  if true {\n\t\tfmt.Println(\"hi\")\n\t}",
		"new_string": "  if false {\n\t\tfmt.Println(\"hi\")\n\t}",
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	_, err = (EditTool{}).Execute(context.Background(), args)
	if err == nil {
		t.Fatal("expected error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "oldString not found") {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(msg, "Indentation mismatch") {
		t.Fatalf("expected indentation hint, got: %v", err)
	}
}
