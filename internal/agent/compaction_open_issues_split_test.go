package agent

import (
	"slices"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

// Regression tests for the open-issue split of the typed checkpoint carry.
//
// The bug these pin: mergeCheckpointTypedStates used to merge the carried open
// issues into one flat list with the fresh submission's, so an issue that only
// an earlier checkpoint had confirmed — "tests not written", three generations
// after the tests were written — rendered exactly like a freshly confirmed
// blocker. The continuation then re-investigated finished work. The fix keeps
// the fresh submission's issues as the current generation's blockers and
// demotes the ones it does not restate into a historical bucket so they
// cannot read as newly confirmed.

func TestCarriedOnlyOpenIssueIsDemotedAndNeverReadsAsCurrent(t *testing.T) {
	prior := checkpointTypedState{OpenIssues: []string{"tests not written yet"}}
	// Generation 2 does not restate the issue: it is no longer a blocker of
	// this checkpoint, so it leaves the current list and keeps its provenance.
	merged, omitted, _, openIssuesOmitted := mergeCheckpointTypedStates(prior, checkpointTypedState{OpenIssuesComplete: true})
	if len(merged.OpenIssues) != 0 {
		t.Fatalf("a carried-only issue must not stay current: %v", merged.OpenIssues)
	}
	if omitted != 0 || openIssuesOmitted != 0 {
		t.Fatalf("demotion is not an omission: omitted=%d openIssuesOmitted=%d", omitted, openIssuesOmitted)
	}
	want := []string{"tests not written yet"}
	if !slices.Equal(merged.CarriedOpenIssues, want) {
		t.Fatalf("carried issues = %+v, want %+v", merged.CarriedOpenIssues, want)
	}

	// Generation 3: the historical entry survives the next carry unchanged —
	// still not current, still in the historical bucket —
	// so the chain never auto-upgrades it just because it survived.
	third, _, _, _ := mergeCheckpointTypedStates(merged, checkpointTypedState{})
	if len(third.OpenIssues) != 0 || !slices.Equal(third.CarriedOpenIssues, want) {
		t.Fatalf("third generation = %+v, want the same historical entry", third)
	}
}

func TestOpenIssueOmissionCarriesForwardUnlessSnapshotIsComplete(t *testing.T) {
	prior := checkpointTypedState{OpenIssues: []string{"still risky"}}
	merged, _, _, _ := mergeCheckpointTypedStates(prior, checkpointTypedState{})
	if !slices.Equal(merged.OpenIssues, []string{"still risky"}) || len(merged.CarriedOpenIssues) != 0 {
		t.Fatalf("omitted issue must remain current under incremental semantics: %+v", merged)
	}
}

func TestTruncatedOpenIssueRestatesAndRetiresByRenderedText(t *testing.T) {
	long := strings.Repeat("current risk ", typedStateCarryMaxItemRunes)
	// The carried block is the only copy a later generation sees, so the long
	// issue reaches it already truncated with the shared marker.
	prior, ok := typedStateForTest(message.CompactionTypedStateHeading + "\n" + renderTypedStateJSON(checkpointTypedState{OpenIssues: []string{long}}))
	if !ok || len(prior.OpenIssues) != 1 || !strings.HasSuffix(prior.OpenIssues[0], typedStateItemTruncatedSuffix) {
		t.Fatalf("long issue was not carried truncated: %+v", prior)
	}
	rendered := prior.OpenIssues[0]

	// Restating the truncated text verbatim is the same issue: it stays
	// current once and is not also demoted into the historical bucket.
	restated, _, _, _ := mergeCheckpointTypedStates(prior, checkpointTypedState{OpenIssues: []string{rendered}, OpenIssuesComplete: true})
	if !slices.Equal(restated.OpenIssues, []string{rendered}) || len(restated.CarriedOpenIssues) != 0 {
		t.Fatalf("verbatim restatement duplicated the issue: %+v", restated)
	}

	// Without the restatement the issue is demoted, and the rendered text the
	// model can see retires it from either bucket, however many generations
	// the carry has run.
	demoted, _, _, _ := mergeCheckpointTypedStates(prior, checkpointTypedState{OpenIssuesComplete: true})
	carried, _, _, _ := mergeCheckpointTypedStates(demoted, checkpointTypedState{})
	if !slices.Equal(carried.CarriedOpenIssues, []string{rendered}) {
		t.Fatalf("demoted issue = %+v, want the rendered text", carried.CarriedOpenIssues)
	}
	if retired := retireCheckpointItems(carried, []string{rendered}); len(retired.CarriedOpenIssues) != 0 {
		t.Fatalf("historical issue was not retired by its rendered text: %+v", retired.CarriedOpenIssues)
	}
	if retired := retireCheckpointItems(prior, []string{rendered}); len(retired.OpenIssues) != 0 {
		t.Fatalf("current issue was not retired by its rendered text: %+v", retired.OpenIssues)
	}
}

func TestRestatedOpenIssueMovesToCurrentWithoutDuplicate(t *testing.T) {
	prior := checkpointTypedState{
		OpenIssues: []string{"verify parser"},
		CarriedOpenIssues: []string{
			"old risk",
		},
	}
	// Surrounding whitespace is insignificant; exact restatement refreshes the issue.
	current := checkpointTypedState{OpenIssues: []string{"  verify parser  ", "new issue"}, OpenIssuesComplete: true}
	merged, omitted, _, openIssuesOmitted := mergeCheckpointTypedStates(prior, current)
	if omitted != 0 || openIssuesOmitted != 0 {
		t.Fatalf("omitted=%d openIssuesOmitted=%d, want 0", omitted, openIssuesOmitted)
	}
	if !slices.Equal(merged.OpenIssues, []string{"verify parser", "new issue"}) {
		t.Fatalf("current issues = %v, want the fresh spelling first", merged.OpenIssues)
	}
	want := []string{"old risk"}
	if !slices.Equal(merged.CarriedOpenIssues, want) {
		t.Fatalf("carried issues = %+v, want %+v", merged.CarriedOpenIssues, want)
	}

	// Distinct wording is never merged: "verify parser" and "verify the
	// parser" stay two issues, because identity is the trimmed text and not a
	// semantic matcher.
	distinct, _, _, _ := mergeCheckpointTypedStates(
		checkpointTypedState{OpenIssues: []string{"verify parser"}},
		checkpointTypedState{OpenIssues: []string{"verify the parser"}, OpenIssuesComplete: true},
	)
	if len(distinct.OpenIssues) != 1 || len(distinct.CarriedOpenIssues) != 1 {
		t.Fatalf("near-match must not merge: %+v", distinct)
	}
}

func TestRetiredOpenIssueLeavesBothBuckets(t *testing.T) {
	prior := checkpointTypedState{
		OpenIssues: []string{"verify parser"},
		CarriedOpenIssues: []string{
			"Old Risk",
		},
	}
	// Retirement preserves meaningful spelling and ignores surrounding whitespace.
	state := retireCheckpointItems(prior, []string{"  verify parser  ", "Old Risk"})
	if len(state.OpenIssues) != 0 || len(state.CarriedOpenIssues) != 0 {
		t.Fatalf("retirement must empty both buckets: %+v", state)
	}
	// Retirement must not mutate the carried snapshot it was handed.
	if len(prior.OpenIssues) != 1 || len(prior.CarriedOpenIssues) != 1 {
		t.Fatal("retirement mutated the source snapshot")
	}

	// A retired entry stays retired across the generations that follow: the
	// merge sees neither bucket and cannot resurrect it.
	merged, _, _, _ := mergeCheckpointTypedStates(state, checkpointTypedState{})
	if len(merged.OpenIssues) != 0 || len(merged.CarriedOpenIssues) != 0 {
		t.Fatalf("retired state resurrected: %+v", merged)
	}

	// Claims also keep their exact-after-trim retirement contract.
	withClaim := checkpointTypedState{Claims: map[string]checkpointClaim{"Use option A": {Status: typedClaimStatusActive}}}
	kept := retireCheckpointItems(withClaim, []string{"use option a"})
	if _, exists := kept.Claims["Use option A"]; !exists {
		t.Fatal("claim retirement must keep the exact-key contract")
	}
}

func TestOpenIssueBudgetIsSharedAndDisclosed(t *testing.T) {
	prior := checkpointTypedState{OpenIssues: []string{"old-a", "old-b", "old-c", "old-d", "old-e", "old-f"}}
	current := checkpointTypedState{OpenIssues: []string{"new-1", "new-2", "new-3", "new-4"}, OpenIssuesComplete: true}
	merged, omitted, _, openIssuesOmitted := mergeCheckpointTypedStates(prior, current)
	// One shared budget, not one per bucket: 4 current + 6 historical compete
	// for the same 8 slots, so the 2 oldest historical entries drop.
	if len(merged.OpenIssues)+len(merged.CarriedOpenIssues) != typedStateCarryMaxOpenIssues {
		t.Fatalf("shared budget exceeded: current=%d historical=%d", len(merged.OpenIssues), len(merged.CarriedOpenIssues))
	}
	if omitted != 2 || openIssuesOmitted != 2 {
		t.Fatalf("omitted=%d openIssuesOmitted=%d, want 2 and 2", omitted, openIssuesOmitted)
	}
	if !slices.Equal(merged.OpenIssues, []string{"new-1", "new-2", "new-3", "new-4"}) {
		t.Fatalf("current issues = %v", merged.OpenIssues)
	}
	if got := merged.CarriedOpenIssues; !slices.Equal(got, []string{"old-a", "old-b", "old-c", "old-d"}) {
		t.Fatalf("carried issues = %v, want the newest four historical entries", got)
	}
}

func TestCurrentOnlyTypedBlockOpenIssuesEnterHistoricalOnNextCarry(t *testing.T) {
	// A checkpoint without carried issues keeps its current blockers.
	currentOnly := `{"open_issues":["tests not written"],"decisions":["d1"],"stage_status":"candidate"}`
	parsed, found, malformed := typedStateFromBody(message.CompactionTypedStateHeading + "\n- " + currentOnly)
	if !found || malformed {
		t.Fatalf("currentOnly typed block must parse found=%v malformed=%v", found, malformed)
	}
	if len(parsed.CarriedOpenIssues) != 0 || !slices.Equal(parsed.OpenIssues, []string{"tests not written"}) {
		t.Fatalf("currentOnly decode = %+v", parsed)
	}

	// The next generation does not restate it: the currentOnly entry is demoted
	// like any other carried issue instead of being dropped with the format
	// change.
	req := &modelDrivenCheckpointRequest{Args: tools.CompactContextArgs{ActiveObjective: "continue", NextStep: "go"}}
	req.Args.OpenIssuesComplete = true
	merged, omitted, _, openIssuesOmitted, broken := mergePriorTypedCheckpointState(req, message.CompactionTypedStateHeading+"\n- "+currentOnly)
	if broken {
		t.Fatal("currentOnly block must not read as malformed")
	}
	if omitted != 0 || openIssuesOmitted != 0 {
		t.Fatalf("omitted=%d openIssuesOmitted=%d, want 0", omitted, openIssuesOmitted)
	}
	if len(merged.Args.OpenIssues) != 0 {
		t.Fatalf("currentOnly issue must leave the current set: %v", merged.Args.OpenIssues)
	}
	want := []string{"tests not written"}
	if !slices.Equal(merged.CarriedOpenIssues, want) {
		t.Fatalf("carried issues = %+v, want %+v", merged.CarriedOpenIssues, want)
	}
}

func TestTypedStateCarriedOpenIssuesRoundTrip(t *testing.T) {
	state := checkpointTypedState{
		OpenIssues: []string{"current blocker"},
		CarriedOpenIssues: []string{
			"older risk",
			strings.Repeat("x", typedStateCarryMaxItemRunes+50),
		},
	}
	parsed, ok := typedStateForTest(message.CompactionTypedStateHeading + "\n" + renderTypedStateJSON(state))
	if !ok {
		t.Fatal("typed state with a historical bucket must round-trip")
	}
	if !slices.Equal(parsed.OpenIssues, []string{"current blocker"}) {
		t.Fatalf("current issues = %v", parsed.OpenIssues)
	}
	if len(parsed.CarriedOpenIssues) != 2 {
		t.Fatalf("carried issues = %+v", parsed.CarriedOpenIssues)
	}
	// Over-long text is bounded with an explicit truncation marker.
	if !strings.HasSuffix(parsed.CarriedOpenIssues[1], typedStateItemTruncatedSuffix) {
		t.Fatalf("over-long carried text must be truncated: %d runes", runeLen(parsed.CarriedOpenIssues[1]))
	}

	// An empty historical bucket must not render the field at all, so a
	// checkpoint that never demoted anything stays byte-identical to one
	// written before the split.
	empty := renderTypedStateJSON(checkpointTypedState{OpenIssues: []string{"only current"}})
	if strings.Contains(empty, "carried_open_issues") || strings.Contains(empty, "generation") {
		t.Fatalf("empty historical bucket must be omitted: %s", empty)
	}
}

func TestRenderOpenProblemsSplitsCurrentFromHistorical(t *testing.T) {
	req := &modelDrivenCheckpointRequest{
		Args: tools.CompactContextArgs{
			OpenIssues: []string{"fixed thing", "current blocker"},
			Completed:  []string{"fixed thing"},
		},
		CarriedOpenIssues: []string{
			"fixed thing",
			"stale risk",
		},
	}
	section := renderOpenProblemsSection(req, 0)
	// The exact-text overlap with completed work stays de-duplicated in both
	// buckets; the near-match is not a semantic match and must survive.
	if strings.Contains(section, "fixed thing") {
		t.Fatalf("completed work must not repeat as an issue:\n%s", section)
	}
	labelAt := strings.Index(section, typedStateCarriedIssuesLabel)
	if labelAt < 0 {
		t.Fatalf("historical block must be introduced by its label:\n%s", section)
	}
	currentAt := strings.Index(section, "current blocker")
	staleAt := strings.Index(section, "stale risk")
	if currentAt < 0 || currentAt > labelAt {
		t.Fatalf("current blocker must render before the historical label:\n%s", section)
	}
	if staleAt < labelAt {
		t.Fatalf("historical issue must render after the label:\n%s", section)
	}
	if strings.Contains(section, typedStateOpenIssuesOmittedNote) {
		t.Fatalf("no omission to disclose:\n%s", section)
	}

	// The omission disclosure belongs to this section, not only to the
	// decisions it also bounds.
	omitted := renderOpenProblemsSection(req, 2)
	if !strings.Contains(omitted, typedStateOpenIssuesOmittedNote) {
		t.Fatalf("open-issue omission must be disclosed here:\n%s", omitted)
	}

	// With nothing historical the section stays exactly what it was: no label,
	// no extra bullet.
	plain := renderOpenProblemsSection(&modelDrivenCheckpointRequest{
		Args: tools.CompactContextArgs{OpenIssues: []string{"only issue"}},
	}, 0)
	if plain != "- only issue" {
		t.Fatalf("section without historical issues = %q", plain)
	}
	if empty := renderOpenProblemsSection(nil, 0); empty != "- (none reported by the model)" {
		t.Fatalf("nil request = %q", empty)
	}
}

func TestOpenIssueHelpersHandleEmptyInputs(t *testing.T) {
	// A zero budget drops every candidate and reports the whole set, matching
	// mergeTypedStateList's contract for a disabled list.
	prior := checkpointTypedState{
		OpenIssues:        []string{"prior-current"},
		CarriedOpenIssues: []string{"prior-historical"},
	}
	confirmed, carried, omitted := mergeTypedStateOpenIssues(prior, checkpointTypedState{OpenIssues: []string{"fresh"}}, 0)
	if len(confirmed) != 0 || len(carried) != 0 || omitted != 3 {
		t.Fatalf("zero budget = %v %v %d, want no survivors and 3 omissions", confirmed, carried, omitted)
	}

	// A blank entry is not an issue, and a blank retirement entry cannot match
	// one: the identity of "   " is empty, so it is skipped on both sides
	// instead of removing every blank entry at once. An internal whitespace
	// run (here a tab) remains significant.
	if key := checkpointItemKey("   \t "); key != "" {
		t.Fatalf("blank issue key = %q, want empty", key)
	}
	if key := checkpointItemKey("verify\t\tparser"); key != "verify\t\tparser" {
		t.Fatalf("internal whitespace run = %q, want it preserved", key)
	}
	if got := boundTypedStateItems([]string{"  "}); got != nil {
		t.Fatalf("blank carried entries must be dropped: %+v", got)
	}
	blank, blankOmitted, _, _ := mergeCheckpointTypedStates(
		checkpointTypedState{OpenIssues: []string{"  "}},
		checkpointTypedState{OpenIssues: []string{"\t"}},
	)
	if len(blank.OpenIssues) != 0 || len(blank.CarriedOpenIssues) != 0 || blankOmitted != 0 {
		t.Fatalf("blank issues must be dropped, not carried: %+v omitted=%d", blank, blankOmitted)
	}
	state := checkpointTypedState{CarriedOpenIssues: []string{"keep"}}
	if got := retireCheckpointItems(state, []string{"   "}); !slices.Equal(got.CarriedOpenIssues, []string{"keep"}) {
		t.Fatalf("blank retirement must not remove entries: %+v", got.CarriedOpenIssues)
	}
	// The renderer must not emit an empty bullet for a blank historical entry.
	section := renderOpenProblemsSection(&modelDrivenCheckpointRequest{
		Args:              tools.CompactContextArgs{OpenIssues: []string{"current"}},
		CarriedOpenIssues: []string{"  "},
	}, 0)
	if section != "- current" {
		t.Fatalf("blank carried entry must not render: %q", section)
	}
}

// TestCarriedOpenIssueSurvivesUsageSummaryAppendixWithoutUpgrade pins the
// recovery chain: a usage-driven compaction sandwiched between two
// model-driven checkpoints carries the typed block verbatim inside its
// `## Previous Checkpoint` appendix, truncated at the display cap with the
// typed line retained. The historical entry must survive that round trip —
// still historical, still not a current blocker — because the appendix is the
// only place the older chain's machine state lives by then.
func TestCarriedOpenIssueSurvivesUsageSummaryAppendixWithoutUpgrade(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	priorState := checkpointTypedState{
		Decisions: []string{"d1: archived decision"},
		CarriedOpenIssues: []string{
			"tests not written yet",
		},
	}
	// The filler keeps the body past the display carry cap, so the carry has
	// to truncate the natural-language part and re-append the typed line — the
	// path where the historical bucket would be lost if it travelled outside
	// the machine block.
	filler := strings.Repeat("- carried natural-language line that keeps the body past the display carry cap\n", 60)
	mdSummary := "## Current User Request\n- refactor the loader\n\n## Progress\n" + filler + "\n## Typed Checkpoint State\n" + renderTypedStateJSON(priorState)
	mdMsg := message.Message{
		Role:                  message.RoleUser,
		Content:               buildCompactionCheckpointMessage(mdSummary, nil, compactionSummaryModeModelDriven, nil),
		IsCompactionSummary:   true,
		CompactionSummaryMode: compactionSummaryModeModelDriven,
	}
	carry := latestPriorCheckpointBody([]message.Message{mdMsg})
	if carry == "" || !strings.Contains(carry, message.CompactionTypedStateHeading) {
		t.Fatalf("the usage-driven carry must retain the typed section:\n%s", carry)
	}
	usageSummary := "## Current User Request\n- continue\n\n## Progress\n- summarized\n\n" + priorCheckpointSectionHeading + "\n" + carry
	usageMsg := message.Message{
		Role:                  message.RoleUser,
		Content:               buildCompactionCheckpointMessage(usageSummary, nil, message.CompactionSummaryModeModelSummary, nil),
		IsCompactionSummary:   true,
		CompactionSummaryMode: message.CompactionSummaryModeModelSummary,
	}

	req := &modelDrivenCheckpointRequest{Args: tools.CompactContextArgs{ActiveObjective: "continue", NextStep: "go"}}
	snapshot := []message.Message{usageMsg, {Role: message.RoleUser, Content: "second request"}}
	next := a.buildModelDrivenCheckpointSummary(modelDrivenBarrierSnapshot{snapshot: snapshot}, snapshot, len(snapshot), req)
	state, ok := typedStateForTest(compactionSummaryBody(next))
	if !ok {
		t.Fatalf("the next generation must carry a typed block:\n%s", next)
	}
	if !slices.Equal(state.Decisions, []string{"d1: archived decision"}) {
		t.Fatalf("prior decisions lost across the appendix: %v", state.Decisions)
	}
	if len(state.OpenIssues) != 0 {
		t.Fatalf("the appendix must not upgrade a historical entry to current: %v", state.OpenIssues)
	}
	if got := state.CarriedOpenIssues; !slices.Equal(got, []string{"tests not written yet"}) {
		t.Fatalf("historical entry lost across the appendix: %v", got)
	}
}

// round 1 declares an open issue, round 2 does not restate it, round 3 retires
// it. The issue must never read as a current blocker, must stay recoverable
// while unretired, and must leave both buckets once retired — a resume of the
// chain must not resurrect it.
func TestOpenIssueDemotionSurvivesDurableApplyChain(t *testing.T) {
	agent := newTestMainAgent(t, t.TempDir())
	agent.newTurn()
	agent.ctxMgr.Append(message.Message{Role: message.RoleUser, Content: "implement and verify the parser"})
	request := func(openIssues, retired []string) *modelDrivenCheckpointRequest {
		return &modelDrivenCheckpointRequest{Args: tools.CompactContextArgs{
			ActiveObjective:    "implement and verify the parser",
			NextStep:           "verify remaining parser cases",
			OpenIssues:         openIssues,
			OpenIssuesComplete: true,
			RetiredItems:       retired,
		}}
	}

	agent.ctxMgr.Append(message.Message{Role: message.RoleAssistant, Content: "round 1"})
	e2eApplyModelDrivenCheckpoint(t, agent, 1, len(agent.ctxMgr.Snapshot())-1, request([]string{"tests not written yet"}, nil))
	first := e2eTypedStateOf(t, e2eCheckpointAt(t, agent))
	if !slices.Equal(first.OpenIssues, []string{"tests not written yet"}) {
		t.Fatalf("round-1 open issues = %v", first.OpenIssues)
	}

	agent.ctxMgr.Append(message.Message{Role: message.RoleAssistant, Content: "round 2"})
	e2eApplyModelDrivenCheckpoint(t, agent, 2, len(agent.ctxMgr.Snapshot())-1, request(nil, nil))
	secondCheckpoint := e2eCheckpointAt(t, agent)
	second := e2eTypedStateOf(t, secondCheckpoint)
	if len(second.OpenIssues) != 0 {
		t.Fatalf("round 2 must not keep the un-restated issue current: %v", second.OpenIssues)
	}
	if got := second.CarriedOpenIssues; !slices.Equal(got, []string{"tests not written yet"}) {
		t.Fatalf("round-2 carried issues = %v", got)
	}
	// The rendered section has to say what the carried entry is: a reader must
	// not take it for a current blocker.
	openProblems, ok := markdownSection(compactionSummaryBody(secondCheckpoint.Content), "## Open Problems")
	if !ok || !strings.Contains(openProblems, typedStateCarriedIssuesLabel) {
		t.Fatalf("round-2 ## Open Problems must label the historical block:\n%s", openProblems)
	}
	if at, labelAt := strings.Index(openProblems, "tests not written yet"), strings.Index(openProblems, typedStateCarriedIssuesLabel); at < labelAt {
		t.Fatalf("round-2 carried issue must render after the label:\n%s", openProblems)
	}

	agent.ctxMgr.Append(message.Message{Role: message.RoleAssistant, Content: "round 3"})
	e2eApplyModelDrivenCheckpoint(t, agent, 3, len(agent.ctxMgr.Snapshot())-1, request(nil, []string{"tests not written yet"}))
	thirdCheckpoint := e2eCheckpointAt(t, agent)
	third := e2eTypedStateOf(t, thirdCheckpoint)
	if len(third.OpenIssues) != 0 || len(third.CarriedOpenIssues) != 0 {
		t.Fatalf("round 3 must retire the issue from both buckets: %+v", third)
	}
	if body := compactionSummaryBody(thirdCheckpoint.Content); strings.Contains(body, "tests not written yet") {
		t.Fatalf("a retired issue must not survive in the checkpoint:\n%s", body)
	}
}
