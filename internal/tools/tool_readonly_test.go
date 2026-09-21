package tools

import (
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
			if v := ClassifyShellReadOnly(command, "bash", false); !v.ReadOnly {
				t.Fatalf("ClassifyShellReadOnly(%q) = false (%s), want true", command, v.Reason)
			}
		})
	}
}

func TestShellReadOnlyCommandAllowed_RejectsMutations(t *testing.T) {
	cases := []string{
		"git status > out.txt",
		"$(pwd)",
		"echo `pwd`",
		"git checkout main",
		"git commit -m test",
		"rm -rf tmp",
	}
	for _, command := range cases {
		t.Run(command, func(t *testing.T) {
			if v := ClassifyShellReadOnly(command, "bash", false); v.ReadOnly {
				t.Fatalf("ClassifyShellReadOnly(%q) = true, want false", command)
			}
		})
	}
}

func TestShellReadOnlyCommandAllowed_AllowsReadOnlyChaining(t *testing.T) {
	// The AST classifier judges each side: chaining alone is not a veto.
	cases := []string{
		"git status && pwd",
		"git status; pwd",
		"git status | cat",
	}
	for _, command := range cases {
		t.Run(command, func(t *testing.T) {
			if v := ClassifyShellReadOnly(command, "bash", false); !v.ReadOnly {
				t.Fatalf("ClassifyShellReadOnly(%q) = false (%s), want true", command, v.Reason)
			}
		})
	}
}
