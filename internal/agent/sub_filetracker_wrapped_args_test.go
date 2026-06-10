package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func TestSubAgent_ConsecutiveEditesWithWrappedArgsDoNotTriggerStaleReadInFinalizePath(t *testing.T) {
	projectRoot := t.TempDir()
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	defer func() { _ = os.Chdir(oldWD) }()
	if err := os.Chdir(projectRoot); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	path := filepath.Join(projectRoot, "demo.txt")
	if err := os.WriteFile(path, []byte("before"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	parent := newTestMainAgent(t, projectRoot)
	parent.tools.Register(tools.ReadTool{})
	parent.tools.Register(tools.EditTool{})

	sub := newControllableTestSubAgent(t, parent, "task-1")

	// Baseline Read (unwrapped args).
	readArgs, err := json.Marshal(map[string]any{"path": path})
	if err != nil {
		t.Fatalf("Marshal read args: %v", err)
	}
	if _, err := sub.executeToolCallWithHook(context.Background(), message.ToolCall{ID: "read-1", Name: tools.NameRead, Args: readArgs}, false); err != nil {
		t.Fatalf("Read failed: %v", err)
	}

	// Two consecutive Edites whose args are JSON-string-wrapped.
	patch1Obj := map[string]any{"path": "demo.txt", "patch": "@@\n-before\n+after\n"}
	patch1Raw, err := json.Marshal(patch1Obj)
	if err != nil {
		t.Fatalf("Marshal patch1 args: %v", err)
	}
	wrapped1, err := json.Marshal(string(patch1Raw))
	if err != nil {
		t.Fatalf("Marshal wrapped patch1 args: %v", err)
	}
	if _, err := sub.executeToolCallWithHook(context.Background(), message.ToolCall{ID: "patch-1", Name: tools.NameEdit, Args: wrapped1}, false); err != nil {
		t.Fatalf("Edit-1 failed: %v", err)
	}

	patch2Obj := map[string]any{"path": "demo.txt", "patch": "@@\n-after\n+final\n"}
	patch2Raw, err := json.Marshal(patch2Obj)
	if err != nil {
		t.Fatalf("Marshal patch2 args: %v", err)
	}
	wrapped2, err := json.Marshal(string(patch2Raw))
	if err != nil {
		t.Fatalf("Marshal wrapped patch2 args: %v", err)
	}
	if _, err := sub.executeToolCallWithHook(context.Background(), message.ToolCall{ID: "patch-2", Name: tools.NameEdit, Args: wrapped2}, false); err != nil {
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

func TestSubAgent_ConsecutiveEditsTreatEquivalentRelativeAndAbsolutePathsAsSameFile(t *testing.T) {
	projectRoot := t.TempDir()
	path := filepath.Join(projectRoot, "demo.txt")
	if err := os.WriteFile(path, []byte("before"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	defer func() { _ = os.Chdir(oldwd) }()
	if err := os.Chdir(projectRoot); err != nil {
		t.Fatalf("Chdir: %v", err)
	}

	parent := newTestMainAgent(t, projectRoot)
	parent.tools.Register(tools.ReadTool{})
	parent.tools.Register(tools.EditTool{})
	sub := newControllableTestSubAgent(t, parent, "task-1")

	readArgs, err := json.Marshal(map[string]any{"path": "./demo.txt"})
	if err != nil {
		t.Fatalf("Marshal read args: %v", err)
	}
	if _, err := sub.executeToolCallWithHook(context.Background(), message.ToolCall{ID: "read-1", Name: tools.NameRead, Args: readArgs}, false); err != nil {
		t.Fatalf("Read failed: %v", err)
	}

	patchArgs, err := json.Marshal(map[string]any{"path": "demo.txt", "patch": "@@\n-before\n+after\n"})
	if err != nil {
		t.Fatalf("Marshal patch args: %v", err)
	}
	if _, err := sub.executeToolCallWithHook(context.Background(), message.ToolCall{ID: "patch-1", Name: tools.NameEdit, Args: patchArgs}, false); err != nil {
		t.Fatalf("Edit failed: %v", err)
	}
}
