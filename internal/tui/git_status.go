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

	tea "charm.land/bubbletea/v2"
)

const (
	gitStatusForegroundInitialInterval = 15 * time.Second
	gitStatusForegroundMaxInterval     = time.Minute
	gitStatusBackgroundInitialInterval = time.Minute
	gitStatusBackgroundMaxInterval     = 5 * time.Minute
	gitStatusCommandTimeout            = time.Second
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
	Present      bool
	Branch       string
	Commit       string
	WorktreeName string
	ChangedFiles int
	Stashes      int
	Ahead        int
	Behind       int
	CapturedAt   time.Time
}

type gitStatusRefreshedMsg struct {
	generation uint64
	result     gitStatusResult
}

type gitStatusTickMsg struct {
	generation uint64
}

func (m *Model) requestGitStatusRefresh() tea.Cmd {
	if m == nil || m.agent == nil || m.gitStatus.Refreshing || m.gitStatus.Disabled {
		return nil
	}
	workDir := strings.TrimSpace(m.workingDir)
	if m.agent != nil {
		if root := strings.TrimSpace(m.agent.ProjectRoot()); root != "" {
			workDir = root
		}
	}
	if workDir == "" {
		return nil
	}
	m.gitStatus.Refreshing = true
	m.gitStatus.Generation++
	generation := m.gitStatus.Generation
	return func() tea.Msg {
		return gitStatusRefreshedMsg{generation: generation, result: collectGitStatus(workDir)}
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
	next := m.gitStatus.NextDelay * 2
	if next > maxDelay {
		next = maxDelay
	}
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
	return tea.Tick(delay, func(time.Time) tea.Msg {
		return gitStatusTickMsg{generation: generation}
	})
}

func gitStatusInfoEqual(a, b gitStatusInfo) bool {
	return a.Present == b.Present &&
		a.Branch == b.Branch &&
		a.Commit == b.Commit &&
		a.WorktreeName == b.WorktreeName &&
		a.ChangedFiles == b.ChangedFiles &&
		a.Stashes == b.Stashes &&
		a.Ahead == b.Ahead &&
		a.Behind == b.Behind
}

func (m *Model) handleGitStatusRefreshed(msg gitStatusRefreshedMsg) tea.Cmd {
	if msg.generation != m.gitStatus.Generation {
		return nil
	}
	previous := m.gitStatus.Info
	m.gitStatus.Refreshing = false
	if msg.result.Disable {
		m.gitStatus.Disabled = true
		m.gitStatus.Info = gitStatusInfo{}
		m.cachedInfoPanelFP = ""
		m.cachedInfoPanelOut = ""
		return nil
	}
	m.gitStatus.Info = msg.result.Info
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
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) != "" {
			count++
		}
	}
	return count
}

func parseGitStatusPorcelainV2(out string) gitStatusInfo {
	var info gitStatusInfo
	for _, line := range strings.Split(out, "\n") {
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
		case strings.HasPrefix(line, "1 "), strings.HasPrefix(line, "2 "), strings.HasPrefix(line, "u "), strings.HasPrefix(line, "? "):
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

func gitStatusSummary(info gitStatusInfo) string {
	if !info.Present {
		return ""
	}
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
	parts := []string{ref}
	if info.Ahead > 0 {
		parts = append(parts, fmt.Sprintf("↑%d", info.Ahead))
	}
	if info.Behind > 0 {
		parts = append(parts, fmt.Sprintf("↓%d", info.Behind))
	}
	if info.ChangedFiles > 0 {
		parts = append(parts, fmt.Sprintf("!%d", info.ChangedFiles))
	}
	if info.Stashes > 0 {
		parts = append(parts, fmt.Sprintf("*%d", info.Stashes))
	}
	return strings.Join(parts, " ")
}
