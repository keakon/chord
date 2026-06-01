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

	a := newTestMainAgent(t, projectRoot)
	a.tools.Register(tools.EditTool{})

	patchArgs, err := json.Marshal(map[string]any{"path": "demo.txt", "patch": "@@\n-before\n+after\n"})
	if err != nil {
		t.Fatalf("Marshal patch args: %v", err)
	}
	_, err = a.executeToolCall(a.parentCtx, message.ToolCall{ID: "patch-1", Name: tools.NameEdit, Args: patchArgs})
	if err == nil {
		t.Fatal("expected Edit precondition error")
	}
	for _, want := range []string{
		"has not been observed in this conversation",
		"use Read or a system-resolved @file mention before Edit",
		"small unique patch hunk",
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

	patchArgs, err := json.Marshal(map[string]any{"path": "demo.txt", "patch": "@@\n-before\n+after\n"})
	if err != nil {
		t.Fatalf("Marshal patch args: %v", err)
	}
	if _, err := a.executeToolCall(a.parentCtx, message.ToolCall{ID: "patch-1", Name: tools.NameEdit, Args: patchArgs}); err != nil {
		t.Fatalf("equivalent paths should satisfy read precondition: %v", err)
	}
}

func TestMainAgent_ConsecutiveEditesWithWrappedArgsDoNotTriggerStaleRead(t *testing.T) {
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

	patch1Obj := map[string]any{"path": filepath.Base(path), "patch": "@@\n-before\n+after\n"}
	patch1Raw, err := json.Marshal(patch1Obj)
	if err != nil {
		t.Fatalf("Marshal patch1 args: %v", err)
	}
	wrapped1, err := json.Marshal(string(patch1Raw))
	if err != nil {
		t.Fatalf("Marshal wrapped patch1 args: %v", err)
	}
	if _, err := a.executeToolCall(a.parentCtx, message.ToolCall{ID: "patch-1", Name: tools.NameEdit, Args: wrapped1}); err != nil {
		t.Fatalf("Edit-1 failed: %v", err)
	}

	patch2Obj := map[string]any{"path": filepath.Base(path), "patch": "@@\n-after\n+final\n"}
	patch2Raw, err := json.Marshal(patch2Obj)
	if err != nil {
		t.Fatalf("Marshal patch2 args: %v", err)
	}
	wrapped2, err := json.Marshal(string(patch2Raw))
	if err != nil {
		t.Fatalf("Marshal wrapped patch2 args: %v", err)
	}
	if _, err := a.executeToolCall(a.parentCtx, message.ToolCall{ID: "patch-2", Name: tools.NameEdit, Args: wrapped2}); err != nil {
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

	patchArgs, err := json.Marshal(map[string]any{"path": "demo.txt", "patch": "@@\n-before\n+after\n"})
	if err != nil {
		t.Fatalf("Marshal patch args: %v", err)
	}
	_, err = a.executeToolCall(a.parentCtx, message.ToolCall{ID: "patch-1", Name: tools.NameEdit, Args: patchArgs})
	if err == nil {
		t.Fatal("expected stale-read/disk-drift error")
	}
	for _, want := range []string{
		"hunk not found",
		"Re-read the target area",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("err = %q, want substring %q", err.Error(), want)
		}
	}
}
