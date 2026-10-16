package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSessionsListStorageAndWorktreeScope(t *testing.T) {
	withTestStateDir(t)
	root := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git: %v: %s", err, out)
		}
	}
	git("init", "-q")
	// The repository is created here rather than by testgit, so it must opt out
	// of the developer's global core.hooksPath itself; a hook that needs a
	// review remote fails every commit made inside it.
	git("config", "core.hooksPath", "")
	// The developer's global commit.gpgsign can also make every commit fail
	// (missing signing key), so the test repository opts out of that too.
	git("config", "commit.gpgsign", "false")
	git("-c", "user.name=Sample", "-c", "user.email=sample@example.invalid", "commit", "--allow-empty", "-qm", "initial")
	linked := filepath.Join(t.TempDir(), "linked")
	git("worktree", "add", "--detach", linked)
	t.Chdir(root)
	locator, err := startupPathLocator()
	if err != nil {
		t.Fatal(err)
	}
	project, err := locator.LocateProject(resolveContentRoot(context.Background(), root))
	if err != nil {
		t.Fatal(err)
	}
	for i, id := range []string{"20261001000000000", "20261002000000000"} {
		dir := filepath.Join(project.ProjectSessionsDir, id)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		file := filepath.Join(dir, "main.jsonl")
		if err := os.WriteFile(file, []byte("{\"role\":\"user\",\"content\":\"private sample\"}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		stamp := time.Date(2026, 10, 1+i, 0, 0, 0, 0, time.UTC)
		if err := os.Chtimes(file, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(root, ".chord", "sessions", "legacy"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{root, linked} {
		var output bytes.Buffer
		cmd := newSessionsListCmd()
		cmd.SetOut(&output)
		cmd.SetArgs([]string{"--json", "--limit", "1", "-d", dir})
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
		var report sessionListReport
		if err := json.Unmarshal(output.Bytes(), &report); err != nil {
			t.Fatal(err)
		}
		if report.SchemaVersion != 1 || report.Workspace != project.CanonicalRoot || len(report.Sessions) != 1 || report.Sessions[0].ID != "20261002000000000" {
			t.Fatalf("%s: %+v", dir, report)
		}
		if strings.Contains(output.String(), "private sample") || strings.Contains(output.String(), "legacy") {
			t.Fatal(output.String())
		}
		output.Reset()
		cmd = newSessionsListCmd()
		cmd.SetOut(&output)
		cmd.SetArgs([]string{"--directory", dir})
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(output.String(), "20261001000000000") {
			t.Fatal(output.String())
		}
	}
}
func TestSessionsListEmptyAndInvalidLimit(t *testing.T) {
	withTestStateDir(t)
	t.Chdir(t.TempDir())
	for _, limit := range []string{"0", "1001"} {
		cmd := newSessionsListCmd()
		cmd.SetArgs([]string{"--limit", limit})
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		if err := cmd.Execute(); err == nil {
			t.Fatal("accepted invalid limit")
		}
	}
	var output bytes.Buffer
	cmd := newSessionsListCmd()
	cmd.SetOut(&output)
	cmd.SetArgs([]string{"--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), `"sessions":[]`) {
		t.Fatal(output.String())
	}
}

func TestSessionsListRejectsInvalidDirectory(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, []byte("sample"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{filepath.Join(root, "missing"), file} {
		cmd := newSessionsListCmd()
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		cmd.SetArgs([]string{"--json", "-d", dir})
		if err := cmd.Execute(); err == nil {
			t.Fatal("accepted invalid directory")
		}
	}
}
