package agent

import (
	"strings"
	"time"

	"github.com/keakon/chord/internal/agent/requesttrim"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/toolname"
	"github.com/keakon/chord/internal/tools"
)

func (a *MainAgent) prepareMessagesForLLM(messages []message.Message) []message.Message {
	return a.prepareMessagesForLLMWithOptions(messages, true)
}

// newReductionHistoryScanForAgent attaches the agent's per-call memo so the
// whole-history scans reuse byte-derived verdicts computed on earlier requests.
func newReductionHistoryScanForAgent(a *MainAgent, messages []message.Message) *requesttrim.Scan {
	var memo *requesttrim.Memo
	if a != nil {
		memo = &a.reductionMemo
	}
	return requesttrim.NewScan(messages, memo)
}

func (a *MainAgent) prepareMessagesForLLMWithOptions(messages []message.Message, rememberPrepared bool) (result []message.Message) {
	messages = projectGeneratedImagePreviews(messages)
	messages = projectCheckpointRequests(messages)
	if a != nil && a.ctxMgr != nil && rememberPrepared {
		ratio := a.ctxMgr.CalibratedRatio()
		before := messages
		defer func() { a.freezeDiagnosticReduction(before, result, ratio) }()
	}
	if a != nil && rememberPrepared {
		a.setPreparedStablePrefixLen(0)
	}
	if len(messages) == 0 {
		if a != nil {
			a.setContextReductionStats(ContextReductionStats{})
		}
		return nil
	}

	policy := a.contextReductionPolicy()
	if policy.Disabled {
		if a != nil {
			stats := ContextReductionStats{TokensBefore: estimateMessagesTokens(a.ctxMgr, messages)}
			stats.TokensAfter = stats.TokensBefore
			a.fillReductionModelContinuity(&stats)
			a.setContextReductionStats(stats)
		}
		return messages
	}
	scan := newReductionHistoryScanForAgent(a, messages)
	currentBatch := a.currentRequestBatch(messages)
	externalReadInvalidated := a.externallyInvalidatedReadsAfterMutatingShell(messages, scan)
	// Two independent read-invalidation sources merge here. The mutating-shell
	// scan covers read results older than a mutating shell — including
	// cancelled reads, which the lazy scan skips — and is the prompt trigger
	// for shell-caused changes; the lazy scan covers every successful read
	// against external edits (any non-tool process) via stat-first
	// verification. Both produce the same stale-read index map and share no
	// downstream distinction.
	if lazyInvalidated := a.externalReadsInvalidatedLazy(messages, scan); len(lazyInvalidated) > 0 {
		if externalReadInvalidated == nil {
			externalReadInvalidated = make(map[int]bool, len(lazyInvalidated))
		}
		for index := range lazyInvalidated {
			externalReadInvalidated[index] = true
		}
	}

	rp := a.newReductionPass(messages, policy, scan, currentBatch, externalReadInvalidated, rememberPrepared)
	if a != nil {
		if reused, handled := rp.tryReuseWrapUpGrace(); handled {
			return reused
		}
		if rememberPrepared {
			if reused, handled := rp.tryReuseStableSurfaceBeforeScan(); handled {
				return reused
			}
		}
	}
	rp.initializeScan()
	rp.collectReductionProposals()
	rp.decideBoundaryFlush()
	rp.applyReductionProposals()
	if a != nil {
		return rp.finalize()
	}
	return rp.prepared
}

// reductionPass carries the per-request state shared by the phases of
// prepareMessagesForLLMWithOptions: the reuse probes, the proposal scan, the
// cache-boundary flush decision, the apply pass, and the statistics
// finalizers all read and mutate the same reduction outcome.
type reductionPass struct {
	a                       *MainAgent
	messages                []message.Message
	policy                  requesttrim.Policy
	scan                    *requesttrim.Scan
	currentBatch            uint64
	externalReadInvalidated map[int]bool
	modelSnapshot           llmModelContinuitySnapshot
	wrapUpGraceActive       bool
	rememberPrepared        bool

	prepared []message.Message
	stats    ContextReductionStats

	incrementalEnabled   bool
	frozenReducedIndices []bool
	frozenNextReviewAge  []int
	previousToolResults  int
	frozenBoundary       int

	callMeta                   map[string]requesttrim.ToolCall
	requestAge                 []int
	repeated                   map[int]bool
	toolResults                int
	nextReviewAge              []int
	toolResultThresholdCrossed bool
	readValidityByIndex        map[int]requesttrim.ReadValidity
	diagnosticsSuperseded      map[int]bool

	discardedInputs        map[string]string
	discardedReadRevisions map[string]string
	recalledInputs         map[string]struct{}

	proposals       []reductionProposal
	semanticRefresh bool
	applyBoundary   bool

	evidenceStats requesttrim.EvidenceStats
}

// reductionProposal is one pass-1 outcome: a tool result that can be reduced,
// with the bytes and bookkeeping the apply pass needs.
type reductionProposal struct {
	index    int
	class    requesttrim.Class
	toolName string
	rule     string
	reduced  string
	decision requesttrim.Decision
	// force applies a frozen-boundary proposal even when the cache
	// amortization gate would defer it. Only a read marker that lacks the
	// stale/superseded guidance uses it: leaving such a read unmarked makes
	// stableReductionSurfaceNeedsReview re-scan on every request, so it is
	// re-rendered once instead of waiting for a flush.
	force      bool
	recallable bool
	// repeated marks outputs whose content survives in an identical later
	// call; reducing such a copy discards nothing, so it must not register
	// in discardedInputs (a repeated read that is also invalidated or
	// superseded classifies as read-like, not repeated, so class alone
	// cannot express this).
	repeated bool
}

// newReductionPass captures the reuse probes' inputs. Full scan preparation
// waits until both probes miss so stable requests keep their cheap path.
func (a *MainAgent) newReductionPass(messages []message.Message, policy requesttrim.Policy, scan *requesttrim.Scan, currentBatch uint64, externalReadInvalidated map[int]bool, rememberPrepared bool) *reductionPass {
	rp := &reductionPass{
		a:                       a,
		messages:                messages,
		policy:                  policy,
		scan:                    scan,
		currentBatch:            currentBatch,
		externalReadInvalidated: externalReadInvalidated,
		rememberPrepared:        rememberPrepared,
	}
	if a != nil {
		rp.modelSnapshot = a.llmModelContinuitySnapshot()
	}
	return rp
}

func (rp *reductionPass) initializeScan() {
	a, messages, policy, scan := rp.a, rp.messages, rp.policy, rp.scan
	rp.prepared = append([]message.Message(nil), messages...)
	rp.stats = ContextReductionStats{TokensBefore: estimateMessagesTokens(a.ctxMgr, rp.prepared)}

	// Incremental reduction: freeze already-reduced tool results from the
	// previous stable surface so their marker content stays cache-stable.
	// Only the not-yet-reduced prefix and the new tail are re-examined.
	// Tool-definition changes invalidate the frozen surface (full re-reduction).
	frozenPrefix, reducedIndices, frozenNextReviewAge, previousToolResults, boundary, enabled := a.incrementalReductionSurface(rp.prepared)
	rp.incrementalEnabled = enabled
	rp.frozenReducedIndices = reducedIndices
	rp.frozenNextReviewAge = frozenNextReviewAge
	rp.previousToolResults = previousToolResults
	rp.frozenBoundary = boundary
	if enabled && boundary > 0 {
		for i := range boundary {
			if reducedIndices != nil && i < len(reducedIndices) && reducedIndices[i] {
				rp.prepared[i] = frozenPrefix[i].Clone()
			}
		}
	}

	rp.callMeta = scan.CallMeta()
	rp.requestAge = requesttrim.RequestBatchesAfter(rp.prepared, rp.currentBatch)
	rp.repeated = scan.RepeatedOutputs()
	rp.toolResults = countToolResults(rp.prepared)
	rp.nextReviewAge = make([]int, len(rp.prepared))
	if enabled && len(frozenNextReviewAge) > 0 {
		copy(rp.nextReviewAge, frozenNextReviewAge)
	}
	rp.toolResultThresholdCrossed = enabled && previousToolResults < policy.MinToolResultsPrune && rp.toolResults >= policy.MinToolResultsPrune
	evidenceStarted := time.Now()
	evidence := requesttrim.BuildFileEvidence(rp.prepared, rp.callMeta)
	rp.evidenceStats = evidence.Stats(time.Since(evidenceStarted))
	rp.readValidityByIndex = evidence.ValidityByMessage()
	rp.diagnosticsSuperseded = diagnosticsSupersededFlags(rp.prepared, rp.callMeta)
	if len(rp.externalReadInvalidated) > 0 && rp.readValidityByIndex == nil {
		rp.readValidityByIndex = make(map[int]requesttrim.ReadValidity, len(rp.externalReadInvalidated))
	}
	for index := range rp.externalReadInvalidated {
		validity := rp.readValidityByIndex[index]
		validity.Invalidated = true
		validity.Superseded = false
		// Neither source of external invalidation leaves the replaced bytes in
		// the transcript: a mutating shell command reports its own output, not
		// the file it rewrote, and an out-of-band edit is invisible until the
		// stat check notices it. The read output is the only record left.
		validity.PriorContentLost = true
		rp.readValidityByIndex[index] = validity
	}
	// discardedInputs is the recall-protection evidence base: input key ->
	// ToolCallID of the call whose output was actually summarized away on this
	// or an earlier request. Repeated-collapse never registers — it always
	// leaves a fresher full copy in context, so a re-issue after it is model
	// redundancy, not proof that reduction dropped needed content. The
	// ToolCallID distinguishes a genuine re-issue (same input, different call)
	// from the discarded message itself being re-evaluated after a surface
	// invalidation.
	rp.discardedInputs = a.lastPreparedDiscardedInputsSnapshot()
	rp.discardedReadRevisions = make(map[string]string)
	// recalledInputs carries the session's recall-protection set into this pass;
	// registrations during the pass update both the local view (so later
	// messages in the same pass see them) and the durable per-agent set.
	rp.recalledInputs = a.recalledReductionInputsSnapshot()
}

// tryReuseWrapUpGrace reuses the previous stable surface while the wrap-up
// grace is active, so the request that closes a turn does not pay for fresh
// low-gain reductions. handled reports that the caller must return the
// reused surface; otherwise the full scan proceeds with the grace flag set.
func (rp *reductionPass) tryReuseWrapUpGrace() (reused []message.Message, handled bool) {
	if rp.modelSnapshot.ProjectedModelRunLength <= 1 || rp.a.hasQueuedUserInputForRecovery() || !rp.a.consumeContextReductionWrapUpGrace(rp.a.currentTurnID()) {
		return nil, false
	}
	rp.wrapUpGraceActive = true
	previous, ok := rp.a.stableReductionSurfaceCandidate(rp.a.currentTurnID())
	if !ok || !hasReductionSavings(previous.Stats) || len(previous.Messages) == 0 || len(rp.messages) < len(previous.Messages) {
		return nil, false
	}
	if stableReductionSurfaceNeedsReview(previous, rp.scan, rp.currentBatch, rp.externalReadInvalidated) {
		rp.wrapUpGraceActive = false
		return nil, false
	}
	owned := append([]message.Message(nil), rp.messages...)
	reuse, compatible := reuseStableReductionPrefix(previous, owned, rp.messages)
	if !compatible {
		return nil, false
	}
	stats := highLevelContextReductionStats(rp.a.ctxMgr, rp.messages, reuse)
	if len(stats.ByToolAndRule) == 0 {
		stats.ByToolAndRule = cloneContextReductionBuckets(previous.Stats.ByToolAndRule)
	}
	stats.Protected = true
	stats.ProtectReason = contextProtectReasonWrapUpGrace
	stats.ReusedStable = true
	stats.fillModelContinuity(rp.modelSnapshot)
	rp.a.setCurrentRequestSurface(&stats, reuse)
	rp.a.setContextReductionStats(stats)
	if rp.rememberPrepared {
		rp.a.setPreparedStablePrefixLen(len(previous.Messages))
		rp.a.rememberPreparedLLMRequest(rp.a.currentTurnID(), rp.messages, reuse, nil, previous.NextReviewAge, previous.ToolResults, previous.Policy)
	}
	return reuse, true
}

// tryReuseStableSurfaceBeforeScan replays the previous stable surface when it
// needs no review, skipping the full scan entirely.
func (rp *reductionPass) tryReuseStableSurfaceBeforeScan() (reused []message.Message, handled bool) {
	reuse, stats, ok := rp.a.tryReuseStableReductionSurfaceBeforeFullScan(rp.messages, rp.policy, rp.scan, rp.currentBatch, rp.externalReadInvalidated)
	if !ok {
		return nil, false
	}
	rp.a.fillReductionModelContinuity(&stats)
	rp.a.setCurrentRequestSurface(&stats, reuse)
	rp.a.setContextReductionStats(stats)
	if n, ok := rp.a.stableReductionSurfacePrefixLen(rp.a.currentTurnID()); ok {
		rp.a.setPreparedStablePrefixLen(n)
	}
	previous, _ := rp.a.stableReductionSurfaceCandidate(rp.a.currentTurnID())
	rp.a.rememberPreparedLLMRequest(rp.a.currentTurnID(), rp.messages, reuse, nil, previous.NextReviewAge, previous.ToolResults, previous.Policy)
	return reuse, true
}

func (rp *reductionPass) noteReduction(toolName, rule, original, reduced string) {
	saved := len(original) - len(reduced)
	if saved <= 0 {
		return
	}
	rp.stats.Messages++
	rp.stats.Bytes += saved
	beforeTokens := estimateMessageTokens(rp.a.ctxMgr, message.Message{Content: original})
	afterTokens := estimateMessageTokens(rp.a.ctxMgr, message.Message{Content: reduced})
	tokensSaved := beforeTokens - afterTokens
	if tokensSaved > 0 {
		rp.stats.TokensSaved += tokensSaved
	}
	if rp.stats.ByToolAndRule == nil {
		rp.stats.ByToolAndRule = make(map[string]ContextReductionBucket)
	}
	key := requesttrim.ToolNameOrUnknown(toolName) + "/" + rule
	bucket := rp.stats.ByToolAndRule[key]
	bucket.Messages++
	bucket.Bytes += saved
	if tokensSaved > 0 {
		bucket.TokensSaved += tokensSaved
	}
	rp.stats.ByToolAndRule[key] = bucket
}

func (rp *reductionPass) noteSkip(reason string) {
	if reason == "" {
		return
	}
	if rp.stats.SkippedByReason == nil {
		rp.stats.SkippedByReason = make(map[string]int)
	}
	rp.stats.SkippedByReason[reason]++
}

func (rp *reductionPass) noteRetention(decision requesttrim.Decision) {
	if decision.Level == "" {
		return
	}
	if rp.stats.ByRetentionLevel == nil {
		rp.stats.ByRetentionLevel = make(map[string]int)
	}
	rp.stats.ByRetentionLevel[string(decision.Level)]++
	if !decision.Recoverable() {
		rp.stats.UnrecoverableReductions++
	}
}

func (rp *reductionPass) noteOverCompression(kind, toolName string) {
	if kind == "" {
		return
	}
	if rp.stats.OverCompression == nil {
		rp.stats.OverCompression = make(map[string]int)
	}
	rp.stats.OverCompression[kind]++
	// Per-tool breakdown alongside the aggregate. The aggregate answers how
	// often reduction went too far; deciding which shape should compress
	// less needs to know whose summary provoked the re-fetch. Tool name is
	// the discriminator available here: the re-fetched output is still too
	// young to have been classified, so the summary shape that caused this
	// is only reachable through the tool that produced it.
	if rp.stats.OverCompressionByTool == nil {
		rp.stats.OverCompressionByTool = make(map[string]int)
	}
	rp.stats.OverCompressionByTool[kind+"/"+requesttrim.ToolNameOrUnknown(toolName)]++
}

func (rp *reductionPass) noteRecalledInput(key string) {
	if key == "" {
		return
	}
	if rp.recalledInputs == nil {
		rp.recalledInputs = make(map[string]struct{})
	}
	rp.recalledInputs[key] = struct{}{}
	rp.a.noteRecalledReductionInput(key)
}

// collectReductionProposals is pass 1: collect reduction proposals without
// mutating anything. Proposals inside the frozen boundary rewrite bytes the
// provider already cached, so they are only applied together ("batched") when
// a flush is justified; proposals in the new tail were never sent and are
// always free to apply.
func (rp *reductionPass) collectReductionProposals() {
	a := rp.a
	// Recover read revisions for discarded reads still present in the frozen
	// prefix so the reread-same-revision over-compression split keeps working
	// across requests. Key membership itself travels via DiscardedInputs: the
	// frozen indices alone cannot distinguish a summarized output from a
	// repeated-collapse, which must not count as discarded.
	for i := range rp.prepared {
		if rp.incrementalEnabled && rp.frozenReducedIndices != nil && i < len(rp.frozenReducedIndices) && rp.frozenReducedIndices[i] && rp.prepared[i].Role == message.RoleTool {
			meta := rp.callMeta[rp.prepared[i].ToolCallID]
			toolName := toolname.Normalize(meta.Name)
			key := a.reductionMemo.ToolInputKey(rp.prepared[i].ToolCallID, meta)
			if _, discarded := rp.discardedInputs[key]; !discarded {
				continue
			}
			if toolName == tools.NameRead {
				if revision := reductionReadRevision(&meta, rp.prepared[i].FileState); revision != "" {
					rp.discardedReadRevisions[key] = revision
				}
			}
		}
	}
	for i := range rp.prepared {
		if rp.prepared[i].Role != message.RoleTool {
			continue
		}
		// Skip already-reduced frozen prefix messages: their content is a stable
		// marker copied from the previous surface. Re-reducing could produce a
		// different marker (e.g. repeated-call detection) and break cache reuse.
		meta := rp.callMeta[rp.prepared[i].ToolCallID]
		toolName := toolname.Normalize(meta.Name)
		age := rp.requestAge[i]
		validity := rp.readValidityByIndex[i]
		if rp.incrementalEnabled && rp.frozenReducedIndices != nil && i < len(rp.frozenReducedIndices) && rp.frozenReducedIndices[i] {
			if toolName != tools.NameRead || (!validity.Invalidated && !validity.Superseded) {
				rp.noteSkip(contextReductionSkipFrozenReduced)
				continue
			}
			// The frozen marker already carries the stale/superseded guidance,
			// so re-rendering it would rewrite cached bytes without changing
			// what the model can act on.
			if readMarkerCarriesValidityGuidance(rp.prepared[i].Content) {
				rp.noteSkip(contextReductionSkipFrozenReduced)
				continue
			}
			ctx := requesttrim.Context{
				ToolName:             toolName,
				ToolCallID:           rp.prepared[i].ToolCallID,
				Memo:                 &a.reductionMemo,
				Meta:                 meta,
				Content:              rp.messages[i].Content,
				ToolStatus:           rp.messages[i].ToolStatus,
				FileState:            rp.messages[i].FileState,
				Age:                  age,
				Policy:               rp.policy,
				ToolResults:          rp.toolResults,
				ReadInvalidated:      validity.Invalidated,
				ReadSuperseded:       validity.Superseded,
				ReadPriorContentLost: validity.PriorContentLost,
				ArchiveDir:           a.sessionDir,
			}
			reduced, rule, ok := requesttrim.Render(requesttrim.ReadLike, ctx)
			if ok {
				rp.proposals = append(rp.proposals, reductionProposal{
					index:      i,
					class:      requesttrim.ReadLike,
					toolName:   toolName,
					rule:       rule,
					reduced:    reduced,
					decision:   requesttrim.RetentionDecision(ctx, requesttrim.ReducedVerdict(requesttrim.ReadLike), rule, reduced),
					force:      true,
					recallable: true,
					repeated:   rp.repeated[i],
				})
				rp.semanticRefresh = true
			}
			continue
		}
		if rp.externalReadInvalidated[i] && toolName == tools.NameRead {
			ctx := requesttrim.Context{
				ToolName:    toolName,
				ToolCallID:  rp.prepared[i].ToolCallID,
				Memo:        &a.reductionMemo,
				Meta:        meta,
				Content:     rp.messages[i].Content,
				ToolStatus:  rp.messages[i].ToolStatus,
				FileState:   rp.messages[i].FileState,
				Age:         age,
				Policy:      rp.policy,
				ToolResults: rp.toolResults,
				// A mutating shell command or an out-of-band process replaced the
				// file: nothing in the transcript holds the bytes this read saw,
				// so the summary must carry an archive address.
				ReadInvalidated:      true,
				ReadPriorContentLost: true,
				ArchiveDir:           a.sessionDir,
			}
			if reduced, rule, ok := requesttrim.Render(requesttrim.ReadLike, ctx); ok {
				rp.proposals = append(rp.proposals, reductionProposal{
					index:      i,
					class:      requesttrim.ReadLike,
					toolName:   toolName,
					rule:       rule,
					reduced:    reduced,
					decision:   requesttrim.RetentionDecision(ctx, requesttrim.ReducedVerdict(requesttrim.ReadLike), rule, reduced),
					recallable: true,
					repeated:   rp.repeated[i],
				})
				rp.semanticRefresh = true
			}
			continue
		}
		if rp.incrementalEnabled && i < rp.frozenBoundary && toolName != tools.NameRead && !rp.repeated[i] && !rp.toolResultThresholdCrossed &&
			i < len(rp.frozenNextReviewAge) && rp.frozenNextReviewAge[i] > age {
			rp.noteSkip(contextReductionSkipDeferredReview)
			continue
		}
		ctx := requesttrim.Context{
			ToolName:              toolName,
			ToolCallID:            rp.prepared[i].ToolCallID,
			Memo:                  &a.reductionMemo,
			Meta:                  meta,
			Content:               rp.prepared[i].Content,
			ToolStatus:            rp.prepared[i].ToolStatus,
			FileState:             rp.prepared[i].FileState,
			Age:                   age,
			Policy:                rp.policy,
			Repeated:              rp.repeated[i],
			ToolResults:           rp.toolResults,
			ShellReadOnly:         toolName == tools.NameShell && a.shellCommandReadOnly(rp.prepared[i].ToolCallID, meta.Args),
			ReadInvalidated:       validity.Invalidated,
			ReadSuperseded:        validity.Superseded,
			ReadPriorContentLost:  validity.PriorContentLost,
			DiagnosticsSuperseded: rp.diagnosticsSuperseded[i],
			ArchiveDir:            a.sessionDir,
		}
		inputKey := a.reductionMemo.ToolInputKey(rp.prepared[i].ToolCallID, meta)
		// Recall protection applies to content-fetch shapes only (reads, web
		// fetches, searches, read-only shell): re-running a mutating command
		// seeks fresh state, not lost content. Older duplicates keep collapsing
		// to repeated markers, and a read known to be stale keeps its stale
		// marker — that guidance outweighs retention.
		staleRead := toolName == tools.NameRead && (validity.Invalidated || validity.Superseded)
		contentFetch := !rp.repeated[i] && !staleRead &&
			(requesttrim.IsReadLike(toolName) || ctx.ShellReadOnly || (toolName != tools.NameShell && requesttrim.LooksLikeSearchResult(ctx)))
		if contentFetch {
			if _, recalled := rp.recalledInputs[inputKey]; recalled {
				rp.noteSkip(contextReductionSkipRecalledInput)
				rp.nextReviewAge[i] = 0
				continue
			}
		}
		verdict := requesttrim.Classify(ctx)
		class := verdict.Class
		if class == requesttrim.None {
			rp.noteRetention(requesttrim.RetentionDecision(ctx, verdict, "", ""))
			if ctx.ReadRetentionProtects() {
				rp.stats.ProtectedReadTokens += estimateMessageTokens(a.ctxMgr, message.Message{Content: rp.prepared[i].Content})
			}
			rp.nextReviewAge[i] = requesttrim.NextReviewAge(ctx)
			// The classification already decided which protection fired; asking
			// isHighRiskToolOutput again would re-scan the whole payload.
			if verdict.Reason == requesttrim.RetentionReasonRecentHighRisk {
				rp.noteSkip(contextReductionSkipRecentHighRisk)
			} else if len(rp.prepared[i].Content) > rp.policy.StaleOutputBytes {
				rp.noteSkip(contextReductionSkipLargeUnreduced)
			}
			if discardedID, discardedBefore := rp.discardedInputs[inputKey]; discardedBefore && discardedID != rp.prepared[i].ToolCallID {
				if contentFetch {
					rp.noteRecalledInput(inputKey)
				}
				if requesttrim.IsReadLike(toolName) {
					rp.noteOverCompression(contextReductionOverCompressionReread, toolName)
					if toolName == tools.NameRead {
						previousRevision := rp.discardedReadRevisions[inputKey]
						currentRevision := reductionReadRevision(&meta, rp.prepared[i].FileState)
						if previousRevision != "" && currentRevision != "" {
							if previousRevision == currentRevision {
								rp.noteOverCompression(contextReductionOverCompressionRereadSameRevision, toolName)
							} else {
								rp.noteOverCompression(contextReductionOverCompressionRereadChangedRevision, toolName)
							}
						}
					}
				} else if requesttrim.LooksLikeSearchResult(ctx) {
					rp.noteOverCompression(contextReductionOverCompressionResearch, toolName)
				}
			}
			continue
		}
		// A second live copy of a content-fetch input whose earlier output was
		// genuinely discarded is about to be reduced too: the model re-fetched
		// content that reduction had dropped. Keep the newest copy instead and
		// remember the input for the rest of the session.
		if contentFetch {
			if discardedID, dup := rp.discardedInputs[inputKey]; dup && discardedID != rp.prepared[i].ToolCallID {
				rp.noteRecalledInput(inputKey)
				rp.noteSkip(contextReductionSkipRecalledInput)
				rp.nextReviewAge[i] = 0
				continue
			}
		}
		rp.nextReviewAge[i] = 0
		reduced, rule, ok := requesttrim.Render(class, ctx)
		if !ok {
			continue
		}
		decision := requesttrim.RetentionDecision(ctx, verdict, rule, reduced)
		// discardedInputs is consumed only by recall protection (content-fetch
		// shapes) and over-compression stats (read-like or search shapes).
		// Keys outside those shapes — mutating shells, edit/apply_patch
		// diagnostics — can never be read back, and their keys embed the full
		// original args (a whole patch for apply_patch), so registering them
		// only grows the map and every per-request clone of it.
		recallable := contentFetch || requesttrim.IsReadLike(toolName) ||
			(toolName != tools.NameShell && requesttrim.LooksLikeSearchResult(ctx))
		rp.proposals = append(rp.proposals, reductionProposal{
			index:      i,
			class:      class,
			toolName:   toolName,
			rule:       rule,
			reduced:    reduced,
			decision:   decision,
			recallable: recallable,
			repeated:   ctx.Repeated,
		})
		// ctx.Repeated (not class) guards the registry: a repeated read that is
		// also invalidated/superseded classifies as read-like, but an identical
		// later call still carries the content, so reducing this copy discards
		// nothing — registering it would flag that later copy as a false
		// over-compression reread.
		if !ctx.Repeated && recallable && (!rp.incrementalEnabled || i >= rp.frozenBoundary) {
			recordDiscardedInputEvidence(rp.discardedInputs, inputKey, rp.prepared[i].ToolCallID)
			if toolName == tools.NameRead {
				if revision := reductionReadRevision(&meta, rp.prepared[i].FileState); revision != "" {
					rp.discardedReadRevisions[inputKey] = revision
				}
			}
		}
	}
}

// decideBoundaryFlush determines whether boundary proposals are applied this
// request. Rewriting the cached prefix at position p re-bills everything after
// p at cache-write price instead of cache-read price, while the reduction
// saves its tokens on every subsequent request. Flush when the cache is
// invalid anyway (first request on this model ref) or when the pending savings
// amortize the rewrite within the bounded policy horizon. A queued checkpoint
// ends the current prefix's reuse period, so it suppresses speculative
// rewrites.
func (rp *reductionPass) decideBoundaryFlush() {
	rp.applyBoundary = true
	if !rp.incrementalEnabled || rp.frozenBoundary <= 0 {
		return
	}
	pendingSaved := 0
	earliestBoundary := -1
	for _, p := range rp.proposals {
		if p.index >= rp.frozenBoundary {
			continue
		}
		if earliestBoundary < 0 {
			earliestBoundary = p.index
		}
		saved := estimateMessageTokens(rp.a.ctxMgr, message.Message{Content: rp.prepared[p.index].Content}) -
			estimateMessageTokens(rp.a.ctxMgr, message.Message{Content: p.reduced})
		if saved > 0 {
			pendingSaved += saved
		}
	}
	if earliestBoundary < 0 {
		return
	}
	cacheInvalidAnyway := rp.modelSnapshot.ProjectedModelRunLength <= 1
	// New messages were not cached: do not charge their ordinary input
	// cost as a penalty for rewriting the previously sent prefix.
	tailTokens := estimateMessagesTokens(rp.a.ctxMgr, rp.prepared[earliestBoundary:rp.frozenBoundary])
	horizon := rp.a.reductionFlushHorizon()
	billableInputTokens := estimateMessagesTokens(rp.a.ctxMgr, rp.prepared) + rp.a.estimateFixedRequestTokens()
	penaltyRatio := rp.a.boundaryFlushPenaltyRatio(rp.modelSnapshot, billableInputTokens)
	amortized := horizon > 0 && float64(pendingSaved)*float64(horizon) >= penaltyRatio*float64(tailTokens)
	rp.applyBoundary = cacheInvalidAnyway || amortized
}

// applyReductionProposals is pass 2. Deferred boundary proposals keep their
// original content so the previously sent bytes stay cache-stable; they will
// be re-proposed on later requests until a flush condition holds.
func (rp *reductionPass) applyReductionProposals() {
	for _, p := range rp.proposals {
		if !p.force && !rp.applyBoundary && rp.incrementalEnabled && p.index < rp.frozenBoundary {
			rp.noteSkip(contextReductionSkipDeferredCache)
			// The message keeps its original bytes this request, so the
			// retention ledger must report it as full rather than as the
			// reduction that was only proposed.
			rp.noteRetention(requesttrim.Decision{Level: requesttrim.RetentionFull, Complete: true, Reason: contextReductionSkipDeferredCache})
			continue
		}
		if !p.repeated && p.recallable && rp.incrementalEnabled && p.index < rp.frozenBoundary {
			meta := rp.callMeta[rp.prepared[p.index].ToolCallID]
			recordDiscardedInputEvidence(rp.discardedInputs, rp.a.reductionMemo.ToolInputKey(rp.prepared[p.index].ToolCallID, meta), rp.prepared[p.index].ToolCallID)
		}
		original := rp.prepared[p.index].Content
		rp.prepared[p.index].Content = p.reduced
		if p.class != requesttrim.Diagnostics {
			rp.prepared[p.index].ToolDiff = ""
		}
		rp.noteReduction(p.toolName, p.rule, original, rp.prepared[p.index].Content)
		rp.noteRetention(p.decision)
	}
}

// finalize folds the applied surface into the durable stats and prepared
// request: evidence and archive readback numbers, the wrap-up grace preserve
// path, the below-threshold stable reuse, and the rememberPrepared
// bookkeeping. It returns the surface the request should use.
func (rp *reductionPass) finalize() []message.Message {
	a := rp.a
	if a == nil {
		return rp.prepared
	}
	rp.stats.EvidenceRebuildDurationUS = rp.evidenceStats.DurationUS
	rp.stats.EvidenceFiles = rp.evidenceStats.Files
	rp.stats.EvidenceObservations = rp.evidenceStats.Observations
	rp.stats.EvidenceCurrent = rp.evidenceStats.Current
	rp.stats.EvidenceStale = rp.evidenceStats.Stale
	rp.stats.EvidenceSuperseded = rp.evidenceStats.Superseded
	rp.stats.ArchiveReads, rp.stats.ArchiveReadFailures = requesttrim.ArtifactReadbackStats(rp.prepared, rp.callMeta, a.sessionDir)
	rp.stats.TokensAfter = estimateMessagesTokens(a.ctxMgr, rp.prepared)
	a.setCurrentRequestSurface(&rp.stats, rp.prepared)
	if rp.stats.TokensSaved == 0 && rp.stats.TokensBefore > rp.stats.TokensAfter {
		rp.stats.TokensSaved = rp.stats.TokensBefore - rp.stats.TokensAfter
	}
	if !rp.semanticRefresh && rp.wrapUpGraceActive && rp.stats.TokensSaved < rp.policy.MinIncrementalTokens {
		// The grace suppresses *new* low-gain reductions; it must not undo
		// the ones already frozen. Returning the raw messages here would
		// restore tool output the model was already told was omitted,
		// rewrite the cached prefix, and grow the context the wrap-up
		// request was meant to leave alone. Keep the established reduced
		// prefix and only leave the fresh tail verbatim.
		// With no established reduced prefix there is nothing the model was
		// told was omitted, so the raw messages are the correct verbatim
		// surface.
		surface := rp.messages
		surfaceReduced := false
		prefixLen := 0
		var reusedFrom stableReductionSurface
		if previous, ok := a.stableReductionSurfaceCandidate(a.currentTurnID()); ok &&
			hasReductionSavings(previous.Stats) && len(previous.Messages) > 0 && len(rp.messages) >= len(previous.Messages) {
			if reused, compatible := reuseStableReductionPrefix(previous, rp.prepared, rp.messages); compatible {
				surface, prefixLen, reusedFrom = reused, len(previous.Messages), previous
			} else {
				// A reduced prefix is established but cannot be reused as
				// it stands. Raw messages would resurrect the very output
				// that prefix already reported as omitted, so fall back to
				// this request's prepared surface instead.
				surface, surfaceReduced = rp.prepared, true
			}
		}
		preserved := ContextReductionStats{
			TokensBefore:  rp.stats.TokensBefore,
			TokensAfter:   rp.stats.TokensBefore,
			Protected:     true,
			ProtectReason: contextProtectReasonWrapUpGrace,
		}
		if surfaceReduced {
			preserved.TokensAfter = rp.stats.TokensAfter
		}
		if prefixLen > 0 {
			preserved = highLevelContextReductionStats(a.ctxMgr, rp.messages, surface)
			if len(preserved.ByToolAndRule) == 0 {
				preserved.ByToolAndRule = cloneContextReductionBuckets(reusedFrom.Stats.ByToolAndRule)
			}
			preserved.Protected = true
			preserved.ProtectReason = contextProtectReasonWrapUpGrace
			preserved.ReusedStable = true
		}
		preserved.fillModelContinuity(rp.modelSnapshot)
		a.setCurrentRequestSurface(&preserved, surface)
		a.setContextReductionStats(preserved)
		if rp.rememberPrepared {
			a.setPreparedStablePrefixLen(prefixLen)
			if prefixLen > 0 {
				a.rememberPreparedLLMRequest(a.currentTurnID(), rp.messages, surface, nil, reusedFrom.NextReviewAge, reusedFrom.ToolResults, reusedFrom.Policy)
			} else {
				a.rememberPreparedLLMRequest(a.currentTurnID(), rp.messages, surface, nil, rp.nextReviewAge, rp.toolResults, rp.policy)
			}
		}
		return surface
	}
	if rp.rememberPrepared && !rp.semanticRefresh {
		if previous, ok := a.stableReductionSurfaceCandidate(a.currentTurnID()); ok &&
			!stableReductionSurfaceNeedsReview(previous, rp.scan, rp.currentBatch, rp.externalReadInvalidated) {
			reuseReason, savedDelta := reuseStableReductionSurfaceReason(rp.policy, rp.stats, previous.Stats)
			rp.stats.ReuseReason = reuseReason
			rp.stats.SavedDelta = savedDelta
			if reuseReason == contextReuseReasonBelowIncrementalMin {
				reused, compatible := reuseStableReductionPrefix(previous, rp.prepared, rp.messages)
				if !compatible {
					rp.stats.ReuseReason = ""
					rp.stats.SavedDelta = 0
				} else {
					rp.prepared = reused
					a.setPreparedStablePrefixLen(len(previous.Messages))
					reusedStats := highLevelContextReductionStats(a.ctxMgr, rp.messages, rp.prepared)
					rp.stats.Messages = reusedStats.Messages
					rp.stats.Bytes = reusedStats.Bytes
					rp.stats.TokensBefore = reusedStats.TokensBefore
					rp.stats.TokensAfter = reusedStats.TokensAfter
					rp.stats.TokensSaved = reusedStats.TokensSaved
					rp.stats.ByToolAndRule = reusedStats.ByToolAndRule
					rp.stats.ReusedStable = true
					a.setCurrentRequestSurface(&rp.stats, rp.prepared)
					if len(rp.stats.ByToolAndRule) == 0 {
						rp.stats.ByToolAndRule = cloneContextReductionBuckets(previous.Stats.ByToolAndRule)
					}
				}
			}
		}
	}
	rp.stats.setCurrentReductionSavings(rp.messages, rp.prepared)
	a.fillReductionModelContinuity(&rp.stats)
	a.setContextReductionStats(rp.stats)
	if rp.rememberPrepared {
		a.rememberPreparedLLMRequest(a.currentTurnID(), rp.messages, rp.prepared, rp.discardedInputs, rp.nextReviewAge, rp.toolResults, rp.policy)
	}
	return rp.prepared
}

func (a *MainAgent) currentRequestBatch(messages []message.Message) uint64 {
	if a != nil {
		if batch := a.requestBatches.current(a.sessionEpoch); batch > 0 {
			return batch
		}
	}
	if messages == nil && a != nil && a.ctxMgr != nil {
		messages = a.ctxMgr.Snapshot()
	}
	return maxRequestBatch(messages)
}

func countToolResults(messages []message.Message) int {
	total := 0
	for _, msg := range messages {
		if msg.Role == message.RoleTool {
			total++
		}
	}
	return total
}

// diagnosticsSupersededFlags reports, per tool-result index, whether a newer
// edit-like result with a diagnostics section appears later in the
// conversation. The later output carries the fresher LSP state, so the older
// block collapses to a single representative line instead of a full list
// (mirroring the read superseded semantics in readValidity).
func diagnosticsSupersededFlags(messages []message.Message, callMeta map[string]requesttrim.ToolCall) map[int]bool {
	latest := -1
	for i := range messages {
		msg := &messages[i]
		if msg.Role != message.RoleTool {
			continue
		}
		switch toolname.Normalize(callMeta[msg.ToolCallID].Name) {
		case tools.NameEdit, tools.NameApplyPatch, tools.NameWrite:
			if strings.Contains(msg.Content, requesttrim.DiagnosticsSectionLabel) {
				latest = i
			}
		}
	}
	if latest <= 0 {
		return nil
	}
	out := make(map[int]bool)
	for i := range latest {
		msg := &messages[i]
		if msg.Role != message.RoleTool {
			continue
		}
		switch toolname.Normalize(callMeta[msg.ToolCallID].Name) {
		case tools.NameEdit, tools.NameApplyPatch, tools.NameWrite:
			if strings.Contains(msg.Content, requesttrim.DiagnosticsSectionLabel) {
				out[i] = true
			}
		}
	}
	return out
}
