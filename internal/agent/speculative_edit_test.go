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

// TestStreamingToolExecutor_ReplaceEditSpeculativeExecution verifies that
// EditTool works correctly in speculative execution:
// 1. Path extraction from args works
// 2. Pre-write state is captured
// 3. File changes are applied
func TestStreamingToolExecutor_ReplaceEditSpeculativeExecution(t *testing.T) {
	projectRoot := t.TempDir()
	path := filepath.Join(projectRoot, "demo.txt")
	if err := os.WriteFile(path, []byte("original\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// Change to project root
	oldwd, _ := os.Getwd()
	defer func() { _ = os.Chdir(oldwd) }()
	if err := os.Chdir(projectRoot); err != nil {
		t.Fatalf("Chdir: %v", err)
	}

	a := newTestMainAgent(t, projectRoot)
	a.tools.Register(tools.EditTool{BaseDir: projectRoot})

	ctx := context.Background()

	// Execute through normal pipeline (which handles both regular and speculative)
	editArgs := map[string]any{
		"path":       "demo.txt",
		"old_string": "original\n",
		"new_string": "modified\n",
	}
	argsJSON, _ := json.Marshal(editArgs)
	call := message.ToolCall{ID: "edit-1", Name: tools.NameEdit, Args: argsJSON}

	result, err := a.executeToolCall(ctx, call)
	if err != nil {
		t.Fatalf("executeToolCall failed: %v", err)
	}

	// Verify pre-write state was captured
	if result.PreFilePath == "" {
		t.Fatal("PreFilePath not captured")
	}
	if result.PreContent != "original\n" {
		t.Fatalf("PreContent = %q, want %q", result.PreContent, "original\n")
	}
	if !result.PreExisted {
		t.Fatal("PreExisted should be true")
	}

	// Verify file was modified
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile after edit: %v", err)
	}
	if string(content) != "modified\n" {
		t.Fatalf("file content after edit = %q, want %q", content, "modified\n")
	}
}

// TestStreamingToolExecutor_ReplaceEditPathExtraction verifies that
// ExtractEditPathFromArgsInDir correctly extracts path from EditTool args
func TestStreamingToolExecutor_ReplaceEditPathExtraction(t *testing.T) {
	projectRoot := t.TempDir()

	tests := []struct {
		name     string
		args     map[string]any
		wantPath string
	}{
		{
			name: "EditTool args with path field",
			args: map[string]any{
				"path":       "src/main.go",
				"old_string": "old",
				"new_string": "new",
			},
			wantPath: filepath.Join(projectRoot, "src/main.go"),
		},
		{
			name: "Relative path with parent directory",
			args: map[string]any{
				"path":       "../outside.txt",
				"old_string": "old",
				"new_string": "new",
			},
			wantPath: filepath.Join(filepath.Dir(projectRoot), "outside.txt"),
		},
		{
			name: "Absolute path",
			args: map[string]any{
				"path":       filepath.Join(projectRoot, "absolute.txt"),
				"old_string": "old",
				"new_string": "new",
			},
			wantPath: filepath.Join(projectRoot, "absolute.txt"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			argsJSON, err := json.Marshal(tt.args)
			if err != nil {
				t.Fatalf("Marshal args: %v", err)
			}

			got := tools.ExtractEditPathFromArgsInDir(argsJSON, projectRoot)
			if got != tt.wantPath {
				t.Errorf("ExtractEditPathFromArgsInDir() = %q, want %q", got, tt.wantPath)
			}
		})
	}
}

// TestStreamingToolExecutor_ReplaceEditConcurrentSafety verifies that
// EditTool pre-write state capture works correctly
func TestStreamingToolExecutor_ReplaceEditPreWriteStateCapture(t *testing.T) {
	projectRoot := t.TempDir()
	path := filepath.Join(projectRoot, "capture.txt")
	if err := os.WriteFile(path, []byte("before\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// Change to project root
	oldwd, _ := os.Getwd()
	defer func() { _ = os.Chdir(oldwd) }()
	if err := os.Chdir(projectRoot); err != nil {
		t.Fatalf("Chdir: %v", err)
	}

	a := newTestMainAgent(t, projectRoot)
	a.tools.Register(tools.EditTool{BaseDir: projectRoot})

	ctx := context.Background()

	// First edit
	editArgs1 := map[string]any{
		"path":       "capture.txt",
		"old_string": "before\n",
		"new_string": "middle\n",
	}
	argsJSON1, _ := json.Marshal(editArgs1)
	call1 := message.ToolCall{ID: "edit-1", Name: tools.NameEdit, Args: argsJSON1}

	result1, err := a.executeToolCall(ctx, call1)
	if err != nil {
		t.Fatalf("executeToolCall(1) failed: %v", err)
	}
	if result1.PreContent != "before\n" {
		t.Fatalf("first edit PreContent = %q, want %q", result1.PreContent, "before\n")
	}

	// Second edit
	editArgs2 := map[string]any{
		"path":       "capture.txt",
		"old_string": "middle\n",
		"new_string": "after\n",
	}
	argsJSON2, _ := json.Marshal(editArgs2)
	call2 := message.ToolCall{ID: "edit-2", Name: tools.NameEdit, Args: argsJSON2}

	result2, err := a.executeToolCall(ctx, call2)
	if err != nil {
		t.Fatalf("executeToolCall(2) failed: %v", err)
	}
	if result2.PreContent != "middle\n" {
		t.Fatalf("second edit PreContent = %q, want %q", result2.PreContent, "middle\n")
	}

	// Verify final state
	content, _ := os.ReadFile(path)
	if string(content) != "after\n" {
		t.Fatalf("final content = %q, want %q", content, "after\n")
	}
}

// TestStreamingToolExecutor_PatchToolSpeculativeExecution verifies that
// PatchTool pre-write state capture works correctly (baseline comparison)
func TestStreamingToolExecutor_PatchToolPreWriteStateCapture(t *testing.T) {
	projectRoot := t.TempDir()
	path := filepath.Join(projectRoot, "patch.txt")
	if err := os.WriteFile(path, []byte("line1\nline2\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	a := newTestMainAgent(t, projectRoot)
	a.tools.Register(tools.PatchTool{BaseDir: projectRoot})

	ctx := context.Background()

	// Execute PatchTool
	patchArgs, _ := json.Marshal(map[string]any{
		"path":  "patch.txt",
		"patch": "@@\n line1\n-line2\n+LINE2\n",
	})
	call := message.ToolCall{ID: "patch-1", Name: tools.NamePatch, Args: patchArgs}

	result, err := a.executeToolCall(ctx, call)
	if err != nil {
		t.Fatalf("executeToolCall failed: %v", err)
	}

	// Verify pre-write state was captured
	if result.PreContent != "line1\nline2\n" {
		t.Fatalf("PreContent = %q, want %q", result.PreContent, "line1\nline2\n")
	}
	if !result.PreExisted {
		t.Fatal("PreExisted should be true")
	}

	// Verify file was modified
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(content) != "line1\nLINE2\n" {
		t.Fatalf("file content = %q, want %q", content, "line1\nLINE2\n")
	}
}
