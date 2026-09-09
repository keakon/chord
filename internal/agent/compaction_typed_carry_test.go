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

	// Every bounded list contributes to the disclosure count, not only
	// decisions. Current entries beyond a cap are bounded as well.
	tooMany := checkpointTypedState{}
	for i := 0; i < typedStateCarryMaxOpenIssues+2; i++ {
		tooMany.OpenIssues = append(tooMany.OpenIssues, "issue-"+string(rune('a'+i)))
	}
	for i := 0; i < typedStateCarryMaxEvidenceRefs+2; i++ {
		tooMany.EvidenceRefs = append(tooMany.EvidenceRefs, "ev-"+string(rune('a'+i)))
	}
	merged, omitted = mergeCheckpointTypedStates(checkpointTypedState{}, tooMany)
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

func TestTypedClaimsCarryIdentityAndStatus(t *testing.T) {
	prior := checkpointTypedState{Claims: map[string]checkpointClaim{
		"tests pass": {Kind: "observed", EvidenceRefs: []string{"ev-old"}, Status: "active"},
	}}
	current := checkpointTypedState{Claims: map[string]checkpointClaim{
		"tests pass": {Kind: "derived", EvidenceRefs: []string{"ev-new"}, Status: "superseded"},
		"next step":  {Kind: "proposed", Status: "active"},
	}}
	merged, _ := mergeCheckpointTypedStates(prior, current)
	if got := merged.Claims["tests pass"]; got.Status != "superseded" || got.Kind != "derived" || len(got.EvidenceRefs) != 1 || got.EvidenceRefs[0] != "ev-new" {
		t.Fatalf("fresh claim did not replace prior identity: %#v", got)
	}
	if got := merged.Claims["next step"]; got.Status != "active" {
		t.Fatalf("new claim status = %q", got.Status)
	}
	encoded := renderTypedStateJSON(merged)
	decoded, ok := parseCheckpointTypedState("## Typed Checkpoint State\n" + encoded)
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
// the C6 fix end to end: the typed JSON line sits near the end of a checkpoint
// body that exceeds compactCheckpointCarryMaxChars, and the prior merge must
// still parse it. The display-truncated body (latestPriorCheckpointBody) drops
// the JSON before the parse and would silently skip the carry.
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
	display := latestPriorCheckpointBody(messages)
	if runeCount(display) > compactCheckpointCarryMaxChars {
		t.Fatalf("display carry must respect the rune cap, got %d", runeCount(display))
	}
	merged, _, malformed := mergePriorTypedCheckpointState(req, full)
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

	// The same merge against the display-truncated body would drop the JSON:
	// the decisions then read as fresh-only, exactly the silent loss the fix
	// removes. Assert the observable contract: callers pass the stripped full
	// body, never the display carry.
	if runeCount(full) <= compactCheckpointCarryMaxChars {
		t.Fatal("fixture prior body must exceed the display carry cap to pin the truncation bug")
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
	merged, _, malformed := mergePriorTypedCheckpointState(req, prior)
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

// TestCrossGenerationCarriedClaimsSurviveWithoutRestatement pins the C1 fix
// through the real render chain: a second generation that does NOT restate the
// first generation's claims must still carry them — classification, evidence
// association and status — because mergePriorTypedCheckpointState keeps the
// merged claim set on the request.
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

	state, ok := parseCheckpointTypedState(compactionSummaryBody(second))
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
// invalidated-state half of C1 across a render chain: a claim invalidated in
// round 1 (its evidence later left the live window) must not come back active
// in round 2 when the model does not restate it. Status travels in the typed
// block, not as a re-derivation from live evidence.
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
	state, ok := parseCheckpointTypedState(compactionSummaryBody(second))
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
	if state, ok := parseCheckpointTypedState(compactionSummaryBody(first)); !ok || state.Claims["claims A works"].Status != "invalidated" {
		t.Fatalf("round-1 checkpoint must carry the claim as invalidated:\n%s", first)
	}

	// Round 2 restates the same claim with fresh evidence and no status, so it
	// is a fresh submission that re-asserts the claim as active.
	round2Req := &modelDrivenCheckpointRequest{Args: tools.CompactContextArgs{
		ActiveObjective: "continue",
		NextStep:        "go",
		Decisions:       []string{"d2"},
		ClaimKinds:      map[string]string{"claims A works": "observed"},
		ClaimEvidence:   map[string][]string{"claims A works": {"ev-new-1"}},
	}}
	two := []message.Message{
		{Role: message.RoleUser, Content: first, IsCompactionSummary: true},
		{Role: message.RoleUser, Content: "second request"},
	}
	second := a.buildModelDrivenCheckpointSummary(modelDrivenBarrierSnapshot{snapshot: two}, two, len(two), round2Req)
	state, ok := parseCheckpointTypedState(compactionSummaryBody(second))
	if !ok {
		t.Fatalf("round-2 checkpoint must carry a typed block:\n%s", second)
	}
	got := state.Claims["claims A works"]
	if got.Status != "active" {
		t.Fatalf("restated claim must read as fresh active, got %#v", got)
	}
	if got.Kind != "observed" || len(got.EvidenceRefs) != 1 || got.EvidenceRefs[0] != "ev-new-1" {
		t.Fatalf("restated claim must carry the fresh classification and evidence: %#v", got)
	}
	if !strings.Contains(second, "claims A works | evidence: ev-new-1") {
		t.Fatalf("round-2 checkpoint must re-render the restated claim evidence:\n%s", second)
	}
}
