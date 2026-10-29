package agent

import (
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/ctxmgr"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func TestValidateModelDrivenCheckpointKindEvidenceRequirements(t *testing.T) {
	// Committed checkpoints are authoritative and must anchor to evidence.
	for _, args := range []tools.CompactContextArgs{
		{CheckpointKind: "committed"},
		{CheckpointKind: "committed", StageStatus: "completed"},
	} {
		err := validateModelDrivenCheckpointKind(args)
		if err == nil {
			t.Fatalf("expected evidence requirement for %#v", args)
		}
		// Rejection must point at the provisional escape hatch so the model
		// does not burn retries fabricating evidence the session never had.
		if !strings.Contains(err.Error(), "retry with checkpoint_kind=provisional") {
			t.Fatalf("rejection %q lacks the provisional downgrade guidance for %#v", err, args)
		}
	}
	// A provisional completed stage carries no claim-classification semantics
	// that require evidence; only observed claims (checked in the observed
	// validators) and committed checkpoints must anchor to evidence IDs,
	// which the model may not even have in view before the first checkpoint.
	if err := validateModelDrivenCheckpointKind(tools.CompactContextArgs{StageStatus: "completed"}); err != nil {
		t.Fatalf("provisional completed stage without evidence should be accepted: %v", err)
	}
	if err := validateModelDrivenCheckpointKind(tools.CompactContextArgs{CheckpointKind: "committed", StageStatus: "completed", EvidenceRefs: []string{"e-1"}}); err != nil {
		t.Fatalf("valid committed checkpoint rejected: %v", err)
	}
	if err := validateModelDrivenCheckpointKind(tools.CompactContextArgs{CheckpointKind: "committed", EvidenceRefs: []string{"e-1"}}); err == nil {
		t.Fatal("committed checkpoint without completed stage should be rejected")
	}
}

func TestValidateModelDrivenEvidenceRefsUsesFullTracker(t *testing.T) {
	a := &MainAgent{}
	a.evidence.add(evidenceItem{Kind: evidenceToolDiff, Key: "full-evidence", Excerpt: "important"})
	if err := a.validateModelDrivenEvidenceRefs("evidence_refs", []string{evidenceItemID(a.evidence.snapshot()[0])}); err != nil {
		t.Fatalf("full tracker evidence was rejected: %v", err)
	}
}

func TestValidateModelDrivenClaimKindsObservedRequiresEvidence(t *testing.T) {
	args := tools.CompactContextArgs{
		Completed:  []string{"tests pass"},
		ClaimKinds: map[string]string{"tests pass": "observed"},
	}
	if err := validateModelDrivenCheckpointKind(args); err == nil {
		t.Fatal("observed claim without evidence should be rejected")
	}
}

// A rejection whose remedy depends on runtime state must carry that state: the
// observed-without-evidence error names the evidence IDs this context actually
// resolves, so the model can cite one or reclassify the claim instead of
// guessing what "observed" required.
func TestExplainCheckpointRejectionHintsResolvableEvidence(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	a.evidence.add(evidenceItem{Kind: evidenceToolDiff, Key: "resolvable-diff", Excerpt: "diff"})
	id := evidenceItemID(a.evidence.snapshot()[0])

	err := a.explainCheckpointRejection(checkpointClaimNeedsEvidenceError{claim: "tests pass"})
	if err == nil {
		t.Fatal("observed claim without evidence must stay rejected")
	}
	for _, want := range []string{"tests pass", id, "claim_evidence", "top-level evidence_refs", "derived/assumed/proposed"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("rejection hint %q must contain %q", err, want)
		}
	}
}

// Only the evidence check needs runtime state appended; every other violation
// text is already complete, so wrapping it would bury the reason.
func TestExplainCheckpointRejectionLeavesOtherErrorsUnchanged(t *testing.T) {
	a := &MainAgent{}
	original := errors.New("committed compact_context requires stage_status=completed")
	if got := a.explainCheckpointRejection(original); got != original {
		t.Fatalf("non-evidence rejection must pass through unchanged, got %v", got)
	}
}

func TestResolvableEvidenceHintOmitsNegativeAndInvalidated(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	a.evidence.add(evidenceItem{Kind: evidenceToolDiff, Key: "positive", Excerpt: "diff"})
	a.evidence.add(evidenceItem{Kind: evidenceToolError, Key: "negative", Excerpt: "failed"})
	a.evidence.add(evidenceItem{Kind: evidenceToolDiff, Key: "stale", Excerpt: "old", Validity: evidenceValidityInvalidated})
	byKey := make(map[string]string)
	for _, item := range a.evidence.snapshot() {
		byKey[item.Key] = evidenceItemID(item)
	}

	hint := a.resolvableEvidenceHint()
	if !strings.Contains(hint, byKey["positive"]) {
		t.Fatalf("hint must list the positive evidence ID, got %q", hint)
	}
	if strings.Contains(hint, byKey["negative"]) || strings.Contains(hint, byKey["stale"]) {
		t.Fatalf("hint must omit negative and invalidated evidence, got %q", hint)
	}
}

// A checkpoint evidence pack is the only place an archived item's ID remains
// citable, and its rendered classification and validity must filter the hint
// the same way live records do, or following the hint would trade one
// rejection for another.
func TestResolvableEvidenceHintOmitsNegativeAndInvalidatedPackIDs(t *testing.T) {
	a := &MainAgent{tools: tools.NewRegistry(), ctxMgr: ctxmgr.NewManager(10000, 1000)}
	positive := buildEvidenceItem(evidenceToolDiff, "positive", "needed", "tool", "diff")
	negative := buildEvidenceItem(evidenceToolError, "negative", "needed", "tool", "failed")
	invalidated := buildEvidenceItem(evidenceToolDiff, "invalidated", "needed", "tool", "stale")
	invalidated.Validity = evidenceValidityInvalidated
	checkpoint := buildCompactionCheckpointMessage("## Current User Request\n- continue", nil, compactionSummaryModeModelDriven, []evidenceItem{positive, negative, invalidated})
	a.ctxMgr.Append(message.Message{Role: message.RoleUser, Content: checkpoint, IsCompactionSummary: true})

	hint := a.resolvableEvidenceHint()
	if !strings.Contains(hint, evidenceItemID(positive)) {
		t.Fatalf("hint must list the positive pack ID, got %q", hint)
	}
	for _, item := range []evidenceItem{negative, invalidated} {
		if strings.Contains(hint, evidenceItemID(item)) {
			t.Fatalf("hint must omit pack evidence %s %q, got %q", item.Kind, evidenceItemID(item), hint)
		}
	}
}

func TestResolvableEvidenceHintWithoutEvidence(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	hint := a.resolvableEvidenceHint()
	if !strings.Contains(hint, "no evidence ID is resolvable in this context") {
		t.Fatalf("hint without evidence must say none is resolvable, got %q", hint)
	}
}

// The unknown-ID exit must be as self-correcting as the observed path: a
// rejection without the resolvable menu left the model to regenerate an
// ev-<12hex> shape from memory (the reported session's invented-ID failures).
func TestUnknownEvidenceRefHintListsResolvableEvidence(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	a.evidence.add(evidenceItem{Kind: evidenceToolDiff, Key: "unknown-ref", Excerpt: "diff"})
	id := evidenceItemID(a.evidence.snapshot()[0])
	err := a.validateModelDrivenEvidenceRefs("evidence_refs", []string{"ev-000000000000"})
	if err == nil {
		t.Fatal("invented evidence ID must stay rejected")
	}
	for _, want := range []string{"ev-000000000000", id, "evidence IDs resolvable in this context"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("unknown-ID rejection %q must carry the resolvable menu entry %q", err, want)
		}
	}
	if err := a.validateModelDrivenEvidenceRefs("evidence_refs", []string{id}); err != nil {
		t.Fatalf("live tracker ID must stay resolvable: %v", err)
	}
}

func TestUnknownEvidenceRefHintListsPackIDs(t *testing.T) {
	positive := buildEvidenceItem(evidenceToolDiff, "unknown-pack", "needed", "tool", "diff")
	checkpoint := buildCompactionCheckpointMessage("## Current User Request\n- continue", nil, compactionSummaryModeModelDriven, []evidenceItem{positive})
	a := &MainAgent{tools: tools.NewRegistry(), ctxMgr: ctxmgr.NewManager(10000, 1000)}
	a.ctxMgr.Append(message.Message{Role: message.RoleUser, Content: checkpoint, IsCompactionSummary: true})
	err := a.validateModelDrivenEvidenceRefs("evidence_refs", []string{"ev-000000000000"})
	if err == nil {
		t.Fatal("invented ID must stay rejected with a pack in context")
	}
	if !strings.Contains(err.Error(), evidenceItemID(positive)) {
		t.Fatalf("unknown-ID rejection %q must list the pack-resolvable ID %q", err, evidenceItemID(positive))
	}
}

func TestUnknownEvidenceRefHintWithoutEvidence(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	hint := a.unknownEvidenceRefHint()
	if !strings.Contains(hint, "no evidence ID is resolvable in this context") {
		t.Fatalf("hint without evidence must say none is resolvable, got %q", hint)
	}
	if !strings.Contains(hint, "derived/assumed/proposed") {
		t.Fatalf("empty menu must keep the downgrade path actionable, got %q", hint)
	}
	if strings.Contains(hint, "resolvable in this context: ") {
		t.Fatalf("hint without evidence must not render an ID list, got %q", hint)
	}
}

// End-to-end through the arm path: the rejection the model sees must be the
// hinted one, and a rejected request must not arm a pending checkpoint.
func TestTryArmModelDrivenCheckpointHintsResolvableEvidence(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	a.newTurn("")
	a.evidence.add(evidenceItem{Kind: evidenceToolDiff, Key: "arm-hint-diff", Excerpt: "diff"})
	id := evidenceItemID(a.evidence.snapshot()[0])
	ccID := "cc-observed"
	a.ctxMgr.Append(message.Message{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{testToolCall(ccID, tools.NameCompactContext)}})

	args := `{"active_objective":"a","next_step":"b","checkpoint_kind":"committed","stage_status":"completed","completed":["tests pass"],"claim_kinds":{"tests pass":"observed"}}`
	_, err := a.tryArmModelDrivenCheckpoint(ccID, args)
	if err == nil {
		t.Fatal("committed observed claim without claim_evidence must be rejected")
	}
	if !strings.Contains(err.Error(), id) {
		t.Fatalf("rejection %q must name the resolvable evidence ID %q", err, id)
	}
	if a.pendingModelDriven != nil {
		t.Fatal("a rejected request must not arm a pending checkpoint")
	}
}

// Same guarantee for the unknown-ID exit: a rejected evidence_refs entry must
// carry the resolvable menu, and a rejected request must never arm a pending
// checkpoint.
func TestTryArmModelDrivenCheckpointUnknownEvidenceRefHintsResolvable(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	a.newTurn("")
	a.evidence.add(evidenceItem{Kind: evidenceToolDiff, Key: "unknown-arm", Excerpt: "diff"})
	id := evidenceItemID(a.evidence.snapshot()[0])
	ccID := "cc-unknown"
	a.ctxMgr.Append(message.Message{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{testToolCall(ccID, tools.NameCompactContext)}})

	args := `{"active_objective":"a","next_step":"b","evidence_refs":["ev-000000000000"]}`
	_, err := a.tryArmModelDrivenCheckpoint(ccID, args)
	if err == nil {
		t.Fatal("unknown evidence ID must be rejected at arm time")
	}
	if !strings.Contains(err.Error(), id) {
		t.Fatalf("rejection %q must name the resolvable evidence ID %q", err, id)
	}
	if a.pendingModelDriven != nil {
		t.Fatal("a rejected request must not arm a pending checkpoint")
	}
}

// A provisional checkpoint keeps a weak observed claim as derived instead of
// rejecting the whole request, and tells the model which claims moved.
func TestTryArmModelDrivenCheckpointDowngradesUnsupportedObservedClaims(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	a.newTurn("")
	a.evidence.add(evidenceItem{Kind: evidenceToolDiff, Key: "good", Excerpt: "diff"})
	a.evidence.add(evidenceItem{Kind: evidenceToolError, Key: "bad", Excerpt: "failed"})
	a.evidence.add(evidenceItem{Kind: evidenceToolDiff, Key: "stale", Excerpt: "old diff", Validity: evidenceValidityInvalidated})
	byKey := map[string]string{}
	for _, item := range a.evidence.snapshot() {
		byKey[item.Key] = evidenceItemID(item)
	}
	ccID := "cc-downgrade"
	a.ctxMgr.Append(message.Message{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{testToolCall(ccID, tools.NameCompactContext)}})

	args := fmt.Sprintf(`{"active_objective":"a","next_step":"b","completed":["edited","no evidence","error only","stale only","mixed"],`+
		`"claim_kinds":{"edited":"observed","no evidence":"observed","error only":"observed","stale only":"observed","mixed":"observed"},`+
		`"claim_evidence":{"edited":[%q],"error only":[%q],"stale only":[%q],"mixed":[%q,%q]}}`, byKey["good"], byKey["bad"], byKey["stale"], byKey["good"], byKey["stale"])
	result, err := a.tryArmModelDrivenCheckpoint(ccID, args)
	if err != nil {
		t.Fatalf("provisional checkpoint with weak observed claims must be accepted: %v", err)
	}
	if a.pendingModelDriven == nil {
		t.Fatal("accepted request must arm a pending checkpoint")
	}
	got := a.pendingModelDriven.Args
	want := map[string]string{"edited": claimKindObserved, "no evidence": tools.CompactContextClaimDerived, "error only": tools.CompactContextClaimDerived, "stale only": tools.CompactContextClaimDerived, "mixed": tools.CompactContextClaimDerived}
	for claim, kind := range want {
		if got.ClaimKinds[claim] != kind {
			t.Fatalf("claim %q kind = %q, want %q", claim, got.ClaimKinds[claim], kind)
		}
	}
	if slices.Contains(got.EvidenceRefs, byKey["stale"]) || got.ClaimEvidence["stale only"] != nil {
		t.Fatalf("invalidated evidence must be dropped from the armed request: %+v", got)
	}
	if !slices.Equal(got.ClaimEvidence["error only"], []string{byKey["bad"]}) {
		t.Fatalf("negative evidence stays cited by the derived claim: %+v", got.ClaimEvidence)
	}
	// A valid reference does not shield a defective one cited beside it: the
	// claim still moves to derived, keeping only the usable reference.
	if !slices.Equal(got.ClaimEvidence["mixed"], []string{byKey["good"]}) {
		t.Fatalf("mixed claim must keep only its valid evidence: %+v", got.ClaimEvidence)
	}
	for _, want := range []string{requestAcceptedToolResult, "Recorded as derived instead of observed", `"no evidence" cited no claim_evidence`, `"error only" cannot use tool_error evidence`, `"stale only" cannot use invalidated evidence`} {
		if !strings.Contains(result, want) {
			t.Fatalf("result %q must contain %q", result, want)
		}
	}
	if strings.Contains(result, `"edited"`) {
		t.Fatalf("a supported observed claim must not be reported as downgraded: %q", result)
	}
}

// A negative record that later went stale (a partially applied patch whose
// successful file changed again) must report both defects: the downgrade keeps
// a merely weak reference but has to drop an invalidated one, otherwise the
// armed request still carries the invalidated ID and the reference check
// rejects the whole provisional checkpoint — the very rejection the downgrade
// exists to avoid. Both resolution sources are covered: a live tracker item and
// a carried checkpoint-pack row.
func TestTryArmModelDrivenCheckpointDowngradesInvalidatedNegativeEvidence(t *testing.T) {
	t.Parallel()
	staleError := evidenceItem{Kind: evidenceToolError, Key: "stale error", Excerpt: "patch partially failed", Validity: evidenceValidityInvalidated}
	staleErrorID := evidenceItemID(staleError)

	// Live: the tracker still holds the invalidated tool_error item.
	live := newTestMainAgent(t, t.TempDir())
	live.newTurn("")
	live.evidence.add(evidenceItem{Kind: evidenceToolDiff, Key: "good", Excerpt: "diff"})
	live.evidence.add(staleError)
	goodID := ""
	for _, item := range live.evidence.snapshot() {
		if item.Key == "good" {
			goodID = evidenceItemID(item)
		}
	}
	ccID := "cc-stale-error-live"
	live.ctxMgr.Append(message.Message{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{testToolCall(ccID, tools.NameCompactContext)}})
	args := fmt.Sprintf(`{"active_objective":"a","next_step":"b","completed":["edited","patch needs inspection"],`+
		`"claim_kinds":{"edited":"observed","patch needs inspection":"observed"},`+
		`"claim_evidence":{"edited":[%q],"patch needs inspection":[%q]}}`, goodID, staleErrorID)
	if _, err := live.tryArmModelDrivenCheckpoint(ccID, args); err != nil {
		t.Fatalf("provisional checkpoint over an invalidated tool_error must arm, got %v", err)
	}
	assertDowngradedStaleError(t, live.pendingModelDriven.Args, goodID, staleErrorID)

	// Carried: the tracker was rebuilt after an apply, so the ID only survives
	// in a checkpoint evidence pack row that renders the kind and validity.
	carried := newTestMainAgent(t, t.TempDir())
	carried.newTurn("")
	checkpoint := buildCompactionCheckpointMessage("## Current User Request\n- continue", nil, compactionSummaryModeModelDriven, []evidenceItem{staleError})
	if !strings.Contains(checkpoint, "Evidence Kind: tool_error") || !strings.Contains(checkpoint, "Validity: invalidated") {
		t.Fatalf("fixture pack must render the negative kind and its invalidation:\n%s", checkpoint)
	}
	carried.ctxMgr.Append(message.Message{Role: message.RoleUser, Content: checkpoint, IsCompactionSummary: true})
	carried.ctxMgr.Append(message.Message{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{testToolCall(ccID, tools.NameCompactContext)}})
	carriedArgs := fmt.Sprintf(`{"active_objective":"a","next_step":"b","completed":["patch needs inspection"],`+
		`"claim_kinds":{"patch needs inspection":"observed"},`+
		`"claim_evidence":{"patch needs inspection":[%q]}}`, staleErrorID)
	if _, err := carried.tryArmModelDrivenCheckpoint(ccID, carriedArgs); err != nil {
		t.Fatalf("provisional checkpoint over a carried invalidated tool_error must arm, got %v", err)
	}
	assertDowngradedStaleError(t, carried.pendingModelDriven.Args, "", staleErrorID)
}

// assertDowngradedStaleError pins the armed request after the downgrade: the
// claim moved to derived, the invalidated reference is gone from both fields,
// and any valid reference cited elsewhere is kept.
func assertDowngradedStaleError(t *testing.T, got tools.CompactContextArgs, goodID, staleID string) {
	t.Helper()
	if got.ClaimKinds["patch needs inspection"] != tools.CompactContextClaimDerived {
		t.Fatalf("claim kind = %q, want derived", got.ClaimKinds["patch needs inspection"])
	}
	if slices.Contains(got.EvidenceRefs, staleID) {
		t.Fatalf("invalidated evidence must be dropped from evidence_refs: %+v", got)
	}
	if slices.Contains(got.ClaimEvidence["patch needs inspection"], staleID) {
		t.Fatalf("invalidated evidence must be dropped from the downgraded claim: %+v", got.ClaimEvidence)
	}
	if goodID != "" {
		if got.ClaimKinds["edited"] != claimKindObserved || !slices.Contains(got.EvidenceRefs, goodID) {
			t.Fatalf("an unrelated supported claim must stay observed with its evidence: %+v", got)
		}
	}
}

// An invented ID is a fabrication, not weak evidence: it stays rejected even on
// a provisional checkpoint.
func TestTryArmModelDrivenCheckpointKeepsRejectingUnknownClaimEvidence(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	a.newTurn("")
	ccID := "cc-invented"
	a.ctxMgr.Append(message.Message{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{testToolCall(ccID, tools.NameCompactContext)}})
	args := `{"active_objective":"a","next_step":"b","completed":["x"],"claim_kinds":{"x":"observed"},"claim_evidence":{"x":["ev-000000000000"]}}`
	if _, err := a.tryArmModelDrivenCheckpoint(ccID, args); err == nil {
		t.Fatal("invented claim evidence must stay rejected")
	}
	if a.pendingModelDriven != nil {
		t.Fatal("a rejected request must not arm a pending checkpoint")
	}
}

func TestValidateObservedClaimEvidenceMustBeListed(t *testing.T) {
	args := tools.CompactContextArgs{
		Completed: []string{"tests pass"}, EvidenceRefs: []string{"ev-1"},
		ClaimKinds:    map[string]string{"tests pass": "observed"},
		ClaimEvidence: map[string][]string{"tests pass": {"ev-2"}},
	}
	if err := validateModelDrivenCheckpointKind(args); err == nil {
		t.Fatal("observed claim evidence outside evidence_refs should be rejected")
	}
}

func TestValidateObservedClaimEvidenceRejectsErrorEvidence(t *testing.T) {
	a := &MainAgent{}
	a.evidence.add(evidenceItem{Kind: evidenceToolError, Key: "err", Excerpt: "failed"})
	id := evidenceItemID(a.evidence.snapshot()[0])
	args := tools.CompactContextArgs{ClaimKinds: map[string]string{"tests pass": "observed"}, ClaimEvidence: map[string][]string{"tests pass": {id}}}
	if err := a.validateObservedClaimEvidence(args); err == nil {
		t.Fatal("error evidence should not support observed completion")
	}
}

func TestValidateCommittedEvidenceRejectsErrorEvidence(t *testing.T) {
	a := &MainAgent{}
	a.evidence.add(evidenceItem{Kind: evidenceToolError, Key: "err", Excerpt: "failed"})
	id := evidenceItemID(a.evidence.snapshot()[0])
	if err := a.validateCommittedEvidence(tools.CompactContextArgs{CheckpointKind: "committed", EvidenceRefs: []string{id}}); err == nil {
		t.Fatal("committed checkpoint should reject error evidence")
	}
}

// TestValidateCommittedEvidenceRejectsEscalateEvidence pins the committed
// evidence consistency rule: escalate is a negative outcome record (an open
// intervention request) exactly like tool_error and done_rejected, so
// committed checkpoints reject it uniformly instead of letting an
// escalated-but-unfinished stage read as completed.
func TestValidateCommittedEvidenceRejectsEscalateEvidence(t *testing.T) {
	a := &MainAgent{}
	a.evidence.add(evidenceItem{Kind: evidenceEscalate, Key: "esc", Excerpt: "subagent requests intervention"})
	id := evidenceItemID(a.evidence.snapshot()[0])
	if err := a.validateCommittedEvidence(tools.CompactContextArgs{CheckpointKind: "committed", EvidenceRefs: []string{id}}); err == nil {
		t.Fatal("committed checkpoint should reject escalate evidence")
	}
}

// TestValidateCommittedEvidenceJudgesArchivedPackEvidenceByRenderedKind pins
// the archived half of the cross-generation evidence chain: after an apply
// archives the negative record's source messages, its ID survives only inside
// the checkpoint evidence pack. The pack now renders the machine category, so
// committed acceptance judges carried references by the same rules as live
// ones — an archived escalate/tool_error cannot upgrade a stage to committed —
// while a record that only proves ID existence (an older pack format without a
// classification) cannot support the committed upgrade either.
func TestValidateCommittedEvidenceJudgesArchivedPackEvidenceByRenderedKind(t *testing.T) {
	newAgentWithCheckpoint := func(item evidenceItem) (*MainAgent, string) {
		t.Helper()
		checkpoint := buildCompactionCheckpointMessage("## Current User Request\n- continue", nil, compactionSummaryModeModelDriven, []evidenceItem{item})
		a := &MainAgent{tools: tools.NewRegistry(), ctxMgr: ctxmgr.NewManager(10000, 1000)}
		a.ctxMgr.Append(message.Message{Role: message.RoleUser, Content: checkpoint, IsCompactionSummary: true})
		return a, checkpoint
	}

	// Archived negative records stay rejected for committed and for observed
	// claims alike.
	escalate := buildEvidenceItem(evidenceEscalate, "subagent intervention open", "needed", "tool", "requests intervention")
	a, checkpoint := newAgentWithCheckpoint(escalate)
	if !strings.Contains(checkpoint, "Evidence Kind: escalate") {
		t.Fatalf("checkpoint pack must render the machine category:\n%s", checkpoint)
	}
	escalateID := evidenceItemID(escalate)
	if err := a.validateCommittedEvidence(tools.CompactContextArgs{CheckpointKind: "committed", EvidenceRefs: []string{escalateID}}); err == nil {
		t.Fatal("committed checkpoint over an archived escalate record must be rejected")
	}
	if err := a.validateObservedClaimEvidence(tools.CompactContextArgs{
		ClaimKinds:    map[string]string{"tests pass": "observed"},
		ClaimEvidence: map[string][]string{"tests pass": {escalateID}},
	}); err == nil {
		t.Fatal("observed claim over an archived escalate record must be rejected")
	}

	toolError := buildEvidenceItem(evidenceToolError, "Go build failure", "blocker", "tool", "undefined: foo")
	b, _ := newAgentWithCheckpoint(toolError)
	toolErrorID := evidenceItemID(toolError)
	if err := b.validateCommittedEvidence(tools.CompactContextArgs{CheckpointKind: "committed", EvidenceRefs: []string{toolErrorID}}); err == nil {
		t.Fatal("committed checkpoint over an archived tool_error record must be rejected")
	}

	// A positive archived record with a rendered kind still supports the
	// committed upgrade: that is the primary cross-generation use case.
	diff := buildEvidenceItem(evidenceToolDiff, "Recent code diff", "needed", "tool", "diff content")
	c, _ := newAgentWithCheckpoint(diff)
	diffID := evidenceItemID(diff)
	if err := c.validateCommittedEvidence(tools.CompactContextArgs{CheckpointKind: "committed", EvidenceRefs: []string{diffID}}); err != nil {
		t.Fatalf("committed checkpoint over an archived positive record must validate: %v", err)
	}

	_, diffCheckpoint := newAgentWithCheckpoint(diff)
	var legacyLines []string
	for line := range strings.SplitSeq(diffCheckpoint, "\n") {
		if !strings.HasPrefix(line, "Evidence Kind: ") {
			legacyLines = append(legacyLines, line)
		}
	}
	legacyContent := strings.Join(legacyLines, "\n")
	legacyAgent := &MainAgent{tools: tools.NewRegistry(), ctxMgr: ctxmgr.NewManager(10000, 1000)}
	legacyAgent.ctxMgr.Append(message.Message{Role: message.RoleUser, Content: legacyContent, IsCompactionSummary: true})
	if err := legacyAgent.validateModelDrivenEvidenceRefs("evidence_refs", []string{diffID}); err != nil {
		t.Fatalf("presence-only pack ID must still resolve as an evidence reference: %v", err)
	}
	if err := legacyAgent.validateObservedClaimEvidence(tools.CompactContextArgs{
		ClaimKinds:    map[string]string{"tests pass": "observed"},
		ClaimEvidence: map[string][]string{"tests pass": {diffID}},
	}); err == nil {
		t.Fatal("observed claim must reject evidence without classification")
	}
	if err := legacyAgent.validateCommittedEvidence(tools.CompactContextArgs{CheckpointKind: "committed", EvidenceRefs: []string{diffID}}); err == nil {
		t.Fatal("committed checkpoint must not upgrade over ID-existence-only proof")
	}
}

// TestValidateModelDrivenEvidenceRefsResolvesCheckpointPackID pins the
// previous-generation resolution half of the cross-generation evidence chain:
// after an apply rebuilds the runtime tracker from the surviving messages, an
// evidence ID that only survives inside the rendered evidence pack of a
// checkpoint still in the context must validate — the checkpoint message is
// the only place the archived item's ID remains visible. Invented IDs stay
// rejected.
func TestValidateModelDrivenEvidenceRefsResolvesCheckpointPackID(t *testing.T) {
	item := buildEvidenceItem(evidenceToolDiff, "Recent code diff", "needed", "tool", "diff content")
	checkpoint := buildCompactionCheckpointMessage(
		"## Current User Request\n- continue",
		nil, compactionSummaryModeModelDriven, []evidenceItem{item})
	a := &MainAgent{tools: tools.NewRegistry(), ctxMgr: ctxmgr.NewManager(10000, 1000)}
	a.ctxMgr.Append(message.Message{Role: message.RoleUser, Content: checkpoint, IsCompactionSummary: true})
	// The tracker is empty: the item's source messages were archived.
	id := evidenceItemID(item)
	if err := a.validateModelDrivenEvidenceRefs("evidence_refs", []string{id}); err != nil {
		t.Fatalf("checkpoint-pack evidence ID must validate after its source was archived: %v", err)
	}
	if err := a.validateObservedClaimEvidence(tools.CompactContextArgs{
		ClaimKinds:    map[string]string{"tests pass": "observed"},
		ClaimEvidence: map[string][]string{"tests pass": {id}},
	}); err != nil {
		t.Fatalf("observed claim over checkpoint-pack evidence must validate: %v", err)
	}
	if err := a.validateModelDrivenEvidenceRefs("evidence_refs", []string{"ev-000000000000"}); err == nil {
		t.Fatal("invented evidence ID must still be rejected")
	}
}

// TestE2EModelDrivenEvidenceIDSurvivesApplyIntoNextGeneration drives the full
// reported chain: a real durable apply archives the runtime evidence, rebuilds
// the tracker empty, and leaves the checkpoint's evidence pack as the only
// place its Evidence IDs remain visible. The next generation's arm validation
// must accept those IDs (resolution against the checkpoint in context) instead
// of starting a rejection chain.
func TestE2EModelDrivenEvidenceIDSurvivesApplyIntoNextGeneration(t *testing.T) {
	t.Parallel()
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.newTurn("")
	a.requestBatches.reserve(a.sessionEpoch, 0)

	// Round 1: real context whose tool diff becomes runtime evidence, then an
	// archival model-driven apply renders the evidence pack inside the
	// checkpoint message.
	callID := "call-diff-1"
	a.ctxMgr.Append(message.Message{Role: message.RoleUser, Content: "refactor the loader"})
	a.ctxMgr.Append(message.Message{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: callID, Name: tools.NameEdit, Args: mustJSONRaw(t, map[string]any{"path": "loader.go"})}}})
	a.ctxMgr.Append(message.Message{Role: message.RoleTool, ToolCallID: callID, ToolStatus: "success", Content: "applied", ToolDiff: "*** Update File: loader.go"})
	a.ctxMgr.Append(message.Message{Role: message.RoleUser, Content: "keep the public API stable"})
	a.ctxMgr.Append(message.Message{Role: message.RoleAssistant, Content: "checking"})

	// Rebuild the evidence candidate list the way the event loop does at the
	// barrier (captureModelDrivenBarrierSnapshot), then render a model-driven
	// checkpoint that carries the archival evidence pack.
	a.resetRuntimeEvidenceFromMessages(a.ctxMgr.Snapshot())
	evidenceItems := filterCompactionEvidenceForArchival(a.evidenceItemsForCompaction(a.ctxMgr.GetMaxTokens()))
	if len(evidenceItems) == 0 {
		t.Fatal("fixture must produce archival evidence items")
	}
	var packID string
	for _, item := range evidenceItems {
		packID = evidenceItemID(item)
	}
	snapshot := a.ctxMgr.Snapshot()
	req := &modelDrivenCheckpointRequest{Args: tools.CompactContextArgs{ActiveObjective: "refactor the loader", NextStep: "continue", EvidenceRefs: []string{packID}}}
	summary := a.buildModelDrivenCheckpointSummary(modelDrivenBarrierSnapshot{snapshot: snapshot}, snapshot, len(snapshot), req)
	content := buildCompactionCheckpointMessage(summary, nil, compactionSummaryModeModelDriven, evidenceItems)
	// The archival model-driven apply archives the whole head; after it only
	// the checkpoint survives, so the archived evidence cannot re-enter the
	// runtime tracker.
	draft := &compactionDraft{
		NewMessages:    []message.Message{{Role: message.RoleUser, Content: content, IsCompactionSummary: true, CompactionSummaryMode: compactionSummaryModeModelDriven}},
		HeadSplit:      len(snapshot),
		Index:          1,
		AbsHistoryPath: filepath.Join(a.sessionDir, "history-1.md"),
		SummaryMode:    compactionSummaryModeModelDriven,
		PlanID:         1,
		Target:         compactionTarget{sessionEpoch: a.sessionEpoch},
	}
	if err := a.applyCompactionDraft(draft); err != nil {
		t.Fatalf("apply round-1 checkpoint: %v", err)
	}

	// The apply rebuilt the runtime evidence from the surviving messages; the
	// checkpoint itself is skipped, so the pack's item is no longer a runtime
	// candidate.
	for _, item := range a.evidence.snapshot() {
		if evidenceItemID(item) == packID {
			t.Fatal("archived evidence must not survive as a runtime candidate after the apply")
		}
	}

	// Next generation's arm validation references the previous pack's ID. It
	// must resolve against the checkpoint still in the context (the reported
	// chain rejected it as unknown).
	live := a.ctxMgr.Snapshot()
	if len(live) == 0 || !strings.Contains(live[0].Content, "Evidence ID: "+packID) {
		t.Fatalf("checkpoint must render the evidence pack with the ID:\n%s", live[0].Content)
	}
	if err := a.validateModelDrivenEvidenceRefs("evidence_refs", []string{packID}); err != nil {
		t.Fatalf("previous-generation evidence pack ID rejected after apply: %v", err)
	}
}
