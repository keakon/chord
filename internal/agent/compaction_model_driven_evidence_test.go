package agent

import (
	"errors"
	"path/filepath"
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

	args := `{"active_objective":"a","next_step":"b","completed":["tests pass"],"claim_kinds":{"tests pass":"observed"}}`
	_, err := a.tryArmModelDrivenCheckpoint(ccID, args)
	if err == nil {
		t.Fatal("observed claim without claim_evidence must be rejected")
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
	newAgentWithCheckpoint := func(item evidenceItem, content string) (*MainAgent, string) {
		t.Helper()
		checkpoint := buildCompactionCheckpointMessage("## Current User Request\n- continue", nil, compactionSummaryModeModelDriven, []evidenceItem{item})
		a := &MainAgent{tools: tools.NewRegistry(), ctxMgr: ctxmgr.NewManager(10000, 1000)}
		a.ctxMgr.Append(message.Message{Role: message.RoleUser, Content: checkpoint, IsCompactionSummary: true})
		return a, checkpoint
	}

	// Archived negative records stay rejected for committed and for observed
	// claims alike.
	escalate := buildEvidenceItem(evidenceEscalate, "subagent intervention open", "needed", "tool", "requests intervention")
	a, checkpoint := newAgentWithCheckpoint(escalate, "")
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
	b, _ := newAgentWithCheckpoint(toolError, "")
	toolErrorID := evidenceItemID(toolError)
	if err := b.validateCommittedEvidence(tools.CompactContextArgs{CheckpointKind: "committed", EvidenceRefs: []string{toolErrorID}}); err == nil {
		t.Fatal("committed checkpoint over an archived tool_error record must be rejected")
	}

	// A positive archived record with a rendered kind still supports the
	// committed upgrade: that is the primary cross-generation use case.
	diff := buildEvidenceItem(evidenceToolDiff, "Recent code diff", "needed", "tool", "diff content")
	c, _ := newAgentWithCheckpoint(diff, "")
	diffID := evidenceItemID(diff)
	if err := c.validateCommittedEvidence(tools.CompactContextArgs{CheckpointKind: "committed", EvidenceRefs: []string{diffID}}); err != nil {
		t.Fatalf("committed checkpoint over an archived positive record must validate: %v", err)
	}

	_, diffCheckpoint := newAgentWithCheckpoint(diff, "")
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
