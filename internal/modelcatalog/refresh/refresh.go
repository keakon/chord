// Package refresh pulls a pinned snapshot of the model catalog from the
// upstream chord-models repository into the local refresh cache. It runs only
// when the user asks for it: every call is an explicit network operation, and
// a failure anywhere leaves the previously effective catalog and the previous
// cache file untouched.
package refresh

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/modelcatalog"
	"github.com/keakon/chord/internal/modelcatalog/gen"
)

// DefaultRepository is the upstream model catalog data repository. Refresh
// pins tags and never follows a branch head; a run only takes effect when the
// fetched catalog version is newer than the one in effect.
const DefaultRepository = "https://github.com/keakon/chord-models"

// refreshTimeout bounds one whole refresh run (tag listing plus shallow
// clone). The refresh is foreground and explicit, but a hung remote must not
// hang the command indefinitely.
const refreshTimeout = 2 * time.Minute

// Result reports what one refresh run did.
type Result struct {
	// Updated reports whether a newer snapshot was written to the cache and
	// installed. A false value means the fetched snapshot is not newer than
	// the catalog in effect; the cache was left untouched.
	Updated bool
	// FromVersion / ToVersion are the catalog versions in effect before the
	// run and in the fetched snapshot.
	FromVersion string
	ToVersion   string
	// Revision is the upstream tag the snapshot was pulled from.
	Revision string
	// Commit is the upstream commit the tag resolved to.
	Commit string
	// CandidateCount is the number of discovery entries that shipped with
	// the snapshot.
	CandidateCount int
}

// Run refreshes the cache at cachePath from repository (empty: the default
// upstream) and installs the fetched snapshot into the running process when
// it is newer. Nothing here runs in the background or touches an active
// session: catalog changes reach a session only when its next context loads.
func Run(ctx context.Context, repository, cachePath string) (Result, error) {
	if strings.TrimSpace(repository) == "" {
		repository = DefaultRepository
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, refreshTimeout)
	defer cancel()

	from := modelcatalog.OriginInfo()

	tag, err := latestTag(ctx, repository)
	if err != nil {
		return Result{}, err
	}
	dir, err := cloneTag(ctx, repository, tag)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = os.RemoveAll(dir) }()

	catalog, err := gen.Load(dir)
	if err != nil {
		return Result{}, fmt.Errorf("upstream snapshot %s is not a valid catalog: %w", tag, err)
	}
	candidates, err := gen.LoadCandidates(dir, catalog)
	if err != nil {
		return Result{}, fmt.Errorf("upstream snapshot %s has invalid candidates: %w", tag, err)
	}

	result := Result{
		FromVersion:    from.Version,
		ToVersion:      catalog.Version,
		Revision:       tag,
		CandidateCount: len(candidates),
	}
	if modelcatalog.CompareVersions(catalog.Version, from.Version) <= 0 {
		return result, nil
	}

	commit, err := gitOutput(ctx, dir, "rev-parse", "HEAD")
	if err != nil {
		return Result{}, err
	}
	cache := &modelcatalog.CacheFile{
		SchemaVersion: modelcatalog.CacheSchemaVersion,
		Repository:    repository,
		Revision:      tag,
		Commit:        strings.TrimSpace(commit),
		FetchedAt:     time.Now().UTC().Format(time.RFC3339),
		Catalog:       catalog,
		Candidates:    candidates,
	}
	if err := writeCacheUnderLock(ctx, cachePath, cache); err != nil {
		return Result{}, err
	}
	if err := modelcatalog.InstallCachedCatalog(cachePath); err != nil {
		return Result{}, fmt.Errorf("install refreshed catalog: %w", err)
	}
	result.Updated = true
	return result, nil
}

// writeCacheUnderLock serializes concurrent refresh runs on the same cache
// file and re-checks the on-disk version under the lock: another process may
// have written a newer snapshot while this one was fetching, and that write
// must win. A corrupt cache file is replaced unconditionally. Callers have
// already established that the fetched version beats the version this
// process started from.
func writeCacheUnderLock(ctx context.Context, cachePath string, cache *modelcatalog.CacheFile) error {
	lock, err := config.LockConfigMutationContext(ctx, cachePath)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Close() }()
	if existing, err := modelcatalog.ReadCacheFile(cachePath); err == nil && existing.Catalog != nil {
		if modelcatalog.CompareVersions(existing.Catalog.Version, cache.Catalog.Version) >= 0 {
			return nil
		}
	}
	return modelcatalog.WriteCacheFile(cachePath, cache)
}

// latestTag resolves the newest version tag on the upstream repository.
func latestTag(ctx context.Context, repository string) (string, error) {
	out, err := gitOutput(ctx, "", "ls-remote", "--tags", repository)
	if err != nil {
		return "", fmt.Errorf("list tags on %s: %w", repository, err)
	}
	tags := parseLsRemoteTags([]byte(out))
	if len(tags) == 0 {
		return "", fmt.Errorf("%s has no version tags; catalog refresh pins tags and never follows a branch head", repository)
	}
	return newestTag(tags), nil
}

// parseLsRemoteTags collects the version tags from `git ls-remote --tags`
// output. Annotated tags appear twice (object and peeled `^{}` lines); only
// `v<version>`-shaped tags count, everything else is a non-release ref.
func parseLsRemoteTags(out []byte) []string {
	seen := make(map[string]bool)
	var tags []string
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		ref := fields[len(fields)-1]
		name, ok := strings.CutPrefix(ref, "refs/tags/")
		if !ok {
			continue
		}
		name = strings.TrimSuffix(name, "^{}")
		if !isVersionTag(name) || seen[name] {
			continue
		}
		seen[name] = true
		tags = append(tags, name)
	}
	return tags
}

func isVersionTag(tag string) bool {
	rest, ok := strings.CutPrefix(tag, "v")
	if !ok {
		return false
	}
	return rest != "" && rest[0] >= '0' && rest[0] <= '9'
}

// newestTag picks the tag with the highest catalog version.
func newestTag(tags []string) string {
	best := ""
	for _, tag := range tags {
		if best == "" || modelcatalog.CompareVersions(strings.TrimPrefix(tag, "v"), strings.TrimPrefix(best, "v")) > 0 {
			best = tag
		}
	}
	return best
}

// cloneTag shallow-clones the repository at a tag into a temp directory. The
// caller owns the directory and removes it.
func cloneTag(ctx context.Context, repository, tag string) (string, error) {
	dir, err := os.MkdirTemp("", "chord-modelcatalog-")
	if err != nil {
		return "", fmt.Errorf("create clone directory: %w", err)
	}
	if _, err := gitOutput(ctx, "", "clone", "--depth", "1", "--single-branch", "--branch", tag, "--quiet", repository, dir); err != nil {
		_ = os.RemoveAll(dir)
		return "", fmt.Errorf("clone %s at tag %s: %w", repository, tag, err)
	}
	return dir, nil
}

func gitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, msg)
		}
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return string(out), nil
}
