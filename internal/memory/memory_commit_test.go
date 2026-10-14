package memory

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestCommitExtractionPreservesProjectFileModes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission bits are not enforced on Windows")
	}
	t.Setenv("CHORD_STATE_DIR", filepath.Join(t.TempDir(), "state"))
	root := t.TempDir()
	if err := os.Chmod(root, 0o755); err != nil {
		t.Fatal(err)
	}
	writeProjectFile(t, root, "MEMORY.md", "# Project Memory\n\nUser notes.\n")
	if err := os.Chmod(filepath.Join(root, "MEMORY.md"), 0o640); err != nil {
		t.Fatal(err)
	}

	m, err := NewManager(root)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if _, err := m.CommitExtractionCtx(context.Background(), "s1", "fp1", 1, 0, extractionOf(testCandidate(TypeFact, "Facts stay fresh.", "Facts stay fresh."))); err != nil {
		t.Fatalf("CommitExtraction: %v", err)
	}

	assertMode(t, root, 0o755) // project root never chmodded by privatefs
	assertMode(t, filepath.Join(root, "MEMORY.md"), 0o640)

	// A freshly written MEMORY.md (no previous file) uses the standard project
	// file mode instead of the private 0600.
	root2 := t.TempDir()
	m2, err := NewManager(root2)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if _, err := m2.CommitExtractionCtx(context.Background(), "s1", "fp1", 1, 0, extractionOf(testCandidate(TypeFact, "New facts.", "New facts."))); err != nil {
		t.Fatalf("CommitExtraction 2: %v", err)
	}
	assertMode(t, filepath.Join(root2, "MEMORY.md"), 0o644)
}

// A cancelled commit must never advance the checkpoint, even when a record
// write already happened: the record is idempotent on retry and the coverage
// stays uncovered.
func TestCommitExtractionCtxCancellationDoesNotAdvanceCheckpoint(t *testing.T) {
	t.Setenv("CHORD_STATE_DIR", filepath.Join(t.TempDir(), "state"))
	root := t.TempDir()
	m, err := NewManager(root)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled before the commit starts

	if _, err := m.CommitExtractionCtx(ctx, "s1", "fp1", 1, 0, extractionOf(testCandidate(TypeFact, "Facts stay fresh.", "Facts stay fresh."))); err == nil {
		t.Fatal("expected cancellation error")
	}
	cp, _ := LoadCheckpoint(m.layout)
	if cp != nil && cp.Covered("s1", "fp1") {
		t.Fatal("cancelled commit must not advance the checkpoint")
	}
}

// A waiter on an identical in-flight commit inherits the first attempt's
// outcome: a failed commit is reported as an error to the waiting goroutine,
// never disguised as a successful no-op.
func TestCommitExtractionSingleFlightWaiterInheritsFailure(t *testing.T) {
	t.Setenv("CHORD_STATE_DIR", filepath.Join(t.TempDir(), "state"))
	m, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	flight := &extractionFlight{done: make(chan struct{})}
	m.mu.Lock()
	m.inflight[singleFlightKey("s1", "fp1")] = flight
	m.mu.Unlock()

	type commitOutcome struct {
		res *CommitResult
		err error
	}
	outCh := make(chan commitOutcome, 1)
	go func() {
		res, cerr := m.CommitExtractionCtx(context.Background(), "s1", "fp1", 1, 0, extractionOf(testCandidate(TypeFact, "Fact", "Fact")))
		outCh <- commitOutcome{res: res, err: cerr}
	}()

	select {
	case got := <-outCh:
		t.Fatalf("waiter returned before the first attempt settled: %+v %v", got.res, got.err)
	case <-time.After(50 * time.Millisecond):
	}

	firstErr := errors.New("memory lock held by another process")
	flight.err = firstErr
	close(flight.done)

	select {
	case got := <-outCh:
		if !errors.Is(got.err, firstErr) {
			t.Fatalf("waiter err = %v, want the first attempt's failure %v", got.err, firstErr)
		}
		if got.res != nil {
			t.Fatalf("waiter result = %+v, want nil when the first attempt failed", got.res)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("waiter did not return after the flight settled")
	}
}

func TestCommitExtractionWritesRecordIndexCheckpoint(t *testing.T) {
	t.Setenv("CHORD_STATE_DIR", filepath.Join(t.TempDir(), "state"))
	root := t.TempDir()
	m, err := NewManager(root)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	candidates := []Candidate{
		{
			Type:         TypePitfall,
			Statement:    "Freeze the old persistence target before installing the new session target.",
			Rationale:    "Installing first can route events to the wrong persistence target.",
			Application:  "Use this ordering whenever switching session persistence.",
			Summary:      "Freeze the old persistence target first.",
			SourceRole:   SourceRoleUser,
			Confidence:   ConfidenceUserStated,
			Outcome:      OutcomePartial,
			ProjectPaths: []string{"internal/agent/session_switch.go"},
		},
	}
	res, err := m.CommitExtractionCtx(context.Background(), "20260821153000123", "fp-abcdef", 42, 3, extractionOf(candidates...))
	if err != nil {
		t.Fatalf("CommitExtraction: %v", err)
	}
	if len(res.Added) != 1 {
		t.Fatalf("added = %v, want 1 record", res.Added)
	}
	id := res.Added[0]
	recPath := filepath.Join(m.layout.RecordsDir, id+".md")
	if _, err := os.Stat(recPath); err != nil {
		t.Fatalf("record file missing: %v", err)
	}
	idx, err := m.LoadIndex()
	if err != nil {
		t.Fatalf("LoadIndex: %v", err)
	}
	if len(idx.Managed) != 1 || idx.Managed[0].ID != id {
		t.Fatalf("index entries = %+v", idx.Managed)
	}
	if !strings.Contains(idx.Managed[0].Link, ".chord/memory/records/") {
		t.Fatalf("link not project-relative: %q", idx.Managed[0].Link)
	}
	cp, err := LoadCheckpoint(m.layout)
	if err != nil {
		t.Fatalf("LoadCheckpoint: %v", err)
	}
	if cp == nil || !cp.Covered("20260821153000123", "fp-abcdef") {
		t.Fatalf("checkpoint = %+v", cp)
	}
	sc := cp.Sessions["20260821153000123"]
	if sc.ProjectedMessages != 42 || sc.CompactionGeneration != 3 {
		t.Fatalf("checkpoint counts = %+v", sc)
	}
}

func TestCommitExtractionIdempotentSameFingerprint(t *testing.T) {
	t.Setenv("CHORD_STATE_DIR", filepath.Join(t.TempDir(), "state"))
	root := t.TempDir()
	m, err := NewManager(root)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	candidates := []Candidate{testCandidate(TypeFact, "A fact.", "Fact.")}
	if _, err := m.CommitExtractionCtx(context.Background(), "s1", "fp1", 1, 0, extractionOf(candidates...)); err != nil {
		t.Fatalf("first commit: %v", err)
	}
	res, err := m.CommitExtractionCtx(context.Background(), "s1", "fp1", 1, 0, extractionOf(candidates...))
	if err != nil {
		t.Fatalf("second commit: %v", err)
	}
	if !res.Noop {
		t.Fatalf("second commit should be a no-op, got %+v", res)
	}
	idx, _ := m.LoadIndex()
	if len(idx.Managed) != 1 {
		t.Fatalf("index grew on idempotent re-commit: %d entries", len(idx.Managed))
	}
}

func TestCommitExtractionAppendAfterSessionGrows(t *testing.T) {
	t.Setenv("CHORD_STATE_DIR", filepath.Join(t.TempDir(), "state"))
	root := t.TempDir()
	m, err := NewManager(root)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	c1 := []Candidate{testCandidate(TypeFact, "Fact A", "A")}
	if _, err := m.CommitExtractionCtx(context.Background(), "s1", "fp-old", 5, 0, extractionOf(c1...)); err != nil {
		t.Fatalf("commit 1: %v", err)
	}
	// New fingerprint (session grew) must be allowed to extract again.
	c2 := []Candidate{testCandidate(TypeFact, "Fact B", "B")}
	res, err := m.CommitExtractionCtx(context.Background(), "s1", "fp-new", 9, 1, extractionOf(c2...))
	if err != nil {
		t.Fatalf("commit 2: %v", err)
	}
	if len(res.Added) != 1 {
		t.Fatalf("expected one new record, got %+v", res)
	}
	idx, _ := m.LoadIndex()
	if len(idx.Managed) != 2 {
		t.Fatalf("index entries = %d, want 2", len(idx.Managed))
	}
}

func TestCommitExtractionDeduplicatesConclusionAcrossSessions(t *testing.T) {
	t.Setenv("CHORD_STATE_DIR", filepath.Join(t.TempDir(), "state"))
	m, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	first := testCandidate(TypeWorkflow, "Run focused tests before broad checks.", "Focused tests first.")
	res, err := m.CommitExtractionCtx(context.Background(), "s1", "fp1", 1, 0, extractionOf(first))
	if err != nil {
		t.Fatalf("first commit: %v", err)
	}
	second := first
	second.Rationale = "A differently worded explanation from another session."
	res2, err := m.CommitExtractionCtx(context.Background(), "s2", "fp2", 1, 0, extractionOf(second))
	if err != nil {
		t.Fatalf("duplicate commit: %v", err)
	}
	if !res2.Noop || len(res2.Added) != 0 || len(res2.AlreadyKnown) != 1 || res2.AlreadyKnown[0] != res.Added[0] {
		t.Fatalf("duplicate result = %+v", res2)
	}
	idx, err := m.LoadIndex()
	if err != nil {
		t.Fatalf("LoadIndex: %v", err)
	}
	if len(idx.Managed) != 1 {
		t.Fatalf("duplicate conclusion grew active index: %+v", idx.Managed)
	}
	cp, err := LoadCheckpoint(m.layout)
	if err != nil || cp == nil || !cp.Covered("s2", "fp2") {
		t.Fatalf("duplicate conclusion did not advance checkpoint: cp=%+v err=%v", cp, err)
	}
}

func TestCommitExtractionSupersedesActiveRecord(t *testing.T) {
	t.Setenv("CHORD_STATE_DIR", filepath.Join(t.TempDir(), "state"))
	m, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	old := testCandidate(TypePreference, "Prefer the compact display.", "Prefer compact display.")
	// The supersede path only exempts user-stated records; a reported one is
	// the model's to revise, so seed it reported to exercise the normal path.
	old.Confidence = ConfidenceReported
	res, err := m.CommitExtractionCtx(context.Background(), "s1", "fp1", 1, 0, extractionOf(old))
	if err != nil {
		t.Fatalf("old commit: %v", err)
	}
	oldID := res.Added[0]
	replacement := testCandidate(TypePreference, "Prefer the expanded display for diagnostics.", "Prefer expanded diagnostics.")
	replacement.Supersedes = []string{oldID}
	res2, err := m.CommitExtractionCtx(context.Background(), "s2", "fp2", 1, 0, extractionOf(replacement))
	if err != nil {
		t.Fatalf("replacement commit: %v", err)
	}
	if len(res2.Added) != 1 || len(res2.Superseded) != 1 || res2.Superseded[0] != oldID {
		t.Fatalf("replacement result = %+v", res2)
	}
	idx, err := m.LoadIndex()
	if err != nil {
		t.Fatalf("LoadIndex: %v", err)
	}
	if len(idx.Managed) != 1 || idx.Managed[0].ID != res2.Added[0] {
		t.Fatalf("active index after replacement = %+v", idx.Managed)
	}
	if _, err := os.Stat(recordPath(m.layout.RecordsDir, oldID)); err != nil {
		t.Fatalf("superseded record should remain as provenance: %v", err)
	}
	record, err := loadRecord(recordPath(m.layout.RecordsDir, res2.Added[0]))
	if err != nil || len(record.Supersedes) != 1 || record.Supersedes[0] != oldID {
		t.Fatalf("replacement provenance = %+v, err=%v", record, err)
	}
}

func TestCommitExtractionRejectsInactiveSupersedes(t *testing.T) {
	t.Setenv("CHORD_STATE_DIR", filepath.Join(t.TempDir(), "state"))
	m, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	candidate := testCandidate(TypeFact, "A durable fact.", "Durable fact.")
	candidate.Supersedes = []string{"missing--1234567890abcdef"}
	if _, err := m.CommitExtractionCtx(context.Background(), "s1", "fp1", 1, 0, extractionOf(candidate)); err == nil {
		t.Fatal("expected inactive supersedes to fail")
	}
	cp, err := LoadCheckpoint(m.layout)
	if err != nil {
		t.Fatalf("LoadCheckpoint: %v", err)
	}
	if cp != nil {
		t.Fatalf("checkpoint advanced after invalid replacement: %+v", cp)
	}
}

func TestCommitExtractionNoCandidatesAdvancesCheckpoint(t *testing.T) {
	t.Setenv("CHORD_STATE_DIR", filepath.Join(t.TempDir(), "state"))
	root := t.TempDir()
	m, err := NewManager(root)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	res, err := m.CommitExtractionCtx(context.Background(), "s1", "fp-empty", 0, 0, nil)
	if err != nil {
		t.Fatalf("CommitExtraction: %v", err)
	}
	if !res.Noop {
		t.Fatalf("expected no-op result, got %+v", res)
	}
	cp, err := LoadCheckpoint(m.layout)
	if err != nil {
		t.Fatalf("LoadCheckpoint: %v", err)
	}
	if cp == nil || !cp.Covered("s1", "fp-empty") {
		t.Fatalf("checkpoint not advanced: %+v", cp)
	}
	// No index file should have been created.
	if _, err := os.Stat(m.layout.IndexPath); !os.IsNotExist(err) {
		t.Fatalf("index file unexpectedly created: %v", err)
	}
}

func TestCommitExtractionKeepsUserNotes(t *testing.T) {
	t.Setenv("CHORD_STATE_DIR", filepath.Join(t.TempDir(), "state"))
	root := t.TempDir()
	writeProjectFile(t, root, "MEMORY.md", "# Project Memory\n\nHand-written notes that must survive.\n")
	m, err := NewManager(root)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	candidates := []Candidate{testCandidate(TypeWorkflow, "Run focused tests first.", "Focused tests first.")}
	if _, err := m.CommitExtractionCtx(context.Background(), "s1", "fp1", 1, 0, extractionOf(candidates...)); err != nil {
		t.Fatalf("CommitExtraction: %v", err)
	}
	data, err := os.ReadFile(m.layout.IndexPath)
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	text := string(data)
	if !strings.Contains(text, "Hand-written notes that must survive") {
		t.Fatalf("user notes clobbered: %s", text)
	}
	if !strings.Contains(text, managedStartMarker) || !strings.Contains(text, managedEndMarker) {
		t.Fatalf("managed markers missing: %s", text)
	}
}

func TestCommitExtractionStopsOnMalformedMarkers(t *testing.T) {
	t.Setenv("CHORD_STATE_DIR", filepath.Join(t.TempDir(), "state"))
	root := t.TempDir()
	// Broken: two managed-start markers.
	writeProjectFile(t, root, "MEMORY.md", "# Notes\n\n<!-- chord:managed:start -->\n<!-- chord:managed:start -->\n<!-- chord:managed:end -->\n")
	m, err := NewManager(root)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	candidates := []Candidate{testCandidate(TypeFact, "Fact", "Fact")}
	if _, err := m.CommitExtractionCtx(context.Background(), "s1", "fp1", 1, 0, extractionOf(candidates...)); err == nil {
		t.Fatal("expected error for malformed markers")
	}
	// Checkpoint must not have advanced.
	cp, _ := LoadCheckpoint(m.layout)
	if cp != nil {
		t.Fatalf("checkpoint advanced despite failed commit: %+v", cp)
	}
}

func TestCommitExtractionDoesNotOverwriteConcurrentUserEdit(t *testing.T) {
	t.Setenv("CHORD_STATE_DIR", filepath.Join(t.TempDir(), "state"))
	root := t.TempDir()
	writeProjectFile(t, root, "MEMORY.md", "# Project Memory\n\nOriginal user notes.\n")
	m, err := NewManager(root)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	candidates := []Candidate{testCandidate(TypeFact, "Fact", "Fact")}
	if _, err := m.CommitExtractionCtx(context.Background(), "s1", "fp1", 1, 0, extractionOf(candidates...)); err != nil {
		t.Fatalf("first commit: %v", err)
	}
	// User edits the file externally (no lock) with new notes.
	writeProjectFile(t, root, "MEMORY.md", "# Project Memory\n\nBrand new user edit.\n")
	// A second extraction must re-merge against the new content, not clobber it.
	c2 := []Candidate{testCandidate(TypeFact, "Fact 2", "Fact 2")}
	if _, err := m.CommitExtractionCtx(context.Background(), "s1", "fp2", 1, 0, extractionOf(c2...)); err != nil {
		t.Fatalf("second commit: %v", err)
	}
	data, _ := os.ReadFile(m.layout.IndexPath)
	text := string(data)
	if !strings.Contains(text, "Brand new user edit") {
		t.Fatalf("concurrent user edit clobbered: %s", text)
	}
	if !strings.Contains(text, "Fact 2") {
		t.Fatalf("new record not indexed: %s", text)
	}
}

// Manual editing is the supported way to remove a record: deleting its line
// from the managed section leaves the record file as an orphan, and Chord must
// never auto-revive it. A later extraction with a different fingerprint must
// not re-add the removed entry; a re-derived identical conclusion counts as
// already known (see TestManualIndexEditNotRevivedByIdenticalConclusion).
func TestManualIndexEditNotAutoRevived(t *testing.T) {
	t.Setenv("CHORD_STATE_DIR", filepath.Join(t.TempDir(), "state"))
	root := t.TempDir()
	m, err := NewManager(root)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	candidates := []Candidate{testCandidate(TypeFact, "Fact", "Fact")}
	res, err := m.CommitExtractionCtx(context.Background(), "s1", "fp1", 1, 0, extractionOf(candidates...))
	if err != nil {
		t.Fatalf("CommitExtraction: %v", err)
	}
	id := res.Added[0]
	// User removes the entry by editing MEMORY.md (keeping the managed markers).
	data, err := os.ReadFile(m.layout.IndexPath)
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	// Remove the "- [id](link)" line and its "  — summary" continuation line.
	lines := strings.Split(string(data), "\n")
	var kept []string
	for i := 0; i < len(lines); i++ {
		if strings.HasPrefix(lines[i], "- ["+id+"](") {
			if i+1 < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[i+1]), "—") {
				i++
			}
			continue
		}
		kept = append(kept, lines[i])
	}
	if err := os.WriteFile(m.layout.IndexPath, []byte(strings.Join(kept, "\n")), 0o644); err != nil {
		t.Fatalf("manual edit: %v", err)
	}
	// A new extraction with a different candidate must not revive the old entry.
	res2, err := m.CommitExtractionCtx(context.Background(), "s2", "fp2", 1, 0, extractionOf(testCandidate(TypeFact, "Another", "Another")))
	if err != nil {
		t.Fatalf("commit 2: %v", err)
	}
	if len(res2.Added) != 1 || res2.Added[0] == id {
		t.Fatalf("manual-removed entry revived: %+v", res2)
	}
	idx, _ := m.LoadIndex()
	for _, e := range idx.Managed {
		if e.ID == id {
			t.Fatalf("removed entry still indexed: %+v", idx.Managed)
		}
	}
	// The record file stays as an orphan.
	if _, err := os.Stat(filepath.Join(m.layout.RecordsDir, id+".md")); err != nil {
		t.Fatalf("record file should remain as orphan: %v", err)
	}
}

// The orphan contract covers identical content too: a later session re-deriving
// the exact removed conclusion must not resurrect the deleted index line. The
// re-derivation counts as already known, so the checkpoint still advances and
// the extraction does not retry the same conclusion forever.
func TestManualIndexEditNotRevivedByIdenticalConclusion(t *testing.T) {
	t.Setenv("CHORD_STATE_DIR", filepath.Join(t.TempDir(), "state"))
	root := t.TempDir()
	m, err := NewManager(root)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	res, err := m.CommitExtractionCtx(context.Background(), "s1", "fp1", 1, 0,
		extractionOf(testCandidate(TypeFact, "The release gate runs after docs sync.", "Release gate ordering.")))
	if err != nil {
		t.Fatalf("CommitExtraction: %v", err)
	}
	id := res.Added[0]
	// User removes the entry by editing MEMORY.md (keeping the managed markers).
	data, err := os.ReadFile(m.layout.IndexPath)
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	// Remove the "- [id](link)" line and its "  — summary" continuation line.
	lines := strings.Split(string(data), "\n")
	var kept []string
	for i := 0; i < len(lines); i++ {
		if strings.HasPrefix(lines[i], "- ["+id+"](") {
			if i+1 < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[i+1]), "—") {
				i++
			}
			continue
		}
		kept = append(kept, lines[i])
	}
	if err := os.WriteFile(m.layout.IndexPath, []byte(strings.Join(kept, "\n")), 0o644); err != nil {
		t.Fatalf("manual edit: %v", err)
	}

	res2, err := m.CommitExtractionCtx(context.Background(), "s2", "fp2", 1, 0,
		extractionOf(testCandidate(TypeFact, "The release gate runs after docs sync.", "Release gate ordering.")))
	if err != nil {
		t.Fatalf("identical re-derivation must not fail the commit: %v", err)
	}
	if len(res2.Added) != 0 || len(res2.AlreadyKnown) != 1 {
		t.Fatalf("result = %+v, want the orphan re-derivation reported as already known", res2)
	}
	idx, err := m.LoadIndex()
	if err != nil {
		t.Fatalf("LoadIndex: %v", err)
	}
	for _, e := range idx.Managed {
		if e.ID == id {
			t.Fatalf("orphan entry resurrected in the index: %+v", idx.Managed)
		}
	}
	cp, err := LoadCheckpoint(m.layout)
	if err != nil || cp == nil || !cp.Covered("s2", "fp2") {
		t.Fatalf("orphan re-derivation did not advance checkpoint: cp=%+v err=%v", cp, err)
	}
}

// A commit interrupted between the record write and the index/checkpoint leaves
// the record on disk with no index line. The retry of that same extraction must
// adopt the record and finish the commit instead of reporting it as a
// user-removed orphan.
func TestCommitExtractionAdoptsInterruptedRecord(t *testing.T) {
	t.Setenv("CHORD_STATE_DIR", filepath.Join(t.TempDir(), "state"))
	m, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	candidate := testCandidate(TypeFact, "The migration runs before the backup.", "Migration ordering.")
	rec := recordFromCandidate("s1", "fp1", candidate)
	rec.ID = RecordID(rec.Summary, rec.ContentHash())
	if created, err := writeRecordImmutable(m.layout, rec); err != nil || !created {
		t.Fatalf("seed interrupted record: created=%v err=%v", created, err)
	}

	res, err := m.CommitExtractionCtx(context.Background(), "s1", "fp1", 1, 0, extractionOf(candidate))
	if err != nil {
		t.Fatalf("retry commit: %v", err)
	}
	if len(res.Added) != 1 || res.Added[0] != rec.ID {
		t.Fatalf("result = %+v, want the interrupted record adopted as added", res)
	}
	idx, err := m.LoadIndex()
	if err != nil {
		t.Fatalf("LoadIndex: %v", err)
	}
	if len(idx.Managed) != 1 || idx.Managed[0].ID != rec.ID {
		t.Fatalf("index entries = %+v", idx.Managed)
	}
	cp, err := LoadCheckpoint(m.layout)
	if err != nil || cp == nil || !cp.Covered("s1", "fp1") {
		t.Fatalf("checkpoint not advanced: cp=%+v err=%v", cp, err)
	}
}

// The same record shape written by another extraction is a user-removed orphan:
// the retry reports it as already known and keeps it out of the index.
func TestCommitExtractionKeepsForeignOrphan(t *testing.T) {
	t.Setenv("CHORD_STATE_DIR", filepath.Join(t.TempDir(), "state"))
	m, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	candidate := testCandidate(TypeFact, "The migration runs before the backup.", "Migration ordering.")
	rec := recordFromCandidate("other-session", "other-fingerprint", candidate)
	rec.ID = RecordID(rec.Summary, rec.ContentHash())
	if created, err := writeRecordImmutable(m.layout, rec); err != nil || !created {
		t.Fatalf("seed orphan record: created=%v err=%v", created, err)
	}

	res, err := m.CommitExtractionCtx(context.Background(), "s1", "fp1", 1, 0, extractionOf(candidate))
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if len(res.Added) != 0 || len(res.AlreadyKnown) != 1 || res.AlreadyKnown[0] != rec.ID {
		t.Fatalf("result = %+v, want the foreign orphan reported as already known", res)
	}
	idx, err := m.LoadIndex()
	if err != nil {
		t.Fatalf("LoadIndex: %v", err)
	}
	for _, e := range idx.Managed {
		if e.ID == rec.ID {
			t.Fatalf("foreign orphan resurrected in the index: %+v", idx.Managed)
		}
	}
	cp, err := LoadCheckpoint(m.layout)
	if err != nil || cp == nil || !cp.Covered("s1", "fp1") {
		t.Fatalf("checkpoint not advanced: cp=%+v err=%v", cp, err)
	}
}

// User content outside the managed section must be preserved byte-for-byte,
// including leading/trailing blank lines, on every managed-section rewrite.
func TestCommitPreservesUserNotesVerbatim(t *testing.T) {
	t.Setenv("CHORD_STATE_DIR", filepath.Join(t.TempDir(), "state"))
	root := t.TempDir()
	notes := "\n\n# Project Memory\n\nHand-written notes.\n\n\n"
	writeProjectFile(t, root, "MEMORY.md", notes)
	m, err := NewManager(root)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	candidates := []Candidate{testCandidate(TypeFact, "Fact", "Fact")}
	if _, err := m.CommitExtractionCtx(context.Background(), "s1", "fp1", 1, 0, extractionOf(candidates...)); err != nil {
		t.Fatalf("CommitExtraction: %v", err)
	}
	data, err := os.ReadFile(m.layout.IndexPath)
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	text := string(data)
	// Head must appear verbatim, terminated only by the managed marker line.
	wantHead := notes
	if !strings.HasPrefix(text, wantHead) {
		t.Fatalf("user notes not preserved verbatim:\nwant prefix %q\ngot %q", wantHead, text)
	}
	if !strings.Contains(text, managedStartMarker) {
		t.Fatalf("managed markers missing: %s", text)
	}
}

// The commit that recovers from a corrupt checkpoint must say so: silent
// self-healing would hide a file that keeps getting corrupted.
func TestCommitExtractionReportsDiscardedCheckpoint(t *testing.T) {
	t.Setenv("CHORD_STATE_DIR", filepath.Join(t.TempDir(), "state"))
	m, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if err := os.MkdirAll(m.layout.StateDir, 0o755); err != nil {
		t.Fatalf("mkdir state dir: %v", err)
	}
	if err := os.WriteFile(m.layout.CheckpointPath, []byte("{not json"), 0o644); err != nil {
		t.Fatalf("write corrupt checkpoint: %v", err)
	}

	res, err := m.CommitExtractionCtx(context.Background(), "s1", "fp1", 0, 0, nil)
	if err != nil {
		t.Fatalf("CommitExtractionCtx: %v", err)
	}
	if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "unreadable extraction checkpoint") {
		t.Fatalf("warnings = %v, want the discarded checkpoint reported", res.Warnings)
	}
	cp, err := LoadCheckpoint(m.layout)
	if err != nil || cp == nil || !cp.Covered("s1", "fp1") {
		t.Fatalf("checkpoint = %+v (err %v), want s1 covered after recovery", cp, err)
	}
}

// Repeated commits must not reshuffle the file. The previous map-iteration merge
// only looked stable because rendering re-sorted by ID; with order now meaningful,
// churn would rewrite MEMORY.md and change what gets injected every time.
func TestCommitPreservesExistingRowOrderAcrossRuns(t *testing.T) {
	t.Setenv("CHORD_STATE_DIR", filepath.Join(t.TempDir(), "state"))
	root := t.TempDir()
	m, err := NewManager(root)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	var seeded []string
	for i := range 4 {
		res, err := m.CommitExtractionCtx(context.Background(), fmt.Sprintf("s%d", i), fmt.Sprintf("fp%d", i), 1, 0,
			extractionOf(testCandidate(TypeFact, fmt.Sprintf("Conclusion number %d.", i), fmt.Sprintf("Summary %d.", i))))
		if err != nil {
			t.Fatalf("commit %d: %v", i, err)
		}
		if len(res.Added) != 1 {
			t.Fatalf("commit %d added = %v", i, res.Added)
		}
		seeded = append(seeded, res.Added[0])
	}
	idx, err := m.LoadIndex()
	if err != nil {
		t.Fatalf("LoadIndex: %v", err)
	}
	// Newest first, so the seeded order is reversed.
	if len(idx.Managed) != len(seeded) {
		t.Fatalf("index = %+v, want %d entries", idx.Managed, len(seeded))
	}
	for i, e := range idx.Managed {
		want := seeded[len(seeded)-1-i]
		if e.ID != want {
			t.Fatalf("index[%d] = %q, want %q (newest first)", i, e.ID, want)
		}
	}
	before, err := os.ReadFile(m.layout.IndexPath)
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	// A no-op commit must not rewrite the file at all.
	if _, err := m.CommitExtractionCtx(context.Background(), "s-noop", "fp-noop", 0, 0, nil); err != nil {
		t.Fatalf("noop commit: %v", err)
	}
	after, err := os.ReadFile(m.layout.IndexPath)
	if err != nil {
		t.Fatalf("re-read index: %v", err)
	}
	if string(before) != string(after) {
		t.Fatalf("no-op commit rewrote MEMORY.md:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}
