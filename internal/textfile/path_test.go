package textfile

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func setHomeEnvForTest(t *testing.T, home string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", home)
		t.Setenv("HOME", home)
		t.Setenv("HOMEDRIVE", "")
		t.Setenv("HOMEPATH", "")
		return
	}
	t.Setenv("HOME", home)
}

func tildePathForTest(rel string) string {
	if rel == "" {
		return "~"
	}
	if runtime.GOOS == "windows" {
		return `~\` + rel
	}
	return "~/" + rel
}

func TestNormalizeCachePathExpandsTildeToSameAbsolutePath(t *testing.T) {
	home := t.TempDir()
	setHomeEnvForTest(t, home)
	path := filepath.Join(home, "cache.txt")

	got := normalizeCachePath(tildePathForTest("cache.txt"))
	want := filepath.Clean(path)
	if got != want {
		t.Fatalf("normalizeCachePath = %q, want %q", got, want)
	}
}

func TestNormalizeCachePathRelativeAndAbsolute(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	want := filepath.Join(dir, "cache.txt")
	for _, path := range []string{"cache.txt", "./cache.txt", "  ./cache.txt  ", want} {
		if got := normalizeCachePath(path); got != want {
			t.Fatalf("normalizeCachePath(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestNormalizeCachePathMissingHome(t *testing.T) {
	setHomeEnvForTest(t, "")
	path := tildePathForTest("cache.txt")
	if got := normalizeCachePath(path); got != filepath.Clean(path) {
		t.Fatalf("normalizeCachePath(%q) = %q, want literal path", path, got)
	}
}

func TestNormalizeCachePathMissingWorkingDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not allow removing the current directory")
	}
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	// Some systems retain the working-directory spelling after removal.
	// Exercise the fallback only when filepath.Abs actually cannot resolve it.
	if _, err := filepath.Abs("cache.txt"); err == nil {
		t.Skip("removed working directory is still resolvable on this platform")
	}
	if got := normalizeCachePath("./cache.txt"); got != "cache.txt" {
		t.Fatalf("normalizeCachePath() = %q, want clean relative fallback", got)
	}
}
