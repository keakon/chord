package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/keakon/bubbletea/v2"
	"github.com/mattn/go-runewidth"

	"github.com/keakon/chord/internal/gitinspect"
	"github.com/keakon/chord/internal/tui/terminaltext"
)

const (
	gitStatusForegroundInitialInterval = 15 * time.Second
	gitStatusForegroundMaxInterval     = time.Minute
	gitStatusBackgroundInitialInterval = time.Minute
	gitStatusBackgroundMaxInterval     = 5 * time.Minute
	// gitStatusSummaryMinRefWidth caps how many display columns are reserved for
	// an over-long ref when status counts compete for the row: counts are dropped
	// only while the ref could not otherwise keep at least this many columns (or
	// its full width, when the ref itself is shorter).
	gitStatusSummaryMinRefWidth = 6
)

type gitStatusState struct {
	Info       gitinspect.Info
	Refreshing bool
	Disabled   bool
	Generation uint64
	NextDelay  time.Duration
}

type gitStatusRefreshedMsg struct {
	generation uint64
	// dir is the checkout the result describes. A refresh in flight when the
	// checkout switches would otherwise land as the new directory's git data.
	dir    string
	result gitinspect.Result
}

type gitStatusTickMsg struct {
	generation uint64
}

// currentCheckoutPath is the directory the panel's git data must describe: the
// agent's live checkout, read in one snapshot so a path and an identity can
// never come from different switches.
func (m *Model) currentCheckoutPath() string {
	if m == nil {
		return ""
	}
	if m.agent != nil {
		if snap := m.agent.WorkDirSnapshot(); strings.TrimSpace(snap.Path) != "" {
			return strings.TrimSpace(snap.Path)
		}
	}
	return strings.TrimSpace(m.workingDir)
}

func (m *Model) requestGitStatusRefresh() tea.Cmd {
	if m == nil || m.agent == nil || m.gitStatus.Refreshing || m.gitStatus.Disabled {
		return nil
	}
	workDir := m.currentCheckoutPath()
	if workDir == "" {
		return nil
	}
	m.gitStatus.Refreshing = true
	m.gitStatus.Generation++
	generation := m.gitStatus.Generation
	return func() tea.Msg {
		return gitStatusRefreshedMsg{generation: generation, dir: workDir, result: gitinspect.Collect(workDir)}
	}
}

func (m *Model) currentGitStatusInitialInterval() time.Duration {
	if m.displayState == stateBackground {
		return gitStatusBackgroundInitialInterval
	}
	return gitStatusForegroundInitialInterval
}

func (m *Model) currentGitStatusMaxInterval() time.Duration {
	if m.displayState == stateBackground {
		return gitStatusBackgroundMaxInterval
	}
	return gitStatusForegroundMaxInterval
}

func (m *Model) resetGitStatusRefreshDelay(delay time.Duration) {
	if delay <= 0 {
		delay = m.currentGitStatusInitialInterval()
	}
	m.gitStatus.NextDelay = delay
}

func (m *Model) advanceGitStatusRefreshDelay() {
	if m.gitStatus.NextDelay <= 0 {
		m.gitStatus.NextDelay = m.currentGitStatusInitialInterval()
		return
	}
	maxDelay := m.currentGitStatusMaxInterval()
	next := min(m.gitStatus.NextDelay*2, maxDelay)
	m.gitStatus.NextDelay = next
}

func (m *Model) switchGitStatusRefreshProfile(delay time.Duration) tea.Cmd {
	if m == nil || m.agent == nil {
		return nil
	}
	m.gitStatus.Refreshing = false
	m.gitStatus.Generation++
	m.resetGitStatusRefreshDelay(delay)
	return m.scheduleGitStatusTick()
}

func (m *Model) switchGitStatusToForegroundRefresh() tea.Cmd {
	if m == nil || m.agent == nil {
		return nil
	}
	m.gitStatus.Refreshing = false
	m.gitStatus.Generation++
	m.resetGitStatusRefreshDelay(gitStatusForegroundInitialInterval)
	return m.requestGitStatusRefresh()
}

func (m *Model) switchGitStatusToBackgroundRefresh() tea.Cmd {
	return m.switchGitStatusRefreshProfile(gitStatusBackgroundInitialInterval)
}

func (m *Model) scheduleGitStatusTick() tea.Cmd {
	if m == nil {
		return nil
	}
	delay := m.gitStatus.NextDelay
	if delay <= 0 {
		delay = m.currentGitStatusInitialInterval()
		m.gitStatus.NextDelay = delay
	}
	generation := m.gitStatus.Generation
	return tickCmd(delay, func(time.Time) tea.Msg {
		return gitStatusTickMsg{generation: generation}
	})
}

func gitStatusInfoEqual(a, b gitinspect.Info) bool {
	return a.Present == b.Present &&
		a.Branch == b.Branch &&
		a.Commit == b.Commit &&
		a.WorktreeName == b.WorktreeName &&
		a.Dir == b.Dir &&
		a.ChangedFiles == b.ChangedFiles &&
		a.StagedFiles == b.StagedFiles &&
		a.ConflictedFiles == b.ConflictedFiles &&
		a.Stashes == b.Stashes &&
		a.Ahead == b.Ahead &&
		a.Behind == b.Behind
}

func (m *Model) handleGitStatusRefreshed(msg gitStatusRefreshedMsg) tea.Cmd {
	if msg.generation != m.gitStatus.Generation {
		return nil
	}
	m.gitStatus.Refreshing = false
	if msg.result.Disable {
		m.gitStatus.Disabled = true
		m.gitStatus.Info = gitinspect.Info{}
		m.cachedInfoPanelFP = ""
		m.cachedInfoPanelOut = ""
		return nil
	}
	if msg.dir != m.currentCheckoutPath() {
		// The checkout switched while this refresh was in flight: the result
		// describes the previous directory, so drop it and schedule a fresh one
		// instead of presenting it as the current checkout's git data.
		return m.scheduleGitStatusTick()
	}
	previous := m.gitStatus.Info
	// Stamp the directory the snapshot describes here, where it is known to be
	// the live checkout, so the panel can tell whether its worktree identity and
	// the directory path it pairs with it still belong together.
	m.gitStatus.Info = msg.result.Info
	m.gitStatus.Info.Dir = msg.dir
	if gitStatusInfoEqual(previous, msg.result.Info) {
		m.advanceGitStatusRefreshDelay()
	} else {
		m.resetGitStatusRefreshDelay(0)
	}
	m.cachedInfoPanelFP = ""
	m.cachedInfoPanelOut = ""
	return m.scheduleGitStatusTick()
}

func (m *Model) handleGitStatusTick(msg gitStatusTickMsg) tea.Cmd {
	if msg.generation != m.gitStatus.Generation {
		return nil
	}
	return m.requestGitStatusRefresh()
}

// gitStatusRef returns the displayed repository ref: the branch with a
// linked-worktree suffix, or the short commit / "detached" when not on a branch.
func gitStatusRef(info gitinspect.Info) string {
	ref := info.Branch
	if ref == "" {
		ref = info.Commit
	}
	if ref == "" {
		ref = "detached"
	}
	if info.WorktreeName != "" {
		ref += "@" + info.WorktreeName
	}
	return ref
}

// gitStatusStatusPart is one numeric status marker of the collapsed git header
// summary. The slice keeps the canonical display order (sync, staged, conflicted,
// changed, stash); dropWeight ranks markers so the least useful one can be dropped
// first when the counts would leave no room for a recognizable ref — the changed-file
// count is the most actionable and survives longest, stash counts go first.
type gitStatusStatusPart struct {
	text       string
	dropWeight int
}

// gitStatusStatusParts lists the numeric markers of the collapsed git header in
// canonical display order: sync, staged, conflicts, changed, stash. dropWeight
// ranks them for the case where the counts would crowd the ref below the minimum
// recognizable width, and the highest weight goes first. The order encodes
// urgency rather than importance: the changed-file count is the primary "work in
// progress" signal and survives longest, an unresolved conflict blocks committing
// so it outranks the ahead/behind sync numbers, and stash counts go first.
func gitStatusStatusParts(info gitinspect.Info) []gitStatusStatusPart {
	var parts []gitStatusStatusPart
	if info.Ahead > 0 {
		parts = append(parts, gitStatusStatusPart{text: fmt.Sprintf("↑%d", info.Ahead), dropWeight: 3})
	}
	if info.Behind > 0 {
		parts = append(parts, gitStatusStatusPart{text: fmt.Sprintf("↓%d", info.Behind), dropWeight: 4})
	}
	if info.StagedFiles > 0 {
		parts = append(parts, gitStatusStatusPart{text: fmt.Sprintf("+%d", info.StagedFiles), dropWeight: 2})
	}
	if info.ConflictedFiles > 0 {
		parts = append(parts, gitStatusStatusPart{text: fmt.Sprintf("~%d", info.ConflictedFiles), dropWeight: 1})
	}
	if info.ChangedFiles > 0 {
		parts = append(parts, gitStatusStatusPart{text: fmt.Sprintf("!%d", info.ChangedFiles), dropWeight: 0})
	}
	if info.Stashes > 0 {
		parts = append(parts, gitStatusStatusPart{text: fmt.Sprintf("*%d", info.Stashes), dropWeight: 5})
	}
	return parts
}

func gitStatusJoinStatusParts(parts []gitStatusStatusPart) string {
	texts := make([]string, 0, len(parts))
	for _, p := range parts {
		texts = append(texts, p.text)
	}
	return strings.Join(texts, " ")
}

// gitStatusSummaryBudgeted composes the collapsed git header summary to fit
// maxW display columns. Status counts always stay fully visible (the least
// useful one is dropped first only when they would crowd the ref below the
// minimum); the ref itself is truncated from the tail, so a long branch name
// no longer hides how many files changed or are staged.
func gitStatusSummaryBudgeted(info gitinspect.Info, maxW int) string {
	if maxW < 1 {
		return ""
	}
	ref := gitStatusRef(info)
	parts := gitStatusStatusParts(info)
	if len(parts) == 0 {
		return terminaltext.TruncateOneLine(ref, maxW)
	}
	joined := gitStatusJoinStatusParts(parts)
	// Reserve room for the ref — its full width when short, otherwise at least
	// the minimum recognizable width — before considering which counts to drop.
	needRef := min(runewidth.StringWidth(ref), gitStatusSummaryMinRefWidth)
	for len(parts) > 1 && maxW-1-runewidth.StringWidth(joined) < needRef {
		drop := 0
		for i := 1; i < len(parts); i++ {
			if parts[i].dropWeight > parts[drop].dropWeight {
				drop = i
			}
		}
		parts = append(parts[:drop], parts[drop+1:]...)
		joined = gitStatusJoinStatusParts(parts)
	}
	refW := maxW - 1 - runewidth.StringWidth(joined)
	if refW < 1 {
		return joined
	}
	if runewidth.StringWidth(ref) > refW {
		ref = terminaltext.TruncateOneLine(ref, refW)
	}
	return ref + " " + joined
}
