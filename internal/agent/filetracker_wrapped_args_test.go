package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func TestMainAgent_ConsecutiveEditsWithWrappedArgsDoNotTriggerStaleRead(t *testing.T) {
	projectRoot := t.TempDir()
	path := filepath.Join(projectRoot, "demo.txt")
	if err := os.WriteFile(path, []byte("before"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	a := newTestMainAgent(t, projectRoot)
	a.tools.Register(tools.ReadTool{})
	a.tools.Register(tools.EditTool{})

	// First, perform a normal Read so FileTracker has a baseline.
	readArgs, err := json.Marshal(map[string]any{"path": path})
	if err != nil {
		t.Fatalf("Marshal read args: %v", err)
	}
	if _, err := a.executeToolCall(a.parentCtx, message.ToolCall{ID: "read-1", Name: tools.NameRead, Args: readArgs}); err != nil {
		t.Fatalf("Read failed: %v", err)
	}

	// Now run two consecutive Edit calls whose args are JSON-string-wrapped.
	// This matches providers that send tool arguments as a string.
	edit1Obj := map[string]any{"path": path, "old_string": "before", "new_string": "after"}
	edit1Raw, err := json.Marshal(edit1Obj)
	if err != nil {
		t.Fatalf("Marshal edit1 args: %v", err)
	}
	wrapped1, err := json.Marshal(string(edit1Raw))
	if err != nil {
		t.Fatalf("Marshal wrapped edit1 args: %v", err)
	}
	if _, err := a.executeToolCall(a.parentCtx, message.ToolCall{ID: "edit-1", Name: tools.NameEdit, Args: wrapped1}); err != nil {
		t.Fatalf("Edit-1 failed: %v", err)
	}

	edit2Obj := map[string]any{"path": path, "old_string": "after", "new_string": "final"}
	edit2Raw, err := json.Marshal(edit2Obj)
	if err != nil {
		t.Fatalf("Marshal edit2 args: %v", err)
	}
	wrapped2, err := json.Marshal(string(edit2Raw))
	if err != nil {
		t.Fatalf("Marshal wrapped edit2 args: %v", err)
	}
	if _, err := a.executeToolCall(a.parentCtx, message.ToolCall{ID: "edit-2", Name: tools.NameEdit, Args: wrapped2}); err != nil {
		t.Fatalf("Edit-2 failed: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if got := string(data); got != "final" {
		t.Fatalf("file content = %q, want final", got)
	}
}
