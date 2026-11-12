package maintenance

import (
	"os"
	"path/filepath"
	"strings"
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

func TestCleanupSessionsDryRunPreviewsSessionButKeepsProjectDir(t *testing.T) {
	loc := testLocator(t)
	project := t.TempDir()
	pl, err := loc.EnsureProject(project)
	if err != nil {
		t.Fatal(err)
	}
	sessionDir := filepath.Join(pl.ProjectSessionsDir, "20260427153042123")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
	}

	res, err := CleanupSessions(loc, CleanupOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !res.DryRun || len(res.Candidates) != 1 || len(res.Deleted) != 0 {
		t.Fatalf("dry-run result = %#v", res)
	}
	if res.Candidates[0].Kind != "session" || res.Candidates[0].Path != sessionDir {
		t.Fatalf("candidates = %#v", res.Candidates)
	}
	if _, err := os.Stat(sessionDir); err != nil {
		t.Fatalf("dry-run removed session dir: %v", err)
	}
	if _, err := os.Stat(pl.ProjectSessionsDir); err != nil {
		t.Fatalf("project sessions dir must be preserved, got: %v", err)
	}
}

func TestCleanupSessionsKeepsMetadataOnlyProjectDirs(t *testing.T) {
	loc := testLocator(t)
	project := t.TempDir()
	pl, err := loc.EnsureProject(project)
	if err != nil {
		t.Fatal(err)
	}

	res, err := CleanupSessions(loc, CleanupOptions{Yes: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.DryRun || len(res.Candidates) != 0 || len(res.Deleted) != 0 {
		t.Fatalf("cleanup result = %#v", res)
	}
	if _, err := os.Stat(pl.ProjectSessionsDir); err != nil {
		t.Fatalf("metadata-only project dir must be preserved, got: %v", err)
	}
}

func TestCleanupSessionsDeletesSessionAndKeepsProjectDir(t *testing.T) {
	loc := testLocator(t)
	project := t.TempDir()
	pl, err := loc.EnsureProject(project)
	if err != nil {
		t.Fatal(err)
	}
	sessionDir := filepath.Join(pl.ProjectSessionsDir, "20260427153042123")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
	}

	res, err := CleanupSessions(loc, CleanupOptions{Yes: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.DryRun || len(res.Deleted) != 1 || res.Deleted[0].Kind != "session" {
		t.Fatalf("cleanup result = %#v", res)
	}
	if _, err := os.Stat(sessionDir); !os.IsNotExist(err) {
		t.Fatalf("session dir still exists err=%v", err)
	}
	if _, err := os.Stat(pl.ProjectSessionsDir); err != nil {
		t.Fatalf("project sessions dir must survive session deletion, got: %v", err)
	}
}

func TestCleanupSessionsOldSessionDeletedByDirMtimeFallback(t *testing.T) {
	loc := testLocator(t)
	project := t.TempDir()
	pl, err := loc.EnsureProject(project)
	if err != nil {
		t.Fatal(err)
	}
	sessionDir := filepath.Join(pl.ProjectSessionsDir, "20260427153042123")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	oldTime := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(sessionDir, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}

	res, err := CleanupSessions(loc, CleanupOptions{Yes: true, OlderThan: 24 * time.Hour, Now: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if res.DryRun || len(res.Deleted) != 1 || res.Deleted[0].Kind != "session" {
		t.Fatalf("cleanup result = %#v", res)
	}
	if _, err := os.Stat(pl.ProjectSessionsDir); err != nil {
		t.Fatalf("project sessions dir must survive session deletion, got: %v", err)
	}
}

// Session age follows the shared activity definition, not the directory
// mtime: taking or releasing a session lock bumps the directory mtime, which
// must not re-arm the retention window of a session whose transcript is old,
// and must not age a session whose transcript is fresh.
func TestCleanupSessionsAgeFollowsTranscriptNotDirMtime(t *testing.T) {
	loc := testLocator(t)
	project := t.TempDir()
	pl, err := loc.EnsureProject(project)
	if err != nil {
		t.Fatal(err)
	}
	oldTime := time.Now().Add(-48 * time.Hour)
	freshTime := time.Now().Add(-time.Hour)

	freshTranscriptDir := filepath.Join(pl.ProjectSessionsDir, "20260427153042123")
	if err := os.MkdirAll(freshTranscriptDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(freshTranscriptDir, "main.jsonl"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(freshTranscriptDir, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	staleTranscriptDir := filepath.Join(pl.ProjectSessionsDir, "20260427153042124")
	if err := os.MkdirAll(staleTranscriptDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staleTranscriptDir, "main.jsonl"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(staleTranscriptDir, freshTime, freshTime); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(filepath.Join(staleTranscriptDir, "main.jsonl"), oldTime, oldTime); err != nil {
		t.Fatal(err)
	}

	res, err := CleanupSessions(loc, CleanupOptions{Yes: true, OlderThan: 24 * time.Hour, Now: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if res.DryRun || len(res.Deleted) != 1 || res.Deleted[0].Path != staleTranscriptDir {
		t.Fatalf("cleanup result = %#v", res)
	}
	if _, err := os.Stat(freshTranscriptDir); err != nil {
		t.Fatalf("session with fresh transcript must be kept despite old dir mtime: %v", err)
	}
	if _, err := os.Stat(staleTranscriptDir); !os.IsNotExist(err) {
		t.Fatalf("session with stale transcript must be deleted despite fresh dir mtime err=%v", err)
	}
}

func TestCleanupSessionsRemovesTombstonesAndSkipsLockedOnes(t *testing.T) {
	loc := testLocator(t)
	project := t.TempDir()
	pl, err := loc.EnsureProject(project)
	if err != nil {
		t.Fatal(err)
	}
	freshTime := time.Now().Add(-time.Hour)
	tombstone := filepath.Join(pl.ProjectSessionsDir, "20260427153042123.deleting")
	if err := os.MkdirAll(tombstone, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(tombstone, freshTime, freshTime); err != nil {
		t.Fatal(err)
	}
	lockedTombstone := filepath.Join(pl.ProjectSessionsDir, "20260427153042124.deleting")
	if err := os.MkdirAll(lockedTombstone, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(lockedTombstone, freshTime, freshTime); err != nil {
		t.Fatal(err)
	}
	lock, err := recovery.AcquireSessionLock(strings.TrimSuffix(lockedTombstone, recovery.SessionDeletingSuffix))
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	if err := os.Remove(lockedTombstone); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(strings.TrimSuffix(lockedTombstone, recovery.SessionDeletingSuffix), lockedTombstone); err != nil {
		t.Fatal(err)
	}

	dry, err := CleanupSessions(loc, CleanupOptions{OlderThan: 24 * time.Hour, Now: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if !dry.DryRun || len(dry.Candidates) != 2 || len(dry.Skipped) != 1 {
		t.Fatalf("dry-run result = %#v", dry)
	}
	res, err := CleanupSessions(loc, CleanupOptions{Yes: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.DryRun || len(res.Deleted) != 1 || res.Deleted[0].Path != tombstone {
		t.Fatalf("cleanup result = %#v", res)
	}
	if _, err := os.Stat(tombstone); !os.IsNotExist(err) {
		t.Fatalf("tombstone still exists err=%v", err)
	}
	if _, err := os.Stat(lockedTombstone); err != nil {
		t.Fatalf("locked tombstone must be kept: %v", err)
	}
}

// removeSessionDir must re-check activity after taking the lock: the scan can
// be arbitrarily older than the lock, and a session that became active in
// between must survive.
func TestRemoveSessionDirRechecksActivityInsideLock(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "20260427153042123")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	mainPath := filepath.Join(dir, "main.jsonl")
	if err := os.WriteFile(mainPath, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	cutoff := time.Now().Add(-24 * time.Hour)
	oldTime := cutoff.Add(-time.Hour)
	if err := os.Chtimes(mainPath, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	// Activity lands after the scan but before the lock: main.jsonl is fresh
	// again when removeSessionDir runs.
	freshTime := time.Now()
	if err := os.Chtimes(mainPath, freshTime, freshTime); err != nil {
		t.Fatal(err)
	}

	if err := removeSessionDir(dir, &cutoff); err == nil {
		t.Fatal("expected active-session refusal")
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("active session must be preserved: %v", err)
	}
}

func TestCleanupLLMDumpsRemovesOnlyOverAgeDumpFiles(t *testing.T) {
	loc := testLocator(t)
	project := t.TempDir()
	pl, err := loc.EnsureProject(project)
	if err != nil {
		t.Fatal(err)
	}
	sessionDir := filepath.Join(pl.ProjectSessionsDir, "20260427153042123")
	dumpsDir := filepath.Join(sessionDir, "dumps", "llm")
	if err := os.MkdirAll(dumpsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	oldTime := now.Add(-48 * time.Hour)
	write := func(name string, when time.Time) string {
		t.Helper()
		path := filepath.Join(dumpsDir, name)
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, when, when); err != nil {
			t.Fatal(err)
		}
		return path
	}
	oldDump := write("0001_20260426_120000_openai_gpt.json", oldTime)
	freshDump := write("0002_20260427_120000_openai_gpt.json", now.Add(-time.Hour))
	strayTmp := write(".tmp-123", oldTime)
	notes := write("notes.txt", oldTime)

	res, err := CleanupLLMDumps(loc, CleanupOptions{Yes: true, OlderThan: 24 * time.Hour, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if res.DryRun || len(res.Deleted) != 1 {
		t.Fatalf("cleanup result = %#v", res)
	}
	if _, err := os.Stat(oldDump); !os.IsNotExist(err) {
		t.Fatalf("over-age dump still exists err=%v", err)
	}
	for _, keep := range []string{freshDump, strayTmp, notes, dumpsDir, filepath.Join(sessionDir, "dumps")} {
		if _, err := os.Stat(keep); err != nil {
			t.Fatalf("%s must be preserved: %v", keep, err)
		}
	}
}

func TestCleanupLLMDumpsSkipsLockedSession(t *testing.T) {
	loc := testLocator(t)
	project := t.TempDir()
	pl, err := loc.EnsureProject(project)
	if err != nil {
		t.Fatal(err)
	}
	sessionDir := filepath.Join(pl.ProjectSessionsDir, "20260427153042123")
	dumpsDir := filepath.Join(sessionDir, "dumps", "llm")
	if err := os.MkdirAll(dumpsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	dump := filepath.Join(dumpsDir, "0001_x.json")
	if err := os.WriteFile(dump, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	oldTime := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(dump, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	lock, err := recovery.AcquireSessionLock(sessionDir)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()

	res, err := CleanupLLMDumps(loc, CleanupOptions{Yes: true, OlderThan: time.Hour, Now: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if res.DryRun || len(res.Deleted) != 0 || len(res.Skipped) != 1 {
		t.Fatalf("cleanup result = %#v", res)
	}
	if _, err := os.Stat(dump); err != nil {
		t.Fatalf("locked session's dump must be kept: %v", err)
	}
}

func TestRunAutoCleanupSkipsWhenLockHeld(t *testing.T) {
	loc := testLocator(t)
	release, skipped, err := acquireCleanupLock(loc.StateDir)
	if err != nil || skipped != "" {
		t.Fatalf("acquireCleanupLock = %q, %v", skipped, err)
	}
	defer release()

	cfg := config.MaintenanceCleanupConfig{Sessions: config.MaintenanceRetainConfig{Enabled: true, RetainDays: 1}}
	res, err := RunAutoCleanup(t.Context(), loc, cfg, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if res.Skipped == "" || res.Sessions != nil {
		t.Fatalf("result = %#v", res)
	}
}

func TestRunAutoCleanupAppliesEnabledPolicies(t *testing.T) {
	loc := testLocator(t)
	project := t.TempDir()
	pl, err := loc.EnsureProject(project)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	oldTime := now.Add(-48 * time.Hour)

	oldSession := filepath.Join(pl.ProjectSessionsDir, "20260427153042123")
	if err := os.MkdirAll(oldSession, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(oldSession, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	keptSession := filepath.Join(pl.ProjectSessionsDir, "20260427153042124")
	if err := os.MkdirAll(keptSession, 0o755); err != nil {
		t.Fatal(err)
	}
	oldDumpDir := filepath.Join(keptSession, "dumps", "llm")
	if err := os.MkdirAll(oldDumpDir, 0o755); err != nil {
		t.Fatal(err)
	}
	oldDump := filepath.Join(oldDumpDir, "0001_x.json")
	if err := os.WriteFile(oldDump, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(oldDump, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}

	// Sessions policy disabled, dumps enabled: session survives, dump goes.
	cfg := config.MaintenanceCleanupConfig{LLMDumps: config.MaintenanceRetainConfig{Enabled: true, RetainDays: 1}}
	res, err := RunAutoCleanup(t.Context(), loc, cfg, now)
	if err != nil {
		t.Fatal(err)
	}
	if res.Skipped != "" || res.Sessions != nil || res.LLMDumps == nil || len(res.LLMDumps.Deleted) != 1 {
		t.Fatalf("result = %#v", res)
	}
	if _, err := os.Stat(oldSession); err != nil {
		t.Fatalf("disabled policy must keep sessions: %v", err)
	}
	if _, err := os.Stat(oldDump); !os.IsNotExist(err) {
		t.Fatalf("over-age dump still exists err=%v", err)
	}

	// Sessions enabled with a 1-day window: the old session goes too.
	cfg.Sessions = config.MaintenanceRetainConfig{Enabled: true, RetainDays: 1}
	res, err = RunAutoCleanup(t.Context(), loc, cfg, now)
	if err != nil {
		t.Fatal(err)
	}
	if res.Sessions == nil || len(res.Sessions.Deleted) != 1 {
		t.Fatalf("result = %#v", res)
	}
	if _, err := os.Stat(oldSession); !os.IsNotExist(err) {
		t.Fatalf("over-age session still exists err=%v", err)
	}
}

func TestRetainCleanupOptionsClampsDays(t *testing.T) {
	now := time.Date(2026, 4, 28, 12, 0, 0, 0, time.UTC)
	ctx := t.Context()
	opts := retainCleanupOptions(ctx, now, 0)
	if opts.Cutoff == nil || !opts.Cutoff.Equal(now.AddDate(0, 0, -1)) {
		t.Fatalf("zero days cutoff = %v, want one day back", opts.Cutoff)
	}
	opts = retainCleanupOptions(ctx, now, config.MaxMaintenanceRetainDays*4)
	if opts.Cutoff == nil || !opts.Cutoff.Equal(now.AddDate(0, 0, -config.MaxMaintenanceRetainDays)) {
		t.Fatalf("huge days cutoff = %v, want clamped to %d days back", opts.Cutoff, config.MaxMaintenanceRetainDays)
	}
	if opts.Ctx != ctx || !opts.Yes {
		t.Fatalf("options = %#v", opts)
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
