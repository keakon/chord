package recovery

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"testing"
)

func TestCreateNewSessionDirUsesUTCCompactTimestampSID(t *testing.T) {
	sessionsDir := filepath.Join(t.TempDir(), "sessions")
	dir, err := CreateNewSessionDir(sessionsDir)
	if err != nil {
		t.Fatalf("CreateNewSessionDir: %v", err)
	}
	if filepath.Dir(dir) != sessionsDir {
		t.Fatalf("parent = %q, want %q", filepath.Dir(dir), sessionsDir)
	}
	if !regexp.MustCompile(`^\d{17}$`).MatchString(filepath.Base(dir)) {
		t.Fatalf("sid = %q, want YYYYMMDDHHmmSSfff", filepath.Base(dir))
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		t.Fatalf("session dir not created: info=%v err=%v", info, err)
	}
	if runtime.GOOS != "windows" {
		assertPrivateDirMode(t, sessionsDir)
		assertPrivateDirMode(t, dir)
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
