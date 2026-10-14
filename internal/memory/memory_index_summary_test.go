package memory

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

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
	if strings.Contains(summary, SummaryTruncatedMarker) {
		t.Fatalf("a summary carrying the whole file must not claim truncation: %q", summary)
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
	if !strings.HasSuffix(summary, SummaryTruncatedMarker) {
		t.Fatalf("a cut summary must end with the truncation marker: %q", summary)
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
