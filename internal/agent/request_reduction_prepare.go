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
	wrapUpGraceActive := false
	var modelSnapshot llmModelContinuitySnapshot
	if a != nil {
		modelSnapshot = a.llmModelContinuitySnapshot()
		if modelSnapshot.ProjectedModelRunLength > 1 && !a.hasQueuedUserInputForRecovery() && a.consumeContextReductionWrapUpGrace(a.currentTurnID()) {
			wrapUpGraceActive = true
			if previous, ok := a.stableReductionSurfaceCandidate(a.currentTurnID()); ok && hasReductionSavings(previous.Stats) && len(previous.Messages) > 0 && len(messages) >= len(previous.Messages) {
				if stableReductionSurfaceNeedsReview(previous, scan, currentBatch, externalReadInvalidated) {
					wrapUpGraceActive = false
				} else {
					owned := append([]message.Message(nil), messages...)
					reused, compatible := reuseStableReductionPrefix(previous, owned, messages)
					if compatible {
						stats := highLevelContextReductionStats(a.ctxMgr, messages, reused)
						if len(stats.ByToolAndRule) == 0 {
							stats.ByToolAndRule = cloneContextReductionBuckets(previous.Stats.ByToolAndRule)
						}
						stats.Protected = true
						stats.ProtectReason = contextProtectReasonWrapUpGrace
						stats.ReusedStable = true
						stats.fillModelContinuity(modelSnapshot)
						a.setCurrentRequestSurface(&stats, reused)
						a.setContextReductionStats(stats)
						if rememberPrepared {
							a.setPreparedStablePrefixLen(len(previous.Messages))
							a.rememberPreparedLLMRequest(a.currentTurnID(), messages, reused, nil, previous.NextReviewAge, previous.ToolResults, previous.Policy)
						}
						return reused
					}
				}
			}
		}
	}
	if a != nil {
		if rememberPrepared {
			if reused, stats, ok := a.tryReuseStableReductionSurfaceBeforeFullScan(messages, policy, scan, currentBatch, externalReadInvalidated); ok {
				a.fillReductionModelContinuity(&stats)
				a.setCurrentRequestSurface(&stats, reused)
				a.setContextReductionStats(stats)
				if n, ok := a.stableReductionSurfacePrefixLen(a.currentTurnID()); ok {
					a.setPreparedStablePrefixLen(n)
				}
				previous, _ := a.stableReductionSurfaceCandidate(a.currentTurnID())
				a.rememberPreparedLLMRequest(a.currentTurnID(), messages, reused, nil, previous.NextReviewAge, previous.ToolResults, previous.Policy)
				return reused
			}
		}
	}
	prepared := append([]message.Message(nil), messages...)
	stats := ContextReductionStats{TokensBefore: estimateMessagesTokens(a.ctxMgr, prepared)}

	// Incremental reduction: freeze already-reduced tool results from the
	// previous stable surface so their marker content stays cache-stable.
	// Only the not-yet-reduced prefix and the new tail are re-examined.
	// Tool-definition changes invalidate the frozen surface (full re-reduction).
	frozenPrefix, frozenReducedIndices, frozenNextReviewAge, previousToolResults, frozenBoundary, incrementalEnabled := a.incrementalReductionSurface(prepared)
	if incrementalEnabled && frozenBoundary > 0 {
		for i := range frozenBoundary {
			if frozenReducedIndices != nil && i < len(frozenReducedIndices) && frozenReducedIndices[i] {
				prepared[i] = frozenPrefix[i].Clone()
			}
		}
	}
	noteReduction := func(toolName, rule, original, reduced string) {
		saved := len(original) - len(reduced)
		if saved <= 0 {
			return
		}
		stats.Messages++
		stats.Bytes += saved
		beforeTokens := estimateMessageTokens(a.ctxMgr, message.Message{Content: original})
		afterTokens := estimateMessageTokens(a.ctxMgr, message.Message{Content: reduced})
		tokensSaved := beforeTokens - afterTokens
		if tokensSaved > 0 {
			stats.TokensSaved += tokensSaved
		}
		if stats.ByToolAndRule == nil {
			stats.ByToolAndRule = make(map[string]ContextReductionBucket)
		}
		key := requesttrim.ToolNameOrUnknown(toolName) + "/" + rule
		bucket := stats.ByToolAndRule[key]
		bucket.Messages++
		bucket.Bytes += saved
		if tokensSaved > 0 {
			bucket.TokensSaved += tokensSaved
		}
		stats.ByToolAndRule[key] = bucket
	}
	noteSkip := func(reason string) {
		if reason == "" {
			return
		}
		if stats.SkippedByReason == nil {
			stats.SkippedByReason = make(map[string]int)
		}
		stats.SkippedByReason[reason]++
	}
	decisionFor := func(ctx requesttrim.Context, verdict requesttrim.Verdict, rule, reduced string) requesttrim.Decision {
		return requesttrim.RetentionDecision(ctx, verdict, rule, reduced)
	}
	noteRetention := func(decision requesttrim.Decision) {
		if decision.Level == "" {
			return
		}
		if stats.ByRetentionLevel == nil {
			stats.ByRetentionLevel = make(map[string]int)
		}
		stats.ByRetentionLevel[string(decision.Level)]++
		if !decision.Recoverable() {
			stats.UnrecoverableReductions++
		}
	}
	noteOverCompression := func(kind, toolName string) {
		if kind == "" {
			return
		}
		if stats.OverCompression == nil {
			stats.OverCompression = make(map[string]int)
		}
		stats.OverCompression[kind]++
		// Per-tool breakdown alongside the aggregate. The aggregate answers how
		// often reduction went too far; deciding which shape should compress
		// less needs to know whose summary provoked the re-fetch. Tool name is
		// the discriminator available here: the re-fetched output is still too
		// young to have been classified, so the summary shape that caused this
		// is only reachable through the tool that produced it.
		if stats.OverCompressionByTool == nil {
			stats.OverCompressionByTool = make(map[string]int)
		}
		stats.OverCompressionByTool[kind+"/"+requesttrim.ToolNameOrUnknown(toolName)]++
	}

	// callMeta and repeated are computed over the original messages but are
	// equally valid for prepared: assistant messages (the only inputs they
	// read) are byte-identical between the two. Read validity is re-analyzed
	// over prepared because frozen-reduced tool results carry marker content.
	callMeta := scan.CallMeta()
	requestAge := requesttrim.RequestBatchesAfter(prepared, currentBatch)
	repeated := scan.RepeatedOutputs()
	toolResults := countToolResults(prepared)
	nextReviewAge := make([]int, len(prepared))
	if incrementalEnabled && len(frozenNextReviewAge) > 0 {
		copy(nextReviewAge, frozenNextReviewAge)
	}
	toolResultThresholdCrossed := incrementalEnabled && previousToolResults < policy.MinToolResultsPrune && toolResults >= policy.MinToolResultsPrune
	evidenceStarted := time.Now()
	evidence := requesttrim.BuildFileEvidence(prepared, callMeta)
	evidenceStats := evidence.Stats(time.Since(evidenceStarted))
	readValidityByIndex := evidence.ValidityByMessage()
	diagnosticsSuperseded := diagnosticsSupersededFlags(prepared, callMeta)
	if len(externalReadInvalidated) > 0 && readValidityByIndex == nil {
		readValidityByIndex = make(map[int]requesttrim.ReadValidity, len(externalReadInvalidated))
	}
	for index := range externalReadInvalidated {
		validity := readValidityByIndex[index]
		validity.Invalidated = true
		validity.Superseded = false
		// Neither source of external invalidation leaves the replaced bytes in
		// the transcript: a mutating shell command reports its own output, not
		// the file it rewrote, and an out-of-band edit is invisible until the
		// stat check notices it. The read output is the only record left.
		validity.PriorContentLost = true
		readValidityByIndex[index] = validity
	}
	// discardedInputs is the recall-protection evidence base: input key ->
	// ToolCallID of the call whose output was actually summarized away on this
	// or an earlier request. Repeated-collapse never registers — it always
	// leaves a fresher full copy in context, so a re-issue after it is model
	// redundancy, not proof that reduction dropped needed content. The
	// ToolCallID distinguishes a genuine re-issue (same input, different call)
	// from the discarded message itself being re-evaluated after a surface
	// invalidation.
	discardedInputs := a.lastPreparedDiscardedInputsSnapshot()
	discardedReadRevisions := make(map[string]string)
	// recalledInputs carries the session's recall-protection set into this pass;
	// registrations during the pass update both the local view (so later
	// messages in the same pass see them) and the durable per-agent set.
	recalledInputs := a.recalledReductionInputsSnapshot()
	noteRecalledInput := func(key string) {
		if key == "" {
			return
		}
		if recalledInputs == nil {
			recalledInputs = make(map[string]struct{})
		}
		recalledInputs[key] = struct{}{}
		a.noteRecalledReductionInput(key)
	}

	// Pass 1: collect reduction proposals without mutating anything. Proposals
	// inside the frozen boundary rewrite bytes the provider already cached, so
	// they are only applied together ("batched") when a flush is justified;
	// proposals in the new tail were never sent and are always free to apply.
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
	var proposals []reductionProposal
	semanticRefresh := false
	// Recover read revisions for discarded reads still present in the frozen
	// prefix so the reread-same-revision over-compression split keeps working
	// across requests. Key membership itself travels via DiscardedInputs: the
	// frozen indices alone cannot distinguish a summarized output from a
	// repeated-collapse, which must not count as discarded.
	for i := range prepared {
		if incrementalEnabled && frozenReducedIndices != nil && i < len(frozenReducedIndices) && frozenReducedIndices[i] && prepared[i].Role == message.RoleTool {
			meta := callMeta[prepared[i].ToolCallID]
			toolName := toolname.Normalize(meta.Name)
			key := a.reductionMemo.ToolInputKey(prepared[i].ToolCallID, meta)
			if _, discarded := discardedInputs[key]; !discarded {
				continue
			}
			if toolName == tools.NameRead {
				if revision := reductionReadRevision(&meta, prepared[i].FileState); revision != "" {
					discardedReadRevisions[key] = revision
				}
			}
		}
	}
	for i := range prepared {
		if prepared[i].Role != message.RoleTool {
			continue
		}
		// Skip already-reduced frozen prefix messages: their content is a stable
		// marker copied from the previous surface. Re-reducing could produce a
		// different marker (e.g. repeated-call detection) and break cache reuse.
		meta := callMeta[prepared[i].ToolCallID]
		toolName := toolname.Normalize(meta.Name)
		age := requestAge[i]
		validity := readValidityByIndex[i]
		if incrementalEnabled && frozenReducedIndices != nil && i < len(frozenReducedIndices) && frozenReducedIndices[i] {
			if toolName != tools.NameRead || (!validity.Invalidated && !validity.Superseded) {
				noteSkip(contextReductionSkipFrozenReduced)
				continue
			}
			// The frozen marker already carries the stale/superseded guidance,
			// so re-rendering it would rewrite cached bytes without changing
			// what the model can act on.
			if readMarkerCarriesValidityGuidance(prepared[i].Content) {
				noteSkip(contextReductionSkipFrozenReduced)
				continue
			}
			ctx := requesttrim.Context{
				ToolName:             toolName,
				ToolCallID:           prepared[i].ToolCallID,
				Memo:                 &a.reductionMemo,
				Meta:                 meta,
				Content:              messages[i].Content,
				ToolStatus:           messages[i].ToolStatus,
				FileState:            messages[i].FileState,
				Age:                  age,
				Policy:               policy,
				ToolResults:          toolResults,
				ReadInvalidated:      validity.Invalidated,
				ReadSuperseded:       validity.Superseded,
				ReadPriorContentLost: validity.PriorContentLost,
				ArchiveDir:           a.sessionDir,
			}
			reduced, rule, ok := requesttrim.Render(requesttrim.ReadLike, ctx)
			if ok {
				proposals = append(proposals, reductionProposal{
					index:      i,
					class:      requesttrim.ReadLike,
					toolName:   toolName,
					rule:       rule,
					reduced:    reduced,
					decision:   decisionFor(ctx, requesttrim.ReducedVerdict(requesttrim.ReadLike), rule, reduced),
					force:      true,
					recallable: true,
					repeated:   repeated[i],
				})
				semanticRefresh = true
			}
			continue
		}
		if externalReadInvalidated[i] && toolName == tools.NameRead {
			ctx := requesttrim.Context{
				ToolName:    toolName,
				ToolCallID:  prepared[i].ToolCallID,
				Memo:        &a.reductionMemo,
				Meta:        meta,
				Content:     messages[i].Content,
				ToolStatus:  messages[i].ToolStatus,
				FileState:   messages[i].FileState,
				Age:         age,
				Policy:      policy,
				ToolResults: toolResults,
				// A mutating shell command or an out-of-band process replaced the
				// file: nothing in the transcript holds the bytes this read saw,
				// so the summary must carry an archive address.
				ReadInvalidated:      true,
				ReadPriorContentLost: true,
				ArchiveDir:           a.sessionDir,
			}
			if reduced, rule, ok := requesttrim.Render(requesttrim.ReadLike, ctx); ok {
				proposals = append(proposals, reductionProposal{
					index:      i,
					class:      requesttrim.ReadLike,
					toolName:   toolName,
					rule:       rule,
					reduced:    reduced,
					decision:   decisionFor(ctx, requesttrim.ReducedVerdict(requesttrim.ReadLike), rule, reduced),
					recallable: true,
					repeated:   repeated[i],
				})
				semanticRefresh = true
			}
			continue
		}
		if incrementalEnabled && i < frozenBoundary && toolName != tools.NameRead && !repeated[i] && !toolResultThresholdCrossed &&
			i < len(frozenNextReviewAge) && frozenNextReviewAge[i] > age {
			noteSkip(contextReductionSkipDeferredReview)
			continue
		}
		ctx := requesttrim.Context{
			ToolName:              toolName,
			ToolCallID:            prepared[i].ToolCallID,
			Memo:                  &a.reductionMemo,
			Meta:                  meta,
			Content:               prepared[i].Content,
			ToolStatus:            prepared[i].ToolStatus,
			FileState:             prepared[i].FileState,
			Age:                   age,
			Policy:                policy,
			Repeated:              repeated[i],
			ToolResults:           toolResults,
			ShellReadOnly:         toolName == tools.NameShell && a.shellCommandReadOnly(prepared[i].ToolCallID, meta.Args),
			ReadInvalidated:       validity.Invalidated,
			ReadSuperseded:        validity.Superseded,
			ReadPriorContentLost:  validity.PriorContentLost,
			DiagnosticsSuperseded: diagnosticsSuperseded[i],
			ArchiveDir:            a.sessionDir,
		}
		inputKey := a.reductionMemo.ToolInputKey(prepared[i].ToolCallID, meta)
		// Recall protection applies to content-fetch shapes only (reads, web
		// fetches, searches, read-only shell): re-running a mutating command
		// seeks fresh state, not lost content. Older duplicates keep collapsing
		// to repeated markers, and a read known to be stale keeps its stale
		// marker — that guidance outweighs retention.
		staleRead := toolName == tools.NameRead && (validity.Invalidated || validity.Superseded)
		contentFetch := !repeated[i] && !staleRead &&
			(requesttrim.IsReadLike(toolName) || ctx.ShellReadOnly || (toolName != tools.NameShell && requesttrim.LooksLikeSearchResult(ctx)))
		if contentFetch {
			if _, recalled := recalledInputs[inputKey]; recalled {
				noteSkip(contextReductionSkipRecalledInput)
				nextReviewAge[i] = 0
				continue
			}
		}
		verdict := requesttrim.Classify(ctx)
		class := verdict.Class
		if class == requesttrim.None {
			noteRetention(decisionFor(ctx, verdict, "", ""))
			if ctx.ReadRetentionProtects() {
				stats.ProtectedReadTokens += estimateMessageTokens(a.ctxMgr, message.Message{Content: prepared[i].Content})
			}
			nextReviewAge[i] = requesttrim.NextReviewAge(ctx)
			// The classification already decided which protection fired; asking
			// isHighRiskToolOutput again would re-scan the whole payload.
			if verdict.Reason == requesttrim.RetentionReasonRecentHighRisk {
				noteSkip(contextReductionSkipRecentHighRisk)
			} else if len(prepared[i].Content) > policy.StaleOutputBytes {
				noteSkip(contextReductionSkipLargeUnreduced)
			}
			if discardedID, discardedBefore := discardedInputs[inputKey]; discardedBefore && discardedID != prepared[i].ToolCallID {
				if contentFetch {
					noteRecalledInput(inputKey)
				}
				if requesttrim.IsReadLike(toolName) {
					noteOverCompression(contextReductionOverCompressionReread, toolName)
					if toolName == tools.NameRead {
						previousRevision := discardedReadRevisions[inputKey]
						currentRevision := reductionReadRevision(&meta, prepared[i].FileState)
						if previousRevision != "" && currentRevision != "" {
							if previousRevision == currentRevision {
								noteOverCompression(contextReductionOverCompressionRereadSameRevision, toolName)
							} else {
								noteOverCompression(contextReductionOverCompressionRereadChangedRevision, toolName)
							}
						}
					}
				} else if requesttrim.LooksLikeSearchResult(ctx) {
					noteOverCompression(contextReductionOverCompressionResearch, toolName)
				}
			}
			continue
		}
		// A second live copy of a content-fetch input whose earlier output was
		// genuinely discarded is about to be reduced too: the model re-fetched
		// content that reduction had dropped. Keep the newest copy instead and
		// remember the input for the rest of the session.
		if contentFetch {
			if discardedID, dup := discardedInputs[inputKey]; dup && discardedID != prepared[i].ToolCallID {
				noteRecalledInput(inputKey)
				noteSkip(contextReductionSkipRecalledInput)
				nextReviewAge[i] = 0
				continue
			}
		}
		nextReviewAge[i] = 0
		reduced, rule, ok := requesttrim.Render(class, ctx)
		if !ok {
			continue
		}
		decision := decisionFor(ctx, verdict, rule, reduced)
		// discardedInputs is consumed only by recall protection (content-fetch
		// shapes) and over-compression stats (read-like or search shapes).
		// Keys outside those shapes — mutating shells, edit/apply_patch
		// diagnostics — can never be read back, and their keys embed the full
		// original args (a whole patch for apply_patch), so registering them
		// only grows the map and every per-request clone of it.
		recallable := contentFetch || requesttrim.IsReadLike(toolName) ||
			(toolName != tools.NameShell && requesttrim.LooksLikeSearchResult(ctx))
		proposals = append(proposals, reductionProposal{
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
		if !ctx.Repeated && recallable && (!incrementalEnabled || i >= frozenBoundary) {
			recordDiscardedInputEvidence(discardedInputs, inputKey, prepared[i].ToolCallID)
			if toolName == tools.NameRead {
				if revision := reductionReadRevision(&meta, prepared[i].FileState); revision != "" {
					discardedReadRevisions[inputKey] = revision
				}
			}
		}
	}

	// Decide whether boundary proposals are applied this request. Rewriting the
	// cached prefix at position p re-bills everything after p at cache-write
	// price instead of cache-read price, while the reduction saves its tokens on
	// every subsequent request. Flush when the cache is invalid anyway (first
	// request on this model ref) or when the pending savings amortize the
	// rewrite within the bounded policy horizon. A queued checkpoint ends the
	// current prefix's reuse period, so it suppresses speculative rewrites.
	applyBoundary := true
	if incrementalEnabled && frozenBoundary > 0 {
		pendingSaved := 0
		earliestBoundary := -1
		for _, p := range proposals {
			if p.index >= frozenBoundary {
				continue
			}
			if earliestBoundary < 0 {
				earliestBoundary = p.index
			}
			saved := estimateMessageTokens(a.ctxMgr, message.Message{Content: prepared[p.index].Content}) -
				estimateMessageTokens(a.ctxMgr, message.Message{Content: p.reduced})
			if saved > 0 {
				pendingSaved += saved
			}
		}
		if earliestBoundary >= 0 {
			cacheInvalidAnyway := modelSnapshot.ProjectedModelRunLength <= 1
			// New messages were not cached: do not charge their ordinary input
			// cost as a penalty for rewriting the previously sent prefix.
			tailTokens := estimateMessagesTokens(a.ctxMgr, prepared[earliestBoundary:frozenBoundary])
			horizon := a.reductionFlushHorizon()
			billableInputTokens := estimateMessagesTokens(a.ctxMgr, prepared) + a.estimateFixedRequestTokens()
			penaltyRatio := a.boundaryFlushPenaltyRatio(modelSnapshot, billableInputTokens)
			amortized := horizon > 0 && float64(pendingSaved)*float64(horizon) >= penaltyRatio*float64(tailTokens)
			applyBoundary = cacheInvalidAnyway || amortized
		}
	}

	// Pass 2: apply. Deferred boundary proposals keep their original content so
	// the previously sent bytes stay cache-stable; they will be re-proposed on
	// later requests until a flush condition holds.
	for _, p := range proposals {
		if !p.force && !applyBoundary && incrementalEnabled && p.index < frozenBoundary {
			noteSkip(contextReductionSkipDeferredCache)
			// The message keeps its original bytes this request, so the
			// retention ledger must report it as full rather than as the
			// reduction that was only proposed.
			noteRetention(requesttrim.Decision{Level: requesttrim.RetentionFull, Complete: true, Reason: contextReductionSkipDeferredCache})
			continue
		}
		if !p.repeated && p.recallable && incrementalEnabled && p.index < frozenBoundary {
			meta := callMeta[prepared[p.index].ToolCallID]
			recordDiscardedInputEvidence(discardedInputs, a.reductionMemo.ToolInputKey(prepared[p.index].ToolCallID, meta), prepared[p.index].ToolCallID)
		}
		original := prepared[p.index].Content
		prepared[p.index].Content = p.reduced
		if p.class != requesttrim.Diagnostics {
			prepared[p.index].ToolDiff = ""
		}
		noteReduction(p.toolName, p.rule, original, prepared[p.index].Content)
		noteRetention(p.decision)
	}

	if a != nil {
		stats.EvidenceRebuildDurationUS = evidenceStats.DurationUS
		stats.EvidenceFiles = evidenceStats.Files
		stats.EvidenceObservations = evidenceStats.Observations
		stats.EvidenceCurrent = evidenceStats.Current
		stats.EvidenceStale = evidenceStats.Stale
		stats.EvidenceSuperseded = evidenceStats.Superseded
		stats.ArchiveReads, stats.ArchiveReadFailures = requesttrim.ArtifactReadbackStats(prepared, callMeta, a.sessionDir)
		stats.TokensAfter = estimateMessagesTokens(a.ctxMgr, prepared)
		a.setCurrentRequestSurface(&stats, prepared)
		if stats.TokensSaved == 0 && stats.TokensBefore > stats.TokensAfter {
			stats.TokensSaved = stats.TokensBefore - stats.TokensAfter
		}
		if !semanticRefresh && wrapUpGraceActive && stats.TokensSaved < policy.MinIncrementalTokens {
			// The grace suppresses *new* low-gain reductions; it must not undo
			// the ones already frozen. Returning the raw messages here would
			// restore tool output the model was already told was omitted,
			// rewrite the cached prefix, and grow the context the wrap-up
			// request was meant to leave alone. Keep the established reduced
			// prefix and only leave the fresh tail verbatim.
			// With no established reduced prefix there is nothing the model was
			// told was omitted, so the raw messages are the correct verbatim
			// surface.
			surface := messages
			surfaceReduced := false
			prefixLen := 0
			var reusedFrom stableReductionSurface
			if previous, ok := a.stableReductionSurfaceCandidate(a.currentTurnID()); ok &&
				hasReductionSavings(previous.Stats) && len(previous.Messages) > 0 && len(messages) >= len(previous.Messages) {
				if reused, compatible := reuseStableReductionPrefix(previous, prepared, messages); compatible {
					surface, prefixLen, reusedFrom = reused, len(previous.Messages), previous
				} else {
					// A reduced prefix is established but cannot be reused as
					// it stands. Raw messages would resurrect the very output
					// that prefix already reported as omitted, so fall back to
					// this request's prepared surface instead.
					surface, surfaceReduced = prepared, true
				}
			}
			preserved := ContextReductionStats{
				TokensBefore:  stats.TokensBefore,
				TokensAfter:   stats.TokensBefore,
				Protected:     true,
				ProtectReason: contextProtectReasonWrapUpGrace,
			}
			if surfaceReduced {
				preserved.TokensAfter = stats.TokensAfter
			}
			if prefixLen > 0 {
				preserved = highLevelContextReductionStats(a.ctxMgr, messages, surface)
				if len(preserved.ByToolAndRule) == 0 {
					preserved.ByToolAndRule = cloneContextReductionBuckets(reusedFrom.Stats.ByToolAndRule)
				}
				preserved.Protected = true
				preserved.ProtectReason = contextProtectReasonWrapUpGrace
				preserved.ReusedStable = true
			}
			preserved.fillModelContinuity(modelSnapshot)
			a.setCurrentRequestSurface(&preserved, surface)
			a.setContextReductionStats(preserved)
			if rememberPrepared {
				a.setPreparedStablePrefixLen(prefixLen)
				if prefixLen > 0 {
					a.rememberPreparedLLMRequest(a.currentTurnID(), messages, surface, nil, reusedFrom.NextReviewAge, reusedFrom.ToolResults, reusedFrom.Policy)
				} else {
					a.rememberPreparedLLMRequest(a.currentTurnID(), messages, surface, nil, nextReviewAge, toolResults, policy)
				}
			}
			return surface
		}
		if rememberPrepared && !semanticRefresh {
			if previous, ok := a.stableReductionSurfaceCandidate(a.currentTurnID()); ok &&
				!stableReductionSurfaceNeedsReview(previous, scan, currentBatch, externalReadInvalidated) {
				reuseReason, savedDelta := reuseStableReductionSurfaceReason(policy, stats, previous.Stats)
				stats.ReuseReason = reuseReason
				stats.SavedDelta = savedDelta
				if reuseReason == contextReuseReasonBelowIncrementalMin {
					reused, compatible := reuseStableReductionPrefix(previous, prepared, messages)
					if !compatible {
						stats.ReuseReason = ""
						stats.SavedDelta = 0
					} else {
						prepared = reused
						a.setPreparedStablePrefixLen(len(previous.Messages))
						reusedStats := highLevelContextReductionStats(a.ctxMgr, messages, prepared)
						stats.Messages = reusedStats.Messages
						stats.Bytes = reusedStats.Bytes
						stats.TokensBefore = reusedStats.TokensBefore
						stats.TokensAfter = reusedStats.TokensAfter
						stats.TokensSaved = reusedStats.TokensSaved
						stats.ByToolAndRule = reusedStats.ByToolAndRule
						stats.ReusedStable = true
						a.setCurrentRequestSurface(&stats, prepared)
						if len(stats.ByToolAndRule) == 0 {
							stats.ByToolAndRule = cloneContextReductionBuckets(previous.Stats.ByToolAndRule)
						}
					}
				}
			}
		}
		stats.setCurrentReductionSavings(messages, prepared)
		a.fillReductionModelContinuity(&stats)
		a.setContextReductionStats(stats)
		if rememberPrepared {
			a.rememberPreparedLLMRequest(a.currentTurnID(), messages, prepared, discardedInputs, nextReviewAge, toolResults, policy)
		}
	}
	return prepared
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
