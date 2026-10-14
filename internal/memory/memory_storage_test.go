package memory

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

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
	created, err := writeRecordImmutable(m.layout, rec)
	if err != nil || !created {
		t.Fatalf("first write: created=%v err=%v", created, err)
	}
	// Same content → idempotent, and reported as not-created so the caller can
	// keep the record an orphan instead of re-indexing it.
	created, err = writeRecordImmutable(m.layout, rec)
	if err != nil || created {
		t.Fatalf("idempotent write: created=%v err=%v", created, err)
	}
	// Different content under same ID → conflict.
	rec2 := *rec
	rec2.Statement = "Different statement."
	rec2.ID = rec.ID
	if _, err := writeRecordImmutable(m.layout, &rec2); err == nil {
		t.Fatal("expected conflict for different content with same ID")
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
