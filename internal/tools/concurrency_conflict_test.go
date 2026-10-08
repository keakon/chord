package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestConcurrencyConflictHierarchy(t *testing.T) {
	tests := []struct {
		name string
		a    ConcurrencyPolicy
		b    ConcurrencyPolicy
		want bool
	}{
		{
			name: "workspace read conflicts with file write",
			a:    ConcurrencyPolicy{Resource: "workspace", Mode: ConcurrencyModeRead},
			b:    ConcurrencyPolicy{Resource: "file:src/main.go", Mode: ConcurrencyModeWrite},
			want: true,
		},
		{
			name: "directory read conflicts with descendant file write",
			a:    ConcurrencyPolicy{Resource: "path:src", Mode: ConcurrencyModeRead},
			b:    ConcurrencyPolicy{Resource: "file:src/main.go", Mode: ConcurrencyModeWrite},
			want: true,
		},
		{
			name: "ancestor and descendant directory overlap",
			a:    ConcurrencyPolicy{Resource: "path:src", Mode: ConcurrencyModeRead},
			b:    ConcurrencyPolicy{Resource: "path:src/pkg", Mode: ConcurrencyModeWrite},
			want: true,
		},
		{
			name: "separate directories do not overlap",
			a:    ConcurrencyPolicy{Resource: "path:src", Mode: ConcurrencyModeRead},
			b:    ConcurrencyPolicy{Resource: "file:test/main.go", Mode: ConcurrencyModeWrite},
			want: false,
		},
		{
			name: "overlapping reads stay concurrent",
			a:    ConcurrencyPolicy{Resource: "path:src", Mode: ConcurrencyModeRead},
			b:    ConcurrencyPolicy{Resource: "file:src/main.go", Mode: ConcurrencyModeRead},
			want: false,
		},
		{
			name: "concurrent calls share the same resource",
			a:    ConcurrencyPolicy{Resource: "mcp:search", Mode: ConcurrencyModeConcurrent},
			b:    ConcurrencyPolicy{Resource: "mcp:search", Mode: ConcurrencyModeConcurrent},
			want: false,
		},
		{
			name: "concurrent shares a resource with a read",
			a:    ConcurrencyPolicy{Resource: "file:src/main.go", Mode: ConcurrencyModeConcurrent},
			b:    ConcurrencyPolicy{Resource: "file:src/main.go", Mode: ConcurrencyModeRead},
			want: false,
		},
		{
			name: "concurrent conflicts with a write of the same resource",
			a:    ConcurrencyPolicy{Resource: "file:src/main.go", Mode: ConcurrencyModeConcurrent},
			b:    ConcurrencyPolicy{Resource: "file:src/main.go", Mode: ConcurrencyModeWrite},
			want: true,
		},
		{
			name: "exclusive serializes against a concurrent call on another resource",
			a:    ConcurrencyPolicy{Resource: "tool:question", Mode: ConcurrencyModeExclusive},
			b:    ConcurrencyPolicy{Resource: "mcp:search", Mode: ConcurrencyModeConcurrent},
			want: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ConcurrencyConflict(tc.a, tc.b); got != tc.want {
				t.Fatalf("ConcurrencyConflict() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestResourceOverlapResolvesFileAliases(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "real")
	if err := os.Mkdir(real, 0o700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(real, alias); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(real, "target.txt")
	if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	aliasTarget := filepath.Join(alias, "target.txt")
	if !sameResourcePath(target, aliasTarget) {
		t.Fatal("a directory alias must name the same file")
	}
	if !resourceOverlap("file:"+target, "file:"+aliasTarget) {
		t.Fatal("file resources through a directory alias must overlap")
	}
	if !resourceOverlap("path:"+alias, "file:"+target) || !resourceOverlap("file:"+target, "path:"+alias) {
		t.Fatal("a directory alias must contain the file it holds")
	}
	// A target that does not exist yet still follows its parent alias.
	if !resourceOverlap("file:"+filepath.Join(real, "future.txt"), "file:"+filepath.Join(alias, "future.txt")) {
		t.Fatal("a nonexistent target under an aliased parent must overlap")
	}
	if resourceOverlap("file:"+target, "file:"+filepath.Join(real, "other.txt")) {
		t.Fatal("unrelated files must stay independent")
	}
}

func TestConcurrencyClassForToolNilRegistryIsExclusive(t *testing.T) {
	if got := ConcurrencyClassForTool(nil, NameRead, json.RawMessage(`{"path":"README.md"}`)); got != ToolConcurrencyClassExclusive {
		t.Fatalf("ConcurrencyClassForTool(nil, Read) = %v, want %v", got, ToolConcurrencyClassExclusive)
	}
}

// fakeReadOnlyTool is read-only but does NOT opt into read-only batching.
type fakeReadOnlyTool struct{}

func (fakeReadOnlyTool) Name() string               { return "FakeReadOnly" }
func (fakeReadOnlyTool) Description() string        { return "" }
func (fakeReadOnlyTool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (fakeReadOnlyTool) Execute(context.Context, json.RawMessage) (string, error) {
	return "ok", nil
}
func (fakeReadOnlyTool) IsReadOnly() bool { return true }

// fakeBatchSafeTool declares the read-only batching class through the interface
// alone, which is exactly what TestConcurrencyClassFollowsToolInterface pins. It
// is not batchable by itself: a real tool must also declare a non-exclusive
// ConcurrencyPolicy (see TestReadOnlyBatchableToolsDeclareConcurrencyPolicy).
type fakeBatchSafeTool struct{ fakeReadOnlyTool }

func (fakeBatchSafeTool) Name() string                                 { return "FakeBatchSafe" }
func (fakeBatchSafeTool) ConcurrencySafeReadOnly(json.RawMessage) bool { return true }

// TestConcurrencyClassFollowsToolInterface verifies that the read-only batching
// class is decided by the tool implementing ConcurrencySafeReadOnlyTool, not by
// a central name allowlist: a read-only tool that does not implement it is
// Exclusive, while implementing it (and nothing else) yields ReadOnly. Merging
// into a batch additionally requires a non-exclusive ConcurrencyPolicy.
func TestConcurrencyClassFollowsToolInterface(t *testing.T) {
	reg := NewRegistry()
	reg.Register(fakeReadOnlyTool{})
	reg.Register(fakeBatchSafeTool{})

	if got := ConcurrencyClassForTool(reg, "FakeReadOnly", nil); got != ToolConcurrencyClassExclusive {
		t.Fatalf("read-only-but-not-batch-safe class = %v, want Exclusive", got)
	}
	if got := ConcurrencyClassForTool(reg, "FakeBatchSafe", nil); got != ToolConcurrencyClassReadOnly {
		t.Fatalf("batch-safe class = %v, want ReadOnly", got)
	}
}

// TestConcurrencyClassShellAllowlist confirms Shell owns its own read-only
// admission: an allowlisted command batches, a mutating one does not.
func TestConcurrencyClassShellAllowlist(t *testing.T) {
	reg := NewRegistry()
	reg.Register(NewShellTool(""))

	roArgs, _ := json.Marshal(map[string]string{"command": "git status"})
	if got := ConcurrencyClassForTool(reg, NameShell, roArgs); got != ToolConcurrencyClassReadOnly {
		t.Fatalf("shell read-only command class = %v, want ReadOnly", got)
	}
	rwArgs, _ := json.Marshal(map[string]string{"command": "git commit -m x"})
	if got := ConcurrencyClassForTool(reg, NameShell, rwArgs); got != ToolConcurrencyClassExclusive {
		t.Fatalf("shell mutating command class = %v, want Exclusive", got)
	}
}

// fakeMutatingTool is a state-changing tool with a resource-scoped policy but
// no batching declaration: it stays a Mutating serialization boundary.
type fakeMutatingTool struct {
	fakeReadOnlyTool
	resource string
	mode     ConcurrencyMode
}

func (fakeMutatingTool) Name() string     { return "FakeMutating" }
func (fakeMutatingTool) IsReadOnly() bool { return false }
func (t fakeMutatingTool) ConcurrencyPolicy(json.RawMessage) ConcurrencyPolicy {
	return ConcurrencyPolicy{Resource: t.resource, Mode: t.mode}
}

// fakeBatchableMutatingTool additionally opts into concurrent batching through
// the interface, the shape file writers and MCP tools use.
type fakeBatchableMutatingTool struct{ fakeMutatingTool }

func (fakeBatchableMutatingTool) Name() string                              { return "FakeBatchableMutating" }
func (fakeBatchableMutatingTool) ConcurrencyBatchable(json.RawMessage) bool { return true }

// TestConcurrencyClassFollowsBatchableInterface verifies that a mutating tool
// becomes Concurrent only when it declares ConcurrencyBatchable: the same
// policy without the interface stays Mutating (a boundary). It also pins the
// batch builder's admission rule to those two classes.
func TestConcurrencyClassFollowsBatchableInterface(t *testing.T) {
	reg := NewRegistry()
	reg.Register(fakeMutatingTool{resource: "file:/tmp/a", mode: ConcurrencyModeWrite})
	reg.Register(fakeBatchableMutatingTool{fakeMutatingTool{resource: "file:/tmp/a", mode: ConcurrencyModeWrite}})

	if got := ConcurrencyClassForTool(reg, "FakeMutating", nil); got != ToolConcurrencyClassMutating {
		t.Fatalf("undeclared mutating class = %v, want Mutating", got)
	}
	if got := ConcurrencyClassForTool(reg, "FakeBatchableMutating", nil); got != ToolConcurrencyClassConcurrent {
		t.Fatalf("batchable mutating class = %v, want Concurrent", got)
	}
	for _, class := range []ToolConcurrencyClass{ToolConcurrencyClassReadOnly, ToolConcurrencyClassConcurrent} {
		if !ConcurrencyClassBatchable(class) {
			t.Fatalf("class %v must be batchable", class)
		}
	}
	for _, class := range []ToolConcurrencyClass{ToolConcurrencyClassUnknown, ToolConcurrencyClassMutating, ToolConcurrencyClassExclusive} {
		if ConcurrencyClassBatchable(class) {
			t.Fatalf("class %v must not be batchable", class)
		}
	}
}

func TestWorkspaceLeaseConflictScopesExclusiveByResource(t *testing.T) {
	shell := ConcurrencyPolicy{Resource: "process:shell", Mode: ConcurrencyModeExclusive}
	question := ConcurrencyPolicy{Resource: "tool:question", Mode: ConcurrencyModeExclusive}
	fileRead := ConcurrencyPolicy{Resource: "file:src/main.go", Mode: ConcurrencyModeRead}
	fileWrite := ConcurrencyPolicy{Resource: "file:src/main.go", Mode: ConcurrencyModeWrite}
	otherWrite := ConcurrencyPolicy{Resource: "file:src/other.go", Mode: ConcurrencyModeWrite}
	workspace := ConcurrencyPolicy{Resource: "workspace", Mode: ConcurrencyModeExclusive}

	if WorkspaceLeaseConflict(shell, fileRead) || WorkspaceLeaseConflict(shell, fileWrite) || WorkspaceLeaseConflict(shell, question) {
		t.Fatal("scoped exclusive lease conflicted with unrelated resources")
	}
	if !WorkspaceLeaseConflict(shell, shell) || !WorkspaceLeaseConflict(question, question) {
		t.Fatal("identical exclusive resources must conflict")
	}
	if !WorkspaceLeaseConflict(fileWrite, fileRead) {
		t.Fatal("same-file write/read must conflict")
	}
	if WorkspaceLeaseConflict(fileWrite, otherWrite) {
		t.Fatal("disjoint file writes must not conflict")
	}
	if !WorkspaceLeaseConflict(workspace, fileRead) || !WorkspaceLeaseConflict(workspace, shell) {
		t.Fatal("workspace resource must overlap everything")
	}
	if WorkspaceLeaseConflict(fileRead, fileRead) {
		t.Fatal("read/read on the same file must not conflict")
	}
	concurrent := ConcurrencyPolicy{Resource: "mcp:search", Mode: ConcurrencyModeConcurrent}
	if WorkspaceLeaseConflict(concurrent, concurrent) {
		t.Fatal("concurrent/concurrent on the same resource must not conflict")
	}
	if WorkspaceLeaseConflict(concurrent, ConcurrencyPolicy{Resource: "mcp:search", Mode: ConcurrencyModeRead}) {
		t.Fatal("read/concurrent on the same resource must not conflict")
	}
	if !WorkspaceLeaseConflict(concurrent, ConcurrencyPolicy{Resource: "mcp:search", Mode: ConcurrencyModeWrite}) {
		t.Fatal("write/concurrent on the same resource must conflict")
	}
	if WorkspaceLeaseConflict(concurrent, fileWrite) {
		t.Fatal("concurrent resources must not conflict with unrelated files")
	}
}
