package memory

import (
	"context"
	"errors"
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
	if _, err := m.CommitExtractionCtx(context.Background(), "s1", "fp1", 1, 0, []Candidate{testCandidate(TypeFact, "Facts stay fresh.", "Facts stay fresh.")}); err != nil {
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
	if _, err := m2.CommitExtractionCtx(context.Background(), "s1", "fp1", 1, 0, []Candidate{testCandidate(TypeFact, "New facts.", "New facts.")}); err != nil {
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

	if _, err := m.CommitExtractionCtx(ctx, "s1", "fp1", 1, 0, []Candidate{testCandidate(TypeFact, "Facts stay fresh.", "Facts stay fresh.")}); err == nil {
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
		res, cerr := m.CommitExtractionCtx(context.Background(), "s1", "fp1", 1, 0, []Candidate{testCandidate(TypeFact, "Fact", "Fact")})
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
	res, err := m.CommitExtractionCtx(context.Background(), "20260821153000123", "fp-abcdef", 42, 3, candidates)
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
	if _, err := m.CommitExtractionCtx(context.Background(), "s1", "fp1", 1, 0, candidates); err != nil {
		t.Fatalf("first commit: %v", err)
	}
	res, err := m.CommitExtractionCtx(context.Background(), "s1", "fp1", 1, 0, candidates)
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
	if _, err := m.CommitExtractionCtx(context.Background(), "s1", "fp-old", 5, 0, c1); err != nil {
		t.Fatalf("commit 1: %v", err)
	}
	// New fingerprint (session grew) must be allowed to extract again.
	c2 := []Candidate{testCandidate(TypeFact, "Fact B", "B")}
	res, err := m.CommitExtractionCtx(context.Background(), "s1", "fp-new", 9, 1, c2)
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
	res, err := m.CommitExtractionCtx(context.Background(), "s1", "fp1", 1, 0, []Candidate{first})
	if err != nil {
		t.Fatalf("first commit: %v", err)
	}
	second := first
	second.Rationale = "A differently worded explanation from another session."
	res2, err := m.CommitExtractionCtx(context.Background(), "s2", "fp2", 1, 0, []Candidate{second})
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
	res, err := m.CommitExtractionCtx(context.Background(), "s1", "fp1", 1, 0, []Candidate{old})
	if err != nil {
		t.Fatalf("old commit: %v", err)
	}
	oldID := res.Added[0]
	replacement := testCandidate(TypePreference, "Prefer the expanded display for diagnostics.", "Prefer expanded diagnostics.")
	replacement.Supersedes = []string{oldID}
	res2, err := m.CommitExtractionCtx(context.Background(), "s2", "fp2", 1, 0, []Candidate{replacement})
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
	if _, err := m.CommitExtractionCtx(context.Background(), "s1", "fp1", 1, 0, []Candidate{candidate}); err == nil {
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
	if _, err := m.CommitExtractionCtx(context.Background(), "s1", "fp1", 1, 0, candidates); err != nil {
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
	if _, err := m.CommitExtractionCtx(context.Background(), "s1", "fp1", 1, 0, candidates); err == nil {
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
	if _, err := m.CommitExtractionCtx(context.Background(), "s1", "fp1", 1, 0, candidates); err != nil {
		t.Fatalf("first commit: %v", err)
	}
	// User edits the file externally (no lock) with new notes.
	writeProjectFile(t, root, "MEMORY.md", "# Project Memory\n\nBrand new user edit.\n")
	// A second extraction must re-merge against the new content, not clobber it.
	c2 := []Candidate{testCandidate(TypeFact, "Fact 2", "Fact 2")}
	if _, err := m.CommitExtractionCtx(context.Background(), "s1", "fp2", 1, 0, c2); err != nil {
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
	res, err := m.CommitExtractionCtx(context.Background(), "s1", "fp1", 1, 0, candidates)
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
	res2, err := m.CommitExtractionCtx(context.Background(), "s2", "fp2", 1, 0, []Candidate{testCandidate(TypeFact, "Another", "Another")})
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
	if _, err := m.CommitExtractionCtx(context.Background(), "s1", "fp1", 1, 0, candidates); err != nil {
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
	cands, dropped, err := ParseExtractionOutput([]byte(valid))
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
	cands, dropped, err = ParseExtractionOutput([]byte(empty))
	if err != nil || len(cands) != 0 || len(dropped) != 0 {
		t.Fatalf("empty candidates: %v %v %v", cands, dropped, err)
	}
	// Malformed JSON is a failure.
	if _, _, err := ParseExtractionOutput([]byte("{not json")); err == nil {
		t.Fatal("expected failure for malformed JSON")
	}
	// Unknown enum is a failure.
	badEnum := `{"candidates":[{"type":"bogus","statement":"x","rationale":"why","application":"how","summary":"x","source_role":"user","confidence":"user_stated","outcome":"success"}]}`
	if _, _, err := ParseExtractionOutput([]byte(badEnum)); err == nil {
		t.Fatal("expected failure for unknown enum")
	}
	// user_stated requires source_role user.
	mismatch := `{"candidates":[{"type":"fact","statement":"x","rationale":"why","application":"how","summary":"x","source_role":"assistant","confidence":"user_stated","outcome":"success"}]}`
	if _, _, err := ParseExtractionOutput([]byte(mismatch)); err == nil {
		t.Fatal("expected failure for user_stated with assistant source")
	}
	// Path escaping is rejected by dropping the offending candidate; the rest
	// of the output still commits.
	badPath := `{"candidates":[{"type":"fact","statement":"x","rationale":"why","application":"how","summary":"x","source_role":"user","confidence":"user_stated","outcome":"success","project_paths":["../escape"]}]}`
	cands, dropped, err = ParseExtractionOutput([]byte(badPath))
	if err != nil {
		t.Fatalf("escaping path must drop the candidate, not fail the run: %v", err)
	}
	if len(dropped) != 1 || len(cands) != 0 {
		t.Fatalf("escaping path: dropped=%v cands=%v, want 1 drop and no candidates", dropped, cands)
	}
	// A summary that would corrupt the managed index is dropped per-candidate.
	badSummary := `{"candidates":[{"type":"fact","statement":"x","rationale":"why","application":"how","summary":"line one\n<!-- chord:managed:start -->","source_role":"user","confidence":"user_stated","outcome":"success"}]}`
	cands, dropped, err = ParseExtractionOutput([]byte(badSummary))
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
	cands, dropped, err = ParseExtractionOutput([]byte(mixed))
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
	cands, dropped, err = ParseExtractionOutput([]byte(secret))
	if err != nil {
		t.Fatalf("secret-only output must not fail the run: %v", err)
	}
	if len(cands) != 0 || len(dropped) != 1 {
		t.Fatalf("secret candidate not dropped: cands=%+v dropped=%v", cands, dropped)
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
