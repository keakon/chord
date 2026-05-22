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
		"git commit --file msg.txt",
		"git commit --amend --no-edit",
		"git commit --amend -C HEAD",
		"git commit --amend -CHEAD",
		"git commit --amend --reuse-message HEAD",
		"git commit --amend --reuse-message=HEAD",
		"git commit -qam msg",
		"git commit --fixup=HEAD",
		"git commit --fixup HEAD",
		"GIT_SEQUENCE_EDITOR=: git rebase -i HEAD~2",
		"GIT_SEQUENCE_EDITOR=true git rebase -i HEAD~2",
		"git -C repo commit -m 'msg'",
		"git -c user.name=test commit -m 'msg'",
		"git --no-pager commit -m 'msg'",
		"git stash push -m 'save'",
		"docker --context remote exec container true",
		"docker exec container command -t arg",
		"docker --context=remote run --rm alpine true",
		"docker run alpine command -t arg",
		"docker run --name test alpine command -t arg",
		"podman --connection remote run alpine true",
		"kubectl --context prod exec pod -- true",
		"echo data > less",
		"printf data | cat > vim",
		"git rebase --continue",
		"npm init -y",
		"npm init --yes",
		"npm init",
		"pnpm init",
		"yarn init",
		"bun init",
		"cargo login token-from-file",
		"ssh -o BatchMode=yes example.com true",
		"ssh example.com true",
		"gh auth login --with-token < token.txt",
		"az login --service-principal -u app -p pass --tenant tenant",
		"aws configure set region us-east-1",
		"sudo -n make install",
		"echo stty",
		"read -r x",
		"select x in a b; do echo $x; done",
		"printf 'stdin_tty='; test -t 0 && echo yes || echo no; printf 'read_result='; IFS= read -r x; printf 'status:%s value:%s\\n' \"$?\" \"$x\"",
		"go test ./...",
		"printf 'y\\n' | some-command",
		"echo '/dev/tty'",
		"git show HEAD:path/to/file",
		"git diff -- README.md",
		"git branch --show-current",
		"python - <<'PY'\nprint('watch')\nPY",
		"python - <<'PY'\nprint('top')\nPY",
		"python - <<'PY'\nwatch = 'value'\nprint(watch)\nPY",
		"GIT_EDITOR=true git rebase -i HEAD~2",
		"GIT_SEQUENCE_EDITOR='python - <<\"PY\"\nprint(\"edit todo\")\nPY' git rebase -i HEAD~2",
		"GIT_SEQUENCE_EDITOR='sed -i s/pick/fixup/' git rebase -i HEAD~2",
		"printf 'y\nn\n' | git add -p",
		"printf 'y\nn\n' | git checkout -p",
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
		{"git commit", "without an explicit message"},
		{"git commit --amend", "without an explicit message"},
		{"git commit --amend -c HEAD", "without an explicit message"},
		{"git commit --amend --reedit-message HEAD", "without an explicit message"},
		{"git commit --fixup=amend:HEAD", "without an explicit message"},
		{"git commit --fixup amend:HEAD", "without an explicit message"},
		{"git commit --fixup=reword:HEAD", "without an explicit message"},
		{"git commit --fixup reword:HEAD", "without an explicit message"},
		{"git commit --squash=HEAD", "without an explicit message"},
		{"git commit -p -m 'msg'", "interactive patch"},
		{"git commit --patch --message=msg", "interactive patch"},
		{"git -C repo commit", "without an explicit message"},
		{"git rebase -i HEAD~2", "requires an editor"},
		{"git add -p", "interactive patch"},
		{"git add -i", "is interactive"},
		{"git checkout --patch file", "interactive patch"},
		{"git restore -p file", "interactive patch"},
		{"git reset -p", "interactive patch"},
		{"git stash -p", "interactive patch"},
		{"git clean -i", "is interactive"},
		{"git difftool", "launches an interactive tool"},
		{"git mergetool", "launches an interactive tool"},
		{"sudo make install", "may prompt for a password"},
		{"ssh example.com", "interactive login session"},
		{"ssh -tt example.com true", "allocates a TTY"},
		{"gh auth login", "authentication wizard"},
		{"gcloud auth login", "authentication wizard"},
		{"az login", "authentication wizard"},
		{"aws configure", "prompts for credentials"},
		{"cat </dev/tty", "direct /dev/tty redirection"},
		{"cat 2>/dev/tty", "direct /dev/tty redirection"},
		{"echo x 1>/dev/tty", "direct /dev/tty redirection"},
		{"stty -echo", "requires a terminal"},
		{"tput cols", "requires a terminal"},
		{"docker --context remote exec -it container sh", "allocates a TTY"},
		{"docker --tlsverify exec -it container sh", "allocates a TTY"},
		{"docker run --tty alpine sh", "allocates a TTY"},
		{"docker login", "may prompt for credentials"},
		{"podman --connection remote run -t alpine sh", "allocates a TTY"},
		{"kubectl --context prod exec -it pod -- sh", "allocates a TTY"},
		{"kubectl attach --tty pod", "allocates a TTY"},
		{"watch date", "interactive terminal UI"},
		{"top", "interactive terminal UI"},
		{"git add -p", "interactive patch workflow"},
		{"git checkout -p", "interactive patch workflow"},
		{"git rebase -i HEAD~2", "requires an editor"},
		{"GIT_SEQUENCE_EDITOR=vim git rebase -i HEAD~2", "requires an editor"},
		{"GIT_SEQUENCE_EDITOR='vim \"$1\"' git rebase -i HEAD~2", "requires an editor"},
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
