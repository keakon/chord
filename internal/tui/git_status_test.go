package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/mattn/go-runewidth"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/gitinspect"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
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
	m.gitStatus.Info = gitinspect.Info{Present: true, Branch: "main"}

	cmd := m.handleGitStatusRefreshed(gitStatusRefreshedMsg{generation: 1, result: gitinspect.Result{Disable: true}})
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

func TestGitStatusSummaryBudgetedFitsKeepsCanonicalOrder(t *testing.T) {
	info := gitinspect.Info{Present: true, Branch: "main", WorktreeName: "fix-ui", Ahead: 3, Behind: 1, ChangedFiles: 2, StagedFiles: 1, Stashes: 4}
	// Ample width: the composition must be byte-identical to the historical
	// ref-first summary, so nothing regresses for branches that already fit.
	got := gitStatusSummaryBudgeted(info, 100)
	if got != "main@fix-ui ↑3 ↓1 +1 !2 *4" {
		t.Fatalf("summary = %q, want %q", got, "main@fix-ui ↑3 ↓1 +1 !2 *4")
	}
}

func TestGitStatusSummaryBudgetedLongBranchKeepsCounts(t *testing.T) {
	info := gitinspect.Info{Present: true, Branch: "feature/very-long-branch-name-here", ChangedFiles: 2}
	got := gitStatusSummaryBudgeted(info, 22)
	if !strings.HasSuffix(got, "!2") {
		t.Fatalf("long branch must not hide the changed-file count, got %q", got)
	}
	if !strings.HasPrefix(got, "feature/") {
		t.Fatalf("branch head should stay recognizable, got %q", got)
	}
	if strings.Contains(got, "branch-name-here") {
		t.Fatalf("branch tail should be truncated, got %q", got)
	}
	if w := runewidth.StringWidth(got); w > 22 {
		t.Fatalf("summary width = %d, want <= 22: %q", w, got)
	}
}

func TestGitStatusSummaryBudgetedShortRefKeepsAllCounts(t *testing.T) {
	info := gitinspect.Info{Present: true, Branch: "ab", Ahead: 123, Behind: 12, StagedFiles: 1, ChangedFiles: 1, Stashes: 5}
	// A short ref already fits, so every count stays visible.
	got := gitStatusSummaryBudgeted(info, 22)
	if got != "ab ↑123 ↓12 +1 !1 *5" {
		t.Fatalf("summary = %q, want %q", got, "ab ↑123 ↓12 +1 !1 *5")
	}
}

func TestGitStatusSummaryBudgetedDropsLeastUsefulCountsForLongRef(t *testing.T) {
	original := runewidth.DefaultCondition.EastAsianWidth
	t.Cleanup(func() { runewidth.DefaultCondition.EastAsianWidth = original })
	info := gitinspect.Info{Present: true, Branch: "feature/very-long-branch-name-here", Ahead: 123, Behind: 12, StagedFiles: 1, ChangedFiles: 1, Stashes: 5}
	for _, testCase := range []struct {
		name string
		wide bool
		want string
	}{
		{name: "narrow", want: "feat... ↑123 ↓12 +1 !1"},
		{name: "wide", wide: true, want: "feature... ↑123 +1 !1"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			runewidth.DefaultCondition.EastAsianWidth = testCase.wide
			if got := gitStatusSummaryBudgeted(info, 22); got != testCase.want {
				t.Fatalf("summary = %q, want %q", got, testCase.want)
			}
		})
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

func TestShouldRefreshGitStatusAfterEditResult(t *testing.T) {
	if !shouldRefreshGitStatusAfterToolResult(agent.ToolResultEvent{Name: tools.NameEdit, Status: message.ToolStatusSuccess}) {
		t.Fatal("successful Edit should refresh git status")
	}
	if shouldRefreshGitStatusAfterToolResult(agent.ToolResultEvent{Name: tools.NameEdit, Status: message.ToolStatusError}) {
		t.Fatal("failed Edit should not refresh git status")
	}
	if !shouldRefreshGitStatusAfterToolResult(agent.ToolResultEvent{
		Name: tools.NameApplyPatch, Status: message.ToolStatusError,
		FileState: &message.ToolFileState{Writes: []message.TrackedFileState{{Path: "committed.go", Exists: true}}},
	}) {
		t.Fatal("partially applied patch should refresh git status")
	}
}
