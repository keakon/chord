package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func setHomeEnvForTest(t *testing.T, home string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", home)
		t.Setenv("HOME", home)
		t.Setenv("HOMEDRIVE", "")
		t.Setenv("HOMEPATH", "")
		return
	}
	t.Setenv("HOME", home)
}

func tildePathForTest(rel string) string {
	if rel == "" {
		return "~"
	}
	if runtime.GOOS == "windows" {
		return `~\` + rel
	}
	return "~/" + rel
}

func TestResolveToolPathExpandsTildeHome(t *testing.T) {
	home := t.TempDir()
	setHomeEnvForTest(t, home)

	got, err := resolveToolPath(tildePathForTest(filepath.Join("docs", "note.txt")))
	if err != nil {
		t.Fatalf("resolveToolPath: %v", err)
	}
	want := filepath.Join(home, "docs", "note.txt")
	if got != want {
		t.Fatalf("resolveToolPath = %q, want %q", got, want)
	}
}

func TestIsBlockedDevicePath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("device path blacklist is unix-specific")
	}

	tests := []struct {
		path string
		want bool
	}{
		{path: "/dev/stdin", want: true},
		{path: "/dev/fd/0", want: true},
		{path: "/dev/fd/2", want: true},
		{path: "/dev/fd/3", want: false},
		{path: "/proc/123/fd/0", want: true},
		{path: "/proc/self/fd/0", want: true},
		{path: "/proc/thread-self/fd/2", want: true},
		{path: "/tmp/demo.txt", want: false},
		{path: "relative.txt", want: false},
	}
	for _, tt := range tests {
		if got := isBlockedDevicePath(tt.path); got != tt.want {
			t.Fatalf("isBlockedDevicePath(%q) = %v, want %v", tt.path, got, tt.want)
		}
	}
}

func TestResolveToolPathDoesNotExpandNonLeadingTilde(t *testing.T) {
	home := t.TempDir()
	setHomeEnvForTest(t, home)

	got, err := resolveToolPath(filepath.Join("tmp", "~", "note.txt"))
	if err != nil {
		t.Fatalf("resolveToolPath: %v", err)
	}
	want := filepath.Clean(filepath.Join("tmp", "~", "note.txt"))
	if got != want {
		t.Fatalf("resolveToolPath = %q, want %q", got, want)
	}
}

func TestNormalizeCachePathExpandsTildeToSameAbsolutePath(t *testing.T) {
	home := t.TempDir()
	setHomeEnvForTest(t, home)
	path := filepath.Join(home, "cache.txt")

	got := normalizeCachePath(tildePathForTest("cache.txt"))
	want := filepath.Clean(path)
	if got != want {
		t.Fatalf("normalizeCachePath = %q, want %q", got, want)
	}
}

func TestFileToolConcurrencyPolicyExpandsTilde(t *testing.T) {
	home := t.TempDir()
	setHomeEnvForTest(t, home)
	args := mustMarshal(t, map[string]any{"path": tildePathForTest("demo.txt")})

	policy := fileToolConcurrencyPolicy(args, true)
	want := "file:" + filepath.Join(home, "demo.txt")
	if policy.Resource != want {
		t.Fatalf("Resource = %q, want %q", policy.Resource, want)
	}
	if policy.Mode != ConcurrencyModeRead {
		t.Fatalf("Mode = %q, want %q", policy.Mode, ConcurrencyModeRead)
	}
}

func TestWriteAndReadToolSupportTildePaths(t *testing.T) {
	home := t.TempDir()
	setHomeEnvForTest(t, home)
	target := filepath.Join(home, "nested", "tilde.txt")
	content := "hello\nworld\n"

	if _, err := (WriteTool{}).Execute(context.Background(), mustMarshal(t, map[string]any{
		"path":    tildePathForTest(filepath.Join("nested", "tilde.txt")),
		"content": content,
	})); err != nil {
		t.Fatalf("WriteTool.Execute: %v", err)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("ReadFile target: %v", err)
	}
	if string(data) != content {
		t.Fatalf("written content = %q, want %q", string(data), content)
	}

	out, err := (ReadTool{}).Execute(context.Background(), mustMarshal(t, map[string]any{
		"path": tildePathForTest(filepath.Join("nested", "tilde.txt")),
	}))
	if err != nil {
		t.Fatalf("ReadTool.Execute: %v", err)
	}
	for _, want := range []string{"     1\thello", "     2\tworld"} {
		if !strings.Contains(out, want) {
			t.Fatalf("Read output %q missing %q", out, want)
		}
	}
}

func TestWriteToolRejectsBlockedDevicePath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("device path blacklist is unix-specific")
	}

	_, err := (WriteTool{}).Execute(context.Background(), mustMarshal(t, map[string]any{
		"path":    "/dev/stdout",
		"content": "must not be written",
	}))
	if err == nil {
		t.Fatal("WriteTool.Execute error = nil, want blocked device path error")
	}
	if !strings.Contains(err.Error(), "cannot write blocked device path") {
		t.Fatalf("WriteTool.Execute error = %q, want blocked device path error", err)
	}
}

func TestWriteToolRejectsSymlinkTarget(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink overwrite protection is platform-specific")
	}

	dir := t.TempDir()
	target := filepath.Join(dir, "target.txt")
	link := filepath.Join(dir, "link.txt")
	if err := os.WriteFile(target, []byte("before"), 0o644); err != nil {
		t.Fatalf("WriteFile target: %v", err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	_, err := (WriteTool{}).Execute(context.Background(), mustMarshal(t, map[string]any{
		"path":    link,
		"content": "after",
	}))
	if err == nil {
		t.Fatal("WriteTool.Execute error = nil, want symlink write error")
	}
	data, readErr := os.ReadFile(target)
	if readErr != nil {
		t.Fatalf("ReadFile target: %v", readErr)
	}
	if string(data) != "before" {
		t.Fatalf("target content = %q, want unchanged", string(data))
	}
}

func TestSaveArtifactToolRejectsSymlinkOverwrite(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink overwrite protection is platform-specific")
	}

	sessionDir := t.TempDir()
	artifactDir := filepath.Join(sessionDir, "artifacts", "subagents", "agent", "task")
	if err := os.MkdirAll(artifactDir, 0o755); err != nil {
		t.Fatalf("MkdirAll artifactDir: %v", err)
	}
	target := filepath.Join(sessionDir, "target.txt")
	link := filepath.Join(artifactDir, "report.md")
	if err := os.WriteFile(target, []byte("before"), 0o644); err != nil {
		t.Fatalf("WriteFile target: %v", err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	ctx := WithSessionDir(context.Background(), sessionDir)
	_, err := (SaveArtifactTool{}).Execute(ctx, mustMarshal(t, map[string]any{
		"filename": "report.md",
		"content":  "after",
		"mode":     "overwrite",
	}))
	if err == nil {
		t.Fatal("SaveArtifactTool.Execute error = nil, want symlink write error")
	}
	data, readErr := os.ReadFile(target)
	if readErr != nil {
		t.Fatalf("ReadFile target: %v", readErr)
	}
	if string(data) != "before" {
		t.Fatalf("target content = %q, want unchanged", string(data))
	}
}

func TestEditToolSupportsTildePaths(t *testing.T) {
	home := t.TempDir()
	setHomeEnvForTest(t, home)
	target := filepath.Join(home, "edit.txt")
	if err := os.WriteFile(target, []byte("before\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	out, err := (EditTool{}).Execute(context.Background(), mustMarshal(t, map[string]any{
		"path":       tildePathForTest("edit.txt"),
		"old_string": "before",
		"new_string": "after",
	}))
	if err != nil {
		t.Fatalf("EditTool.Execute: %v", err)
	}
	if !strings.Contains(out, "Replaced 1 occurrence") {
		t.Fatalf("unexpected output %q", out)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != "after\n" {
		t.Fatalf("edited content = %q, want %q", string(data), "after\n")
	}
}

func TestDeleteToolSupportsTildePaths(t *testing.T) {
	home := t.TempDir()
	setHomeEnvForTest(t, home)
	target := filepath.Join(home, "remove.txt")
	if err := os.WriteFile(target, []byte("delete me"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	out, err := (DeleteTool{}).Execute(context.Background(), mustMarshal(t, map[string]any{
		"paths":  []string{tildePathForTest("remove.txt")},
		"reason": "test tilde delete",
	}))
	if err != nil {
		t.Fatalf("DeleteTool.Execute: %v", err)
	}
	if !strings.Contains(out, "Deleted (1):") {
		t.Fatalf("unexpected output %q", out)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("expected file deleted, stat err = %v", err)
	}
}

func TestGlobToolSupportsTildeBasePath(t *testing.T) {
	home := t.TempDir()
	setHomeEnvForTest(t, home)
	if err := os.WriteFile(filepath.Join(home, "found.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	out, err := (GlobTool{}).Execute(context.Background(), mustMarshal(t, map[string]any{
		"path":    "~",
		"pattern": "*.go",
	}))
	if err != nil {
		t.Fatalf("GlobTool.Execute: %v", err)
	}
	if !strings.Contains(out, "found.go") {
		t.Fatalf("Glob output %q missing found.go", out)
	}
}

func TestGrepToolSupportsTildePath(t *testing.T) {
	home := t.TempDir()
	setHomeEnvForTest(t, home)
	target := filepath.Join(home, "notes.txt")
	if err := os.WriteFile(target, []byte("alpha\nbeta match\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	out, err := (GrepTool{}).Execute(context.Background(), mustMarshal(t, map[string]any{
		"path":    tildePathForTest("notes.txt"),
		"pattern": "match",
	}))
	if err != nil {
		t.Fatalf("GrepTool.Execute: %v", err)
	}
	if !strings.Contains(out, fmt.Sprintf("%s:2:beta match", target)) {
		t.Fatalf("Grep output %q missing expected match", out)
	}
}

func TestShellToolSupportsTildeWorkdir(t *testing.T) {
	home := t.TempDir()
	setHomeEnvForTest(t, home)
	marker := filepath.Join(home, "marker.txt")

	out, err := (ShellTool{}).Execute(context.Background(), mustMarshal(t, map[string]any{
		"command":     "pwd",
		"description": "print working directory",
		"workdir":     "~",
		"timeout":     5,
	}))
	if err != nil {
		t.Fatalf("ShellTool.Execute: %v", err)
	}
	if !strings.Contains(strings.TrimSpace(out), home) {
		t.Fatalf("pwd output %q does not include home %q", out, home)
	}

	_, err = (ShellTool{}).Execute(context.Background(), mustMarshal(t, map[string]any{
		"command":     fmt.Sprintf("printf ok > %q", filepath.Base(marker)),
		"description": "write marker file",
		"workdir":     "~",
		"timeout":     5,
	}))
	if err != nil {
		t.Fatalf("ShellTool.Execute write: %v", err)
	}
	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("ReadFile marker: %v", err)
	}
	if string(data) != "ok" {
		t.Fatalf("marker content = %q, want ok", string(data))
	}
}

func TestSpawnToolSupportsTildeWorkdir(t *testing.T) {
	resetSpawnRegistryOnlyForTest(t)
	home := t.TempDir()
	setHomeEnvForTest(t, home)
	ctx := WithSessionDir(context.Background(), t.TempDir())
	marker := filepath.Join(home, "spawn-marker.txt")

	out, err := NewSpawnTool("posix").Execute(ctx, mustMarshal(t, map[string]any{
		"command":     fmt.Sprintf("printf spawned > %q && sleep 1", filepath.Base(marker)),
		"description": "spawn in tilde workdir",
		"workdir":     "~",
		"timeout":     5,
	}))
	if err != nil {
		t.Fatalf("SpawnTool.Execute: %v", err)
	}
	id := extractBackgroundID(t, out)
	defer func() {
		_, _ = (SpawnStopTool{}).Execute(context.Background(), mustMarshal(t, map[string]any{"id": id}))
	}()
	waitForFile(t, marker)
	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("ReadFile marker: %v", err)
	}
	if string(data) != "spawned" {
		t.Fatalf("marker content = %q, want spawned", string(data))
	}
}

func TestHandoffToolSupportsTildePlanPath(t *testing.T) {
	home := t.TempDir()
	setHomeEnvForTest(t, home)
	planPath := filepath.Join(home, "plan.md")
	if err := os.WriteFile(planPath, []byte("# plan\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	out, err := (HandoffTool{}).Execute(context.Background(), mustMarshal(t, map[string]any{
		"plan_path": tildePathForTest("plan.md"),
	}))
	if err != nil {
		t.Fatalf("HandoffTool.Execute: %v", err)
	}
	var result map[string]string
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("Unmarshal output: %v", err)
	}
	if result["plan_path"] != planPath {
		t.Fatalf("plan_path = %q, want %q", result["plan_path"], planPath)
	}
}

func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		_, err := os.Stat(path)
		if err == nil {
			return
		}
		if !os.IsNotExist(err) {
			t.Fatalf("Stat(%q): %v", path, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for file %q", path)
}
