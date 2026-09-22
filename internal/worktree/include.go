package worktree

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/keakon/chord/internal/pathutil"
)

// WorktreeIncludeFile is the repository-root file that lists, in gitignore
// syntax, the gitignored files chord copies into every new worktree — the
// local configuration and secrets a fresh checkout never contains.
const WorktreeIncludeFile = ".worktreeinclude"

// defaultWorktreeIncludePatterns applies when the repository has no
// .worktreeinclude file: the environment files most local setups keep out of
// git.
var defaultWorktreeIncludePatterns = []string{".env*"}

// ensureWorktreeRootGitignore writes a self-ignoring `.gitignore` into root so
// chord-created worktrees stay out of the main checkout's `git status`. It
// applies only when root lies strictly inside mainRoot (the state-dir layout is
// outside the repository and needs nothing) and never overwrites a `.gitignore`
// the user already placed there. The file only keeps the status clean; it is
// not a protection mechanism.
func ensureWorktreeRootGitignore(mainRoot, root string) error {
	rel, ok := pathutil.RelToBase(root, mainRoot)
	if !ok || rel == "." {
		return nil
	}
	gitignorePath := filepath.Join(root, ".gitignore")
	if _, err := os.Lstat(gitignorePath); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat worktree root .gitignore: %w", err)
	}
	if err := os.WriteFile(gitignorePath, []byte("*\n"), 0o644); err != nil {
		return fmt.Errorf("write worktree root .gitignore: %w", err)
	}
	return nil
}

// copyWorktreeIncludeFiles copies the gitignored files matched by the
// repository's .worktreeinclude (default: `.env*`) from mainRoot into a fresh
// worktree, preserving relative paths. Only files git already ignores are
// copied, and files tracked in the worktree are never overwritten. The patterns
// are matched by git itself, so exact gitignore semantics apply.
//
// Best-effort: individual copy failures are reported as one error after the
// remaining candidates are tried.
func copyWorktreeIncludeFiles(ctx context.Context, mainRoot, worktreePath string) error {
	patterns, err := readWorktreeInclude(mainRoot)
	if err != nil {
		return err
	}
	candidates, err := listUntrackedMatching(ctx, mainRoot, patterns)
	if err != nil {
		return err
	}
	if len(candidates) == 0 {
		return nil
	}
	included, err := filterIgnored(ctx, mainRoot, candidates)
	if err != nil {
		return err
	}
	if len(included) == 0 {
		return nil
	}
	tracked, err := listTrackedFiles(ctx, worktreePath)
	if err != nil {
		return err
	}
	var firstErr error
	for _, rel := range included {
		if _, ok := tracked[rel]; ok {
			continue
		}
		if err := copyIncludeFile(mainRoot, worktreePath, rel); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("copy %s: %w", rel, err)
		}
	}
	return firstErr
}

// readWorktreeInclude returns the patterns to copy: the lines of
// .worktreeinclude when it exists and lists at least one pattern, the default
// `.env*` otherwise.
func readWorktreeInclude(mainRoot string) ([]string, error) {
	data, err := os.ReadFile(filepath.Join(mainRoot, WorktreeIncludeFile))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return defaultWorktreeIncludePatterns, nil
		}
		return nil, fmt.Errorf("read %s: %w", WorktreeIncludeFile, err)
	}
	patterns := parseIncludePatterns(string(data))
	if len(patterns) == 0 {
		return defaultWorktreeIncludePatterns, nil
	}
	return patterns, nil
}

// parseIncludePatterns reads one pattern per non-empty, non-comment line,
// following gitignore file syntax.
func parseIncludePatterns(content string) []string {
	var patterns []string
	for line := range strings.SplitSeq(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		patterns = append(patterns, line)
	}
	return patterns
}

// listUntrackedMatching returns the untracked files in dir whose paths match
// patterns (gitignore syntax), as slash-separated paths relative to dir. The
// patterns are handed to git through a temporary exclude file, so ignore
// matching is never reimplemented here.
func listUntrackedMatching(ctx context.Context, dir string, patterns []string) ([]string, error) {
	patternFile, err := writeIncludePatternsFile(patterns)
	if err != nil {
		return nil, err
	}
	defer os.Remove(patternFile)
	out, err := runGit(ctx, dir, "ls-files", "-z", "--others", "--ignored", "--exclude-from="+patternFile)
	if err != nil {
		return nil, err
	}
	return splitNUL(out), nil
}

// filterIgnored returns the subset of paths that git's own ignore rules
// (repository and global excludes) mark as ignored. Candidates are checked in
// batches so a broad .worktreeinclude cannot overflow the argument list; the
// :(literal) pathspec magic keeps file names with glob characters exact.
func filterIgnored(ctx context.Context, dir string, paths []string) ([]string, error) {
	const batchSize = 128
	var ignored []string
	for start := 0; start < len(paths); start += batchSize {
		end := min(start+batchSize, len(paths))
		args := make([]string, 0, 6+end-start)
		args = append(args, "ls-files", "-z", "--others", "--ignored", "--exclude-standard", "--")
		for _, p := range paths[start:end] {
			args = append(args, ":(literal)"+p)
		}
		out, err := runGit(ctx, dir, args...)
		if err != nil {
			return nil, err
		}
		ignored = append(ignored, splitNUL(out)...)
	}
	return ignored, nil
}

// listTrackedFiles returns the files tracked in dir, as slash-separated paths
// relative to dir.
func listTrackedFiles(ctx context.Context, dir string) (map[string]struct{}, error) {
	out, err := runGit(ctx, dir, "ls-files", "-z")
	if err != nil {
		return nil, err
	}
	files := make(map[string]struct{})
	for _, rel := range splitNUL(out) {
		files[rel] = struct{}{}
	}
	return files, nil
}

// writeIncludePatternsFile writes patterns to a temporary gitignore-syntax file
// and returns its path; the caller removes it.
func writeIncludePatternsFile(patterns []string) (string, error) {
	f, err := os.CreateTemp("", "chord-worktreeinclude-*")
	if err != nil {
		return "", fmt.Errorf("create .worktreeinclude pattern file: %w", err)
	}
	path := f.Name()
	if _, err := f.WriteString(strings.Join(patterns, "\n") + "\n"); err != nil {
		f.Close()
		os.Remove(path)
		return "", fmt.Errorf("write .worktreeinclude pattern file: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(path)
		return "", fmt.Errorf("close .worktreeinclude pattern file: %w", err)
	}
	return path, nil
}

// copyIncludeFile copies one included file, keeping its relative directory
// layout. Only regular files are copied; symlinks and special files are
// skipped rather than replicated.
func copyIncludeFile(mainRoot, worktreePath, rel string) error {
	src := filepath.Join(mainRoot, filepath.FromSlash(rel))
	info, err := os.Lstat(src)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if !info.Mode().IsRegular() {
		return nil
	}
	dst := filepath.Join(worktreePath, filepath.FromSlash(rel))
	if _, err := os.Lstat(dst); err == nil {
		// The worktree already carries this path; never clobber it.
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return copyRegularFile(src, dst, info.Mode().Perm())
}

func copyRegularFile(src, dst string, perm os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(dst)
		return err
	}
	return out.Close()
}

// splitNUL splits NUL-terminated git output into non-empty entries.
func splitNUL(out []byte) []string {
	parts := strings.Split(string(out), "\x00")
	items := parts[:0]
	for _, p := range parts {
		if p != "" {
			items = append(items, p)
		}
	}
	return items
}
