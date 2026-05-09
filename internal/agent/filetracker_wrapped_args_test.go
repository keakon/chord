package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func TestMainAgent_EditRequiresPriorRead(t *testing.T) {
	projectRoot := t.TempDir()
	path := filepath.Join(projectRoot, "demo.txt")
	if err := os.WriteFile(path, []byte("before"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	a := newTestMainAgent(t, projectRoot)
	a.tools.Register(tools.EditTool{})

	editArgs, err := json.Marshal(map[string]any{"path": path, "old_string": "before", "new_string": "after"})
	if err != nil {
		t.Fatalf("Marshal edit args: %v", err)
	}
	_, err = a.executeToolCall(a.parentCtx, message.ToolCall{ID: "edit-1", Name: tools.NameEdit, Args: editArgs})
	if err == nil {
		t.Fatal("expected edit precondition error")
	}
	for _, want := range []string{
		"has not been read in this conversation",
		"use Read on this file before editing",
		"small unique old_string block",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("err = %q, want substring %q", err.Error(), want)
		}
	}
}

func TestMainAgent_EditReadPreconditionTreatsEquivalentRelativeAndAbsolutePathsAsSameFile(t *testing.T) {
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

	a := newTestMainAgent(t, projectRoot)
	a.tools.Register(tools.ReadTool{})
	a.tools.Register(tools.EditTool{})

	readArgs, err := json.Marshal(map[string]any{"path": "./demo.txt"})
	if err != nil {
		t.Fatalf("Marshal read args: %v", err)
	}
	if _, err := a.executeToolCall(a.parentCtx, message.ToolCall{ID: "read-1", Name: tools.NameRead, Args: readArgs}); err != nil {
		t.Fatalf("Read failed: %v", err)
	}

	editArgs, err := json.Marshal(map[string]any{"path": "demo.txt", "old_string": "before", "new_string": "after"})
	if err != nil {
		t.Fatalf("Marshal edit args: %v", err)
	}
	if _, err := a.executeToolCall(a.parentCtx, message.ToolCall{ID: "edit-1", Name: tools.NameEdit, Args: editArgs}); err != nil {
		t.Fatalf("equivalent paths should satisfy read precondition: %v", err)
	}
}

func TestMainAgent_ConsecutiveEditsWithWrappedArgsDoNotTriggerStaleRead(t *testing.T) {
	projectRoot := t.TempDir()
	path := filepath.Join(projectRoot, "demo.txt")
	if err := os.WriteFile(path, []byte("before"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	a := newTestMainAgent(t, projectRoot)
	a.tools.Register(tools.ReadTool{})
	a.tools.Register(tools.EditTool{})

	readArgs, err := json.Marshal(map[string]any{"path": path})
	if err != nil {
		t.Fatalf("Marshal read args: %v", err)
	}
	if _, err := a.executeToolCall(a.parentCtx, message.ToolCall{ID: "read-1", Name: tools.NameRead, Args: readArgs}); err != nil {
		t.Fatalf("Read failed: %v", err)
	}

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

func TestMainAgent_EditReportsDiskDriftWithRereadGuidance(t *testing.T) {
	projectRoot := t.TempDir()
	path := filepath.Join(projectRoot, "demo.txt")
	if err := os.WriteFile(path, []byte("before"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	a := newTestMainAgent(t, projectRoot)
	a.tools.Register(tools.ReadTool{})
	a.tools.Register(tools.EditTool{})

	readArgs, err := json.Marshal(map[string]any{"path": path})
	if err != nil {
		t.Fatalf("Marshal read args: %v", err)
	}
	if _, err := a.executeToolCall(a.parentCtx, message.ToolCall{ID: "read-1", Name: tools.NameRead, Args: readArgs}); err != nil {
		t.Fatalf("Read failed: %v", err)
	}

	if err := os.WriteFile(path, []byte("externally changed"), 0o644); err != nil {
		t.Fatalf("WriteFile external change: %v", err)
	}

	editArgs, err := json.Marshal(map[string]any{"path": path, "old_string": "before", "new_string": "after"})
	if err != nil {
		t.Fatalf("Marshal edit args: %v", err)
	}
	_, err = a.executeToolCall(a.parentCtx, message.ToolCall{ID: "edit-1", Name: tools.NameEdit, Args: editArgs})
	if err == nil {
		t.Fatal("expected stale-read/disk-drift error")
	}
	for _, want := range []string{
		"changed on disk since the last read",
		"re-read this file before editing",
		"latest content",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("err = %q, want substring %q", err.Error(), want)
		}
	}
}
