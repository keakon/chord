package tools

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestIsMachineStateRelPath(t *testing.T) {
	cases := []struct {
		rel  string
		want bool
	}{
		{".chord/plans/x.md", true},
		{".chord/plans", true},
		{".chord/plans/deep/nested/x.md", true},
		{".chord/notes/x.md", true},
		{".chord/memory/MEMORY.md", true},
		{".chord/docs/architecture/index.md", true},
		{".chord/worktrees/feat", true},
		{"MEMORY.md", true},
		// Branch content stays with the checkout: a branch may carry its own
		// config/agents/skills, and redirecting writes would split them from
		// the copies reads pick up.
		{".chord/config.yaml", false},
		{".chord/agents/reviewer.yaml", false},
		{".chord/skills/demo/SKILL.md", false},
		{"AGENTS.md", false},
		{"src/main.go", false},
		{".chord/plansfoo/x.md", false},
		{"docs/MEMORY.md", false},
		{"", false},
		{".", false},
		{"..", false},
		{"../.chord/plans/x.md", false},
	}
	for _, tc := range cases {
		if got := IsMachineStateRelPath(tc.rel); got != tc.want {
			t.Errorf("IsMachineStateRelPath(%q) = %v, want %v", tc.rel, got, tc.want)
		}
	}
}

// TestMachineStateTargetsInDir models a session working inside a worktree: the
// base dir is the checkout, and the machine-state paths are spelled relative to
// it exactly as the model would spell them.
func TestMachineStateTargetsInDir(t *testing.T) {
	base := filepath.Join("/repo", ".chord", "worktrees", "feat")
	inside := func(p string) string { return filepath.Join(base, filepath.FromSlash(p)) }
	cases := []struct {
		name string
		tool string
		args string
		want bool
	}{
		{"write plan", NameWrite, `{"path":".chord/plans/x.md"}`, true},
		{"write note", NameWrite, `{"path":".chord/notes/x.md"}`, true},
		{"write root memory", NameWrite, `{"path":"MEMORY.md"}`, true},
		{"write source", NameWrite, `{"path":"src/main.go"}`, false},
		{"write branch config", NameWrite, `{"path":".chord/config.yaml"}`, false},
		{"read note", NameRead, `{"path":".chord/notes/x.md"}`, true},
		{"view image in plans", NameViewImage, `{"path":".chord/plans/diagram.png"}`, true},
		{"edit plan", NameEdit, `{"path":".chord/plans/x.md"}`, true},
		{"edit source", NameEdit, `{"path":"src/main.go"}`, false},
		{"handoff plan", NameHandoff, `{"plan_path":".chord/plans/x.md"}`, true},
		{"delete plan", NameDelete, `{"paths":[".chord/plans/x.md"],"reason":"stale plan"}`, true},
		{"delete mixed", NameDelete, `{"paths":[".chord/plans/x.md","src/main.go"],"reason":"cleanup"}`, false},
		{"patch plan", NameApplyPatch, `{"patch":"*** Begin Patch\n*** Add File: .chord/plans/x.md\n+hi\n*** End Patch"}`, true},
		{"patch source", NameApplyPatch, `{"patch":"*** Begin Patch\n*** Update File: src/main.go\n@@\n-a\n+b\n*** End Patch"}`, false},
		// Searches join the same rule as reads and writes: a search root that
		// names machine state is anchored to the content root, while an omitted
		// root (the session working directory) and a mixed call are not.
		{"grep plan dir", NameGrep, `{"pattern":"foo","paths":[".chord/plans"]}`, true},
		{"grep singular path alias", NameGrep, `{"pattern":"foo","path":".chord/notes"}`, true},
		{"grep source dir", NameGrep, `{"pattern":"foo","paths":["src"]}`, false},
		{"grep default root", NameGrep, `{"pattern":"foo"}`, false},
		{"grep mixed roots", NameGrep, `{"pattern":"foo","paths":[".chord/plans","src"]}`, false},
		{"glob plan dir", NameGlob, `{"patterns":["**/*.md"],"path":".chord/plans"}`, true},
		{"glob default root", NameGlob, `{"patterns":["**/*.md"]}`, false},
		// The rule reads the resolved path, so the same logical machine-state
		// location is redirected however it is spelled; a path outside the
		// base dir is left to the ordinary base dir.
		{"absolute outside base", NameWrite, `{"path":"/tmp/x.md"}`, false},
		{"absolute inside base", NameWrite, `{"path":"` + inside(".chord/plans/x.md") + `"}`, true},
		// Unclassifiable calls keep the ordinary base dir.
		{"unknown tool", NameShell, `{"command":"ls .chord/plans"}`, false},
		{"malformed args", NameWrite, `{`, false},
		{"missing path", NameWrite, `{}`, false},
		{"empty patch", NameApplyPatch, `{"patch":""}`, false},
		{"delete without paths", NameDelete, `{}`, false},
	}
	for _, tc := range cases {
		if got := MachineStateTargetsInDir(tc.tool, json.RawMessage(tc.args), base); got != tc.want {
			t.Errorf("%s: MachineStateTargetsInDir(%s, %s) = %v, want %v", tc.name, tc.tool, tc.args, got, tc.want)
		}
	}
}

// TestMachineStateTargetsInDirIsBaseRelative pins that the rule reads the path
// the way the tool will, relative to the base dir it is given.
func TestMachineStateTargetsInDirIsBaseRelative(t *testing.T) {
	args := json.RawMessage(`{"path":".chord/plans/x.md"}`)
	for _, base := range []string{"/repo", "/repo/.chord/worktrees/feat"} {
		if !MachineStateTargetsInDir(NameWrite, args, base) {
			t.Errorf("relative .chord/plans path under %s should be machine state", base)
		}
	}
	// A path that resolves outside the base dir is never redirected.
	abs := filepath.Join("/tmp", ".chord", "plans", "x.md")
	if MachineStateTargetsInDir(NameWrite, json.RawMessage(`{"path":"`+abs+`"}`), "/repo/.chord/worktrees/feat") {
		t.Error("an absolute path outside the base dir must not be redirected")
	}
}
