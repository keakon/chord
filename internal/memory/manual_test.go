package memory

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/config"
)

func manualTestManager(t *testing.T) *Manager {
	t.Helper()
	root := t.TempDir()
	state := t.TempDir()
	m, err := NewManager(root, &config.PathLocator{StateDir: state, ConfigHome: state, SessionsRoot: filepath.Join(state, "sessions"), CacheDir: filepath.Join(state, "cache"), LogsDir: filepath.Join(state, "logs"), ExportsDir: filepath.Join(state, "exports")})
	if err != nil {
		t.Fatal(err)
	}
	for n, text := range []string{"Prefer concise reports.", "Keep conditions in reports."} {
		if _, err = m.CommitExtractionCtx(context.Background(), "session", "source-"+string(rune('a'+n)), 1, 0, extractionOf(testCandidate(TypePreference, text, text))); err != nil {
			t.Fatal(err)
		}
	}
	return m
}

func TestManualRemovalUndoKeepsConcurrentAddition(t *testing.T) {
	m := manualTestManager(t)
	ctx := context.Background()
	snap, err := m.Review(ctx)
	if err != nil {
		t.Fatal(err)
	}
	id := snap.Items[0].Entry.ID
	subset, err := snap.Select([]string{id})
	if err != nil {
		t.Fatal(err)
	}
	writeProjectFile(t, m.layout.ProjectRoot, ProjectIndexName, "User notes\n"+snap.Index.Raw+"Tail notes\n")
	// A notes edit is not a change to the targeted entry.
	if err = m.ApplyManual(ctx, NewRemovalDraft(subset)); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(subset.Items[0].Path); err != nil {
		t.Fatalf("removed immutable record: %v", err)
	}
	res, err := m.CommitExtractionCtx(ctx, "another", "another-source", 1, 0, extractionOf(testCandidate(TypeFact, "A stable configuration fact.", "Configuration fact")))
	if err != nil {
		t.Fatal(err)
	}
	// Undo works from a new Manager, without process-local or git state.
	other := &Manager{layout: m.layout}
	if err = other.UndoManual(ctx); err != nil {
		t.Fatal(err)
	}
	idx, err := m.LoadIndex()
	if err != nil {
		t.Fatal(err)
	}
	if len(idx.Managed) != 3 || idx.Managed[0].ID != res.Added[0] {
		t.Fatalf("lost concurrent entry: %+v", idx.Managed)
	}
	if _, ok := findReviewEntry(idx.Managed, id); !ok {
		t.Fatal("removed entry was not restored")
	}
	if !strings.Contains(idx.Raw, "User notes") || !strings.Contains(idx.Raw, "Tail notes") {
		t.Fatal("user notes lost")
	}
	if err = other.UndoManual(ctx); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("undo should be consumed: %v", err)
	}
}

func TestManualRemovalRejectsStaleRecord(t *testing.T) {
	m := manualTestManager(t)
	snap, err := m.Review(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	base, err := snap.Select([]string{snap.Items[0].Entry.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(base.Items[0].Path, []byte(base.Items[0].Content+"\nChanged externally"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err = m.ApplyManual(context.Background(), NewRemovalDraft(base)); !errors.Is(err, ErrReviewConflict) {
		t.Fatalf("wanted stale preview rejection: %v", err)
	}
	idx, _ := m.LoadIndex()
	if len(idx.Managed) != 2 {
		t.Fatal("stale removal changed index")
	}
}

func TestManualOrganizationRequiresSourcesAndPreservesTrust(t *testing.T) {
	m := manualTestManager(t)
	ctx := context.Background()
	snap, err := m.Review(ctx)
	if err != nil {
		t.Fatal(err)
	}
	candidate := testCandidate(TypePreference, "Prefer concise reports while retaining conditions.", "Report preferences")
	for _, i := range snap.Items {
		candidate.Supersedes = append(candidate.Supersedes, i.Entry.ID)
	}
	data, _ := json.Marshal(map[string]any{"candidates": []Candidate{candidate}, "retire": []RetireRequest{}, "issues": []string{"An unresolved question."}})
	draft, err := ParseOrganization(snap, true, "review-session", data)
	if err != nil {
		t.Fatal(err)
	}
	if draft.Records[0].Confidence != ConfidenceUncertain || draft.Records[0].Outcome != OutcomeUncertain {
		t.Fatal("organization upgraded trust")
	}
	before, _ := m.LoadIndex()
	if before.Raw != snap.Index.Raw {
		t.Fatal("generation wrote files")
	}
	if err = m.ApplyManual(ctx, draft); err != nil {
		t.Fatal(err)
	}
	idx, _ := m.LoadIndex()
	if len(idx.Managed) != 1 {
		t.Fatalf("organization not applied: %+v", idx.Managed)
	}
	record, err := loadRecord(recordPath(m.layout.RecordsDir, idx.Managed[0].ID))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(record.Supersedes, candidate.Supersedes) {
		t.Fatal("source lineage lost")
	}
	if err = m.UndoManual(ctx); err != nil {
		t.Fatal(err)
	}
	idx, _ = m.LoadIndex()
	if !slices.Equal(idx.Managed, snap.Index.Managed) {
		t.Fatalf("undo did not restore ordering: %+v", idx.Managed)
	}
	candidate.Supersedes = []string{"unselected--1111111111111111"}
	data, _ = json.Marshal(map[string]any{"candidates": []Candidate{candidate}})
	if _, err = ParseOrganization(snap, true, "review-session", data); err == nil {
		t.Fatal("accepted unselected source")
	}
	candidate.Supersedes = nil
	data, _ = json.Marshal(map[string]any{"candidates": []Candidate{candidate}})
	if _, err = ParseOrganization(snap, true, "review-session", data); err == nil {
		t.Fatal("accepted sourceless replacement")
	}
}

func TestManualFullOrganizationRejectsConcurrentIndexChange(t *testing.T) {
	m := manualTestManager(t)
	snap, err := m.Review(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	draft := NewRemovalDraft(snap)
	draft.All = true
	_, err = m.CommitExtractionCtx(context.Background(), "new", "new-source", 1, 0, extractionOf(testCandidate(TypeFact, "New project fact.", "New fact")))
	if err != nil {
		t.Fatal(err)
	}
	if err = m.ApplyManual(context.Background(), draft); !errors.Is(err, ErrReviewConflict) {
		t.Fatalf("wanted conflict: %v", err)
	}
}

func TestManualCommitRecoveryRecognizesCommittedIndex(t *testing.T) {
	m := manualTestManager(t)
	snap, err := m.Review(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	before := snap.Index.Managed
	after := before[1:]
	op := &manualUndo{Before: before, After: after, Changed: []string{before[0].ID}}
	if err = m.saveManualPending(op); err != nil {
		t.Fatal(err)
	}
	idx := *snap.Index
	idx.Managed = after
	if err = writeMemoryFileAtomic(m.layout, renderManagedMarkdown(&idx)); err != nil {
		t.Fatal(err)
	}
	refreshed, err := m.Review(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !refreshed.CanUndo {
		t.Fatal("committed operation did not recover undo")
	}
	if err = m.UndoManual(context.Background()); err != nil {
		t.Fatal(err)
	}
	idxNow, _ := m.LoadIndex()
	if !slices.Equal(before, idxNow.Managed) {
		t.Fatal("recovered undo did not restore entries")
	}
}

func TestReviewRejectsEscapingLinkAndSupportsBrokenRemoval(t *testing.T) {
	m := manualTestManager(t)
	snap, err := m.Review(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	idx := *snap.Index
	idx.Managed = append([]ManagedEntry(nil), idx.Managed...)
	idx.Managed[0].Link = "../outside.md"
	if err = writeMemoryFileAtomic(m.layout, renderManagedMarkdown(&idx)); err != nil {
		t.Fatal(err)
	}
	snap, err = m.Review(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snap.Items[0].Record != nil || snap.Items[0].Error == "" {
		t.Fatal("escaping record link was followed")
	}
	subset, _ := snap.Select([]string{snap.Items[0].Entry.ID})
	if err = m.ApplyManual(context.Background(), NewRemovalDraft(subset)); err != nil {
		t.Fatal(err)
	}
}

func TestManualUndoRefusesDamagedOriginal(t *testing.T) {
	m := manualTestManager(t)
	ctx := context.Background()
	snap, err := m.Review(ctx)
	if err != nil {
		t.Fatal(err)
	}
	base, _ := snap.Select([]string{snap.Items[0].Entry.ID})
	if err = m.ApplyManual(ctx, NewRemovalDraft(base)); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(base.Items[0].Path, []byte("broken record"), 0o644); err != nil {
		t.Fatal(err)
	}
	before, _ := m.LoadIndex()
	if err = m.UndoManual(ctx); !errors.Is(err, ErrReviewConflict) {
		t.Fatalf("damaged original accepted: %v", err)
	}
	after, _ := m.LoadIndex()
	if before.Raw != after.Raw {
		t.Fatal("failed undo changed index")
	}
}

func TestManualCommitRecoveryRejectsAmbiguousState(t *testing.T) {
	for _, committed := range []bool{false, true} {
		t.Run(map[bool]string{false: "prepared", true: "conflict"}[committed], func(t *testing.T) {
			m := manualTestManager(t)
			snap, err := m.Review(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			op := &manualUndo{Before: snap.Index.Managed, After: snap.Index.Managed[1:], Changed: []string{snap.Items[0].Entry.ID}}
			if err = m.saveManualPending(op); err != nil {
				t.Fatal(err)
			}
			if committed {
				idx := *snap.Index
				idx.Managed = append([]ManagedEntry(nil), idx.Managed...)
				idx.Managed[0].Summary = "External edit"
				if err = writeMemoryFileAtomic(m.layout, renderManagedMarkdown(&idx)); err != nil {
					t.Fatal(err)
				}
			}
			_, err = m.Review(context.Background())
			if committed && !errors.Is(err, ErrReviewConflict) {
				t.Fatalf("wanted conflict: %v", err)
			}
			if !committed && err != nil {
				t.Fatal(err)
			}
			if !committed {
				if _, err = os.Stat(m.manualPendingPath()); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("prepared journal not cleared")
				}
			}
		})
	}
}

func TestManualDraftRejectsIncompleteAllAndSourceCollision(t *testing.T) {
	m := manualTestManager(t)
	ctx := context.Background()
	snap, err := m.Review(ctx)
	if err != nil {
		t.Fatal(err)
	}
	partial, _ := snap.Select([]string{snap.Items[0].Entry.ID})
	incomplete := NewRemovalDraft(partial)
	incomplete.All = true
	if err = m.ApplyManual(ctx, incomplete); err == nil {
		t.Fatal("incomplete all accepted")
	}
	c := testCandidate(TypePreference, "Consolidated reporting preference.", "Reporting preference")
	c.Supersedes = []string{snap.Items[0].Entry.ID}
	data, _ := json.Marshal(map[string]any{"candidates": []Candidate{c}})
	d, err := ParseOrganization(partial, false, "review", data)
	if err != nil {
		t.Fatal(err)
	}
	orphan := *d.Records[0]
	orphan.Supersedes = []string{snap.Items[1].Entry.ID}
	if _, err = writeRecordImmutable(m.layout, &orphan); err != nil {
		t.Fatal(err)
	}
	if err = m.ApplyManual(ctx, d); err == nil {
		t.Fatal("existing record with different sources reused")
	}
	idx, _ := m.LoadIndex()
	if idx.Raw != snap.Index.Raw {
		t.Fatal("collision changed index")
	}
}
