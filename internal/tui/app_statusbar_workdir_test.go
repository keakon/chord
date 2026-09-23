package tui

import (
	"strings"
	"testing"

	"github.com/mattn/go-runewidth"

	"github.com/keakon/chord/internal/agent"
)

func TestStatusBarPathFormsDisplay(t *testing.T) {
	bound := statusBarPathForms{value: "~/.local/state/chord/worktrees/repo-abc/feat-auth", repo: "chord", checkout: "feat-auth"}
	if got := bound.display(40); got != "chord · feat-auth" {
		t.Fatalf("wide bound display = %q, want the identity label", got)
	}
	if got := bound.display(len("feat-auth")); got != "feat-auth" {
		t.Fatalf("narrow bound display = %q, want the checkout name alone", got)
	}
	if got := bound.display(len("feat-auth") - 1); got != "" {
		t.Fatalf("too-narrow bound display = %q, want the region hidden", got)
	}
	// A managed checkout never renders a path-like form at any width: an
	// abbreviated identifier would be a different, misleading name.
	for width := 1; width <= 60; width++ {
		got := bound.display(width)
		if got == "" || got == "chord · feat-auth" || got == "feat-auth" {
			continue
		}
		t.Fatalf("bound display at width %d = %q, want an identity form or nothing", width, got)
	}

	unbound := statusBarPathForms{value: "~/Workspace/chord"}
	if got := unbound.display(40); got != "~/Workspace/chord" {
		t.Fatalf("wide unbound display = %q, want the abbreviated path", got)
	}
	if got := unbound.display(6); !strings.Contains(got, "…") || !strings.HasPrefix(got, "~") {
		t.Fatalf("narrow unbound display = %q, want a middle-truncated path", got)
	}
}

// The notification is an invalidation: the model applies the release the agent
// has published, never the generation named by the event.
func TestWorkDirChangedEventAppliesLatestSnapshot(t *testing.T) {
	backend := &sessionControlAgent{contentRoot: "/home/user/projects/chord"}
	backend.workDir = "/state/worktrees/repo-abc/feat-one"
	backend.workDirID = "feat-one"
	backend.workDirGeneration = 1
	m := NewModelWithSize(backend, 200, 24)
	m.layout = m.generateLayout(m.width, m.height)
	if m.workingDirID != "feat-one" || m.workingDirGeneration != 1 {
		t.Fatalf("seeded checkout = %q gen %d, want the startup worktree release", m.workingDirID, m.workingDirGeneration)
	}

	backend.workDir = "/state/worktrees/repo-abc/feat-two"
	backend.workDirID = "feat-two"
	backend.workDirGeneration = 2
	updated, _ := m.Update(agentEventBatchMsg{{event: agent.WorkDirChangedEvent{Generation: 1}}})
	m = *updated.(*Model)
	if m.workingDirID != "feat-two" || m.workingDirGeneration != 2 {
		t.Fatalf("checkout after event = %q gen %d, want feat-two gen 2", m.workingDirID, m.workingDirGeneration)
	}
	if got := stripANSI(m.renderStatusBar()); !strings.Contains(got, "chord · feat-two") {
		t.Fatalf("status bar = %q, want the current checkout's identity label", got)
	}
	if want := displayWorkingDir(backend.workDir); m.statusPath.value != want {
		t.Fatalf("copy value = %q, want the full checkout path %q", m.statusPath.value, want)
	}
}

// Leaving a worktree keeps the session's startup directory, so the path stays
// the same while the identity is cleared; the display must follow the identity.
func TestWorkDirIdentityClearedSwitchesBackToPath(t *testing.T) {
	backend := &sessionControlAgent{contentRoot: "/home/user/projects/chord"}
	backend.workDir = "/state/worktrees/repo-abc/feat-one"
	backend.workDirID = "feat-one"
	backend.workDirGeneration = 1
	m := NewModelWithSize(backend, 200, 24)
	m.layout = m.generateLayout(m.width, m.height)
	if got := stripANSI(m.renderStatusBar()); !strings.Contains(got, "chord · feat-one") {
		t.Fatalf("status bar = %q, want the worktree identity label", got)
	}

	backend.workDirID = ""
	backend.workDirGeneration = 2
	updated, _ := m.Update(agentEventBatchMsg{{event: agent.WorkDirChangedEvent{Generation: 2}}})
	m = *updated.(*Model)
	if m.workingDirID != "" {
		t.Fatalf("workingDirID = %q, want cleared", m.workingDirID)
	}
	if m.workingDir != backend.workDir {
		t.Fatalf("workingDir = %q, want the unchanged path %q", m.workingDir, backend.workDir)
	}
	got := stripANSI(m.renderStatusBar())
	if strings.Contains(got, "chord · feat-one") {
		t.Fatalf("status bar = %q, stale worktree label survived the switch", got)
	}
	if want := displayWorkingDir(backend.workDir); m.statusPath.display != want {
		t.Fatalf("path region display = %q, want the abbreviated checkout path %q", m.statusPath.display, want)
	}
	if !strings.Contains(got, displayWorkingDir(backend.workDir)) {
		t.Fatalf("status bar = %q, want the abbreviated checkout path", got)
	}
}

// Notifications are best-effort: a batch of unrelated events must still pick up
// a checkout change whose notification was dropped.
func TestEventBatchReconcilesDroppedWorkDirNotification(t *testing.T) {
	backend := &sessionControlAgent{contentRoot: "/home/user/projects/chord"}
	backend.workDir = "/state/worktrees/repo-abc/feat-one"
	backend.workDirID = "feat-one"
	backend.workDirGeneration = 1
	m := NewModelWithSize(backend, 200, 24)
	m.layout = m.generateLayout(m.width, m.height)

	backend.workDir = "/state/worktrees/repo-abc/feat-two"
	backend.workDirID = "feat-two"
	backend.workDirGeneration = 2
	updated, _ := m.Update(agentEventBatchMsg{})
	m = *updated.(*Model)
	if m.workingDirID != "feat-two" || m.workingDirGeneration != 2 {
		t.Fatalf("checkout after an unrelated batch = %q gen %d, want the published release", m.workingDirID, m.workingDirGeneration)
	}
	if got := stripANSI(m.renderStatusBar()); !strings.Contains(got, "chord · feat-two") {
		t.Fatalf("status bar = %q, want the reconciled identity label", got)
	}
}

// Applying a new checkout must also refresh the git snapshot: the info panel's
// GIT section keeps rendering the previous checkout's branch, worktree name and
// change count until a refresh lands, which is the same "new directory, old
// worktree name" pairing the checkout switch exists to remove. The event alone
// cannot be what requests the refresh — the batch entry reconciles the same
// published snapshot before the event is dispatched, so the per-event branch's
// change check is false by then.
func TestWorkDirChangedEventRequestsGitStatusRefresh(t *testing.T) {
	backend := &sessionControlAgent{contentRoot: "/home/user/projects/chord"}
	backend.workDir = "/state/worktrees/repo-abc/feat-one"
	backend.workDirID = "feat-one"
	backend.workDirGeneration = 1
	m := NewModelWithSize(backend, 200, 24)
	m.layout = m.generateLayout(m.width, m.height)
	before := m.gitStatus.Generation

	backend.workDir = "/state/worktrees/repo-abc/feat-two"
	backend.workDirID = "feat-two"
	backend.workDirGeneration = 2
	updated, _ := m.Update(agentEventBatchMsg{{event: agent.WorkDirChangedEvent{Generation: 1}}})
	m = *updated.(*Model)

	if m.workingDirID != "feat-two" || m.workingDirGeneration != 2 {
		t.Fatalf("checkout after event = %q gen %d, want feat-two gen 2", m.workingDirID, m.workingDirGeneration)
	}
	if m.gitStatus.Generation <= before {
		t.Fatalf("git status generation = %d, want it advanced past %d so the new checkout is collected", m.gitStatus.Generation, before)
	}
	if !m.gitStatus.Refreshing {
		t.Fatal("a checkout change must leave a git refresh in flight")
	}
}

// A git refresh in flight when the checkout switches describes the previous
// directory; it must be dropped rather than presented as the current one.
func TestGitStatusResultForPreviousCheckoutDropped(t *testing.T) {
	backend := &sessionControlAgent{contentRoot: "/repo/wt", workDir: "/repo/wt"}
	m := NewModelWithSize(backend, 160, 24)
	m.gitStatus.Generation = 7

	cmd := m.handleGitStatusRefreshed(gitStatusRefreshedMsg{
		generation: 7,
		dir:        "/repo/other",
		result:     gitStatusResult{Info: gitStatusInfo{Present: true, Branch: "other"}},
	})
	if m.gitStatus.Info.Present {
		t.Fatalf("git info = %+v, want the previous checkout's result dropped", m.gitStatus.Info)
	}
	if m.gitStatus.Refreshing {
		t.Fatal("dropping a stale result must clear the refreshing flag")
	}
	if cmd == nil {
		t.Fatal("a dropped result must schedule a fresh refresh")
	}

	cmd = m.handleGitStatusRefreshed(gitStatusRefreshedMsg{
		generation: 7,
		dir:        "/repo/wt",
		result:     gitStatusResult{Info: gitStatusInfo{Present: true, Branch: "main"}},
	})
	if !m.gitStatus.Info.Present || m.gitStatus.Info.Branch != "main" {
		t.Fatalf("git info = %+v, want the current checkout's result applied", m.gitStatus.Info)
	}
	if cmd == nil {
		t.Fatal("applying a result must keep scheduling refreshes")
	}
}

// The applied snapshot records the checkout it describes, which is what lets
// the panel tell a current worktree identity from one that outlived its
// checkout.
func TestGitStatusResultStampsCheckoutDir(t *testing.T) {
	backend := &sessionControlAgent{contentRoot: "/repo/wt", workDir: "/repo/wt"}
	m := NewModelWithSize(backend, 160, 24)
	m.gitStatus.Generation = 4

	m.handleGitStatusRefreshed(gitStatusRefreshedMsg{
		generation: 4,
		dir:        "/repo/wt",
		result:     gitStatusResult{Info: gitStatusInfo{Present: true, Branch: "main", WorktreeName: "wt"}},
	})
	if m.gitStatus.Info.Dir != "/repo/wt" {
		t.Fatalf("applied snapshot dir = %q, want the checkout it was collected in", m.gitStatus.Info.Dir)
	}
}

// The expanded panel pairs the git snapshot's worktree name with the live
// checkout path, so a snapshot from the previous checkout must not relabel the
// new directory: the path row waits for a snapshot that describes it.
func TestInfoPanelHidesWorktreePathForPreviousCheckout(t *testing.T) {
	backend := &sessionControlAgent{contentRoot: "/repo", workDir: "/state/worktrees/repo-abc/feat-two"}
	m := NewModelWithSize(backend, 160, 24)
	m.gitStatus.Info = gitStatusInfo{
		Present:      true,
		Branch:       "chord/feat-one",
		WorktreeName: "feat-one",
		Dir:          "/state/worktrees/repo-abc/feat-one",
		ChangedFiles: 2,
	}
	m.toggleInfoPanelSection(infoPanelSectionGit)

	joined := strings.Join(infoPanelSectionLines(infoPanelPlainLines(m.renderInfoPanel(48, 24)), "▼ GIT"), "\n")
	if !strings.Contains(joined, "Worktree: feat-one") {
		t.Fatalf("expanded git section should keep the snapshot's worktree name, got:\n%s", joined)
	}
	if strings.Contains(joined, "Worktree Path:") {
		t.Fatalf("a snapshot from the previous checkout must not show a live path row, got:\n%s", joined)
	}
	if strings.Contains(joined, "/state/worktrees/repo-abc/feat-two") {
		t.Fatalf("expanded git section must not pair the old worktree name with the new path, got:\n%s", joined)
	}
}

// The path region budget is measured in display cells: a CJK repository name or
// checkout label must not be reported as fitting when it does not, nor be cut
// apart to fit.
func TestStatusBarPathFormsDisplayMeasuresCJKWidth(t *testing.T) {
	forms := statusBarPathForms{
		value:    "/state/worktrees/repo-abc/特性",
		repo:     "仓库",
		checkout: "特性",
	}
	full := forms.repo + statusBarPathLabelSeparator + forms.checkout
	fullWidth := runewidth.StringWidth(full)
	if got := forms.display(fullWidth); got != full {
		t.Fatalf("display(%d) = %q, want the full label at its exact width", fullWidth, got)
	}
	if got := forms.display(fullWidth - 1); got != forms.checkout {
		t.Fatalf("display(%d) = %q, want the checkout label alone", fullWidth-1, got)
	}
	tooNarrow := runewidth.StringWidth(forms.checkout) - 1
	if got := forms.display(tooNarrow); got != "" {
		t.Fatalf("display(%d) = %q, want the region hidden", tooNarrow, got)
	}
}

// A restore rebuild — compaction applying a summary, a reload of the same
// session — must leave the path region as it was while the agent's binding is
// unchanged. The path used to move at compaction time because the region
// followed a rebuild rather than the agent's published checkout, so this is the
// regression guard that an unrelated rebuild no longer moves it.
func TestSessionRestoreWithUnchangedBindingKeepsWorkDirDisplay(t *testing.T) {
	backend := &sessionControlAgent{contentRoot: "/home/user/projects/chord"}
	backend.workDir = "/state/worktrees/repo-abc/feat-one"
	backend.workDirID = "feat-one"
	backend.workDirGeneration = 3
	m := NewModelWithSize(backend, 200, 24)
	m.layout = m.generateLayout(m.width, m.height)
	if got := stripANSI(m.renderStatusBar()); !strings.Contains(got, "chord · feat-one") {
		t.Fatalf("status bar = %q, want the checkout identity label", got)
	}
	beforeValue := m.statusPath.value
	beforeDisplay := m.statusPath.display

	applyTestCmd(t, &m, m.handleAgentEvent(agentEventMsg{event: agent.SessionRestoredEvent{}}))

	if m.workingDirID != "feat-one" || m.workingDirGeneration != 3 {
		t.Fatalf("checkout identity = %q gen %d, want the unchanged release", m.workingDirID, m.workingDirGeneration)
	}
	if m.statusPath.value != beforeValue || m.statusPath.display != beforeDisplay {
		t.Fatalf("path region = %q (copy %q), want the unchanged %q (copy %q)", m.statusPath.display, m.statusPath.value, beforeDisplay, beforeValue)
	}
	if got := stripANSI(m.renderStatusBar()); !strings.Contains(got, "chord · feat-one") {
		t.Fatalf("status bar after restore = %q, want the identity label still", got)
	}
}
