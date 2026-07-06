package tools

import "testing"

func TestBaseDirToolImplementationsCoverSessionPathTools(t *testing.T) {
	tests := []struct {
		name string
		tool Tool
	}{
		{name: "patch", tool: PatchTool{}},
		{name: "edit", tool: EditTool{}},
		{name: "read", tool: ReadTool{}},
		{name: "write", tool: WriteTool{}},
		{name: "delete", tool: DeleteTool{}},
		{name: "grep", tool: GrepTool{}},
		{name: "glob", tool: GlobTool{}},
		{name: "handoff", tool: HandoffTool{}},
		{name: "shell", tool: NewShellTool("")},
		{name: "spawn", tool: NewSpawnTool("")},
		{name: "lsp", tool: LspTool{}},
		{name: "view_image", tool: NewViewImageTool(nil)},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			baseDirTool, ok := tc.tool.(BaseDirTool)
			if !ok {
				t.Fatalf("%T does not implement BaseDirTool", tc.tool)
			}
			got := baseDirTool.WithBaseDir("/repo")
			if got == nil {
				t.Fatal("WithBaseDir returned nil")
			}
		})
	}
}
