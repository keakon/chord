package gitinspect

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestParseGitStatusPorcelainV2(t *testing.T) {
	info := parseGitStatusPorcelainV2("# branch.oid 1234567890abcdef\n# branch.head main\n# branch.upstream origin/main\n# branch.ab +14 -2\n1 M. N... 100644 100644 100644 abc abc staged.go\n1 .M N... 100644 100644 100644 abc abc file.go\n? new.txt\n")
	if info.Branch != "main" {
		t.Fatalf("Branch = %q, want main", info.Branch)
	}
	if info.Commit != "1234567890ab" {
		t.Fatalf("Commit = %q, want short oid", info.Commit)
	}
	if info.Ahead != 14 || info.Behind != 2 {
		t.Fatalf("ahead/behind = %d/%d, want 14/2", info.Ahead, info.Behind)
	}
	if info.ChangedFiles != 3 {
		t.Fatalf("ChangedFiles = %d, want 3", info.ChangedFiles)
	}
	if info.StagedFiles != 1 {
		t.Fatalf("StagedFiles = %d, want 1", info.StagedFiles)
	}
}

func TestCountGitStashEntries(t *testing.T) {
	got := countGitStashEntries("stash@{0}\nstash@{1}\n\n")
	if got != 2 {
		t.Fatalf("stash entries = %d, want 2", got)
	}
}

func TestCollectDistinguishesMissingMarkerAndMissingGit(t *testing.T) {
	t.Setenv("PATH", "")
	root := t.TempDir()
	if result := Collect(root); result.Disable || result.Info.Present {
		t.Fatalf("directory without Git metadata: %+v", result)
	}
	if err := os.WriteFile(filepath.Join(root, ".git"), []byte("gitdir: elsewhere\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if result := Collect(root); !result.Disable || result.Info.Present {
		t.Fatalf("missing Git executable: %+v", result)
	}
}

func TestFilesPreservesExecutableAndCancellationErrors(t *testing.T) {
	t.Setenv("PATH", "")
	root := t.TempDir()
	for _, tracked := range []bool{false, true} {
		out, err := Files(t.Context(), root, tracked)
		if out != "" || !errors.Is(err, exec.ErrNotFound) {
			t.Fatalf("tracked=%v: missing executable produced %q, %v", tracked, out, err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		out, err = Files(ctx, root, tracked)
		if out != "" || !errors.Is(err, context.Canceled) {
			t.Fatalf("tracked=%v: cancellation produced %q, %v", tracked, out, err)
		}
	}
}
