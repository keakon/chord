package config

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWriteConfigFileAtomicallyCreatesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	data := []byte("providers:\n  openai:\n    type: responses\n")
	if err := WriteConfigFileAtomically(path, data, 0o644); err != nil {
		t.Fatalf("WriteConfigFileAtomically: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != string(data) {
		t.Fatalf("file content = %q, want %q", got, data)
	}
}

func TestLockConfigMutationContextStopsWaiting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	first, err := LockConfigMutation(path)
	if err != nil {
		t.Fatalf("LockConfigMutation: %v", err)
	}
	defer first.Close()

	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err = LockConfigMutationContext(ctx, path)
	if err == nil {
		t.Fatal("LockConfigMutationContext acquired a held lock")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("LockConfigMutationContext error = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("context cancellation took too long: %v", elapsed)
	}
}

func TestLockConfigMutationContextRejectsCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := LockConfigMutationContext(ctx, filepath.Join(t.TempDir(), "config.yaml"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("LockConfigMutationContext error = %v, want context canceled", err)
	}
}

// A config file the user deliberately installed as a symlink — a dotfiles
// repository or a shared checkout beside the project — has to stay a symlink.
// Renaming a temp file over it would replace the link with a regular file and
// leave the real source stale, silently undoing that choice.
func TestCreateOrUpdateConfigFileLockedWritesThroughSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "shared", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(target, []byte("providers: {}\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	link := filepath.Join(dir, "config.yaml")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := CreateOrUpdateConfigFileLocked(link, func(current []byte) ([]byte, error) {
		return append(current, []byte("# added\n")...), nil
	}); err != nil {
		t.Fatalf("CreateOrUpdateConfigFileLocked: %v", err)
	}
	info, err := os.Lstat(link)
	if err != nil {
		t.Fatalf("Lstat: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("config symlink was replaced by a regular file")
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("ReadFile target: %v", err)
	}
	if string(got) != "providers: {}\n# added\n" {
		t.Fatalf("symlink target = %q, want the appended content", got)
	}
}

// A dangling link still names the file the user meant to keep, so the write
// recreates that file rather than replacing the link with a local copy.
func TestCreateOrUpdateConfigFileLockedWritesThroughDanglingSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "shared", "config.yaml")
	link := filepath.Join(dir, "config.yaml")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := CreateOrUpdateConfigFileLocked(link, func([]byte) ([]byte, error) {
		return []byte("providers: {}\n"), nil
	}); err != nil {
		t.Fatalf("CreateOrUpdateConfigFileLocked: %v", err)
	}
	info, err := os.Lstat(link)
	if err != nil {
		t.Fatalf("Lstat: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("dangling config symlink was replaced by a regular file")
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("expected the link target to be created: %v", err)
	}
}

func TestWriteConfigFileAtomicallyRejectsExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := WriteConfigFileAtomically(path, []byte("new"), 0o644); err == nil {
		t.Fatal("expected existing file write to fail")
	} else if !os.IsExist(err) {
		t.Fatalf("expected os.ErrExist, got %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "old" {
		t.Fatalf("existing file changed: %q", got)
	}
}

func TestWriteConfigFileAtomicallyPreservesDanglingSymlink(t *testing.T) {
	for _, relative := range []bool{false, true} {
		t.Run(map[bool]string{false: "absolute", true: "relative"}[relative], func(t *testing.T) {
			dir := t.TempDir()
			target := filepath.Join(dir, "shared", "config.yaml")
			link := filepath.Join(dir, "config.yaml")
			linkTarget := target
			if relative {
				linkTarget = filepath.Join("shared", "config.yaml")
			}
			if err := os.Symlink(linkTarget, link); err != nil {
				t.Fatal(err)
			}
			data := []byte("sample: true\n")
			if err := WriteConfigFileAtomically(link, data, 0o600); err != nil {
				t.Fatal(err)
			}
			info, err := os.Lstat(link)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode()&os.ModeSymlink == 0 {
				t.Fatal("config creation replaced the symlink")
			}
			got, err := os.ReadFile(target)
			if err != nil || string(got) != string(data) {
				t.Fatalf("target content = %q, err = %v", got, err)
			}
			if err := WriteConfigFileAtomically(link, []byte("changed\n"), 0o600); !errors.Is(err, os.ErrExist) {
				t.Fatalf("second write error = %v, want ErrExist", err)
			}
		})
	}
}

func TestConfigWritesPreserveSymlinkChains(t *testing.T) {
	for name, write := range map[string]func(string, []byte) error{
		"create": func(path string, data []byte) error {
			return WriteConfigFileAtomically(path, data, 0o600)
		},
		"update": func(path string, data []byte) error {
			return CreateOrUpdateConfigFileLocked(path, func([]byte) ([]byte, error) { return data, nil })
		},
	} {
		for _, relative := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/relative=%t", name, relative), func(t *testing.T) {
				dir := t.TempDir()
				target := filepath.Join(dir, "nested", "config.yaml")
				links := []string{filepath.Join(dir, "config.yaml"), filepath.Join(dir, "alias.yaml")}
				for i, link := range links {
					next := target
					if i == 0 {
						next = links[1]
					}
					if relative {
						var err error
						next, err = filepath.Rel(filepath.Dir(link), next)
						if err != nil {
							t.Fatal(err)
						}
					}
					if err := os.Symlink(next, link); err != nil {
						t.Fatal(err)
					}
				}
				data := []byte("sample: true\n")
				if err := write(links[0], data); err != nil {
					t.Fatal(err)
				}
				for _, link := range links {
					info, err := os.Lstat(link)
					if err != nil || info.Mode()&os.ModeSymlink == 0 {
						t.Fatalf("link %s was replaced: %v", link, err)
					}
				}
				got, err := os.ReadFile(target)
				if err != nil || string(got) != string(data) {
					t.Fatalf("target = %q, %v; want %q", got, err, data)
				}
			})
		}
		t.Run(name+"/cycle", func(t *testing.T) {
			dir := t.TempDir()
			first, second := filepath.Join(dir, "config.yaml"), filepath.Join(dir, "alias.yaml")
			if err := os.Symlink(second, first); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(first, second); err != nil {
				t.Fatal(err)
			}
			if err := write(first, []byte("sample: true\n")); err == nil {
				t.Fatal("cyclic config chain was accepted")
			}
			for _, link := range []string{first, second} {
				if _, err := os.Readlink(link); err != nil {
					t.Fatalf("cycle link was replaced: %v", err)
				}
			}
		})
	}
}

func TestConfigWriteTargetUsesResolvedParentForRelativeLink(t *testing.T) {
	dir := t.TempDir()
	parent := filepath.Join(dir, "nested", "rules")
	if err := os.MkdirAll(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(parent, filepath.Join(dir, "alias")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../config.yaml", filepath.Join(parent, "config.yaml")); err != nil {
		t.Fatal(err)
	}
	if err := WriteConfigFileAtomically(filepath.Join(dir, "alias", "config.yaml"), []byte("sample: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "nested", "config.yaml")); err != nil {
		t.Fatalf("relative link did not use its physical parent: %v", err)
	}
}

func TestConfigWriteTargetPreservesSymlinkTargetTraversal(t *testing.T) {
	dir := t.TempDir()
	parent := filepath.Join(dir, "nested", "rules")
	if err := os.MkdirAll(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(parent, filepath.Join(dir, "alias")); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "config.yaml")
	if err := os.Symlink("alias/../target.yaml", link); err != nil {
		t.Fatal(err)
	}
	if err := WriteConfigFileAtomically(link, []byte("sample: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "nested", "target.yaml")); err != nil {
		t.Fatalf("link target traversal did not preserve directory symlinks: %v", err)
	}
}

func TestConfigWriteTargetRejectsTraversalThroughRegularFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "regular"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "config.yaml")
	if err := os.Symlink("regular/../target.yaml", link); err != nil {
		t.Fatal(err)
	}
	if err := WriteConfigFileAtomically(link, []byte("sample: true\n"), 0o600); err == nil {
		t.Fatal("traversal through a regular file was accepted")
	}
	if _, err := os.Stat(filepath.Join(dir, "target.yaml")); !os.IsNotExist(err) {
		t.Fatalf("invalid traversal created a target: %v", err)
	}
}

func TestLockConfigMutationClosePreservesLockFileAndAllowsReacquire(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	lockPath := path + ".lock"

	lock, err := LockConfigMutation(path)
	if err != nil {
		t.Fatalf("LockConfigMutation: %v", err)
	}
	if _, err = os.Stat(lockPath); err != nil {
		t.Fatalf("expected lock file to exist while held: %v", err)
	}
	if err = lock.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err = os.Stat(lockPath); err != nil {
		t.Fatalf("expected stable lock file after close, got %v", err)
	}

	lock, err = LockConfigMutation(path)
	if err != nil {
		t.Fatalf("LockConfigMutation reacquire: %v", err)
	}
	if err = lock.Close(); err != nil {
		t.Fatalf("Close reacquired lock: %v", err)
	}
}
