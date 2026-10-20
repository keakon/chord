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

// A path with staged content counts only as staged, so the two counts cannot
// overlap and the summary stays a faithful partition of the changed paths.
func TestGitStatusSummaryDoesNotDoubleCountPartiallyStaged(t *testing.T) {
	// 4 paths in total: 2 staged (one of them also dirty in the worktree),
	// 1 changed only, 1 untracked.
	info := gitinspect.Info{Present: true, Branch: "main", ChangedFiles: 2, StagedFiles: 2}
	if got := gitStatusSummaryBudgeted(info, 100); got != "main +2 !2" {
		t.Fatalf("summary = %q, want %q", got, "main +2 !2")
	}
}

// Unmerged paths are their own marker, ordered after staged and before changed.
func TestGitStatusSummaryShowsConflictedMarker(t *testing.T) {
	info := gitinspect.Info{Present: true, Branch: "main", ChangedFiles: 2, StagedFiles: 1, ConflictedFiles: 3}
	if got := gitStatusSummaryBudgeted(info, 100); got != "main +1 ~3 !2" {
		t.Fatalf("summary = %q, want %q", got, "main +1 ~3 !2")
	}
}

// ConflictedFiles is part of the snapshot identity: a refresh that only changes
// the conflict count must not read as an unchanged snapshot, or the adaptive
// polling delay would keep growing through the one change the panel shows.
func TestGitStatusInfoEqualComparesConflictedFiles(t *testing.T) {
	base := gitinspect.Info{Present: true, Branch: "main", ChangedFiles: 1, StagedFiles: 1, Stashes: 2, Ahead: 1, Behind: 1}
	if !gitStatusInfoEqual(base, base) {
		t.Fatal("identical snapshots reported as changed")
	}
	conflicted := base
	conflicted.ConflictedFiles = 2
	if gitStatusInfoEqual(base, conflicted) {
		t.Fatal("a conflict-only change was reported as an unchanged snapshot")
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

// Drop order is urgency-ranked, not display order: the changed count outlives
// conflicts, which outlive staged, which outlive sync; stash counts go first.
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

// A conflict blocks committing, so the ~ marker outranks the sync numbers and the
// staged count when the ref has to share its space, while the stash count is the
// first to go.
func TestGitStatusSummaryConflictsOutrankSyncAndStaged(t *testing.T) {
	info := gitinspect.Info{Present: true, Branch: "feature/very-long-branch-name-here", Ahead: 123, Behind: 12, StagedFiles: 7, ChangedFiles: 3, ConflictedFiles: 2, Stashes: 5}
	for _, width := range []int{22, 18, 14} {
		got := gitStatusSummaryBudgeted(info, width)
		if !strings.Contains(got, "~2") || !strings.Contains(got, "!3") {
			t.Fatalf("conflict and changed counts must survive at width %d, got %q", width, got)
		}
		if strings.Contains(got, "*5") {
			t.Fatalf("stash count should be dropped first, got %q", got)
		}
	}
	// At the tightest width the sync numbers and the staged count give way.
	if got := gitStatusSummaryBudgeted(info, 18); strings.Contains(got, "↑123") {
		t.Fatalf("sync count should be dropped before conflicts, got %q", got)
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
