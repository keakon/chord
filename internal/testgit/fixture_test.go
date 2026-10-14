package testgit

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestRepositoriesHaveIndependentWorktreesAndRefs(t *testing.T) {
	contents := map[string]string{"README.md": "sample\n"}
	first := NewRepository(t, contents)
	second := NewRepository(t, contents)
	if err := os.WriteFile(filepath.Join(first, "README.md"), []byte("changed\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(first, ".git", "refs", "heads", "main")); err != nil {
		t.Fatal(err)
	}
	for _, root := range []string{second, NewRepository(t, contents)} {
		data, err := os.ReadFile(filepath.Join(root, "README.md"))
		if err != nil || string(data) != contents["README.md"] {
			t.Fatalf("fixture contents = %q, %v", data, err)
		}
		cmd := exec.Command("git", "-C", root, "status", "--porcelain")
		cmd.Env = environment()
		if out, err := cmd.CombinedOutput(); err != nil || len(out) != 0 {
			t.Fatalf("fixture is not a clean repository: %s, %v", out, err)
		}
	}
	other := NewRepository(t, map[string]string{"README.md": "other\n"})
	if data, err := os.ReadFile(filepath.Join(other, "README.md")); err != nil || string(data) != "other\n" {
		t.Fatalf("different fixture = %q, %v", data, err)
	}
}

func TestRepositoryIgnoresInheritedGitLocation(t *testing.T) {
	external := t.TempDir()
	for _, key := range []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES"} {
		t.Setenv(key, external)
	}
	t.Setenv("GIT_INDEX_FILE", filepath.Join(external, "index"))
	root := NewRepository(t, map[string]string{"sample.txt": "isolated\n"})
	cmd := exec.Command("git", "-C", root, "ls-files")
	cmd.Env = environment()
	if out, err := cmd.CombinedOutput(); err != nil || string(out) != "sample.txt\n" {
		t.Fatalf("fixture tracked files = %q, %v", out, err)
	}
	if entries, err := os.ReadDir(external); err != nil || len(entries) != 0 {
		t.Fatalf("external Git location was modified: %v, %v", entries, err)
	}
}

func TestRejectsPathsOutsideTheFixtureWorktree(t *testing.T) {
	for _, path := range []string{"../outside", `..\outside`, ".git/config", ".GIT/config"} {
		if _, err := build(map[string]string{path: "sample"}); err == nil {
			t.Errorf("accepted fixture path %q", path)
		}
	}
}
