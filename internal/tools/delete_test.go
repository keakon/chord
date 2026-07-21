package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDeleteToolPathsDescriptionWarnsAgainstGuessing(t *testing.T) {
	props := (DeleteTool{}).Parameters()["properties"].(map[string]any)
	desc := props["paths"].(map[string]any)["description"].(string)
	for _, want := range []string{"verified files or symlinks", "Do not guess paths for destructive operations"} {
		if !strings.Contains(desc, want) {
			t.Fatalf("paths description missing %q: %q", want, desc)
		}
	}
}

func TestDeleteToolDeletesFileAndInvalidatesCaches(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "remove.txt")
	content := []byte("hello\nworld\n")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	clearEncodingCaches()
	setPathCache(path, pathCacheEntry{Size: int64(len(content)), ModTime: time.Now().UnixNano(), Hash: cacheKeyForBytes(content)})

	args := deleteArgsJSON(t, []string{path}, "remove test file")
	got, err := (DeleteTool{}).Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("DeleteTool.Execute: %v", err)
	}
	if !strings.Contains(got, "Deleted (1):") {
		t.Fatalf("unexpected result %q", got)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected file removed, stat err = %v", err)
	}
	if _, ok := getPathCache(path); ok {
		t.Fatal("expected path cache entry removed")
	}
}

func TestDeleteToolReportsCanonicalAuditPaths(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "remove.txt")
	if err := os.WriteFile(path, []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	collector := &DeleteAuditCollector{}
	ctx := WithDeleteAuditSink(context.Background(), collector)
	got, err := (DeleteTool{BaseDir: dir}).Execute(ctx, mustRelativeDeleteArgs(t, "remove.txt"))
	if err != nil {
		t.Fatalf("DeleteTool.Execute: %v", err)
	}
	if !strings.Contains(got, "- remove.txt") {
		t.Fatalf("result = %q, want display path", got)
	}
	audit, ok := collector.Audit()
	if !ok || len(audit.Deleted) != 1 || audit.Deleted[0] != path {
		t.Fatalf("audit = %#v, ok=%v, want canonical deleted path %q", audit, ok, path)
	}
}

func TestDeleteToolReportsAuditWhenExecutionStops(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "first.txt")
	second := filepath.Join(dir, "second.txt")
	for _, path := range []string{first, second} {
		if err := os.WriteFile(path, []byte("hello\n"), 0o644); err != nil {
			t.Fatalf("WriteFile %s: %v", path, err)
		}
	}
	collector := &DeleteAuditCollector{}
	ctx, cancel := context.WithCancel(WithDeleteAuditSink(context.Background(), collector))
	cancel()
	_, err := (DeleteTool{BaseDir: dir}).Execute(ctx, deleteArgsJSON(t, []string{"first.txt", "second.txt"}, "cleanup"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("DeleteTool.Execute error = %v, want context canceled", err)
	}
	audit, ok := collector.Audit()
	if !ok || len(audit.NotAttempted) != 2 || audit.NotAttempted[0] != first || audit.NotAttempted[1] != second {
		t.Fatalf("audit = %#v, ok=%v, want canonical not-attempted paths", audit, ok)
	}
}

func TestDeleteToolDeletesSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.txt")
	link := filepath.Join(dir, "link.txt")
	if err := os.WriteFile(target, []byte("hello"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("Symlink: %v", err)
	}
	args := deleteArgsJSON(t, []string{link}, "remove symlink")
	got, err := (DeleteTool{}).Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("DeleteTool.Execute: %v", err)
	}
	if !strings.Contains(got, "Deleted (1):") {
		t.Fatalf("unexpected result %q", got)
	}
	if _, err := os.Lstat(link); !os.IsNotExist(err) {
		t.Fatalf("expected symlink removed, lstat err = %v", err)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("expected target preserved, stat err = %v", err)
	}
}

func TestDeleteToolMissingPathIsReportedAsAlreadyAbsent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "absent.txt")
	args := deleteArgsJSON(t, []string{path}, "cleanup")
	got, err := (DeleteTool{}).Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("DeleteTool.Execute: %v", err)
	}
	if !strings.Contains(got, "Already absent (1):") {
		t.Fatalf("unexpected result %q", got)
	}
}

func TestDeleteToolRejectsDirectories(t *testing.T) {
	dir := t.TempDir()
	args := deleteArgsJSON(t, []string{dir}, "cleanup")
	_, err := (DeleteTool{}).Execute(context.Background(), args)
	if err == nil || !strings.Contains(err.Error(), "failed before execution") {
		t.Fatalf("err = %v, want pre-execution failure", err)
	}
}

func TestDeleteToolDeleteMultipleFiles(t *testing.T) {
	dir := t.TempDir()
	paths := []string{filepath.Join(dir, "a.txt"), filepath.Join(dir, "b.txt")}
	for _, path := range paths {
		if err := os.WriteFile(path, []byte(path), 0o644); err != nil {
			t.Fatalf("WriteFile(%s): %v", path, err)
		}
	}
	args := deleteArgsJSON(t, []string{paths[1], paths[0]}, "remove temp files")
	got, err := (DeleteTool{}).Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("DeleteTool.Execute: %v", err)
	}
	if !strings.Contains(got, "Deleted (2):") {
		t.Fatalf("unexpected result %q", got)
	}
	for _, path := range paths {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("expected file removed, stat err = %v", err)
		}
	}
}

func deleteArgsJSON(t *testing.T, paths []string, reason string) json.RawMessage {
	t.Helper()
	args, err := json.Marshal(map[string]any{"paths": paths, "reason": reason})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	return args
}

func mustRelativeDeleteArgs(t *testing.T, path string) json.RawMessage {
	t.Helper()
	return deleteArgsJSON(t, []string{path}, "remove test file")
}
