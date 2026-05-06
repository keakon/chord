package worktree

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// gitNoPromptEnv keeps git from opening /dev/tty or showing a credential
// dialog. Headless stdin is the JSON protocol channel; if git ever read
// from it, the gateway would lose bytes irrecoverably.
var gitNoPromptEnv = []string{
	"GIT_TERMINAL_PROMPT=0",
	"GIT_ASKPASS=",
	"GCM_INTERACTIVE=Never",
}

// runGit executes git in cwd with stdin closed and credential prompts
// disabled. stdoutBytes is the raw stdout; stderr is folded into the
// returned error on non-zero exit.
func runGit(ctx context.Context, cwd string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	if cwd != "" {
		cmd.Dir = cwd
	}
	cmd.Env = append(os.Environ(), gitNoPromptEnv...)
	cmd.Stdin = nil
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
			msg := strings.TrimSpace(stderr.String())
			if msg == "" {
				msg = strings.TrimSpace(stdout.String())
			}
			if msg == "" {
				msg = exitErr.Error()
			}
			return stdout.Bytes(), fmt.Errorf("git %s: %s", strings.Join(args, " "), msg)
		}
		return stdout.Bytes(), fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return stdout.Bytes(), nil
}

// runGitText is runGit with the stdout already trimmed and converted to a
// string; convenient for one-line outputs like rev-parse.
func runGitText(ctx context.Context, cwd string, args ...string) (string, error) {
	out, err := runGit(ctx, cwd, args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// porcelainEntry represents one worktree as reported by
// `git worktree list --porcelain`. Branch is the full ref (e.g.
// "refs/heads/chord/foo") when present; Detached and Bare are mutually
// exclusive flags emitted by porcelain output.
type porcelainEntry struct {
	Path     string
	Head     string
	Branch   string
	Detached bool
	Bare     bool
	Locked   string
}

// parseWorktreeListPorcelain parses the porcelain output of
// `git worktree list --porcelain`.
//
// Format (one record per worktree, blank line separated):
//
//	worktree /abs/path
//	HEAD <sha>
//	branch refs/heads/<name>      (or "detached" or "bare")
//	locked <reason>?
func parseWorktreeListPorcelain(out []byte) []porcelainEntry {
	var entries []porcelainEntry
	var cur *porcelainEntry
	flush := func() {
		if cur != nil && cur.Path != "" {
			entries = append(entries, *cur)
		}
		cur = nil
	}
	for line := range strings.SplitSeq(string(out), "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			flush()
			continue
		}
		if strings.HasPrefix(line, "worktree ") {
			flush()
			cur = &porcelainEntry{Path: strings.TrimPrefix(line, "worktree ")}
			continue
		}
		if cur == nil {
			continue
		}
		switch {
		case strings.HasPrefix(line, "HEAD "):
			cur.Head = strings.TrimPrefix(line, "HEAD ")
		case strings.HasPrefix(line, "branch "):
			cur.Branch = strings.TrimPrefix(line, "branch ")
		case line == "detached":
			cur.Detached = true
		case line == "bare":
			cur.Bare = true
		case strings.HasPrefix(line, "locked"):
			// "locked" or "locked <reason>"
			cur.Locked = strings.TrimSpace(strings.TrimPrefix(line, "locked"))
			if cur.Locked == "" {
				cur.Locked = "locked"
			}
		}
	}
	flush()
	return entries
}

// shortBranch strips a leading "refs/heads/" from the porcelain branch
// field. Returns the input unchanged when the prefix is absent.
func shortBranch(ref string) string {
	return strings.TrimPrefix(ref, "refs/heads/")
}
