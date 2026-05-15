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

func TestContainsShellMetacharDetectsNewlyBlockedCharacters(t *testing.T) {
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
			if got := containsShellMetachar(tc.command); got != tc.want {
				t.Fatalf("containsShellMetachar(%q) = %v, want %v", tc.command, got, tc.want)
			}
		})
	}
}
