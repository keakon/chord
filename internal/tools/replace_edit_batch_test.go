package tools

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestEditBatchUsesOriginalText(t *testing.T) {
	dir := t.TempDir()
	path := writeEditFixture(t, dir, "sample.txt", "alpha middle alpha omega\n")
	result, err := runEdit(t, dir, map[string]any{
		"path": path,
		"edits": []map[string]any{
			{"old_string": "alpha", "new_string": "omega", "replace_all": true},
			{"old_string": "omega", "new_string": "last"},
			{"old_string": "middle ", "new_string": ""},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "omega omega last\n" {
		t.Fatalf("content = %q", got)
	}
	if !strings.Contains(result, "4 replacements") {
		t.Fatalf("result = %s", result)
	}
}

func TestEditBatchRejectsWithoutWriting(t *testing.T) {
	for _, tc := range []struct {
		name  string
		edits []map[string]any
		want  string
	}{
		{"empty", []map[string]any{}, "at least one"},
		{"missing", []map[string]any{{"old_string": "alpha", "new_string": "changed"}, {"old_string": "absent", "new_string": "text"}}, "edits[1]"},
		{"dependent", []map[string]any{{"old_string": "alpha", "new_string": "changed"}, {"old_string": "changed", "new_string": "text"}}, "original file"},
		{"overlap", []map[string]any{{"old_string": "alpha", "new_string": "changed"}, {"old_string": "alpha beta", "new_string": "text"}}, "overlaps"},
		{"ambiguous", []map[string]any{{"old_string": "beta", "new_string": "changed"}}, "found 2 times"},
		{"missing replacement", []map[string]any{{"old_string": "alpha"}}, "new_string is required"},
		{"empty search", []map[string]any{{"old_string": "", "new_string": "text"}}, "old_string is required"},
		{"control", []map[string]any{{"old_string": "alpha", "new_string": "\x00"}}, "new_string"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			const original = "alpha beta beta\n"
			path := writeEditFixture(t, dir, "sample.txt", original)
			_, err := runEdit(t, dir, map[string]any{"path": path, "edits": tc.edits})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != original {
				t.Fatalf("failed batch changed file: %q", got)
			}
		})
	}
}

func TestEditBatchRejectsMixedFields(t *testing.T) {
	dir := t.TempDir()
	path := writeEditFixture(t, dir, "sample.txt", "alpha")
	_, err := runEdit(t, dir, map[string]any{"path": path, "old_string": "", "edits": []map[string]any{{"old_string": "alpha", "new_string": "beta"}}})
	if err == nil || !strings.Contains(err.Error(), "cannot be combined") {
		t.Fatalf("error = %v", err)
	}
}

func TestEditBatchOverlapNamesEntriesInFileOrder(t *testing.T) {
	dir := t.TempDir()
	const original = "alpha beta beta\n"
	path := writeEditFixture(t, dir, "sample.txt", original)
	_, err := runEdit(t, dir, map[string]any{
		"path": path,
		"edits": []map[string]any{
			{"old_string": "beta beta", "new_string": "one"},
			{"old_string": "alpha beta", "new_string": "two"},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "edits[1] overlaps edits[0]") {
		t.Fatalf("error = %v, want the earlier match named first", err)
	}
	if got, readErr := os.ReadFile(path); readErr != nil || string(got) != original {
		t.Fatalf("failed batch changed file: %q (%v)", got, readErr)
	}
}

func TestEditBatchOverlapAtSameOffsetNamesEntriesInEditsOrder(t *testing.T) {
	dir := t.TempDir()
	path := writeEditFixture(t, dir, "sample.txt", "alpha beta\n")
	_, err := runEdit(t, dir, map[string]any{
		"path": path,
		"edits": []map[string]any{
			{"old_string": "alpha beta", "new_string": "one"},
			{"old_string": "alpha", "new_string": "two"},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "edits[0] overlaps edits[1]") {
		t.Fatalf("error = %v, want matches at one offset named in edits order", err)
	}
}

// A batch entry that misses only by a trailing newline is not proof the text is
// absent: the same entry succeeds on its own, because a single edit tolerates
// that difference. The batch error has to say so, or the model rebuilds a batch
// that was never wrong.
func TestEditBatchNotFoundNamesTheSingleEditFallback(t *testing.T) {
	dir := t.TempDir()
	const original = "alpha beta\n"
	path := writeEditFixture(t, dir, "sample.txt", original)
	entry := map[string]any{"old_string": "alpha beta\n\n", "new_string": "alpha omega\n\n"}
	_, err := runEdit(t, dir, map[string]any{"path": path, "edits": []map[string]any{entry}})
	if err == nil {
		t.Fatal("expected the batch to reject a near-miss entry")
	}
	if !strings.Contains(err.Error(), "not found in the original file; no changes written.") {
		t.Fatalf("error = %v, want it to state that nothing was written before any guidance", err)
	}
	if !strings.Contains(err.Error(), "single edit") || !strings.Contains(err.Error(), tolerantMatchNote) {
		t.Fatalf("error = %v, want it to name the single-edit fallback and %q", err, tolerantMatchNote)
	}
	if got, readErr := os.ReadFile(path); readErr != nil || string(got) != original {
		t.Fatalf("failed batch changed file: %q (%v)", got, readErr)
	}
	// The claim in that message: the same entry is accepted as a single edit.
	if _, err := runEdit(t, dir, map[string]any{"path": path, "old_string": "alpha beta\n\n", "new_string": "alpha omega\n\n"}); err != nil {
		t.Fatalf("single edit of the same entry failed: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "alpha omega\n" {
		t.Fatalf("single edit content = %q", got)
	}
}

func TestEditBatchSchema(t *testing.T) {
	for _, raw := range []string{
		`{"path":"sample.txt","edits":[{"old_string":"alpha","new_string":"beta"}]}`,
		`{"path":"sample.txt","old_string":"alpha","new_string":"beta"}`,
	} {
		if err := ValidateToolArgs(EditTool{}, json.RawMessage(raw)); err != nil {
			t.Fatal(err)
		}
	}
	if err := ValidateToolArgs(EditTool{}, json.RawMessage(`{"path":"sample.txt","edits":[{"old_string":"alpha"}]}`)); err == nil {
		t.Fatal("missing new_string accepted")
	}
}
