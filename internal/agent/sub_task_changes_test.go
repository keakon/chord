package agent

import (
	"path/filepath"
	"testing"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func TestRecordTaskToolChangesShellReadOnlyMapping(t *testing.T) {
	// A shell the classifier admits skips file-attribution
	// bookkeeping entirely, even on error; a rejected one marks the
	// attribution incomplete when nothing proves what it touched.
	registry := tools.NewRegistry()
	registry.Register(tools.NewShellTool("bash"))
	sub := &SubAgent{tools: registry, workDir: t.TempDir()}

	readonly := &toolResult{CallID: "rg", Name: tools.NameShell, ArgsJSON: `{"command":"rg --no-config -n pat --glob '*.go'"}`}
	if files, incomplete := sub.recordTaskToolChanges(readonly, false); len(files) != 0 || incomplete {
		t.Fatalf("read-only shell success = (%v, %v), want (nil, false)", files, incomplete)
	}
	if files, incomplete := sub.recordTaskToolChanges(readonly, true); len(files) != 0 || incomplete {
		t.Fatalf("read-only shell error = (%v, %v), want (nil, false)", files, incomplete)
	}
	if sub.fileAttributionIncomplete {
		t.Fatal("read-only shells must not flag file attribution incomplete")
	}

	mutating := &toolResult{CallID: "test", Name: tools.NameShell, ArgsJSON: `{"command":"go test ./..."}`}
	if files, incomplete := sub.recordTaskToolChanges(mutating, false); len(files) != 0 || !incomplete {
		t.Fatalf("mutating shell success = (%v, %v), want (nil, true)", files, incomplete)
	}
	if files, incomplete := sub.recordTaskToolChanges(mutating, true); len(files) != 0 || !incomplete {
		t.Fatalf("mutating shell error = (%v, %v), want (nil, true)", files, incomplete)
	}
	if !sub.fileAttributionIncomplete {
		t.Fatal("unattributed mutating shell must flag file attribution incomplete")
	}
}

func TestRecordTaskToolChangesSpellsPathsAgainstActiveCheckout(t *testing.T) {
	// A worktree switch republishes the binding without touching the startup
	// directory, so a file changed in the new checkout must be spelled
	// relative to it instead of falling back to a "../" spelling.
	_, sub := newMixedBatchTestSubAgent(t)
	checkout := t.TempDir()
	sub.workDirState.store(WorkDirState{Path: checkout, WorktreeID: "feat-x", Generation: 1})

	files, incomplete := sub.recordTaskToolChanges(&toolResult{
		Name:      tools.NameWrite,
		ArgsJSON:  `{"path":"internal/observed.go","content":"package observed"}`,
		FileState: &message.ToolFileState{Writes: []message.TrackedFileState{{Path: filepath.Join(checkout, "internal", "observed.go"), Exists: true}}},
	}, false)
	if incomplete {
		t.Fatal("file attribution unexpectedly incomplete")
	}
	if len(files) != 1 || files[0] != "internal/observed.go" {
		t.Fatalf("attributed files = %#v, want the active checkout spelling", files)
	}
}

func TestRecordTaskToolChangesWorktreeSwitchesAreAttributionNeutral(t *testing.T) {
	_, sub := newMixedBatchTestSubAgent(t)
	sub.tools.Register(tools.NewWorktreeExitTool(nil))

	cases := []struct {
		name     string
		args     string
		removing bool
	}{
		{tools.NameWorktreeEnter, `{"name":"feat-x"}`, false},
		{tools.NameWorktreeExit, `{"action":"keep"}`, false},
		{tools.NameWorktreeExit, `{}`, false},
		{tools.NameWorktreeExit, `{"action":"remove"}`, true},
		{tools.NameWorktreeExit, `{"action":"remove","discard_changes":true}`, true},
	}
	for _, tc := range cases {
		sub.fileAttributionIncomplete = false
		files, incomplete := sub.recordTaskToolChanges(&toolResult{Name: tc.name, ArgsJSON: tc.args}, false)
		if incomplete != tc.removing {
			t.Fatalf("%s %s: incomplete=%v, want %v", tc.name, tc.args, incomplete, tc.removing)
		}
		if len(files) != 0 {
			t.Fatalf("%s %s: files=%#v, want none", tc.name, tc.args, files)
		}
	}
}
