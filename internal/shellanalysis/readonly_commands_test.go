package shellanalysis

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
			if v := ClassifyReadOnly(command, "bash", false); !v.ReadOnly {
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
			if v := ClassifyReadOnly(command, "bash", false); v.ReadOnly {
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
			if v := ClassifyReadOnly(command, "bash", false); !v.ReadOnly {
				t.Fatalf("ClassifyShellReadOnly(%q) = false (%s), want true", command, v.Reason)
			}
		})
	}
}

func TestShellReadOnlyAllowlistCoversNewCommands(t *testing.T) {
	allowed := []string{
		"head -n 5 file", "tail -n 5 file", "wc -l file", "stat file", "file x", "file -b x", "file --mime-type x", "du -sh .", "df -h",
	}
	for _, command := range allowed {
		if v := ClassifyReadOnly(command, "bash", false); !v.ReadOnly {
			t.Errorf("ClassifyShellReadOnly(%q) = false (%s), want true", command, v.Reason)
		}
	}
	denied := []string{
		"tail -f log", "tail -F log", "tail --follow log", "tail --follow=name log", "tail -fn 100 log", "rm -rf x", "go test ./...", "cat file > out",
		// file -C/--compile writes magic.mgc, so it is not a read.
		"file -C", "file --compile", "file -Cm custom.magic", "file --compile --magic custom.magic",
	}
	for _, command := range denied {
		if v := ClassifyReadOnly(command, "bash", false); v.ReadOnly {
			t.Errorf("ClassifyShellReadOnly(%q) = true, want false", command)
		}
	}
	if v := ClassifyReadOnly("git status --short", "bash", true); v.ReadOnly {
		t.Error("ClassifyShellReadOnly(background git status) = true, want false")
	}
}
