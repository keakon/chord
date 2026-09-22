package worktree

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/config"
)

// DefaultBranchPrefix is the prefix used for chord-managed branches when
// the caller does not override it (`<prefix><slug>` is the branch name).
// `git worktree list --porcelain` is filtered by this prefix to identify
// chord-managed worktrees.
const DefaultBranchPrefix = "chord/"

// ErrNotGitRepository reports that a path is not inside a git repository.
var ErrNotGitRepository = errors.New("not a git repository")

// NormalizeBranchPrefix returns a usable branch prefix for the worktree
// machinery: empty input falls back to DefaultBranchPrefix; otherwise
// the input is trimmed, validated for git-branch safety, and a trailing
// "/" is appended when missing. The returned string always ends with
// "/" so callers can concatenate `prefix + slug` directly.
func NormalizeBranchPrefix(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return DefaultBranchPrefix, nil
	}
	// Reject characters that git itself disallows in ref names. We don't
	// try to be exhaustive (git's own check is the source of truth at
	// `worktree add` time); we just guard against the most common typos
	// that would silently bypass the prefix filter or shell out badly.
	if strings.HasPrefix(s, "/") || strings.HasPrefix(s, "-") {
		return "", fmt.Errorf("worktree branch_prefix %q must not start with %q", s, string(s[0]))
	}
	if strings.Contains(s, "..") || strings.Contains(s, "//") {
		return "", fmt.Errorf("worktree branch_prefix %q contains forbidden sequence", s)
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z',
			c >= 'A' && c <= 'Z',
			c >= '0' && c <= '9',
			c == '.' || c == '_' || c == '-' || c == '/':
			// allowed
		default:
			return "", fmt.Errorf("worktree branch_prefix %q contains forbidden character %q", s, string(c))
		}
	}
	if !strings.HasSuffix(s, "/") {
		s += "/"
	}
	return s, nil
}

// effectiveBranchPrefix is the package-internal counterpart used by the
// Create/List/Remove paths: it accepts a possibly-empty caller-supplied
// prefix and falls back to DefaultBranchPrefix without re-validating
// (validation belongs to NormalizeBranchPrefix at the cmd-layer entry
// point so configuration errors surface at startup).
func effectiveBranchPrefix(s string) string {
	if s == "" {
		return DefaultBranchPrefix
	}
	if !strings.HasSuffix(s, "/") {
		return s + "/"
	}
	return s
}

// Info describes one chord-managed worktree on disk and in the repo
// index. The same struct is returned by Create, List, and ResolveByName
// so callers can route a single value through any of those entry points.
type Info struct {
	Slug       string // bare slug (no branch-prefix)
	Name       string // user-facing name; equal to Slug for v1
	Branch     string // full branch including branch-prefix (e.g. "chord/<slug>")
	Path       string // canonical worktree root
	RepoRoot   string // canonical main repo root
	RepoID     string // RepoIDFor(RepoRoot)
	BaseSHA    string // commit the worktree HEAD pointed at after create
	HEADBranch string // base branch HEAD was on (best-effort, may be "" when detached)
	Existed    bool   // true on fast-resume of an existing worktree
	MainDirty  bool   // true when the main repo had uncommitted changes at create time
	Owner      *Owner // creator recorded in the worktree's git dir, when known
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
	// BranchPrefix overrides DefaultBranchPrefix when non-empty. The
	// trailing "/" is added automatically. Callers should pass the
	// already-normalized value from NormalizeBranchPrefix when they need
	// validation; raw empty is accepted and falls back to default.
	BranchPrefix string
	// ResetBranch allows resetting an existing branch that no worktree has
	// checked out (left behind by a removed worktree) to HEAD. Without it
	// such a branch is refused, because the branch may hold the only copy
	// of commits from that earlier worktree.
	ResetBranch bool
	// Path overrides the default location. Empty means
	// <stateDir>/worktrees/<repoID>/<slug>. A relative path is resolved
	// against the caller's directory.
	Path string
	// Root overrides where the worktree is created (worktree.root): empty
	// keeps the state-dir layout, a relative path resolves against the main
	// repository root, an absolute path is used as-is. See WorktreeRoot.
	Root string
	// Base is the commit-ish a newly created branch starts at. Empty means
	// the main repository's HEAD. Callers that want the current checkout's
	// HEAD (the Enter tool) pass it explicitly.
	Base string
	// Branch checks out an existing chord-managed branch instead of creating
	// one from Name. Name then defaults to the branch's slug. The branch must
	// already exist, must carry the effective branch prefix, and must not be
	// checked out by another worktree; ResetBranch and Base are rejected.
	Branch string
	// AllowNested permits creating a worktree while the caller is already
	// inside a linked worktree (the Enter tool creates sibling checkouts).
	// The command line leaves this false and refuses nested creation.
	AllowNested bool
	// Owner is written to the new worktree's git directory right after
	// `git worktree add` succeeds. A failed write removes the fresh worktree
	// again: chord never leaves behind a worktree nobody can delete.
	Owner *Owner
}

// HoldersResolver reports live work still anchored to a worktree: another
// agent bound to it, a background command running there, or a session in
// another chord process. It returns human-readable reasons; a non-empty slice
// refuses the removal.
//
// The resolver is called by Remove immediately before the checkout is deleted,
// not earlier, so the window between "nothing holds it" and "it is deleted"
// stays as narrow as the caller can make it. A resolver that cannot determine
// the answer must return a reason rather than an empty slice: an unverifiable
// checkout is treated as in use.
type HoldersResolver func(info *Info) []string

// RemoveOptions controls Remove. By default Remove protects the worktree
// branch (commits may exist only there) and refuses dirty trees.
type RemoveOptions struct {
	// Force removes the worktree even when the working tree is dirty
	// and force-deletes the branch (`git branch -D`). Has no effect on
	// the cwd-self-removal guard.
	Force bool
	// DeleteBranch removes the chord-managed branch using `git branch -d`
	// (refused unless merged). Implied by Force.
	DeleteBranch bool
	// DiscardChanges removes the worktree even when its working tree is
	// dirty, without touching the branch. It is the tool-facing counterpart
	// of Force, which additionally force-deletes the branch.
	DiscardChanges bool
	// BranchPrefix scopes the lookup of the worktree's name to a specific
	// prefix. Empty falls back to DefaultBranchPrefix. Must match the
	// prefix Create used, otherwise the worktree won't be found.
	BranchPrefix string
	// PurgeSessions also deletes the worktree's own session/export store,
	// which only holds sessions created by chord versions that keyed
	// sessions per checkout. Sessions created today live under the
	// repository content root's project key, shared with every checkout, so
	// they are never deleted by removing a worktree.
	PurgeSessions bool
	// Holders reports live work anchored to the worktree. It is consulted
	// last, immediately before the checkout is deleted, so every removal
	// path -- the agent tools and the command line alike -- refuses to pull
	// the directory out from under running work. nil means "no resolver was
	// supplied", which is not the same as "nothing holds it": the caller
	// that cannot answer must pass a resolver that says so.
	Holders HoldersResolver
}

// WorktreeRoot resolves the directory under which chord creates worktrees for
// the repository whose main root is mainRoot. configured is the raw
// worktree.root value: empty keeps the state-dir layout
// (<stateDir>/worktrees/<repoID>), a relative path resolves against mainRoot,
// and an absolute path is used as-is. Create appends the slug to the result.
func WorktreeRoot(pl *config.PathLocator, mainRoot, configured string) (string, error) {
	if pl == nil {
		return "", fmt.Errorf("resolve worktree root: nil PathLocator")
	}
	root := strings.TrimSpace(configured)
	if root == "" {
		return filepath.Join(pl.StateDir, "worktrees", RepoIDFor(mainRoot)), nil
	}
	if !filepath.IsAbs(root) {
		root = filepath.Join(mainRoot, root)
	}
	return filepath.Clean(root), nil
}

// Create either creates a new git worktree at
// <stateDir>/worktrees/<repoID>/<slug> based on HEAD or fast-resumes an
// existing chord-managed worktree with the same branch name. An existing
// branch that no worktree has checked out is refused unless ResetBranch is
// set. Returns Info describing the worktree on success.
func Create(ctx context.Context, opts CreateOptions) (*Info, error) {
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
	if inLinked && !opts.AllowNested {
		return nil, fmt.Errorf("nested worktree creation refused: %s is inside a linked worktree; create from the main repo", rootIn)
	}
	repoID := ResolveRepoID(ctx, rootIn, mainRoot)
	prefix := effectiveBranchPrefix(opts.BranchPrefix)
	name := strings.TrimSpace(opts.Name)
	if name == "" && strings.TrimSpace(opts.Branch) != "" {
		name = strings.TrimPrefix(strings.TrimSpace(opts.Branch), prefix)
	}
	if err := ValidateSlug(name); err != nil {
		return nil, err
	}
	branch := prefix + name
	if b := strings.TrimSpace(opts.Branch); b != "" {
		if opts.ResetBranch {
			return nil, fmt.Errorf("create worktree: branch and reset_branch are mutually exclusive")
		}
		if strings.TrimSpace(opts.Base) != "" {
			return nil, fmt.Errorf("create worktree: branch and base are mutually exclusive")
		}
		if !strings.HasPrefix(b, prefix) {
			return nil, fmt.Errorf("create worktree: branch %s is not chord-managed (expected the %q prefix)", b, prefix)
		}
		branch = b
	}
	worktreeRoot, err := WorktreeRoot(opts.PathLocator, mainRoot, opts.Root)
	if err != nil {
		return nil, err
	}
	wantPath := filepath.Join(worktreeRoot, name)
	if p := strings.TrimSpace(opts.Path); p != "" {
		if !filepath.IsAbs(p) {
			p = filepath.Join(rootIn, p)
		}
		wantPath = filepath.Clean(p)
	}

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
				Name:     name,
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
	// A leftover branch (an earlier worktree was removed but the branch was
	// kept) must not be reset silently: it may hold the only copy of that
	// work's commits. The same guard doubles as the existence check when the
	// caller explicitly asks to check out an existing branch.
	branchExists, err := BranchRefExists(ctx, mainRoot, branch)
	if err != nil {
		return nil, err
	}
	if b := strings.TrimSpace(opts.Branch); b != "" {
		if !branchExists {
			return nil, fmt.Errorf("branch %s does not exist; omit branch to create a new branch from %s", branch, baseRefLabel(opts.Base))
		}
	} else if branchExists && !opts.ResetBranch {
		return nil, fmt.Errorf("branch %s already exists and is not checked out in any worktree; pass --reset-branch to reset it to HEAD, or choose another worktree name", branch)
	}
	if err := os.MkdirAll(filepath.Dir(wantPath), 0o755); err != nil {
		return nil, fmt.Errorf("mkdir worktree parent: %w", err)
	}
	// Keep the container out of `git status` before the checkout exists, so an
	// interrupted create still leaves a clean main checkout behind.
	if err := ensureWorktreeRootGitignore(mainRoot, worktreeRoot); err != nil {
		log.Warnf("write worktree root .gitignore failed root=%v error=%v", worktreeRoot, err)
	}
	mainDirty, _ := IsDirty(ctx, mainRoot)
	var addArgs []string
	if strings.TrimSpace(opts.Branch) != "" {
		addArgs = []string{"worktree", "add", wantPath, branch}
	} else {
		addFlag := "-b"
		if opts.ResetBranch {
			addFlag = "-B"
		}
		baseRef := "HEAD"
		if b := strings.TrimSpace(opts.Base); b != "" {
			baseRef = b
		}
		addArgs = []string{"worktree", "add", addFlag, branch, wantPath, baseRef}
	}
	if _, err := runGit(ctx, mainRoot, addArgs...); err != nil {
		return nil, err
	}
	canonical, err := canonicalDir(wantPath)
	if err != nil {
		canonical = wantPath
	}
	if opts.Owner != nil {
		if err := WriteOwner(ctx, canonical, *opts.Owner); err != nil {
			// Fail closed: a worktree whose owner record cannot be written
			// would be undeletable through the tools, so undo the checkout.
			if _, rmErr := runGit(ctx, mainRoot, "worktree", "remove", "--force", canonical); rmErr != nil {
				return nil, fmt.Errorf("record worktree owner: %w (rollback of the new worktree also failed: %v)", err, rmErr)
			}
			return nil, fmt.Errorf("record worktree owner: %w", err)
		}
	}
	if err := copyWorktreeIncludeFiles(ctx, mainRoot, canonical); err != nil {
		log.Warnf("copy .worktreeinclude files failed worktree=%v error=%v", canonical, err)
	}
	headSHA, _ := runGitText(ctx, canonical, "rev-parse", "HEAD")
	headBranch, _ := runGitText(ctx, mainRoot, "rev-parse", "--abbrev-ref", "HEAD")
	return &Info{
		Slug:       name,
		Name:       name,
		Branch:     branch,
		Path:       canonical,
		RepoRoot:   mainRoot,
		RepoID:     repoID,
		BaseSHA:    headSHA,
		HEADBranch: headBranch,
		Existed:    false,
		MainDirty:  mainDirty,
		Owner:      opts.Owner,
	}, nil
}

// baseRefLabel renders the base ref for error messages.
func baseRefLabel(base string) string {
	if b := strings.TrimSpace(base); b != "" {
		return b
	}
	return "HEAD"
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

// RegisterInIndex inserts or updates the repo index entry for info and stamps
// it as last used. The index is a display cache: session storage is derived
// from the repository root, never from the index, so no project key is
// recorded per worktree.
func RegisterInIndex(pl *config.PathLocator, info *Info) error {
	if pl == nil {
		return fmt.Errorf("register worktree in index: nil PathLocator")
	}
	if info == nil {
		return fmt.Errorf("register worktree in index: nil info")
	}
	entry := RepoIndexWorktree{
		Name:   info.Name,
		Slug:   info.Slug,
		Branch: info.Branch,
		Path:   info.Path,
	}
	if info.Owner != nil {
		entry.OwnerSessionID = info.Owner.SessionID
		entry.OwnerAgentID = info.Owner.AgentID
		entry.OwnerKind = string(info.Owner.Kind)
	}
	return WithRepoIndexLock(pl.StateDir, info.RepoID, func(idx *RepoIndex) error {
		idx.RepoID = info.RepoID
		idx.MainRepoRoot = info.RepoRoot
		if idx.DisplayName == "" {
			idx.DisplayName = filepath.Base(info.RepoRoot)
		}
		idx.UpsertWorktree(entry)
		idx.TouchLastUsed(info.Name)
		return nil
	})
}

// List returns chord-managed worktrees discovered via
// `git worktree list --porcelain`, filtered by the configured branch
// prefix (DefaultBranchPrefix when branchPrefix is empty). It does NOT
// consult the repo index; callers wanting LastUsedAt etc. should merge
// with LoadRepoIndex separately.
func List(ctx context.Context, repoRoot, branchPrefix string) ([]Info, error) {
	mainRoot, err := GitMainRoot(ctx, repoRoot)
	if err != nil {
		return nil, err
	}
	out, err := runGit(ctx, mainRoot, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	prefix := effectiveBranchPrefix(branchPrefix)
	repoID := ResolveRepoID(ctx, repoRoot, mainRoot)
	var infos []Info
	for _, e := range parseWorktreeListPorcelain(out) {
		br := shortBranch(e.Branch)
		if !strings.HasPrefix(br, prefix) {
			continue
		}
		slug := strings.TrimPrefix(br, prefix)
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

// CheckoutPaths returns the canonical path of every worktree of the repository,
// including checkouts chord did not create (a branch without the chord prefix,
// or one added by hand with `git worktree add`).
//
// This is the policy-root view, not the management view: every checkout of one
// repository contributes to a single set of relative roots, so the set must not
// depend on which worktrees chord manages. A checkout missing from it would let
// a repository-relative rule silently stop matching there while still matching
// in the main checkout — the cwd-dependent drift the merged-roots design exists
// to remove. Use List when the caller needs chord's own worktrees with their
// slug, branch, and repo metadata.
func CheckoutPaths(ctx context.Context, repoRoot string) ([]string, error) {
	mainRoot, err := GitMainRoot(ctx, repoRoot)
	if err != nil {
		return nil, err
	}
	out, err := runGit(ctx, mainRoot, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	var paths []string
	seen := make(map[string]struct{})
	for _, e := range parseWorktreeListPorcelain(out) {
		path, err := canonicalDir(e.Path)
		if err != nil || path == "" {
			continue
		}
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		paths = append(paths, path)
	}
	return paths, nil
}

// ResolveByName returns the worktree with the given name (= slug in v1),
// or an error when not found. branchPrefix scopes the search; empty
// falls back to DefaultBranchPrefix and must match the prefix Create
// used.
func ResolveByName(ctx context.Context, repoRoot, name, branchPrefix string) (*Info, error) {
	if err := ValidateSlug(name); err != nil {
		return nil, err
	}
	infos, err := List(ctx, repoRoot, branchPrefix)
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

// IsWorktreeOf reports whether dir is a linked git worktree whose main
// repository is mainRoot. It is the existence check resume needs: a recorded
// checkout that still belongs to this repository can be resumed, while one
// that was removed, is no longer a worktree root, or belongs elsewhere must
// fall back. Anything unverifiable (missing directory, not a git repository)
// reports false, so the caller falls back rather than restoring a stale path.
func IsWorktreeOf(ctx context.Context, dir, mainRoot string) bool {
	dir = strings.TrimSpace(dir)
	if dir == "" || strings.TrimSpace(mainRoot) == "" {
		return false
	}
	canonDir, err := canonicalDir(dir)
	if err != nil {
		return false
	}
	top, err := runGitText(ctx, canonDir, "rev-parse", "--show-toplevel")
	if err != nil {
		return false
	}
	topCanon, err := canonicalDir(top)
	if err != nil || topCanon != canonDir {
		return false
	}
	linked, err := IsInsideLinkedWorktree(ctx, canonDir)
	if err != nil || !linked {
		return false
	}
	main, err := GitMainRoot(ctx, canonDir)
	if err != nil {
		return false
	}
	mainCanon, err := canonicalDir(mainRoot)
	if err != nil {
		return false
	}
	return main == mainCanon
}

// ResolveByPath returns the chord-managed worktree whose root is dir, or an
// error when dir is not one of this repository's worktrees. dir is
// canonicalized first, so a caller may pass the path recorded in session
// metadata, a relative spelling, or a symlinked path.
func ResolveByPath(ctx context.Context, repoRoot, dir, branchPrefix string) (*Info, error) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return nil, fmt.Errorf("empty worktree path")
	}
	target, err := canonicalDir(dir)
	if err != nil {
		return nil, fmt.Errorf("resolve worktree path %s: %w", dir, err)
	}
	infos, err := List(ctx, repoRoot, branchPrefix)
	if err != nil {
		return nil, err
	}
	for i := range infos {
		if infos[i].Path == target {
			return &infos[i], nil
		}
	}
	return nil, fmt.Errorf("path %s is not a chord-managed worktree of this repository", target)
}

// Remove deletes a chord-managed worktree and its index entry, and clears the
// per-checkout runtime state chord derived from it (runtime cache and the
// project registry metadata). Branch is preserved unless DeleteBranch or Force
// is set.
//
// Sessions and exports are shared by every checkout of the repository — they
// live under the content root's project key — so removing a worktree never
// deletes them. PurgeSessions additionally deletes the worktree's own
// session/export store, which only holds sessions from versions that keyed
// sessions per checkout.
//
// pathLocator is required to compute and clean the worktree's
// ProjectKey-scoped state. Passing nil is an error.
func Remove(ctx context.Context, repoRoot, name string, opts RemoveOptions, pathLocator *config.PathLocator) error {
	if err := ValidateSlug(name); err != nil {
		return err
	}
	if pathLocator == nil {
		return fmt.Errorf("remove worktree: nil PathLocator")
	}
	info, err := ResolveByName(ctx, repoRoot, name, opts.BranchPrefix)
	if err != nil {
		return err
	}
	cwd, _ := os.Getwd()
	if cwdMatch, _ := canonicalDir(cwd); cwdMatch != "" && cwdMatch == info.Path {
		return fmt.Errorf("refusing to remove worktree %q: it is the current working directory", name)
	}
	if !opts.Force && !opts.DiscardChanges {
		statusOut, err := runGit(ctx, info.Path, "status", "--porcelain")
		if err != nil {
			return err
		}
		if len(strings.TrimSpace(string(statusOut))) > 0 {
			return fmt.Errorf("worktree %q has uncommitted changes; pass --force to remove anyway", name)
		}
	}
	// Consulted last on purpose: this is the only check that stands between an
	// in-flight removal and a checkout that something is still working in, so
	// it runs as close to the deletion as possible. The dirty and cwd checks
	// above are cheap fail-fasts; a holder that appeared while they ran must
	// still be seen here.
	if opts.Holders != nil {
		if holders := opts.Holders(info); len(holders) > 0 {
			return fmt.Errorf("worktree %q is still in use: %s; finish or stop that work before removing the checkout", info.Name, strings.Join(holders, "; "))
		}
	}
	gitArgs := []string{"worktree", "remove"}
	if opts.Force || opts.DiscardChanges {
		gitArgs = append(gitArgs, "--force")
	}
	gitArgs = append(gitArgs, info.Path)
	if _, err := runGit(ctx, info.RepoRoot, gitArgs...); err != nil {
		return err
	}
	if opts.Force {
		// `branch -D` succeeds even when unmerged; matches Force semantics.
		if _, berr := runGit(ctx, info.RepoRoot, "branch", "-D", info.Branch); berr != nil {
			// Non-fatal: the worktree itself is already gone, so this is a
			// diagnostic rather than a failure of the removal.
			log.Warnf("remove worktree branch failed worktree=%v branch=%v error=%v", name, info.Branch, berr)
		}
	} else if opts.DeleteBranch {
		if _, berr := runGit(ctx, info.RepoRoot, "branch", "-d", info.Branch); berr != nil {
			return fmt.Errorf("delete branch %s (use --force to override): %w", info.Branch, berr)
		}
	}
	if err := cleanupWorktreeProjectState(info.Path, pathLocator, opts.PurgeSessions); err != nil {
		log.Warnf("cleanup worktree project state failed worktree=%v error=%v", name, err)
	}
	if err := WithRepoIndexLock(pathLocator.StateDir, info.RepoID, func(idx *RepoIndex) error {
		idx.RemoveWorktree(name)
		return nil
	}); err != nil {
		return fmt.Errorf("update repo index: %w", err)
	}
	return nil
}

// cleanupWorktreeProjectState deletes the per-project state chord generated for
// the worktree. Runtime cache and the registry metadata always go; the
// worktree's own sessions and exports store is deleted only when
// purgeSessions is set, because it is the only remaining copy of sessions
// created before sessions became repository-scoped.
func cleanupWorktreeProjectState(worktreePath string, pl *config.PathLocator, purgeSessions bool) error {
	pj, err := pl.LocateProject(worktreePath)
	if err != nil {
		return err
	}
	dirs := []string{pj.RuntimeCacheDir}
	if purgeSessions {
		dirs = append(dirs, pj.ProjectSessionsDir, pj.ProjectExportsDir)
	}
	var firstErr error
	for _, p := range dirs {
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
		return "", fmt.Errorf("%w: %s", ErrNotGitRepository, dir)
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

// IsBareRepository reports whether dir is inside a bare git repository
// (`git rev-parse --is-bare-repository`). Bare repositories have no working
// tree: GitMainRoot would otherwise resolve to the container directory of
// the bare git dir, so callers must fall back to the working directory and
// must not hash the container directory as the repository identity.
func IsBareRepository(ctx context.Context, dir string) (bool, error) {
	if strings.TrimSpace(dir) == "" {
		var err error
		dir, err = os.Getwd()
		if err != nil {
			return false, fmt.Errorf("bare repository check: cwd: %w", err)
		}
	}
	out, err := runGitText(ctx, dir, "rev-parse", "--is-bare-repository")
	if err != nil {
		return false, nil
	}
	return strings.TrimSpace(out) == "true", nil
}

// GitCommonDir returns the canonical shared git directory
// (`--git-common-dir`) for dir. For bare repositories this is the bare git
// dir itself, which is the stable identity to hash instead of its container.
func GitCommonDir(ctx context.Context, dir string) (string, error) {
	if strings.TrimSpace(dir) == "" {
		var err error
		dir, err = os.Getwd()
		if err != nil {
			return "", fmt.Errorf("git common dir: cwd: %w", err)
		}
	}
	common, err := runGitText(ctx, dir, "rev-parse", "--git-common-dir")
	if err != nil {
		return "", fmt.Errorf("%w: %s", ErrNotGitRepository, dir)
	}
	return absClean(common, dir)
}

// ResolveRepoID returns the stable repository identifier for dir whose main
// root resolved to mainRoot. Bare repositories hash the bare git dir itself:
// hashing Dir(git-common-dir) would collide for two bare repositories under
// the same parent.
func ResolveRepoID(ctx context.Context, dir, mainRoot string) string {
	if bare, err := IsBareRepository(ctx, dir); err == nil && bare {
		if common, cerr := GitCommonDir(ctx, dir); cerr == nil && strings.TrimSpace(common) != "" {
			return RepoIDFor(common)
		}
	}
	return RepoIDFor(mainRoot)
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
