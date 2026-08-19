package recovery

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"testing"
	"time"
)

func TestCreateNewSessionDirUsesLocalDigitsOnlySID(t *testing.T) {
	sessionsDir := filepath.Join(t.TempDir(), "sessions")
	dir, err := CreateNewSessionDir(sessionsDir)
	if err != nil {
		t.Fatalf("CreateNewSessionDir: %v", err)
	}
	if filepath.Dir(dir) != sessionsDir {
		t.Fatalf("parent = %q, want %q", filepath.Dir(dir), sessionsDir)
	}
	sid := filepath.Base(dir)
	// YYYYMMDDHHmmSSfff, local wall clock, digits only — safe as a directory
	// or filename on every platform and readable as local date/time.
	if !regexp.MustCompile(`^\d{17}$`).MatchString(sid) {
		t.Fatalf("sid = %q, want YYYYMMDDHHmmSSfff", sid)
	}
	parsed, err := time.ParseInLocation("20060102150405", sid[:14], time.Local)
	if err != nil {
		t.Fatalf("parse sid %q: %v", sid, err)
	}
	delta := time.Since(parsed)
	if delta < 0 {
		delta = -delta
	}
	if delta > 2*time.Second {
		t.Fatalf("sid %q parses to %v, %v away from now", sid, parsed, delta)
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		t.Fatalf("session dir not created: info=%v err=%v", info, err)
	}
	if runtime.GOOS != "windows" {
		assertPrivateDirMode(t, sessionsDir)
		assertPrivateDirMode(t, dir)
	}
}

// The local-time rule is pinned against a fixed zone: the CreateNewSessionDir
// assertion above compares against time.Local, which passes trivially on a
// UTC runner and so would not catch a revert to time.Now().UTC().
func TestSessionIDForTimeRendersLocalWallClock(t *testing.T) {
	zone := time.FixedZone("UTC+8", 8*60*60)
	instant := time.Date(2026, 8, 21, 7, 5, 9, 123*int(time.Millisecond), zone)

	if got := SessionIDForTime(instant); got != "20260821070509123" {
		t.Fatalf("SessionIDForTime = %q, want the wall clock in the value's own zone", got)
	}
	// The same instant rendered in UTC is a different, earlier-sorting ID: this
	// is exactly the mix a sessions directory carries after the change, which is
	// why the SID is not the ordering key.
	if got := SessionIDForTime(instant.UTC()); got != "20260820230509123" {
		t.Fatalf("SessionIDForTime(UTC) = %q, want the UTC wall clock", got)
	}
}

func assertPrivateDirMode(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Fatalf("mode(%s) = %04o, want 0700", path, got)
	}
}
