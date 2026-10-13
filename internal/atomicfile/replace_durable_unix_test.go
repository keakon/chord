//go:build !windows

package atomicfile

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestReplaceDurablePermissions(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "private", "record.json")
	if _, err := ReplaceDurable(path, []byte("value"), 0o600); err != nil {
		t.Fatal(err)
	}
	for path, mode := range map[string]os.FileMode{path: 0o600, filepath.Dir(path): 0o700} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != mode {
			t.Fatalf("permissions %o, want %o", info.Mode().Perm(), mode)
		}
	}
}

func TestReplaceDurableFailedDirectorySync(t *testing.T) {
	path := filepath.Join(t.TempDir(), "record.json")
	if _, err := ReplaceDurable(path, []byte("before"), 0o600); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("injected directory sync failure")
	outcome, err := replaceDurable(path, []byte("after"), 0o600, func(source, target string) (ReplaceOutcome, error) {
		return commitDurableWithSync(source, target, func(string) error { return failure })
	})
	if outcome != ReplacementUnknown || !errors.Is(err, failure) {
		t.Fatalf("got %s, %v", outcome, err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "after" {
		t.Fatalf("replacement should be visible: %q, %v", data, err)
	}
	// A later lock holder confirms the complete surviving version verbatim.
	if outcome, err := ReplaceDurable(path, data, 0o600); outcome != Durable || err != nil {
		t.Fatalf("confirm: %s, %v", outcome, err)
	}
}

func TestReplaceDurableProcessExit(t *testing.T) {
	if path := os.Getenv("CHORD_ATOMICFILE_CRASH_PATH"); path != "" {
		_, _ = replaceDurable(path, []byte("after"), 0o600, func(source, target string) (ReplaceOutcome, error) {
			if os.Getenv("CHORD_ATOMICFILE_CRASH_POINT") == "after" {
				if err := os.Rename(source, target); err != nil {
					os.Exit(3)
				}
			}
			os.Exit(23)
			return NotReplaced, nil
		})
		os.Exit(4)
	}
	for _, point := range []string{"before", "after"} {
		t.Run(point, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "record.json")
			if _, err := ReplaceDurable(path, []byte("before"), 0o600); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(os.Args[0], "-test.run=^TestReplaceDurableProcessExit$")
			cmd.Env = append(os.Environ(), "CHORD_ATOMICFILE_CRASH_PATH="+path, "CHORD_ATOMICFILE_CRASH_POINT="+point)
			err := cmd.Run()
			if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() != 23 {
				t.Fatalf("child exit: %v", err)
			}
			data, err := os.ReadFile(path)
			if err != nil || string(data) != point {
				t.Fatalf("surviving record: %q, %v", data, err)
			}
			if outcome, err := ReplaceDurable(path, data, 0o600); outcome != Durable || err != nil {
				t.Fatalf("confirm: %s, %v", outcome, err)
			}
		})
	}
}
