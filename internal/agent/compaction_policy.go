package agent

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/keakon/chord/internal/ctxmgr"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/toolname"
	"github.com/keakon/chord/internal/tools"
)

func evidencePackTokenBudget(contextLimit int) int {
	if contextLimit <= 0 {
		return CompactEvidenceMaxTokens
	}
	b := contextLimit * compactEvidencePercentNumer / compactEvidencePercentDenom
	b = max(b, compactEvidenceMinTokens)
	b = min(b, CompactEvidenceMaxTokens)
	return b
}

func splitMessagesForCompactionWithSelections(messages []message.Message, recentTail []message.Message, evidenceItems []evidenceItem) (head []message.Message, evidence []message.Message) {
	if len(messages) < 4 {
		return nil, nil
	}
	archiveEnd := len(messages)
	if len(recentTail) > 0 {
		archiveEnd = len(messages) - len(recentTail)
	}
	if archiveEnd <= 0 {
		return nil, nil
	}
	archiveHead := make([]message.Message, archiveEnd)
	copy(archiveHead, messages[:archiveEnd])
	if len(evidenceItems) == 0 {
		return archiveHead, nil
	}
	artifact := renderEvidenceArtifact(evidenceItems)
	return archiveHead, []message.Message{artifact}
}

func (a *MainAgent) prepareMessagesForLLM(messages []message.Message) []message.Message {
	return a.prepareMessagesForLLMWithOptions(messages, true)
}

// reductionHistoryScan lazily computes and caches the whole-history scans
// (tool-call metadata, repeated-output detection, read-validity analysis)
// shared by the surface-review checks and the main reduction pass of a single
// prepare call. Several review call sites can run per request; sharing one
// scan avoids re-walking the full history at each of them.
//
// The scans run over the original messages. callMeta and repeatedOutputs
// depend only on roles, tool-call IDs, and assistant tool calls, which are
// identical in the prepared copy, so those results are also valid for it.
// readValidity additionally parses tool-result content, so it is only valid
// for the original messages; the main pass computes its own validity over the
// prepared copy. Not safe for concurrent use.
type reductionHistoryScan struct {
	messages      []message.Message
	meta          map[string]toolCallMeta
	repeated      map[int]bool
	evidence      fileEvidenceView
	evidenceDone  bool
	evidenceStats fileEvidenceStats
}

func newReductionHistoryScan(messages []message.Message) *reductionHistoryScan {
	return &reductionHistoryScan{messages: messages}
}

func (s *reductionHistoryScan) callMeta() map[string]toolCallMeta {
	if s.meta == nil {
		s.meta = buildToolCallMeta(s.messages)
	}
	return s.meta
}

func (s *reductionHistoryScan) repeatedOutputs() map[int]bool {
	if s.repeated == nil {
		s.repeated = detectRepeatedToolOutputs(s.messages, s.callMeta())
	}
	return s.repeated
}

func (s *reductionHistoryScan) readValidity() map[int]readValidity {
	return s.fileEvidence().validityByMessage()
}

func (a *MainAgent) refreshVisibleContextReductionStats(messages []message.Message) {
	if a == nil {
		return
	}
	a.clearReductionCache(true)
	_ = a.prepareMessagesForLLMWithOptions(messages, false)
}

func (a *MainAgent) prepareMessagesForLLMWithOptions(messages []message.Message, rememberPrepared bool) []message.Message {
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
	scan := newReductionHistoryScan(messages)
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
					reused, compatible := reuseStableReductionPrefix(previous, messages, messages)
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
				prepared[i] = cloneMessageForRequestShape(frozenPrefix[i])
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
		key := toolNameOrUnknown(toolName) + "/" + rule
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
	decisionFor := func(ctx requestReductionContext, verdict requestReductionVerdict, rule, reduced string) retentionDecision {
		return retentionDecisionFor(ctx, verdict, rule, reduced)
	}
	noteRetention := func(decision retentionDecision) {
		if decision.Level == "" {
			return
		}
		if stats.ByRetentionLevel == nil {
			stats.ByRetentionLevel = make(map[string]int)
		}
		stats.ByRetentionLevel[string(decision.Level)]++
		if !decision.retentionDecisionRecoverable() {
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
		stats.OverCompressionByTool[kind+"/"+toolNameOrUnknown(toolName)]++
	}

	// callMeta and repeated are computed over the original messages but are
	// equally valid for prepared: assistant messages (the only inputs they
	// read) are byte-identical between the two. Read validity is re-analyzed
	// over prepared because frozen-reduced tool results carry marker content.
	callMeta := scan.callMeta()
	requestAge := requestBatchesAfter(prepared, currentBatch)
	repeated := scan.repeatedOutputs()
	toolResults := countToolResults(prepared)
	nextReviewAge := make([]int, len(prepared))
	if incrementalEnabled && len(frozenNextReviewAge) > 0 {
		copy(nextReviewAge, frozenNextReviewAge)
	}
	toolResultThresholdCrossed := incrementalEnabled && previousToolResults < policy.MinToolResultsPrune && toolResults >= policy.MinToolResultsPrune
	evidenceStarted := time.Now()
	evidence := buildFileEvidenceViewWithMeta(prepared, callMeta)
	evidenceStats := evidence.stats(time.Since(evidenceStarted))
	readValidityByIndex := evidence.validityByMessage()
	diagnosticsSuperseded := diagnosticsSupersededFlags(prepared, callMeta)
	if len(externalReadInvalidated) > 0 && readValidityByIndex == nil {
		readValidityByIndex = make(map[int]readValidity, len(externalReadInvalidated))
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
		index      int
		class      requestReductionClass
		toolName   string
		rule       string
		reduced    string
		decision   retentionDecision
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
			key := contextReductionToolInputKey(toolName, meta.Args)
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
			ctx := requestReductionContext{
				ToolName:             toolName,
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
			reduced, rule, ok := reduceRequestToolOutput(requestReductionReadLike, ctx)
			if ok {
				proposals = append(proposals, reductionProposal{
					index:      i,
					class:      requestReductionReadLike,
					toolName:   toolName,
					rule:       rule,
					reduced:    reduced,
					decision:   decisionFor(ctx, reducedVerdict(requestReductionReadLike), rule, reduced),
					force:      true,
					recallable: true,
					repeated:   repeated[i],
				})
				semanticRefresh = true
			}
			continue
		}
		if externalReadInvalidated[i] && toolName == tools.NameRead {
			ctx := requestReductionContext{
				ToolName:    toolName,
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
			if reduced, rule, ok := reduceRequestToolOutput(requestReductionReadLike, ctx); ok {
				proposals = append(proposals, reductionProposal{
					index:      i,
					class:      requestReductionReadLike,
					toolName:   toolName,
					rule:       rule,
					reduced:    reduced,
					decision:   decisionFor(ctx, reducedVerdict(requestReductionReadLike), rule, reduced),
					force:      true,
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
		ctx := requestReductionContext{
			ToolName:              toolName,
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
		inputKey := contextReductionToolInputKey(toolName, meta.Args)
		// Recall protection applies to content-fetch shapes only (reads, web
		// fetches, searches, read-only shell): re-running a mutating command
		// seeks fresh state, not lost content. Older duplicates keep collapsing
		// to repeated markers, and a read known to be stale keeps its stale
		// marker — that guidance outweighs retention.
		staleRead := toolName == tools.NameRead && (validity.Invalidated || validity.Superseded)
		contentFetch := !repeated[i] && !staleRead &&
			(contextReductionIsReadLike(toolName) || ctx.ShellReadOnly || (toolName != tools.NameShell && looksLikeSearchResult(ctx)))
		if contentFetch {
			if _, recalled := recalledInputs[inputKey]; recalled {
				noteSkip(contextReductionSkipRecalledInput)
				nextReviewAge[i] = 0
				continue
			}
		}
		verdict := classifyRequestReduction(ctx)
		class := verdict.Class
		if class == requestReductionNone {
			noteRetention(decisionFor(ctx, verdict, "", ""))
			if ctx.readRetentionProtects() {
				stats.ProtectedReadTokens += estimateMessageTokens(a.ctxMgr, message.Message{Content: prepared[i].Content})
			}
			nextReviewAge[i] = nextContextReductionReviewAge(ctx)
			// The classification already decided which protection fired; asking
			// isHighRiskToolOutput again would re-scan the whole payload.
			if verdict.Reason == retentionReasonRecentHighRisk {
				noteSkip(contextReductionSkipRecentHighRisk)
			} else if len(prepared[i].Content) > policy.StaleOutputBytes {
				noteSkip(contextReductionSkipLargeUnreduced)
			}
			if discardedID, discardedBefore := discardedInputs[inputKey]; discardedBefore && discardedID != prepared[i].ToolCallID {
				if contentFetch {
					noteRecalledInput(inputKey)
				}
				if contextReductionIsReadLike(toolName) {
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
				} else if looksLikeSearchResult(ctx) {
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
		reduced, rule, ok := reduceRequestToolOutput(class, ctx)
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
		recallable := contentFetch || contextReductionIsReadLike(toolName) ||
			(toolName != tools.NameShell && looksLikeSearchResult(ctx))
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
	// cached prefix at position p re-bills everything after p at input price
	// (~10x the cache-read price), while the reduction saves its tokens on
	// every subsequent request. Flush when the cache is invalid anyway (first
	// request on this model ref) or when the pending
	// savings amortize the rewrite within a short horizon of future requests.
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
			tailTokens := estimateMessagesTokens(a.ctxMgr, prepared[earliestBoundary:])
			amortized := pendingSaved*reductionFlushHorizonRequests >= cacheMissPenaltyRatio*tailTokens
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
			noteRetention(retentionDecision{Level: retentionFull, Complete: true, Reason: contextReductionSkipDeferredCache})
			continue
		}
		if !p.repeated && p.recallable && incrementalEnabled && p.index < frozenBoundary {
			meta := callMeta[prepared[p.index].ToolCallID]
			recordDiscardedInputEvidence(discardedInputs, contextReductionToolInputKey(p.toolName, meta.Args), prepared[p.index].ToolCallID)
		}
		original := prepared[p.index].Content
		prepared[p.index].Content = p.reduced
		if p.class != requestReductionDiagnostics {
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
		stats.ArchiveReads, stats.ArchiveReadFailures = artifactReadbackStats(prepared, callMeta, a.sessionDir)
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
				reuseReason, savedDelta := policy.reuseStableReductionSurfaceReason(stats, previous.Stats)
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
func diagnosticsSupersededFlags(messages []message.Message, callMeta map[string]toolCallMeta) map[int]bool {
	latest := -1
	for i := range messages {
		msg := &messages[i]
		if msg.Role != message.RoleTool {
			continue
		}
		switch toolname.Normalize(callMeta[msg.ToolCallID].Name) {
		case tools.NameEdit, tools.NameApplyPatch, tools.NameWrite:
			if strings.Contains(msg.Content, diagnosticsSectionLabel) {
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
			if strings.Contains(msg.Content, diagnosticsSectionLabel) {
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
	return maxRequestBatch(messages)
}

func reductionReadRevision(meta *toolCallMeta, state *message.ToolFileState) string {
	if meta == nil || state == nil {
		return ""
	}
	request := meta.parsedReadRequest()
	if revision := firstReadHashForPath(state, request.Path); revision != "" {
		return revision
	}
	for _, read := range state.Reads {
		if read.Exists && strings.TrimSpace(read.SHA256) != "" {
			return strings.TrimSpace(read.SHA256)
		}
	}
	return ""
}

// shellReadOnlyClassMemo caches the read-only classification of shell calls
// by ToolCallID. A completed call's args never change and the read-only
// allowlist is static, so the verdict is immutable — while the reduction pass
// re-evaluates every shell result still in context on each LLM request.
type shellReadOnlyClassMemo struct {
	mu       sync.Mutex
	verdicts map[string]bool
}

// shellCommandReadOnly reports whether a shell call's command line is on the
// read-only allowlist, memoized per ToolCallID so the JSON args are parsed
// once per call instead of once per LLM request.
func (a *MainAgent) shellCommandReadOnly(toolCallID string, args string) bool {
	if a == nil {
		return false
	}
	toolCallID = strings.TrimSpace(toolCallID)
	if toolCallID == "" {
		return tools.ConcurrencyClassForTool(a.tools, tools.NameShell, json.RawMessage(args)) == tools.ToolConcurrencyClassReadOnly
	}
	a.shellReadOnlyClass.mu.Lock()
	if verdict, ok := a.shellReadOnlyClass.verdicts[toolCallID]; ok {
		a.shellReadOnlyClass.mu.Unlock()
		return verdict
	}
	a.shellReadOnlyClass.mu.Unlock()
	verdict := tools.ConcurrencyClassForTool(a.tools, tools.NameShell, json.RawMessage(args)) == tools.ToolConcurrencyClassReadOnly
	a.shellReadOnlyClass.mu.Lock()
	if a.shellReadOnlyClass.verdicts == nil {
		a.shellReadOnlyClass.verdicts = make(map[string]bool)
	}
	a.shellReadOnlyClass.verdicts[toolCallID] = verdict
	a.shellReadOnlyClass.mu.Unlock()
	return verdict
}

// shellReadInvalidationMemo remembers, per mutating shell result, which
// (path, expected-hash) pairs were already verified against the disk. It is
// keyed by the shell's ToolCallID: a completed command's side effects are
// fixed, so its verdicts never change, and a newer mutating shell resets the
// map.
type shellReadInvalidationMemo struct {
	mu       sync.Mutex
	shellID  string
	verdicts map[string]bool
}

func (a *MainAgent) externallyInvalidatedReadsAfterMutatingShell(messages []message.Message, scan *reductionHistoryScan) map[int]bool {
	if a == nil || a.tools == nil || len(messages) == 0 {
		return nil
	}
	boundary := 0
	if surface, ok := a.stableReductionSurfaceCandidate(a.currentTurnID()); ok {
		boundary = min(len(surface.Messages), len(messages))
	}
	callMeta := scan.callMeta()
	mutatingShell := false
	shellIndex := -1
	for i := len(messages) - 1; i >= boundary; i-- {
		msg := messages[i]
		if msg.Role != message.RoleTool || isToolResultErrorStatus(msg.ToolStatus) {
			continue
		}
		meta := callMeta[msg.ToolCallID]
		if toolname.Normalize(meta.Name) == tools.NameShell && !a.shellCommandReadOnly(msg.ToolCallID, meta.Args) {
			mutatingShell = true
			shellIndex = i
			break
		}
	}
	if !mutatingShell || shellIndex <= 0 {
		return nil
	}
	type currentFileRevision struct {
		hash   string
		exists bool
		valid  bool
	}
	hashes := make(map[string]currentFileRevision)
	// A completed shell's side effects are fixed, so each (path, expected-hash)
	// pair needs one disk verification per shell result instead of one per LLM
	// request; a newer mutating shell resets the memo and re-verifies.
	shellID := strings.TrimSpace(messages[shellIndex].ToolCallID)
	a.shellReadMemo.mu.Lock()
	defer a.shellReadMemo.mu.Unlock()
	if a.shellReadMemo.shellID != shellID || a.shellReadMemo.verdicts == nil {
		a.shellReadMemo.shellID = shellID
		a.shellReadMemo.verdicts = make(map[string]bool)
	}
	verdicts := a.shellReadMemo.verdicts
	var invalidated map[int]bool
	for i := 0; i < shellIndex; i++ {
		msg := messages[i]
		if msg.Role != message.RoleTool || msg.FileState == nil || toolname.Normalize(callMeta[msg.ToolCallID].Name) != tools.NameRead {
			continue
		}
		for _, read := range msg.FileState.Reads {
			path := strings.TrimSpace(read.Path)
			expected := strings.TrimSpace(read.SHA256)
			if path == "" || expected == "" || !read.Exists {
				continue
			}
			if !filepath.IsAbs(path) && a.projectRoot != "" {
				path = filepath.Join(a.projectRoot, path)
			}
			key := path + "\x00" + expected
			verdict, cached := verdicts[key]
			if !cached {
				current, checked := hashes[path]
				if !checked {
					hash, exists, _, err := verifiedCurrentFileHash(path)
					current = currentFileRevision{hash: hash, exists: exists, valid: err == nil}
					hashes[path] = current
				}
				if !current.valid {
					// Transient verify error: leave the pair unmemoized so the
					// next pass retries instead of freezing a bad verdict.
					continue
				}
				verdict = !current.exists || current.hash != expected
				verdicts[key] = verdict
			}
			if verdict {
				if invalidated == nil {
					invalidated = make(map[int]bool)
				}
				invalidated[i] = true
				break
			}
		}
	}
	return invalidated
}

func stableReductionSurfaceNeedsReview(surface stableReductionSurface, scan *reductionHistoryScan, currentBatch uint64, externalInvalidated map[int]bool) bool {
	messages := scan.messages
	boundary := len(surface.Messages)
	if boundary == 0 || len(messages) < boundary {
		return true
	}
	latestBatch := uint64(0)
	for i := range boundary {
		if messages[i].Role == message.RoleAssistant && len(messages[i].ToolCalls) > 0 && messages[i].RequestBatch > 0 {
			latestBatch = messages[i].RequestBatch
		}
		if i >= len(surface.NextReviewAge) || surface.NextReviewAge[i] <= 0 || messages[i].Role != message.RoleTool || latestBatch == 0 || currentBatch <= latestBatch {
			continue
		}
		if int(currentBatch-latestBatch) >= surface.NextReviewAge[i] {
			return true
		}
	}
	if len(externalInvalidated) > 0 {
		return true
	}
	newToolResults := countToolResults(messages[boundary:])
	toolResults := surface.ToolResults + newToolResults
	if surface.ToolResults < surface.Policy.MinToolResultsPrune && toolResults >= surface.Policy.MinToolResultsPrune {
		return true
	}
	if newToolResults == 0 {
		return false
	}
	callMeta := scan.callMeta()
	repeated := scan.repeatedOutputs()
	validity := scan.readValidity()
	for i := range boundary {
		alreadyReduced := i < len(surface.ReducedIndices) && surface.ReducedIndices[i]
		// Repeated detection runs over the immutable original history, so a
		// duplicated call flags its earlier index forever. Once that index is
		// reduced to the repeated marker there is nothing left to review, and
		// treating it as reviewable would permanently disable surface reuse.
		if repeated[i] && !alreadyReduced {
			return true
		}
		if !alreadyReduced || messages[i].Role != message.RoleTool || toolname.Normalize(callMeta[messages[i].ToolCallID].Name) != tools.NameRead {
			continue
		}
		state := validity[i]
		marker := surface.Messages[i].Content
		// Validity here is computed over the ORIGINAL messages while the
		// frozen marker was built from the PREPARED view, so the two can
		// legitimately disagree on stale-vs-superseded for an already-reduced
		// read (a later edit overlaps the original range but not the kept
		// head). Either marker means the read was trimmed with guidance; the
		// still-full covering read carries the stale warning. Requiring the
		// exact class here would re-run the full scan on every request for
		// the rest of the session without changing any output.
		reducedMarked := strings.Contains(marker, "truncated="+tools.ReadTruncatedStale) ||
			strings.Contains(marker, "truncated="+tools.ReadTruncatedSuperseded)
		if (state.Invalidated || state.Superseded) && !reducedMarked {
			return true
		}
	}
	return false
}

type llmModelContinuitySnapshot struct {
	PreviousModel           string
	CurrentModel            string
	ProjectedModelRunLength int
}

func (a *MainAgent) llmModelContinuitySnapshot() llmModelContinuitySnapshot {
	if a == nil {
		return llmModelContinuitySnapshot{}
	}
	a.llmMu.RLock()
	defer a.llmMu.RUnlock()
	current := a.runningModelRef
	if current == "" {
		current = a.providerModelRef
	}
	snapshot := llmModelContinuitySnapshot{
		PreviousModel: a.previousLLMModelRef,
		CurrentModel:  current,
	}
	if current == "" {
		return snapshot
	}
	if a.lastLLMRequestModelRef != current {
		snapshot.ProjectedModelRunLength = 1
		return snapshot
	}
	if a.llmModelRunLength <= 0 {
		snapshot.ProjectedModelRunLength = 1
		return snapshot
	}
	snapshot.ProjectedModelRunLength = a.llmModelRunLength + 1
	return snapshot
}

func (a *MainAgent) recordLLMModelRun(ref string) {
	if a == nil {
		return
	}
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return
	}
	a.llmMu.Lock()
	defer a.llmMu.Unlock()
	if a.lastLLMRequestModelRef == ref {
		a.llmModelRunLength++
	} else {
		a.lastLLMRequestModelRef = ref
		a.llmModelRunLength = 1
	}
}

func (a *MainAgent) resetLLMModelRun() {
	if a == nil {
		return
	}
	a.llmMu.Lock()
	defer a.llmMu.Unlock()
	a.lastLLMRequestModelRef = ""
	a.llmModelRunLength = 0
}

// modelChangedSinceLastPreparedRequest reports whether the running model
// differs from the one used by the previous prepared LLM request. The stable
// reduction surface was produced under the previous model's budget, so a change
// here means it must not be reused: the new model is entitled to its own
// reduction. Returns false when there was no previous prepared request (fresh
// session), since there is nothing to invalidate.
func (a *MainAgent) modelChangedSinceLastPreparedRequest() bool {
	if a == nil {
		return false
	}
	a.llmMu.RLock()
	defer a.llmMu.RUnlock()
	if a.lastLLMRequestModelRef == "" {
		return false
	}
	current := a.runningModelRef
	if current == "" {
		current = a.providerModelRef
	}
	return current != a.lastLLMRequestModelRef
}

func (a *MainAgent) rememberPreparedLLMRequest(turnID uint64, original, prepared []message.Message, discardedInputs map[string]string, nextReviewAge []int, toolResults int, policy contextReductionPolicy) {
	if a == nil || turnID == 0 {
		return
	}
	reducedIndices := computeReducedToolResultIndices(original, prepared)
	toolDefHash := a.computeToolDefinitionHash()
	a.loopReductionMu.Lock()
	defer a.loopReductionMu.Unlock()
	a.lastPreparedLLMTurnID = turnID
	shapes, source := a.incrementalMessageShapesLocked(original)
	a.lastPreparedLLMRequestShape = shapes
	a.lastPreparedLLMShapeSource = source
	a.lastPreparedLLMRequestPrefix = cloneMessageSliceForRequestShape(prepared)
	a.lastPreparedLLMReducedIndices = reducedIndices
	if discardedInputs != nil {
		a.lastPreparedLLMDiscardedInputs = maps.Clone(discardedInputs)
	}
	a.lastPreparedLLMNextReviewAge = append([]int(nil), nextReviewAge...)
	a.lastPreparedLLMToolResults = toolResults
	a.lastPreparedReductionPolicy = policy
	a.lastPreparedLLMToolDefHash = toolDefHash
	a.lastPreparedReductionStats = cloneContextReductionStats(a.contextReductionStats)
}

// incrementalMessageShapesLocked computes shapes for original plus the source
// copy to store alongside them, reusing stored entries for the leading run of
// messages that are field-equal to the previous shape source. In the steady
// state (unchanged history) both return values are the previously stored
// slices and nothing is hashed or allocated; with an append-only history only
// the new tail is hashed. Caller must hold loopReductionMu.
func (a *MainAgent) incrementalMessageShapesLocked(original []message.Message) ([]stableReductionMessageShape, []message.Message) {
	if len(original) == 0 {
		return nil, nil
	}
	prevSource := a.lastPreparedLLMShapeSource
	prevShape := a.lastPreparedLLMRequestShape
	reusable := 0
	if len(prevSource) == len(prevShape) {
		reusable = reusableMessagePrefixLen(prevSource, original)
	}
	if reusable == len(original) && len(prevSource) == len(original) {
		return prevShape, prevSource
	}
	source := append([]message.Message(nil), original...)
	if reusable == 0 {
		return stableReductionMessageShapes(original), source
	}
	shapes := make([]stableReductionMessageShape, len(original))
	copy(shapes, prevShape[:reusable])
	for i := reusable; i < len(original); i++ {
		shapes[i] = stableReductionMessageShapeOf(&original[i])
	}
	return shapes, source
}

func (a *MainAgent) setPreparedStablePrefixLen(n int) {
	if a == nil {
		return
	}
	a.loopReductionMu.Lock()
	defer a.loopReductionMu.Unlock()
	a.lastPreparedStablePrefixLen = n
}

// computeReducedToolResultIndices reports which tool-result positions had
// their content changed by reduction. Used to freeze already-reduced messages
// in incremental reduction so their marker content stays cache-stable.
func computeReducedToolResultIndices(original, prepared []message.Message) []bool {
	n := len(prepared)
	if n == 0 {
		return nil
	}
	reduced := make([]bool, n)
	for i := 0; i < n && i < len(original); i++ {
		if prepared[i].Role != message.RoleTool {
			continue
		}
		if prepared[i].Content != original[i].Content || prepared[i].ToolDiff != original[i].ToolDiff {
			reduced[i] = true
		}
	}
	return reduced
}

// computeToolDefinitionHash returns a stable hash of the frozen tool surface.
// A mismatch between turns invalidates the frozen prefix because tool changes
// alter the cacheable prefix and may require re-evaluating earlier tool results.
// The hash is memoized per frozen snapshot: freezeToolSurfaceFromDefinitions
// stores an immutable slice behind an atomic pointer, so pointer identity is a
// sound cache key and repeated per-request calls skip re-marshaling schemas.
func (a *MainAgent) computeToolDefinitionHash() [sha256.Size]byte {
	frozen := a.frozenToolDefs.Load()
	if frozen == nil {
		return toolDefinitionsHash(a.mainLLMToolDefinitions())
	}
	if memo := a.toolDefHashMemo.Load(); memo != nil && memo.defs == frozen {
		return memo.hash
	}
	hash := toolDefinitionsHash(*frozen)
	a.toolDefHashMemo.Store(&toolDefHashMemoEntry{defs: frozen, hash: hash})
	return hash
}

// toolDefinitionsHash returns a stable hash of a tool surface. Name,
// description, and input schema all contribute because any of them changes the
// cacheable tool surface even when the others are unchanged. encoding/json
// marshals map keys in sorted order, so equal schemas hash to the same bytes
// across turns.
func toolDefinitionsHash(defs []message.ToolDefinition) [sha256.Size]byte {
	h := sha256.New()
	stableReductionWriteInt(h, len(defs))
	for _, def := range defs {
		stableReductionWriteString(h, def.Name)
		stableReductionWriteString(h, def.Description)
		schema, err := json.Marshal(def.InputSchema)
		if err != nil {
			schema = nil
		}
		stableReductionWriteBytes(h, schema)
	}
	return stableReductionHashBytes(h.Sum(nil))
}

// stableReductionSurfacePrefixLen returns the previous request's stable reduced
// prefix length for use as an Anthropic prompt-cache boundary hint. It reflects
// the frozen surface reused by the current request, not the full prepared list.
func (a *MainAgent) stableReductionSurfacePrefixLen(turnID uint64) (int, bool) {
	if a == nil || turnID == 0 {
		return 0, false
	}
	a.loopReductionMu.Lock()
	defer a.loopReductionMu.Unlock()
	if len(a.lastPreparedLLMRequestPrefix) == 0 {
		return 0, false
	}
	return len(a.lastPreparedLLMRequestPrefix), true
}

// consumePreparedStablePrefixLen returns and clears the stable reduced prefix
// length recorded during message preparation. The LLM layer consumes it as a
// one-shot cache-placement hint before subsequent turn bookkeeping overwrites it.
func (a *MainAgent) consumePreparedStablePrefixLen() int {
	if a == nil {
		return 0
	}
	a.loopReductionMu.Lock()
	defer a.loopReductionMu.Unlock()
	n := a.lastPreparedStablePrefixLen
	a.lastPreparedStablePrefixLen = 0
	return n
}

// incrementalReductionSurface returns the previous stable surface's frozen
// prefix, the per-message "was reduced" flags, the frozen boundary length, and
// whether incremental reduction is enabled for this request. When enabled,
// already-reduced tool results in the prefix are frozen (copied verbatim from
// the previous surface) and skipped during the reduction scan; only
// not-yet-reduced prefix messages and the new tail are re-examined.
//
// Incremental reduction is disabled when:
//   - there is no previous stable surface for this turn;
//   - the prefix shapes are incompatible (history changed underneath us);
//   - the tool-definition surface changed (cache prefix invalidated).
func (a *MainAgent) incrementalReductionSurface(prepared []message.Message) (frozenPrefix []message.Message, reducedIndices []bool, nextReviewAge []int, previousToolResults, boundary int, enabled bool) {
	if a == nil {
		return nil, nil, nil, 0, 0, false
	}
	previous, ok := a.stableReductionSurfaceCandidate(a.currentTurnID())
	if !ok || len(previous.Messages) == 0 || len(prepared) < len(previous.Messages) {
		return nil, nil, nil, 0, 0, false
	}
	// A model switch that happened this turn must invalidate the frozen
	// surface: the previous surface was reduced under the old model's context
	// budget and run-length assumptions, so reusing it would skip the
	// reduction the new model is entitled to. Compare against the model used by
	// the previous prepared request, not ProjectedModelRunLength, since the
	// latter is also 1 on the first request of a fresh model run (no switch).
	if a.modelChangedSinceLastPreparedRequest() {
		return nil, nil, nil, 0, 0, false
	}
	// Tool-definition change invalidates the entire frozen surface.
	if previous.ToolDefHash != a.computeToolDefinitionHash() {
		return nil, nil, nil, 0, 0, false
	}
	if previous.Policy != a.contextReductionPolicy() {
		return nil, nil, nil, 0, 0, false
	}
	// Shape compatibility ensures the prefix messages have not changed since
	// the previous surface was recorded (same content, Role, ToolCalls, etc).
	if !stableReductionPrefixCompatible(previous, prepared[:len(previous.Messages)]) {
		return nil, nil, nil, 0, 0, false
	}
	return previous.Messages, previous.ReducedIndices, previous.NextReviewAge, previous.ToolResults, len(previous.Messages), true
}

func (a *MainAgent) updatePreparedLLMRequestSurface(turnID uint64, prepared []message.Message) {
	if a == nil || turnID == 0 {
		return
	}
	a.loopReductionMu.Lock()
	defer a.loopReductionMu.Unlock()
	if a.lastPreparedLLMTurnID != turnID || len(a.lastPreparedLLMRequestShape) != len(prepared) {
		a.lastPreparedLLMRequestShape = stableReductionMessageShapes(prepared)
		a.lastPreparedLLMShapeSource = append([]message.Message(nil), prepared...)
	}
	a.lastPreparedLLMTurnID = turnID
	a.lastPreparedLLMRequestPrefix = cloneMessageSliceForRequestShape(prepared)
	a.lastPreparedReductionStats = cloneContextReductionStats(a.contextReductionStats)
}

type stableReductionSurface struct {
	Messages []message.Message
	Shape    []stableReductionMessageShape
	// ShapeSource holds shallow copies of the original messages Shape was
	// computed from, when available. It enables direct field-equality
	// compatibility checks that skip content re-hashing. May be nil (e.g.
	// loop-frozen surfaces); callers must fall back to hash comparison.
	ShapeSource    []message.Message
	Stats          ContextReductionStats
	ReducedIndices []bool
	NextReviewAge  []int
	ToolResults    int
	Policy         contextReductionPolicy
	ToolDefHash    [sha256.Size]byte
}

// toolDefHashMemoEntry caches the hash of one frozen tool-definition snapshot.
// defs is the exact pointer stored in MainAgent.frozenToolDefs; snapshots are
// immutable after freeze, so pointer identity implies hash validity.
type toolDefHashMemoEntry struct {
	defs *[]message.ToolDefinition
	hash [sha256.Size]byte
}

type stableReductionMessageShape struct {
	Role                message.Role
	ContentHash         [sha256.Size]byte
	PartsHash           [sha256.Size]byte
	ThinkingHash        [sha256.Size]byte
	ResponsesOutputHash [sha256.Size]byte
	GeminiPartsHash     [sha256.Size]byte
	ReasoningHash       [sha256.Size]byte
	CompactionFilesHash [sha256.Size]byte
	ToolCallsHash       [sha256.Size]byte
	ToolCallID          string
	RequestBatch        uint64
	ToolDiffHash        [sha256.Size]byte
	ToolDiffAdded       int
	ToolDiffRemoved     int
	ToolStatus          string
	Provenance          stableReductionProvenanceShape
	IsCompactionSummary bool
	Kind                string
	ToolRecoveryState   string
}

type stableReductionProvenanceShape struct {
	Source     string
	ProviderID string
	ModelID    string
	Variant    string
	ModelRef   string
	WireFamily string
	Imported   bool
}

// stableReductionSurfaceCandidate returns a read-only borrowed view of the
// last prepared request surface. The returned slices alias agent state that is
// only ever replaced wholesale (never mutated in place), so the view stays
// consistent even if a later store swaps the fields. Callers must not mutate
// the returned surface; reuse paths already clone every message they copy into
// an outgoing request.
//
// The surface deliberately survives across turns: reuse correctness is
// guaranteed by shape compatibility against the current message prefix, not by
// turn identity, and keeping reduced markers byte-stable across turns is what
// keeps the provider prompt cache warm at turn boundaries.
func (a *MainAgent) stableReductionSurfaceCandidate(turnID uint64) (stableReductionSurface, bool) {
	if a == nil || turnID == 0 {
		return stableReductionSurface{}, false
	}
	a.loopReductionMu.Lock()
	defer a.loopReductionMu.Unlock()
	if len(a.lastPreparedLLMRequestPrefix) == 0 {
		return stableReductionSurface{}, false
	}
	return stableReductionSurface{
		Messages:       a.lastPreparedLLMRequestPrefix,
		Shape:          a.lastPreparedLLMRequestShape,
		ShapeSource:    a.lastPreparedLLMShapeSource,
		Stats:          a.lastPreparedReductionStats,
		ReducedIndices: a.lastPreparedLLMReducedIndices,
		NextReviewAge:  a.lastPreparedLLMNextReviewAge,
		ToolResults:    a.lastPreparedLLMToolResults,
		Policy:         a.lastPreparedReductionPolicy,
		ToolDefHash:    a.lastPreparedLLMToolDefHash,
	}, true
}

func reuseStableReductionPrefix(previous stableReductionSurface, current, shapeSource []message.Message) ([]message.Message, bool) {
	previousMessages := previous.Messages
	if len(previousMessages) == 0 {
		return current, true
	}
	if len(current) < len(previousMessages) || len(shapeSource) < len(previousMessages) || !stableReductionPrefixCompatible(previous, shapeSource[:len(previousMessages)]) {
		return current, false
	}
	// Build the output in one pass: the prefix is cloned from the stored
	// surface, the tail from current. Cloning current first and overwriting
	// the prefix would clone every prefix message twice for nothing.
	out := make([]message.Message, len(current))
	for i := range previousMessages {
		out[i] = cloneMessageForRequestShape(previousMessages[i])
	}
	for i := len(previousMessages); i < len(current); i++ {
		out[i] = cloneMessageForRequestShape(current[i])
	}
	if stableReductionReuseWouldCreateOrphans(current, out) {
		return current, false
	}
	return out, true
}

// stableReductionPrefixCompatible reports whether the first len(previous.Shape)
// messages of the current request still match the surface's recorded shape.
// When the surface carries its shape source, plain field equality is used:
// unchanged messages share string backing with the source copies, so each
// comparison is O(1) and allocation-free. Without a matching source it falls
// back to hashing the current prefix.
func stableReductionPrefixCompatible(previous stableReductionSurface, currentPrefix []message.Message) bool {
	if len(previous.ShapeSource) == len(previous.Shape) && len(previous.ShapeSource) == len(currentPrefix) {
		return stableReductionMessagesEquivalent(previous.ShapeSource, currentPrefix)
	}
	return stableReductionShapesCompatible(previous.Shape, currentPrefix)
}

func stableReductionShapesCompatible(previous []stableReductionMessageShape, current []message.Message) bool {
	if len(previous) != len(current) {
		return false
	}
	currentShape := stableReductionMessageShapes(current)
	for i := range previous {
		if previous[i] != currentShape[i] {
			return false
		}
	}
	return true
}

func stableReductionMessageShapes(messages []message.Message) []stableReductionMessageShape {
	if len(messages) == 0 {
		return nil
	}
	shapes := make([]stableReductionMessageShape, len(messages))
	for i := range messages {
		shapes[i] = stableReductionMessageShapeOf(&messages[i])
	}
	return shapes
}

func stableReductionMessageShapeOf(msg *message.Message) stableReductionMessageShape {
	return stableReductionMessageShape{
		Role:                msg.Role,
		ContentHash:         stableReductionHashString(msg.Content),
		PartsHash:           stableReductionContentPartsHash(msg.Parts),
		ThinkingHash:        stableReductionThinkingBlocksHash(msg.ThinkingBlocks),
		ResponsesOutputHash: stableReductionResponsesOutputHash(msg.ResponsesOutput),
		GeminiPartsHash:     stableReductionGeminiPartsHash(msg.GeminiParts),
		ReasoningHash:       stableReductionHashString(msg.ReasoningContent),
		CompactionFilesHash: stableReductionStringMapHash(msg.CompactionFileRevisions),
		ToolCallsHash:       stableReductionToolCallsHash(msg.ToolCalls),
		ToolCallID:          msg.ToolCallID,
		RequestBatch:        msg.RequestBatch,
		ToolDiffHash:        stableReductionHashString(msg.ToolDiff),
		ToolDiffAdded:       msg.ToolDiffAdded,
		ToolDiffRemoved:     msg.ToolDiffRemoved,
		ToolStatus:          msg.ToolStatus,
		Provenance:          stableReductionProvenanceShapeFor(msg.Provenance),
		IsCompactionSummary: msg.IsCompactionSummary,
		Kind:                msg.Kind,
		ToolRecoveryState:   msg.ToolRecoveryState,
	}
}

// stableReductionMessagesEquivalent reports whether every message pair would
// produce identical stableReductionMessageShape values. It compares exactly
// the fields the shape hashes cover, using direct equality instead of
// hashing: string comparison short-circuits on shared backing arrays, so an
// unchanged (append-only) history costs O(1) per message with no allocation.
// Field equality implies hash equality, so this is strictly at least as
// precise as comparing hashes.
func stableReductionMessagesEquivalent(source, current []message.Message) bool {
	return len(source) == len(current) && reusableMessagePrefixLen(source, current) == len(source)
}

func stableReductionMessageEquivalent(a, b *message.Message) bool {
	if a.Role != b.Role ||
		a.Content != b.Content ||
		a.ReasoningContent != b.ReasoningContent ||
		a.ToolCallID != b.ToolCallID ||
		a.RequestBatch != b.RequestBatch ||
		a.ToolDiff != b.ToolDiff ||
		a.ToolDiffAdded != b.ToolDiffAdded ||
		a.ToolDiffRemoved != b.ToolDiffRemoved ||
		a.ToolStatus != b.ToolStatus ||
		a.IsCompactionSummary != b.IsCompactionSummary ||
		a.Kind != b.Kind ||
		a.ToolRecoveryState != b.ToolRecoveryState {
		return false
	}
	if !maps.Equal(a.CompactionFileRevisions, b.CompactionFileRevisions) {
		return false
	}
	if stableReductionProvenanceShapeFor(a.Provenance) != stableReductionProvenanceShapeFor(b.Provenance) {
		return false
	}
	if len(a.ToolCalls) != len(b.ToolCalls) {
		return false
	}
	for i := range a.ToolCalls {
		ac, bc := &a.ToolCalls[i], &b.ToolCalls[i]
		if ac.ID != bc.ID || ac.Name != bc.Name || ac.ThoughtSignature != bc.ThoughtSignature || !bytes.Equal(ac.Args, bc.Args) {
			return false
		}
	}
	if len(a.ThinkingBlocks) != len(b.ThinkingBlocks) {
		return false
	}
	for i := range a.ThinkingBlocks {
		if a.ThinkingBlocks[i] != b.ThinkingBlocks[i] {
			return false
		}
	}
	if !slices.Equal(a.GeminiParts, b.GeminiParts) || !responsesOutputItemsEqual(a.ResponsesOutput, b.ResponsesOutput) {
		return false
	}
	if len(a.Parts) != len(b.Parts) {
		return false
	}
	for i := range a.Parts {
		ap, bp := &a.Parts[i], &b.Parts[i]
		if ap.Type != bp.Type ||
			ap.Text != bp.Text ||
			ap.DisplayText != bp.DisplayText ||
			ap.InlineToken != bp.InlineToken ||
			ap.MimeType != bp.MimeType ||
			ap.ImagePath != bp.ImagePath ||
			ap.FileName != bp.FileName ||
			!bytes.Equal(ap.Data, bp.Data) {
			return false
		}
	}
	return true
}

func stableReductionProvenanceShapeFor(provenance *message.MessageProvenance) stableReductionProvenanceShape {
	if provenance == nil {
		return stableReductionProvenanceShape{}
	}
	return stableReductionProvenanceShape{
		Source:     provenance.Source,
		ProviderID: provenance.ProviderID,
		ModelID:    provenance.ModelID,
		Variant:    provenance.Variant,
		ModelRef:   provenance.ModelRef,
		WireFamily: provenance.WireFamily,
		Imported:   provenance.Imported,
	}
}

func stableReductionHashString(value string) [sha256.Size]byte {
	return sha256.Sum256([]byte(value))
}

func stableReductionStringMapHash(values map[string]string) [sha256.Size]byte {
	if len(values) == 0 {
		return stableReductionEmptySequenceHash
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	h := sha256.New()
	stableReductionWriteInt(h, len(keys))
	for _, key := range keys {
		stableReductionWriteString(h, key)
		stableReductionWriteString(h, values[key])
	}
	return stableReductionHashBytes(h.Sum(nil))
}

var stableReductionEmptySequenceHash = func() [sha256.Size]byte {
	h := sha256.New()
	stableReductionWriteInt(h, 0)
	return stableReductionHashBytes(h.Sum(nil))
}()

func stableReductionContentPartsHash(parts []message.ContentPart) [sha256.Size]byte {
	if len(parts) == 0 {
		return stableReductionEmptySequenceHash
	}
	h := sha256.New()
	stableReductionWriteInt(h, len(parts))
	for _, part := range parts {
		stableReductionWriteString(h, string(part.Type))
		stableReductionWriteString(h, part.Text)
		stableReductionWriteString(h, part.DisplayText)
		stableReductionWriteString(h, part.InlineToken)
		stableReductionWriteString(h, part.MimeType)
		stableReductionWriteBytes(h, part.Data)
		stableReductionWriteString(h, part.ImagePath)
		stableReductionWriteString(h, part.FileName)
	}
	return stableReductionHashBytes(h.Sum(nil))
}

func stableReductionThinkingBlocksHash(blocks []message.ThinkingBlock) [sha256.Size]byte {
	if len(blocks) == 0 {
		return stableReductionEmptySequenceHash
	}
	h := sha256.New()
	stableReductionWriteInt(h, len(blocks))
	for _, block := range blocks {
		stableReductionWriteString(h, block.Thinking)
		stableReductionWriteString(h, block.Signature)
		stableReductionWriteString(h, block.Data)
	}
	return stableReductionHashBytes(h.Sum(nil))
}

func stableReductionToolCallsHash(calls []message.ToolCall) [sha256.Size]byte {
	if len(calls) == 0 {
		return stableReductionEmptySequenceHash
	}
	h := sha256.New()
	stableReductionWriteInt(h, len(calls))
	for _, call := range calls {
		stableReductionWriteString(h, call.ID)
		stableReductionWriteString(h, call.Name)
		stableReductionWriteBytes(h, call.Args)
		stableReductionWriteString(h, call.ThoughtSignature)
	}
	return stableReductionHashBytes(h.Sum(nil))
}

func stableReductionResponsesOutputHash(items []message.ResponsesOutputItem) [sha256.Size]byte {
	if len(items) == 0 {
		return stableReductionEmptySequenceHash
	}
	h := sha256.New()
	stableReductionWriteInt(h, len(items))
	for _, item := range items {
		stableReductionWriteString(h, item.Type)
		stableReductionWriteString(h, item.ID)
		stableReductionWriteString(h, item.CallID)
		stableReductionWriteString(h, item.Role)
		stableReductionWriteString(h, item.Name)
		stableReductionWriteString(h, item.Arguments)
		stableReductionWriteString(h, item.Phase)
		stableReductionWriteString(h, item.EncryptedContent)
		stableReductionWriteInt(h, len(item.Content))
		for _, content := range item.Content {
			stableReductionWriteString(h, content.Type)
			stableReductionWriteString(h, content.Text)
			stableReductionWriteString(h, content.Refusal)
		}
		stableReductionWriteInt(h, len(item.Summary))
		for _, summary := range item.Summary {
			stableReductionWriteString(h, summary.Type)
			stableReductionWriteString(h, summary.Text)
		}
	}
	return stableReductionHashBytes(h.Sum(nil))
}

func stableReductionGeminiPartsHash(parts []message.GeminiReplayPart) [sha256.Size]byte {
	if len(parts) == 0 {
		return stableReductionEmptySequenceHash
	}
	h := sha256.New()
	stableReductionWriteInt(h, len(parts))
	for _, part := range parts {
		stableReductionWriteString(h, part.Type)
		stableReductionWriteString(h, part.Text)
		stableReductionWriteString(h, part.ToolCallID)
		stableReductionWriteString(h, part.ThoughtSignature)
	}
	return stableReductionHashBytes(h.Sum(nil))
}

func responsesOutputItemsEqual(a, b []message.ResponsesOutputItem) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Type != b[i].Type || a[i].ID != b[i].ID || a[i].CallID != b[i].CallID || a[i].Role != b[i].Role || a[i].Name != b[i].Name || a[i].Arguments != b[i].Arguments || a[i].Phase != b[i].Phase || a[i].EncryptedContent != b[i].EncryptedContent || !slices.Equal(a[i].Content, b[i].Content) || !slices.Equal(a[i].Summary, b[i].Summary) {
			return false
		}
	}
	return true
}

func stableReductionHashBytes(value []byte) [sha256.Size]byte {
	return sha256.Sum256(value)
}

func stableReductionWriteString(h interface{ Write([]byte) (int, error) }, value string) {
	stableReductionWriteBytes(h, []byte(value))
}

func stableReductionWriteBytes(h interface{ Write([]byte) (int, error) }, value []byte) {
	stableReductionWriteInt(h, len(value))
	_, _ = h.Write(value)
}

func stableReductionWriteInt(h interface{ Write([]byte) (int, error) }, value int) {
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], uint64(value))
	_, _ = h.Write(buf[:])
}

func stableReductionReuseWouldCreateOrphans(current, reused []message.Message) bool {
	currentDropped := message.CountDroppedOrphanToolResults(current)
	reusedDropped := message.CountDroppedOrphanToolResults(reused)
	if reusedDropped > currentDropped {
		return true
	}
	currentSupported := supportedToolResultIDs(current)
	reusedSupported := supportedToolResultIDs(reused)
	for id := range currentSupported {
		if _, ok := reusedSupported[id]; !ok {
			return true
		}
	}
	return false
}

func supportedToolResultIDs(messages []message.Message) map[string]struct{} {
	supported := make(map[string]struct{})
	for i, msg := range messages {
		if msg.Role != message.RoleTool || msg.ToolCallID == "" {
			continue
		}
		if toolResultSupportedByNearestAssistant(messages, i) {
			supported[msg.ToolCallID] = struct{}{}
		}
	}
	return supported
}

func toolResultSupportedByNearestAssistant(messages []message.Message, toolIdx int) bool {
	id := messages[toolIdx].ToolCallID
	for i := toolIdx - 1; i >= 0; i-- {
		msg := messages[i]
		if msg.Role != message.RoleAssistant || len(msg.ToolCalls) == 0 {
			continue
		}
		for _, call := range msg.ToolCalls {
			if call.ID == id {
				return true
			}
		}
		return false
	}
	return false
}

func (a *MainAgent) tryReuseStableReductionSurfaceBeforeFullScan(messages []message.Message, policy contextReductionPolicy, scan *reductionHistoryScan, currentBatch uint64, externalInvalidated map[int]bool) ([]message.Message, ContextReductionStats, bool) {
	previous, ok := a.stableReductionSurfaceCandidate(a.currentTurnID())
	if !ok || len(previous.Messages) == 0 || len(messages) < len(previous.Messages) {
		return nil, ContextReductionStats{}, false
	}
	if !hasReductionSavings(previous.Stats) {
		return nil, ContextReductionStats{}, false
	}
	if previous.Policy != policy {
		return nil, ContextReductionStats{}, false
	}
	// A model switch this turn invalidates the previous reduction surface for
	// the same reason as the incremental path: the new model must get a fresh
	// reduction under its own context budget, not the old model's frozen one.
	if a.modelChangedSinceLastPreparedRequest() {
		return nil, ContextReductionStats{}, false
	}
	if stableReductionSurfaceNeedsReview(previous, scan, currentBatch, externalInvalidated) {
		return nil, ContextReductionStats{}, false
	}
	tailTokens := estimateMessagesTokens(a.ctxMgr, messages[len(previous.Messages):])
	if tailTokens >= policy.MinIncrementalTokens {
		return nil, ContextReductionStats{}, false
	}
	reused, compatible := reuseStableReductionPrefix(previous, messages, messages)
	if !compatible {
		return nil, ContextReductionStats{}, false
	}
	stats := highLevelContextReductionStats(a.ctxMgr, messages, reused)
	a.setCurrentRequestSurface(&stats, reused)
	if len(stats.ByToolAndRule) == 0 {
		stats.ByToolAndRule = cloneContextReductionBuckets(previous.Stats.ByToolAndRule)
	}
	stats.ReusedStable = true
	stats.ReuseReason = contextReuseReasonBelowIncrementalMin
	stats.SavedDelta = tailTokens
	return reused, stats, true
}

func hasReductionSavings(stats ContextReductionStats) bool {
	return stats.TokensSaved > 0 || stats.Bytes > 0 || stats.Messages > 0
}

func contextReductionToolInputKey(toolName, args string) string {
	if strings.TrimSpace(toolName) == "" && strings.TrimSpace(args) == "" {
		return ""
	}
	return toolname.Normalize(toolName) + "\x00" + strings.TrimSpace(args)
}

// reductionRecallProtectMaxKeys bounds the per-session recall-protection set so
// a pathological session cannot grow it without limit. Beyond the cap new
// recall evidence is dropped; existing protections persist.
const reductionRecallProtectMaxKeys = 512

// recordDiscardedInputEvidence bounds the session-scoped evidence copied into
// every reduction pass. Once the cap is reached, existing evidence remains
// useful while new entries are conservatively omitted rather than allowing a
// long session's unique reads/searches to make each request grow without
// bound.
func recordDiscardedInputEvidence(evidence map[string]string, key, callID string) {
	if evidence == nil || strings.TrimSpace(key) == "" {
		return
	}
	if _, exists := evidence[key]; !exists && len(evidence) >= reductionRecallProtectMaxKeys {
		return
	}
	evidence[key] = callID
}

// noteRecalledReductionInput records that the output of this tool input was
// reduced on an earlier request and the model re-issued the identical call —
// direct evidence that the reduction discarded content the model still needed.
// The newest output of a recalled input is exempt from reduction for the rest
// of the session.
func (a *MainAgent) noteRecalledReductionInput(key string) {
	if a == nil || key == "" {
		return
	}
	a.loopReductionMu.Lock()
	defer a.loopReductionMu.Unlock()
	if a.recalledReductionInputs == nil {
		a.recalledReductionInputs = make(map[string]struct{})
	}
	if len(a.recalledReductionInputs) >= reductionRecallProtectMaxKeys {
		if _, ok := a.recalledReductionInputs[key]; !ok {
			return
		}
	}
	a.recalledReductionInputs[key] = struct{}{}
}

// lastPreparedDiscardedInputsSnapshot clones the session's discarded-input
// evidence set for a reduction pass. The set is monotonic session state —
// input keys whose newest output was genuinely summarized away (never
// repeated-collapse, which always leaves a fresher full copy in context) —
// and is dropped with the visible reduction caches on restore or model switch.
func (a *MainAgent) lastPreparedDiscardedInputsSnapshot() map[string]string {
	out := make(map[string]string)
	if a == nil {
		return out
	}
	a.loopReductionMu.Lock()
	defer a.loopReductionMu.Unlock()
	maps.Copy(out, a.lastPreparedLLMDiscardedInputs)
	return out
}

func (a *MainAgent) recalledReductionInputsSnapshot() map[string]struct{} {
	if a == nil {
		return nil
	}
	a.loopReductionMu.Lock()
	defer a.loopReductionMu.Unlock()
	if len(a.recalledReductionInputs) == 0 {
		return nil
	}
	out := make(map[string]struct{}, len(a.recalledReductionInputs))
	for key := range a.recalledReductionInputs {
		out[key] = struct{}{}
	}
	return out
}

func highLevelContextReductionStats(mgr *ctxmgr.Manager, original, reduced []message.Message) ContextReductionStats {
	stats := ContextReductionStats{
		TokensBefore: estimateMessagesTokens(mgr, original),
		TokensAfter:  estimateMessagesTokens(mgr, reduced),
	}
	stats.setCurrentMessageSurface(reduced)
	if stats.TokensBefore > stats.TokensAfter {
		stats.TokensSaved = stats.TokensBefore - stats.TokensAfter
	}
	stats.setCurrentReductionSavings(original, reduced)
	return stats
}

func (s *ContextReductionStats) setCurrentReductionSavings(original, reduced []message.Message) {
	s.Messages = 0
	s.Bytes = 0
	limit := min(len(original), len(reduced))
	for i := range limit {
		saved := contextContributorBytes(original[i]) - contextContributorBytes(reduced[i])
		if saved <= 0 {
			continue
		}
		s.Messages++
		s.Bytes += saved
	}
}

func (s *ContextReductionStats) setCurrentMessageSurface(messages []message.Message) {
	s.CurrentBytes = ctxmgr.MessagePayloadBytes(messages)
	s.CurrentMessages = len(messages)
}

func (a *MainAgent) setCurrentRequestSurface(stats *ContextReductionStats, messages []message.Message) {
	stats.setCurrentMessageSurface(messages)
	if a != nil && a.ctxMgr != nil {
		stats.CurrentBytes += a.ctxMgr.SystemPromptPayloadBytes()
	}
	if a != nil {
		stats.CurrentBytes += toolDefinitionBytes(a.mainLLMToolDefinitions())
	}
}

func (a *MainAgent) fillReductionModelContinuity(stats *ContextReductionStats) {
	if a == nil || stats == nil {
		return
	}
	stats.fillModelContinuity(a.llmModelContinuitySnapshot())
}

func (s *ContextReductionStats) fillModelContinuity(snapshot llmModelContinuitySnapshot) {
	if s == nil {
		return
	}
	s.PreviousModel = snapshot.PreviousModel
	s.ModelChanged = snapshot.PreviousModel != "" && snapshot.CurrentModel != "" && snapshot.PreviousModel != snapshot.CurrentModel
	s.ModelRunLength = snapshot.ProjectedModelRunLength
}

func (a *MainAgent) setContextReductionStats(stats ContextReductionStats) {
	if a == nil {
		return
	}
	a.loopReductionMu.Lock()
	defer a.loopReductionMu.Unlock()
	a.contextReductionStats = cloneContextReductionStats(stats)
}

func cloneContextReductionStats(stats ContextReductionStats) ContextReductionStats {
	stats.ByToolAndRule = cloneContextReductionBuckets(stats.ByToolAndRule)
	stats.SkippedByReason = cloneContextReductionIntMap(stats.SkippedByReason)
	stats.OverCompression = cloneContextReductionIntMap(stats.OverCompression)
	stats.OverCompressionByTool = cloneContextReductionIntMap(stats.OverCompressionByTool)
	stats.ByRetentionLevel = cloneContextReductionIntMap(stats.ByRetentionLevel)
	return stats
}

func cloneContextReductionBuckets(buckets map[string]ContextReductionBucket) map[string]ContextReductionBucket {
	if buckets == nil {
		return nil
	}
	cloned := make(map[string]ContextReductionBucket, len(buckets))
	maps.Copy(cloned, buckets)
	return cloned
}

func cloneContextReductionIntMap(values map[string]int) map[string]int {
	if values == nil {
		return nil
	}
	cloned := make(map[string]int, len(values))
	maps.Copy(cloned, values)
	return cloned
}

func (a *MainAgent) resetContextReductionStats() {
	a.setContextReductionStats(ContextReductionStats{})
}

type contextContributor struct {
	Index  int
	Role   message.Role
	Tool   string
	Bytes  int
	Tokens int
}

func contextContributorLabel(c contextContributor) string {
	if c.Tool != "" {
		return fmt.Sprintf("#%d %s/%s bytes=%d tokens_est=%d", c.Index, c.Role, c.Tool, c.Bytes, c.Tokens)
	}
	return fmt.Sprintf("#%d %s bytes=%d tokens_est=%d", c.Index, c.Role, c.Bytes, c.Tokens)
}

func contextContributorBytes(msg message.Message) int {
	n := len(msg.Content)
	for _, part := range msg.Parts {
		n += len(part.Text)
	}
	for _, tc := range msg.ToolCalls {
		n += len(tc.Args)
	}
	for _, tb := range msg.ThinkingBlocks {
		n += len(tb.Thinking)
	}
	return n
}

func topContextContributors(mgr *ctxmgr.Manager, messages []message.Message, limit int) []contextContributor {
	if limit <= 0 || len(messages) == 0 {
		return nil
	}
	callMeta := buildToolCallMeta(messages)
	contributors := make([]contextContributor, 0, len(messages))
	for i, msg := range messages {
		bytes := contextContributorBytes(msg)
		if bytes <= 0 {
			continue
		}
		toolName := ""
		if msg.Role == message.RoleTool {
			toolName = strings.TrimSpace(callMeta[msg.ToolCallID].Name)
		}
		contributors = append(contributors, contextContributor{
			Index:  i,
			Role:   msg.Role,
			Tool:   toolName,
			Bytes:  bytes,
			Tokens: estimateMessageTokens(mgr, msg),
		})
	}
	sort.SliceStable(contributors, func(i, j int) bool {
		return contributors[i].Bytes > contributors[j].Bytes
	})
	if len(contributors) > limit {
		contributors = contributors[:limit]
	}
	return contributors
}

// clearPreparedReductionCache drops the prepared-request reduction caches
// along with the visible stats derived from them.
func (a *MainAgent) clearPreparedReductionCache() {
	a.clearReductionCache(true)
}

// clearReductionCache resets the transient reduction state. The wrap-up grace
// window always goes; clearVisibleStats additionally drops the cached prepared
// surface, so callers that only close a turn keep the last request's stats
// visible in the UI.
func (a *MainAgent) clearReductionCache(clearVisibleStats bool) {
	if a == nil {
		return
	}
	a.loopReductionMu.Lock()
	defer a.loopReductionMu.Unlock()
	a.wrapUpGraceTurnID = 0
	a.wrapUpGraceRemaining = 0
	if clearVisibleStats {
		a.lastPreparedLLMTurnID = 0
		a.lastPreparedLLMRequestShape = nil
		a.lastPreparedLLMShapeSource = nil
		a.lastPreparedLLMRequestPrefix = nil
		a.lastPreparedLLMReducedIndices = nil
		a.lastPreparedLLMDiscardedInputs = nil
		a.lastPreparedLLMNextReviewAge = nil
		a.lastPreparedLLMToolResults = 0
		a.lastPreparedReductionPolicy = contextReductionPolicy{}
		a.lastPreparedLLMToolDefHash = [sha256.Size]byte{}
		a.lastPreparedReductionStats = ContextReductionStats{}
		a.contextReductionStats = ContextReductionStats{}
		// Recall protection derives from the same conversation the caches
		// describe; dropping it alongside them is conservative — a stale entry
		// could only over-protect, never mis-reduce, but hygiene wins.
		a.recalledReductionInputs = nil
		// Read-only shell verdicts are immutable per ToolCallID; dropping them
		// here only bounds the map across restores and model switches.
		a.shellReadOnlyClass.mu.Lock()
		a.shellReadOnlyClass.verdicts = nil
		a.shellReadOnlyClass.mu.Unlock()
	}
}

func (a *MainAgent) beginContextReductionWrapUpGrace() {
	if a == nil {
		return
	}
	turnID := a.currentTurnID()
	if turnID == 0 {
		return
	}
	requests := a.contextReductionPolicy().WrapUpGraceRequests
	if requests <= 0 {
		return
	}
	a.loopReductionMu.Lock()
	a.wrapUpGraceTurnID = turnID
	a.wrapUpGraceRemaining = requests
	a.loopReductionMu.Unlock()
}

func (a *MainAgent) clearContextReductionWrapUpGrace() {
	if a == nil {
		return
	}
	a.loopReductionMu.Lock()
	a.wrapUpGraceTurnID = 0
	a.wrapUpGraceRemaining = 0
	a.loopReductionMu.Unlock()
}

func (a *MainAgent) consumeContextReductionWrapUpGrace(turnID uint64) bool {
	if a == nil || turnID == 0 {
		return false
	}
	a.loopReductionMu.Lock()
	defer a.loopReductionMu.Unlock()
	if a.wrapUpGraceTurnID != turnID || a.wrapUpGraceRemaining <= 0 {
		return false
	}
	a.wrapUpGraceRemaining--
	if a.wrapUpGraceRemaining == 0 {
		a.wrapUpGraceTurnID = 0
	}
	return true
}

func (a *MainAgent) GetContextReductionStats() ContextReductionStats {
	if a == nil {
		return ContextReductionStats{}
	}
	if sub := a.validFocusedSubAgent(); sub != nil {
		return sub.GetContextReductionStats()
	}
	return a.currentMainContextReductionStats()
}

func (a *MainAgent) currentMainContextReductionStats() ContextReductionStats {
	if a == nil {
		return ContextReductionStats{}
	}
	a.loopReductionMu.Lock()
	defer a.loopReductionMu.Unlock()
	return cloneContextReductionStats(a.contextReductionStats)
}

func isZeroContextReductionStats(stats ContextReductionStats) bool {
	return stats.Messages == 0 &&
		stats.Bytes == 0 &&
		stats.TokensSaved == 0 &&
		!stats.Protected &&
		!stats.ReusedStable &&
		len(stats.ByToolAndRule) == 0 &&
		len(stats.SkippedByReason) == 0 &&
		len(stats.OverCompression) == 0 &&
		len(stats.OverCompressionByTool) == 0
}

func (a *MainAgent) preparedContextReductionStatsForTurn(turnID uint64) ContextReductionStats {
	if a == nil || turnID == 0 {
		return ContextReductionStats{}
	}
	a.loopReductionMu.Lock()
	defer a.loopReductionMu.Unlock()
	if a.lastPreparedLLMTurnID != turnID {
		return ContextReductionStats{}
	}
	return cloneContextReductionStats(a.lastPreparedReductionStats)
}

// noteContextSurfaceIdentityChanged drops the prepared-request caches when the
// identity behind the request surface changes (model switch, key rotation).
// The cached surface was produced under the previous identity, so neither its
// reduced prefix nor the model-run counter may carry over.
func (a *MainAgent) noteContextSurfaceIdentityChanged() {
	if a == nil {
		return
	}
	a.clearPreparedReductionCache()
	a.resetLLMModelRun()
}

func cloneMessageSliceForRequestShape(messages []message.Message) []message.Message {
	if len(messages) == 0 {
		return nil
	}
	cloned := make([]message.Message, len(messages))
	for i := range messages {
		cloned[i] = cloneMessageForRequestShape(messages[i])
	}
	return cloned
}

func cloneMessageForRequestShape(msg message.Message) message.Message {
	cloned := msg
	if len(msg.Parts) > 0 {
		cloned.Parts = cloneContentParts(msg.Parts)
	}
	if len(msg.ToolCalls) > 0 {
		cloned.ToolCalls = make([]message.ToolCall, len(msg.ToolCalls))
		for i, tc := range msg.ToolCalls {
			cloned.ToolCalls[i] = tc
			if len(tc.Args) > 0 {
				cloned.ToolCalls[i].Args = append([]byte(nil), tc.Args...)
			}
		}
	}
	if len(msg.ThinkingBlocks) > 0 {
		cloned.ThinkingBlocks = append([]message.ThinkingBlock(nil), msg.ThinkingBlocks...)
	}
	if len(msg.ResponsesOutput) > 0 {
		cloned.ResponsesOutput = make([]message.ResponsesOutputItem, len(msg.ResponsesOutput))
		copy(cloned.ResponsesOutput, msg.ResponsesOutput)
		for i := range cloned.ResponsesOutput {
			cloned.ResponsesOutput[i].Content = append([]message.ResponsesOutputContent(nil), msg.ResponsesOutput[i].Content...)
			cloned.ResponsesOutput[i].Summary = append([]message.ResponsesReasoningSummary(nil), msg.ResponsesOutput[i].Summary...)
		}
	}
	if len(msg.GeminiParts) > 0 {
		cloned.GeminiParts = append([]message.GeminiReplayPart(nil), msg.GeminiParts...)
	}
	cloned.CompactionFileRevisions = cloneCompactionFileRevisions(msg.CompactionFileRevisions)
	if msg.FileState != nil {
		cloned.FileState = msg.FileState.Clone()
	}
	if len(msg.ToolChangedPaths) > 0 {
		cloned.ToolChangedPaths = append([]string(nil), msg.ToolChangedPaths...)
	}
	if len(msg.LSPReviews) > 0 {
		cloned.LSPReviews = append([]message.LSPReview(nil), msg.LSPReviews...)
	}
	if msg.Audit != nil {
		cloned.Audit = msg.Audit.Clone()
	}
	if msg.Provenance != nil {
		cloned.Provenance = cloneProvenance(msg.Provenance)
	}
	if msg.Usage != nil {
		usage := *msg.Usage
		cloned.Usage = &usage
	}
	return cloned
}

func buildToolCallMeta(messages []message.Message) map[string]toolCallMeta {
	meta := make(map[string]toolCallMeta)
	for _, msg := range messages {
		for _, tc := range msg.ToolCalls {
			meta[tc.ID] = toolCallMeta{Name: tools.NormalizeName(tc.Name), Args: string(tc.Args)}
		}
	}
	return meta
}

// requestBatchesAfter reports tool-result age in main-model request batches.
// Persisted request IDs preserve failed-request gaps; legacy histories without
// IDs fall back to counting later assistant responses. All parallel tool calls
// declared by one assistant message share its request batch.
func requestBatchesAfter(messages []message.Message, currentBatch uint64) []int {
	ages := make([]int, len(messages))
	batchByToolCall := make(map[string]uint64)
	for _, msg := range messages {
		if msg.Role != message.RoleAssistant || msg.RequestBatch == 0 {
			continue
		}
		for _, call := range msg.ToolCalls {
			batchByToolCall[call.ID] = msg.RequestBatch
		}
	}
	seenAssistants := 0
	seenUsers := 0
	for i := len(messages) - 1; i >= 0; i-- {
		ages[i] = max(seenAssistants, seenUsers)
		batch := messages[i].RequestBatch
		if messages[i].Role == message.RoleTool {
			batch = batchByToolCall[messages[i].ToolCallID]
		}
		if batch > 0 && currentBatch >= batch {
			ages[i] = int(currentBatch - batch)
		}
		if messages[i].Role == message.RoleAssistant {
			seenAssistants++
		}
		if messages[i].Role == message.RoleUser {
			seenUsers++
		}
	}
	return ages
}

// detectRepeatedToolOutputs marks tool results whose content survives verbatim
// in a later call with identical arguments. Identical arguments alone are not
// enough: a command can be deterministic (grep, cat) or time-varying (git
// status, go test, ls), and for the latter the older output is the "before"
// state the model may still be comparing against. Collapsing it to a marker
// that asserts the calls are identical would delete evidence and misstate what
// remains, so a differing older copy is left to the normal shape-based rules,
// which summarize it without claiming a fresher copy carries it.
func detectRepeatedToolOutputs(messages []message.Message, meta map[string]toolCallMeta) map[int]bool {
	repeated := make(map[int]bool)
	seen := make(map[string][sha256.Size]byte)
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		if msg.Role != message.RoleTool {
			continue
		}
		call, ok := meta[msg.ToolCallID]
		if !ok {
			continue
		}
		key := contextReductionToolInputKey(call.Name, call.Args)
		if digest, established := seen[key]; established {
			if digest == stableReductionHashString(msg.Content) {
				repeated[i] = true
			}
			// The newest trustworthy copy stays the comparison base: it is the
			// one the marker points at, and re-basing on an intermediate copy
			// would make the assertion false for everything before it.
			continue
		}
		// Only a trustworthy result establishes "a fresher identical output
		// exists later": explicit failures/cancellations must not make the
		// repeated marker point at an unsuccessful run. Content sniffing is
		// reserved for status-less legacy transcripts (matching the rendered
		// "Error:" prefix, as in classifyRequestReductionToolOutput): an
		// explicit success that merely mentions "Error:" mid-output — a grep
		// over error handling, a log dump — is still a trustworthy copy.
		trustworthy := isToolResultSuccessStatus(msg.ToolStatus) ||
			(strings.TrimSpace(msg.ToolStatus) == "" && !isToolErrorContent(msg.Content))
		if trustworthy {
			seen[key] = stableReductionHashString(msg.Content)
		}
	}
	return repeated
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
