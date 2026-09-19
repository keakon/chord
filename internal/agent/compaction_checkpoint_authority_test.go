package agent

import (
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

// A checkpoint is the one place where model-authored text sits next to runtime
// facts in the same shape. The tests here pin the contract that keeps the
// difference legible to whoever reads the checkpoint next: model-declared
// state_files references are rendered as unverified claims (never existence-
// stamped, which would turn a checkpoint into a cross-permission probe), and
// the continuation guidance states which source wins when they disagree.

func TestStateFilesSectionNeverStampsExistence(t *testing.T) {
	section := renderStateFilesSection([]string{
		"notes.md — the running task notes",
		"missing/plan.md",
		"the design decisions we agreed on",
	}, nil, nil)
	if strings.Contains(section, "verified present") || strings.Contains(section, "NOT FOUND") {
		t.Fatalf("state_files must never be existence-stamped:\n%s", section)
	}
	if !strings.Contains(section, "notes.md — the running task notes") {
		t.Fatalf("an existing reference was dropped:\n%s", section)
	}
	if !strings.Contains(section, "missing/plan.md") {
		t.Fatalf("a missing reference was dropped:\n%s", section)
	}
	if !strings.Contains(section, "existence is not verified") {
		t.Fatalf("the section must state references are unverified:\n%s", section)
	}
}

func TestStateFilesSectionEmptyFallback(t *testing.T) {
	section := renderStateFilesSection(nil, nil, nil)
	if !strings.Contains(section, "none reported") {
		t.Fatalf("empty state_files must render a fallback marker:\n%s", section)
	}
	// An empty list is legitimate, so the fallback is actionable rather than a
	// rejection: it states the archive facts and points at the next checkpoint
	// as the place to register a notes/plan file.
	//
	// Nothing is archived here, so there is no ID to cite: the ID route must
	// not be offered, or the continuation is told to take an action it cannot.
	if strings.Contains(section, "Cite an archived ID") {
		t.Fatalf("an empty archive has no ID to cite:\n%s", section)
	}
	if !strings.Contains(section, "no archived evidence exists to cite") {
		t.Fatalf("empty state_files must state that no archived evidence exists:\n%s", section)
	}
	if !strings.Contains(section, "register a notes or plan file at the next checkpoint") {
		t.Fatalf("empty state_files must tell the continuation how to close the gap:\n%s", section)
	}
	// The nudge belongs to the empty case only; a registered reference must
	// not carry it.
	registered := renderStateFilesSection([]string{"notes/current.md"}, nil, nil)
	if strings.Contains(registered, "register a notes or plan file at the next checkpoint") {
		t.Fatalf("a non-empty state_files section must not carry the empty-case nudge:\n%s", registered)
	}
}

// The empty case states how much archived evidence exists and how much of it
// the submission's own references resolve to, so the continuation can tell
// whether the record it needs is still reachable by ID instead of re-deriving
// it from the archive.
func TestStateFilesSectionEmptyFallbackCountsArchivedReferences(t *testing.T) {
	messages := []message.Message{{Role: message.RoleTool, ToolCallID: "call-1", ToolStatus: message.ToolStatusError, Content: "boom"}}
	items := selectEvidenceItems(messages, 8192)
	if len(items) == 0 {
		t.Fatal("expected the failing tool result to produce an evidence item")
	}
	section := renderStateFilesSection(nil, items, []string{evidenceItemID(items[0])})
	if !strings.Contains(section, "1 archived evidence item(s) exist") {
		t.Fatalf("the archived count must be stated:\n%s", section)
	}
	// An archive is present, so the ID route is actionable and stays offered.
	if !strings.Contains(section, "Cite an archived ID") {
		t.Fatalf("a non-empty archive must offer the ID route:\n%s", section)
	}
	if !strings.Contains(section, "references 1 of them") {
		t.Fatalf("a cited archived ID must be counted as referenced:\n%s", section)
	}
	uncited := renderStateFilesSection(nil, items, []string{"ev-000000000000"})
	if !strings.Contains(uncited, "references 0 of them") {
		t.Fatalf("an unknown reference must not be counted:\n%s", uncited)
	}
}

// The footer's counts must describe the archive the continuation actually
// receives. Evidence the archival pack drops — an invalidated item, or a kind
// the pack does not carry — is not reachable by ID, so counting it would
// promise a record that is not there, and a reference to it would look
// resolved. The raw barrier snapshot still feeds the sections that need to
// know about every item, so the filtering has to happen where the footer is
// rendered.
func TestCheckpointStateFilesFooterCountsTheArchivedPackNotRawEvidence(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	snapshot := []message.Message{{Role: message.RoleUser, Content: "keep going"}}
	kept := evidenceItem{Key: "ev-kept", Kind: evidenceToolError, Title: "the build failed"}
	invalidated := evidenceItem{Key: "ev-invalidated", Kind: evidenceToolError, Title: "a failure the user disproved", Validity: evidenceValidityInvalidated}
	unarchived := evidenceItem{Key: "ev-unarchived", Kind: evidenceDoneRejected, Title: "the user rejected a done report"}
	bundle := modelDrivenBarrierSnapshot{
		snapshot:      snapshot,
		evidenceItems: []evidenceItem{kept, invalidated, unarchived},
	}

	summaryFor := func(refs ...string) string {
		req := &modelDrivenCheckpointRequest{Args: tools.CompactContextArgs{
			ActiveObjective: "keep going",
			NextStep:        "continue",
			EvidenceRefs:    refs,
		}}
		return a.buildModelDrivenCheckpointSummary(bundle, snapshot, len(snapshot), req)
	}

	summary := summaryFor()
	if !strings.Contains(summary, "1 archived evidence item(s) exist") {
		t.Fatalf("the footer must count only what the pack carries:\n%s", summary)
	}
	if !strings.Contains(summaryFor(evidenceItemID(kept)), "references 1 of them") {
		t.Fatalf("a reference to archived evidence must be counted:\n%s", summaryFor(evidenceItemID(kept)))
	}
	// A dropped item cannot be reached by ID from the archive, so neither the
	// count nor the reference resolution may include it.
	for name, ref := range map[string]string{
		"invalidated": evidenceItemID(invalidated),
		"unarchived":  evidenceItemID(unarchived),
	} {
		got := summaryFor(ref)
		if !strings.Contains(got, "references 0 of them") {
			t.Fatalf("a reference to %s evidence must not resolve:\n%s", name, got)
		}
	}
}

// TestCheckpointContinuationStatesPrecedence covers the rule the checkpoint
// itself cannot express: which source wins when the preserved summary and a
// newer fact disagree. Both continuation paths must carry it, and the user's
// newest message must outrank the checkpoint text.
func TestCheckpointContinuationStatesPrecedence(t *testing.T) {
	for name, text := range map[string]string{
		"overlay":       appendContextPressureVerificationGuidance("System note: checkpoint applied."),
		"auto_continue": autoContinuePrompt(),
	} {
		t.Run(name, func(t *testing.T) {
			userIdx := strings.Index(text, "latest user message")
			checkpointIdx := strings.Index(text, "checkpoint's own text")
			if userIdx < 0 || checkpointIdx < 0 {
				t.Fatalf("continuation guidance lost its precedence clause: %q", text)
			}
			if userIdx > checkpointIdx {
				t.Fatalf("the checkpoint must never outrank the newest user message: %q", text)
			}
			for _, source := range []string{"runtime state", "files on disk", "tool results", "archived artifacts"} {
				if !strings.Contains(text, source) {
					t.Fatalf("precedence clause omits %q: %q", source, text)
				}
			}
		})
	}
}
