package worktree

import (
	"reflect"
	"testing"
)

func TestParseWorktreeListPorcelain_Basic(t *testing.T) {
	in := []byte("worktree /repo\nHEAD abc123\nbranch refs/heads/main\n\n" +
		"worktree /repo/.git/worktrees/foo\nHEAD def456\nbranch refs/heads/chord/foo\n")
	got := parseWorktreeListPorcelain(in)
	want := []porcelainEntry{
		{Path: "/repo", Head: "abc123", Branch: "refs/heads/main"},
		{Path: "/repo/.git/worktrees/foo", Head: "def456", Branch: "refs/heads/chord/foo"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestParseWorktreeListPorcelain_Detached(t *testing.T) {
	in := []byte("worktree /repo\nHEAD abc123\ndetached\n")
	got := parseWorktreeListPorcelain(in)
	if len(got) != 1 || !got[0].Detached || got[0].Branch != "" {
		t.Errorf("got %+v, want one detached entry", got)
	}
}

func TestParseWorktreeListPorcelain_LockedWithReason(t *testing.T) {
	in := []byte("worktree /repo/wt\nHEAD x\nbranch refs/heads/foo\nlocked manual\n")
	got := parseWorktreeListPorcelain(in)
	if len(got) != 1 || got[0].Locked != "manual" {
		t.Errorf("got %+v, want locked=manual", got)
	}
}

func TestParseWorktreeListPorcelain_EmptyAndBare(t *testing.T) {
	if got := parseWorktreeListPorcelain(nil); got != nil {
		t.Errorf("nil input got %+v, want nil", got)
	}
	in := []byte("worktree /repo.git\nbare\n")
	got := parseWorktreeListPorcelain(in)
	if len(got) != 1 || !got[0].Bare {
		t.Errorf("got %+v, want one bare entry", got)
	}
}

func TestShortBranch(t *testing.T) {
	if got := shortBranch("refs/heads/chord/foo"); got != "chord/foo" {
		t.Errorf("got %q, want chord/foo", got)
	}
	if got := shortBranch("chord/foo"); got != "chord/foo" {
		t.Errorf("got %q, want chord/foo", got)
	}
}
