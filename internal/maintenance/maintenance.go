package maintenance

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/recovery"
)

type Status struct {
	StateDir     string
	CacheDir     string
	LogsDir      string
	SessionsRoot string
	ExportsDir   string
	StateBytes   int64
	CacheBytes   int64
	LogsBytes    int64
	SessionCount int
	ProjectCount int
	ComputedAt   time.Time
	Warnings     []string
}

type CleanupOptions struct {
	ProjectRoot string
	OlderThan   time.Duration
	Yes         bool
	Now         time.Time
	// Cutoff overrides OlderThan when set. The startup sweep uses it to apply
	// RetainDays directly, avoiding a Duration conversion that overflows
	// beyond ~106752 days and would flip the sign into "no age limit".
	Cutoff *time.Time
	// Ctx cancels a long sweep between candidates; nil means no cancellation.
	Ctx context.Context
}

type CleanupCandidate struct {
	Path  string
	Kind  string
	Bytes int64
	Skip  string
}

type CleanupResult struct {
	DryRun     bool
	Candidates []CleanupCandidate
	Deleted    []CleanupCandidate
	Skipped    []CleanupCandidate
}

func BuildStatus(locator *config.PathLocator) (*Status, error) {
	if locator == nil {
		return nil, fmt.Errorf("path locator is nil")
	}
	st := &Status{StateDir: locator.StateDir, CacheDir: locator.CacheDir, LogsDir: locator.LogsDir, SessionsRoot: locator.SessionsRoot, ExportsDir: locator.ExportsDir, ComputedAt: time.Now().UTC()}
	st.StateBytes = dirSize(locator.StateDir, &st.Warnings)
	st.CacheBytes = dirSize(locator.CacheDir, &st.Warnings)
	st.LogsBytes = dirSize(locator.LogsDir, &st.Warnings)
	st.ProjectCount, st.SessionCount = countProjectsAndSessions(locator.SessionsRoot, &st.Warnings)
	return st, nil
}

func CleanupProject(locator *config.PathLocator, opts CleanupOptions) (*CleanupResult, error) {
	if locator == nil {
		return nil, fmt.Errorf("path locator is nil")
	}
	if strings.TrimSpace(opts.ProjectRoot) == "" {
		return nil, fmt.Errorf("project root is required")
	}
	pl, err := locator.LocateProject(opts.ProjectRoot)
	if err != nil {
		return nil, err
	}
	res := &CleanupResult{DryRun: !opts.Yes}
	for _, cand := range []CleanupCandidate{
		{Path: pl.ProjectSessionsDir, Kind: "project sessions"},
		{Path: pl.RuntimeCacheDir, Kind: "project runtime cache"},
		{Path: pl.ProjectExportsDir, Kind: "project exports"},
	} {
		cand.Bytes = dirSize(cand.Path, nil)
		if cand.Bytes == 0 && !pathExists(cand.Path) {
			continue
		}
		res.Candidates = append(res.Candidates, cand)
		if res.DryRun {
			continue
		}
		if err := os.RemoveAll(cand.Path); err != nil {
			cand.Skip = err.Error()
			res.Skipped = append(res.Skipped, cand)
			continue
		}
		res.Deleted = append(res.Deleted, cand)
	}
	return res, nil
}

func CleanupCache(locator *config.PathLocator, opts CleanupOptions) (*CleanupResult, error) {
	if locator == nil {
		return nil, fmt.Errorf("path locator is nil")
	}
	return cleanupChildren(filepath.Join(locator.CacheDir, "runtime", "session-cache"), "runtime cache", opts)
}

func CleanupLogs(locator *config.PathLocator, opts CleanupOptions) (*CleanupResult, error) {
	if locator == nil {
		return nil, fmt.Errorf("path locator is nil")
	}
	return cleanupChildren(locator.LogsDir, "logs", opts)
}

// CleanupSessions collects and removes expired session directories under
// <sessions-root>/<project-key>/. Project directories themselves are never
// removed: a concurrent process can create and lock a new session inside one
// at any time, and pruning the parent would race that creation (the space
// saved by dropping a leftover project.json is not worth the coordination).
//
// Age is the session's last activity per recovery.SessionActivityTime — the
// shared "last active" definition of the session list and --continue; the
// directory mtime only feeds sessions without an activity signal, and any
// failure to determine age, lock state, or identity skips the candidate.
// Deletion goes through the rename-to-tombstone protocol (see removeSessionDir).
func CleanupSessions(locator *config.PathLocator, opts CleanupOptions) (*CleanupResult, error) {
	if locator == nil {
		return nil, fmt.Errorf("path locator is nil")
	}
	res := &CleanupResult{DryRun: !opts.Yes}
	cutoff := cutoffTime(opts)
	projectDirs, err := os.ReadDir(locator.SessionsRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return res, nil
		}
		return nil, err
	}
	for _, projectEntry := range projectDirs {
		if err := ctxErr(opts.Ctx); err != nil {
			return res, err
		}
		if !projectEntry.IsDir() {
			continue
		}
		projectDir := filepath.Join(locator.SessionsRoot, projectEntry.Name())
		sessionEntries, err := os.ReadDir(projectDir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		for _, entry := range sessionEntries {
			if err := ctxErr(opts.Ctx); err != nil {
				return res, err
			}
			if !entry.IsDir() {
				continue
			}
			dir := filepath.Join(projectDir, entry.Name())
			candidate, ageErr := sessionRemovalCandidate(dir, entry.Name(), cutoff)
			if !candidate && ageErr == nil {
				continue
			}
			cand := CleanupCandidate{Path: dir, Kind: "session", Bytes: dirSize(dir, nil)}
			if ageErr != nil {
				cand.Skip = "activity check failed: " + ageErr.Error()
			} else {
				cand.Skip = sessionRemovalSkipReason(dir)
			}
			if cand.Skip != "" {
				res.Candidates = append(res.Candidates, cand)
				res.Skipped = append(res.Skipped, cand)
				continue
			}
			res.Candidates = append(res.Candidates, cand)
			if res.DryRun {
				continue
			}
			if err := removeSessionDir(dir, cutoff); err != nil {
				cand.Skip = err.Error()
				res.Skipped = append(res.Skipped, cand)
				continue
			}
			res.Deleted = append(res.Deleted, cand)
		}
	}
	return res, nil
}

// sessionRemovalCandidate reports whether a session directory is old enough to
// be considered for removal. Tombstones are always candidates regardless of
// age: they are logically deleted already and only await their final removal.
func sessionRemovalCandidate(dir, name string, cutoff *time.Time) (bool, error) {
	if recovery.IsDeletingSessionDirName(name) {
		return true, nil
	}
	if cutoff == nil {
		return true, nil
	}
	age, err := sessionAge(dir)
	return !age.After(*cutoff), err
}

// sessionRemovalSkipReason reports why a candidate must not be removed, or ""
// when removal may proceed. Age-based exclusions are handled by
// sessionRemovalCandidate; this covers lock state and identity failures only.
func sessionRemovalSkipReason(dir string) string {
	if locked, err := recovery.SessionLockActive(dir); err != nil {
		return "lock check failed: " + err.Error()
	} else if locked {
		return "session is locked"
	}
	return ""
}

// sessionAge reports when a session was last active. The shared activity
// definition (main.jsonl / input-delivery.json / usage summary mtimes) is the
// primary signal; the directory mtime only backs sessions without one. The
// directory mtime is also bumped by taking or releasing the session lock, so
// it must never override the file-based activity signals.
func sessionAge(dir string) (time.Time, error) {
	age, present, err := recovery.SessionActivityTime(dir)
	if err != nil {
		return time.Time{}, err
	}
	if present {
		return age, nil
	}
	info, err := os.Stat(dir)
	if err != nil {
		return time.Time{}, err
	}
	return info.ModTime(), nil
}

// removeSessionDir deletes one session directory under the deletion protocol:
// take the directory guard (never creating the directory), re-check
// inside the lock that the session stayed inactive, then rename
// to the tombstone name and remove. A session locked between the probe and
// here, or active between the scan and the lock, is skipped, not deleted.
func removeSessionDir(dir string, cutoff *time.Time) error {
	if err := recovery.DeleteSessionDir(dir, cutoff); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("session vanished")
		}
		return err
	}
	return nil
}

// CleanupLLMDumps removes over-age published LLM dump files
// (<session>/dumps/llm/*.json) from sessions that no process holds open.
// Only files are removed: the dump directory stays in place because DumpWriter
// creates it lazily and removing it would race a concurrent dump write between
// its MkdirAll and CreateTemp.
func CleanupLLMDumps(locator *config.PathLocator, opts CleanupOptions) (*CleanupResult, error) {
	if locator == nil {
		return nil, fmt.Errorf("path locator is nil")
	}
	res := &CleanupResult{DryRun: !opts.Yes}
	cutoff := cutoffTime(opts)
	projectDirs, err := os.ReadDir(locator.SessionsRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return res, nil
		}
		return nil, err
	}
	for _, projectEntry := range projectDirs {
		if err := ctxErr(opts.Ctx); err != nil {
			return res, err
		}
		if !projectEntry.IsDir() {
			continue
		}
		projectDir := filepath.Join(locator.SessionsRoot, projectEntry.Name())
		sessionEntries, err := os.ReadDir(projectDir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		for _, sessionEntry := range sessionEntries {
			if err := ctxErr(opts.Ctx); err != nil {
				return res, err
			}
			if !sessionEntry.IsDir() {
				continue
			}
			sessionDir := filepath.Join(projectDir, sessionEntry.Name())
			dumpsDir := filepath.Join(sessionDir, "dumps", "llm")
			dumpEntries, err := os.ReadDir(dumpsDir)
			if err != nil {
				if os.IsNotExist(err) {
					continue
				}
				res.Skipped = append(res.Skipped, CleanupCandidate{Path: sessionDir, Kind: "llm dumps", Skip: err.Error()})
				continue
			}
			locked, err := recovery.SessionLockActive(sessionDir)
			if err != nil {
				res.Skipped = append(res.Skipped, CleanupCandidate{Path: sessionDir, Kind: "llm dumps", Skip: "lock check failed: " + err.Error()})
				continue
			}
			if locked {
				// An active session may be appending dumps right now.
				res.Skipped = append(res.Skipped, CleanupCandidate{Path: sessionDir, Kind: "llm dumps", Skip: "session is locked"})
				continue
			}
			for _, f := range dumpEntries {
				if f.IsDir() {
					continue
				}
				name := f.Name()
				if !strings.HasSuffix(name, ".json") || strings.HasPrefix(name, ".tmp-") {
					continue
				}
				path := filepath.Join(dumpsDir, name)
				cand := CleanupCandidate{Path: path, Kind: "llm dump"}
				info, err := f.Info()
				if err != nil {
					cand.Skip = "stat failed: " + err.Error()
					res.Candidates = append(res.Candidates, cand)
					res.Skipped = append(res.Skipped, cand)
					continue
				}
				if !info.Mode().IsRegular() || cutoff != nil && info.ModTime().After(*cutoff) {
					continue
				}
				cand.Bytes = info.Size()
				res.Candidates = append(res.Candidates, cand)
				if res.DryRun {
					continue
				}
				if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
					cand.Skip = err.Error()
					res.Skipped = append(res.Skipped, cand)
					continue
				}
				res.Deleted = append(res.Deleted, cand)
			}
		}
	}
	return res, nil
}

func cleanupChildren(root, kind string, opts CleanupOptions) (*CleanupResult, error) {
	res := &CleanupResult{DryRun: !opts.Yes}
	cutoff := cutoffTime(opts)
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return res, nil
		}
		return nil, err
	}
	for _, entry := range entries {
		path := filepath.Join(root, entry.Name())
		if cutoff != nil {
			info, err := entry.Info()
			if err == nil && info.ModTime().After(*cutoff) {
				continue
			}
		}
		cand := CleanupCandidate{Path: path, Kind: kind, Bytes: dirSize(path, nil)}
		res.Candidates = append(res.Candidates, cand)
		if res.DryRun {
			continue
		}
		if err := os.RemoveAll(path); err != nil {
			cand.Skip = err.Error()
			res.Skipped = append(res.Skipped, cand)
			continue
		}
		res.Deleted = append(res.Deleted, cand)
	}
	return res, nil
}

func cutoffTime(opts CleanupOptions) *time.Time {
	if opts.Cutoff != nil {
		return opts.Cutoff
	}
	if opts.OlderThan <= 0 {
		return nil
	}
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}
	cutoff := now.Add(-opts.OlderThan)
	return &cutoff
}

// ctxErr returns the context error, or nil when no context is set.
func ctxErr(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}

func dirSize(root string, warnings *[]string) int64 {
	var total int64
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		return nil
	})
	if err != nil && warnings != nil && !errors.Is(err, os.ErrNotExist) {
		*warnings = append(*warnings, fmt.Sprintf("scan %s: %v", root, err))
	}
	return total
}

func countProjectsAndSessions(sessionsRoot string, warnings *[]string) (projects, sessions int) {
	entries, err := os.ReadDir(sessionsRoot)
	if err != nil {
		if warnings != nil && !os.IsNotExist(err) {
			*warnings = append(*warnings, fmt.Sprintf("scan sessions: %v", err))
		}
		return 0, 0
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		projects++
		children, err := os.ReadDir(filepath.Join(sessionsRoot, e.Name()))
		if err != nil {
			if warnings != nil {
				*warnings = append(*warnings, fmt.Sprintf("scan project sessions %s: %v", e.Name(), err))
			}
			continue
		}
		for _, child := range children {
			if child.IsDir() {
				sessions++
			}
		}
	}
	return projects, sessions
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
