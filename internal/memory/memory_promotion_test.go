package memory

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

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

// Supersede is a removal with a replacement, but the replacement is model
// text: merging a user-stated entry into a consolidated candidate must not
// silently drop it from the index, same as retirement and promotion.
func TestCommitSupersedeKeepsUserStatedSourceIndexed(t *testing.T) {
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
	statedID := res.Added[0]

	merged := testCandidate(TypePreference, "Fix mechanical lint directly in the same pass.", "Lint handling.")
	merged.Supersedes = []string{statedID}
	res, err = m.CommitExtractionCtx(context.Background(), "s2", "fp2", 1, 0, extractionOf(merged))
	if err != nil {
		t.Fatalf("superseding a user-stated record must not fail the commit: %v", err)
	}
	if len(res.Superseded) != 0 {
		t.Fatalf("superseded = %v, want the user-stated record kept indexed", res.Superseded)
	}
	if len(res.Added) != 1 {
		t.Fatalf("added = %v, want the merged candidate committed", res.Added)
	}
	if len(res.Warnings) == 0 {
		t.Fatal("the kept entry must be surfaced as a warning, not silently retained")
	}
	idx, err := m.LoadIndex()
	if err != nil {
		t.Fatalf("LoadIndex: %v", err)
	}
	if len(idx.Managed) != 2 {
		t.Fatalf("managed entries = %+v, want the user-stated entry and the merged candidate", idx.Managed)
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
