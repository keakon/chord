package pathutil

import (
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestOpenCanonicalRegular(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "sample.txt")
	if err := os.WriteFile(path, []byte("sample"), 0600); err != nil {
		t.Fatal(err)
	}
	f, err := OpenCanonicalRegular(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil || string(data) != "sample" {
		t.Fatalf("read: %q %v", data, err)
	}
	for _, path := range []string{dir, "relative.txt", filepath.Join(dir, "missing")} {
		if f, err := OpenCanonicalRegular(path); err == nil {
			f.Close()
			t.Fatalf("accepted %s", path)
		}
	}
}

// A path resolved a moment ago may name a directory that has since been
// replaced by a link to somewhere else. Opening it must fail rather than follow
// the replacement: the caller resolved the path to decide what it was allowed
// to read, and that decision is void once the directory changed.
func TestOpenCanonicalRegularRejectsReplacedParent(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	external := t.TempDir()
	parent := filepath.Join(root, "rules")
	if err := os.Mkdir(parent, 0700); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{parent, external} {
		if err := os.WriteFile(filepath.Join(dir, "sample.txt"), []byte("sample"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(parent, "sample.txt")
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(parent, parent+"-saved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, parent); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if f, err := OpenCanonicalRegular(resolved); err == nil {
		f.Close()
		t.Fatal("opened a file through a replaced parent")
	}
}

func TestOpenCanonicalRegularRejectsLinks(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "sample.txt")
	if err := os.WriteFile(target, []byte("sample"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	parent := filepath.Join(dir, "parent")
	if err := os.Symlink(dir, parent); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{link, filepath.Join(parent, "sample.txt")} {
		if f, err := OpenCanonicalRegular(path); err == nil {
			f.Close()
			t.Fatalf("accepted link %s", path)
		}
	}
}
