package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	tea "github.com/keakon/bubbletea/v2"
	"github.com/mattn/go-runewidth"
)

const (
	gitStatusForegroundInitialInterval = 15 * time.Second
	gitStatusForegroundMaxInterval     = time.Minute
	gitStatusBackgroundInitialInterval = time.Minute
	gitStatusBackgroundMaxInterval     = 5 * time.Minute
	gitStatusCommandTimeout            = time.Second
	// gitStatusSummaryMinRefWidth caps how many display columns are reserved for
	// an over-long ref when status counts compete for the row: counts are dropped
	// only while the ref could not otherwise keep at least this many columns (or
	// its full width, when the ref itself is shorter).
	gitStatusSummaryMinRefWidth = 6
)

type gitStatusState struct {
	Info       gitStatusInfo
	Refreshing bool
	Disabled   bool
	Generation uint64
	NextDelay  time.Duration
}

type gitStatusResult struct {
	Info    gitStatusInfo
	Disable bool
}

type gitStatusInfo struct {
	Present bool
	Branch  string
	Commit  string
	// WorktreeName is the linked worktree the snapshot was taken in, and Dir is
	// the checkout it describes. The panel pairs the name with a live directory
	// path, so both must be kept: a snapshot that outlived its checkout would
	// otherwise label a different directory as that worktree's location.
	WorktreeName string
	Dir          string
	ChangedFiles int
	StagedFiles  int
	Stashes      int
	Ahead        int
	Behind       int
	CapturedAt   time.Time
}

type gitStatusRefreshedMsg struct {
	generation uint64
	// dir is the checkout the result describes. A refresh in flight when the
	// checkout switches would otherwise land as the new directory's git data.
	dir    string
	result gitStatusResult
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
		return gitStatusRefreshedMsg{generation: generation, dir: workDir, result: collectGitStatus(workDir)}
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

func gitStatusInfoEqual(a, b gitStatusInfo) bool {
	return a.Present == b.Present &&
		a.Branch == b.Branch &&
		a.Commit == b.Commit &&
		a.WorktreeName == b.WorktreeName &&
		a.Dir == b.Dir &&
		a.ChangedFiles == b.ChangedFiles &&
		a.StagedFiles == b.StagedFiles &&
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
		m.gitStatus.Info = gitStatusInfo{}
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

func collectGitStatus(workDir string) gitStatusResult {
	if !hasGitMarker(workDir) {
		return gitStatusResult{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), gitStatusCommandTimeout)
	defer cancel()

	root, gitDir, err := gitRevParse(ctx, workDir)
	if err != nil || root == "" {
		return gitStatusResult{Disable: isGitExecutableMissing(err)}
	}
	out, err := gitCommand(ctx, workDir, "status", "--porcelain=v2", "--branch", "--untracked-files=normal")
	if err != nil {
		return gitStatusResult{Disable: isGitExecutableMissing(err)}
	}
	info := parseGitStatusPorcelainV2(out)
	stashOut, err := gitCommand(ctx, workDir, "stash", "list", "--format=%gd")
	if err != nil {
		return gitStatusResult{Disable: isGitExecutableMissing(err)}
	}
	info.Present = true
	info.Stashes = countGitStashEntries(stashOut)
	info.WorktreeName = linkedWorktreeName(root, gitDir)
	info.CapturedAt = time.Now()
	return gitStatusResult{Info: info}
}

func isGitExecutableMissing(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, exec.ErrNotFound)
}

func hasGitMarker(workDir string) bool {
	if workDir == "" {
		return false
	}
	dir, err := filepath.Abs(workDir)
	if err != nil {
		return false
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return false
		}
		dir = parent
	}
}

func gitRevParse(ctx context.Context, workDir string) (root, gitDir string, err error) {
	out, err := gitCommand(ctx, workDir, "rev-parse", "--show-toplevel", "--git-dir")
	if err != nil {
		return "", "", err
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 2 {
		return "", "", errors.New("missing git rev-parse output")
	}
	root = strings.TrimSpace(lines[0])
	gitDir = strings.TrimSpace(lines[1])
	if root == "" || gitDir == "" {
		return "", "", errors.New("empty git rev-parse output")
	}
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(workDir, gitDir)
	}
	gitDir, _ = filepath.Abs(gitDir)
	return root, gitDir, nil
}

func gitCommand(ctx context.Context, workDir string, args ...string) (string, error) {
	cmdArgs := append([]string{"-C", workDir}, args...)
	cmd := exec.CommandContext(ctx, "git", cmdArgs...)
	out, err := cmd.Output()
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func countGitStashEntries(out string) int {
	count := 0
	for line := range strings.SplitSeq(out, "\n") {
		if strings.TrimSpace(line) != "" {
			count++
		}
	}
	return count
}

func parseGitStatusPorcelainV2(out string) gitStatusInfo {
	var info gitStatusInfo
	for line := range strings.SplitSeq(out, "\n") {
		line = strings.TrimRight(line, "\r")
		switch {
		case strings.HasPrefix(line, "# branch.head "):
			head := strings.TrimSpace(strings.TrimPrefix(line, "# branch.head "))
			if head != "(detached)" {
				info.Branch = head
			}
		case strings.HasPrefix(line, "# branch.oid "):
			oid := strings.TrimSpace(strings.TrimPrefix(line, "# branch.oid "))
			if len(oid) > 12 {
				oid = oid[:12]
			}
			info.Commit = oid
		case strings.HasPrefix(line, "# branch.ab "):
			fields := strings.Fields(strings.TrimPrefix(line, "# branch.ab "))
			if len(fields) >= 2 {
				info.Ahead = parseGitAB(fields[0], '+')
				info.Behind = parseGitAB(fields[1], '-')
			}
		case strings.HasPrefix(line, "1 "), strings.HasPrefix(line, "2 "):
			info.ChangedFiles++
			fields := strings.Fields(line)
			if len(fields) >= 2 && len(fields[1]) >= 1 && fields[1][0] != '.' {
				info.StagedFiles++
			}
		case strings.HasPrefix(line, "u "):
			info.ChangedFiles++
			info.StagedFiles++
		case strings.HasPrefix(line, "? "):
			info.ChangedFiles++
		}
	}
	return info
}

func parseGitAB(field string, prefix byte) int {
	if len(field) < 2 || field[0] != prefix {
		return 0
	}
	n, err := strconv.Atoi(field[1:])
	if err != nil || n < 0 {
		return 0
	}
	return n
}

func linkedWorktreeName(root, gitDir string) string {
	if gitDir == "" {
		return ""
	}
	parts := strings.Split(filepath.ToSlash(filepath.Clean(gitDir)), "/")
	for i := 0; i < len(parts)-1; i++ {
		if parts[i] == "worktrees" && parts[i+1] != "" {
			return parts[i+1]
		}
	}
	gitPath := filepath.Join(root, ".git")
	if content, err := os.ReadFile(gitPath); err == nil {
		line := strings.TrimSpace(strings.Split(string(content), "\n")[0])
		const prefix = "gitdir: "
		if strings.HasPrefix(line, prefix) {
			gitDirRef := strings.TrimSpace(line[len(prefix):])
			parts := strings.Split(filepath.ToSlash(filepath.Clean(gitDirRef)), "/")
			for i := 0; i < len(parts)-1; i++ {
				if parts[i] == "worktrees" && parts[i+1] != "" {
					return parts[i+1]
				}
			}
		}
	}
	return ""
}

// gitStatusRef returns the displayed repository ref: the branch with a
// linked-worktree suffix, or the short commit / "detached" when not on a branch.
func gitStatusRef(info gitStatusInfo) string {
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
// summary. The slice keeps the canonical display order (sync, staged, changed,
// stash); dropWeight ranks markers so the least useful one can be dropped first
// when the counts would leave no room for a recognizable ref — the changed-file
// count is the most actionable and survives longest, stash counts go first.
type gitStatusStatusPart struct {
	text       string
	dropWeight int
}

func gitStatusStatusParts(info gitStatusInfo) []gitStatusStatusPart {
	var parts []gitStatusStatusPart
	if info.Ahead > 0 {
		parts = append(parts, gitStatusStatusPart{text: fmt.Sprintf("↑%d", info.Ahead), dropWeight: 2})
	}
	if info.Behind > 0 {
		parts = append(parts, gitStatusStatusPart{text: fmt.Sprintf("↓%d", info.Behind), dropWeight: 3})
	}
	if info.StagedFiles > 0 {
		parts = append(parts, gitStatusStatusPart{text: fmt.Sprintf("+%d", info.StagedFiles), dropWeight: 1})
	}
	if info.ChangedFiles > 0 {
		parts = append(parts, gitStatusStatusPart{text: fmt.Sprintf("!%d", info.ChangedFiles), dropWeight: 0})
	}
	if info.Stashes > 0 {
		parts = append(parts, gitStatusStatusPart{text: fmt.Sprintf("*%d", info.Stashes), dropWeight: 4})
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
func gitStatusSummaryBudgeted(info gitStatusInfo, maxW int) string {
	if maxW < 1 {
		return ""
	}
	ref := gitStatusRef(info)
	parts := gitStatusStatusParts(info)
	if len(parts) == 0 {
		return truncateOneLine(ref, maxW)
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
		ref = truncateOneLine(ref, refW)
	}
	return ref + " " + joined
}
