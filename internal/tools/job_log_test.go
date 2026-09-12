package tools

import (
	"bytes"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestRotatingJobLogRotatesWhenExceedingMaxBytes(t *testing.T) {
	restore := maxJobLogBytes
	maxJobLogBytes = 8
	t.Cleanup(func() { maxJobLogBytes = restore })

	sessionDir := t.TempDir()
	logPath := filepath.Join(sessionDir, "job-1.log")
	writer, err := openRotatingJobLog(sessionDir, logPath)
	if err != nil {
		t.Fatalf("openRotatingJobLog: %v", err)
	}
	if _, err := writer.Write([]byte("aaaaaaaa")); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if _, err := writer.Write([]byte("bbbb")); err != nil {
		t.Fatalf("second write: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	backup, err := os.ReadFile(logPath + ".1")
	if err != nil {
		t.Fatalf("read backup log: %v", err)
	}
	if string(backup) != "aaaaaaaa" {
		t.Fatalf("backup log = %q, want the pre-rotation contents", backup)
	}
	active, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read active log: %v", err)
	}
	if string(active) != "bbbb" {
		t.Fatalf("active log = %q, want only post-rotation contents", active)
	}

	// A write that arrives after Close must not panic or resurrect the file.
	// It also must not fail: the tee would then drop the bytes from the
	// in-memory window as well, so a straggler write at teardown would cost the
	// model its output.
	if _, err := writer.Write([]byte("cccc")); err != nil {
		t.Fatalf("write after Close = %v, want it swallowed", err)
	}
}

func TestRotatingJobLogBoundsFileWhenRenameFails(t *testing.T) {
	restore := maxJobLogBytes
	maxJobLogBytes = 8
	t.Cleanup(func() { maxJobLogBytes = restore })

	sessionDir := t.TempDir()
	logPath := filepath.Join(sessionDir, "job-1.log")
	writer, err := openRotatingJobLog(sessionDir, logPath)
	if err != nil {
		t.Fatalf("openRotatingJobLog: %v", err)
	}
	t.Cleanup(func() { _ = writer.Close() })

	// A non-empty directory at the backup path makes both the Remove and the
	// Rename fail, so the full file cannot be moved aside.
	backup := logPath + ".1"
	if err := os.MkdirAll(filepath.Join(backup, "occupied"), 0o700); err != nil {
		t.Fatalf("seed backup path: %v", err)
	}

	if _, err := writer.Write([]byte("aaaaaaaa")); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if _, err := writer.Write([]byte("bbbb")); err != nil {
		t.Fatalf("second write: %v", err)
	}

	info, err := os.Stat(logPath)
	if err != nil {
		t.Fatalf("stat active log: %v", err)
	}
	if info.Size() > maxJobLogBytes {
		t.Fatalf("active log size = %d, want <= %d after a failed rotation", info.Size(), maxJobLogBytes)
	}
	active, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read active log: %v", err)
	}
	if string(active) != "bbbb" {
		t.Fatalf("active log = %q, want the newest bytes", active)
	}
}

func TestRotatingJobLogWriteSurvivesReopenFailure(t *testing.T) {
	restore := maxJobLogBytes
	maxJobLogBytes = 4
	t.Cleanup(func() { maxJobLogBytes = restore })

	sessionDir := t.TempDir()
	logPath := filepath.Join(sessionDir, "job-1.log")
	writer, err := openRotatingJobLog(sessionDir, logPath)
	if err != nil {
		t.Fatalf("openRotatingJobLog: %v", err)
	}
	t.Cleanup(func() { _ = writer.Close() })
	if _, err := writer.Write([]byte("aaaa")); err != nil {
		t.Fatalf("first write: %v", err)
	}

	// Point the sink outside its root so the rotation's reopen fails and leaves
	// no file behind. The tee's in-memory window still needs the bytes, so Write
	// must report success instead of dereferencing the missing file.
	writer.path = filepath.Join(t.TempDir(), "outside.log")

	if n, err := writer.Write([]byte("bbbb")); err != nil || n != len("bbbb") {
		t.Fatalf("Write after failed reopen = (%d, %v), want (%d, nil)", n, err, len("bbbb"))
	}
}

func TestRotatingJobLogConcurrentWrites(t *testing.T) {
	restore := maxJobLogBytes
	maxJobLogBytes = 1024
	t.Cleanup(func() { maxJobLogBytes = restore })

	// stdout and stderr tee through separate MultiWriter instances, so os/exec
	// runs concurrent copier goroutines against the same sink.
	sessionDir := t.TempDir()
	logPath := filepath.Join(sessionDir, "job-1.log")
	writer, err := openRotatingJobLog(sessionDir, logPath)
	if err != nil {
		t.Fatalf("openRotatingJobLog: %v", err)
	}
	const writers = 4
	const iterations = 200
	var wg sync.WaitGroup
	for id := 0; id < writers; id++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			chunk := bytes.Repeat([]byte{byte('a' + id)}, 64)
			for i := 0; i < iterations; i++ {
				if _, err := writer.Write(chunk); err != nil {
					t.Errorf("writer %d: %v", id, err)
					return
				}
			}
		}(id)
	}
	wg.Wait()
	if err := writer.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	for _, path := range []string{logPath, logPath + ".1"} {
		info, err := os.Stat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatalf("stat %s: %v", path, err)
		}
		if info.Size() > maxJobLogBytes {
			t.Fatalf("%s size = %d, want <= %d", path, info.Size(), maxJobLogBytes)
		}
	}
}

func TestRotatingJobLogTrimsOversizedWrite(t *testing.T) {
	restore := maxJobLogBytes
	maxJobLogBytes = 4
	t.Cleanup(func() { maxJobLogBytes = restore })

	sessionDir := t.TempDir()
	logPath := filepath.Join(sessionDir, "job-1.log")
	writer, err := openRotatingJobLog(sessionDir, logPath)
	if err != nil {
		t.Fatalf("openRotatingJobLog: %v", err)
	}
	// A single write larger than the cap must not land whole: the tee reports
	// the full input as written so the in-memory window keeps every byte, while
	// the disk file retains only the newest tail.
	if n, err := writer.Write([]byte("abcdefgh")); err != nil || n != len("abcdefgh") {
		t.Fatalf("Write = (%d, %v), want (%d, nil)", n, err, len("abcdefgh"))
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	active, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read active log: %v", err)
	}
	if string(active) != "efgh" {
		t.Fatalf("active log = %q, want the newest %d bytes", active, maxJobLogBytes)
	}
	if _, err := os.Stat(logPath + ".1"); !os.IsNotExist(err) {
		t.Fatalf("an oversized first write must not create a rotated file (err=%v)", err)
	}
}

func TestPruneJobLogsRemovesStaleFiles(t *testing.T) {
	dir := t.TempDir()
	stale := filepath.Join(dir, "job-old.log")
	staleRotated := filepath.Join(dir, "job-mid.log.1")
	fresh := filepath.Join(dir, "job-new.log")
	unrelated := filepath.Join(dir, "keep.txt")
	for _, path := range []string{stale, staleRotated, fresh, unrelated} {
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatalf("seed %s: %v", path, err)
		}
	}
	old := time.Now().Add(-2 * jobLogRetention)
	for _, path := range []string{stale, staleRotated} {
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatalf("age %s: %v", path, err)
		}
	}

	pruneJobLogs(dir)

	for _, path := range []string{stale, staleRotated} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("%s still exists after prune (err=%v)", path, err)
		}
	}
	for _, path := range []string{fresh, unrelated} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("%s was removed by prune: %v", path, err)
		}
	}
}
