package tools

import (
	"strings"
	"testing"
)

func TestDetectInteractiveShellCommandAllowsNonInteractiveCommands(t *testing.T) {
	cases := []string{
		"git status",
		"git commit -m 'msg'",
		"git commit --message=msg --allow-empty",
		"git commit -F msg.txt",
		"git rebase --continue",
		"npm init -y",
		"npm init --yes",
		"sudo -n make install",
		"go test ./...",
		"printf 'y\\n' | some-command",
		"echo '/dev/tty'",
	}
	for _, command := range cases {
		t.Run(command, func(t *testing.T) {
			if got := DetectInteractiveShellCommand(command); got != nil {
				t.Fatalf("DetectInteractiveShellCommand(%q) = %#v, want nil", command, got)
			}
		})
	}
}

func TestDetectInteractiveShellCommandRejectsHighConfidenceInteractiveCommands(t *testing.T) {
	cases := []struct {
		command string
		want    string
	}{
		{"vim file", "interactive terminal UI"},
		{"less README.md", "interactive terminal UI"},
		{"man git", "interactive terminal UI"},
		{"git commit", "without -m/-F opens an editor"},
		{"git rebase -i HEAD~2", "requires an editor"},
		{"git add -p", "interactive patch"},
		{"git checkout --patch file", "interactive patch"},
		{"git restore -p file", "interactive patch"},
		{"git reset -p", "interactive patch"},
		{"git clean -i", "is interactive"},
		{"git difftool", "launches an interactive tool"},
		{"git mergetool", "launches an interactive tool"},
		{"sudo make install", "may prompt for a password"},
		{"ssh example.com", "may require login"},
		{"gh auth login", "authentication wizard"},
		{"gcloud auth login", "authentication wizard"},
		{"az login", "authentication wizard"},
		{"aws configure", "prompts for credentials"},
		{"npm init", "may prompt"},
		{"pnpm init", "may prompt"},
		{"yarn init", "may prompt"},
		{"bun init", "may prompt"},
		{"cargo login", "may prompt"},
		{"read -p 'continue?' x", "waits for user input"},
		{"select x in a b; do echo $x; done", "waits for user input"},
		{"cat </dev/tty", "direct /dev/tty redirection"},
		{"cat 2>/dev/tty", "direct /dev/tty redirection"},
		{"echo x 1>/dev/tty", "direct /dev/tty redirection"},
		{"stty -echo", "requires a terminal"},
		{"PATH=/tmp vim file", "interactive terminal UI"},
	}
	for _, tc := range cases {
		t.Run(tc.command, func(t *testing.T) {
			got := DetectInteractiveShellCommand(tc.command)
			if got == nil {
				t.Fatalf("DetectInteractiveShellCommand(%q) = nil, want finding containing %q", tc.command, tc.want)
			}
			if !strings.Contains(got.Reason, tc.want) {
				t.Fatalf("reason = %q, want to contain %q", got.Reason, tc.want)
			}
			if got.Hint == "" {
				t.Fatal("expected actionable hint")
			}
		})
	}
}
