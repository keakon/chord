package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func testCompactContextCall() (string, string) {
	return "cc-1", `{
		"active_objective": "land the model-driven context reset",
		"completed": ["tool written", "runtime barrier wired"],
		"decisions": ["keep archival profile", "MainAgent-only"],
		"open_issues": ["gateway contract test"],
		"next_step": "run the agent tests",
		"state_files": ["internal/agent/compaction_model_driven.go"]
	}`
}

func testToolCall(id, name string) message.ToolCall {
	return message.ToolCall{ID: id, Name: name, Args: json.RawMessage(`{}`)}
}

func TestCompactContextSoleToolCallDetection(t *testing.T) {
	ccID, _ := testCompactContextCall()
	msgs := []message.Message{
		{Role: message.RoleUser, Content: "do work"},
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{testToolCall("sibling-1", "read")}},
		{Role: message.RoleTool, ToolCallID: "sibling-1", Content: "ok"},
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{testToolCall(ccID, tools.NameCompactContext)}},
	}
	if !compactContextSoleToolCall(msgs, ccID) {
		t.Fatal("sole compact_context call should pass the sole-call check")
	}

	multi := []message.Message{
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{
			testToolCall(ccID, tools.NameCompactContext),
			testToolCall("other", "read"),
		}},
	}
	if compactContextSoleToolCall(multi, ccID) {
		t.Fatal("compact_context with a sibling tool call must fail the sole-call check")
	}
	if compactContextSoleToolCall([]message.Message{{Role: message.RoleUser, Content: "x"}}, ccID) {
		t.Fatal("missing declaring message must fail the sole-call check")
	}
}

func TestTryArmModelDrivenCheckpointAcceptsValidArgs(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.newTurn()
	ccID, args := testCompactContextCall()
	a.ctxMgr.Append(message.Message{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{testToolCall(ccID, tools.NameCompactContext)}})
	result, err := a.tryArmModelDrivenCheckpoint(ccID, args)
	if err != nil {
		t.Fatalf("tryArmModelDrivenCheckpoint: %v", err)
	}
	if !strings.Contains(result, "accepted") {
		t.Fatalf("result = %q, want accepted wording", result)
	}
	if a.pendingModelDriven == nil {
		t.Fatal("pendingModelDriven should be armed")
	}
	if a.pendingModelDriven.Args.ActiveObjective != "land the model-driven context reset" {
		t.Fatalf("armed objective = %q", a.pendingModelDriven.Args.ActiveObjective)
	}
	if len(a.pendingModelDriven.Args.StateFiles) != 1 {
		t.Fatalf("state_files = %#v", a.pendingModelDriven.Args.StateFiles)
	}
}

func TestTryArmModelDrivenCheckpointRejectsBadArgs(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.newTurn()
	if _, err := a.tryArmModelDrivenCheckpoint("cc-1", `{"state_files":["/etc/passwd"]}`); err == nil {
		t.Fatal("expected rejection for absolute state path")
	}
	if a.pendingModelDriven != nil {
		t.Fatal("pendingModelDriven must not be armed on rejection")
	}
	if _, err := a.tryArmModelDrivenCheckpoint("cc-1", `{}`); err == nil {
		t.Fatal("expected rejection for missing required fields")
	}

	// A compaction already owning the slot does not reject the request: the
	// model's explicit checkpoint may override an in-flight automatic
	// compaction, and the model-driven barrier discards the running
	// compaction before starting the model's own worker. The request is
	// armed normally.
	a.ctxMgr.Append(message.Message{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{testToolCall("cc-1", tools.NameCompactContext)}})
	a.beginCompactionState(9, compactionTarget{turnID: 1, turnEpoch: 1, sessionEpoch: a.sessionEpoch}, compactionTriggerUsageDriven, continuationPlan{kind: compactionResumeAutoContinue, turnID: 1}, 0, nil)
	defer a.resetCompactionState()
	if _, err := a.tryArmModelDrivenCheckpoint("cc-1", `{"active_objective":"a","next_step":"b"}`); err != nil {
		t.Fatalf("model checkpoint must be accepted while an automatic compaction runs: %v", err)
	}
	if a.pendingModelDriven == nil {
		t.Fatal("pendingModelDriven must be armed while a compaction is running (override path)")
	}
}

func TestMaybeStartModelDrivenBarrierSkipsWithoutPending(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.newTurn()
	if a.maybeStartModelDrivenBarrier() {
		t.Fatal("barrier must not start with no pending request")
	}
}

func TestMaybeStartModelDrivenBarrierStartsWorker(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.newTurn()
	for _, msg := range []message.Message{
		{Role: message.RoleUser, Content: "first request"},
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{testToolCall("cc-1", tools.NameCompactContext)}},
	} {
		a.ctxMgr.Append(msg)
	}
	ccID, args := testCompactContextCall()
	if _, err := a.tryArmModelDrivenCheckpoint(ccID, args); err != nil {
		t.Fatalf("tryArm: %v", err)
	}
	if !a.maybeStartModelDrivenBarrier() {
		t.Fatal("barrier should start for a pending model-driven request")
	}
	if a.pendingModelDriven != nil {
		t.Fatal("pending request must be consumed by the barrier")
	}
	if !a.IsCompactionRunning() {
		t.Fatal("compaction should be running after the barrier")
	}
	if a.compactionState.trigger != compactionTriggerModelDriven {
		t.Fatalf("trigger = %q, want model_driven", a.compactionState.trigger)
	}
	if a.compactionState.continuation.kind != compactionResumeModelDriven {
		t.Fatalf("continuation kind = %q, want model_driven", a.compactionState.continuation.kind)
	}
	if a.compactionState.headSplit <= 0 {
		t.Fatalf("head split = %d, want > 0", a.compactionState.headSplit)
	}
	// The worker runs without an event loop in this test, so it never reaches
	// a terminal state here; teardown joins it via signalStopping + outputWg.
}

func TestMaybeStartModelDrivenBarrierOverridesRunningUsageCompaction(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.newTurn()
	for _, msg := range []message.Message{
		{Role: message.RoleUser, Content: "first request"},
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{testToolCall("cc-1", tools.NameCompactContext)}},
	} {
		a.ctxMgr.Append(msg)
	}
	// An automatic usage-driven compaction owns the slot (a threshold crossing
	// started its async worker). The model's checkpoint request is accepted
	// and the barrier discards the running compaction before the model-driven
	// worker starts: the model chose this boundary on purpose, so it wins over
	// the runtime's background summary.
	cancelCalled := false
	a.beginCompactionState(7, compactionTarget{turnID: 1, turnEpoch: 1, sessionEpoch: a.sessionEpoch}, compactionTriggerUsageDriven, continuationPlan{kind: compactionResumeAutoContinue, turnID: 1}, 5, func() { cancelCalled = true })
	ccID, args := testCompactContextCall()
	if _, err := a.tryArmModelDrivenCheckpoint(ccID, args); err != nil {
		t.Fatalf("tryArm must accept the override request: %v", err)
	}
	if a.pendingModelDriven == nil {
		t.Fatal("pending request must be armed")
	}
	if !a.maybeStartModelDrivenBarrier() {
		t.Fatal("barrier should start the model-driven worker")
	}
	if !cancelCalled {
		t.Fatal("the running usage compaction must be cancelled for the model override")
	}
	if a.compactionState.trigger != compactionTriggerModelDriven {
		t.Fatalf("trigger = %q, want model_driven (model wins over usage-driven)", a.compactionState.trigger)
	}
	if a.compactionState.planID == 7 {
		t.Fatal("compaction state must belong to the new model-driven plan")
	}
}

func TestMaybeStartModelDrivenBarrierDiscardsReadyUsageDraft(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.newTurn()
	for _, msg := range []message.Message{
		{Role: message.RoleUser, Content: "first request"},
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{testToolCall("cc-1", tools.NameCompactContext)}},
	} {
		a.ctxMgr.Append(msg)
	}
	// A usage-driven draft is ready and waiting for the continuation barrier
	// (Case C). The model's checkpoint arrives in the tool batch before that
	// barrier resolves: the barrier must discard the ready draft (removing its
	// orphan history files) and start the model-driven worker instead.
	readyHistory := filepath.Join(t.TempDir(), "history-1.md")
	if err := os.WriteFile(readyHistory, []byte("# archived"), 0o644); err != nil {
		t.Fatal(err)
	}
	a.beginCompactionState(7, compactionTarget{turnID: 1, turnEpoch: 1, sessionEpoch: a.sessionEpoch}, compactionTriggerUsageDriven, continuationPlan{kind: compactionResumeAutoContinue, turnID: 1}, 5, nil)
	a.compactionState.readyDraft = &compactionDraft{PlanID: 7, AbsHistoryPath: readyHistory}
	ccID, args := testCompactContextCall()
	if _, err := a.tryArmModelDrivenCheckpoint(ccID, args); err != nil {
		t.Fatalf("tryArm must accept the override request: %v", err)
	}
	if !a.maybeStartModelDrivenBarrier() {
		t.Fatal("barrier should start the model-driven worker")
	}
	if a.compactionState.readyDraft != nil {
		t.Fatal("ready draft must be discarded on the model override")
	}
	if _, err := os.Stat(readyHistory); !os.IsNotExist(err) {
		t.Fatalf("orphan ready history must be removed, stat err = %v", err)
	}
	if a.compactionState.trigger != compactionTriggerModelDriven {
		t.Fatalf("trigger = %q, want model_driven", a.compactionState.trigger)
	}
}

func TestModelDrivenLowGainPreflightRejectsTinyContext(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.newTurn()
	for _, msg := range []message.Message{
		{Role: message.RoleUser, Content: "short request"},
		{Role: message.RoleAssistant, Content: "short reply"},
	} {
		a.ctxMgr.Append(msg)
	}
	req := &modelDrivenCheckpointRequest{
		ToolCallID: "cc-1",
		Args:       tools.CompactContextArgs{ActiveObjective: "x", NextStep: "y"},
	}
	snapshot := a.ctxMgr.Snapshot()
	bundle := modelDrivenBarrierSnapshot{
		snapshot:     snapshot,
		maxTokens:    a.ctxMgr.GetMaxTokens(),
		scratchAgent: a.compactionReductionScratch(),
	}
	reason, skip, _ := a.modelDrivenLowGainPreflight(bundle, len(snapshot), snapshot, snapshot, req)
	if !skip {
		t.Fatal("tiny context must be skipped by the low-gain gate")
	}
	if !strings.Contains(reason, "low-gain") {
		t.Fatalf("reason = %q, want low-gain mention", reason)
	}
}

func TestModelDrivenSkipSettlesWithoutClearingUsageState(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.newTurn()
	a.autoCompactRequested.Store(true)
	a.ctxMgr.RestoreStats(message.TokenUsage{InputTokens: 1000})
	a.modelDrivenSkipNotice = ""
	a.settleModelDrivenSkip(&compactionDraft{Skip: true, InfoMessage: "projected savings too small"})
	if !a.autoCompactRequested.Load() {
		t.Fatal("model-driven skip must NOT clear autoCompactRequested")
	}
	if got := a.ctxMgr.GetStats().InputTokens; got != 1000 {
		t.Fatalf("model-driven skip must NOT clear LastTokenUsage, got %d", got)
	}
	if a.modelDrivenSkipNotice == "" {
		t.Fatal("skip notice should be stored for the continuation")
	}
	a.appendModelDrivenContinuationNotice()
	if a.modelDrivenSkipNotice != "" {
		t.Fatal("skip notice should be consumed after surfacing")
	}
	if a.pendingModelDrivenNotice == "" {
		t.Fatal("continuation notice should be queued as a transient overlay")
	}
}

func TestModelDrivenSettleRecordsLifecycleAndTerminalTrigger(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.newTurn()
	a.beginCompactionState(7, compactionTarget{turnID: 1, turnEpoch: 1, sessionEpoch: a.sessionEpoch}, compactionTriggerModelDriven, continuationPlan{kind: compactionResumeModelDriven, turnID: 1}, 4, nil)

	preflight := &modelDrivenPreflightStats{CurrentTokens: 8000, ProjectedTokens: 5000, SavedTokens: 3000, SavedRatioPct: 37, ContinuationTokens: 90}
	a.settleModelDrivenSkip(&compactionDraft{Skip: true, InfoMessage: "Context checkpoint skipped: projected savings too small", ModelDrivenPreflight: preflight})

	// Lifecycle analytics: one skipped/model_driven event with the preflight
	// fields (P2-2 pilot metrics).
	stats := a.usageTracker.SessionStats()
	if stats.CompactionLifecycle["skipped/model_driven"] != 1 {
		t.Fatalf("skipped/model_driven count = %d, want 1; got %+v", stats.CompactionLifecycle["skipped/model_driven"], stats.CompactionLifecycle)
	}

	// Terminal event trigger comes from the active compaction state (P1-8).
	evt := a.compactionStatusEvent(CompactionStatusFailed, "boom")
	if evt.Trigger != string(compactionTriggerModelDriven) || evt.Reason != "boom" {
		t.Fatalf("terminal event = %+v, want trigger model_driven + reason boom", evt)
	}

	// Failure and cancel settlements also settle exactly once with the trigger.
	a.settleModelDrivenFailure(errCompactionWatchdog)
	stats = a.usageTracker.SessionStats()
	if stats.CompactionLifecycle["failed/model_driven"] != 1 {
		t.Fatalf("failed/model_driven count = %d, want 1", stats.CompactionLifecycle["failed/model_driven"])
	}
	a.settleModelDrivenCancelled("cancelled by the user")
	stats = a.usageTracker.SessionStats()
	if stats.CompactionLifecycle["cancelled/model_driven"] != 1 {
		t.Fatalf("cancelled/model_driven count = %d, want 1", stats.CompactionLifecycle["cancelled/model_driven"])
	}
}

func TestModelDrivenSkipKeepsUsageDrivenSafetyNetArmed(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.gitStatusInjected.Store(true)
	a.ctxMgr.SetMaxTokens(1024)
	a.newTurn()
	turnID := a.turn.ID
	a.autoCompactRequested.Store(true)
	target := compactionTarget{turnID: turnID, turnEpoch: a.turn.Epoch, sessionEpoch: a.sessionEpoch}
	a.startCompactionState(3, target, compactionTriggerModelDriven, continuationPlan{kind: compactionResumeModelDriven, turnID: turnID, turnEpoch: a.turn.Epoch, agentErrSourceID: "main"})
	a.modelDrivenSkipNotice = ""
	draft := &compactionDraft{
		Skip:        true,
		InfoMessage: "Context checkpoint skipped: projected savings too small",
		PlanID:      3,
		Target:      target,
	}
	a.handleCompactionReady(Event{Type: EventCompactionReady, TurnID: turnID, Payload: draft})

	// The skip settles without clearing the usage-driven request...
	if !a.autoCompactRequested.Load() {
		t.Fatal("model-driven skip must keep autoCompactRequested armed")
	}
	if a.pendingModelDrivenNotice == "" {
		t.Fatal("skip resume must queue a transient continuation notice")
	}
	// ...and the resume re-enters beginMainLLMAfterPreparation, so the
	// usage-driven gate runs on the old context and starts a real
	// usage-driven compaction instead of spawning the next request directly.
	if !a.IsCompactionRunning() {
		t.Fatal("usage-driven compaction should have started after the skip resume")
	}
	if a.compactionState.trigger != compactionTriggerUsageDriven {
		t.Fatalf("trigger after skip resume = %q, want usage_driven", a.compactionState.trigger)
	}
}

func TestModelDrivenApplyFailureSurfacesRealReason(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.gitStatusInjected.Store(true)
	a.ctxMgr.SetMaxTokens(1024)
	a.ctxMgr.Append(message.Message{Role: "user", Content: "head message being compacted"})
	a.newTurn()
	turnID := a.turn.ID
	target := compactionTarget{turnID: turnID, turnEpoch: a.turn.Epoch, sessionEpoch: a.sessionEpoch}
	a.startCompactionState(5, target, compactionTriggerModelDriven, continuationPlan{kind: compactionResumeModelDriven, turnID: turnID, turnEpoch: a.turn.Epoch, agentErrSourceID: "main"})
	a.modelDrivenSkipNotice = ""

	// A draft whose apply must fail the provenance gate: the source refs claim
	// the head is a tool message, but it is a user message.
	draft := &compactionDraft{
		NewMessages:        []message.Message{{Role: "user", Content: "[Context Summary]\ncheckpoint"}},
		Index:              1,
		HeadSplit:          1,
		SourceRefs:         []checkpointSourceRef{{LegacyOrdinal: 0, Role: "tool"}},
		SourceFingerprint:  "forged",
		AbsHistoryPath:     filepath.Join(projectRoot, "history-1.md"),
		AbsHistoryMetaPath: filepath.Join(projectRoot, "history-1.md.meta"),
		SummaryMode:        compactionSummaryModeModelDriven,
		ModelRef:           "model_declared",
		PlanID:             5,
		Target:             target,
	}
	a.handleCompactionReady(Event{Type: EventCompactionReady, TurnID: turnID, Payload: draft})

	// The failure reason is consumed by the resume path into the transient
	// continuation notice; it must not be the low-gain default.
	notice := a.pendingModelDrivenNotice
	if a.modelDrivenSkipNotice != "" {
		notice = a.modelDrivenSkipNotice
	}
	if notice == "" {
		t.Fatal("apply failure must surface a model-driven reason")
	}
	if strings.Contains(notice, "projected savings") {
		t.Fatalf("apply failure misreported as low-gain skip: %q", notice)
	}
	if !strings.Contains(notice, "provenance") {
		t.Fatalf("apply failure reason = %q, want provenance failure wording", notice)
	}
}

func TestModelDrivenCheckpointWrapperNamesItsMode(t *testing.T) {
	content := buildCompactionCheckpointMessage("## Current User Request\n- x", nil, compactionSummaryModeModelDriven, nil)
	if !strings.Contains(content, "model-driven context checkpoint") {
		t.Fatalf("checkpoint wrapper missing model-driven copy:\n%s", content)
	}
	if !strings.Contains(content, "no summarization model was called") {
		t.Fatalf("checkpoint wrapper must state no summarization call:\n%s", content)
	}
}

func TestModelDrivenAppliedDraftCarriesPreflightStats(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.newTurn()

	snapshot := []message.Message{
		{Role: message.RoleUser, Content: "original user request"},
		{Role: message.RoleAssistant, Content: strings.Repeat("analysis ", 4000)},
		{Role: message.RoleUser, Content: "follow up"},
		{Role: message.RoleAssistant, Content: strings.Repeat("more analysis ", 4000)},
		{Role: message.RoleUser, Content: "another follow up"},
		{Role: message.RoleAssistant, Content: strings.Repeat("final analysis ", 4000)},
	}
	bundle := modelDrivenBarrierSnapshot{
		snapshot:                    snapshot,
		maxTokens:                   a.ctxMgr.GetMaxTokens(),
		sessionDir:                  a.sessionDir,
		scratchAgent:                a.compactionReductionScratch(),
		fixedRequestTokens:          1000,
		postResetFixedRequestTokens: 4000,
	}
	req := &modelDrivenCheckpointRequest{
		ToolCallID: "cc-1",
		Args:       tools.CompactContextArgs{ActiveObjective: "keep going", NextStep: "continue"},
	}

	draft, err := a.produceModelDrivenDraftAsync(t.Context(), bundle, 1, compactionTarget{}, 4, req)
	if err != nil {
		t.Fatalf("produceModelDrivenDraftAsync: %v", err)
	}
	if draft.ModelDrivenPreflight == nil {
		t.Fatal("applied draft must carry model-driven preflight stats")
	}
	p := draft.ModelDrivenPreflight
	if p.SavedTokens == 0 || p.CurrentTokens == 0 || p.ProjectedTokens == 0 {
		t.Fatalf("applied draft preflight savings must be populated, got %+v", p)
	}
	if p.SavedRatioPct == 0 {
		// The projected side must stay smaller than the current side for the
		// draft to be produced at all; a zero ratio with non-zero savings on
		// a big context would mean the numbers were never computed.
		t.Fatalf("applied draft preflight saved ratio must be populated, got %+v", p)
	}
	if p.CurrentBytes == 0 || p.ProjectedBytes == 0 {
		t.Fatalf("applied draft preflight bytes must be populated, got %+v", p)
	}
	if p.CheckpointBytes == 0 || p.HistoryMapBytes == 0 || p.ContinuationTokens == 0 {
		t.Fatalf("applied draft content stats must be populated, applied=%+v", p)
	}
	if p.AnchorBytes < 0 {
		t.Fatalf("anchor bytes must not be negative, got %d", p.AnchorBytes)
	}
	if p.ProjectedTokens <= bundle.fixedRequestTokens {
		t.Fatalf("projected tokens %d must exceed the current fixed surface %d", p.ProjectedTokens, bundle.fixedRequestTokens)
	}
	projected := append([]message.Message{
		{Role: message.RoleUser, Content: draft.NewMessages[0].Content, IsCompactionSummary: true},
	}, snapshot[4:]...)
	recomputedProjected := estimateMessagesTokens(a.ctxMgr, projected) + bundle.postResetFixedRequestTokens + modelDrivenPostResetOverlayTokens
	// The preflight builds the checkpoint before history export and the
	// post-export draft re-builds it with the just-written archive in the
	// history map, so the projected surfaces differ by that map delta (~tens
	// of tokens). If the projected side silently used the current-side fixed
	// surface (fixedRequestTokens=1000) instead of the post-reset full
	// injection (postResetFixedRequestTokens=4000), the difference would be
	// ~3000 tokens and this assertion fails.
	if diff := p.ProjectedTokens - recomputedProjected; diff < -500 || diff > 500 {
		t.Fatalf("projected tokens %d diverge from the post-reset fixed surface accounting %d (diff=%d)", p.ProjectedTokens, recomputedProjected, diff)
	}
}

func TestModelDrivenCheckpointSummaryKeepsModelTextAndNeutralizesHeadings(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
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
	summary := a.buildModelDrivenCheckpointSummary(bundle, bundle.snapshot, nil, req)
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
	// P2-1: the deterministic checkpoint must cap the latest-request anchor
	// like the structured-fallback summary does; an overlong user message or
	// Done-rejected reason must not crowd out the rest of the checkpoint.
	longText := strings.Repeat("a", modelDrivenAnchorMaxRunes*3)
	got := modelDrivenCurrentUserRequestSection(fallbackAnchor{Kind: "user_request", Label: "Latest user request", Text: longText})
	if strings.Contains(got, strings.Repeat("a", modelDrivenAnchorMaxRunes*2)) {
		t.Fatalf("anchor was not truncated: %d runes retained", len(got))
	}
	if !strings.Contains(got, "...") {
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

func TestCancelCompactionForTurnCancellationDiscardsModelDrivenWorker(t *testing.T) {
	// P1-1: cancelling the requesting turn must stop an in-flight model-driven
	// checkpoint so a late ready draft can never rewrite history for abandoned
	// work. The worker context is parented on parentCtx, not the turn context,
	// so without explicit cancellation it would keep running past ESC.
	a := newTestMainAgent(t, t.TempDir())
	a.newTurn()
	turnID := a.turn.ID
	workerCtx, cancel := context.WithCancel(context.Background())
	a.beginCompactionState(
		5,
		compactionTarget{turnID: turnID, turnEpoch: 1, sessionEpoch: a.sessionEpoch},
		compactionTriggerModelDriven,
		continuationPlan{kind: compactionResumeModelDriven, turnID: turnID, turnEpoch: 1},
		2,
		cancel,
	)

	a.cancelCompactionForTurnCancellation(turnID)

	if workerCtx.Err() != context.Canceled {
		t.Fatalf("model-driven worker context was not cancelled: %v", workerCtx.Err())
	}
	if !a.compactionState.discard {
		t.Fatal("compaction state must be marked discard so a late ready draft cannot apply")
	}
	// The running worker settles on its own cancellation failure event (the
	// discard flag makes finishCompactionState drop the pending continuation);
	// simulate that event now to verify the state settles.
	a.handleCompactionFailed(Event{
		Type: EventCompactionFailed,
		Payload: &compactionFailure{
			planID: 5,
			target: compactionTarget{turnID: turnID, turnEpoch: 1, sessionEpoch: a.sessionEpoch},
			err:    context.Canceled,
		},
	})
	if a.IsCompactionRunning() {
		t.Fatal("compaction must no longer be running after the worker failure settles")
	}

	// Cancelling a different turn's checkpoint (e.g. a usage-driven compaction
	// from an earlier turn) must be a no-op.
	a2 := newTestMainAgent(t, t.TempDir())
	a2.newTurn()
	otherCtx, otherCancel := context.WithCancel(context.Background())
	a2.beginCompactionState(
		6,
		compactionTarget{turnID: turnID, turnEpoch: 1, sessionEpoch: a2.sessionEpoch},
		compactionTriggerModelDriven,
		continuationPlan{kind: compactionResumeModelDriven, turnID: turnID, turnEpoch: 1},
		2,
		otherCancel,
	)
	a2.cancelCompactionForTurnCancellation(999)
	if otherCtx.Err() != nil {
		t.Fatal("cancelling an unrelated turn must not cancel the compaction worker")
	}
	if a2.compactionState.discard {
		t.Fatal("cancelling an unrelated turn must not mark the compaction discard")
	}
}

func TestCancelCompactionForTurnCancellationSettlesParkedDraftWithLiveTrigger(t *testing.T) {
	// A model-driven draft already parked at the continuation barrier belongs
	// to the cancelled turn. Cancelling the turn must settle it while the
	// compaction state is still live — the terminal status event carries the
	// model_driven trigger and plan id only when it is built before the
	// reset — then clean its orphan history files and reset the state.
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.newTurn()
	turnID := a.turn.ID
	history := filepath.Join(projectRoot, "history-7.md")
	if err := os.WriteFile(history, []byte("parked archive body"), 0o644); err != nil {
		t.Fatalf("write parked history archive: %v", err)
	}
	a.beginCompactionState(
		7,
		compactionTarget{turnID: turnID, turnEpoch: 1, sessionEpoch: a.sessionEpoch},
		compactionTriggerModelDriven,
		continuationPlan{kind: compactionResumeModelDriven, turnID: turnID, turnEpoch: 1},
		2,
		nil,
	)
	// Park the ready draft at the barrier like the model-driven apply path in
	// handleCompactionReady does once the worker reaches its terminal event.
	a.compactionState.readyDraft = &compactionDraft{PlanID: 7, AbsHistoryPath: history}

	a.cancelCompactionForTurnCancellation(turnID)

	if a.IsCompactionRunning() {
		t.Fatal("compaction must no longer be running after the parked draft settles")
	}
	if _, err := os.Stat(history); !os.IsNotExist(err) {
		t.Fatalf("parked draft history archive must be cleaned up, stat err=%v", err)
	}
	if a.modelDrivenSkipNotice == "" {
		t.Fatal("cancelling the parked draft must surface the continuation notice")
	}
	// Drain events until the terminal status event arrives (newTurn and the
	// settle path may interleave unrelated bookkeeping events first).
	deadline := time.After(2 * time.Second)
	for {
		select {
		case evt := <-a.outputCh:
			status, ok := evt.(CompactionStatusEvent)
			if !ok {
				continue
			}
			if status.Status != CompactionStatusCancelled {
				t.Fatalf("status = %v, want %v", status.Status, CompactionStatusCancelled)
			}
			if status.Trigger != string(compactionTriggerModelDriven) {
				t.Fatalf("trigger = %q, want %q (built from the live compaction state before reset)", status.Trigger, compactionTriggerModelDriven)
			}
			if status.PlanID != "7" {
				t.Fatalf("plan_id = %q, want \"7\"", status.PlanID)
			}
			return
		case <-deadline:
			t.Fatal("no compaction status event was emitted for the cancelled parked draft")
		}
	}
}

func TestModelDrivenDraftCommittedArchiveSurvives(t *testing.T) {
	// The deferred archive cleanup only fires for failures: a successful
	// draft must keep its history file and meta so the apply step can use
	// them.
	dir := t.TempDir()
	a := newTestMainAgent(t, dir)
	a.newTurn()

	snapshot := []message.Message{
		{Role: message.RoleUser, Content: "original user request"},
		{Role: message.RoleAssistant, Content: strings.Repeat("analysis ", 4000)},
		{Role: message.RoleUser, Content: "follow up"},
		{Role: message.RoleAssistant, Content: strings.Repeat("more analysis ", 4000)},
		{Role: message.RoleUser, Content: "another follow up"},
		{Role: message.RoleAssistant, Content: strings.Repeat("final analysis ", 4000)},
	}
	bundle := modelDrivenBarrierSnapshot{
		snapshot:                    snapshot,
		maxTokens:                   a.ctxMgr.GetMaxTokens(),
		sessionDir:                  a.sessionDir,
		scratchAgent:                a.compactionReductionScratch(),
		fixedRequestTokens:          1000,
		postResetFixedRequestTokens: 4000,
	}
	req := &modelDrivenCheckpointRequest{Args: tools.CompactContextArgs{ActiveObjective: "keep going", NextStep: "continue"}}

	draft, err := a.produceModelDrivenDraftAsync(context.Background(), bundle, 1, compactionTarget{}, 4, req)
	if err != nil {
		t.Fatalf("produceModelDrivenDraftAsync: %v", err)
	}
	if draft == nil || draft.AbsHistoryPath == "" || draft.AbsHistoryMetaPath == "" {
		t.Fatalf("draft = %+v, want committed history/meta paths", draft)
	}
	if _, statErr := os.Stat(draft.AbsHistoryPath); statErr != nil {
		t.Fatalf("history file missing after a successful draft: %v", statErr)
	}
	if _, statErr := os.Stat(draft.AbsHistoryMetaPath); statErr != nil {
		t.Fatalf("meta file missing after a successful draft: %v", statErr)
	}
}

func TestModelDrivenDraftPreExportCancellationLeavesNoFiles(t *testing.T) {
	// Cancelling before the archive is written must return the cancellation
	// with nothing on disk: the worker's first ctx check fires before export,
	// so no history/meta files are ever created.
	dir := t.TempDir()
	a := newTestMainAgent(t, dir)
	a.newTurn()

	snapshot := []message.Message{
		{Role: message.RoleUser, Content: "original user request"},
		{Role: message.RoleAssistant, Content: strings.Repeat("analysis ", 4000)},
		{Role: message.RoleUser, Content: "follow up"},
	}
	bundle := modelDrivenBarrierSnapshot{
		snapshot:                    snapshot,
		maxTokens:                   a.ctxMgr.GetMaxTokens(),
		sessionDir:                  a.sessionDir,
		scratchAgent:                a.compactionReductionScratch(),
		fixedRequestTokens:          1000,
		postResetFixedRequestTokens: 4000,
	}
	req := &modelDrivenCheckpointRequest{Args: tools.CompactContextArgs{ActiveObjective: "keep going", NextStep: "continue"}}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	draft, err := a.produceModelDrivenDraftAsync(ctx, bundle, 1, compactionTarget{}, 4, req)
	if err == nil {
		t.Fatalf("cancelled worker must return an error, got draft %+v", draft)
	}
	entries, _ := os.ReadDir(a.sessionDir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "history-") {
			t.Fatalf("history file left by a pre-export cancellation: %s", e.Name())
		}
	}
}

func TestModelDrivenIntervalVerdictRequiresThreeBatches(t *testing.T) {
	a := &MainAgent{}
	// No previous apply: no interval gate.
	if _, _, skip := a.modelDrivenIntervalCooldownVerdict(modelDrivenBarrierSnapshot{currentRequestBatch: 1}); skip {
		t.Fatal("no previous apply must not interval-skip")
	}
	// Same batch as the last apply: interval skip.
	bundle := modelDrivenBarrierSnapshot{currentRequestBatch: 3, lastModelDrivenApplyBatch: 3}
	reason, skipReason, skip := a.modelDrivenIntervalCooldownVerdict(bundle)
	if !skip || skipReason != "interval" || !strings.Contains(reason, "interval") {
		t.Fatalf("same batch must interval-skip, reason=%q skipReason=%q skip=%v", reason, skipReason, skip)
	}
	// 2 batches after apply: still skipping.
	if _, _, skip := a.modelDrivenIntervalCooldownVerdict(modelDrivenBarrierSnapshot{currentRequestBatch: 5, lastModelDrivenApplyBatch: 3}); !skip {
		t.Fatal("2 batches after the last apply must interval-skip")
	}
	// 3 batches after apply: allowed.
	if _, _, skip := a.modelDrivenIntervalCooldownVerdict(modelDrivenBarrierSnapshot{currentRequestBatch: 6, lastModelDrivenApplyBatch: 3}); skip {
		t.Fatal("3 batches after the last apply must pass the interval")
	}
}

func TestModelDrivenIntervalVerdictNoUnderflowAfterRestore(t *testing.T) {
	a := &MainAgent{}
	// Restored session: the in-memory batch counter restarts at 0 while the
	// persisted last-apply batch comes from the pre-crash history. Without the
	// current > last guard, 0-5 would underflow to a huge number and the
	// interval would be misread as satisfied, allowing an immediate re-reset.
	reason, _, skip := a.modelDrivenIntervalCooldownVerdict(modelDrivenBarrierSnapshot{currentRequestBatch: 0, lastModelDrivenApplyBatch: 5})
	if !skip {
		t.Fatal("restored session must not treat the interval as satisfied (uint64 underflow guard)")
	}
	if !strings.Contains(reason, "interval") {
		t.Fatalf("restored-session skip reason = %q, want interval wording", reason)
	}
}

func TestModelDrivenSkipCooldownShortCircuitsLowGain(t *testing.T) {
	a := &MainAgent{}
	// Previous low-gain skip at batch 10; retry one batch later.
	bundle := modelDrivenBarrierSnapshot{currentRequestBatch: 11, lastModelDrivenSkipReason: "low_gain", lastModelDrivenSkipBatch: 10}
	reason, skipReason, skip := a.modelDrivenIntervalCooldownVerdict(bundle)
	if !skip {
		t.Fatal("same-reason low-gain retry within the cooldown must short-circuit")
	}
	if skipReason != "low_gain" {
		t.Fatalf("cooldown bound reason = %q, want low_gain", skipReason)
	}
	if !strings.Contains(reason, "cooling down") {
		t.Fatalf("cooldown reason = %q, want cooling-down wording", reason)
	}
	// Two batches later the cooldown expires and the interval gate decides.
	if _, _, skip := a.modelDrivenIntervalCooldownVerdict(modelDrivenBarrierSnapshot{currentRequestBatch: 12, lastModelDrivenSkipReason: "low_gain", lastModelDrivenSkipBatch: 10}); skip {
		t.Fatal("low-gain cooldown must expire after 2 batches")
	}
}

func TestModelDrivenIntervalRejectionNotCooldownBlocked(t *testing.T) {
	a := &MainAgent{}
	// An interval skip was recorded at batch 10 (last apply at 9). The model
	// retries at batch 11: the interval still has not elapsed (11-9=2 < 3), so
	// the retry is interval-skipped again — gated by the deterministic
	// interval, never cooled down by the previous interval record.
	bundle := modelDrivenBarrierSnapshot{currentRequestBatch: 11, lastModelDrivenApplyBatch: 9, lastModelDrivenSkipReason: "interval", lastModelDrivenSkipBatch: 10}
	_, skipReason, skip := a.modelDrivenIntervalCooldownVerdict(bundle)
	if !skip {
		t.Fatal("retry while the interval has not elapsed must skip")
	}
	if skipReason != "interval" {
		t.Fatalf("interval-gated retry must keep the interval reason, got %q (must not be cooldown-blocked)", skipReason)
	}
	// Once the interval elapses (3 batches since the apply), a retry proceeds
	// to preflight: it is free of any cooldown.
	if _, _, skip := a.modelDrivenIntervalCooldownVerdict(modelDrivenBarrierSnapshot{currentRequestBatch: 12, lastModelDrivenApplyBatch: 9, lastModelDrivenSkipReason: "interval", lastModelDrivenSkipBatch: 10}); skip {
		t.Fatal("interval-satisfied retry must not be blocked by the interval skip record")
	}
}

func TestModelDrivenLowGainCheckSubtractsCacheRebuildCost(t *testing.T) {
	// Raw savings pass both gates...
	if reason, skip := modelDrivenLowGainCheck(10000, 5000, 5000, 0); skip {
		t.Fatalf("raw savings above the gate must pass, got reason %q", reason)
	}
	// ...but subtracting the cache rebuild cost drops them below the absolute
	// floor (5000 - 4000 = 1000 < 2048).
	if reason, skip := modelDrivenLowGainCheck(10000, 5000, 5000, 4000); !skip {
		t.Fatal("savings net of cache rebuild cost below the gate must skip")
	} else if !strings.Contains(reason, "cache rebuild") {
		t.Fatalf("cache-gated skip reason must mention the rebuild cost, got %q", reason)
	}
	// The 10% relative gate applies to the net savings too.
	if reason, skip := modelDrivenLowGainCheck(100000, 95000, 5000, 4000); !skip {
		t.Fatalf("net savings below 10%% of the prepared surface must skip, got %q", reason)
	} else if !strings.Contains(reason, "cache rebuild") {
		t.Fatalf("relative-gate skip reason must mention the rebuild cost, got %q", reason)
	}
}

func TestModelDrivenApplyRecordsLastApplyBatch(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.newTurn()
	a.ctxMgr.Append(message.Message{Role: "user", Content: "one"})
	a.ctxMgr.Append(message.Message{Role: "assistant", Content: "two"})
	a.ctxMgr.Append(message.Message{Role: "user", Content: "three"})
	// Reserve one request batch so currentRequestBatch returns a known value.
	a.requestBatches.reserve(a.sessionEpoch, 0)

	draft := &compactionDraft{
		NewMessages:    []message.Message{{Role: "user", Content: "[Context Summary]", IsCompactionSummary: true}},
		HeadSplit:      2,
		Index:          1,
		AbsHistoryPath: filepath.Join(a.sessionDir, "history-1.md"),
		SummaryMode:    compactionSummaryModeModelDriven,
		PlanID:         1,
		Target:         compactionTarget{sessionEpoch: a.sessionEpoch},
	}
	if err := a.applyCompactionDraft(draft); err != nil {
		t.Fatalf("applyCompactionDraft: %v", err)
	}
	if a.lastModelDrivenApplyBatch != 1 {
		t.Fatalf("lastModelDrivenApplyBatch = %d, want 1", a.lastModelDrivenApplyBatch)
	}
	if a.lastModelDrivenSkipBatch != 0 || a.lastModelDrivenSkipReason != "" {
		t.Fatalf("apply must clear the skip-cooldown state, got batch=%d reason=%q", a.lastModelDrivenSkipBatch, a.lastModelDrivenSkipReason)
	}
}

func TestModelDrivenSettleRecordsSkipCooldownState(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.newTurn()
	a.settleModelDrivenSkip(&compactionDraft{Skip: true, InfoMessage: "Context checkpoint skipped: the minimum 3-request-batch interval since the last applied context checkpoint has not elapsed", ModelDrivenSkipReason: "interval", ModelDrivenSkipBatch: 7})
	if a.lastModelDrivenSkipReason != "interval" || a.lastModelDrivenSkipBatch != 7 {
		t.Fatalf("interval skip must record the cooldown state, got reason=%q batch=%d", a.lastModelDrivenSkipReason, a.lastModelDrivenSkipBatch)
	}
	// Structural skips carry no verdict and must not touch the cooldown state.
	a.settleModelDrivenSkip(&compactionDraft{Skip: true, InfoMessage: "Not enough history to compact."})
	if a.lastModelDrivenSkipReason != "interval" || a.lastModelDrivenSkipBatch != 7 {
		t.Fatalf("structural skip must not touch the cooldown state, got reason=%q batch=%d", a.lastModelDrivenSkipReason, a.lastModelDrivenSkipBatch)
	}
}

func TestModelDrivenSkipDraftCarriesVerdict(t *testing.T) {
	draft := modelDrivenSkipDraft(3, compactionTarget{}, "reason text", "low_gain", 9, nil)
	if !draft.Skip || draft.InfoMessage != "Context checkpoint skipped: reason text" {
		t.Fatalf("skip draft = %+v, want skip with reason text", draft)
	}
	if draft.ModelDrivenSkipReason != "low_gain" || draft.ModelDrivenSkipBatch != 9 {
		t.Fatalf("skip draft verdict = reason=%q batch=%d, want low_gain/9", draft.ModelDrivenSkipReason, draft.ModelDrivenSkipBatch)
	}
}

func TestEstimatePostResetFixedRequestTokensAtLeastCurrentFixed(t *testing.T) {
	// P1-2: the projected side of the low-gain gate must account for the
	// post-reset full-injection tool surface. forceFullMCPToolInjection drops
	// cache-friendly mounts after apply, so the projected fixed surface can
	// never be smaller than the current one.
	a := newTestMainAgent(t, t.TempDir())
	current := a.estimateFixedRequestTokens()
	postReset := a.estimatePostResetFixedRequestTokens()
	if postReset < current {
		t.Fatalf("post-reset fixed tokens %d < current fixed tokens %d; full-injection surface cannot shrink", postReset, current)
	}
	if postReset <= 0 {
		t.Fatal("post-reset fixed surface must be positive")
	}
}
