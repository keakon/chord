package recovery

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestScanAppliedCompactionHistoryRejectsUnsafeFiles(t *testing.T) {
	for _, kind := range []string{"status", "snapshot", "directory", "oversized status"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			writeCompactionFixtureStatus(t, dir, 1, CompactionHistoryStatusApplied)
			writeCompactionFixtureJSONL(t, dir, 1, compactionFixtureMessages(t, 1))
			writeCompactionFixtureStatus(t, dir, 2, CompactionHistoryStatusApplied)
			switch kind {
			case "status":
				writeCompactionFixtureJSONL(t, dir, 2, compactionFixtureMessages(t, 1))
				if err := os.Remove(filepath.Join(dir, compactionHistoryStatusName(2))); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(dir, compactionHistoryStatusName(1)), filepath.Join(dir, compactionHistoryStatusName(2))); err != nil {
					t.Fatal(err)
				}
			case "snapshot":
				if err := os.Symlink(filepath.Join(dir, compactionPreCompressName(1)), filepath.Join(dir, compactionPreCompressName(2))); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Mkdir(filepath.Join(dir, compactionPreCompressName(2)), 0o700); err != nil {
					t.Fatal(err)
				}
			case "oversized status":
				writeCompactionFixtureJSONL(t, dir, 2, compactionFixtureMessages(t, 1))
				data := `{"status":"applied"}` + strings.Repeat(" ", 1<<20) + "invalid"
				if err := os.WriteFile(filepath.Join(dir, compactionHistoryStatusName(2)), []byte(data), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			snapshots, issues, err := ScanAppliedCompactionHistory(context.Background(), "", dir)
			if err != nil || len(snapshots) != 1 || snapshots[0].Index != 1 {
				t.Fatalf("scan = %v, %v", snapshots, err)
			}
			if len(issues) != 1 || issues[0].Index != 2 || !errors.Is(issues[0].Err, ErrCompactionSnapshotInvalid) {
				t.Fatalf("issues = %v", issues)
			}
		})
	}
}

func TestScanAppliedCompactionHistorySkipsUnappliedAndCancels(t *testing.T) {
	dir := t.TempDir()
	for _, index := range []int{1, 2, 3, 7} {
		writeCompactionFixtureJSONL(t, dir, index, compactionFixtureMessages(t, 1))
	}
	writeCompactionFixtureStatus(t, dir, 1, "pending_apply")
	for _, index := range []int{3, 7} {
		writeCompactionFixtureStatus(t, dir, index, CompactionHistoryStatusApplied)
	}
	snapshots, issues, err := ScanAppliedCompactionHistory(context.Background(), "", dir)
	if err != nil || len(issues) != 0 || len(snapshots) != 2 || snapshots[0].Index != 3 || snapshots[1].Index != 7 {
		t.Fatalf("scan = %v, %v, %v", snapshots, issues, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err = ScanAppliedCompactionHistory(ctx, "", dir)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled scan = %v", err)
	}
}

func TestCompactionSnapshotErrorNamesFileAndRecord(t *testing.T) {
	dir := t.TempDir()
	writeCompactionFixtureStatus(t, dir, 3, CompactionHistoryStatusApplied)
	writeCompactionFixtureJSONL(t, dir, 3, compactionFixtureMessages(t, 1)+`{"role":`)
	_, err := LoadAppliedCompactionSnapshot(context.Background(), "", dir, 3)
	if !errors.Is(err, ErrCompactionSnapshotParse) || !strings.Contains(err.Error(), compactionPreCompressName(3)) || !strings.Contains(err.Error(), "record 2") {
		t.Fatalf("parse error lost location: %v", err)
	}
	writeCompactionFixtureStatus(t, dir, 7, CompactionHistoryStatusApplied)
	_, err = LoadAppliedCompactionSnapshot(context.Background(), "", dir, 7)
	if !errors.Is(err, os.ErrNotExist) || !errors.Is(err, ErrCompactionSnapshotUnavailable) {
		t.Fatalf("missing file error lost cause: %v", err)
	}
}

type cancellingSnapshotReader struct {
	cancel context.CancelFunc
	reads  int
}

func (r *cancellingSnapshotReader) Read(p []byte) (int, error) {
	r.reads++
	r.cancel()
	return copy(p, strings.Repeat("x", len(p))), nil
}

func TestDecodeSealedSnapshotCancelsInsideLongRecord(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reader := &cancellingSnapshotReader{cancel: cancel}
	_, err := decodeSealedSnapshot(ctx, reader, 1<<20)
	if !errors.Is(err, context.Canceled) || reader.reads != 1 {
		t.Fatalf("cancelled decode = %v, reads=%d", err, reader.reads)
	}
	_, err = decodeSealedSnapshot(context.Background(), io.MultiReader(strings.NewReader(""), failingSnapshotReader{}), 1)
	if !errors.Is(err, io.ErrUnexpectedEOF) || !errors.Is(err, ErrCompactionSnapshotRead) {
		t.Fatalf("read error lost cause: %v", err)
	}
}

type failingSnapshotReader struct{}

func (failingSnapshotReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
