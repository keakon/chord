package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestDirectoryInstructionReadPinsAuthorizedTarget(t *testing.T) {
	r, root := newResolverFixture(t)
	external := filepath.Join(t.TempDir(), "sample.txt")
	if err := os.WriteFile(external, []byte("external rules"), 0600); err != nil {
		t.Fatal(err)
	}
	real := filepath.Join(root, "rules.txt")
	if err := os.WriteFile(real, []byte("local rules"), 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "AGENTS.md")
	if err := os.Symlink(real, path); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if got := r.readInstructionFile(context.Background(), path); got.Status != "present" || got.Body != "local rules" {
		t.Fatalf("authorized link failed: %+v", got)
	}
	r.permissionCheck = func(_ context.Context, p string) error {
		if p == external {
			return fmt.Errorf("read denied")
		}
		if p == real {
			if err := os.Remove(real); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(external, real); err != nil {
				t.Fatal(err)
			}
		}
		return nil
	}
	got := r.readInstructionFile(context.Background(), path)
	if got.Status != "blocked" || got.Body != "" {
		t.Fatalf("read denied external content under authorized identity: %+v", got)
	}
}

func TestDirectoryInstructionReadRejectsReplacedParent(t *testing.T) {
	r, root := newResolverFixture(t)
	external := t.TempDir()
	parent := filepath.Join(root, "rules")
	if err := os.Mkdir(parent, 0700); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{parent, external} {
		if err := os.WriteFile(filepath.Join(dir, "sample.txt"), []byte("sample rules"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	real := filepath.Join(parent, "sample.txt")
	path := filepath.Join(root, "AGENTS.md")
	if err := os.Symlink(real, path); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	r.permissionCheck = func(_ context.Context, p string) error {
		if p == real {
			if err := os.Rename(parent, parent+"-saved"); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(external, parent); err != nil {
				t.Fatal(err)
			}
		}
		return nil
	}
	got := r.readInstructionFile(context.Background(), path)
	if got.Status != "blocked" || got.Body != "" {
		t.Fatalf("accepted replaced parent: %+v", got)
	}
}
