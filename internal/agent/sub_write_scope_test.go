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

type mutatingScopeTestTool struct{ name string }

func (t mutatingScopeTestTool) Name() string             { return t.name }
func (mutatingScopeTestTool) Description() string        { return "mutates workspace" }
func (mutatingScopeTestTool) Parameters() map[string]any { return nil }
func (mutatingScopeTestTool) IsReadOnly() bool           { return false }
func (mutatingScopeTestTool) Execute(context.Context, json.RawMessage) (string, error) {
	return "ok", nil
}

func TestSubAgentWriteScopeAllowsDeclaredFileAndRejectsOtherFile(t *testing.T) {
	parent, sub := newMixedBatchTestSubAgent(t)
	root := t.TempDir()
	parent.projectRoot = root
	sub.workDir = root
	sub.writeScope = tools.WriteScope{Files: []string{"allowed.txt"}}
	sub.tools.Register(tools.WriteTool{BaseDir: root})

	allowedArgs, _ := json.Marshal(map[string]string{"path": "allowed.txt", "content": "ok"})
	if _, err := sub.executeToolCall(context.Background(), message.ToolCall{ID: "allowed", Name: tools.NameWrite, Args: allowedArgs}); err != nil {
		t.Fatalf("declared write failed: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(root, "allowed.txt")); err != nil || string(got) != "ok" {
		t.Fatalf("allowed file = %q, %v", got, err)
	}

	deniedArgs, _ := json.Marshal(map[string]string{"path": "other.txt", "content": "no"})
	if _, err := sub.executeToolCall(context.Background(), message.ToolCall{ID: "denied", Name: tools.NameWrite, Args: deniedArgs}); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("out-of-scope write error = %v, want scope rejection", err)
	}
	if _, err := os.Stat(filepath.Join(root, "other.txt")); !os.IsNotExist(err) {
		t.Fatalf("out-of-scope file unexpectedly exists: %v", err)
	}
}

func TestSubAgentReadOnlyScopeRejectsMutatingTool(t *testing.T) {
	parent, sub := newMixedBatchTestSubAgent(t)
	root := t.TempDir()
	parent.projectRoot = root
	sub.workDir = root
	sub.writeScope = tools.WriteScope{ReadOnly: true}
	sub.tools.Register(tools.WriteTool{BaseDir: root})

	args, _ := json.Marshal(map[string]string{"path": "blocked.txt", "content": "no"})
	if _, err := sub.executeToolCall(context.Background(), message.ToolCall{ID: "blocked", Name: tools.NameWrite, Args: args}); err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("read-only write error = %v, want rejection", err)
	}
}

func TestSubAgentReadOnlyScopeRejectsShellEvenWhenCommandLooksReadOnly(t *testing.T) {
	parent, sub := newMixedBatchTestSubAgent(t)
	root := t.TempDir()
	parent.projectRoot = root
	sub.workDir = root
	sub.writeScope = tools.WriteScope{ReadOnly: true}
	sub.tools.Register(tools.ShellTool{})

	args, _ := json.Marshal(map[string]string{"command": "git branch scope-bypass", "description": "attempt hidden mutation"})
	if _, err := sub.executeToolCall(context.Background(), message.ToolCall{ID: "shell-read", Name: tools.NameShell, Args: args}); err == nil || !strings.Contains(err.Error(), "shell is unavailable") {
		t.Fatalf("scoped Shell error = %v, want conservative rejection", err)
	}
}

func TestSubAgentReadOnlyScopeAllowsCoordinationTools(t *testing.T) {
	_, sub := newMixedBatchTestSubAgent(t)
	sub.writeScope = tools.WriteScope{ReadOnly: true}
	args, _ := json.Marshal(map[string]string{"summary": "review complete"})
	if err := sub.toolExecutionPipeline().validateWriteScope(message.ToolCall{ID: "complete", Name: tools.NameComplete, Args: args}); err != nil {
		t.Fatalf("read-only Complete rejected: %v", err)
	}
}

func TestSubAgentPathScopeRejectsMutatingShell(t *testing.T) {
	parent, sub := newMixedBatchTestSubAgent(t)
	root := t.TempDir()
	parent.projectRoot = root
	sub.workDir = root
	sub.writeScope = tools.WriteScope{PathPrefix: []string{"internal"}}
	sub.tools.Register(tools.ShellTool{})

	args, _ := json.Marshal(map[string]string{"command": "touch internal/file.txt", "description": "mutate file"})
	if _, err := sub.executeToolCall(context.Background(), message.ToolCall{ID: "shell", Name: tools.NameShell, Args: args}); err == nil || !strings.Contains(err.Error(), "shell is unavailable") {
		t.Fatalf("mutating shell error = %v, want scope-safe rejection", err)
	}
}

func TestSubAgentModuleOnlyScopeRequiresPathDeclaration(t *testing.T) {
	parent, sub := newMixedBatchTestSubAgent(t)
	root := t.TempDir()
	parent.projectRoot = root
	sub.workDir = root
	sub.writeScope = tools.WriteScope{Modules: []string{"backend"}}
	sub.tools.Register(tools.WriteTool{BaseDir: root})

	args, _ := json.Marshal(map[string]string{"path": "backend/file.txt", "content": "no"})
	if _, err := sub.executeToolCall(context.Background(), message.ToolCall{ID: "module", Name: tools.NameWrite, Args: args}); err == nil || !strings.Contains(err.Error(), "logical modules") {
		t.Fatalf("module-only scope error = %v, want path declaration requirement", err)
	}
}

func TestSubAgentPathScopeRejectsUnknownMutatingTool(t *testing.T) {
	parent, sub := newMixedBatchTestSubAgent(t)
	parent.projectRoot = t.TempDir()
	sub.writeScope = tools.WriteScope{PathPrefix: []string{"internal"}}
	sub.tools.Register(mutatingScopeTestTool{name: "CustomMutator"})

	if err := sub.toolExecutionPipeline().validateWriteScope(message.ToolCall{ID: "custom", Name: "CustomMutator"}); err == nil || !strings.Contains(err.Error(), "cannot validate") {
		t.Fatalf("unknown mutating tool error = %v, want conservative rejection", err)
	}
}

func TestSubAgentPathScopeRejectsSymlinkEscape(t *testing.T) {
	parent, sub := newMixedBatchTestSubAgent(t)
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "allowed"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "allowed", "link")); err != nil {
		t.Fatalf("Symlink: %v", err)
	}
	parent.projectRoot = root
	sub.workDir = root
	sub.writeScope = tools.WriteScope{PathPrefix: []string{"allowed"}}
	sub.tools.Register(tools.WriteTool{BaseDir: root})

	args, _ := json.Marshal(map[string]string{"path": "allowed/link/escaped.txt", "content": "no"})
	if _, err := sub.executeToolCall(context.Background(), message.ToolCall{ID: "escape", Name: tools.NameWrite, Args: args}); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("symlink escape error = %v, want scope rejection", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "escaped.txt")); !os.IsNotExist(err) {
		t.Fatalf("escaped file unexpectedly exists: %v", err)
	}
}

func TestSubAgentEmptyScopePreservesLegacyWriteBehavior(t *testing.T) {
	parent, sub := newMixedBatchTestSubAgent(t)
	root := t.TempDir()
	parent.projectRoot = root
	sub.workDir = root
	sub.writeScope = tools.WriteScope{}
	sub.tools.Register(tools.WriteTool{BaseDir: root})

	args, _ := json.Marshal(map[string]string{"path": "legacy.txt", "content": "ok"})
	if _, err := sub.executeToolCall(context.Background(), message.ToolCall{ID: "legacy", Name: tools.NameWrite, Args: args}); err != nil {
		t.Fatalf("empty-scope legacy write failed: %v", err)
	}
}
