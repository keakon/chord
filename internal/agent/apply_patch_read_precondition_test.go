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

func TestMainAgent_ApplyPatchRequiresReadFirst(t *testing.T) {
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
	a.tools.Register(tools.ApplyPatchTool{})

	patchArgs, err := json.Marshal(map[string]any{"patch": "*** Begin Patch\n*** Update File: demo.txt\n@@\n-before\n+after\n*** End Patch\n"})
	if err != nil {
		t.Fatalf("Marshal patch args: %v", err)
	}
	result, err := a.executeToolCall(context.Background(), message.ToolCall{ID: "patch-1", Name: tools.NameApplyPatch, Args: patchArgs})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "has not been read") {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.PreFilePath != "" || result.PreContent != "" || result.PreExisted {
		t.Fatalf("unread ApplyPatch captured pre-write state before read precondition: path=%q existed=%v content=%q", result.PreFilePath, result.PreExisted, result.PreContent)
	}

	readArgs, err := json.Marshal(map[string]any{"path": path})
	if err != nil {
		t.Fatalf("Marshal read args: %v", err)
	}
	if _, err := a.executeToolCall(context.Background(), message.ToolCall{ID: "read-1", Name: tools.NameRead, Args: readArgs}); err != nil {
		t.Fatalf("Read failed: %v", err)
	}

	if _, err := a.executeToolCall(context.Background(), message.ToolCall{ID: "patch-2", Name: tools.NameApplyPatch, Args: patchArgs}); err != nil {
		t.Fatalf("ApplyPatch after Read failed: %v", err)
	}
}
