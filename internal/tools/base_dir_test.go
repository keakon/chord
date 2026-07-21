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

func TestWithBaseDirOverridesInheritedSessionDirectory(t *testing.T) {
	tool := ReadTool{BaseDir: "/parent"}
	got, ok := tool.WithBaseDir("/child").(ReadTool)
	if !ok {
		t.Fatalf("WithBaseDir returned %T", tool.WithBaseDir("/child"))
	}
	if got.BaseDir != "/child" {
		t.Fatalf("BaseDir = %q, want child directory", got.BaseDir)
	}
}

func TestViewImageWithBaseDirDoesNotMutateParentTool(t *testing.T) {
	parent := &ViewImageTool{BaseDir: "/parent"}
	child, ok := parent.WithBaseDir("/child").(*ViewImageTool)
	if !ok {
		t.Fatalf("WithBaseDir returned %T", parent.WithBaseDir("/child"))
	}
	if parent.BaseDir != "/parent" || child.BaseDir != "/child" || child == parent {
		t.Fatalf("parent=%#v child=%#v, want independent directories", parent, child)
	}
}
