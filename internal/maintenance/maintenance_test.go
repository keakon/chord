package maintenance

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/recovery"
)

func testLocator(t *testing.T) *config.PathLocator {
	t.Helper()
	loc, err := config.ResolvePathLocator(nil, config.PathOptions{StateDir: filepath.Join(t.TempDir(), "state"), CacheDir: filepath.Join(t.TempDir(), "cache")})
	if err != nil {
		t.Fatalf("ResolvePathLocator: %v", err)
	}
	return loc
}

func TestMaintenanceHelpers(t *testing.T) {
	now := time.Date(2026, 4, 28, 12, 0, 0, 0, time.UTC)
	if got := cutoffTime(CleanupOptions{}); got != nil {
		t.Fatalf("cutoff without OlderThan = %v, want nil", got)
	}
	cutoff := cutoffTime(CleanupOptions{OlderThan: time.Hour, Now: now})
	if cutoff == nil || !cutoff.Equal(now.Add(-time.Hour)) {
		t.Fatalf("cutoff = %v, want %v", cutoff, now.Add(-time.Hour))
	}

	dir := t.TempDir()
	file := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(file, []byte("12345"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := dirSize(dir, nil); got != 5 {
		t.Fatalf("dirSize = %d, want 5", got)
	}
	if !pathExists(file) || pathExists(filepath.Join(dir, "missing")) {
		t.Fatal("pathExists returned unexpected result")
	}

	candidates := []CleanupCandidate{{Path: "z"}, {Path: "a"}, {Path: "m"}}
	SortCandidates(candidates)
	if candidates[0].Path != "a" || candidates[2].Path != "z" {
		t.Fatalf("SortCandidates = %+v", candidates)
	}
}

func TestCleanupLogsDryRunAndMissingRoot(t *testing.T) {
	loc := testLocator(t)
	if err := os.MkdirAll(loc.LogsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(loc.LogsDir, "old.log")
	newer := filepath.Join(loc.LogsDir, "new.log")
	if err := os.WriteFile(old, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newer, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := os.Chtimes(old, now.Add(-2*time.Hour), now.Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	res, err := CleanupLogs(loc, CleanupOptions{OlderThan: time.Hour, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if !res.DryRun || len(res.Candidates) != 1 || filepath.Base(res.Candidates[0].Path) != "old.log" {
		t.Fatalf("cleanup logs dry-run = %#v", res)
	}

	missingLoc := *loc
	missingLoc.LogsDir = filepath.Join(t.TempDir(), "missing")
	res, err = CleanupLogs(&missingLoc, CleanupOptions{})
	if err != nil || !res.DryRun || len(res.Candidates) != 0 {
		t.Fatalf("missing logs cleanup = %#v, %v", res, err)
	}
}

func TestCleanupProjectValidationAndDryRun(t *testing.T) {
	if _, err := CleanupProject(nil, CleanupOptions{}); err == nil {
		t.Fatal("expected nil locator error")
	}
	loc := testLocator(t)
	if _, err := CleanupProject(loc, CleanupOptions{}); err == nil {
		t.Fatal("expected missing project root error")
	}
	project := t.TempDir()
	pl, err := loc.EnsureProject(project)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(pl.ProjectExportsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pl.ProjectExportsDir, "x.md"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := CleanupProject(loc, CleanupOptions{ProjectRoot: project})
	if err != nil {
		t.Fatal(err)
	}
	if !res.DryRun || len(res.Candidates) == 0 || len(res.Deleted) != 0 {
		t.Fatalf("project dry-run = %#v", res)
	}
}

func TestBuildStatusCountsProjectsSessionsAndBytes(t *testing.T) {
	loc := testLocator(t)
	project := t.TempDir()
	pl, err := loc.EnsureProject(project)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(pl.ProjectSessionsDir, "20260427153042123"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pl.ProjectSessionsDir, "20260427153042123", "main.jsonl"), []byte("abc"), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := BuildStatus(loc)
	if err != nil {
		t.Fatal(err)
	}
	if st.ProjectCount != 1 || st.SessionCount != 1 || st.StateBytes < 3 {
		t.Fatalf("status = %#v", st)
	}
}

func TestCleanupSessionsDryRunAndSkipsLocked(t *testing.T) {
	loc := testLocator(t)
	project := t.TempDir()
	pl, err := loc.EnsureProject(project)
	if err != nil {
		t.Fatal(err)
	}
	oldDir := filepath.Join(pl.ProjectSessionsDir, "20260427153042123")
	lockedDir := filepath.Join(pl.ProjectSessionsDir, "20260427153042124")
	if err := os.MkdirAll(oldDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(lockedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	lock, err := recovery.AcquireSessionLock(lockedDir)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	res, err := CleanupSessions(loc, CleanupOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !res.DryRun || len(res.Candidates) != 2 || len(res.Skipped) != 1 {
		t.Fatalf("dry-run result = %#v", res)
	}
	if _, err := os.Stat(oldDir); err != nil {
		t.Fatalf("dry-run removed old dir: %v", err)
	}
}

func TestCleanupCacheDeletesWhenYes(t *testing.T) {
	loc := testLocator(t)
	dir := filepath.Join(loc.CacheDir, "runtime", "session-cache", "project", "sid")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	res, err := CleanupCache(loc, CleanupOptions{Yes: true, OlderThan: time.Nanosecond, Now: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if res.DryRun || len(res.Deleted) == 0 {
		t.Fatalf("cleanup result = %#v", res)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("cache dir still exists err=%v", err)
	}
}
