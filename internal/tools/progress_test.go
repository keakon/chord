package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

type progressRecorder struct {
	snapshots []ToolProgressSnapshot
}

func (r *progressRecorder) ReportToolProgress(progress ToolProgressSnapshot) {
	r.snapshots = append(r.snapshots, progress)
}

func TestGrepToolReportsScannedFileProgress(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("alpha\nneedle\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(a.txt): %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("beta\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(b.txt): %v", err)
	}

	recorder := &progressRecorder{}
	ctx := WithToolProgressReporter(context.Background(), recorder)
	out, err := (GrepTool{}).Execute(ctx, mustMarshal(t, map[string]any{
		"pattern": "needle",
		"path":    dir,
	}))
	if err != nil {
		t.Fatalf("Execute(): %v", err)
	}
	if out == "" {
		t.Fatal("expected grep output")
	}
	if len(recorder.snapshots) == 0 {
		t.Fatal("expected grep progress snapshots")
	}
	last := recorder.snapshots[len(recorder.snapshots)-1]
	if last.Label != "files" {
		t.Fatalf("last progress label = %q, want files", last.Label)
	}
	if last.Current != 2 {
		t.Fatalf("last progress current = %d, want 2", last.Current)
	}
}

func TestDeleteToolReportsProcessedPathProgress(t *testing.T) {
	dir := t.TempDir()
	fileA := filepath.Join(dir, "a.txt")
	fileB := filepath.Join(dir, "b.txt")
	missing := filepath.Join(dir, "missing.txt")
	if err := os.WriteFile(fileA, []byte("a"), 0o644); err != nil {
		t.Fatalf("WriteFile(a): %v", err)
	}
	if err := os.WriteFile(fileB, []byte("b"), 0o644); err != nil {
		t.Fatalf("WriteFile(b): %v", err)
	}

	recorder := &progressRecorder{}
	ctx := WithToolProgressReporter(context.Background(), recorder)
	out, err := (DeleteTool{}).Execute(ctx, mustMarshal(t, map[string]any{
		"paths":  []string{fileA, fileB, missing},
		"reason": "cleanup",
	}))
	if err != nil {
		t.Fatalf("Execute(): %v", err)
	}
	if out == "" {
		t.Fatal("expected delete output")
	}
	if len(recorder.snapshots) == 0 {
		t.Fatal("expected delete progress snapshots")
	}
	last := recorder.snapshots[len(recorder.snapshots)-1]
	if last.Label != "paths" {
		t.Fatalf("last progress label = %q, want paths", last.Label)
	}
	if last.Current != 3 || last.Total != 3 {
		t.Fatalf("last progress = (%d/%d), want (3/3)", last.Current, last.Total)
	}
}
