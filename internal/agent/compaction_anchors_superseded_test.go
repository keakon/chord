package agent

import (
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
)

func TestSupersededConstraintDetection(t *testing.T) {
	if idx := supersededConstraintMatch("修改 internal/parser.go", []string{"不要修改 internal/parser.go"}); idx != 0 {
		t.Fatalf("allow should supersede the matching prohibition, got %d", idx)
	}
	if idx := supersededConstraintMatch("不要修改 internal/parser.go", []string{"修改 internal/parser.go"}); idx != 0 {
		t.Fatalf("prohibition should supersede the matching allowance, got %d", idx)
	}
	if idx := supersededConstraintMatch("修改 internal/parser.go", []string{"不要修改 internal/lexer.go"}); idx != -1 {
		t.Fatalf("unrelated constraints must not supersede each other, got %d", idx)
	}
	if idx := supersededConstraintMatch("保持现有 API 行为不变", []string{"不要改变 API 行为"}); idx != -1 {
		t.Fatalf("near-synonyms must not be treated as contradictions, got %d", idx)
	}
	if idx := supersededConstraintMatch("modify notebook.md", []string{"not modify notebook.md"}); idx != 0 {
		t.Fatalf("'not ' with a trailing space should negate the allowance, got %d", idx)
	}
	if _, negated := constraintNegationRest("notebook.md 保持原样"); negated {
		t.Fatal("bare 'not' inside a word must not count as negation")
	}
}

func TestBuildCompactionAnchorsSupersedesContradictoryConstraint(t *testing.T) {
	first := buildCompactionAnchors(compactionAnchors{}, "req", anchorEvidence("不要修改 internal/parser.go"))
	if len(first.Constraints) != 1 || first.Constraints[0] != "不要修改 internal/parser.go" {
		t.Fatalf("first anchors = %+v", first)
	}
	// The newest instruction contradicts the older prohibition: it supersedes it
	// instead of both staying active.
	second := buildCompactionAnchors(first, "req", anchorEvidence("修改 internal/parser.go"))
	if len(second.Constraints) != 1 || second.Constraints[0] != "修改 internal/parser.go" {
		t.Fatalf("active constraints after supersession = %+v", second.Constraints)
	}
	if len(second.SupersededConstraints) != 1 || second.SupersededConstraints[0] != "不要修改 internal/parser.go" {
		t.Fatalf("superseded constraints = %+v", second.SupersededConstraints)
	}
	rendered := renderCompactionAnchors(second)
	if !strings.Contains(rendered, "- ~ 不要修改 internal/parser.go") {
		t.Fatalf("superseded constraint not rendered with the ~ marker: %q", rendered)
	}
	// The superseded state round-trips through the checkpoint.
	parsed := latestCompactionAnchors([]message.Message{checkpointWithAnchors(t, second)})
	if len(parsed.Constraints) != 1 || parsed.Constraints[0] != "修改 internal/parser.go" {
		t.Fatalf("parsed active = %+v", parsed.Constraints)
	}
	if len(parsed.SupersededConstraints) != 1 || parsed.SupersededConstraints[0] != "不要修改 internal/parser.go" {
		t.Fatalf("parsed superseded = %+v", parsed.SupersededConstraints)
	}
}

func TestSupersededHistoryCarriesForwardAndStaysBounded(t *testing.T) {
	prev := compactionAnchors{
		OriginalRequest:       "req",
		Constraints:           []string{"修改 a.go"},
		SupersededConstraints: []string{"不要修改 b.go"},
	}
	next := buildCompactionAnchors(prev, "req", nil)
	if len(next.SupersededConstraints) != 1 || next.SupersededConstraints[0] != "不要修改 b.go" {
		t.Fatalf("superseded history not carried forward: %+v", next.SupersededConstraints)
	}
	// Bounded: a large superseded history drops the oldest entries.
	overflow := compactionAnchors{OriginalRequest: "req", Constraints: []string{"修改 a.go"}}
	for i := range 12 {
		overflow = buildCompactionAnchors(overflow, "req", anchorEvidence("不要修改 file-"+string(rune('a'+i))+".go"))
	}
	if len(overflow.SupersededConstraints) > supersededConstraintMaxStored {
		t.Fatalf("superseded history unbounded: %d > %d", len(overflow.SupersededConstraints), supersededConstraintMaxStored)
	}
}

func TestVerifyAnchorsCoherence(t *testing.T) {
	if problems := verifyAnchorsCoherence(compactionAnchors{Constraints: []string{"修改 a.go"}, SupersededConstraints: []string{"不要修改 b.go"}}); len(problems) != 0 {
		t.Fatalf("coherent anchors reported problems: %v", problems)
	}
	dup := verifyAnchorsCoherence(compactionAnchors{Constraints: []string{"修改 a.go", "修改 a.go"}})
	if len(dup) == 0 {
		t.Fatal("duplicate active constraint not reported")
	}
	both := verifyAnchorsCoherence(compactionAnchors{Constraints: []string{"修改 a.go"}, SupersededConstraints: []string{"修改 a.go"}})
	if len(both) == 0 {
		t.Fatal("active+superseded coexistence not reported")
	}
	conflict := verifyAnchorsCoherence(compactionAnchors{Constraints: []string{"修改 a.go", "不要修改 a.go"}})
	if len(conflict) == 0 {
		t.Fatal("mutually contradictory active constraints not reported")
	}
}

// A stated constraint from a plain user message must not trip the superseded
// detection against an unrelated correction.
func TestSupersededDetectionIgnoresPlainRequests(t *testing.T) {
	if idx := supersededConstraintMatch("继续修 bug", []string{"不要修改 internal/parser.go"}); idx != -1 {
		t.Fatalf("plain request superseded a prohibition: %d", idx)
	}
}

// The supersede fold runs over the real selection order. Evidence selection
// returns items newest first, so folding them in that order would let the older
// instruction supersede the newest one and leave the stale direction active.
func TestAnchorConstraintsFoldInTimeOrderNotSelectionOrder(t *testing.T) {
	var tracker evidenceCandidateTracker
	// Chronological: the allowance first, then the newer prohibition.
	tracker.add(buildEvidenceItem(evidenceUserCorrection, "c", "w", "msg-1", "只改 a.go"))
	tracker.add(buildEvidenceItem(evidenceUserCorrection, "c", "w", "msg-2", "不要只改 a.go"))

	selected := evidenceItemsFromCandidates(tracker.snapshot(), 1<<20)
	if len(selected) < 2 || selected[0].Excerpt != "不要只改 a.go" {
		t.Fatalf("precondition: selection must lead with the newest item, got %+v", selected)
	}

	anchors := buildCompactionAnchors(compactionAnchors{}, "req", selected)
	if len(anchors.Constraints) != 1 || anchors.Constraints[0] != "不要只改 a.go" {
		t.Fatalf("active constraint = %v, want the newest instruction to win", anchors.Constraints)
	}
	if len(anchors.SupersededConstraints) != 1 || anchors.SupersededConstraints[0] != "只改 a.go" {
		t.Fatalf("superseded = %v, want the older allowance", anchors.SupersededConstraints)
	}
}

// Restating a constraint that was superseded in between re-activates it: the
// tracker keeps one entry per excerpt, so the retained entry's recency must be
// refreshed or the fold would replay the stale order.
func TestAnchorConstraintsReactivateAfterRestatement(t *testing.T) {
	var tracker evidenceCandidateTracker
	tracker.add(buildEvidenceItem(evidenceUserCorrection, "c", "w", "msg-1", "只改 a.go"))
	tracker.add(buildEvidenceItem(evidenceUserCorrection, "c", "w", "msg-2", "不要只改 a.go"))
	tracker.add(buildEvidenceItem(evidenceUserCorrection, "c", "w", "msg-3", "只改 a.go"))

	anchors := buildCompactionAnchors(compactionAnchors{}, "req", evidenceItemsFromCandidates(tracker.snapshot(), 1<<20))
	if len(anchors.Constraints) != 1 || anchors.Constraints[0] != "只改 a.go" {
		t.Fatalf("active = %v, want the restated instruction active again", anchors.Constraints)
	}
	if len(anchors.SupersededConstraints) != 1 || anchors.SupersededConstraints[0] != "不要只改 a.go" {
		t.Fatalf("superseded = %v, want the intermediate prohibition superseded", anchors.SupersededConstraints)
	}
}

// A repeated identical user request keeps one candidate but must refresh its
// recency: the sequence resolves which request is the latest.
func TestEvidenceTrackerRefreshesRecencyOnRestatement(t *testing.T) {
	var tracker evidenceCandidateTracker
	tracker.add(buildLatestUserRequestEvidence("msg-1", "keep fixing the build errors"))
	tracker.add(buildEvidenceItem(evidenceToolError, "e", "w", "msg-2", "exit status 1"))
	tracker.add(buildLatestUserRequestEvidence("msg-3", "keep fixing the build errors"))

	items := tracker.snapshot()
	var request, toolErr evidenceItem
	for _, item := range items {
		switch item.Kind {
		case evidenceUserRequest:
			request = item
		case evidenceToolError:
			toolErr = item
		}
	}
	if len(items) != 2 {
		t.Fatalf("tracker kept %d items, want the repeat deduplicated: %+v", len(items), items)
	}
	if request.Sequence <= toolErr.Sequence {
		t.Fatalf("restated request seq=%d must outrank the older tool error seq=%d", request.Sequence, toolErr.Sequence)
	}
}

// Superseded-only anchors must round-trip: the constraints header gates parsing
// of the "~ " lines, so omitting it would silently drop the superseded history.
func TestSupersededOnlyAnchorsRoundTrip(t *testing.T) {
	anchors := compactionAnchors{OriginalRequest: "do the thing", SupersededConstraints: []string{"不要修改 a.go"}}
	parsed := parseCompactionAnchors(renderCompactionAnchors(anchors))
	if parsed.OriginalRequest != "do the thing" {
		t.Fatalf("OriginalRequest = %q", parsed.OriginalRequest)
	}
	if len(parsed.SupersededConstraints) != 1 || parsed.SupersededConstraints[0] != "不要修改 a.go" {
		t.Fatalf("superseded = %v, want it preserved without an active constraint", parsed.SupersededConstraints)
	}
	if len(parsed.Constraints) != 0 {
		t.Fatalf("active = %v, want none", parsed.Constraints)
	}
}
