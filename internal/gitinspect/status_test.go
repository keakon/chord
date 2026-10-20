package gitinspect

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
	if info.ChangedFiles != 2 {
		t.Fatalf("ChangedFiles = %d, want 2", info.ChangedFiles)
	}
	if info.StagedFiles != 1 {
		t.Fatalf("StagedFiles = %d, want 1", info.StagedFiles)
	}
	if info.ConflictedFiles != 0 {
		t.Fatalf("ConflictedFiles = %d, want 0", info.ConflictedFiles)
	}
}

// Staged and changed are disjoint buckets: a path with staged content counts as
// staged even when the worktree also differs, so a partially staged path can
// never inflate both numbers.
func TestParseGitStatusPorcelainV2PartitionsStagedAndChanged(t *testing.T) {
	info := parseGitStatusPorcelainV2(strings.Join([]string{
		"1 MM N... 100644 100644 100644 abc abc partial.go",
		"1 MD N... 100644 000000 000000 abc 0000 staged_deleted.go",
		"1 A. N... 000000 100644 100644 0000 abc staged_new.go",
		"1 .M N... 100644 100644 100644 abc abc worktree.go",
		"1 .D N... 100644 100644 000000 abc 0000 worktree_deleted.go",
		"2 R. N... 100644 100644 100644 abc abc renamed.go\told.go",
		"2 RM N... 100644 100644 100644 abc abc renamed_dirty.go\told2.go",
		"1 .A N... 000000 000000 100644 0000 0000 intent_to_add.go",
		"? untracked.txt",
	}, "\n") + "\n")

	if info.StagedFiles != 5 {
		t.Fatalf("StagedFiles = %d, want 5 (MM, MD, A., R., RM)", info.StagedFiles)
	}
	if info.ChangedFiles != 4 {
		t.Fatalf("ChangedFiles = %d, want 4 (.M, .D, intent-to-add, untracked)", info.ChangedFiles)
	}
	if info.ConflictedFiles != 0 {
		t.Fatalf("ConflictedFiles = %d, want 0", info.ConflictedFiles)
	}
}

// Unmerged paths are their own bucket: git reports them in a separate section,
// so they must not be counted as staged or changed.
func TestParseGitStatusPorcelainV2CountsUnmergedSeparately(t *testing.T) {
	info := parseGitStatusPorcelainV2("# branch.head main\nu UU N... 100644 100644 100644 100644 aaa bbb conflict.go\n1 M. N... 100644 100644 100644 abc abc staged.go\n")
	if info.ConflictedFiles != 1 {
		t.Fatalf("ConflictedFiles = %d, want 1", info.ConflictedFiles)
	}
	if info.StagedFiles != 1 {
		t.Fatalf("StagedFiles = %d, want 1", info.StagedFiles)
	}
	if info.ChangedFiles != 0 {
		t.Fatalf("ChangedFiles = %d, want 0", info.ChangedFiles)
	}
}

// A record whose XY status field is missing must not be counted at all, rather
// than inflating both buckets the way the old unconditional ChangedFiles++ did.
func TestParseGitStatusPorcelainV2SkipsRecordsWithoutStatusField(t *testing.T) {
	info := parseGitStatusPorcelainV2("1 \n2\n1 .M N... 100644 100644 100644 abc abc file.go\n")
	if info.ChangedFiles != 1 || info.StagedFiles != 0 || info.ConflictedFiles != 0 {
		t.Fatalf("counts = %d/%d/%d, want 1/0/0", info.ChangedFiles, info.StagedFiles, info.ConflictedFiles)
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
