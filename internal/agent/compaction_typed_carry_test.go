package agent

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func TestParseTypedStateRoundTripsThroughCheckpointBody(t *testing.T) {
	state := checkpointTypedState{
		Decisions:    []string{"keep the parser contract", "drop the shim"},
		OpenIssues:   []string{"port the loader"},
		EvidenceRefs: []string{"ev-1", "ev-2"},
		StageID:      "impl",
		StageStatus:  "candidate",
		Kind:         "provisional",
	}
	body := "## Key Decisions\n- keep the parser contract\n\n## Typed Checkpoint State\n" + renderTypedStateJSON(state)
	parsed, ok := parseCheckpointTypedState(body)
	if !ok {
		t.Fatal("typed state not found in body")
	}
	if strings.Join(parsed.Decisions, "|") != strings.Join(state.Decisions, "|") {
		t.Fatalf("decisions = %v, want %v", parsed.Decisions, state.Decisions)
	}
	if strings.Join(parsed.EvidenceRefs, "|") != strings.Join(state.EvidenceRefs, "|") {
		t.Fatalf("evidence refs = %v, want %v", parsed.EvidenceRefs, state.EvidenceRefs)
	}
	if parsed.StageStatus != "candidate" || parsed.Kind != "provisional" || parsed.StageID != "impl" {
		t.Fatalf("stage = %+v", parsed)
	}
	if _, ok := parseCheckpointTypedState("## Key Decisions\n- plain body"); ok {
		t.Fatal("body without a typed block must not parse")
	}
	if _, ok := parseCheckpointTypedState(""); ok {
		t.Fatal("empty body must not parse")
	}
}

func TestParseTypedStateIgnoresLegacyConstraintsKey(t *testing.T) {
	// The legacy block declared a "constraints" key that was never populated.
	// Old checkpoints that happen to carry it must still parse (the field is
	// ignored), so a chain spanning the format change keeps its decisions.
	body := "## Typed Checkpoint State\n- {\"constraints\":[\"c1\"],\"decisions\":[\"d1\"]}"
	state, ok := parseCheckpointTypedState(body)
	if !ok {
		t.Fatal("legacy typed state must parse")
	}
	if strings.Join(state.Decisions, "|") != "d1" {
		t.Fatalf("decisions = %v, want [d1]", state.Decisions)
	}
}

func TestMergeTypedStateKeepsNewestAndDisclosesOmission(t *testing.T) {
	prior := checkpointTypedState{
		Decisions:    []string{"d-old-1", "d-old-2", "d-old-3"},
		EvidenceRefs: []string{"ev-old"},
		StageStatus:  "completed",
		Kind:         "committed",
	}
	current := checkpointTypedState{
		Decisions:   []string{"d-new-1", "d-new-2"},
		OpenIssues:  []string{"issue-new"},
		StageStatus: "candidate",
		Kind:        "provisional",
	}
	// Decisions cap is 8, so nothing is dropped here; the fresh submission's
	// items come first and the carried ones follow, and the fresh stage
	// metadata wins.
	merged, omitted := mergeCheckpointTypedStates(prior, current)
	if omitted != 0 {
		t.Fatalf("omitted = %d, want 0", omitted)
	}
	if strings.Join(merged.Decisions, "|") != "d-new-1|d-new-2|d-old-1|d-old-2|d-old-3" {
		t.Fatalf("decisions = %v", merged.Decisions)
	}
	if merged.StageStatus != "candidate" || merged.Kind != "provisional" {
		t.Fatalf("stage must come from the fresh submission: %+v", merged)
	}

	// Exceeding the cap drops the oldest carried entries (the fresh
	// submission always fits) and reports the omission count: 10 carried
	// entries compete for 6 remaining slots after the fresh 2, so 4 drop.
	over := checkpointTypedState{}
	for i := 0; i < typedStateCarryMaxDecisions+2; i++ {
		over.Decisions = append(over.Decisions, "old-"+string(rune('a'+i)))
	}
	merged, omitted = mergeCheckpointTypedStates(over, current)
	if len(merged.Decisions) != typedStateCarryMaxDecisions {
		t.Fatalf("merged decisions = %d, want cap %d", len(merged.Decisions), typedStateCarryMaxDecisions)
	}
	if omitted != 4 {
		t.Fatalf("omitted = %d, want 4", omitted)
	}
	if merged.Decisions[0] != "d-new-1" || merged.Decisions[1] != "d-new-2" {
		t.Fatalf("fresh submission items must come first: %v", merged.Decisions)
	}

	// A carried item past the per-item cap is bounded on the way back out of
	// the merge, so the readable sections and the typed block can never
	// diverge on the same decision.
	oversizedPrior := checkpointTypedState{Decisions: []string{strings.Repeat("x", typedStateCarryMaxItemRunes+50)}}
	merged, omitted = mergeCheckpointTypedStates(oversizedPrior, current)
	if omitted != 0 {
		t.Fatalf("omitted = %d, want 0", omitted)
	}
	for _, item := range merged.Decisions {
		if runeLen(item) > typedStateCarryMaxItemRunes+len(typedStateItemTruncatedSuffix) {
			t.Fatalf("merged decision not item-bounded: %d runes", runeLen(item))
		}
	}
}

func TestRenderTypedStateBoundsOversizedCarriedItem(t *testing.T) {
	state := checkpointTypedState{Decisions: []string{strings.Repeat("x", typedStateCarryMaxItemRunes+50)}}
	rendered := renderTypedStateJSON(state)
	if !strings.Contains(rendered, typedStateItemTruncatedSuffix) {
		t.Fatalf("oversized carried item must be explicitly truncated: %s", rendered)
	}
	if strings.Contains(rendered, strings.Repeat("x", typedStateCarryMaxItemRunes+10)) {
		t.Fatal("truncated item leaked beyond the bound")
	}
	parsed, ok := parseCheckpointTypedState("## Typed Checkpoint State\n" + rendered)
	if !ok || !strings.HasSuffix(parsed.Decisions[0], typedStateItemTruncatedSuffix) {
		t.Fatalf("truncated item must parse back with its marker: %+v ok=%v", parsed, ok)
	}

	// Multibyte content truncates at a rune boundary without corrupting UTF-8.
	long := strings.Repeat("保持顺序", 110) // 440 runes > 400 cap
	multi := checkpointTypedState{Decisions: []string{long}}
	out := renderTypedStateJSON(multi)
	if !strings.Contains(out, typedStateItemTruncatedSuffix) {
		t.Fatalf("multibyte item must be truncated: %s", out)
	}
	parsed, ok = parseCheckpointTypedState("## Typed Checkpoint State\n" + out)
	if !ok || !utf8.ValidString(parsed.Decisions[0]) {
		t.Fatalf("truncated multibyte item must stay valid UTF-8: %+v ok=%v", parsed, ok)
	}
	if runeLen(parsed.Decisions[0]) >= runeLen(multi.Decisions[0]) {
		t.Fatalf("truncated item did not shrink: %q", parsed.Decisions[0])
	}
}

func TestCheckpointBodyTypedStateSurvivesGenerationChain(t *testing.T) {
	// Three generations: each build merges the previous typed block and
	// re-renders it, so decisions accumulate up to the cap with the newest
	// submission always first, and the omission is disclosed on the round
	// that would overflow.
	a := newTestMainAgent(t, t.TempDir())
	generation := func(prior string, decisions []string) string {
		snapshot := []message.Message{{Role: message.RoleUser, Content: prior, IsCompactionSummary: true}, {Role: message.RoleUser, Content: "continue"}}
		req := &modelDrivenCheckpointRequest{Args: tools.CompactContextArgs{ActiveObjective: "continue", NextStep: "go", Decisions: decisions}}
		return a.buildModelDrivenCheckpointSummary(modelDrivenBarrierSnapshot{snapshot: snapshot}, snapshot, len(snapshot), req)
	}
	// Build a prior checkpoint with a typed block.
	first := generation("", []string{"d1"})
	second := generation(first, []string{"d2"})
	third := generation(second, []string{"d3"})
	// Generation 2 carries d1 (from the first typed block) plus d2; generation
	// 3 carries all three, newest submission first.
	assertDecisions := func(summary string, want ...string) {
		t.Helper()
		state, ok := parseCheckpointTypedState(compactionSummaryBody(summary))
		if !ok {
			t.Fatalf("typed block missing from generation:\n%s", summary)
		}
		for _, item := range want {
			if !containsString(state.Decisions, item) {
				t.Fatalf("decisions = %v, want it to contain %q", state.Decisions, item)
			}
		}
	}
	assertDecisions(second, "d1", "d2")
	assertDecisions(third, "d1", "d2", "d3")
	if strings.Contains(compactionSummaryBody(third), priorCheckpointSectionHeading) {
		t.Fatal("no generation may carry a natural-language previous-checkpoint section")
	}
}

func containsString(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}
