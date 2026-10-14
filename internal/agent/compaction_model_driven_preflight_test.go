package agent

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func TestModelDrivenLowGainPreflightRejectsTinyContext(t *testing.T) {
	t.Parallel()
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.newTurn("")
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
		snapshot:              snapshot,
		maxTokens:             a.ctxMgr.GetMaxTokens(),
		prepareReducedRequest: a.compactionReductionScratch().prepareMessagesForLLM,
	}
	reason, skip, _ := a.modelDrivenLowGainPreflight(bundle, len(snapshot), snapshot, a.newModelDrivenCheckpointBuilder(t.Context(), bundle, snapshot, len(snapshot), req))
	if !skip {
		t.Fatal("tiny context must be skipped by the low-gain gate")
	}
	if !strings.Contains(reason, "low-gain") {
		t.Fatalf("reason = %q, want low-gain mention", reason)
	}
}

func TestModelDrivenAppliedDraftCarriesPreflightStats(t *testing.T) {
	t.Parallel()
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.newTurn("")

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
		prepareReducedRequest:       a.compactionReductionScratch().prepareMessagesForLLM,
		fixedRequestTokens:          1000,
		postResetFixedRequestTokens: 4000,
		archiveMeta:                 a.captureCompactionArchiveMeta(),
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

func TestModelDrivenIntervalVerdictStaleAnchorCountsAsSatisfied(t *testing.T) {
	a := &MainAgent{}
	// Restored session whose batch numbering restarted below the persisted
	// last-apply anchor: the anchor is stale. It must neither underflow into
	// "satisfied by a huge gap" nor throttle every model-driven request until
	// the counter catches up; the interval simply counts as satisfied.
	if _, _, skip := a.modelDrivenIntervalCooldownVerdict(modelDrivenBarrierSnapshot{currentRequestBatch: 2, lastModelDrivenApplyBatch: 5}); skip {
		t.Fatal("a stale (current < last) apply anchor must not block model-driven requests")
	}
	// current == last is a genuine zero-batch gap and stays throttled.
	reason, _, skip := a.modelDrivenIntervalCooldownVerdict(modelDrivenBarrierSnapshot{currentRequestBatch: 5, lastModelDrivenApplyBatch: 5})
	if !skip || !strings.Contains(reason, "interval") {
		t.Fatalf("same-batch retry must interval-skip, got skip=%v reason=%q", skip, reason)
	}
	// currentRequestBatch 0 (no batch reserved yet) with a persisted anchor is
	// the classic restore shape; it is stale as well.
	if _, _, skip := a.modelDrivenIntervalCooldownVerdict(modelDrivenBarrierSnapshot{currentRequestBatch: 0, lastModelDrivenApplyBatch: 5}); skip {
		t.Fatal("batch 0 against a persisted anchor must not throttle")
	}
}

func TestModelDrivenPreflightReusesLastPreparedSurface(t *testing.T) {
	t.Parallel()
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.newTurn("")
	snapshot := []message.Message{
		{Role: message.RoleUser, Content: "original user request"},
		{Role: message.RoleAssistant, Content: strings.Repeat("analysis ", 4000)},
	}
	// This turn actually sent the above request, so the remembered reduced
	// surface is the provider-truth baseline.
	a.rememberPreparedLLMRequest(a.currentTurnID(), snapshot, snapshot, nil, nil, countToolResults(snapshot), a.contextReductionPolicy())
	snapshot = append(snapshot,
		message.Message{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{testToolCall("cc-1", tools.NameCompactContext)}},
		message.Message{Role: message.RoleTool, ToolCallID: "cc-1", Content: requestAcceptedToolResult},
	)
	req := &modelDrivenCheckpointRequest{
		ToolCallID: "cc-1",
		Args:       tools.CompactContextArgs{ActiveObjective: "keep going", NextStep: "continue"},
	}
	bundle := a.captureModelDrivenBarrierSnapshot(snapshot)
	reason, skip, preflight := a.modelDrivenLowGainPreflight(bundle, len(snapshot), snapshot, a.newModelDrivenCheckpointBuilder(t.Context(), bundle, snapshot, len(snapshot), req))
	if skip {
		t.Fatalf("gate must pass on the reused baseline, got skip reason %q", reason)
	}
	if preflight.CurrentSource != "last_prepared" {
		t.Fatalf("current source = %q, want last_prepared", preflight.CurrentSource)
	}
}

func TestModelDrivenPreflightFallsBackToScratchWhenHeadChanged(t *testing.T) {
	t.Parallel()
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.newTurn("")
	snapshot := []message.Message{
		{Role: message.RoleUser, Content: "original user request"},
		{Role: message.RoleAssistant, Content: strings.Repeat("analysis ", 4000)},
	}
	a.rememberPreparedLLMRequest(a.currentTurnID(), snapshot, snapshot, nil, nil, countToolResults(snapshot), a.contextReductionPolicy())
	// A mid-head change (the user edited the original request) invalidates the
	// remembered prefix: the preflight must fall back to the scratch
	// reduction instead of reusing a stale baseline.
	snapshot[0] = message.Message{Role: message.RoleUser, Content: "edited user request"}
	snapshot = append(snapshot,
		message.Message{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{testToolCall("cc-1", tools.NameCompactContext)}},
		message.Message{Role: message.RoleTool, ToolCallID: "cc-1", Content: requestAcceptedToolResult},
	)
	req := &modelDrivenCheckpointRequest{
		ToolCallID: "cc-1",
		Args:       tools.CompactContextArgs{ActiveObjective: "keep going", NextStep: "continue"},
	}
	bundle := a.captureModelDrivenBarrierSnapshot(snapshot)
	_, skip, preflight := a.modelDrivenLowGainPreflight(bundle, len(snapshot), snapshot, a.newModelDrivenCheckpointBuilder(t.Context(), bundle, snapshot, len(snapshot), req))
	if skip {
		t.Fatal("fixture must pass the gate")
	}
	if preflight.CurrentSource != "scratch" {
		t.Fatalf("current source = %q, want scratch for a changed head", preflight.CurrentSource)
	}
	// With no remembered request at all the same fallback applies.
	bundleNoPrep := modelDrivenBarrierSnapshot{
		snapshot:                    snapshot,
		maxTokens:                   a.ctxMgr.GetMaxTokens(),
		sessionDir:                  a.sessionDir,
		prepareReducedRequest:       a.compactionReductionScratch().prepareMessagesForLLM,
		fixedRequestTokens:          1000,
		postResetFixedRequestTokens: 4000,
		archiveMeta:                 a.captureCompactionArchiveMeta(),
	}
	_, skip2, preflight2 := a.modelDrivenLowGainPreflight(bundleNoPrep, len(snapshot), snapshot, a.newModelDrivenCheckpointBuilder(t.Context(), bundleNoPrep, snapshot, len(snapshot), req))
	if skip2 {
		t.Fatal("fixture must pass the gate without a prepared surface")
	}
	if preflight2.CurrentSource != "scratch" {
		t.Fatalf("current source = %q, want scratch without a prepared surface", preflight2.CurrentSource)
	}
}

func TestModelDrivenCacheRebuildCostAmortizesProjectedPrefix(t *testing.T) {
	// The rebuild charge is the write-read delta (1.15x) of the NEW checkpoint
	// prefix, amortized over the minimum apply interval — never the archived
	// head, which is dropped rather than rewritten.
	if got := modelDrivenCacheRebuildCost(0); got != 0 {
		t.Fatalf("zero projected prefix cost = %d, want 0", got)
	}
	projected := 30000
	want := projected * modelDrivenCacheRebuildDeltaNumer / modelDrivenCacheRebuildDeltaDenom / minModelDrivenApplyIntervalBatches
	if got := modelDrivenCacheRebuildCost(projected); got != want {
		t.Fatalf("rebuild cost = %d, want %d", got, want)
	}
	if got := modelDrivenCacheRebuildCost(projected); got >= projected {
		t.Fatalf("amortized rebuild cost %d must stay well below the projected prefix %d", got, projected)
	}
}

func TestModelDrivenPreflightRecordsCacheRebuildCostWithoutSubtracting(t *testing.T) {
	t.Parallel()
	// The prompt-cache rebuild charge is telemetry: a cacheable session's
	// preflight records the amortized cost on the stats while the gate keeps
	// deciding on raw surface savings, so a charge that would have flipped the
	// verdict under the old "net = saved − cost" rule no longer denies the
	// reset — the cost telemetry answers whether cache rebuilds eat the
	// gains instead of pre-deciding it inside the gate.
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	snapshot := []message.Message{
		{Role: message.RoleUser, Content: "original user request"},
		{Role: message.RoleAssistant, Content: strings.Repeat("analysis ", 4000)},
		{Role: message.RoleUser, Content: "follow up"},
		{Role: message.RoleAssistant, Content: strings.Repeat("more analysis ", 4000)},
		{Role: message.RoleUser, Content: "tail request"},
	}
	bundle := modelDrivenBarrierSnapshot{
		snapshot:              snapshot,
		maxTokens:             a.ctxMgr.GetMaxTokens(),
		sessionDir:            a.sessionDir,
		prepareReducedRequest: a.compactionReductionScratch().prepareMessagesForLLM,
		promptCacheCapable:    true,
	}
	req := &modelDrivenCheckpointRequest{
		ToolCallID: "cc-1",
		Args:       tools.CompactContextArgs{ActiveObjective: "keep going", NextStep: "continue"},
	}
	reason, skip, preflight := a.modelDrivenLowGainPreflight(bundle, 4, snapshot, a.newModelDrivenCheckpointBuilder(t.Context(), bundle, snapshot, 4, req))
	if skip {
		t.Fatalf("gate must pass on raw savings, got skip reason %q", reason)
	}
	if preflight.CacheRebuildCost <= 0 {
		t.Fatalf("cache-capable preflight must record the rebuild cost, got %d", preflight.CacheRebuildCost)
	}
	if preflight.SavedTokens < modelDrivenLowGainMinTokens {
		t.Fatalf("fixture must produce savings above the floor, got %d", preflight.SavedTokens)
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

func TestModelDrivenLowGainCheckComparesRawSavings(t *testing.T) {
	// The gates compare raw surface savings; the cache rebuild charge is a
	// per-provider billing weight recorded for telemetry, never subtracted
	// here, so a cacheable session is not pre-denied inside the gate.
	if reason, skip := modelDrivenLowGainCheck(10000, 5000); skip {
		t.Fatalf("raw savings above the gate must pass, got reason %q", reason)
	}
	// Savings that would have fallen below the floor only after subtracting a
	// 4000-token rebuild charge still pass: the cost no longer gates.
	if reason, skip := modelDrivenLowGainCheck(10000, 5000); skip {
		t.Fatalf("savings unaffected by the (telemetry-only) rebuild cost must pass, got reason %q", reason)
	}
	// The absolute floor still applies to raw savings.
	if reason, skip := modelDrivenLowGainCheck(10000, 1000); !skip {
		t.Fatal("raw savings below the absolute floor must skip")
	} else if !strings.Contains(reason, "below the low-gain gate") {
		t.Fatalf("floor skip reason must mention the gate, got %q", reason)
	}
	// The 10% relative gate applies to the raw savings against the current
	// prepared surface.
	if reason, skip := modelDrivenLowGainCheck(100000, 4000); !skip {
		t.Fatalf("raw savings below 10%% of the prepared surface must skip, got %q", reason)
	} else if !strings.Contains(reason, "below the low-gain gate") {
		t.Fatalf("relative-gate skip reason must mention the gate, got %q", reason)
	}
}

func TestModelDrivenApplyRecordsLastApplyBatch(t *testing.T) {
	t.Parallel()
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.newTurn("")
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
	// The checkpoint message itself carries the apply batch so a restart whose
	// transcript is a lone checkpoint restores the sequence continuously
	// instead of restarting at 0.
	checkpoint := a.ctxMgr.Snapshot()
	if len(checkpoint) == 0 || checkpoint[0].RequestBatch != 1 {
		var got uint64
		if len(checkpoint) > 0 {
			got = checkpoint[0].RequestBatch
		}
		t.Fatalf("checkpoint message RequestBatch = %d, want the apply batch 1", got)
	}
	// A durable apply advances the in-memory overlay window key: the reminder
	// claim keys on it, so the next window re-arms delivered and ccCalled.
	if a.compactionWindowGeneration != 1 {
		t.Fatalf("compactionWindowGeneration = %d, want 1 after the apply", a.compactionWindowGeneration)
	}
}

func TestUsageDrivenApplyClearsSkipCooldownAndGrace(t *testing.T) {
	t.Parallel()
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.newTurn("")
	a.ctxMgr.Append(message.Message{Role: "user", Content: "one"})
	a.ctxMgr.Append(message.Message{Role: "assistant", Content: "two"})
	a.ctxMgr.Append(message.Message{Role: "user", Content: "three"})
	a.requestBatches.reserve(a.sessionEpoch, 0)
	a.lastModelDrivenSkipBatch = 1
	a.lastModelDrivenSkipReason = "low_gain"
	a.compactionGraceStartBatch = 1
	a.compactionGraceExhausted = true
	a.pendingCompactionImminent = "stale"

	draft := &compactionDraft{
		NewMessages:    []message.Message{{Role: "user", Content: "[Context Summary]", IsCompactionSummary: true}},
		HeadSplit:      2,
		Index:          1,
		AbsHistoryPath: filepath.Join(a.sessionDir, "history-1.md"),
		PlanID:         1,
		Target:         compactionTarget{sessionEpoch: a.sessionEpoch},
	}
	if err := a.applyCompactionDraft(draft); err != nil {
		t.Fatalf("applyCompactionDraft: %v", err)
	}
	// A non-model-driven apply does not move the interval anchor…
	if a.lastModelDrivenApplyBatch != 0 {
		t.Fatalf("usage-driven apply must not record a model-driven apply batch, got %d", a.lastModelDrivenApplyBatch)
	}
	// …but the prepared surface the last low-gain verdict was computed on is
	// gone, so the skip cooldown and the grace window must reset.
	if a.lastModelDrivenSkipBatch != 0 || a.lastModelDrivenSkipReason != "" {
		t.Fatalf("usage-driven apply must clear the skip-cooldown state, got batch=%d reason=%q", a.lastModelDrivenSkipBatch, a.lastModelDrivenSkipReason)
	}
	if a.compactionGraceStartBatch != 0 || a.compactionGraceExhausted || a.pendingCompactionImminent != "" {
		t.Fatalf("durable apply must clear the grace state, got start=%d exhausted=%v pending=%q", a.compactionGraceStartBatch, a.compactionGraceExhausted, a.pendingCompactionImminent)
	}
}

func TestModelDrivenSettleRecordsSkipCooldownState(t *testing.T) {
	t.Parallel()
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.newTurn("")
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
	t.Parallel()
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

// TestModelDrivenCooldownExpiresDespitePerBatchRetries pins that the same-reason
// cooldown is a fixed window, not a sliding one: a model that retries on every
// batch must still reach a fresh preflight on the third batch. Refreshing the
// skip anchor on a cooldown hit would keep current-last pinned at 1 forever and
// the low-gain preflight would never run again.
func TestModelDrivenCooldownExpiresDespitePerBatchRetries(t *testing.T) {
	t.Parallel()
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.newTurn("")
	// A genuine low-gain skip settled at batch 10.
	a.settleModelDrivenSkip(modelDrivenSkipDraft(1, compactionTarget{}, "projected savings too small", modelDrivenSkipReasonLowGain, 10, nil))
	if a.lastModelDrivenSkipBatch != 10 {
		t.Fatalf("low-gain skip must anchor the cooldown at its own batch, got %d", a.lastModelDrivenSkipBatch)
	}

	// Batch 11: the model retries immediately and is cooled down.
	bundle := modelDrivenBarrierSnapshot{
		currentRequestBatch:       11,
		lastModelDrivenSkipReason: a.lastModelDrivenSkipReason,
		lastModelDrivenSkipBatch:  a.lastModelDrivenSkipBatch,
	}
	reason, skipReason, skip := a.modelDrivenIntervalCooldownVerdict(bundle)
	if !skip || skipReason != modelDrivenSkipReasonLowGain {
		t.Fatalf("retry one batch after a low-gain skip must be cooled down, got skip=%v reason=%q", skip, skipReason)
	}
	a.settleModelDrivenSkip(modelDrivenSkipDraft(2, compactionTarget{}, reason, skipReason, modelDrivenPolicySkipRecordBatch(skipReason, bundle.currentRequestBatch), nil))
	if a.lastModelDrivenSkipBatch != 10 {
		t.Fatalf("a cooldown hit must not slide the window, anchor batch = %d, want 10", a.lastModelDrivenSkipBatch)
	}

	// Batch 12: the cooldown has expired, so the request reaches the preflight
	// again instead of being short-circuited forever.
	if _, _, skip := a.modelDrivenIntervalCooldownVerdict(modelDrivenBarrierSnapshot{
		currentRequestBatch:       12,
		lastModelDrivenSkipReason: a.lastModelDrivenSkipReason,
		lastModelDrivenSkipBatch:  a.lastModelDrivenSkipBatch,
	}); skip {
		t.Fatal("the cooldown must expire two batches after the original low-gain skip even when the model retried every batch")
	}
}

// TestModelDrivenDuplicateSkipKeepsLowGainCooldownAnchor pins that a duplicate
// verdict leaves the cooldown state alone. No gate reads "duplicate" — the
// cooldown binds to low_gain and the duplicate verdict re-derives itself from
// the checkpoint fingerprint — so recording it would only overwrite a live
// low-gain anchor with a reason nothing consults, cutting that window short.
func TestModelDrivenDuplicateSkipKeepsLowGainCooldownAnchor(t *testing.T) {
	t.Parallel()
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.newTurn("")
	a.settleModelDrivenSkip(modelDrivenSkipDraft(1, compactionTarget{}, "projected savings too small", modelDrivenSkipReasonLowGain, 10, nil))

	// A duplicate request settles at a later batch: the low-gain anchor and its
	// reason must survive so the cooldown window still expires on its own clock.
	skipReason := modelDrivenSkipReasonDuplicate
	a.settleModelDrivenSkip(modelDrivenSkipDraft(2, compactionTarget{}, "no new work since the last applied checkpoint", skipReason, modelDrivenPolicySkipRecordBatch(skipReason, 11), nil))
	if a.lastModelDrivenSkipBatch != 10 || a.lastModelDrivenSkipReason != modelDrivenSkipReasonLowGain {
		t.Fatalf("duplicate skip must not touch the cooldown state, got batch=%d reason=%q, want 10/%q", a.lastModelDrivenSkipBatch, a.lastModelDrivenSkipReason, modelDrivenSkipReasonLowGain)
	}
	if _, _, skip := a.modelDrivenIntervalCooldownVerdict(modelDrivenBarrierSnapshot{
		currentRequestBatch:       11,
		lastModelDrivenSkipReason: a.lastModelDrivenSkipReason,
		lastModelDrivenSkipBatch:  a.lastModelDrivenSkipBatch,
	}); !skip {
		t.Fatal("the original low-gain cooldown must still hold at batch 11 after a duplicate skip")
	}
}

func TestModelDrivenCheckpointDuplicateUsesArgsAndRuntimeFingerprint(t *testing.T) {
	runtime := modelDrivenRuntimeInputFingerprint(modelDrivenRuntimeInput{
		todos: []tools.TodoItem{{ID: "task-1", Content: "finish", Status: "completed"}},
	})
	args := tools.CompactContextArgs{ActiveObjective: "finish", NextStep: "wait"}
	req := &modelDrivenCheckpointRequest{
		Args:            args,
		ArgsFingerprint: modelDrivenArgsFingerprint(args),
	}
	bundle := modelDrivenBarrierSnapshot{
		snapshot:                             []message.Message{{IsCompactionSummary: true, CompactionSummaryMode: compactionSummaryModeModelDriven}},
		runtimeStateFingerprint:              runtime,
		lastModelDrivenCheckpointFingerprint: modelDrivenCheckpointFingerprint(req.ArgsFingerprint, runtime),
	}
	if reason, skipReason, skip := (&MainAgent{}).modelDrivenCheckpointSkipVerdict(bundle, req); !skip || skipReason != modelDrivenSkipReasonDuplicate || !strings.Contains(reason, "unchanged") {
		t.Fatalf("unchanged checkpoint must be skipped as duplicate: reason=%q skip_reason=%q skip=%v", reason, skipReason, skip)
	}
	bundle.runtimeStateFingerprint = modelDrivenRuntimeInputFingerprint(modelDrivenRuntimeInput{
		todos: []tools.TodoItem{{ID: "task-1", Content: "finish", Status: "in_progress"}},
	})
	if _, skipReason, skip := (&MainAgent{}).modelDrivenCheckpointSkipVerdict(bundle, req); skip || skipReason == modelDrivenSkipReasonDuplicate {
		t.Fatalf("changed runtime state must not be treated as duplicate: skip_reason=%q skip=%v", skipReason, skip)
	}
}

// TestModelDrivenApplyRecordsFingerprintForDuplicateSkip pins the record side
// of the duplicate chain end to end: an applied checkpoint must write the
// fingerprint the skip verdict reads, so re-arming the same args with the
// same runtime state skips as duplicate. A test that only hand-sets the
// fingerprint asserts the verdict in isolation; this one fails if the apply
// stops recording it or records a different input.
func TestModelDrivenApplyRecordsFingerprintForDuplicateSkip(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	a.newTurn("")
	a.requestBatches.reserve(a.sessionEpoch, 0)

	a.ctxMgr.Append(message.Message{Role: message.RoleUser, Content: "verify parser"})
	callID := "checkpoint-1"
	a.ctxMgr.Append(message.Message{Role: message.RoleAssistant, RequestBatch: 1, ToolCalls: []message.ToolCall{testToolCall(callID, tools.NameCompactContext)}})
	argsJSON := `{"active_objective":"verify parser","next_step":"run tests"}`
	if _, err := a.tryArmModelDrivenCheckpoint(callID, argsJSON); err != nil {
		t.Fatalf("tryArmModelDrivenCheckpoint: %v", err)
	}
	pending := a.pendingModelDriven
	if pending == nil {
		t.Fatal("arming must leave a pending model-driven call")
	}
	snapshot := a.ctxMgr.Snapshot()
	bundle := a.captureModelDrivenBarrierSnapshot(snapshot)
	draft := &compactionDraft{
		SummaryMode:                compactionSummaryModeModelDriven,
		RuntimeGeneration:          bundle.currentRequestBatch,
		RuntimeStateFingerprint:    bundle.runtimeStateFingerprint,
		ModelDrivenArgsFingerprint: pending.ArgsFingerprint,
		HeadSplit:                  len(snapshot),
		Index:                      1,
		PlanID:                     1,
		Target:                     compactionTarget{sessionEpoch: a.sessionEpoch},
		NewMessages: []message.Message{{
			Role:                  message.RoleUser,
			Content:               "checkpoint summary",
			IsCompactionSummary:   true,
			CompactionSummaryMode: compactionSummaryModeModelDriven,
		}},
	}
	if err := a.applyCompactionDraft(draft); err != nil {
		t.Fatalf("applyCompactionDraft: %v", err)
	}
	if a.lastModelDrivenCheckpointFingerprint == "" {
		t.Fatal("apply must record the checkpoint fingerprint")
	}

	// Re-arming the same args against the same runtime state must now skip
	// as a duplicate; the fingerprint the verdict compares against has to be
	// the one the apply just wrote, not a hand-set fixture.
	after := a.captureModelDrivenBarrierSnapshot(a.ctxMgr.Snapshot())
	retryCallID := "checkpoint-2"
	a.ctxMgr.Append(message.Message{Role: message.RoleAssistant, RequestBatch: after.currentRequestBatch, ToolCalls: []message.ToolCall{testToolCall(retryCallID, tools.NameCompactContext)}})
	if _, err := a.tryArmModelDrivenCheckpoint(retryCallID, argsJSON); err != nil {
		t.Fatalf("re-arming identical args: %v", err)
	}
	retry := a.pendingModelDriven
	if retry == nil {
		t.Fatal("re-arming must leave a pending model-driven call")
	}
	if reason, skipReason, skip := a.modelDrivenCheckpointSkipVerdict(after, retry); !skip || skipReason != modelDrivenSkipReasonDuplicate {
		t.Fatalf("re-armed identical checkpoint must skip as duplicate: reason=%q skip_reason=%q skip=%v", reason, skipReason, skip)
	}
}
