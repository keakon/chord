package recovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
)

func writeCompactionFixtureStatus(t *testing.T, dir string, index int, status string) {
	t.Helper()
	data, err := json.Marshal(map[string]any{"version": 1, "status": status})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("history-%d.status.json", index)), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeCompactionFixtureJSONL(t *testing.T, dir string, index int, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("main.pre-compress-%d.jsonl", index)), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func compactionFixtureMessages(t *testing.T, count int) string {
	t.Helper()
	var sb strings.Builder
	for i := range count {
		data, err := json.Marshal(message.Message{Role: message.RoleUser, Content: fmt.Sprintf("m%d", i)})
		if err != nil {
			t.Fatal(err)
		}
		sb.Write(data)
		sb.WriteByte('\n')
	}
	return sb.String()
}

func TestScanCompactionHistorySkipsGapsAndUnapplied(t *testing.T) {
	dir := t.TempDir()
	for _, index := range []int{1, 2, 3, 5} {
		writeCompactionFixtureJSONL(t, dir, index, compactionFixtureMessages(t, 1))
	}
	writeCompactionFixtureStatus(t, dir, 1, CompactionHistoryStatusApplied)
	writeCompactionFixtureStatus(t, dir, 3, CompactionHistoryStatusApplied)
	writeCompactionFixtureStatus(t, dir, 5, "pending_apply")
	// index 2 has no status: the apply never completed.

	snapshots, issues, err := ScanCompactionHistory(dir)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(issues) != 0 {
		t.Fatalf("issues = %+v, want none", issues)
	}
	if len(snapshots) != 2 || snapshots[0].Index != 1 || snapshots[1].Index != 3 {
		t.Fatalf("snapshots = %+v, want indexes [1 3]", snapshots)
	}
	if snapshots[0].JSONLName != "main.pre-compress-1.jsonl" || snapshots[1].StatusName != "history-3.status.json" {
		t.Fatalf("unexpected snapshot names: %+v", snapshots)
	}
}

func TestScanCompactionHistoryReportsCorruptStatus(t *testing.T) {
	dir := t.TempDir()
	writeCompactionFixtureJSONL(t, dir, 1, compactionFixtureMessages(t, 1))
	writeCompactionFixtureStatus(t, dir, 1, CompactionHistoryStatusApplied)
	writeCompactionFixtureJSONL(t, dir, 2, compactionFixtureMessages(t, 1))
	if err := os.WriteFile(filepath.Join(dir, "history-2.status.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	snapshots, issues, err := ScanCompactionHistory(dir)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(snapshots) != 1 || snapshots[0].Index != 1 {
		t.Fatalf("snapshots = %+v, want only index 1", snapshots)
	}
	if len(issues) != 1 || issues[0].Index != 2 || issues[0].Err == nil {
		t.Fatalf("issues = %+v, want corrupt status for index 2", issues)
	}
}

func TestScanCompactionHistoryReadDirFailure(t *testing.T) {
	_, _, err := ScanCompactionHistory(filepath.Join(t.TempDir(), "missing"))
	if err == nil {
		t.Fatal("scan of missing dir succeeded, want error")
	}
}

func TestLoadAppliedCompactionSnapshot(t *testing.T) {
	sessionsRoot := t.TempDir()
	dir := filepath.Join(sessionsRoot, "session-1")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeCompactionFixtureStatus(t, dir, 1, CompactionHistoryStatusApplied)
	writeCompactionFixtureJSONL(t, dir, 1, compactionFixtureMessages(t, 3))

	msgs, err := LoadAppliedCompactionSnapshot(context.Background(), sessionsRoot, dir, 1)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(msgs) != 3 || msgs[0].Content != "m0" || msgs[2].Content != "m2" {
		t.Fatalf("messages = %+v", msgs)
	}
}

func TestLoadAppliedCompactionSnapshotAcceptsFinalRecordWithoutNewline(t *testing.T) {
	sessionsRoot := t.TempDir()
	dir := filepath.Join(sessionsRoot, "session-1")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeCompactionFixtureStatus(t, dir, 7, CompactionHistoryStatusApplied)
	content := strings.TrimSuffix(compactionFixtureMessages(t, 2), "\n")
	writeCompactionFixtureJSONL(t, dir, 7, content)

	msgs, err := LoadAppliedCompactionSnapshot(context.Background(), sessionsRoot, dir, 7)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(msgs) != 2 || msgs[1].Content != "m1" {
		t.Fatalf("messages = %+v", msgs)
	}
}

func TestLoadAppliedCompactionSnapshotFailures(t *testing.T) {
	validRecord := compactionFixtureMessages(t, 1)
	tests := []struct {
		name    string
		content string
		want    error
	}{
		{"empty", "", ErrCompactionSnapshotEmpty},
		{"blank record", "\n", ErrCompactionSnapshotParse},
		{"corrupt middle record", validRecord + "{not json\n" + validRecord, ErrCompactionSnapshotParse},
		{"corrupt final record", validRecord + `{"role":`, ErrCompactionSnapshotParse},
		{"corrupt terminated final record", validRecord + "{not json\n", ErrCompactionSnapshotParse},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sessionsRoot := t.TempDir()
			dir := filepath.Join(sessionsRoot, "session-1")
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			writeCompactionFixtureStatus(t, dir, 1, CompactionHistoryStatusApplied)
			writeCompactionFixtureJSONL(t, dir, 1, tc.content)

			_, err := LoadAppliedCompactionSnapshot(context.Background(), sessionsRoot, dir, 1)
			if !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestLoadAppliedCompactionSnapshotRejectsUnavailable(t *testing.T) {
	tests := []struct {
		name   string
		setup  func(t *testing.T, dir string)
		target string
	}{
		{
			name: "missing status",
			setup: func(t *testing.T, dir string) {
				writeCompactionFixtureJSONL(t, dir, 1, compactionFixtureMessages(t, 1))
			},
		},
		{
			name: "pending status",
			setup: func(t *testing.T, dir string) {
				writeCompactionFixtureStatus(t, dir, 1, "pending_apply")
				writeCompactionFixtureJSONL(t, dir, 1, compactionFixtureMessages(t, 1))
			},
		},
		{
			name: "missing snapshot",
			setup: func(t *testing.T, dir string) {
				writeCompactionFixtureStatus(t, dir, 1, CompactionHistoryStatusApplied)
			},
		},
		{
			name: "missing session dir",
			setup: func(t *testing.T, dir string) {
				_ = dir
			},
			target: "absent",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sessionsRoot := t.TempDir()
			dir := filepath.Join(sessionsRoot, "session-1")
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			tc.setup(t, dir)
			target := dir
			if tc.target != "" {
				target = filepath.Join(sessionsRoot, tc.target)
			}
			_, err := LoadAppliedCompactionSnapshot(context.Background(), sessionsRoot, target, 1)
			if !errors.Is(err, ErrCompactionSnapshotUnavailable) {
				t.Fatalf("error = %v, want unavailable", err)
			}
		})
	}
}

func TestLoadAppliedCompactionSnapshotRejectsInvalidBoundary(t *testing.T) {
	sessionsRoot := t.TempDir()
	dir := filepath.Join(sessionsRoot, "session-1")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeCompactionFixtureStatus(t, dir, 1, CompactionHistoryStatusApplied)
	writeCompactionFixtureJSONL(t, dir, 1, compactionFixtureMessages(t, 1))

	if _, err := LoadAppliedCompactionSnapshot(context.Background(), sessionsRoot, dir, 0); !errors.Is(err, ErrCompactionSnapshotInvalid) {
		t.Fatalf("zero index error = %v, want invalid", err)
	}
	if _, err := LoadAppliedCompactionSnapshot(context.Background(), sessionsRoot, sessionsRoot, 1); !errors.Is(err, ErrCompactionSnapshotInvalid) {
		t.Fatalf("root as session error = %v, want invalid", err)
	}
	// A directory below the root is inside the boundary: it is read like any
	// other directory there, and it simply holds no snapshot.
	nested := filepath.Join(dir, "nested")
	if err := os.Mkdir(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadAppliedCompactionSnapshot(context.Background(), sessionsRoot, nested, 1); !errors.Is(err, ErrCompactionSnapshotUnavailable) {
		t.Fatalf("nested session error = %v, want unavailable", err)
	}

	outside := t.TempDir()
	writeCompactionFixtureStatus(t, outside, 1, CompactionHistoryStatusApplied)
	writeCompactionFixtureJSONL(t, outside, 1, compactionFixtureMessages(t, 1))
	if _, err := LoadAppliedCompactionSnapshot(context.Background(), sessionsRoot, outside, 1); !errors.Is(err, ErrCompactionSnapshotInvalid) {
		t.Fatalf("outside session error = %v, want invalid", err)
	}
	escape := filepath.Join(sessionsRoot, "escape")
	if err := os.Symlink(outside, escape); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadAppliedCompactionSnapshot(context.Background(), sessionsRoot, escape, 1); !errors.Is(err, ErrCompactionSnapshotInvalid) {
		t.Fatalf("symlink escape error = %v, want invalid", err)
	}
}

func TestLoadAppliedCompactionSnapshotRejectsSymlinksAndNonRegularFiles(t *testing.T) {
	t.Run("snapshot symlink", func(t *testing.T) {
		sessionsRoot := t.TempDir()
		dir := filepath.Join(sessionsRoot, "session-1")
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(dir, "real.jsonl")
		if err := os.WriteFile(target, []byte(compactionFixtureMessages(t, 1)), 0o600); err != nil {
			t.Fatal(err)
		}
		writeCompactionFixtureStatus(t, dir, 1, CompactionHistoryStatusApplied)
		if err := os.Symlink(target, filepath.Join(dir, "main.pre-compress-1.jsonl")); err != nil {
			t.Fatal(err)
		}
		_, err := LoadAppliedCompactionSnapshot(context.Background(), sessionsRoot, dir, 1)
		if !errors.Is(err, ErrCompactionSnapshotInvalid) {
			t.Fatalf("error = %v, want invalid", err)
		}
	})

	t.Run("status symlink", func(t *testing.T) {
		sessionsRoot := t.TempDir()
		dir := filepath.Join(sessionsRoot, "session-1")
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		writeCompactionFixtureJSONL(t, dir, 1, compactionFixtureMessages(t, 1))
		target := filepath.Join(dir, "real.status.json")
		if err := os.WriteFile(target, []byte(`{"status":"applied"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(dir, "history-1.status.json")); err != nil {
			t.Fatal(err)
		}
		_, err := LoadAppliedCompactionSnapshot(context.Background(), sessionsRoot, dir, 1)
		if !errors.Is(err, ErrCompactionSnapshotInvalid) {
			t.Fatalf("error = %v, want invalid", err)
		}
	})

	t.Run("snapshot directory", func(t *testing.T) {
		sessionsRoot := t.TempDir()
		dir := filepath.Join(sessionsRoot, "session-1")
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(filepath.Join(dir, "main.pre-compress-1.jsonl"), 0o700); err != nil {
			t.Fatal(err)
		}
		writeCompactionFixtureStatus(t, dir, 1, CompactionHistoryStatusApplied)
		_, err := LoadAppliedCompactionSnapshot(context.Background(), sessionsRoot, dir, 1)
		if !errors.Is(err, ErrCompactionSnapshotInvalid) {
			t.Fatalf("error = %v, want invalid", err)
		}
	})
}

func TestLoadAppliedCompactionSnapshotRejectsCorruptStatus(t *testing.T) {
	sessionsRoot := t.TempDir()
	dir := filepath.Join(sessionsRoot, "session-1")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeCompactionFixtureJSONL(t, dir, 1, compactionFixtureMessages(t, 1))
	if err := os.WriteFile(filepath.Join(dir, "history-1.status.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadAppliedCompactionSnapshot(context.Background(), sessionsRoot, dir, 1)
	if !errors.Is(err, ErrCompactionSnapshotInvalid) {
		t.Fatalf("error = %v, want invalid", err)
	}
}

func TestLoadAppliedCompactionSnapshotHonorsCancellation(t *testing.T) {
	sessionsRoot := t.TempDir()
	dir := filepath.Join(sessionsRoot, "session-1")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeCompactionFixtureStatus(t, dir, 1, CompactionHistoryStatusApplied)
	writeCompactionFixtureJSONL(t, dir, 1, compactionFixtureMessages(t, 1))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := LoadAppliedCompactionSnapshot(ctx, sessionsRoot, dir, 1)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}
