package agent

import (
	"fmt"
	"maps"
	"slices"
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
	parsed, ok := typedStateForTest(body)
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
	if _, ok := typedStateForTest("## Key Decisions\n- plain body"); ok {
		t.Fatal("body without a typed block must not parse")
	}
	if _, ok := typedStateForTest(""); ok {
		t.Fatal("empty body must not parse")
	}
}

func TestParseTypedStateIgnoresUnknownJSONFields(t *testing.T) {
	// A block that carries a JSON field the typed decoder does not declare is
	// still machine state: encoding/json ignores unknown fields, so a future
	// key added by a newer writer must never make an old reader drop the
	// decisions it does understand.
	body := "## Typed Checkpoint State\n- {\"unknown_key\":[\"x\"],\"decisions\":[\"d1\"]}"
	state, ok := typedStateForTest(body)
	if !ok {
		t.Fatal("typed state with an unknown field must parse")
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
	merged, omitted, _ := mergeCheckpointTypedStates(prior, current)
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
	for i := range typedStateCarryMaxDecisions + 2 {
		over.Decisions = append(over.Decisions, "old-"+string(rune('a'+i)))
	}
	merged, omitted, _ = mergeCheckpointTypedStates(over, current)
	if len(merged.Decisions) != typedStateCarryMaxDecisions {
		t.Fatalf("merged decisions = %d, want cap %d", len(merged.Decisions), typedStateCarryMaxDecisions)
	}
	if omitted != 4 {
		t.Fatalf("omitted = %d, want 4", omitted)
	}
	if merged.Decisions[0] != "d-new-1" || merged.Decisions[1] != "d-new-2" {
		t.Fatalf("fresh submission items must come first: %v", merged.Decisions)
	}

	// Every bounded list contributes to the disclosure count, not only
	// decisions. Current entries beyond a cap are bounded as well.
	tooMany := checkpointTypedState{}
	for i := range typedStateCarryMaxOpenIssues + 2 {
		tooMany.OpenIssues = append(tooMany.OpenIssues, "issue-"+string(rune('a'+i)))
	}
	for i := range typedStateCarryMaxEvidenceRefs + 2 {
		tooMany.EvidenceRefs = append(tooMany.EvidenceRefs, "ev-"+string(rune('a'+i)))
	}
	merged, omitted, _ = mergeCheckpointTypedStates(checkpointTypedState{}, tooMany)
	if len(merged.OpenIssues) != typedStateCarryMaxOpenIssues || len(merged.EvidenceRefs) != typedStateCarryMaxEvidenceRefs {
		t.Fatalf("current typed lists exceeded bounds: open=%d evidence=%d", len(merged.OpenIssues), len(merged.EvidenceRefs))
	}
	if omitted != 4 {
		t.Fatalf("current typed list omissions = %d, want 4", omitted)
	}

	// A carried item past the per-item cap is bounded on the way back out of
	// the merge, so the readable sections and the typed block can never
	// diverge on the same decision.
	oversizedPrior := checkpointTypedState{Decisions: []string{strings.Repeat("x", typedStateCarryMaxItemRunes+50)}}
	merged, omitted, _ = mergeCheckpointTypedStates(oversizedPrior, current)
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
	parsed, ok := typedStateForTest("## Typed Checkpoint State\n" + rendered)
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
	parsed, ok = typedStateForTest("## Typed Checkpoint State\n" + out)
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
		state, ok := typedStateForTest(compactionSummaryBody(summary))
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
	return slices.Contains(items, want)
}

// typedStateForTest is the two-value convenience form of typedStateFromBody
// the tests use: a parseable typed block (found and not malformed). Blocks
// that must distinguish the malformed case call typedStateFromBody directly.
func typedStateForTest(body string) (checkpointTypedState, bool) {
	state, found, malformed := typedStateFromBody(body)
	return state, found && !malformed
}

func TestTypedClaimsCarryIdentityAndStatus(t *testing.T) {
	prior := checkpointTypedState{Claims: map[string]checkpointClaim{
		"tests pass": {Kind: "observed", EvidenceRefs: []string{"ev-old"}, Status: "active"},
	}}
	current := checkpointTypedState{Claims: map[string]checkpointClaim{
		"tests pass": {Kind: "derived", EvidenceRefs: []string{"ev-new"}, Status: "superseded"},
		"next step":  {Kind: "proposed", Status: "active"},
	}}
	merged, _, _ := mergeCheckpointTypedStates(prior, current)
	if got := merged.Claims["tests pass"]; got.Status != "superseded" || got.Kind != "derived" || len(got.EvidenceRefs) != 1 || got.EvidenceRefs[0] != "ev-new" {
		t.Fatalf("fresh claim did not replace prior identity: %#v", got)
	}
	if got := merged.Claims["next step"]; got.Status != "active" {
		t.Fatalf("new claim status = %q", got.Status)
	}
	encoded := renderTypedStateJSON(merged)
	decoded, ok := typedStateForTest("## Typed Checkpoint State\n" + encoded)
	if !ok || decoded.Claims["tests pass"].Status != "superseded" {
		t.Fatalf("claim state did not round-trip: %#v", decoded.Claims)
	}
}

func TestTypedClaimsInvalidateOnEvidenceStatus(t *testing.T) {
	req := &modelDrivenCheckpointRequest{Args: tools.CompactContextArgs{
		ClaimKinds:    map[string]string{"tests pass": "observed"},
		ClaimEvidence: map[string][]string{"tests pass": {evidenceItemID(evidenceItem{Key: "ev-1"})}},
	}}
	markTypedClaimsInvalidated(req, []evidenceItem{{Key: "ev-1", Validity: evidenceValidityInvalidated}})
	if req.ClaimStatuses["tests pass"] != "invalidated" {
		t.Fatalf("claim status = %q, want invalidated", req.ClaimStatuses["tests pass"])
	}
}

func TestMergeTypedStateListDeduplicatesAcrossGenerations(t *testing.T) {
	// A recursive checkpoint chain restates carried decisions verbatim (the
	// summarizer folded the previous block into the fresh submission). Each
	// restated copy must not consume a fresh slot: distinct carried items stay
	// in the bounded list instead of being squeezed out by duplicates that look
	// new.
	prior := benchmarkTypedCheckpointList("same", typedStateCarryMaxDecisions)
	current := []string{benchmarkTypedCheckpointList("same", typedStateCarryMaxDecisions)[0]} // restates only the first
	merged, omitted := mergeTypedStateList(prior, current, typedStateCarryMaxDecisions)
	if len(merged) != typedStateCarryMaxDecisions {
		t.Fatalf("merged length = %d, want the full cap %d with duplicates collapsed", len(merged), typedStateCarryMaxDecisions)
	}
	if merged[0] != prior[0] {
		t.Fatalf("fresh-restated copy must win the first slot: %q", merged[0])
	}
	for _, want := range prior {
		if !containsString(merged, want) {
			t.Fatalf("deduplicated merge dropped a distinct carried item %q: %v", want, merged)
		}
	}
	if omitted != 0 {
		t.Fatalf("omitted = %d, want 0 when duplicates collapse within the cap", omitted)
	}

	// Without the dedupe every restated copy of one identical item claims a
	// fresh slot; all of them collapse onto the single distinct text.
	same := benchmarkTypedCheckpointList("same", 1)[0]
	over := make([]string, 0, typedStateCarryMaxDecisions+1)
	for i := 0; i <= typedStateCarryMaxDecisions; i++ {
		over = append(over, same)
	}
	merged, omitted = mergeTypedStateList(nil, over, typedStateCarryMaxDecisions)
	if len(merged) != 1 {
		t.Fatalf("deduplicated single-item list length = %d, want 1", len(merged))
	}
	// Duplicates collapse without consuming a slot, so they disclose nothing:
	// no distinct carried content was dropped by the cap.
	if omitted != 0 {
		t.Fatalf("omitted = %d, want 0 for duplicate-only input", omitted)
	}
}

func TestTypedStateFromBodyDistinguishesMalformedBlock(t *testing.T) {
	if _, found, malformed := typedStateFromBody(""); found || malformed {
		t.Fatalf("empty body: found=%v malformed=%v, want neither", found, malformed)
	}
	if _, found, malformed := typedStateFromBody("## Key Decisions\n- plain body"); found || malformed {
		t.Fatalf("body without typed block: found=%v malformed=%v, want neither", found, malformed)
	}
	if _, found, malformed := typedStateFromBody("## Typed Checkpoint State\n- not json"); !found || !malformed {
		t.Fatalf("present-but-broken block: found=%v malformed=%v, want both", found, malformed)
	}
	if _, found, malformed := typedStateFromBody("## Typed Checkpoint State\n- {\"decisions\":[\"d1\"]}"); !found || malformed {
		t.Fatalf("valid block: found=%v malformed=%v, want found-only", found, malformed)
	}
}

// TestMergePriorTypedCheckpointStateReadsFullBodyBeyondDisplayTruncation pins
// the full-body parse end to end: the typed JSON line sits near the end of a
// checkpoint body that exceeds compactCheckpointCarryMaxChars, and the prior
// merge must still parse it. The display carry is now composed so the typed
// section survives the truncation, so merging against the display body keeps
// the carried state instead of silently dropping it.
func TestMergePriorTypedCheckpointStateReadsFullBodyBeyondDisplayTruncation(t *testing.T) {
	req := &modelDrivenCheckpointRequest{Args: tools.CompactContextArgs{
		ActiveObjective: "continue",
		NextStep:        "go",
		Decisions:       []string{"fresh-decision"},
	}}
	messages := []message.Message{benchmarkPriorTypedCheckpointMessage()}
	full := latestPriorCheckpointStrippedBody(messages)
	if full == "" {
		t.Fatal("prior checkpoint body must be found")
	}
	if runeCount(full) <= compactCheckpointCarryMaxChars {
		t.Fatal("fixture prior body must exceed the display carry cap to pin the truncation bug")
	}
	display := latestPriorCheckpointBody(messages)
	// The natural-language part is capped at the rune budget; the retained
	// typed section is the machine-carryable exemption, so the whole display
	// body may exceed it. What must hold is that the typed JSON survived and
	// stays parseable.
	if _, found, malformed := typedStateFromBody(display); !found || malformed {
		t.Fatalf("display carry must keep a parseable typed block found=%v malformed=%v:\n%s", found, malformed, display)
	}
	prelude := display
	if before, _, ok := strings.Cut(display, typedStateSectionHeading); ok {
		prelude = before
	}
	if runeCount(prelude) > compactCheckpointCarryMaxChars {
		t.Fatalf("natural-language display carry exceeded the rune cap: %d", runeCount(prelude))
	}
	// The merge works against the stripped full body (the head scanner passes
	// the raw body to the merge).
	merged, _, _, malformed := mergePriorTypedCheckpointState(req, full)
	if malformed {
		t.Fatal("full body with a valid typed block must not report malformed")
	}
	if merged == req {
		t.Fatal("merge must return a distinct request when a typed block is carried")
	}
	hasCarriedDecision := false
	for _, item := range merged.Args.Decisions {
		if strings.HasPrefix(item, "carried-d-") {
			hasCarriedDecision = true
		}
	}
	if !hasCarriedDecision || merged.Args.Decisions[0] != "fresh-decision" {
		t.Fatalf("full-body merge must carry prior-only decisions behind the fresh one: %v", merged.Args.Decisions)
	}
}

func runeCount(s string) int {
	n := 0
	for range s {
		n++
	}
	return n
}

// TestMergePriorTypedCheckpointStateDisclosesUnreadableCarry pins the
// malformed path: a prior body carrying a typed block that cannot be parsed
// must surface as malformed so the renderer can disclose it (the decision
// section gains typedStateUnreadableNote) instead of silently reading the
// carry as empty.
func TestMergePriorTypedCheckpointStateDisclosesUnreadableCarry(t *testing.T) {
	req := &modelDrivenCheckpointRequest{Args: tools.CompactContextArgs{
		ActiveObjective: "continue",
		NextStep:        "go",
		Decisions:       []string{"fresh-decision"},
	}}
	prior := "## Current User Request\n- continue\n\n## Typed Checkpoint State\n- {this is not valid json"
	merged, _, _, malformed := mergePriorTypedCheckpointState(req, prior)
	if !malformed {
		t.Fatal("unparseable typed block must report malformed")
	}
	if merged != req {
		t.Fatalf("malformed carry must leave the submission untouched, got %+v", merged.Args)
	}

	// End to end through the render chain: the note must land in the summary.
	a := newTestMainAgent(t, t.TempDir())
	snapshot := []message.Message{
		{Role: message.RoleUser, Content: prior, IsCompactionSummary: true},
		{Role: message.RoleUser, Content: "continue"},
	}
	summary := a.buildModelDrivenCheckpointSummary(modelDrivenBarrierSnapshot{snapshot: snapshot}, snapshot, len(snapshot), req)
	body := compactionSummaryBody(summary)
	if !strings.Contains(body, typedStateUnreadableNote) {
		t.Fatalf("unreadable carry must be disclosed in the checkpoint:\n%s", body)
	}
}

// TestCrossGenerationCarriedClaimsSurviveWithoutRestatement pins the
// cross-generation claim carry through the real render chain: a second
// generation that does NOT restate the first generation's claims must still
// carry them — classification, evidence association and status — because
// mergePriorTypedCheckpointState keeps the merged claim set on the request.
func TestCrossGenerationCarriedClaimsSurviveWithoutRestatement(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	// Round 1: a model declares two claims with kinds + evidence.
	round1Req := &modelDrivenCheckpointRequest{Args: tools.CompactContextArgs{
		ActiveObjective: "continue",
		NextStep:        "go",
		Decisions:       []string{"d1"},
		ClaimKinds: map[string]string{
			"tests pass":   "observed",
			"design final": "assumed",
		},
		ClaimEvidence: map[string][]string{
			"tests pass": {"ev-1", "ev-2"},
		},
	}}
	one := []message.Message{
		{Role: message.RoleUser, Content: "first request"},
		{Role: message.RoleAssistant, Content: "first work"},
	}
	first := a.buildModelDrivenCheckpointSummary(modelDrivenBarrierSnapshot{snapshot: one}, one, len(one), round1Req)

	// Round 2: the model does NOT restate the claims (only fresh decisions).
	round2Req := &modelDrivenCheckpointRequest{Args: tools.CompactContextArgs{
		ActiveObjective: "continue",
		NextStep:        "go",
		Decisions:       []string{"d2"},
	}}
	two := []message.Message{
		{Role: message.RoleUser, Content: first, IsCompactionSummary: true},
		{Role: message.RoleUser, Content: "second request"},
	}
	second := a.buildModelDrivenCheckpointSummary(modelDrivenBarrierSnapshot{snapshot: two}, two, len(two), round2Req)

	state, ok := typedStateForTest(compactionSummaryBody(second))
	if !ok {
		t.Fatalf("round-2 checkpoint must carry a typed block:\n%s", second)
	}
	got := state.Claims["tests pass"]
	if got.Kind != "observed" || len(got.EvidenceRefs) != 2 || got.Status == "" {
		t.Fatalf("carried claim lost classification/evidence/status: %#v", got)
	}
	if secondKind := state.Claims["design final"]; secondKind.Kind != "assumed" {
		t.Fatalf("carried assumed claim lost its kind: %#v", secondKind)
	}
	// The readable Claim Evidence section renders the same carried evidence.
	if !strings.Contains(second, "tests pass | evidence: ev-1, ev-2") {
		t.Fatalf("round-2 checkpoint must re-render the carried claim evidence:\n%s", second)
	}
	if !strings.Contains(second, "design final | kind: assumed") {
		t.Fatalf("round-2 checkpoint must re-render the carried claim kind:\n%s", second)
	}
}

// TestCarriedInvalidatedClaimStaysInvalidatedWithoutItsEvidence pins the
// invalidated-state half of the claim carry across a render chain: a claim
// invalidated in round 1 (its evidence later left the live window) must not
// come back active in round 2 when the model does not restate it. Status
// travels in the typed block, not as a re-derivation from live evidence.
func TestCarriedInvalidatedClaimStaysInvalidatedWithoutItsEvidence(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	invalidatedID := evidenceItemID(evidenceItem{Key: "ev-gone"})
	round1Req := &modelDrivenCheckpointRequest{Args: tools.CompactContextArgs{
		ActiveObjective: "continue",
		NextStep:        "go",
		ClaimKinds:      map[string]string{"claims A works": "observed"},
		ClaimEvidence:   map[string][]string{"claims A works": {invalidatedID}},
	}}
	// Round 1 renders with the evidence still marked invalidated.
	markTypedClaimsInvalidated(round1Req, []evidenceItem{{Key: "ev-gone", Validity: evidenceValidityInvalidated}})
	if round1Req.ClaimStatuses["claims A works"] != "invalidated" {
		t.Fatal("round-1 invalidated marking missing")
	}
	one := []message.Message{
		{Role: message.RoleUser, Content: "first request"},
		{Role: message.RoleAssistant, Content: "first work"},
	}
	first := a.buildModelDrivenCheckpointSummary(modelDrivenBarrierSnapshot{snapshot: one}, one, len(one), round1Req)

	// Round 2's live evidence window no longer contains the evidence at all;
	// the model does not restate the claim.
	round2Req := &modelDrivenCheckpointRequest{Args: tools.CompactContextArgs{
		ActiveObjective: "continue",
		NextStep:        "go",
	}}
	two := []message.Message{
		{Role: message.RoleUser, Content: first, IsCompactionSummary: true},
		{Role: message.RoleUser, Content: "second request"},
	}
	second := a.buildModelDrivenCheckpointSummary(modelDrivenBarrierSnapshot{snapshot: two}, two, len(two), round2Req)
	state, ok := typedStateForTest(compactionSummaryBody(second))
	if !ok {
		t.Fatalf("round-2 checkpoint must carry a typed block:\n%s", second)
	}
	if got := state.Claims["claims A works"]; got.Status != "invalidated" {
		t.Fatalf("invalidated claim must stay invalidated across generations, got %#v", got)
	}
}

// TestRestatedInvalidatedClaimReadsFreshActive pins the restatement half of
// the claim carry: when a later generation restates a claim that an earlier
// checkpoint carried as invalidated, the fresh submission wins and the claim
// reads as a fresh active claim with the new classification and evidence.
// Invalidated is the carried claim's state, not a tombstone that blocks a
// deliberate re-assertion.
func TestRestatedInvalidatedClaimReadsFreshActive(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	invalidatedID := evidenceItemID(evidenceItem{Key: "ev-gone"})
	freshID := evidenceItemID(evidenceItem{Key: "ev-new-1"})
	round1Req := &modelDrivenCheckpointRequest{Args: tools.CompactContextArgs{
		ActiveObjective: "continue",
		NextStep:        "go",
		ClaimKinds:      map[string]string{"claims A works": "observed"},
		ClaimEvidence:   map[string][]string{"claims A works": {invalidatedID}},
	}}
	markTypedClaimsInvalidated(round1Req, []evidenceItem{{Key: "ev-gone", Validity: evidenceValidityInvalidated}})
	one := []message.Message{
		{Role: message.RoleUser, Content: "first request"},
		{Role: message.RoleAssistant, Content: "first work"},
	}
	first := a.buildModelDrivenCheckpointSummary(modelDrivenBarrierSnapshot{snapshot: one}, one, len(one), round1Req)
	if state, ok := typedStateForTest(compactionSummaryBody(first)); !ok || state.Claims["claims A works"].Status != "invalidated" {
		t.Fatalf("round-1 checkpoint must carry the claim as invalidated:\n%s", first)
	}

	// Round 2 restates the same claim with fresh evidence and no status, so it
	// is a fresh submission that re-asserts the claim as active. The fresh
	// evidence is a live runtime item of the round-2 barrier, so the build's
	// invalidation pass resolves it and leaves the restated claim active.
	round2Req := &modelDrivenCheckpointRequest{Args: tools.CompactContextArgs{
		ActiveObjective: "continue",
		NextStep:        "go",
		Decisions:       []string{"d2"},
		ClaimKinds:      map[string]string{"claims A works": "observed"},
		ClaimEvidence:   map[string][]string{"claims A works": {freshID}},
	}}
	two := []message.Message{
		{Role: message.RoleUser, Content: first, IsCompactionSummary: true},
		{Role: message.RoleUser, Content: "second request"},
	}
	second := a.buildModelDrivenCheckpointSummary(modelDrivenBarrierSnapshot{snapshot: two, evidenceItems: []evidenceItem{{Key: "ev-new-1"}}}, two, len(two), round2Req)
	state, ok := typedStateForTest(compactionSummaryBody(second))
	if !ok {
		t.Fatalf("round-2 checkpoint must carry a typed block:\n%s", second)
	}
	got := state.Claims["claims A works"]
	if got.Status != "active" {
		t.Fatalf("restated claim must read as fresh active, got %#v", got)
	}
	if got.Kind != "observed" || len(got.EvidenceRefs) != 1 || got.EvidenceRefs[0] != freshID {
		t.Fatalf("restated claim must carry the fresh classification and evidence: %#v", got)
	}
	if !strings.Contains(second, "claims A works | evidence: "+freshID) {
		t.Fatalf("round-2 checkpoint must re-render the restated claim evidence:\n%s", second)
	}
}

// TestMergeTypedStatesDoesNotInheritTerminalCarriedStage pins the stage half
// of the empty-restate fix: a fresh submission that declares no stage must
// not re-render a carried completed/committed stage as the current stage of
// the new checkpoint (completed is terminal acceptance semantics that only
// the armed fresh submission can declare, because only it ran the
// committed-evidence validation). The completed work stays carried as
// decisions and claims history.
func TestMergeTypedStatesDoesNotInheritTerminalCarriedStage(t *testing.T) {
	prior := checkpointTypedState{
		StageID:     "task-one",
		StageStatus: "completed",
		Kind:        "committed",
		Decisions:   []string{"task one done"},
	}
	current := checkpointTypedState{}
	merged, _, _ := mergeCheckpointTypedStates(prior, current)
	if merged.StageID != "" || merged.StageStatus != "" || merged.Kind != "" {
		t.Fatalf("terminal carried stage must not become the current stage on an empty restate: %+v", merged)
	}
	if !containsString(merged.Decisions, "task one done") {
		t.Fatalf("completed work must stay carried as history: %v", merged.Decisions)
	}
}

// TestMergeTypedStatesKeepsInFlightCarriedStageOnEmptyRestate pins the
// in-flight half of the stage rule: a carried stage that is not terminal
// (still candidate/provisional) keeps falling back on an empty restate, so a
// checkpoint submitted without stage metadata does not lose the
// still-current stage mid-task.
func TestMergeTypedStatesKeepsInFlightCarriedStageOnEmptyRestate(t *testing.T) {
	prior := checkpointTypedState{
		StageID:     "impl",
		StageStatus: "candidate",
		Kind:        "provisional",
	}
	current := checkpointTypedState{}
	merged, _, _ := mergeCheckpointTypedStates(prior, current)
	if merged.StageID != "impl" || merged.StageStatus != "candidate" || merged.Kind != "provisional" {
		t.Fatalf("in-flight carried stage must stay current on an empty restate: %+v", merged)
	}
}

// TestMergeTypedClaimsDemotesCarriedOnlyActiveClaimToStale pins the claim
// downgrade: a claim the fresh submission does not restate keeps its kind and
// evidence but loses the trusted active posture (stale until a later
// generation restates it). Restating the claim replaces it wholesale with the
// fresh active identity, and low-trust terminal postures (invalidated) are
// never demoted.
func TestMergeTypedClaimsDemotesCarriedOnlyActiveClaimToStale(t *testing.T) {
	prior := map[string]checkpointClaim{
		"tests pass":  {Kind: "observed", EvidenceRefs: []string{"ev-old"}, Status: typedClaimStatusActive},
		"old finding": {Kind: "observed", EvidenceRefs: []string{"ev-stale"}, Status: typedClaimStatusInvalidated},
	}
	current := map[string]checkpointClaim{
		"next step": {Kind: "proposed", Status: typedClaimStatusActive},
	}
	merged, dropped := mergeTypedClaims(prior, current)
	if dropped != 0 {
		t.Fatalf("dropped = %d, want 0", dropped)
	}
	if got := merged["tests pass"]; got.Status != typedClaimStatusStale || got.Kind != "observed" || len(got.EvidenceRefs) != 1 || got.EvidenceRefs[0] != "ev-old" {
		t.Fatalf("carried-only claim must be demoted to stale while keeping kind and evidence: %#v", got)
	}
	if got := merged["old finding"]; got.Status != typedClaimStatusInvalidated {
		t.Fatalf("invalidated claim must keep its low-trust posture: %#v", got)
	}
	if got := merged["next step"]; got.Status != typedClaimStatusActive {
		t.Fatalf("fresh claim must stay active: %#v", got)
	}

	// Restating the claim replaces the carried identity: kind/evidence are
	// the fresh submission's and the status reads active again.
	restated := map[string]checkpointClaim{
		"tests pass": {Kind: "derived", EvidenceRefs: []string{"ev-new"}},
	}
	merged, dropped = mergeTypedClaims(prior, restated)
	if dropped != 0 {
		t.Fatalf("dropped = %d, want 0", dropped)
	}
	if got := merged["tests pass"]; got.Status != typedClaimStatusActive || got.Kind != "derived" || len(got.EvidenceRefs) != 1 || got.EvidenceRefs[0] != "ev-new" {
		t.Fatalf("restated claim must read as fresh active: %#v", got)
	}
}

// TestMergeTypedClaimsBoundsCarriedSetAndDisclosesOmission pins the claim-set
// bound end to end: carried-only claims beyond typedStateCarryMaxClaims are
// evicted deterministically, the merge reports the eviction count, and the
// checkpoint renderer discloses it with a note under the typed JSON line
// (which the typed parser ignores, keeping the machine block intact).
func TestMergeTypedClaimsBoundsCarriedSetAndDisclosesOmission(t *testing.T) {
	prior := make(map[string]checkpointClaim, 45)
	for i := range 45 {
		prior[fmt.Sprintf("carried claim %d", i)] = checkpointClaim{Kind: "observed", EvidenceRefs: []string{"ev-1"}, Status: typedClaimStatusActive}
	}
	current := map[string]checkpointClaim{"fresh claim": {Kind: "proposed", Status: typedClaimStatusActive}}
	merged, dropped := mergeTypedClaims(prior, current)
	if len(merged) != typedStateCarryMaxClaims {
		t.Fatalf("merged claim count = %d, want cap %d", len(merged), typedStateCarryMaxClaims)
	}
	if dropped != 45+1-typedStateCarryMaxClaims {
		t.Fatalf("dropped = %d, want %d", dropped, 45+1-typedStateCarryMaxClaims)
	}
	if _, ok := merged["fresh claim"]; !ok {
		t.Fatal("fresh claims must always survive the bound")
	}
	// The surviving carried set is deterministic under map iteration order:
	// re-merging the same claims from a differently-built map must evict the
	// same count.
	reshuffled := make(map[string]checkpointClaim, len(prior))
	maps.Copy(reshuffled, prior)
	second, _ := mergeTypedClaims(reshuffled, current)
	if len(second) != len(merged) {
		t.Fatalf("deterministic eviction violated: %d != %d", len(second), len(merged))
	}

	// The disclosure note reaches the rendered checkpoint.
	a := newTestMainAgent(t, t.TempDir())
	priorBody := "## Key Decisions\n- d-old\n\n## Typed Checkpoint State\n" + renderTypedStateJSON(checkpointTypedState{Claims: prior})
	req := &modelDrivenCheckpointRequest{Args: tools.CompactContextArgs{ActiveObjective: "continue", NextStep: "go", Decisions: []string{"fresh decision"}}}
	req, _, claimsOmitted, _ := mergePriorTypedCheckpointState(req, priorBody)
	if claimsOmitted != len(prior)-typedStateCarryMaxClaims {
		t.Fatalf("claimsOmitted = %d, want %d", claimsOmitted, len(prior)-typedStateCarryMaxClaims)
	}
	snapshot := []message.Message{
		{Role: message.RoleUser, Content: priorBody, IsCompactionSummary: true},
		{Role: message.RoleUser, Content: "continue"},
	}
	summary := a.buildModelDrivenCheckpointSummary(modelDrivenBarrierSnapshot{snapshot: snapshot}, snapshot, len(snapshot), req)
	body := compactionSummaryBody(summary)
	if !strings.Contains(body, typedStateClaimsOmittedNote) {
		t.Fatalf("claim omission must be disclosed in the checkpoint:\n%s", body)
	}
	if _, ok := typedStateForTest(body); !ok {
		t.Fatalf("disclosure note must not break the typed block:\n%s", body)
	}
}

// TestTypedCarrySurvivesUsageSummaryBetweenModelDrivenCheckpoints pins the
// usage-summary carry gap: a usage-driven summary sandwiched between two
// model-driven checkpoints declares no typed block itself, so the third
// generation merges against the nearest older checkpoint message that does
// carry one instead of silently dropping the first generation's
// machine-carryable state.
func TestTypedCarrySurvivesUsageSummaryBetweenModelDrivenCheckpoints(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	evID := evidenceItemID(evidenceItem{Key: "ev-1"})
	round1Req := &modelDrivenCheckpointRequest{Args: tools.CompactContextArgs{
		ActiveObjective: "continue",
		NextStep:        "go",
		Decisions:       []string{"d1: model-driven decision"},
		ClaimKinds:      map[string]string{"feature works": "observed"},
		ClaimEvidence:   map[string][]string{"feature works": {evID}},
		StageID:         "impl", StageStatus: "candidate", CheckpointKind: "provisional",
	}}
	one := []message.Message{
		{Role: message.RoleUser, Content: "first request"},
		{Role: message.RoleAssistant, Content: "first work"},
	}
	// Round 1 renders with its evidence live in the runtime bundle, the way a
	// real barrier capture would present a freshly observed claim.
	first := a.buildModelDrivenCheckpointSummary(modelDrivenBarrierSnapshot{snapshot: one, evidenceItems: []evidenceItem{{Key: "ev-1"}}}, one, len(one), round1Req)
	firstMsg := message.Message{Role: message.RoleUser, Content: first, IsCompactionSummary: true, CompactionSummaryMode: compactionSummaryModeModelDriven}

	// The usage-driven summary is the newest checkpoint and carries no typed
	// block of its own; the model-driven checkpoint it replaced still exists
	// as an older message in this head.
	usageBody := buildCompactionCheckpointMessage(
		"## Current User Request\n- continue the work\n\n## Progress\n- summarized progress",
		nil, message.CompactionSummaryModeModelSummary, nil)
	usageMsg := message.Message{Role: message.RoleUser, Content: usageBody, IsCompactionSummary: true, CompactionSummaryMode: message.CompactionSummaryModeModelSummary}

	round3Req := &modelDrivenCheckpointRequest{Args: tools.CompactContextArgs{
		ActiveObjective: "continue",
		NextStep:        "go",
		Decisions:       []string{"d3: fresh decision"},
	}}
	three := []message.Message{firstMsg, usageMsg, {Role: message.RoleUser, Content: "second request"}}
	third := a.buildModelDrivenCheckpointSummary(modelDrivenBarrierSnapshot{snapshot: three}, three, len(three), round3Req)
	state, ok := typedStateForTest(compactionSummaryBody(third))
	if !ok {
		t.Fatalf("round-3 checkpoint must carry a typed block:\n%s", third)
	}
	if !containsString(state.Decisions, "d1: model-driven decision") {
		t.Fatalf("first generation's decision must survive the usage summary in between: %v", state.Decisions)
	}
	if state.Decisions[0] != "d3: fresh decision" {
		t.Fatalf("fresh decision must lead: %v", state.Decisions)
	}
	got := state.Claims["feature works"]
	if got.Kind != "observed" || len(got.EvidenceRefs) != 1 {
		t.Fatalf("first generation's claim must survive with its classification and evidence: %#v", got)
	}
	if got.Status != typedClaimStatusStale {
		t.Fatalf("carried-only claim must read as stale, not active: %#v", got)
	}
}

// TestTypedCarryRecoveredFromUsageCheckpointAppendix pins the appendix half
// of the typed-carry recovery: when an intermediate usage-driven compaction
// archives a model-driven checkpoint, the model-driven body survives inside
// the newer summary's `## Previous Checkpoint` appendix (appendPriorCheckpointCarry runs
// on every mode, so the appendix always lands inside the summary body, before
// the [Context compressed] marker). The next model-driven generation must
// parse the raw body — not just the appendix-stripped body — to recover the
// typed line from the appendix.
func TestTypedCarryRecoveredFromUsageCheckpointAppendix(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	appendix := "## Key Decisions\n- carried via appendix\n\n## Typed Checkpoint State\n" +
		renderTypedStateJSON(checkpointTypedState{Decisions: []string{"d1: archived decision"}, StageID: "impl", StageStatus: "candidate", Kind: "provisional"})
	// Mirrors the usage-driven runner: appendPriorCheckpointCarry appends the
	// prior checkpoint body to the summary TEXT, and the checkpoint wrapper
	// is built around that text afterwards — so the appendix sits inside the
	// [Context Summary]..[Context compressed] body, never after the wrapper.
	summary := "## Current User Request\n- continue\n\n## Progress\n- summarized\n\n" + priorCheckpointSectionHeading + "\n" + appendix
	usageMsg := message.Message{
		Role:                  message.RoleUser,
		Content:               buildCompactionCheckpointMessage(summary, nil, message.CompactionSummaryModeModelSummary, nil),
		IsCompactionSummary:   true,
		CompactionSummaryMode: message.CompactionSummaryModeModelSummary,
	}

	req := &modelDrivenCheckpointRequest{Args: tools.CompactContextArgs{
		ActiveObjective: "continue",
		NextStep:        "go",
		Decisions:       []string{"d3: fresh decision"},
	}}
	snapshot := []message.Message{usageMsg, {Role: message.RoleUser, Content: "second request"}}
	third := a.buildModelDrivenCheckpointSummary(modelDrivenBarrierSnapshot{snapshot: snapshot}, snapshot, len(snapshot), req)
	state, ok := typedStateForTest(compactionSummaryBody(third))
	if !ok {
		t.Fatalf("round-3 checkpoint must carry a typed block:\n%s", third)
	}
	if !containsString(state.Decisions, "d1: archived decision") {
		t.Fatalf("typed state carried inside the Previous Checkpoint appendix must be merged: %v", state.Decisions)
	}
	if state.Decisions[0] != "d3: fresh decision" {
		t.Fatalf("fresh decision must lead: %v", state.Decisions)
	}
	if state.StageID != "impl" || state.StageStatus != "candidate" {
		t.Fatalf("carried stage must merge from the appendix: %+v", state)
	}
}

// TestTypedStateSurvivesTwoRealGenericAppliesIntoModelDriven drives the typed
// carry through real generic build + apply rounds: a model-driven checkpoint
// that carries typed state is archived by a usage-driven apply, then archived
// again by a second usage-driven apply, and only then does a model-driven
// generation merge the carried state. Each usage apply really replaces the
// transcript (ReplacePrefixAtomic), so the model-driven checkpoint of round 0
// is gone from the live context after the first usage apply and its typed
// block can only survive inside the carried `## Previous Checkpoint` appendix
// chain. A generic carry that stripped the appendix before extracting the
// typed state would silently sever the chain by the second usage round.
func TestTypedStateSurvivesTwoRealGenericAppliesIntoModelDriven(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.newTurn()
	a.requestBatches.reserve(a.sessionEpoch, 0)

	// Round 0: real context plus a model-driven checkpoint carrying typed
	// state (a decision and an observed claim over archived evidence).
	mdSummary := "## Current User Request\n- finish the loader\n\n## Active Objective\n- port it\n\n## Key Decisions\n- fresh loader note\n\n## Typed Checkpoint State\n" +
		renderTypedStateJSON(checkpointTypedState{
			Decisions:   []string{"d1: loader port complete"},
			StageID:     "impl",
			StageStatus: "candidate",
			Kind:        "provisional",
		})
	mdMsg := message.Message{
		Role:                  message.RoleUser,
		Content:               buildCompactionCheckpointMessage(mdSummary, nil, compactionSummaryModeModelDriven, nil),
		IsCompactionSummary:   true,
		CompactionSummaryMode: compactionSummaryModeModelDriven,
	}
	for i := range 4 {
		a.ctxMgr.Append(message.Message{Role: message.RoleUser, Content: fmt.Sprintf("base message %d", i)})
	}
	a.ctxMgr.Append(mdMsg)

	genericApply := func(planID uint64) message.Message {
		t.Helper()
		snapshot := a.ctxMgr.Snapshot()
		draft, err := a.produceCompactionDraftAsync(t.Context(), snapshot, false, planID,
			compactionTarget{sessionEpoch: a.sessionEpoch}, len(snapshot), compactionProfileArchival,
			snapshot[0].Content, nil, a.captureCompactionArchiveMeta())
		if err != nil {
			t.Fatalf("produce usage draft: %v", err)
		}
		if draft.Skip || len(draft.NewMessages) == 0 {
			t.Fatalf("unexpected usage draft: %+v", draft)
		}
		if err := a.applyCompactionDraft(draft); err != nil {
			t.Fatalf("apply usage draft: %v", err)
		}
		applied := a.ctxMgr.Snapshot()
		if len(applied) != 1 {
			t.Fatalf("usage apply must leave exactly the checkpoint, got %d messages", len(applied))
		}
		return applied[0]
	}

	// First generic build + apply archives the model-driven checkpoint; the
	// typed JSON must survive inside the carried appendix of the new usage
	// checkpoint.
	cp1 := genericApply(1)
	if !strings.Contains(cp1.Content, "d1: loader port complete") {
		t.Fatalf("first usage checkpoint must carry the typed state in its appendix:\n%s", cp1.Content)
	}

	// Second generic build + apply archives the first usage checkpoint. The
	// generic carry strips the previous `## Previous Checkpoint` appendix
	// before re-carrying; the typed state it contained must be extracted and
	// re-appended as its own machine block instead of being stripped away.
	for i := range 3 {
		a.ctxMgr.Append(message.Message{Role: message.RoleUser, Content: fmt.Sprintf("continuation message %d", i)})
	}
	cp2 := genericApply(2)
	if !strings.Contains(cp2.Content, "d1: loader port complete") {
		t.Fatalf("second usage checkpoint must keep the typed state across the appendix strip:\n%s", cp2.Content)
	}

	// A following model-driven generation merges the typed state out of the
	// second usage checkpoint instead of silently losing the round-0
	// decisions.
	head := a.ctxMgr.Snapshot()
	req := &modelDrivenCheckpointRequest{Args: tools.CompactContextArgs{
		ActiveObjective: "continue",
		NextStep:        "go",
		Decisions:       []string{"fresh decision"},
	}}
	third := a.buildModelDrivenCheckpointSummary(modelDrivenBarrierSnapshot{snapshot: head}, head, len(head), req)
	state, found, malformed := typedStateFromBody(compactionSummaryBody(third))
	if !found || malformed {
		t.Fatalf("model-driven generation after two usage applies must carry a parseable typed block found=%v malformed=%v:\n%s", found, malformed, third)
	}
	if !containsString(state.Decisions, "d1: loader port complete") {
		t.Fatalf("round-0 decision must survive two real generic applies: %v", state.Decisions)
	}
	if state.Decisions[0] != "fresh decision" {
		t.Fatalf("fresh decision must lead: %v", state.Decisions)
	}
}

// TestRestatedClaimWithArchivedEvidenceIsDemoted pins the archived-evidence
// downgrade of markTypedClaimsInvalidated: a claim the current generation
// restates against evidence that no longer exists in the runtime evidence
// (its source messages were archived by an earlier apply) must not keep
// rendering as an active observed assertion. The claim's status is downgraded
// to invalidated even though the runtime never marked that evidence
// invalidated — the ref simply cannot be resolved any more, which is
// functionally the same for the rendered posture.
func TestRestatedClaimWithArchivedEvidenceIsDemoted(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	archivedID := evidenceItemID(evidenceItem{Key: "ev-archived"})
	req := &modelDrivenCheckpointRequest{Args: tools.CompactContextArgs{
		ActiveObjective: "continue",
		NextStep:        "go",
		ClaimKinds:      map[string]string{"server contract holds": "observed"},
		ClaimEvidence:   map[string][]string{"server contract holds": {archivedID}},
	}}
	// The round-1 build resolves nothing: the evidence left the runtime (empty
	// bundle), exactly the post-apply state this fix targets.
	snapshot := []message.Message{
		{Role: message.RoleUser, Content: "first request"},
		{Role: message.RoleAssistant, Content: "first work"},
	}
	summary := a.buildModelDrivenCheckpointSummary(modelDrivenBarrierSnapshot{snapshot: snapshot}, snapshot, len(snapshot), req)
	state, ok := typedStateForTest(compactionSummaryBody(summary))
	if !ok {
		t.Fatalf("checkpoint must carry a typed block:\n%s", summary)
	}
	if got := state.Claims["server contract holds"]; got.Status != typedClaimStatusInvalidated {
		t.Fatalf("restated claim with archived evidence must read as invalidated, got %#v", got)
	}
}

// TestMarkTypedClaimsInvalidatedResolvesOnlyLiveEvidence pins the resolution
// boundary of markTypedClaimsInvalidated at the function level: a claim whose
// evidence is present and valid stays untouched (no status override), a claim
// whose evidence is invalidated is downgraded, and a claim whose evidence is
// gone from the runtime entirely is downgraded the same way. A claim that is
// already carried as stale (demoted by the merge because the current
// generation did not restate it) keeps its stale posture: stale already marks
// it as not re-asserted, and the runtime does not upgrade it to invalidated
// without a fresh restatement.
func TestMarkTypedClaimsInvalidatedResolvesOnlyLiveEvidence(t *testing.T) {
	validID := evidenceItemID(evidenceItem{Key: "ev-live"})
	invalidatedID := evidenceItemID(evidenceItem{Key: "ev-bad"})
	archivedID := evidenceItemID(evidenceItem{Key: "ev-gone"})

	// Present and valid: no override.
	live := &modelDrivenCheckpointRequest{Args: tools.CompactContextArgs{
		ClaimKinds:    map[string]string{"c": "observed"},
		ClaimEvidence: map[string][]string{"c": {validID}},
	}}
	markTypedClaimsInvalidated(live, []evidenceItem{{Key: "ev-live"}})
	if len(live.ClaimStatuses) != 0 {
		t.Fatalf("live-valid claim must keep its active posture, got %v", live.ClaimStatuses)
	}

	// Present and invalidated: downgrade.
	bad := &modelDrivenCheckpointRequest{Args: tools.CompactContextArgs{
		ClaimKinds:    map[string]string{"c": "observed"},
		ClaimEvidence: map[string][]string{"c": {invalidatedID}},
	}}
	markTypedClaimsInvalidated(bad, []evidenceItem{{Key: "ev-bad", Validity: evidenceValidityInvalidated}})
	if bad.ClaimStatuses["c"] != typedClaimStatusInvalidated {
		t.Fatalf("invalidated evidence claim status = %q, want invalidated", bad.ClaimStatuses["c"])
	}

	// Gone from the runtime: downgrade a fresh active claim.
	gone := &modelDrivenCheckpointRequest{Args: tools.CompactContextArgs{
		ClaimKinds:    map[string]string{"c": "observed"},
		ClaimEvidence: map[string][]string{"c": {archivedID}},
	}}
	markTypedClaimsInvalidated(gone, nil)
	if gone.ClaimStatuses["c"] != typedClaimStatusInvalidated {
		t.Fatalf("archived-evidence claim status = %q, want invalidated", gone.ClaimStatuses["c"])
	}

	// Carried as stale (a prior-generation claim this submission did not
	// restate) keeps stale even when its evidence is gone: the merge already
	// demoted it, and a stale claim must not read as a fresh invalidation.
	staleCarried := &modelDrivenCheckpointRequest{
		ClaimStatuses: map[string]string{},
		Claims: map[string]checkpointClaim{
			"c": {Kind: "observed", EvidenceRefs: []string{archivedID}, Status: typedClaimStatusStale},
		},
	}
	markTypedClaimsInvalidated(staleCarried, nil)
	if _, overridden := staleCarried.ClaimStatuses["c"]; overridden {
		t.Fatalf("carried stale claim must not be re-marked invalidated: %v", staleCarried.ClaimStatuses)
	}
}

// TestTypedCarrySurvivesOverLimitAppendixThroughUsageSummary drives the
// over-limit carry chain end to end: a model-driven checkpoint whose typed
// JSON line sits past the display carry cap (claims fill the body past 2400
// runes) is archived by a usage-driven compaction. The runner's verbatim
// carry truncates the natural-language body from the top and — with the fix —
// retains the typed section after it, so the next model-driven generation can
// parse the prior claims/decisions out of the `## Previous Checkpoint`
// appendix instead of silently losing them.
func TestTypedCarrySurvivesOverLimitAppendixThroughUsageSummary(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	// A model-driven checkpoint body whose typed line sits after the rune cap.
	claims := map[string]checkpointClaim{
		"feature works": {Kind: "observed", EvidenceRefs: []string{"ev-1"}, Status: typedClaimStatusActive},
		"api stable":    {Kind: "derived", Status: typedClaimStatusStale},
	}
	priorState := checkpointTypedState{
		Decisions:   []string{"d1: model-driven decision"},
		StageID:     "impl",
		StageStatus: "candidate",
		Kind:        "provisional",
		Claims:      claims,
	}
	typedLine := renderTypedStateJSON(priorState)
	filler := strings.Repeat("- carried natural-language line that keeps the body comfortably past the rune budget of the display carry cap\n", 60)
	mdSummary := "## Current User Request\n- refactor the loader\n\n## Progress\n- done\n\n## Evidence References\n" + filler + "## Typed Checkpoint State\n" + typedLine
	if runeCount(mdSummary) <= compactCheckpointCarryMaxChars {
		t.Fatal("fixture model-driven body must exceed the display carry cap")
	}
	mdMsg := message.Message{
		Role:                  message.RoleUser,
		Content:               buildCompactionCheckpointMessage(mdSummary, nil, compactionSummaryModeModelDriven, nil),
		IsCompactionSummary:   true,
		CompactionSummaryMode: compactionSummaryModeModelDriven,
	}

	// Usage-driven runner equivalence: the previous checkpoint body is carried
	// verbatim (now with the typed section retained past the truncation) and
	// the usage summary is wrapped around it.
	carry := latestPriorCheckpointBody([]message.Message{mdMsg})
	if carry == "" || !strings.Contains(carry, typedStateSectionHeading) {
		t.Fatalf("usage-driven carry must retain the typed section:\n%s", carry)
	}
	usageSummary := "## Current User Request\n- continue\n\n## Progress\n- summarized\n\n" + priorCheckpointSectionHeading + "\n" + carry
	usageMsg := message.Message{
		Role:                  message.RoleUser,
		Content:               buildCompactionCheckpointMessage(usageSummary, nil, message.CompactionSummaryModeModelSummary, nil),
		IsCompactionSummary:   true,
		CompactionSummaryMode: message.CompactionSummaryModeModelSummary,
	}

	// The next model-driven generation must still recover the typed state from
	// the appendix (the pre-fix carry dropped the line, leaving the raw-body
	// fallback nothing to find).
	priorTyped, broken := latestPriorTypedCheckpointBody([]message.Message{usageMsg})
	if broken {
		t.Fatal("over-limit appendix must not read as an unreadable typed block")
	}
	if priorTyped == "" {
		t.Fatal("typed state must be recoverable from the over-limit appendix")
	}
	state, found, malformed := typedStateFromBody(priorTyped)
	if !found || malformed {
		t.Fatalf("appendix typed state must parse found=%v malformed=%v:\n%s", found, malformed, priorTyped)
	}
	if !containsString(state.Decisions, "d1: model-driven decision") {
		t.Fatalf("prior decision lost across the over-limit appendix: %v", state.Decisions)
	}
	if got := state.Claims["feature works"]; got.Kind != "observed" || len(got.EvidenceRefs) != 1 {
		t.Fatalf("prior claim lost across the over-limit appendix: %#v", got)
	}

	// Full render chain: the third generation merges the recovered state.
	req := &modelDrivenCheckpointRequest{Args: tools.CompactContextArgs{
		ActiveObjective: "continue",
		NextStep:        "go",
		Decisions:       []string{"d3: fresh decision"},
	}}
	snapshot := []message.Message{usageMsg, {Role: message.RoleUser, Content: "second request"}}
	third := a.buildModelDrivenCheckpointSummary(modelDrivenBarrierSnapshot{snapshot: snapshot}, snapshot, len(snapshot), req)
	thirdState, ok := typedStateForTest(compactionSummaryBody(third))
	if !ok {
		t.Fatalf("round-3 checkpoint must carry a typed block:\n%s", third)
	}
	if !containsString(thirdState.Decisions, "d1: model-driven decision") {
		t.Fatalf("first generation's decision must survive the over-limit appendix: %v", thirdState.Decisions)
	}
	if got := thirdState.Claims["feature works"]; got.Kind != "observed" || len(got.EvidenceRefs) != 1 {
		t.Fatalf("first generation's claim must survive the over-limit appendix: %#v", got)
	}
}
