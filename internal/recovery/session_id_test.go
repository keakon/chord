package recovery

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

func TestCreateNewSessionDirUsesUTCCompactTimestampSID(t *testing.T) {
	sessionsDir := t.TempDir()
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
}
