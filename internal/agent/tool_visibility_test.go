package agent

import (
	"os/exec"
	"testing"

	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/tools"
)

func TestMainToolVisibleMirrorsLiveSurface(t *testing.T) {
	// The exported visibility gate exists so descriptions and prompt blocks
	// can reference a sibling tool without pushing one the model cannot
	// call. It must mirror the live surface exactly: registered and not
	// denied → visible; missing, denied, or a nil agent → not visible.
	a := &MainAgent{}
	a.tools = tools.NewRegistry()
	a.tools.Register(tools.NewTodoWriteTool(nil))

	if !a.MainToolVisible(tools.NameTodoWrite) {
		t.Fatal("registered, non-denied todo_write must be visible")
	}
	if !a.MainToolVisible(" " + tools.NameTodoWrite + " ") {
		t.Fatal("lookup must trim the queried name")
	}
	if a.MainToolVisible(tools.NameCompactContext) {
		t.Fatal("an unregistered tool must not be visible")
	}
	if (&MainAgent{}).MainToolVisible(tools.NameTodoWrite) {
		t.Fatal("a nil tool registry must yield no visible tools")
	}
}

func TestMainToolVisibleFalseWhenToolDenied(t *testing.T) {
	a := &MainAgent{}
	a.tools = tools.NewRegistry()
	a.tools.Register(tools.NewTodoWriteTool(nil))
	a.ruleset = permission.Ruleset{{Permission: tools.NameTodoWrite, Pattern: "*", Action: permission.ActionDeny}}

	if a.MainToolVisible(tools.NameTodoWrite) {
		t.Fatal("a permission-deny ruleset must hide todo_write from the visible surface")
	}
}

// TestVisibleLLMToolsHidesWorktreeToolsWithoutGit pins the user-visible
// outcome: a machine without git never lists the worktree tools, because every
// one of their operations shells out to git.
func TestVisibleLLMToolsHidesWorktreeToolsWithoutGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	parent := newTestMainAgent(t, t.TempDir())
	reg := tools.NewRegistry()
	reg.Register(tools.NewWorktreeEnterTool(parent))
	reg.Register(tools.NewWorktreeExitTool(parent))
	reg.Register(tools.NewWorktreeListTool(parent))
	reg.Register(tools.NewTodoWriteTool(nil))

	worktreeTools := []string{tools.NameWorktreeEnter, tools.NameWorktreeExit, tools.NameWorktreeList}
	visible := visibleLLMTools(reg, permission.Ruleset{}, func(string) bool { return false }, toolPermissionContext{})
	for _, name := range worktreeTools {
		if !containsToolNamed(visible, name) {
			t.Fatalf("%s must be visible with git on PATH", name)
		}
	}

	t.Setenv("PATH", t.TempDir())
	visible = visibleLLMTools(reg, permission.Ruleset{}, func(string) bool { return false }, toolPermissionContext{})
	for _, name := range worktreeTools {
		if containsToolNamed(visible, name) {
			t.Errorf("%s must be hidden when git is not installed", name)
		}
	}
	if !containsToolNamed(visible, tools.NameTodoWrite) {
		t.Error("an unrelated tool must not be affected by the git check")
	}
}

func TestVisibleLLMToolsHidesJobToolsWhenShellDisabled(t *testing.T) {
	// The job tools read and stop jobs that only shell can start, so disabling
	// shell hides the whole family — unless a non-wildcard rule names a tool,
	// which is the ruleset author asking for that tool on its own.
	newRegistry := func() *tools.Registry {
		reg := tools.NewRegistry()
		reg.Register(tools.JobOutputTool{})
		reg.Register(tools.JobListTool{})
		reg.Register(tools.JobKillTool{})
		reg.Register(tools.NewTodoWriteTool(nil))
		return reg
	}
	cases := []struct {
		name       string
		rules      string
		wantJob    []string
		wantHidden []string
	}{
		{
			name: "shell denied hides the whole job family",
			rules: `
"*": allow
shell: deny
`,
			wantHidden: []string{tools.NameJobOutput, tools.NameJobList, tools.NameJobKill},
		},
		{
			name: "a rule naming one job tool keeps it",
			rules: `
"*": allow
shell: deny
job_output: allow
`,
			wantJob:    []string{tools.NameJobOutput},
			wantHidden: []string{tools.NameJobList, tools.NameJobKill},
		},
		{
			name: "an ask rule also counts as an explicit grant",
			rules: `
"*": allow
shell: deny
job_list: ask
`,
			wantJob:    []string{tools.NameJobList},
			wantHidden: []string{tools.NameJobOutput, tools.NameJobKill},
		},
		{
			name: "shell allowed keeps the whole job family",
			rules: `
"*": allow
`,
			wantJob: []string{tools.NameJobOutput, tools.NameJobList, tools.NameJobKill},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			visible := visibleLLMTools(newRegistry(), permissionRuleset(t, tc.rules), func(string) bool { return false }, toolPermissionContext{})
			for _, name := range tc.wantJob {
				if !containsToolNamed(visible, name) {
					t.Errorf("%s must stay visible", name)
				}
			}
			for _, name := range tc.wantHidden {
				if containsToolNamed(visible, name) {
					t.Errorf("%s must be hidden while shell is disabled", name)
				}
			}
			if !containsToolNamed(visible, tools.NameTodoWrite) {
				t.Error("an unrelated tool must not be affected by the shell coupling")
			}
		})
	}
}
