package tools

import (
	"context"
	"encoding/json"
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
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ConcurrencyConflict(tc.a, tc.b); got != tc.want {
				t.Fatalf("ConcurrencyConflict() = %v, want %v", got, tc.want)
			}
		})
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

// fakeBatchSafeTool opts into read-only batching via the interface alone.
type fakeBatchSafeTool struct{ fakeReadOnlyTool }

func (fakeBatchSafeTool) Name() string                                 { return "FakeBatchSafe" }
func (fakeBatchSafeTool) ConcurrencySafeReadOnly(json.RawMessage) bool { return true }

// TestConcurrencyClassFollowsToolInterface verifies that read-only batching is
// decided by the tool implementing ConcurrencySafeReadOnlyTool, not by a
// central name allowlist: a read-only tool that does not implement it is
// Exclusive, while implementing it (and nothing else) yields ReadOnly.
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
