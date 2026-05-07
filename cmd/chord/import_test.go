package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewImportCmd_AllowsIDWithoutFile(t *testing.T) {
	cmd := newImportCmd()
	if err := cmd.Args(cmd, []string{"codex"}); err != nil {
		t.Fatalf("Args rejected single source arg: %v", err)
	}
}

func TestNewImportCmd_RejectsNoInputAndNoID(t *testing.T) {
	cmd := newImportCmd()
	_ = cmd.Flags().Set("project", t.TempDir())
	if err := cmd.RunE(cmd, []string{"codex"}); err == nil {
		t.Fatal("expected error when neither file nor --id is provided")
	}
}

func TestNewImportCmd_SucceedsWithIDAndRoot(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("CHORD_STATE_DIR", stateDir)
	t.Setenv("CHORD_SESSIONS_DIR", "")
	root := t.TempDir()
	rollout := filepath.Join(root, "2026", "05", "07", "rollout-1.jsonl")
	if err := os.MkdirAll(filepath.Dir(rollout), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(rollout, []byte(`{"timestamp":"2026-01-01T00:00:00Z","item":{"session_id":"sess-1","role":"user","content":"hi"}}`+"\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	cmd := newImportCmd()
	_ = cmd.Flags().Set("project", t.TempDir())
	_ = cmd.Flags().Set("id", "sess-1")
	_ = cmd.Flags().Set("root", root)

	out, err := captureStdout(t, func() error {
		return cmd.RunE(cmd, []string{"codex"})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "Imported codex session") {
		t.Fatalf("unexpected output: %q", out)
	}
}
