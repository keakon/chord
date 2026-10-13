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
