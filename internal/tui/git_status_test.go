package tui

import (
	"testing"
	"time"
)

func TestGitStatusRefreshDelayBackoffAndProfileSwitches(t *testing.T) {
	m := NewModel(newInfoPanelAgent())
	m.displayState = stateForeground
	m.resetGitStatusRefreshDelay(0)
	if m.gitStatus.NextDelay != gitStatusForegroundInitialInterval {
		t.Fatalf("foreground initial delay = %s", m.gitStatus.NextDelay)
	}
	m.advanceGitStatusRefreshDelay()
	if m.gitStatus.NextDelay != 30*time.Second {
		t.Fatalf("foreground backed off delay = %s, want 30s", m.gitStatus.NextDelay)
	}
	m.advanceGitStatusRefreshDelay()
	m.advanceGitStatusRefreshDelay()
	if m.gitStatus.NextDelay != gitStatusForegroundMaxInterval {
		t.Fatalf("foreground max delay = %s", m.gitStatus.NextDelay)
	}

	m.displayState = stateBackground
	m.switchGitStatusToBackgroundRefresh()
	if m.gitStatus.NextDelay != gitStatusBackgroundInitialInterval {
		t.Fatalf("background switch delay = %s", m.gitStatus.NextDelay)
	}
	m.advanceGitStatusRefreshDelay()
	if m.gitStatus.NextDelay != 2*time.Minute {
		t.Fatalf("background backed off delay = %s, want 2m", m.gitStatus.NextDelay)
	}
	m.advanceGitStatusRefreshDelay()
	m.advanceGitStatusRefreshDelay()
	if m.gitStatus.NextDelay != gitStatusBackgroundMaxInterval {
		t.Fatalf("background max delay = %s", m.gitStatus.NextDelay)
	}

	m.displayState = stateForeground
	m.switchGitStatusToForegroundRefresh()
	if m.gitStatus.NextDelay != gitStatusForegroundInitialInterval {
		t.Fatalf("foreground switch delay = %s", m.gitStatus.NextDelay)
	}
}

func TestGitStatusDisablesAfterMissingExecutable(t *testing.T) {
	m := NewModel(newInfoPanelAgent())
	m.gitStatus.Refreshing = true
	m.gitStatus.Generation = 1
	m.gitStatus.Info = gitStatusInfo{Present: true, Branch: "main"}

	cmd := m.handleGitStatusRefreshed(gitStatusRefreshedMsg{generation: 1, result: gitStatusResult{Disable: true}})
	if cmd != nil {
		t.Fatal("missing git executable should not schedule another refresh")
	}
	if !m.gitStatus.Disabled {
		t.Fatal("git status should be disabled after missing executable")
	}
	if m.gitStatus.Refreshing {
		t.Fatal("git status should not remain refreshing after disable")
	}
	if m.gitStatus.Info.Present {
		t.Fatal("git info should be cleared after disable")
	}
	if cmd := m.requestGitStatusRefresh(); cmd != nil {
		t.Fatalf("disabled git status should not request refresh, got %#v", cmd)
	}
}

func TestParseGitStatusPorcelainV2(t *testing.T) {
	info := parseGitStatusPorcelainV2("# branch.oid 1234567890abcdef\n# branch.head main\n# branch.upstream origin/main\n# branch.ab +14 -2\n1 .M N... 100644 100644 100644 abc abc file.go\n? new.txt\n")
	if info.Branch != "main" {
		t.Fatalf("Branch = %q, want main", info.Branch)
	}
	if info.Commit != "1234567890ab" {
		t.Fatalf("Commit = %q, want short oid", info.Commit)
	}
	if info.Ahead != 14 || info.Behind != 2 {
		t.Fatalf("ahead/behind = %d/%d, want 14/2", info.Ahead, info.Behind)
	}
	if info.ChangedFiles != 2 {
		t.Fatalf("ChangedFiles = %d, want 2", info.ChangedFiles)
	}
}

func TestCountGitStashEntries(t *testing.T) {
	got := countGitStashEntries("stash@{0}\nstash@{1}\n\n")
	if got != 2 {
		t.Fatalf("stash entries = %d, want 2", got)
	}
}

func TestGitStatusSummary(t *testing.T) {
	got := gitStatusSummary(gitStatusInfo{Present: true, Branch: "main", WorktreeName: "fix-ui", Ahead: 3, Behind: 1, ChangedFiles: 2, Stashes: 4})
	if got != "main@fix-ui ↑3 ↓1 !2 *4" {
		t.Fatalf("summary = %q", got)
	}
}

func TestShellCommandMayRunGit(t *testing.T) {
	for _, cmd := range []string{"git status", "pwd && git status", "echo ok; git push", "true || git pull", "pwd\ngit status"} {
		if !shellCommandMayRunGit(cmd) {
			t.Fatalf("%q should be detected as git command", cmd)
		}
	}
	for _, cmd := range []string{"gitlab --version", "echo git status", "rg git"} {
		if shellCommandMayRunGit(cmd) {
			t.Fatalf("%q should not be detected as git command", cmd)
		}
	}
}
