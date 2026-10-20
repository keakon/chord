// Package gitinspect observes checkout metadata and indexed paths through Git.
package gitinspect

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const commandTimeout = time.Second

// Result contains a Git snapshot or an indication that Git is unavailable.
type Result struct {
	Info    Info
	Disable bool
}

// Info describes one checkout observation. Dir is assigned by the publisher
// after matching the result to the live checkout generation.
type Info struct {
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
	// ConflictedFiles counts unmerged paths. Git reports them in their own
	// section, so they belong to neither the staged nor the changed bucket.
	ConflictedFiles int
	Stashes         int
	Ahead           int
	Behind          int
	CapturedAt      time.Time
}

// Collect observes status, stashes and linked-worktree identity within one timeout.
// A directory without a Git marker returns an empty result without starting Git.
func Collect(workDir string) Result {
	if !hasGitMarker(workDir) {
		return Result{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()

	root, gitDir, err := gitRevParse(ctx, workDir)
	if err != nil || root == "" {
		return Result{Disable: isGitExecutableMissing(err)}
	}
	out, err := gitCommand(ctx, workDir, "status", "--porcelain=v2", "--branch", "--untracked-files=normal")
	if err != nil {
		return Result{Disable: isGitExecutableMissing(err)}
	}
	info := parseGitStatusPorcelainV2(out)
	stashOut, err := gitCommand(ctx, workDir, "stash", "list", "--format=%gd")
	if err != nil {
		return Result{Disable: isGitExecutableMissing(err)}
	}
	info.Present = true
	info.Stashes = countGitStashEntries(stashOut)
	info.WorktreeName = linkedWorktreeName(root, gitDir)
	info.CapturedAt = time.Now()
	return Result{Info: info}
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

func parseGitStatusPorcelainV2(out string) Info {
	var info Info
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
			// X describes the index against HEAD and Y the worktree against the
			// index, so both halves can be non-empty for the same path. Staged
			// and changed are disjoint buckets: a path with any staged content
			// counts once as staged, otherwise once as changed. Otherwise a
			// partially staged path would inflate both numbers.
			fields := strings.Fields(line)
			if len(fields) < 2 {
				continue
			}
			if fields[1][0] != '.' {
				info.StagedFiles++
			} else {
				info.ChangedFiles++
			}
		case strings.HasPrefix(line, "u "):
			info.ConflictedFiles++
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
