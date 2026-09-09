package tools

import (
	"encoding/json"
	"testing"
)

func TestShellReadOnlyCommandAllowed_AllowsKnownReadOnlyCommands(t *testing.T) {
	cases := []string{
		"pwd",
		"ls -la",
		"cat README.md",
		"which git",
		"git status --short",
		"git diff --stat HEAD~1",
		"git show HEAD~1:README.md",
		"git branch --show-current",
		"git rev-parse HEAD",
	}
	for _, command := range cases {
		t.Run(command, func(t *testing.T) {
			args, err := json.Marshal(map[string]any{"command": command})
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			if !shellReadOnlyCommandAllowed(args) {
				t.Fatalf("shellReadOnlyCommandAllowed(%q) = false, want true", command)
			}
		})
	}
}

func TestShellReadOnlyCommandAllowed_RejectsMetacharAndMutations(t *testing.T) {
	cases := []string{
		"git status && pwd",
		"git status; pwd",
		"git status | cat",
		"git status > out.txt",
		"$(pwd)",
		"echo `pwd`",
		"git checkout main",
		"git commit -m test",
		"rm -rf tmp",
	}
	for _, command := range cases {
		t.Run(command, func(t *testing.T) {
			args, err := json.Marshal(map[string]any{"command": command})
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			if shellReadOnlyCommandAllowed(args) {
				t.Fatalf("shellReadOnlyCommandAllowed(%q) = true, want false", command)
			}
		})
	}
}

func TestContainsShellConstructDetectsBlockedCharacters(t *testing.T) {
	cases := []struct {
		command string
		want    bool
	}{
		{command: "git status", want: false},
		{command: "echo $(pwd)", want: true},
		{command: "echo `pwd`", want: true},
		{command: "echo *", want: true},
		{command: "echo [a-z]", want: true},
		{command: "echo {a,b}", want: true},
		{command: "printf 'x'\\n", want: true},
	}
	for _, tc := range cases {
		t.Run(tc.command, func(t *testing.T) {
			if got := containsShellConstruct(tc.command); got != tc.want {
				t.Fatalf("containsShellConstruct(%q) = %v, want %v", tc.command, got, tc.want)
			}
		})
	}
}

// TestShellReadOnlyRejectsEveryShellSyntaxCharacter pins the read-only
// classifier against shell syntax character by character, so a future change
// to either construct set cannot silently widen what the allowlist accepts.
// Chaining constructs introduce a second command; argument-expansion
// constructs stay inside one command but decide which files it touches, so the
// allowlist refuses them too rather than depend on an expansion it never
// performs.
func TestShellReadOnlyRejectsEveryShellSyntaxCharacter(t *testing.T) {
	for _, r := range shellCommandChainingCharacters {
		command := "ls a" + string(r) + "b"
		if shellReadOnlyCommandAllowed(mustJSONCommand(t, command)) {
			t.Fatalf("shellReadOnlyCommandAllowed(%q) = true, want false", command)
		}
	}
	for _, r := range shellArgumentExpansionCharacters {
		command := "cat README.md" + string(r) + "x"
		if shellReadOnlyCommandAllowed(mustJSONCommand(t, command)) {
			t.Fatalf("shellReadOnlyCommandAllowed(%q) = true, want false", command)
		}
	}
}

func mustJSONCommand(t *testing.T, command string) []byte {
	t.Helper()
	args, err := json.Marshal(map[string]any{"command": command})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	return args
}
