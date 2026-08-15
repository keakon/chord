package recovery

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestSessionMeta_IsZero(t *testing.T) {
	if !(SessionMeta{}).IsZero() {
		t.Errorf("zero value reports non-zero")
	}
	if (SessionMeta{ForkedFrom: "x"}).IsZero() {
		t.Errorf("ForkedFrom only reports zero")
	}
	if (SessionMeta{Title: "custom title"}).IsZero() {
		t.Errorf("Title only reports zero")
	}
	if (SessionMeta{WorktreeName: "feat"}).IsZero() {
		t.Errorf("WorktreeName only reports zero")
	}
	if (SessionMeta{ImportedFrom: &ImportMeta{Source: "opencode", ImportedAt: time.Now()}}).IsZero() {
		t.Errorf("ImportedFrom only reports zero")
	}
	if (SessionMeta{IsMainWorktree: true}).IsZero() {
		t.Errorf("IsMainWorktree=true reports zero")
	}
	if (SessionMeta{MCPEnabledServers: []string{"manual-search"}}).IsZero() {
		t.Errorf("MCPEnabledServers only reports zero")
	}
}

func TestNormalizeMCPEnabledServers(t *testing.T) {
	if got := NormalizeMCPEnabledServers(nil); got != nil {
		t.Fatalf("nil input -> %v, want nil", got)
	}
	if got := NormalizeMCPEnabledServers([]string{" b ", "a", "b", "", " a"}); !slices.Equal(got, []string{"a", "b"}) {
		t.Fatalf("normalize -> %v, want [a b]", got)
	}
}

func TestSessionMeta_MCPEnabledServersRoundTrip(t *testing.T) {
	dir := t.TempDir()
	in := SessionMeta{
		Title:             "keep me",
		MCPEnabledServers: []string{" manual-search ", "manual-files", "manual-search", ""},
	}
	if err := SaveSessionMeta(dir, in); err != nil {
		t.Fatalf("SaveSessionMeta: %v", err)
	}
	out, err := LoadSessionMeta(dir)
	if err != nil {
		t.Fatalf("LoadSessionMeta: %v", err)
	}
	if out == nil || out.Title != "keep me" {
		t.Fatalf("round-trip lost unrelated field: %+v", out)
	}
	if !slices.Equal(out.MCPEnabledServers, []string{"manual-files", "manual-search"}) {
		t.Fatalf("round-trip lost MCPEnabledServers: %+v", out.MCPEnabledServers)
	}
}

func TestSetMCPEnabledServersPreservesOtherFields(t *testing.T) {
	dir := t.TempDir()
	if err := SaveSessionMeta(dir, SessionMeta{Title: "keep me", ForkedFrom: "src"}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := SetMCPEnabledServers(dir, []string{" b ", "a", "b"}); err != nil {
		t.Fatalf("SetMCPEnabledServers: %v", err)
	}
	got, err := LoadSessionMeta(dir)
	if err != nil {
		t.Fatalf("LoadSessionMeta: %v", err)
	}
	if got == nil {
		t.Fatal("nil meta after update")
	}
	if got.Title != "keep me" || got.ForkedFrom != "src" {
		t.Fatalf("update clobbered unrelated fields: %+v", got)
	}
	if !slices.Equal(got.MCPEnabledServers, []string{"a", "b"}) {
		t.Fatalf("MCPEnabledServers = %v, want [a b]", got.MCPEnabledServers)
	}
	if err := SetMCPEnabledServers(dir, nil); err != nil {
		t.Fatalf("clear: %v", err)
	}
	got, err = LoadSessionMeta(dir)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got == nil || len(got.MCPEnabledServers) != 0 {
		t.Fatalf("clear left MCPEnabledServers = %v", got.MCPEnabledServers)
	}
	if got.Title != "keep me" {
		t.Fatalf("clear clobbered title: %+v", got)
	}
}

func TestSaveLoadSessionMeta_RoundTrip_Worktree(t *testing.T) {
	dir := t.TempDir()
	in := SessionMeta{
		RepoID:         "deadbeef00000000",
		RepoRoot:       "/repo/main",
		WorktreeName:   "feat-a",
		WorktreeBranch: "chord/feat-a",
		WorktreePath:   "/state/wt/feat-a",
	}
	if err := SaveSessionMeta(dir, in); err != nil {
		t.Fatalf("SaveSessionMeta: %v", err)
	}
	out, err := LoadSessionMeta(dir)
	if err != nil {
		t.Fatalf("LoadSessionMeta: %v", err)
	}
	if out == nil {
		t.Fatalf("LoadSessionMeta returned nil for worktree-only meta — Load should treat any populated worktree field as meaningful")
	}
	if out.WorktreeName != in.WorktreeName || out.RepoID != in.RepoID {
		t.Errorf("round-trip lost fields: got %+v", out)
	}
	if out.ForkedFrom != "" {
		t.Errorf("ForkedFrom unexpectedly set: %q", out.ForkedFrom)
	}
}

func TestLoadSessionMeta_ForkedFromOnly(t *testing.T) {
	dir := t.TempDir()
	if err := SaveSessionMeta(dir, SessionMeta{ForkedFrom: "abc"}); err != nil {
		t.Fatalf("SaveSessionMeta: %v", err)
	}
	got, err := LoadSessionMeta(dir)
	if err != nil {
		t.Fatalf("LoadSessionMeta: %v", err)
	}
	if got == nil || got.ForkedFrom != "abc" {
		t.Fatalf("ForkedFrom-only round-trip failed: %+v", got)
	}
}

func TestLoadSessionMeta_TitleOnly(t *testing.T) {
	dir := t.TempDir()
	if err := SaveSessionMeta(dir, SessionMeta{Title: "custom title"}); err != nil {
		t.Fatalf("SaveSessionMeta: %v", err)
	}
	got, err := LoadSessionMeta(dir)
	if err != nil {
		t.Fatalf("LoadSessionMeta: %v", err)
	}
	if got == nil || got.Title != "custom title" {
		t.Fatalf("Title-only round-trip failed: %+v", got)
	}
}

func TestLoadSessionMeta_Missing(t *testing.T) {
	dir := t.TempDir()
	got, err := LoadSessionMeta(dir)
	if err != nil {
		t.Fatalf("LoadSessionMeta on missing dir: %v", err)
	}
	if got != nil {
		t.Errorf("missing meta returned %+v, want nil", got)
	}
}

func TestLoadSessionMeta_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, sessionMetaFile)
	if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
		t.Fatalf("seed empty meta: %v", err)
	}
	got, err := LoadSessionMeta(dir)
	if err != nil {
		t.Fatalf("LoadSessionMeta: %v", err)
	}
	if got != nil {
		t.Errorf("empty meta {} returned %+v, want nil", got)
	}
}
