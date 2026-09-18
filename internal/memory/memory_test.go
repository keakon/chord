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
	"unicode/utf8"

	"github.com/keakon/chord/internal/config"
)

func writeProjectFile(t *testing.T, projectRoot, rel, content string) {
	t.Helper()
	path := filepath.Join(projectRoot, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func testCandidate(kind Type, statement, summary string) Candidate {
	return Candidate{
		Type:        kind,
		Statement:   statement,
		Rationale:   "This learning is not otherwise visible from the repository.",
		Application: "Apply it when the same situation appears in a future session.",
		Summary:     summary,
		SourceRole:  SourceRoleUser,
		Confidence:  ConfidenceUserStated,
		Outcome:     OutcomeSuccess,
	}
}

// extractionOf wraps candidates as a commit payload for tests that only exercise
// the addition path.
func extractionOf(candidates ...Candidate) *ExtractionOutput {
	return &ExtractionOutput{Candidates: candidates}
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("mode(%s) = %04o, want %04o", path, got, want)
	}
}

// The project root and an existing MEMORY.md are normal project files: an
// automatic commit must never chmod the project root or overwrite the file's
// existing permissions. Only the new record file gets the default project mode.
// A caller-provided path locator must drive the machine state directory so
// custom paths.state_dir / paths.sessions_dir settings are honored instead of
// re-deriving the default paths.
func TestNewManagerUsesProvidedPathLocator(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(t.TempDir(), "custom-state")
	loc := &config.PathLocator{
		ConfigHome:   filepath.Join(t.TempDir(), "config"),
		StateDir:     stateDir,
		SessionsRoot: filepath.Join(stateDir, "sessions"),
		CacheDir:     filepath.Join(stateDir, "cache"),
		LogsDir:      filepath.Join(stateDir, "logs"),
		ExportsDir:   filepath.Join(stateDir, "exports"),
	}
	m, err := NewManager(root, loc)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if !strings.HasPrefix(m.layout.StateDir, stateDir) {
		t.Fatalf("state dir = %q, want under %q", m.layout.StateDir, stateDir)
	}
	// The default-locator path still works for callers that do not pass one.
	m2, err := NewManager(root)
	if err != nil {
		t.Fatalf("NewManager default: %v", err)
	}
	if m2.layout.StateDir == m.layout.StateDir {
		t.Fatalf("default state dir unexpectedly equals the provided one: %q", m2.layout.StateDir)
	}
}

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

func TestBoundedSummaryUserNotesOnly(t *testing.T) {
	t.Setenv("CHORD_STATE_DIR", filepath.Join(t.TempDir(), "state"))
	root := t.TempDir()
	writeProjectFile(t, root, "MEMORY.md", "# Project Memory\n\n- Prefer focused verification before broad test suites.")
	m, err := NewManager(root)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	summary, active, err := m.BoundedSummary()
	if err != nil {
		t.Fatalf("BoundedSummary: %v", err)
	}
	if !active {
		t.Fatal("expected active summary for notes-only MEMORY.md")
	}
	if !strings.Contains(summary, "focused verification") {
		t.Fatalf("summary missing notes: %q", summary)
	}
}

func TestBoundedSummaryInactiveWithoutFile(t *testing.T) {
	t.Setenv("CHORD_STATE_DIR", filepath.Join(t.TempDir(), "state"))
	root := t.TempDir()
	m, err := NewManager(root)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	_, active, err := m.BoundedSummary()
	if err != nil {
		t.Fatalf("BoundedSummary: %v", err)
	}
	if active {
		t.Fatal("expected inactive summary without MEMORY.md")
	}
}

func TestBoundedSummaryManagedAndNotes(t *testing.T) {
	t.Setenv("CHORD_STATE_DIR", filepath.Join(t.TempDir(), "state"))
	root := t.TempDir()
	content := `# Project Memory

- User note kept.

<!-- chord:managed:start -->

## Managed Records

- [abc-1234567890abcdef](.chord/memory/records/abc-1234567890abcdef.md)
  — Short summary.

<!-- chord:managed:end -->
`
	writeProjectFile(t, root, "MEMORY.md", content)
	m, err := NewManager(root)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	summary, active, err := m.BoundedSummary()
	if err != nil {
		t.Fatalf("BoundedSummary: %v", err)
	}
	if !active {
		t.Fatal("expected active summary")
	}
	if !strings.Contains(summary, "User note kept") {
		t.Fatalf("summary missing user notes: %q", summary)
	}
	if !strings.Contains(summary, "abc-1234567890abcdef") {
		t.Fatalf("summary missing managed entry: %q", summary)
	}
}

func TestBoundedSummaryNeverRendersRecordBodies(t *testing.T) {
	t.Setenv("CHORD_STATE_DIR", filepath.Join(t.TempDir(), "state"))
	root := t.TempDir()
	content := `# Project Memory

<!-- chord:managed:start -->

## Managed Records

- [abc-1234567890abcdef](.chord/memory/records/abc-1234567890abcdef.md)
  — Freeze old persistence before installing a new session target.

<!-- chord:managed:end -->
`
	writeProjectFile(t, root, "MEMORY.md", content)
	writeProjectFile(t, root, ".chord/memory/records/abc-1234567890abcdef.md", "---\nid: abc-1234567890abcdef\ntype: pitfall\n---\n\nLong body that must not be injected.\n")
	m, err := NewManager(root)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	summary, _, err := m.BoundedSummary()
	if err != nil {
		t.Fatalf("BoundedSummary: %v", err)
	}
	if strings.Contains(summary, "Long body that must not be injected") {
		t.Fatalf("summary leaked record body: %q", summary)
	}
}

func TestParseManagedIndexPreservesUserNotes(t *testing.T) {
	t.Setenv("CHORD_STATE_DIR", filepath.Join(t.TempDir(), "state"))
	root := t.TempDir()
	content := `# My Notes

Hand written paragraph.

<!-- chord:managed:start -->

## Managed Records

- [aaa-1234567890abcdef](.chord/memory/records/aaa-1234567890abcdef.md)
  — One.

- [bbb-1234567890abcdef](.chord/memory/records/bbb-1234567890abcdef.md)
  — Two.

<!-- chord:managed:end -->
`
	writeProjectFile(t, root, "MEMORY.md", content)
	m, err := NewManager(root)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	idx, err := m.LoadIndex()
	if err != nil {
		t.Fatalf("LoadIndex: %v", err)
	}
	if len(idx.Managed) != 2 {
		t.Fatalf("managed entries = %d, want 2", len(idx.Managed))
	}
	if !strings.Contains(idx.Head, "Hand written paragraph") {
		t.Fatalf("head lost user notes: %q", idx.Head)
	}
	if idx.Managed[0].ID != "aaa-1234567890abcdef" || idx.Managed[0].Summary != "One." {
		t.Fatalf("entry 0 = %+v", idx.Managed[0])
	}
}

func TestParseManagedIndexRejectsMalformedMarkers(t *testing.T) {
	cases := []struct {
		name    string
		content string
	}{
		{"missing end", "<!-- chord:managed:start -->\n## Managed Records\n- [a-1234567890abcdef](x.md)\n"},
		{"missing start", "## Managed Records\n- [a-1234567890abcdef](x.md)\n<!-- chord:managed:end -->\n"},
		{"duplicate start", "<!-- chord:managed:start -->\n<!-- chord:managed:start -->\n<!-- chord:managed:end -->\n"},
		{"end before start", "<!-- chord:managed:end -->\n<!-- chord:managed:start -->\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseMemoryFile(tc.content)
			if err == nil {
				t.Fatal("expected ErrManagedMarkers")
			}
			if !strings.Contains(err.Error(), "managed") {
				t.Fatalf("error does not mention managed markers: %v", err)
			}
		})
	}
}

func TestRecordMarshalParseRoundTrip(t *testing.T) {
	r := &Record{
		Type:              TypePitfall,
		Created:           time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC),
		OriginSessionID:   "20260821153000123",
		SourceFingerprint: "fp123",
		Confidence:        ConfidenceReported,
		Outcome:           OutcomePartial,
		Summary:           "Freeze the old persistence target first.",
		Statement:         "Freeze the old persistence target before installing the new session target.",
		Rationale:         "Installing the new target first can persist later events to the wrong session.",
		Application:       "Use this ordering whenever a session persistence target changes.",
		ProjectPaths:      []string{"internal/agent/session_switch.go", "internal/agent/main_persist.go"},
	}
	data, err := MarshalRecord(r)
	if err != nil {
		t.Fatalf("MarshalRecord: %v", err)
	}
	parsed, err := ParseRecord(data)
	if err != nil {
		t.Fatalf("ParseRecord: %v", err)
	}
	if parsed.Type != r.Type || parsed.Confidence != r.Confidence || parsed.Outcome != r.Outcome {
		t.Fatalf("parsed classification mismatch: %+v", parsed)
	}
	if parsed.Statement != r.Statement {
		t.Fatalf("statement = %q, want %q", parsed.Statement, r.Statement)
	}
	if parsed.Rationale != r.Rationale || parsed.Application != r.Application {
		t.Fatalf("record guidance mismatch: %+v", parsed)
	}
	if parsed.Summary != r.Summary {
		t.Fatalf("summary = %q, want %q", parsed.Summary, r.Summary)
	}
	// MarshalRecord sorts paths; compare as a set.
	wantPaths := map[string]bool{"internal/agent/session_switch.go": true, "internal/agent/main_persist.go": true}
	if len(parsed.ProjectPaths) != len(wantPaths) {
		t.Fatalf("paths = %v, want 2 entries", parsed.ProjectPaths)
	}
	for _, p := range parsed.ProjectPaths {
		if !wantPaths[p] {
			t.Fatalf("unexpected path %q", p)
		}
	}
	if parsed.Created.IsZero() {
		t.Fatal("created lost")
	}
}

func TestRecordIDStableAndContentSensitive(t *testing.T) {
	r := &Record{
		Type:              TypeFact,
		OriginSessionID:   "s1",
		SourceFingerprint: "fp",
		Confidence:        ConfidenceUserStated,
		Outcome:           OutcomeSuccess,
		Summary:           "Use focused tests before the full suite",
		Statement:         "Prefer focused verification before broad test suites.",
		Rationale:         "Focused verification gives faster and clearer feedback.",
		Application:       "Run tests for changed packages before broader checks.",
		ProjectPaths:      []string{"a.go"},
	}
	hash1 := r.ContentHash()
	r2 := *r
	if hash1 != r2.ContentHash() {
		t.Fatal("identical records must hash identically")
	}
	r2.Statement = "Prefer focused verification before broad test suites!"
	if hash1 == r2.ContentHash() {
		t.Fatal("changed statement must change hash")
	}
	id1 := RecordID(r.Summary, hash1)
	id2 := RecordID(r.Summary, hash1)
	if id1 != id2 {
		t.Fatal("record ID must be stable")
	}
	if !ValidateRecordID(id1) {
		t.Fatalf("generated ID %q should validate", id1)
	}
	if ValidateRecordID("bad-id") || ValidateRecordID("a--123") || ValidateRecordID("--1234567890abcdef") {
		t.Fatal("malformed IDs must not validate")
	}
	// Content hash must not depend on last_used (not a record field).
	if strings.Contains(r2.Statement, "!") == (hash1 == r2.ContentHash()) {
		t.Fatal("content sensitivity broken")
	}
}

// TestRecordIDMultiByteSummaryRoundTrips guards against byte-level slug
// truncation: a CJK summary longer than the 40-byte slug cap must still
// produce a valid UTF-8 record ID that round-trips ValidateRecordID. Cutting
// mid-rune previously produced an invalid ID that failed the whole commit.
func TestRecordIDMultiByteSummaryRoundTrips(t *testing.T) {
	summary := "回复使用中文并保持简洁的中文摘要说明文字"
	id := RecordID(summary, "0123456789abcdef")
	slug, _, _ := strings.Cut(id, "--")
	if !utf8.ValidString(slug) {
		t.Fatalf("slug %q is not valid UTF-8", slug)
	}
	if len(slug) > 40 {
		t.Fatalf("slug %q exceeds the 40-byte cap", slug)
	}
	if !ValidateRecordID(id) {
		t.Fatalf("record ID %q from a multi-byte summary must round-trip validation", id)
	}
	// Same summary must stay content-stable regardless of the hash part.
	again := RecordID(summary, "fedcba9876543210")
	if !ValidateRecordID(again) {
		t.Fatalf("record ID %q must also validate", again)
	}
}

func TestWriteRecordImmutableConflict(t *testing.T) {
	t.Setenv("CHORD_STATE_DIR", filepath.Join(t.TempDir(), "state"))
	root := t.TempDir()
	m, err := NewManager(root)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	rec := &Record{
		Type:              TypePitfall,
		Created:           time.Now().UTC(),
		OriginSessionID:   "s1",
		SourceFingerprint: "fp1",
		Confidence:        ConfidenceReported,
		Outcome:           OutcomePartial,
		Summary:           "Boundary",
		Statement:         "Freeze before install.",
		Rationale:         "The old target must not receive new-session events.",
		Application:       "Use this ordering at persistence-target transitions.",
	}
	hash := rec.ContentHash()
	rec.ID = RecordID(rec.Summary, hash)
	if err := writeRecordImmutable(m.layout, rec); err != nil {
		t.Fatalf("first write: %v", err)
	}
	// Same content → idempotent.
	if err := writeRecordImmutable(m.layout, rec); err != nil {
		t.Fatalf("idempotent write: %v", err)
	}
	// Different content under same ID → conflict.
	rec2 := *rec
	rec2.Statement = "Different statement."
	rec2.ID = rec.ID
	if err := writeRecordImmutable(m.layout, &rec2); err == nil {
		t.Fatal("expected conflict for different content with same ID")
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
// not re-add the removed entry unless the model produces it as new content.
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

func TestCheckpointRoundTrip(t *testing.T) {
	t.Setenv("CHORD_STATE_DIR", filepath.Join(t.TempDir(), "state"))
	root := t.TempDir()
	m, err := NewManager(root)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if cp, err := LoadCheckpoint(m.layout); err != nil || cp != nil {
		t.Fatalf("missing checkpoint: cp=%v err=%v", cp, err)
	}
	cp := &ExtractionCheckpoint{}
	cp.SetCovered("s1", "fp", 7, 2)
	if err := SaveCheckpoint(m.layout, cp); err != nil {
		t.Fatalf("SaveCheckpoint: %v", err)
	}
	got, err := LoadCheckpoint(m.layout)
	if err != nil {
		t.Fatalf("LoadCheckpoint: %v", err)
	}
	if !got.Covered("s1", "fp") {
		t.Fatalf("checkpoint round trip = %+v", got)
	}
	if got.Covered("s1", "fp-other") || got.Covered("s2", "fp") {
		t.Fatalf("checkpoint must be per session + fingerprint: %+v", got)
	}
	// Coverage is tracked independently per session: covering a second session
	// must not erase the first.
	cp2 := &ExtractionCheckpoint{}
	cp2.SetCovered("s2", "fp2", 3, 0)
	if err := SaveCheckpoint(m.layout, cp2); err != nil {
		t.Fatalf("SaveCheckpoint s2: %v", err)
	}
	got2, err := LoadCheckpoint(m.layout)
	if err != nil {
		t.Fatalf("LoadCheckpoint after s2: %v", err)
	}
	if !got2.Covered("s1", "fp") || !got2.Covered("s2", "fp2") {
		t.Fatalf("second session clobbered first coverage: %+v", got2)
	}
}

func TestExtractionOutputParsing(t *testing.T) {
	valid := `{"candidates":[{"type":"preference","statement":"Prefer focused verification before broad test suites.","rationale":"Focused checks provide faster and clearer feedback.","application":"Run changed-package checks before broader suites.","summary":"Prefer focused verification.","source_role":"user","confidence":"user_stated","outcome":"success","project_paths":["a.go"]}]}`
	out, err := ParseExtractionOutput([]byte(valid), MaxRetirePerSessionRun)
	cands, dropped := outParts(out)
	if err != nil {
		t.Fatalf("ParseExtractionOutput: %v", err)
	}
	if len(dropped) != 0 {
		t.Fatalf("valid candidate dropped: %v", dropped)
	}
	if len(cands) != 1 || cands[0].Type != TypePreference || cands[0].Confidence != ConfidenceUserStated {
		t.Fatalf("candidates = %+v", cands)
	}
	// Empty candidates is a legal no-op.
	empty := `{"candidates":[]}`
	out, err = ParseExtractionOutput([]byte(empty), MaxRetirePerSessionRun)
	cands, dropped = outParts(out)
	if err != nil || len(cands) != 0 || len(dropped) != 0 {
		t.Fatalf("empty candidates: %v %v %v", cands, dropped, err)
	}
	// Malformed JSON is a failure.
	if _, err := ParseExtractionOutput([]byte("{not json"), MaxRetirePerSessionRun); err == nil {
		t.Fatal("expected failure for malformed JSON")
	}
	// Unknown enum is a failure.
	badEnum := `{"candidates":[{"type":"bogus","statement":"x","rationale":"why","application":"how","summary":"x","source_role":"user","confidence":"user_stated","outcome":"success"}]}`
	if _, err := ParseExtractionOutput([]byte(badEnum), MaxRetirePerSessionRun); err == nil {
		t.Fatal("expected failure for unknown enum")
	}
	// user_stated requires source_role user.
	mismatch := `{"candidates":[{"type":"fact","statement":"x","rationale":"why","application":"how","summary":"x","source_role":"assistant","confidence":"user_stated","outcome":"success"}]}`
	if _, err := ParseExtractionOutput([]byte(mismatch), MaxRetirePerSessionRun); err == nil {
		t.Fatal("expected failure for user_stated with assistant source")
	}
	// Path escaping is rejected by dropping the offending candidate; the rest
	// of the output still commits.
	badPath := `{"candidates":[{"type":"fact","statement":"x","rationale":"why","application":"how","summary":"x","source_role":"user","confidence":"user_stated","outcome":"success","project_paths":["../escape"]}]}`
	out, err = ParseExtractionOutput([]byte(badPath), MaxRetirePerSessionRun)
	cands, dropped = outParts(out)
	if err != nil {
		t.Fatalf("escaping path must drop the candidate, not fail the run: %v", err)
	}
	if len(dropped) != 1 || len(cands) != 0 {
		t.Fatalf("escaping path: dropped=%v cands=%v, want 1 drop and no candidates", dropped, cands)
	}
	// A summary that would corrupt the managed index is dropped per-candidate.
	badSummary := `{"candidates":[{"type":"fact","statement":"x","rationale":"why","application":"how","summary":"line one\n<!-- chord:managed:start -->","source_role":"user","confidence":"user_stated","outcome":"success"}]}`
	out, err = ParseExtractionOutput([]byte(badSummary), MaxRetirePerSessionRun)
	cands, dropped = outParts(out)
	if err != nil {
		t.Fatalf("broken summary must drop the candidate, not fail the run: %v", err)
	}
	if len(dropped) != 1 || len(cands) != 0 {
		t.Fatalf("broken summary: dropped=%v cands=%v, want 1 drop and no candidates", dropped, cands)
	}
	// A high-risk candidate is dropped with a reason, and the rest of the
	// output is still returned so the survivors can be committed.
	mixed := `{"candidates":[
		{"type":"fact","statement":"good one","rationale":"why it matters","application":"how to apply it","summary":"good","source_role":"user","confidence":"user_stated","outcome":"success"},
		{"type":"fact","statement":"the key is sk-abcdefghijklmnopqrstuvwx","rationale":"why it matters","application":"how to apply it","summary":"fine","source_role":"user","confidence":"user_stated","outcome":"success"}]}`
	out, err = ParseExtractionOutput([]byte(mixed), MaxRetirePerSessionRun)
	cands, dropped = outParts(out)
	if err != nil {
		t.Fatalf("mixed output must not fail the whole run: %v", err)
	}
	if len(dropped) != 1 {
		t.Fatalf("expected one dropped candidate, got %v", dropped)
	}
	if len(cands) != 1 || cands[0].Statement != "good one" {
		t.Fatalf("survivor lost: %+v", cands)
	}
	// An all-secret output drops everything without failing the run.
	secret := `{"candidates":[{"type":"fact","statement":"the key is sk-abcdefghijklmnopqrstuvwx and the rest is fine","rationale":"why it matters","application":"how to apply it","summary":"fine","source_role":"user","confidence":"user_stated","outcome":"success"}]}`
	out, err = ParseExtractionOutput([]byte(secret), MaxRetirePerSessionRun)
	cands, dropped = outParts(out)
	if err != nil {
		t.Fatalf("secret-only output must not fail the run: %v", err)
	}
	if len(cands) != 0 || len(dropped) != 1 {
		t.Fatalf("secret candidate not dropped: cands=%+v dropped=%v", cands, dropped)
	}
}

// Session-local identifiers are machine-checkable, so the deterministic layer
// drops them per-candidate: a commit SHA or machine-absolute path in durable
// text either goes stale or leaks a local layout into every later session.
// Prompt advice alone did not stop the same shapes from recurring post-rule.
func TestExtractionDropsSessionLocalIdentifiers(t *testing.T) {
	sha := `{"candidates":[{"type":"fact","statement":"Fixed in commit e127cde6 with the new helper.","rationale":"why it matters","application":"how to apply it","summary":"A fix","source_role":"assistant","confidence":"reported","outcome":"success","project_paths":["internal/agent/foo.go"]}]}`
	out, err := ParseExtractionOutput([]byte(sha), MaxRetirePerSessionRun)
	cands, dropped := outParts(out)
	if err != nil {
		t.Fatalf("SHA output must not fail the run: %v", err)
	}
	if len(cands) != 0 || len(dropped) != 1 {
		t.Fatalf("SHA candidate not dropped: cands=%+v dropped=%v", cands, dropped)
	}
	if !strings.Contains(dropped[0], "statement contains a session-local commit SHA") {
		t.Fatalf("SHA drop reason should name the field, got %v", dropped)
	}
	upper := `{"candidates":[{"type":"fact","statement":"Fixed in commit E127CDE6 with the new helper.","rationale":"why it matters","application":"how to apply it","summary":"A fix","source_role":"assistant","confidence":"reported","outcome":"success","project_paths":["internal/agent/foo.go"]}]}`
	out, err = ParseExtractionOutput([]byte(upper), MaxRetirePerSessionRun)
	cands, dropped = outParts(out)
	if err != nil {
		t.Fatalf("uppercase SHA output must not fail the run: %v", err)
	}
	if len(cands) != 0 || len(dropped) != 1 {
		t.Fatalf("uppercase SHA candidate not dropped: cands=%+v dropped=%v", cands, dropped)
	}
	rationaleSHA := `{"candidates":[{"type":"fact","statement":"Keep mechanical lint fixes short.","rationale":"The user said e127cde6 can be reworded.","application":"how to apply it","summary":"A preference","source_role":"assistant","confidence":"reported","outcome":"success","project_paths":["internal/agent/foo.go"]}]}`
	out, err = ParseExtractionOutput([]byte(rationaleSHA), MaxRetirePerSessionRun)
	cands, dropped = outParts(out)
	if err != nil {
		t.Fatalf("rationale SHA output must not fail the run: %v", err)
	}
	if len(cands) != 0 || len(dropped) != 1 {
		t.Fatalf("rationale SHA candidate not dropped: cands=%+v dropped=%v", cands, dropped)
	}
	if !strings.Contains(dropped[0], "rationale contains a session-local commit SHA") {
		t.Fatalf("rationale SHA drop reason should name the field, got %v", dropped)
	}
	abs := `{"candidates":[{"type":"fact","statement":"Config lives on this machine.","rationale":"why it matters","application":"Check /Users/tester/projects/chord for the file.","summary":"A fact","source_role":"assistant","confidence":"reported","outcome":"success","project_paths":["internal/agent/foo.go"]}]}`
	out, err = ParseExtractionOutput([]byte(abs), MaxRetirePerSessionRun)
	cands, dropped = outParts(out)
	if err != nil {
		t.Fatalf("absolute-path output must not fail the run: %v", err)
	}
	if len(cands) != 0 || len(dropped) != 1 {
		t.Fatalf("absolute-path candidate not dropped: cands=%+v dropped=%v", cands, dropped)
	}
	if !strings.Contains(dropped[0], "application contains a machine-absolute path") {
		t.Fatalf("absolute-path drop reason should name the field, got %v", dropped)
	}
	// Best-effort prefixes beyond the common roots, plus Windows drives.
	for _, tc := range []struct {
		name string
		raw  string
	}{
		{"root", `{"candidates":[{"type":"fact","statement":"Config at /root/f must exist.","rationale":"why","application":"how","summary":"s","source_role":"assistant","confidence":"reported","outcome":"success","project_paths":["internal/agent/foo.go"]}]}`},
		{"mnt", `{"candidates":[{"type":"fact","statement":"Data under /mnt/d/f is cached.","rationale":"why","application":"how","summary":"s","source_role":"assistant","confidence":"reported","outcome":"success","project_paths":["internal/agent/foo.go"]}]}`},
		{"drive", `{"candidates":[{"type":"fact","statement":"Log at C:\\Users\\x\\f shows it.","rationale":"why","application":"how","summary":"s","source_role":"assistant","confidence":"reported","outcome":"success","project_paths":["internal/agent/foo.go"]}]}`},
	} {
		out, err := ParseExtractionOutput([]byte(tc.raw), MaxRetirePerSessionRun)
		cands, dropped := outParts(out)
		if err != nil {
			t.Fatalf("%s output must not fail the run: %v", tc.name, err)
		}
		if len(cands) != 0 || len(dropped) != 1 {
			t.Fatalf("%s candidate not dropped: cands=%+v dropped=%v", tc.name, cands, dropped)
		}
	}
	// Pure-digit tokens are issue numbers, counts, timeouts, or dates — never
	// SHAs — and home-relative references stay portable, so all of these commit.
	for _, tc := range []struct {
		name string
		raw  string
	}{
		{"timeout", `{"candidates":[{"type":"fact","statement":"Retry after 3000000 ms backoff.","rationale":"why","application":"how","summary":"s","source_role":"assistant","confidence":"reported","outcome":"success","project_paths":["internal/agent/foo.go"]}]}`},
		{"count", `{"candidates":[{"type":"fact","statement":"Flushed 1234567 rows in one pass.","rationale":"why","application":"how","summary":"s","source_role":"assistant","confidence":"reported","outcome":"success","project_paths":["internal/agent/foo.go"]}]}`},
		{"issue", `{"candidates":[{"type":"fact","statement":"See issue 12345678 for context.","rationale":"why","application":"how","summary":"s","source_role":"assistant","confidence":"reported","outcome":"success","project_paths":["internal/agent/foo.go"]}]}`},
		{"date", `{"candidates":[{"type":"fact","statement":"Decided on 20260918 to keep it.","rationale":"why","application":"how","summary":"s","source_role":"assistant","confidence":"reported","outcome":"success","project_paths":["internal/agent/foo.go"]}]}`},
		{"home", `{"candidates":[{"type":"fact","statement":"Check ~/notes/20260918-x.md for the thread.","rationale":"why","application":"how","summary":"s","source_role":"assistant","confidence":"reported","outcome":"success","project_paths":["internal/agent/foo.go"]}]}`},
	} {
		out, err := ParseExtractionOutput([]byte(tc.raw), MaxRetirePerSessionRun)
		cands, dropped := outParts(out)
		if err != nil {
			t.Fatalf("%s output must not fail: %v", tc.name, err)
		}
		if len(cands) != 1 || len(dropped) != 0 {
			t.Fatalf("%s candidate wrongly dropped: cands=%+v dropped=%v", tc.name, cands, dropped)
		}
	}
	// Ordinary prose without hex tokens or absolute paths still commits, and
	// pure-letter words spelled with a-f letters are not SHAs.
	clean := `{"candidates":[{"type":"fact","statement":"The defaced card face must not be clamped again.","rationale":"why it matters","application":"Check internal/agent/foo.go before changing the render path.","summary":"A fact","source_role":"assistant","confidence":"reported","outcome":"success","project_paths":["internal/agent/foo.go"]}]}`
	out, err = ParseExtractionOutput([]byte(clean), MaxRetirePerSessionRun)
	cands, dropped = outParts(out)
	if err != nil {
		t.Fatalf("clean output must not fail: %v", err)
	}
	if len(cands) != 1 || len(dropped) != 0 {
		t.Fatalf("clean candidate wrongly dropped: cands=%+v dropped=%v", cands, dropped)
	}
}

func TestSanitizeTextRedactsSecrets(t *testing.T) {
	input := "Authorization: Bearer abc123\nx-api-key: def456\npassword=secret12345\nhttps://user:pass@example.com\n-----BEGIN RSA PRIVATE KEY-----\nMIIE\n-----END RSA PRIVATE KEY-----"
	out := SanitizeText(input)
	if strings.Contains(out, "abc123") || strings.Contains(out, "def456") || strings.Contains(out, "secret12345") || strings.Contains(out, "user:pass") || strings.Contains(out, "BEGIN RSA PRIVATE KEY") {
		t.Fatalf("sanitizer leaked secrets: %q", out)
	}
	if !strings.Contains(out, "REDACTED") {
		t.Fatalf("sanitizer produced no redaction marker: %q", out)
	}
	// The redaction keeps the readable prefix (scheme, header, or assignment)
	// so the surrounding text stays intact instead of being collapsed.
	for _, want := range []string{
		"Authorization: Bearer [REDACTED]",
		"x-api-key: [REDACTED]",
		"password=[REDACTED]",
		"https://[REDACTED]@example.com",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("sanitized text should keep prefix %q; got: %q", want, out)
		}
	}
	// Raw bare tokens and PEM blocks are high-risk and must be dropped.
	if !HighRisk("sk-abcdefghijklmnopqrstuvwx") {
		t.Fatal("bare secret token should be high-risk")
	}
	if HighRisk(SanitizeText("sk-abcdefghijklmnopqrstuvwx")) {
		t.Fatal("sanitized text should not be high-risk")
	}
	if !HighRisk("-----BEGIN OPENSSH PRIVATE KEY-----\nAAA\n-----END OPENSSH PRIVATE KEY-----") {
		t.Fatal("PEM block should be high-risk")
	}
}

// The bare-token shapes must require their separator: without it "sk"/"pk"/"rk"
// matched ordinary identifiers, redacting them out of the transcript sent to the
// model and dropping any candidate that mentioned one.
func TestBareSecretTokenDoesNotMatchOrdinaryIdentifiers(t *testing.T) {
	for _, benign := range []string{
		"skipLockedSessions",
		"skipped_records",
		"skeletonization",
		"pkg_resources_loader",
		"rkhunter_configuration",
	} {
		if HighRisk(benign) {
			t.Fatalf("HighRisk(%q) = true, want false", benign)
		}
		if got := SanitizeText(benign); got != benign {
			t.Fatalf("SanitizeText(%q) = %q, want it untouched", benign, got)
		}
	}
	// Real key shapes still match on both separators.
	for _, secret := range []string{
		"sk-abcdefghijklmnopqrstuvwx",
		"sk_live_abcdefghijklmnop",
		"AKIAIOSFODNN7EXAMPLE",
	} {
		if !HighRisk(secret) {
			t.Fatalf("HighRisk(%q) = false, want true", secret)
		}
	}
}

// The extraction checkpoint is rebuildable machine state: an unparsable file
// must be replaced, not turned into a permanent commit failure that no retry
// and no later session can clear.
func TestSaveCheckpointReplacesCorruptFile(t *testing.T) {
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
	if _, err := LoadCheckpoint(m.layout); !errors.Is(err, ErrCorruptCheckpoint) {
		t.Fatalf("LoadCheckpoint err = %v, want ErrCorruptCheckpoint", err)
	}

	cp := &ExtractionCheckpoint{}
	cp.SetCovered("s1", "fp1", 3, 0)
	if err := SaveCheckpoint(m.layout, cp); err != nil {
		t.Fatalf("SaveCheckpoint over corrupt file: %v", err)
	}
	got, err := LoadCheckpoint(m.layout)
	if err != nil {
		t.Fatalf("LoadCheckpoint after save: %v", err)
	}
	if !got.Covered("s1", "fp1") {
		t.Fatalf("checkpoint = %+v, want s1 covered", got)
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

// outParts unpacks the addition-path fields most parser assertions care about,
// tolerating the nil output a failed parse returns.
func outParts(out *ExtractionOutput) ([]Candidate, []string) {
	if out == nil {
		return nil, nil
	}
	return out.Candidates, out.Dropped
}

// The reminder budget truncates the tail of the index, so ordering decides what
// a session actually sees. Entries must render in MEMORY.md's own order: sorting
// by ID would rank by slug spelling, and multi-byte IDs sort last, so they could
// never be injected at all.
func TestBoundedSummaryRendersFileOrderNotIDOrder(t *testing.T) {
	t.Setenv("CHORD_STATE_DIR", filepath.Join(t.TempDir(), "state"))
	root := t.TempDir()
	writeProjectFile(t, root, "MEMORY.md", `<!-- chord:managed:start -->

## Managed Records

- [zebra-last-alphabetically--1111111111111111](.chord/memory/records/zebra-last-alphabetically--1111111111111111.md)
  — Written most recently.
- [alpha-first-alphabetically--2222222222222222](.chord/memory/records/alpha-first-alphabetically--2222222222222222.md)
  — Written earlier.
<!-- chord:managed:end -->
`)
	m, err := NewManager(root)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	summary, active, err := m.BoundedSummary()
	if err != nil {
		t.Fatalf("BoundedSummary: %v", err)
	}
	if !active {
		t.Fatal("expected active summary")
	}
	zebra := strings.Index(summary, "zebra-last-alphabetically")
	alpha := strings.Index(summary, "alpha-first-alphabetically")
	if zebra < 0 || alpha < 0 {
		t.Fatalf("both entries should render: %q", summary)
	}
	if zebra > alpha {
		t.Fatalf("entries were reordered by ID instead of file order: %q", summary)
	}
}

// managedSectionMinTokens is a floor, not a cap. With no User Notes the index
// must be free to use the whole reminder budget; capping it at the floor silently
// hid most of the index from every session.
func TestBoundedSummaryIndexUsesBudgetBeyondFloor(t *testing.T) {
	t.Setenv("CHORD_STATE_DIR", filepath.Join(t.TempDir(), "state"))
	root := t.TempDir()
	var sb strings.Builder
	sb.WriteString("<!-- chord:managed:start -->\n\n## Managed Records\n\n")
	const total = 24
	for i := range total {
		id := fmt.Sprintf("record-number-%02d--aaaaaaaaaaaaaa%02d", i, i)
		fmt.Fprintf(&sb, "- [%s](.chord/memory/records/%s.md)\n  — Summary for entry number %02d.\n", id, id, i)
	}
	sb.WriteString("<!-- chord:managed:end -->\n")
	writeProjectFile(t, root, "MEMORY.md", sb.String())
	m, err := NewManager(root)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	summary, _, err := m.BoundedSummary()
	if err != nil {
		t.Fatalf("BoundedSummary: %v", err)
	}
	rendered := strings.Count(summary, "- [record-number-")
	if rendered <= managedSectionMinTokens/25 {
		t.Fatalf("index rendered only %d/%d entries; the floor is being used as a cap", rendered, total)
	}
	if rendered != total {
		t.Fatalf("index rendered %d/%d entries at the soft limit; the budget should hold a full index", rendered, total)
	}
	// The whole point of the floor is that it bounds nothing when notes are absent,
	// but the total still respects the summary budget.
	if got := (len(summary) + 3) / 4; got > maxSummaryTokens {
		t.Fatalf("summary is %d tokens, over the %d budget", got, maxSummaryTokens)
	}
}

// A long hand-written preamble must not squeeze the index out entirely: notes are
// bounded first and the index keeps at least its floor.
func TestBoundedSummaryLongNotesStillLeaveIndexFloor(t *testing.T) {
	t.Setenv("CHORD_STATE_DIR", filepath.Join(t.TempDir(), "state"))
	root := t.TempDir()
	var sb strings.Builder
	sb.WriteString("# Project Memory\n\n")
	for i := range 400 {
		fmt.Fprintf(&sb, "- Hand-written navigation line number %d that goes on at length.\n", i)
	}
	sb.WriteString("\n<!-- chord:managed:start -->\n\n## Managed Records\n\n")
	for i := range 10 {
		id := fmt.Sprintf("record-number-%02d--bbbbbbbbbbbbbb%02d", i, i)
		fmt.Fprintf(&sb, "- [%s](.chord/memory/records/%s.md)\n  — Summary for entry number %02d.\n", id, id, i)
	}
	sb.WriteString("<!-- chord:managed:end -->\n")
	writeProjectFile(t, root, "MEMORY.md", sb.String())
	m, err := NewManager(root)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	summary, _, err := m.BoundedSummary()
	if err != nil {
		t.Fatalf("BoundedSummary: %v", err)
	}
	if !strings.Contains(summary, "Hand-written navigation line number 0") {
		t.Fatalf("notes prefix missing: %q", summary)
	}
	if got := strings.Count(summary, "- [record-number-"); got == 0 {
		t.Fatalf("long notes squeezed the index out entirely: %q", summary)
	}
}

// A pitfall claims something about this codebase, so it has to be able to point
// at the code. Without a path it is generic advice or a note about the
// assistant's own output — the shape that filled the index with material already
// covered by project instructions. Other types legitimately carry no path.
func TestPitfallWithoutProjectPathsIsDropped(t *testing.T) {
	pitfall := `{"candidates":[{"type":"pitfall","statement":"Do not trust unsupported review summaries.","rationale":"why it matters","application":"how to apply it","summary":"Distrust unsupported summaries.","source_role":"assistant","confidence":"reported","outcome":"uncertain"}]}`
	out, err := ParseExtractionOutput([]byte(pitfall), MaxRetirePerSessionRun)
	if err != nil {
		t.Fatalf("a pathless pitfall must drop, not fail the run: %v", err)
	}
	if len(out.Candidates) != 0 || len(out.Dropped) != 1 {
		t.Fatalf("pathless pitfall: candidates=%+v dropped=%v", out.Candidates, out.Dropped)
	}
	// An environment fact has no code to point at and must still be accepted.
	fact := `{"candidates":[{"type":"fact","statement":"Reference checkouts live under the workspace directory.","rationale":"why it matters","application":"how to apply it","summary":"Reference checkout locations.","source_role":"user","confidence":"user_stated","outcome":"success"}]}`
	out, err = ParseExtractionOutput([]byte(fact), MaxRetirePerSessionRun)
	if err != nil {
		t.Fatalf("ParseExtractionOutput: %v", err)
	}
	if len(out.Candidates) != 1 || len(out.Dropped) != 0 {
		t.Fatalf("pathless fact should survive: candidates=%+v dropped=%v", out.Candidates, out.Dropped)
	}
}

func TestParseExtractionRetireAndPromotionBounds(t *testing.T) {
	over := `{"candidates":[],"retire":[
		{"id":"one--1111111111111111","reason":"already covered by project instructions"},
		{"id":"two--2222222222222222","reason":"no longer true"},
		{"id":"three--3333333333333333","reason":"never belonged"},
		{"id":"four--4444444444444444","reason":"over the limit"}]}`
	out, err := ParseExtractionOutput([]byte(over), MaxRetirePerSessionRun)
	if err != nil {
		t.Fatalf("ParseExtractionOutput: %v", err)
	}
	if len(out.Retire) != MaxRetirePerSessionRun || len(out.Dropped) != 1 {
		t.Fatalf("retire = %+v dropped = %v, want %d kept and 1 dropped", out.Retire, out.Dropped, MaxRetirePerSessionRun)
	}
	// A review run consolidates harder, so the same payload fits.
	out, err = ParseExtractionOutput([]byte(over), MaxRetirePerReviewRun)
	if err != nil {
		t.Fatalf("ParseExtractionOutput review: %v", err)
	}
	if len(out.Retire) != 4 || len(out.Dropped) != 0 {
		t.Fatalf("review retire = %+v dropped = %v", out.Retire, out.Dropped)
	}
	// Shape problems drop the single item; they never fail a batch that also
	// carries valid additions.
	bad := `{"candidates":[],"retire":[{"id":"not a record id","reason":"x"},{"id":"dup--1111111111111111","reason":""}]}`
	out, err = ParseExtractionOutput([]byte(bad), MaxRetirePerSessionRun)
	if err != nil {
		t.Fatalf("malformed retire must drop, not fail: %v", err)
	}
	if len(out.Retire) != 0 || len(out.Dropped) != 2 {
		t.Fatalf("retire = %+v dropped = %v", out.Retire, out.Dropped)
	}
	// An unknown promotion target is an enum violation: a whole-batch failure,
	// consistent with the other enums.
	badTarget := `{"candidates":[],"promotions":[{"target":"somewhere_else","summary":"s","draft_text":"d","reason":"r"}]}`
	if _, err := ParseExtractionOutput([]byte(badTarget), MaxRetirePerSessionRun); err == nil {
		t.Fatal("expected failure for unknown promotion target")
	}
	ok := `{"candidates":[],"promotions":[{"target":"project_docs","summary":"Rarely triggered LSP detail","draft_text":"Root discovery and file-type gating are orthogonal.","reason":"useful but rarely triggered"}]}`
	out, err = ParseExtractionOutput([]byte(ok), MaxRetirePerSessionRun)
	if err != nil {
		t.Fatalf("ParseExtractionOutput promotion: %v", err)
	}
	if len(out.Promotions) != 1 || out.Promotions[0].Target != PromotionProjectDocs {
		t.Fatalf("promotions = %+v", out.Promotions)
	}
	if out.Empty() {
		t.Fatal("a promotion-only run is not empty")
	}
}

// Retiring with no addition still has to rewrite the index: treating it as a
// no-op would make the correction channel silently do nothing.
func TestCommitRetireOnlyRewritesIndexAndKeepsRecordFile(t *testing.T) {
	t.Setenv("CHORD_STATE_DIR", filepath.Join(t.TempDir(), "state"))
	root := t.TempDir()
	m, err := NewManager(root)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	seed := testCandidate(TypeFact, "A conclusion that will be retired.", "Retire me.")
	seed.SourceRole = SourceRoleAssistant
	seed.Confidence = ConfidenceReported
	res, err := m.CommitExtractionCtx(context.Background(), "s1", "fp1", 1, 0, extractionOf(seed))
	if err != nil {
		t.Fatalf("seed commit: %v", err)
	}
	if len(res.Added) != 1 {
		t.Fatalf("seed added = %v", res.Added)
	}
	id := res.Added[0]
	recordPath := filepath.Join(root, ".chord/memory/records", id+".md")
	if _, err := os.Stat(recordPath); err != nil {
		t.Fatalf("seed record missing: %v", err)
	}

	res, err = m.CommitExtractionCtx(context.Background(), "s2", "fp2", 1, 0, &ExtractionOutput{
		Retire: []RetireRequest{{ID: id, Reason: "already covered by project instructions"}},
	})
	if err != nil {
		t.Fatalf("retire commit: %v", err)
	}
	if res.Noop {
		t.Fatal("a retire-only commit must not report a no-op")
	}
	if len(res.Retired) != 1 || res.Retired[0] != id {
		t.Fatalf("retired = %v, want %q", res.Retired, id)
	}
	idx, err := m.LoadIndex()
	if err != nil {
		t.Fatalf("LoadIndex: %v", err)
	}
	if len(idx.Managed) != 0 {
		t.Fatalf("retired entry still indexed: %+v", idx.Managed)
	}
	// Provenance survives: only the index entry goes away, exactly as for a
	// superseded record.
	if _, err := os.Stat(recordPath); err != nil {
		t.Fatalf("retire must keep the record file as an orphan: %v", err)
	}
}

// What the user stated is not the model's to forget. A stale preference is for
// the user to drop or for a promotion to relocate, never for an extraction pass
// to delete on its own.
func TestCommitRefusesToRetireUserStatedRecord(t *testing.T) {
	t.Setenv("CHORD_STATE_DIR", filepath.Join(t.TempDir(), "state"))
	root := t.TempDir()
	m, err := NewManager(root)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	// testCandidate is user_stated by construction.
	res, err := m.CommitExtractionCtx(context.Background(), "s1", "fp1", 1, 0,
		extractionOf(testCandidate(TypePreference, "Always fix mechanical lint directly.", "Fix lint directly.")))
	if err != nil {
		t.Fatalf("seed commit: %v", err)
	}
	id := res.Added[0]

	res, err = m.CommitExtractionCtx(context.Background(), "s2", "fp2", 1, 0, &ExtractionOutput{
		Retire: []RetireRequest{{ID: id, Reason: "looks stale to me"}},
	})
	if err != nil {
		t.Fatalf("refusing a retirement must not fail the commit: %v", err)
	}
	if len(res.Retired) != 0 {
		t.Fatalf("retired = %v, want the user-stated record kept", res.Retired)
	}
	if len(res.Warnings) == 0 {
		t.Fatal("refusal must be surfaced as a warning, not silently dropped")
	}
	idx, err := m.LoadIndex()
	if err != nil {
		t.Fatalf("LoadIndex: %v", err)
	}
	if len(idx.Managed) != 1 || idx.Managed[0].ID != id {
		t.Fatalf("user-stated entry should stay indexed: %+v", idx.Managed)
	}
}

// A promotion writes a pending suggestion file and unindexes its active source,
// so the conclusion leaves every future turn's context until a human accepts
// it. Chord must never touch the authoritative files itself.
func TestCommitPromotionWritesSuggestionAndUnindexes(t *testing.T) {
	t.Setenv("CHORD_STATE_DIR", filepath.Join(t.TempDir(), "state"))
	root := t.TempDir()
	m, err := NewManager(root)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	reported := testCandidate(TypePreference, "Commits must be authored by the project owner.", "Commit author rule.")
	reported.Confidence = ConfidenceReported
	res, err := m.CommitExtractionCtx(context.Background(), "s1", "fp1", 1, 0, extractionOf(reported))
	if err != nil {
		t.Fatalf("seed commit: %v", err)
	}
	id := res.Added[0]

	res, err = m.CommitExtractionCtx(context.Background(), "s2", "fp2", 1, 0, &ExtractionOutput{
		Promotions: []Promotion{{
			SourceID:          id,
			Target:            PromotionProjectInstructions,
			Summary:           "Commit author rule belongs in project instructions",
			SuggestedLocation: "Commit rules",
			DraftText:         "Commits must be authored by the project owner.",
			Reason:            "mandatory and repo-wide",
		}},
	})
	if err != nil {
		t.Fatalf("promotion commit: %v", err)
	}
	if res.Promoted != 1 {
		t.Fatalf("promoted = %d, want 1", res.Promoted)
	}
	entries, err := os.ReadDir(filepath.Join(root, ".chord/memory/promotions"))
	if err != nil {
		t.Fatalf("promotions dir missing: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("promotions dir has %d entries, want exactly one suggestion", len(entries))
	}
	data, err := os.ReadFile(filepath.Join(root, ".chord/memory/promotions", entries[0].Name()))
	if err != nil {
		t.Fatalf("suggestion file unreadable: %v", err)
	}
	for _, want := range []string{"project_instructions", "Commit rules", id, "authored by the project owner"} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("suggestion file missing %q: %s", want, data)
		}
	}
	idx, err := m.LoadIndex()
	if err != nil {
		t.Fatalf("LoadIndex: %v", err)
	}
	if len(idx.Managed) != 0 {
		t.Fatalf("promoted entry still indexed: %+v", idx.Managed)
	}
	// Chord proposes; it never edits the authoritative file itself.
	if _, err := os.Stat(filepath.Join(root, "AGENTS.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("promotion must not create or edit AGENTS.md, stat err = %v", err)
	}
}

// The pending queue is human-reviewed; extraction reads it only to avoid
// suggesting the same conclusion twice across sessions. Titles come back newest
// first, malformed files are skipped, and a missing directory is an empty view.
func TestPendingPromotionSummariesNewestFirst(t *testing.T) {
	t.Setenv("CHORD_STATE_DIR", filepath.Join(t.TempDir(), "state"))
	root := t.TempDir()
	m, err := NewManager(root)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if pending, err := m.PendingPromotionSummaries(10); err != nil || len(pending) != 0 {
		t.Fatalf("empty promotions dir = %v, %v", pending, err)
	}
	dir := filepath.Join(root, ".chord/memory/promotions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir promotions: %v", err)
	}
	write := func(name, title string, mod time.Time) {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("# Pending promotion: "+title+"\n\ndraft body\n"), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		if err := os.Chtimes(path, mod, mod); err != nil {
			t.Fatalf("chtimes %s: %v", name, err)
		}
	}
	older := time.Now().Add(-2 * time.Hour)
	newer := time.Now().Add(-time.Hour)
	write("older--1111111111111111.md", "Older suggestion", older)
	write("newer--2222222222222222.md", "Newer suggestion", newer)
	bad := filepath.Join(dir, "bad--3333333333333333.md")
	if err := os.WriteFile(bad, []byte("# Not a promotion: ignored\n"), 0o644); err != nil {
		t.Fatalf("write bad: %v", err)
	}
	if err := os.Chtimes(bad, newer, newer); err != nil {
		t.Fatalf("chtimes bad: %v", err)
	}
	pending, err := m.PendingPromotionSummaries(10)
	if err != nil {
		t.Fatalf("PendingPromotionSummaries: %v", err)
	}
	if len(pending) != 2 || pending[0] != "Newer suggestion" || pending[1] != "Older suggestion" {
		t.Fatalf("pending = %+v, want newest first with the malformed file skipped", pending)
	}
	if capped, err := m.PendingPromotionSummaries(1); err != nil || len(capped) != 1 || capped[0] != "Newer suggestion" {
		t.Fatalf("capped = %v, %v", capped, err)
	}
	if none, err := m.PendingPromotionSummaries(0); err != nil || len(none) != 0 {
		t.Fatalf("zero cap = %v, %v", none, err)
	}
}

// A promotion never unindexes a user-stated record on its own: until a human
// accepts the suggestion, the indexed entry is what every future session sees.
// The suggestion file itself is still written for review.
func TestCommitPromotionKeepsUserStatedSourceIndexed(t *testing.T) {
	t.Setenv("CHORD_STATE_DIR", filepath.Join(t.TempDir(), "state"))
	root := t.TempDir()
	m, err := NewManager(root)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	res, err := m.CommitExtractionCtx(context.Background(), "s1", "fp1", 1, 0,
		extractionOf(testCandidate(TypePreference, "Always answer in the reviewer's language.", "Answer language rule.")))
	if err != nil {
		t.Fatalf("seed commit: %v", err)
	}
	id := res.Added[0]

	res, err = m.CommitExtractionCtx(context.Background(), "s2", "fp2", 1, 0, &ExtractionOutput{
		Promotions: []Promotion{{
			SourceID:  id,
			Target:    PromotionProjectInstructions,
			Summary:   "Answer language rule belongs in project instructions",
			DraftText: "Always answer in the reviewer's language.",
			Reason:    "stated as a lasting preference",
		}},
	})
	if err != nil {
		t.Fatalf("promotion commit: %v", err)
	}
	if res.Promoted != 1 {
		t.Fatalf("promoted = %d, want 1", res.Promoted)
	}
	entries, err := os.ReadDir(filepath.Join(root, ".chord/memory/promotions"))
	if err != nil {
		t.Fatalf("promotions dir missing: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("promotions dir has %d entries, want exactly one suggestion", len(entries))
	}
	idx, err := m.LoadIndex()
	if err != nil {
		t.Fatalf("LoadIndex: %v", err)
	}
	if len(idx.Managed) != 1 || idx.Managed[0].ID != id {
		t.Fatalf("user-stated source should stay indexed: %+v", idx.Managed)
	}
	found := false
	for _, w := range res.Warnings {
		if strings.Contains(w, id) {
			found = true
		}
	}
	if !found {
		t.Fatalf("keeping the entry must be surfaced as a warning, got %v", res.Warnings)
	}
}

// Re-running a commit after a late failure or cancellation rewrites the same
// suggestions; content-addressed exclusive-create must keep them deduplicated.
func TestWritePromotionFilesIdempotent(t *testing.T) {
	dir := t.TempDir()
	l := &Layout{PromotionsDir: filepath.Join(dir, ".chord/memory/promotions")}
	promos := []Promotion{{
		Target:    PromotionProjectInstructions,
		Summary:   "Keep localized docs in sync",
		DraftText: "Apply wording changes to every language variant at once.",
		Reason:    "docs drift when only one is edited",
	}}
	if err := writePromotionFiles(l, "s1", promos); err != nil {
		t.Fatalf("first write: %v", err)
	}
	// A retry from a different attempt (even a different session key) writes the
	// identical canonical content and must land on the same file.
	if err := writePromotionFiles(l, "s2", promos); err != nil {
		t.Fatalf("retry write: %v", err)
	}
	entries, err := os.ReadDir(l.PromotionsDir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	if len(entries) != 1 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("got %d files %v, want the single idempotent suggestion", len(entries), names)
	}
	data, err := os.ReadFile(filepath.Join(l.PromotionsDir, entries[0].Name()))
	if err != nil {
		t.Fatalf("read suggestion: %v", err)
	}
	if !strings.Contains(string(data), "- Session: s1") {
		t.Fatalf("suggestion should carry the first writing session: %s", data)
	}
}

// Ordering is load-bearing because the reminder truncates the tail: new entries
// go first so recent learning stays inside the injected prefix, and existing
// order (including a manual reorder) is preserved instead of being reshuffled.
func TestBuildManagedIndexPrependsNewAndPreservesOrder(t *testing.T) {
	existing := &MemoryIndex{
		Managed: []ManagedEntry{
			{ID: "first--1111111111111111", Link: ".chord/memory/records/first--1111111111111111.md", Summary: "First"},
			{ID: "second--2222222222222222", Link: ".chord/memory/records/second--2222222222222222.md", Summary: "Second"},
			{ID: "third--3333333333333333", Link: ".chord/memory/records/third--3333333333333333.md", Summary: "Third"},
		},
		HasManagedMarker: true,
	}
	merged, err := BuildManagedIndexReplacing(existing,
		[]ManagedEntry{
			{ID: "fresh--4444444444444444", Link: ".chord/memory/records/fresh--4444444444444444.md", Summary: "Fresh"},
			{ID: "second--2222222222222222", Link: ".chord/memory/records/second--2222222222222222.md", Summary: "Second, restated"},
		},
		[]string{"third--3333333333333333"})
	if err != nil {
		t.Fatalf("BuildManagedIndexReplacing: %v", err)
	}
	idx, err := parseMemoryFile(merged)
	if err != nil {
		t.Fatalf("parseMemoryFile: %v", err)
	}
	var ids []string
	for _, e := range idx.Managed {
		ids = append(ids, e.ID)
	}
	want := []string{"fresh--4444444444444444", "first--1111111111111111", "second--2222222222222222"}
	if len(ids) != len(want) {
		t.Fatalf("ids = %v, want %v", ids, want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("ids = %v, want %v", ids, want)
		}
	}
	// A restated entry is updated in place, not moved to the front.
	if idx.Managed[2].Summary != "Second, restated" {
		t.Fatalf("restated summary = %q", idx.Managed[2].Summary)
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

// TestExtractionKeepsIdentifierBoundHexRuns pins the boundary the SHA drop
// must not cross: a hex run glued into a longer identifier by '-' or '_' is a
// UUID segment or this store's own <slug>--<hash> record id, both of which a
// later session still resolves. Dropping them would make a record that cites
// the one it supersedes unrepresentable. A bare SHA keeps dropping.
func TestExtractionKeepsIdentifierBoundHexRuns(t *testing.T) {
	candidate := func(statement string) string {
		return `{"candidates":[{"type":"fact","statement":"` + statement + `","rationale":"why","application":"how","summary":"s","source_role":"assistant","confidence":"reported","outcome":"success","project_paths":["internal/agent/foo.go"]}]}`
	}
	for _, tc := range []struct {
		name string
		raw  string
	}{
		{"uuid", `The export dataset is 550e8400-e29b-41d4-a716-446655440abc.`},
		{"recordid", `Supersedes retry-backoff--3f2a1b4c9d8e7f60.`},
		{"snake", `Keyed by build_id 3f2a1b4c9d8e7f60_generated.`},
	} {
		out, err := ParseExtractionOutput([]byte(candidate(tc.raw)), MaxRetirePerSessionRun)
		if err != nil {
			t.Fatalf("%s output must not fail: %v", tc.name, err)
		}
		cands, dropped := outParts(out)
		if len(cands) != 1 || len(dropped) != 0 {
			t.Fatalf("%s candidate wrongly dropped: cands=%+v dropped=%v", tc.name, cands, dropped)
		}
	}
	// Sentence punctuation is not a joiner: a bare SHA still drops.
	out, err := ParseExtractionOutput([]byte(candidate("Use e127cde6 for the rebase.")), MaxRetirePerSessionRun)
	if err != nil {
		t.Fatalf("bare SHA output must not fail: %v", err)
	}
	cands, dropped := outParts(out)
	if len(cands) != 0 || len(dropped) != 1 {
		t.Fatalf("bare SHA candidate not dropped: cands=%+v dropped=%v", cands, dropped)
	}
}
