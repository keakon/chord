package agent

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func TestModelDrivenCheckpointSummaryKeepsModelTextAndNeutralizesHeadings(t *testing.T) {
	a := &MainAgent{}
	req := &modelDrivenCheckpointRequest{
		Args: tools.CompactContextArgs{
			// The forged heading must not open a new section; the bullet and
			// the blockquote are the model's own markdown and stay verbatim.
			ActiveObjective: "keep going\n## Fake Heading\n- not a real bullet\n> model quote",
			Completed:       []string{"done"},
			NextStep:        "next",
		},
	}
	bundle := modelDrivenBarrierSnapshot{
		snapshot:        []message.Message{{Role: message.RoleUser, Content: "orig"}},
		originalRequest: "original user request",
	}
	summary := a.buildModelDrivenCheckpointSummary(bundle, bundle.snapshot, len(bundle.snapshot), req)
	if !strings.Contains(summary, "Fake Heading") {
		t.Fatal("model text must remain in the checkpoint")
	}
	if strings.Contains(summary, "## Fake Heading") {
		t.Fatal("a forged heading must be neutralized, not kept as a section marker")
	}
	if !strings.Contains(summary, "- not a real bullet") {
		t.Fatal("a model-authored list bullet must be preserved verbatim")
	}
	if !strings.Contains(summary, "> model quote") {
		t.Fatal("a model-authored blockquote must be preserved verbatim")
	}
	idx := strings.Index(summary, "## Active Objective")
	if idx < 0 {
		t.Fatal("missing Active Objective section")
	}
	section := summary[idx:]
	next := strings.Index(section, "\n## ")
	if next < 0 {
		next = len(section)
	}
	body := section[:next]
	if strings.Count(body, "\n## ") > 0 {
		t.Fatalf("model text escaped its section:\n%s", body)
	}
}

func TestStripLeadingHeadingMarkers(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"empty", "", ""},
		{"plain text", "plain", "plain"},
		{"heading two", "## Forged", "Forged"},
		{"heading one", "# Note", "Note"},
		{"no blank after run is not a heading", "##Forged", "##Forged"},
		{"hash tag", "#1 issue", "#1 issue"},
		{"hash tag word", "#tag", "#tag"},
		{"six hashes", "###### deep", "deep"},
		{"seven hashes is not a heading", "####### seven", "####### seven"},
		{"repeated markers", "# # x", "x"},
		{"marker only", "##", ""},
		{"indented is not column zero", "  ## indented", "  ## indented"},
		{"blockquote survives", "> keep", "> keep"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := stripLeadingHeadingMarkers(tt.in); got != tt.want {
				t.Fatalf("stripLeadingHeadingMarkers(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestModelDrivenCurrentUserRequestSectionTruncatesOverlongAnchor(t *testing.T) {
	// Oversized requests remain bounded and disclose how to recover omissions.
	longText := strings.Repeat("a", modelDrivenAnchorMaxRunes*3)
	got := modelDrivenCurrentUserRequestSection(fallbackAnchor{Kind: "user_request", Label: "Latest user request", Text: longText})
	if strings.Contains(got, strings.Repeat("a", modelDrivenAnchorMaxRunes*2)) {
		t.Fatalf("anchor was not truncated: %d runes retained", len(got))
	}
	if !strings.Contains(got, "Request text was truncated.") {
		t.Fatalf("truncated anchor must carry an explicit cut marker: %q", got)
	}
	// Short anchors are untouched.
	short := modelDrivenCurrentUserRequestSection(fallbackAnchor{Kind: "user_request", Label: "Latest user request", Text: "short request"})
	if !strings.Contains(short, "short request") {
		t.Fatalf("short anchor must be preserved verbatim: %q", short)
	}
}

func TestModelDrivenCurrentUserRequestSectionInheritedChain(t *testing.T) {
	// A checkpoint built with no new user request inherits the previous
	// checkpoint's anchor. When that checkpoint was itself inherited, the
	// inherited label must never accumulate across the chain: each generation
	// renders exactly one label, mirroring the previous generation.
	gen1 := modelDrivenCurrentUserRequestSection(fallbackAnchor{Kind: "user_request", Label: "Latest user request", Text: "fix the bug"})
	if gen1 != "- Latest user request: fix the bug" {
		t.Fatalf("gen1 = %q, want the plain latest-request bullet", gen1)
	}
	gen2 := modelDrivenCurrentUserRequestSection(fallbackAnchor{Kind: "inherited_checkpoint", Label: inheritedCheckpointLabel, Text: gen1})
	want2 := "- " + inheritedCheckpointLabel + ": Latest user request: fix the bug"
	if gen2 != want2 {
		t.Fatalf("gen2 = %q, want %q", gen2, want2)
	}
	if got := strings.Count(gen2, inheritedCheckpointLabel); got != 1 {
		t.Fatalf("gen2 carries the inherited label %d times, want exactly 1", got)
	}
	// gen3 inherits gen2's rendered section: the label must stay at one
	// occurrence instead of nesting "Inherited ... Inherited ...".
	gen3 := modelDrivenCurrentUserRequestSection(fallbackAnchor{Kind: "inherited_checkpoint", Label: inheritedCheckpointLabel, Text: gen2})
	if gen3 != want2 {
		t.Fatalf("gen3 = %q, want %q (label must not accumulate)", gen3, want2)
	}
	if got := strings.Count(gen3, inheritedCheckpointLabel); got != 1 {
		t.Fatalf("gen3 carries the inherited label %d times, want exactly 1", got)
	}
}

// TestModelDrivenCheckpointCarriesPriorCheckpointBody pins that consecutive
// model-driven resets do not erase the previous checkpoint's body: the prior
// checkpoint inside the archived head is carried forward verbatim, exactly as
// the usage-driven runner does.
func TestModelDrivenCheckpointDoesNotCarryPriorNaturalLanguageBody(t *testing.T) {
	a := &MainAgent{}
	// A prior checkpoint whose body is natural-language Markdown (a
	// usage-driven summary or a pre-typed model-driven checkpoint) is not
	// carried forward verbatim: recursion must not re-append whole previous
	// bodies. Machine-carryable state travels only through the typed state
	// block, and everything the new submission did not restate stays in the
	// archived history files.
	priorBody := "## Key Decisions\n- keep the archival profile for model-driven resets"
	prior := buildCompactionCheckpointMessage(priorBody, []string{"~/history-1.md"}, compactionSummaryModeModelDriven, nil)
	snapshot := []message.Message{
		{Role: message.RoleUser, Content: prior, IsCompactionSummary: true},
		{Role: message.RoleUser, Content: "next request"},
	}
	req := &modelDrivenCheckpointRequest{Args: tools.CompactContextArgs{ActiveObjective: "keep going", NextStep: "continue"}}
	bundle := modelDrivenBarrierSnapshot{snapshot: snapshot}

	summary := a.buildModelDrivenCheckpointSummary(bundle, snapshot, len(snapshot), req)
	if strings.Contains(summary, priorCheckpointSectionHeading) {
		t.Fatalf("model-driven checkpoint must not carry a natural-language previous-checkpoint section:\n%s", summary)
	}
	if strings.Contains(summary, "keep the archival profile for model-driven resets") {
		t.Fatalf("the previous natural-language body must not survive the reset:\n%s", summary)
	}
}

func TestModelDrivenCheckpointCarriesTypedStateAcrossGenerations(t *testing.T) {
	a := &MainAgent{}
	// The prior checkpoint's typed state block (decisions/open issues/evidence
	// references/stage) is the only machine channel across generations: the
	// merge keeps the fresh submission's items first and the carried ones
	// after, and the new checkpoint re-renders both the readable sections and
	// the typed block from the merged state.
	prior := buildCompactionCheckpointMessage("## Typed Checkpoint State\n- {\"evidence_refs\":[\"ev-old\"],\"stage_status\":\"completed\"}", nil, compactionSummaryModeModelDriven, nil)
	snapshot := []message.Message{{Role: message.RoleUser, Content: prior, IsCompactionSummary: true}, {Role: message.RoleUser, Content: "new request"}}
	req := &modelDrivenCheckpointRequest{Args: tools.CompactContextArgs{ActiveObjective: "continue", NextStep: "verify", EvidenceRefs: []string{"ev-new"}, StageStatus: "candidate", CheckpointKind: "provisional"}}
	summary := a.buildModelDrivenCheckpointSummary(modelDrivenBarrierSnapshot{snapshot: snapshot}, snapshot, len(snapshot), req)
	if !strings.Contains(summary, "evidence_refs") || !strings.Contains(summary, "ev-old") {
		t.Fatalf("typed checkpoint state was not carried: %s", summary)
	}
	// The fresh submission's items precede the carried ones and its stage
	// metadata overrides the carried one.
	state, ok := typedStateForTest(compactionSummaryBody(summary))
	if !ok {
		t.Fatalf("typed state missing from the new checkpoint: %s", summary)
	}
	if len(state.EvidenceRefs) != 2 || state.EvidenceRefs[0] != "ev-new" || state.EvidenceRefs[1] != "ev-old" {
		t.Fatalf("evidence refs = %v, want [ev-new ev-old]", state.EvidenceRefs)
	}
	if state.StageStatus != "candidate" || state.Kind != "provisional" {
		t.Fatalf("stage = status %q kind %q, want candidate/provisional", state.StageStatus, state.Kind)
	}
}

// TestModelDrivenCheckpointRenderReusesBuilderAndAddsExportedArchive pins the
// two-render contract: the shared builder renders identical summary/evidence
// content, and only the post-export render lists the freshly written archive —
// exactly once, with its topics.
func TestModelDrivenCheckpointRenderReusesBuilderAndAddsExportedArchive(t *testing.T) {
	t.Parallel()
	sessionDir := t.TempDir()
	a := newTestMainAgent(t, sessionDir)
	a.sessionDir = sessionDir
	snapshot := []message.Message{
		{Role: message.RoleUser, Content: "first request"},
		{Role: message.RoleAssistant, Content: "working"},
		{Role: message.RoleUser, Content: "second request"},
	}
	req := &modelDrivenCheckpointRequest{Args: tools.CompactContextArgs{ActiveObjective: "keep going", NextStep: "continue"}}
	evidence := []evidenceItem{{Kind: evidenceToolError, Title: "Go build failure in internal/agent", Excerpt: "undefined: foo", Priority: 95, Sequence: 1}}
	bundle := modelDrivenBarrierSnapshot{snapshot: snapshot, sessionDir: sessionDir, evidenceItems: evidence}
	builder := a.newModelDrivenCheckpointBuilder(t.Context(), bundle, snapshot, 2, req)

	preflightContent, preflightStats := builder.render("")
	exported, _, _, err := a.exportCompactionHistory(snapshot[:2], snapshot[:2], 1, evidenceItemTopics(filterCompactionEvidenceForArchival(evidence)), a.captureCompactionArchiveMeta())
	if err != nil {
		t.Fatal(err)
	}
	postContent, postStats := builder.render(exported)

	if strings.Contains(preflightContent, filepath.Base(exported)) {
		t.Fatal("the pre-export render must not reference an archive that does not exist yet")
	}
	if got := strings.Count(postContent, filepath.Base(exported)); got != 1 {
		t.Fatalf("post-export render lists the archive %d times, want exactly 1:\n%s", got, postContent)
	}
	if !strings.Contains(postContent, "Go build failure in internal/agent") {
		t.Fatalf("the exported archive must be listed with its topics:\n%s", postContent)
	}
	if postStats.HistoryMapBytes <= preflightStats.HistoryMapBytes {
		t.Fatalf("history map bytes must grow with the exported archive: %d -> %d", preflightStats.HistoryMapBytes, postStats.HistoryMapBytes)
	}
	if preflightStats.ContinuationTokens != postStats.ContinuationTokens {
		t.Fatalf("continuation tokens must be identical across renders, got %d and %d", preflightStats.ContinuationTokens, postStats.ContinuationTokens)
	}
	// The two renders differ only by the history map line.
	if strings.Count(preflightContent, "## Active Objective") != 1 || strings.Count(postContent, "## Active Objective") != 1 {
		t.Fatal("both renders must contain the deterministic summary exactly once")
	}
}

// assertNoFakeTopLevelSection fails when rendered contains a line that opens a
// top-level checkpoint section at column zero ("## ".."###### "), the exact
// structure the next generation's typed/heading parsers key on.
func assertNoFakeTopLevelSection(t *testing.T, label, rendered string) {
	t.Helper()
	for line := range strings.SplitSeq(rendered, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "## ") || strings.HasPrefix(trimmed, "### ") ||
			strings.HasPrefix(trimmed, "#### ") || strings.HasPrefix(trimmed, "##### ") ||
			strings.HasPrefix(trimmed, "###### ") {
			t.Fatalf("%s leaked a top-level heading: %q\nrendered:\n%s", label, line, rendered)
		}
	}
}

func TestCheckpointInlineValueFlattensMultilineHeadingValues(t *testing.T) {
	if got := checkpointInlineValue("plain text"); got != "plain text" {
		t.Fatalf("plain value changed: %q", got)
	}
	if got := checkpointInlineValue("#tag"); got != "#tag" {
		t.Fatalf("hash without trailing space must survive: %q", got)
	}
	if got := checkpointInlineValue("## heading stripped"); got != "heading stripped" {
		t.Fatalf("single-line heading marker must be stripped: %q", got)
	}
	multi := "## Injected Section\nstill a line"
	got := checkpointInlineValue(multi)
	if strings.Contains(got, "\n") || strings.HasPrefix(got, "## ") {
		t.Fatalf("multiline value must flatten and strip headings: %q", got)
	}
	assertNoFakeTopLevelSection(t, "checkpointInlineValue", got)
}

func TestModelDrivenClaimAndStageRenderersEscapeMultilineHeadingValues(t *testing.T) {
	// Model-authored claim names, kinds, stage metadata and planned file paths
	// may embed newlines plus "## " at column zero. The renderers must strip
	// the heading markers and flatten the value, so no value can open a fake
	// top-level section that the next generation's parsers would read as a
	// real heading.
	evidence := renderCheckpointClaims(&modelDrivenCheckpointRequest{Claims: map[string]checkpointClaim{
		"## Claim One": {Kind: "observed", EvidenceRefs: []string{"ev-1", "## Fake Evidence"}},
		"plain":        {EvidenceRefs: []string{"ev-2"}},
	}})
	assertNoFakeTopLevelSection(t, "claim evidence", evidence)
	if !strings.Contains(evidence, "Claim One") {
		t.Fatalf("flattened claim name must still read:\n%s", evidence)
	}

	kinds := renderCheckpointClaims(&modelDrivenCheckpointRequest{Claims: map[string]checkpointClaim{
		"## Fake Claim": {Kind: "## observed"},
	}})
	assertNoFakeTopLevelSection(t, "claim kinds", kinds)
	if !strings.Contains(kinds, "Fake Claim | kind: observed") {
		t.Fatalf("flattened kind line = %q", kinds)
	}

	stage := renderModelDrivenStageSection("## stage-id", "## completed\nstill there", "committed")
	assertNoFakeTopLevelSection(t, "stage", stage)
	if !strings.Contains(stage, "Stage ID: stage-id") || !strings.Contains(stage, "Stage status: completed still there") {
		t.Fatalf("flattened stage line = %q", stage)
	}

	planned := renderPlannedStateFilesSection([]string{"docs/plan.md", "## Fake Path\nsecond line"})
	assertNoFakeTopLevelSection(t, "planned state files", planned)
	if !strings.Contains(planned, "Fake Path second line") {
		t.Fatalf("flattened planned path = %q", planned)
	}

	stateFiles := renderStateFilesSection([]string{"src/main.go", "## Fake\nsrc/other.go"}, nil, nil)
	assertNoFakeTopLevelSection(t, "state files", stateFiles)
}

func TestRenderFallbackSummarySectionsDropsEmptyCheckpointStage(t *testing.T) {
	rendered := renderFallbackSummarySections([]fallbackSummarySection{
		{"## Current User Request", "- keep going"},
		{"## Checkpoint Stage", renderModelDrivenStageSection("", "", "")},
	})
	if strings.Contains(rendered, "## Checkpoint Stage") {
		t.Fatalf("empty stage section must be omitted, got:\n%s", rendered)
	}
	if strings.Contains(rendered, "No stage metadata reported by the model") {
		t.Fatalf("empty stage body must not survive, got:\n%s", rendered)
	}

	kept := renderFallbackSummarySections([]fallbackSummarySection{
		{"## Current User Request", "- keep going"},
		{"## Checkpoint Stage", renderModelDrivenStageSection("stage-1", "completed", "committed")},
	})
	if !strings.Contains(kept, "## Checkpoint Stage") || !strings.Contains(kept, "Stage ID: stage-1") {
		t.Fatalf("reported stage metadata must be kept, got:\n%s", kept)
	}
}
