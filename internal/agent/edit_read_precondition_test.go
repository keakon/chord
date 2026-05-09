package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func TestMainAgent_EditRequiresReadFirst(t *testing.T) {
	projectRoot := t.TempDir()
	path := filepath.Join(projectRoot, "demo.txt")
	if err := os.WriteFile(path, []byte("before"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	a := newTestMainAgent(t, projectRoot)
	a.tools.Register(tools.ReadTool{})
	a.tools.Register(tools.EditTool{})

	editArgs, err := json.Marshal(map[string]any{"path": path, "old_string": "before", "new_string": "after"})
	if err != nil {
		t.Fatalf("Marshal edit args: %v", err)
	}
	_, err = a.executeToolCall(context.Background(), message.ToolCall{ID: "edit-1", Name: tools.NameEdit, Args: editArgs})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "has not been read") {
		t.Fatalf("unexpected error: %v", err)
	}

	readArgs, err := json.Marshal(map[string]any{"path": path})
	if err != nil {
		t.Fatalf("Marshal read args: %v", err)
	}
	if _, err := a.executeToolCall(context.Background(), message.ToolCall{ID: "read-1", Name: tools.NameRead, Args: readArgs}); err != nil {
		t.Fatalf("Read failed: %v", err)
	}

	if _, err := a.executeToolCall(context.Background(), message.ToolCall{ID: "edit-2", Name: tools.NameEdit, Args: editArgs}); err != nil {
		t.Fatalf("Edit after Read failed: %v", err)
	}
}
