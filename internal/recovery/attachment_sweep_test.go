package recovery

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeAttachment creates an attachment file with an explicit age so the sweep
// can be exercised without sleeping.
func writeAttachment(t *testing.T, dir, name string, age time.Duration) string {
	t.Helper()
	images := filepath.Join(dir, "images")
	if err := os.MkdirAll(images, 0o700); err != nil {
		t.Fatalf("mkdir images: %v", err)
	}
	path := filepath.Join(images, name)
	if err := os.WriteFile(path, []byte("payload-"+name), 0o600); err != nil {
		t.Fatalf("write attachment %s: %v", name, err)
	}
	stamp := time.Now().Add(-age)
	if err := os.Chtimes(path, stamp, stamp); err != nil {
		t.Fatalf("age attachment %s: %v", name, err)
	}
	return path
}

func TestSweepOrphanAttachmentsRemovesOnlyUnreferencedFiles(t *testing.T) {
	dir := t.TempDir()
	lock, err := AcquireSessionLock(dir)
	if err != nil {
		t.Fatalf("AcquireSessionLock: %v", err)
	}
	t.Cleanup(func() { _ = lock.Release() })

	live := writeAttachment(t, dir, "1700000000000000000-0.png", 48*time.Hour)
	orphan := writeAttachment(t, dir, "1700000000000000001-1.png", 48*time.Hour)
	fresh := writeAttachment(t, dir, "1700000000000000002-2.png", time.Minute)

	// A transcript record referencing exactly one attachment by path.
	record := `{"role":"user","parts":[{"type":"image","image_path":"` + live + `"}]}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "main.jsonl"), []byte(record), 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}

	res, err := SweepOrphanAttachments(dir, time.Now(), time.Hour)
	if err != nil {
		t.Fatalf("SweepOrphanAttachments: %v", err)
	}
	if len(res.Removed) != 1 || res.Removed[0] != filepath.Base(orphan) {
		t.Fatalf("Removed = %v, want [%s]", res.Removed, filepath.Base(orphan))
	}
	if res.KeptNew != 1 {
		t.Errorf("KeptNew = %d, want 1", res.KeptNew)
	}
	if res.Bytes <= 0 {
		t.Errorf("Bytes = %d, want the removed file size", res.Bytes)
	}
	if _, err := os.Stat(live); err != nil {
		t.Errorf("referenced attachment was removed: %v", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Errorf("attachment inside the grace period was removed: %v", err)
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Errorf("orphan still exists: %v", err)
	}
}

func TestSweepOrphanAttachmentsKeepsImageOperationOriginals(t *testing.T) {
	dir := t.TempDir()
	lock, err := AcquireSessionLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lock.Release() })
	name := "sha256-sample.png"
	live := writeAttachment(t, dir, name, 48*time.Hour)
	orphan := writeAttachment(t, dir, "1700000000000000001-0.png", 48*time.Hour)
	operations := filepath.Join(dir, "images", "operations")
	if err := os.MkdirAll(operations, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(operations, "sample.json"), []byte(`{"candidates":[{"image":{"reference":"artifact:images/`+name+`"},"saved":true}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	// A payload must not keep itself alive merely by containing its filename.
	if err := os.WriteFile(orphan, []byte(filepath.Base(orphan)), 0600); err != nil {
		t.Fatal(err)
	}
	stamp := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(orphan, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	res, err := SweepOrphanAttachments(dir, time.Now(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Removed) != 1 || res.Removed[0] != filepath.Base(orphan) {
		t.Fatalf("removed = %v", res.Removed)
	}
	if _, err := os.Stat(live); err != nil {
		t.Fatalf("manifest-owned original lost: %v", err)
	}
}

type boundedReferenceReader struct {
	reader io.Reader
	max    int
}

func (r boundedReferenceReader) Read(p []byte) (int, error) {
	return r.reader.Read(p[:min(len(p), r.max)])
}

func TestScanAttachmentReferencesAcrossReadBoundaries(t *testing.T) {
	name := "1700000000000000001-0.png"
	for _, chunk := range []int{1, 17, 64 << 10} {
		data := strings.Repeat("x", (64<<10)-5) + name + strings.Repeat("x", 128<<10)
		referenced := make(map[string]struct{})
		remaining, err := scanAttachmentReferences(boundedReferenceReader{strings.NewReader(data), chunk}, []attachmentCandidate{{name: name}, {name: "absent.pdf"}}, referenced)
		if err != nil || len(remaining) != 1 || remaining[0].name != "absent.pdf" {
			t.Fatalf("chunk=%d remaining=%v err=%v", chunk, remaining, err)
		}
		if _, ok := referenced[name]; !ok {
			t.Fatalf("chunk=%d missed cross-boundary reference", chunk)
		}
	}
}

type failedReferenceReader struct{ err error }

func (r failedReferenceReader) Read([]byte) (int, error) { return 0, r.err }

func TestScanAttachmentReferencesPropagatesReadFailure(t *testing.T) {
	failure := errors.New("read failed")
	reader := io.MultiReader(bytes.NewReader([]byte("record")), failedReferenceReader{failure})
	_, err := scanAttachmentReferences(reader, []attachmentCandidate{{name: "sample.png"}}, make(map[string]struct{}))
	if !errors.Is(err, failure) {
		t.Fatalf("error = %v", err)
	}
}

// The pending-input queue and the snapshot are independent durable owners; a
// reference in either one must keep the file.
func TestSweepOrphanAttachmentsHonoursEveryRecordKind(t *testing.T) {
	dir := t.TempDir()
	lock, err := AcquireSessionLock(dir)
	if err != nil {
		t.Fatalf("AcquireSessionLock: %v", err)
	}
	t.Cleanup(func() { _ = lock.Release() })

	fromSnapshot := writeAttachment(t, dir, "1700000000000000100-0.png", 48*time.Hour)
	fromPending := writeAttachment(t, dir, "1700000000000000101-1.pdf", 48*time.Hour)
	fromCheckpoint := writeAttachment(t, dir, "1700000000000000102-2.png", 48*time.Hour)
	orphan := writeAttachment(t, dir, "1700000000000000103-3.png", 48*time.Hour)

	if err := os.WriteFile(filepath.Join(dir, "snapshot.json"), []byte(`{"path":"`+fromSnapshot+`"}`), 0o600); err != nil {
		t.Fatalf("write snapshot: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "input-delivery.json"), []byte(`{"path":"`+fromPending+`"}`), 0o600); err != nil {
		t.Fatalf("write pending inputs: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.pre-compress-1.jsonl"), []byte(`{"path":"`+fromCheckpoint+`"}`), 0o600); err != nil {
		t.Fatalf("write checkpoint: %v", err)
	}

	res, err := SweepOrphanAttachments(dir, time.Now(), time.Hour)
	if err != nil {
		t.Fatalf("SweepOrphanAttachments: %v", err)
	}
	if len(res.Removed) != 1 || res.Removed[0] != filepath.Base(orphan) {
		t.Fatalf("Removed = %v, want [%s]", res.Removed, filepath.Base(orphan))
	}
}

// Diagnostic-only directories cannot own an attachment, so a name that appears
// only in dumps, traces, or job logs must not keep the file alive.
func TestSweepOrphanAttachmentsIgnoresDiagnosticDirs(t *testing.T) {
	dir := t.TempDir()
	lock, err := AcquireSessionLock(dir)
	if err != nil {
		t.Fatalf("AcquireSessionLock: %v", err)
	}
	t.Cleanup(func() { _ = lock.Release() })

	orphan := writeAttachment(t, dir, "1700000000000000300-0.png", 48*time.Hour)
	for _, sub := range []string{"dumps/llm", "traces", "job-logs"} {
		path := filepath.Join(dir, sub)
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", sub, err)
		}
		if err := os.WriteFile(filepath.Join(path, "record.json"), []byte(`{"path":"`+orphan+`"}`), 0o600); err != nil {
			t.Fatalf("write %s record: %v", sub, err)
		}
	}

	res, err := SweepOrphanAttachments(dir, time.Now(), time.Hour)
	if err != nil {
		t.Fatalf("SweepOrphanAttachments: %v", err)
	}
	if len(res.Removed) != 1 || res.Removed[0] != filepath.Base(orphan) {
		t.Fatalf("Removed = %v, want [%s]", res.Removed, filepath.Base(orphan))
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Errorf("orphan referenced only by diagnostics still exists: %v", err)
	}
}

// Sweeping an unowned session would race with the process that does own it.
func TestSweepOrphanAttachmentsRefusesUnlockedSession(t *testing.T) {
	dir := t.TempDir()
	orphan := writeAttachment(t, dir, "1700000000000000200-0.png", 48*time.Hour)

	if _, err := SweepOrphanAttachments(dir, time.Now(), time.Hour); err == nil {
		t.Fatal("expected an error for an unlocked session")
	}
	if _, err := os.Stat(orphan); err != nil {
		t.Errorf("sweep touched a file without owning the session: %v", err)
	}
}

// A symlink inside images/ is not something Chord wrote; it must be left alone
// and never followed.
func TestSweepOrphanAttachmentsSkipsNonRegularEntries(t *testing.T) {
	dir := t.TempDir()
	lock, err := AcquireSessionLock(dir)
	if err != nil {
		t.Fatalf("AcquireSessionLock: %v", err)
	}
	t.Cleanup(func() { _ = lock.Release() })

	outside := filepath.Join(t.TempDir(), "outside.png")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	images := filepath.Join(dir, "images")
	if err := os.MkdirAll(images, 0o700); err != nil {
		t.Fatalf("mkdir images: %v", err)
	}
	link := filepath.Join(images, "1700000000000000300-0.png")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	res, err := SweepOrphanAttachments(dir, time.Now(), time.Hour)
	if err != nil {
		t.Fatalf("SweepOrphanAttachments: %v", err)
	}
	if len(res.Removed) != 0 {
		t.Fatalf("Removed = %v, want none", res.Removed)
	}
	if _, err := os.Lstat(link); err != nil {
		t.Errorf("symlink was removed: %v", err)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Errorf("symlink target was removed: %v", err)
	}
}

// A session with no attachments is the common case and must be a no-op.
func TestSweepOrphanAttachmentsWithoutImagesDir(t *testing.T) {
	dir := t.TempDir()
	lock, err := AcquireSessionLock(dir)
	if err != nil {
		t.Fatalf("AcquireSessionLock: %v", err)
	}
	t.Cleanup(func() { _ = lock.Release() })

	res, err := SweepOrphanAttachments(dir, time.Now(), time.Hour)
	if err != nil {
		t.Fatalf("SweepOrphanAttachments: %v", err)
	}
	if len(res.Removed) != 0 || res.KeptNew != 0 {
		t.Fatalf("unexpected result: %+v", res)
	}
}
