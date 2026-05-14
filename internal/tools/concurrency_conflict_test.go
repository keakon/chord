package tools

import (
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
