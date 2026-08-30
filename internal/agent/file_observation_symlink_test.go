package agent

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// A read through a symlink records the symlink path. The lazy validation memo
// must therefore key on the target's identity: keying on the link's own
// mtime/size would keep reporting "unchanged" after an external edit to the
// target, leaving a stale read in context marked as current.
func TestLazyReadValidationFollowsSymlinkTarget(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation is platform-specific")
	}
	dir := t.TempDir()
	a := newTestMainAgent(t, dir)

	target := filepath.Join(dir, "target.txt")
	link := filepath.Join(dir, "link.txt")
	if err := os.WriteFile(target, []byte("original content\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	expected, exists, _, err := verifiedCurrentFileHash(link)
	if err != nil || !exists {
		t.Fatalf("hash through symlink: exists=%v err=%v", exists, err)
	}

	verdicts := map[string]externalReadLazyVerdict{}
	if a.lazyReadStatCheck(link, expected, verdicts) {
		t.Fatal("unmodified symlink read reported as changed")
	}

	linkBefore, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("externally edited content, a different length\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	linkAfter, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	// Precondition: the link's own metadata is untouched by the target edit, so
	// a link-keyed memo could not notice the change.
	if !linkBefore.ModTime().Equal(linkAfter.ModTime()) || linkBefore.Size() != linkAfter.Size() {
		t.Skip("platform updates the symlink's own stat on target writes")
	}

	if !a.lazyReadStatCheck(link, expected, verdicts) {
		t.Fatal("edit to the symlink target was not detected: the stale read stays marked current")
	}
}
