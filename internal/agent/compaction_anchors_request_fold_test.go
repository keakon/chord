package agent

import (
	"strings"
	"testing"

	"github.com/keakon/chord/internal/agent/checkpointfmt"
)

// The original request is rendered verbatim as the anchors' Original request
// line. When that message is classified as a user correction, the constraint
// window must not render the same message a second time as a shorter standing
// constraint: one instruction, one canonical copy.
func TestCompactionAnchorsOmitDerivedOriginalRequestConstraint(t *testing.T) {
	request := "不要修改公开 API，必须保持现有行为；只保留必要的改动。"
	anchors := buildCompactionAnchors(compactionAnchors{}, request, []evidenceItem{
		buildUserCorrectionEvidence("message 1 (user)", request),
	})
	if len(anchors.Constraints) != 0 {
		t.Fatalf("Constraints = %v, want the derived original request copy folded out", anchors.Constraints)
	}
	if anchors.OriginalRequest != request {
		t.Fatalf("OriginalRequest = %q, want %q", anchors.OriginalRequest, request)
	}
	rendered := renderCompactionAnchors(anchors)
	if count := strings.Count(rendered, request); count != 1 {
		t.Fatalf("rendered anchors carry the request %d times, want exactly once:\n%s", count, rendered)
	}
	// A correction from a different message in the same window still anchors.
	next := buildCompactionAnchors(anchors, request, []evidenceItem{
		buildUserCorrectionEvidence("message 2 (user)", "只能用标准库"),
	})
	if len(next.Constraints) != 1 || next.Constraints[0] != "只能用标准库" {
		t.Fatalf("Constraints = %v, want the unrelated constraint anchored", next.Constraints)
	}
}

// A checkpoint written before the fold existed already carries the derived copy
// in its active list. The next compaction must drop that copy and keep the
// request line plus the unrelated constraints.
func TestCompactionAnchorsFoldInheritedDerivedRequestConstraint(t *testing.T) {
	request := strings.Repeat("keep the public API stable. ", 40)
	previous := compactionAnchors{
		OriginalRequest: anchorLine(request, compactAnchorsRequestChars),
		Constraints: []string{
			anchorLine(request, compactAnchorsConstraintChars),
			"只能用标准库",
		},
	}
	next := buildCompactionAnchors(previous, "", nil)
	if len(next.Constraints) != 1 || next.Constraints[0] != "只能用标准库" {
		t.Fatalf("Constraints = %v, want the derived request copy folded out", next.Constraints)
	}
	if next.OriginalRequest != previous.OriginalRequest {
		t.Fatalf("OriginalRequest = %q, want %q", next.OriginalRequest, previous.OriginalRequest)
	}
}

// Folding the derived copy out of the active list must not lose the
// supersession semantics: a later contradictory instruction still retires the
// original request's direction instead of co-existing with it.
func TestCompactionAnchorsSupersedeFoldedRequestConstraint(t *testing.T) {
	request := "不要修改 internal/parser.go"
	first := buildCompactionAnchors(compactionAnchors{}, request, []evidenceItem{
		buildUserCorrectionEvidence("message 1 (user)", request),
	})
	if len(first.Constraints) != 0 {
		t.Fatalf("Constraints = %v, want the request copy folded out", first.Constraints)
	}
	second := buildCompactionAnchors(first, "", []evidenceItem{
		buildUserCorrectionEvidence("message 2 (user)", "修改 internal/parser.go"),
	})
	if len(second.Constraints) != 1 || second.Constraints[0] != "修改 internal/parser.go" {
		t.Fatalf("Constraints = %v, want the new direction active", second.Constraints)
	}
	if len(second.SupersededConstraints) != 1 || second.SupersededConstraints[0] != request {
		t.Fatalf("SupersededConstraints = %v, want the original direction retired", second.SupersededConstraints)
	}
	rendered := renderCompactionAnchors(second)
	if !strings.Contains(rendered, compactAnchorsSupersededLabel+"\n- "+request) {
		t.Fatalf("superseded request direction not rendered under its labeled list:\n%s", rendered)
	}
}

// The structured fallback renders ## User Constraints from the same correction
// evidence. With the original request's source known, the section must not
// repeat the request excerpt that the anchors already carry.
func TestStructuredFallbackOmitsOriginalRequestConstraintCopy(t *testing.T) {
	request := "must not change the public API"
	input := &compactionInput{
		EvidenceItems:         []evidenceItem{buildUserCorrectionEvidence("message 1 (user)", request)},
		OriginalRequestSource: checkpointRequestSource(request),
	}
	summary := buildStructuredFallbackSummary("~/history-1.md", input, nil, nil, nil, nil)
	section, ok := checkpointfmt.Section(summary, checkpointfmt.UserConstraintsHeading)
	if !ok {
		t.Fatalf("fallback summary missing the %s section:\n%s", checkpointfmt.UserConstraintsHeading, summary)
	}
	if strings.Contains(section, request) {
		t.Fatalf("user constraints repeat the original request:\n%s", section)
	}
	if !strings.Contains(section, "No preserved user constraints") {
		t.Fatalf("empty user constraints must keep the explicit placeholder:\n%s", section)
	}
}
