package worktree

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/keakon/chord/internal/config"
)

// BranchPrefix is the marker that identifies a chord-managed branch in
// `git worktree list --porcelain` output.
const BranchPrefix = "chord/"

// Info describes one chord-managed worktree on disk and in the repo
// index. The same struct is returned by Create, List, and ResolveByName
// so callers can route a single value through any of those entry points.
type Info struct {
	Slug       string // bare slug (no chord/ prefix)
	Name       string // user-facing name; equal to Slug for v1
	Branch     string // full branch including chord/ prefix
	Path       string // canonical worktree root
	RepoRoot   string // canonical main repo root
	RepoID     string // RepoIDFor(RepoRoot)
	BaseSHA    string // commit the worktree HEAD pointed at after create
	HEADBranch string // base branch HEAD was on (best-effort, may be "" when detached)
	Existed    bool   // true on fast-resume of an existing worktree
	MainDirty  bool   // true when the main repo had uncommitted changes at create time
}

// CreateOptions controls Create. RepoRoot may be a sub-directory of the
// main repo or any linked worktree of it; Create resolves to the main
// repo root before doing anything destructive.
type CreateOptions struct {
	// Name is the user-supplied worktree name. ValidateSlug must accept
	// it. Empty is rejected here; Create's caller should auto-generate.
	Name string
	// RepoRoot is the directory used to discover the main repo; can be
	// the cwd. Worktrees are created from the main repo, never from a
	// linked worktree (Create rejects that case).
	RepoRoot string
	// PathLocator supplies the global state directory under which
	// <stateDir>/worktrees/<repoID>/<slug> lives.
	PathLocator *config.PathLocator
}

// RemoveOptions controls Remove. By default Remove protects the worktree
// branch (commits may exist only there) and refuses dirty trees.
type RemoveOptions struct {
	// Force removes the worktree even when the working tree is dirty
	// and force-deletes the branch (`git branch -D`). Has no effect on
	// the cwd-self-removal guard.
	Force bool
	// DeleteBranch removes the chord/<slug> branch using `git branch -d`
	// (refused unless merged). Implied by Force.
	DeleteBranch bool
}

// Create either creates a new git worktree at
// <stateDir>/worktrees/<repoID>/<slug> based on HEAD or fast-resumes an
// existing chord-managed worktree with the same branch name. Returns
// Info describing the worktree on success.
func Create(ctx context.Context, opts CreateOptions) (*Info, error) {
	if err := ValidateSlug(opts.Name); err != nil {
		return nil, err
	}
	if opts.PathLocator == nil {
		return nil, fmt.Errorf("create worktree: nil PathLocator")
	}
	rootIn := opts.RepoRoot
	if rootIn == "" {
		var err error
		rootIn, err = os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("create worktree: cwd: %w", err)
		}
	}
	mainRoot, err := GitMainRoot(ctx, rootIn)
	if err != nil {
		return nil, err
	}
	inLinked, err := IsInsideLinkedWorktree(ctx, rootIn)
	if err != nil {
		return nil, err
	}
	if inLinked {
		return nil, fmt.Errorf("nested worktree creation refused: %s is inside a linked worktree; create from the main repo", rootIn)
	}
	repoID := RepoIDFor(mainRoot)
	branch := BranchPrefix + opts.Name
	wantPath := filepath.Join(opts.PathLocator.StateDir, "worktrees", repoID, opts.Name)

	// Fast-resume: branch already registered to a worktree.
	listOut, err := runGit(ctx, mainRoot, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	for _, e := range parseWorktreeListPorcelain(listOut) {
		if shortBranch(e.Branch) == branch {
			path, _ := canonicalDir(e.Path)
			head, _ := runGitText(ctx, path, "rev-parse", "HEAD")
			return &Info{
				Slug:     opts.Name,
				Name:     opts.Name,
				Branch:   branch,
				Path:     path,
				RepoRoot: mainRoot,
				RepoID:   repoID,
				BaseSHA:  head,
				Existed:  true,
			}, nil
		}
	}

	if err := guardWorktreePath(wantPath); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(wantPath), 0o755); err != nil {
		return nil, fmt.Errorf("mkdir worktree parent: %w", err)
	}
	mainDirty, _ := IsDirty(ctx, mainRoot)
	// -B (rather than -b) silently resets a leftover branch to HEAD when
	// a previous worktree dir was removed but the branch was kept. This
	// matches user expectation "I asked for a fresh chord/<slug>".
	if _, err := runGit(ctx, mainRoot, "worktree", "add", "-B", branch, wantPath, "HEAD"); err != nil {
		return nil, err
	}
	canonical, err := canonicalDir(wantPath)
	if err != nil {
		canonical = wantPath
	}
	headSHA, _ := runGitText(ctx, canonical, "rev-parse", "HEAD")
	headBranch, _ := runGitText(ctx, mainRoot, "rev-parse", "--abbrev-ref", "HEAD")
	return &Info{
		Slug:       opts.Name,
		Name:       opts.Name,
		Branch:     branch,
		Path:       canonical,
		RepoRoot:   mainRoot,
		RepoID:     repoID,
		BaseSHA:    headSHA,
		HEADBranch: headBranch,
		Existed:    false,
		MainDirty:  mainDirty,
	}, nil
}

// guardWorktreePath fails when the target path already exists but is
// not a chord-managed worktree (residue from a manual rm -rf, etc.).
// `git worktree add` will refuse to overwrite, but we want a clearer
// error before invoking git.
func guardWorktreePath(path string) error {
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("worktree target path already exists: %s", path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat worktree target: %w", err)
	}
	return nil
}

// List returns chord-managed worktrees discovered via
// `git worktree list --porcelain`, filtered by branch prefix `chord/`.
// It does NOT consult the repo index; callers wanting LastUsedAt etc.
// should merge with LoadRepoIndex separately.
func List(ctx context.Context, repoRoot string) ([]Info, error) {
	mainRoot, err := GitMainRoot(ctx, repoRoot)
	if err != nil {
		return nil, err
	}
	out, err := runGit(ctx, mainRoot, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	repoID := RepoIDFor(mainRoot)
	var infos []Info
	for _, e := range parseWorktreeListPorcelain(out) {
		br := shortBranch(e.Branch)
		if !strings.HasPrefix(br, BranchPrefix) {
			continue
		}
		slug := strings.TrimPrefix(br, BranchPrefix)
		path, _ := canonicalDir(e.Path)
		infos = append(infos, Info{
			Slug:     slug,
			Name:     slug,
			Branch:   br,
			Path:     path,
			RepoRoot: mainRoot,
			RepoID:   repoID,
			BaseSHA:  e.Head,
		})
	}
	return infos, nil
}

// ResolveByName returns the worktree with the given name (= slug in v1),
// or an error when not found.
func ResolveByName(ctx context.Context, repoRoot, name string) (*Info, error) {
	if err := ValidateSlug(name); err != nil {
		return nil, err
	}
	infos, err := List(ctx, repoRoot)
	if err != nil {
		return nil, err
	}
	for i := range infos {
		if infos[i].Name == name {
			return &infos[i], nil
		}
	}
	return nil, fmt.Errorf("worktree %q not found", name)
}

// Remove deletes a chord-managed worktree, its index entry, and the
// associated chord per-project state (sessions/cache/exports/registry).
// Branch is preserved unless DeleteBranch or Force is set.
//
// pathLocator is required to compute and purge the worktree's
// ProjectKey-scoped state. Passing nil is an error.
func Remove(ctx context.Context, repoRoot, name string, opts RemoveOptions, pathLocator *config.PathLocator) error {
	if err := ValidateSlug(name); err != nil {
		return err
	}
	if pathLocator == nil {
		return fmt.Errorf("remove worktree: nil PathLocator")
	}
	info, err := ResolveByName(ctx, repoRoot, name)
	if err != nil {
		return err
	}
	cwd, _ := os.Getwd()
	if cwdMatch, _ := canonicalDir(cwd); cwdMatch != "" && cwdMatch == info.Path {
		return fmt.Errorf("refusing to remove worktree %q: it is the current working directory", name)
	}
	if !opts.Force {
		statusOut, err := runGit(ctx, info.Path, "status", "--porcelain")
		if err != nil {
			return err
		}
		if len(strings.TrimSpace(string(statusOut))) > 0 {
			return fmt.Errorf("worktree %q has uncommitted changes; pass --force to remove anyway", name)
		}
	}
	gitArgs := []string{"worktree", "remove"}
	if opts.Force {
		gitArgs = append(gitArgs, "--force")
	}
	gitArgs = append(gitArgs, info.Path)
	if _, err := runGit(ctx, info.RepoRoot, gitArgs...); err != nil {
		return err
	}
	if opts.Force {
		// `branch -D` succeeds even when unmerged; matches Force semantics.
		if _, berr := runGit(ctx, info.RepoRoot, "branch", "-D", info.Branch); berr != nil {
			// Non-fatal: worktree itself is already gone.
			fmt.Fprintf(os.Stderr, "warning: %v\n", berr)
		}
	} else if opts.DeleteBranch {
		if _, berr := runGit(ctx, info.RepoRoot, "branch", "-d", info.Branch); berr != nil {
			return fmt.Errorf("delete branch %s (use --force to override): %w", info.Branch, berr)
		}
	}
	if err := purgeWorktreeProjectState(info.Path, pathLocator); err != nil {
		fmt.Fprintf(os.Stderr, "warning: cleanup project state: %v\n", err)
	}
	if err := WithRepoIndexLock(pathLocator.StateDir, info.RepoID, func(idx *RepoIndex) error {
		idx.RemoveWorktree(name)
		return nil
	}); err != nil {
		return fmt.Errorf("update repo index: %w", err)
	}
	return nil
}

// purgeWorktreeProjectState deletes the per-project state that chord
// generated for the worktree: sessions, runtime cache, exports, and the
// registry metadata file.
func purgeWorktreeProjectState(worktreePath string, pl *config.PathLocator) error {
	pj, err := pl.LocateProject(worktreePath)
	if err != nil {
		return err
	}
	var firstErr error
	for _, p := range []string{pj.ProjectSessionsDir, pj.RuntimeCacheDir, pj.ProjectExportsDir} {
		if p == "" {
			continue
		}
		if err := os.RemoveAll(p); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if pj.RegistryMetaPath != "" {
		if err := os.Remove(pj.RegistryMetaPath); err != nil && !errors.Is(err, os.ErrNotExist) && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// GitMainRoot resolves the main repository root for dir, even when dir
// is inside a linked worktree. Uses `--git-common-dir` to find the
// shared ".git" then walks one level up.
func GitMainRoot(ctx context.Context, dir string) (string, error) {
	if dir == "" {
		var err error
		dir, err = os.Getwd()
		if err != nil {
			return "", fmt.Errorf("git main root: cwd: %w", err)
		}
	}
	common, err := runGitText(ctx, dir, "rev-parse", "--git-common-dir")
	if err != nil {
		return "", fmt.Errorf("not a git repository: %s", dir)
	}
	commonAbs, err := absClean(common, dir)
	if err != nil {
		return "", err
	}
	mainRoot := filepath.Dir(commonAbs)
	canonical, err := canonicalDir(mainRoot)
	if err != nil {
		return mainRoot, nil
	}
	return canonical, nil
}

// IsInsideLinkedWorktree reports whether dir is inside a linked
// worktree (i.e. `--git-dir` and `--git-common-dir` resolve to different
// paths after canonicalization). Avoids the well-known footgun where
// the two outputs differ in absolute-vs-relative form depending on cwd.
func IsInsideLinkedWorktree(ctx context.Context, dir string) (bool, error) {
	if dir == "" {
		var err error
		dir, err = os.Getwd()
		if err != nil {
			return false, fmt.Errorf("linked worktree check: cwd: %w", err)
		}
	}
	gitDir, err := runGitText(ctx, dir, "rev-parse", "--git-dir")
	if err != nil {
		return false, nil
	}
	commonDir, err := runGitText(ctx, dir, "rev-parse", "--git-common-dir")
	if err != nil {
		return false, nil
	}
	gitDirAbs, err := absClean(gitDir, dir)
	if err != nil {
		return false, err
	}
	commonAbs, err := absClean(commonDir, dir)
	if err != nil {
		return false, err
	}
	return gitDirAbs != commonAbs, nil
}

// absClean returns filepath.Clean(filepath.Abs(p)) resolved against
// baseDir when p is relative, with EvalSymlinks applied best-effort. Used
// to compare git-reported paths that may be relative or absolute
// depending on whether cwd is the repo root.
func absClean(p, baseDir string) (string, error) {
	if p == "" {
		return "", fmt.Errorf("empty path")
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(baseDir, p)
	}
	clean := filepath.Clean(p)
	if real, err := filepath.EvalSymlinks(clean); err == nil {
		clean = real
	}
	return clean, nil
}

// canonicalDir returns CanonicalProjectRoot(p). Wrapped so call sites
// don't pull in the config package directly.
func canonicalDir(p string) (string, error) {
	return config.CanonicalProjectRoot(p)
}

// FormatRelativeTime renders a coarse human-readable age, used by the
// `worktree list` table. Returns "-" when t is zero.
func FormatRelativeTime(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

// IsDirty reports whether path's working tree has uncommitted changes.
// Returns (false, false) when status could not be probed (e.g. path
// missing); the second return distinguishes that case from a clean tree.
func IsDirty(ctx context.Context, path string) (dirty, ok bool) {
	out, err := runGit(ctx, path, "status", "--porcelain")
	if err != nil {
		return false, false
	}
	return len(strings.TrimSpace(string(out))) > 0, true
}
