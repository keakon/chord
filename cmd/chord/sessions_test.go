package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func writeTestSessionDir(t *testing.T, lines ...string) string {
	t.Helper()
	dir := t.TempDir()
	content := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(dir, "main.jsonl"), []byte(content), 0o600); err != nil {
		t.Fatalf("write main.jsonl: %v", err)
	}
	return dir
}

func runSessionsProject(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := newSessionsCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return buf.String(), err
}

func TestSessionsProjectStdout(t *testing.T) {
	dir := writeTestSessionDir(t,
		`{"role":"user","content":"first request"}`,
		`{"role":"assistant","content":"working","tool_calls":[{"id":"call-1","name":"read","args":{"path":"sample.txt"}}]}`,
		`{"role":"tool","content":"file content","tool_call_id":"call-1","tool_status":"success"}`,
		`{"role":"user","content":"second request"}`,
		`{"role":"assistant","content":"done"}`,
	)
	out, err := runSessionsProject(t, "project", "--session-dir", dir)
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 JSONL turns, got %d: %q", len(lines), out)
	}
	var first map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatalf("parse turn: %v", err)
	}
	if first["trigger"] != "user_message" {
		t.Fatalf("trigger = %v", first["trigger"])
	}
	// Read-only: the session directory gains no new files.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "main.jsonl" {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("session dir changed: %v", names)
	}
}

func TestSessionsProjectOutFile(t *testing.T) {
	dir := writeTestSessionDir(t,
		`{"role":"user","content":"hello"}`,
		`{"role":"assistant","content":"hi"}`,
	)
	outPath := filepath.Join(t.TempDir(), "projection.jsonl")
	if _, err := runSessionsProject(t, "project", "ignored-id", "--session-dir", dir, "--out", outPath); err != nil {
		t.Fatalf("project: %v", err)
	}
	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read out: %v", err)
	}
	if lines := strings.Split(strings.TrimSpace(string(data)), "\n"); len(lines) != 1 {
		t.Fatalf("want 1 turn, got %q", data)
	}
}

func TestSessionsProjectRejectsSessionAlias(t *testing.T) {
	dir := writeTestSessionDir(t, `{"role":"user","content":"hello"}`)
	alias := filepath.Join(t.TempDir(), "alias.jsonl")
	if err := os.Link(filepath.Join(dir, "main.jsonl"), alias); err != nil {
		t.Fatalf("link: %v", err)
	}
	before, err := os.ReadFile(filepath.Join(dir, "main.jsonl"))
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	linkDir := filepath.Join(t.TempDir(), "linkdir")
	if err := os.Symlink(dir, linkDir); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	cycleA := filepath.Join(t.TempDir(), "cycle-a")
	cycleB := filepath.Join(t.TempDir(), "cycle-b")
	if err := os.Symlink(cycleB, cycleA); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	if err := os.Symlink(cycleA, cycleB); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	dangling := filepath.Join(t.TempDir(), "dangling.jsonl")
	if err := os.Symlink(filepath.Join(dir, "created.jsonl"), dangling); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	// A ".." after the link survives the write (MkdirAll/WriteFile do not
	// clean) but filepath.Join would erase it, so the path is spelled out.
	dotdot := linkDir + "/../" + filepath.Base(dir) + "/evil.jsonl"
	for _, out := range []string{
		filepath.Join(dir, "out.jsonl"),
		filepath.Join(linkDir, "out.jsonl"),
		filepath.Join(linkDir, "sub", "out.jsonl"),
		alias,
		dangling,
		dotdot,
		cycleA,
	} {
		if outText, err := runSessionsProject(t, "project", "ignored-id", "--session-dir", dir, "--out", out); err == nil {
			t.Fatalf("project --out %s must be refused, wrote %q", out, outText)
		}
	}
	after, err := os.ReadFile(filepath.Join(dir, "main.jsonl"))
	if err != nil {
		t.Fatalf("read source after: %v", err)
	}
	if string(after) != string(before) {
		t.Fatalf("source transcript changed:\n%s", after)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir session dir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "main.jsonl" {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("refused writes still changed the session dir: %v", names)
	}

	// The same ".." spelling is fine when it walks out of the session.
	outside := linkDir + "/../outside.jsonl"
	if _, err := runSessionsProject(t, "project", "ignored-id", "--session-dir", dir, "--out", outside); err != nil {
		t.Fatalf("project --out %s: %v", outside, err)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("outside projection missing: %v", err)
	}
}

func TestSessionsProjectEmptySessionErrors(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.jsonl"), []byte(""), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := runSessionsProject(t, "project", "--session-dir", dir); err == nil {
		t.Fatal("empty session must fail instead of emitting empty output")
	}
}

func TestSessionsProjectRejectsNestedSessionHardLink(t *testing.T) {
	dir := writeTestSessionDir(t, `{"role":"user","content":"hello"}`)
	source := filepath.Join(dir, "subagents", "worker", "main.jsonl")
	if err := os.MkdirAll(filepath.Dir(source), 0o700); err != nil {
		t.Fatal(err)
	}
	before := []byte("worker transcript")
	if err := os.WriteFile(source, before, 0o600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "projection.jsonl")
	if err := os.Link(source, alias); err != nil {
		t.Fatal(err)
	}
	if _, err := runSessionsProject(t, "project", "--session-dir", dir, "--out", alias); err == nil {
		t.Fatal("nested session hard link must be rejected")
	}
	after, err := os.ReadFile(source)
	if err != nil || !bytes.Equal(after, before) {
		t.Fatalf("source changed: %q, %v", after, err)
	}
}

func TestSessionsProjectRequiresSource(t *testing.T) {
	for _, args := range [][]string{{"project"}, {"project", "--session-dir", ""}, {"project", "one", "two"}} {
		if _, err := runSessionsProject(t, args...); err == nil {
			t.Fatalf("invalid source accepted: %v", args)
		}
	}
}

func TestSessionsProjectOutputOwnerOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permissions")
	}
	for _, mode := range []os.FileMode{0, 0o600, 0o644} {
		t.Run(mode.String(), func(t *testing.T) {
			dir := writeTestSessionDir(t, `{"role":"user","content":"hello"}`)
			out := filepath.Join(t.TempDir(), "projection.jsonl")
			if mode != 0 {
				if err := os.WriteFile(out, []byte("previous"), mode); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(out, mode); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := runSessionsProject(t, "project", "--session-dir", dir, "--out", out); err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(out)
			if err != nil {
				t.Fatal(err)
			}
			if got := info.Mode().Perm(); got != 0o600 {
				t.Fatalf("output permissions = %o, want 600", got)
			}
		})
	}
}
