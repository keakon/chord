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

// TestShellConstructSetsAgreeOnCommandChaining pins the relationship between
// the two definitions of "contains shell syntax" in this package: the read-only
// classifier is the stricter one, and it must never accept a construct that a
// verification declaration already refuses.
func TestShellConstructSetsAgreeOnCommandChaining(t *testing.T) {
	for _, r := range shellCommandChainingCharacters {
		command := "ls a" + string(r) + "b"
		if !containsShellConstruct(command) {
			t.Fatalf("containsShellConstruct(%q) = false, want the read-only gate at least as strict as ValidateVerificationCommands", command)
		}
		if err := ValidateVerificationCommands([]string{command}); err == nil {
			t.Fatalf("ValidateVerificationCommands(%q) = nil, want the chaining character rejected", command)
		}
	}
	// Argument expansion is the deliberate difference: the read-only gate
	// refuses it because it decides which files the command reads, while a
	// verification declaration may carry it because the delegator authorized
	// that exact string and it introduces no second command.
	for _, r := range shellArgumentExpansionCharacters {
		command := "go test ./pkg" + string(r)
		if !containsShellConstruct(command) {
			t.Fatalf("containsShellConstruct(%q) = false, want expansion characters refused by the read-only gate", command)
		}
		if err := ValidateVerificationCommands([]string{command}); err != nil {
			t.Fatalf("ValidateVerificationCommands(%q) = %v, want expansion tolerated in a declaration", command, err)
		}
	}
}
