package maintenance

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
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
		if !projectEntry.IsDir() {
			continue
		}
		projectDir := filepath.Join(locator.SessionsRoot, projectEntry.Name())
		sessionEntries, err := os.ReadDir(projectDir)
		if err != nil {
			return nil, err
		}
		for _, entry := range sessionEntries {
			if !entry.IsDir() {
				continue
			}
			dir := filepath.Join(projectDir, entry.Name())
			if cutoff != nil {
				info, err := entry.Info()
				if err == nil && info.ModTime().After(*cutoff) {
					continue
				}
			}
			cand := CleanupCandidate{Path: dir, Kind: "session", Bytes: dirSize(dir, nil)}
			if locked, err := recovery.SessionLockActive(dir); err != nil {
				cand.Skip = "lock check failed: " + err.Error()
			} else if locked {
				cand.Skip = "session is locked"
			}
			res.Candidates = append(res.Candidates, cand)
			if cand.Skip != "" {
				res.Skipped = append(res.Skipped, cand)
				continue
			}
			if res.DryRun {
				continue
			}
			if err := os.RemoveAll(dir); err != nil {
				cand.Skip = err.Error()
				res.Skipped = append(res.Skipped, cand)
				continue
			}
			res.Deleted = append(res.Deleted, cand)
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

func SortCandidates(candidates []CleanupCandidate) {
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].Path < candidates[j].Path })
}
