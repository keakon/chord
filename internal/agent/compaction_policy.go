package agent

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"maps"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/keakon/chord/internal/ctxmgr"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/ratelimit"
	"github.com/keakon/chord/internal/recovery"
	"github.com/keakon/chord/internal/session"
	"github.com/keakon/chord/internal/toolname"
	"github.com/keakon/chord/internal/tools"
)

func evidencePackTokenBudget(contextLimit int) int {
	if contextLimit <= 0 {
		return compactEvidenceMaxTokens
	}
	b := contextLimit * compactEvidencePercentNumer / compactEvidencePercentDenom
	b = max(b, compactEvidenceMinTokens)
	b = min(b, compactEvidenceMaxTokens)
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
	a.clearLoopReductionCache(true)
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
			stats := ContextReductionStats{TokensBefore: ctxmgr.EstimateMessagesTokens(messages)}
			stats.TokensAfter = stats.TokensBefore
			a.fillReductionModelContinuity(&stats)
			a.setContextReductionStats(stats)
		}
		return messages
	}
	scan := newReductionHistoryScan(messages)
	currentBatch := a.currentRequestBatch(messages)
	externalReadInvalidated := a.externallyInvalidatedReadsAfterMutatingShell(messages, scan)
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
						stats := highLevelContextReductionStats(messages, reused)
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
		loopEnabled, frozen := a.contextSurfaceReductionSnapshot()
		if loopEnabled && (len(frozen.Messages) == 0 || !stableReductionSurfaceNeedsReview(frozen, scan, currentBatch, externalReadInvalidated)) {
			prepared := append([]message.Message(nil), messages...)
			return a.applyLoopFrozenReductionPrefix(prepared, frozen)
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
	stats := ContextReductionStats{TokensBefore: ctxmgr.EstimateMessagesTokens(prepared)}

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
		beforeTokens := ctxmgr.EstimateMessageTokens(message.Message{Content: original})
		afterTokens := ctxmgr.EstimateMessageTokens(message.Message{Content: reduced})
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
	noteOverCompression := func(kind string) {
		if kind == "" {
			return
		}
		if stats.OverCompression == nil {
			stats.OverCompression = make(map[string]int)
		}
		stats.OverCompression[kind]++
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
	if len(externalReadInvalidated) > 0 && readValidityByIndex == nil {
		readValidityByIndex = make(map[int]readValidity, len(externalReadInvalidated))
	}
	for index := range externalReadInvalidated {
		validity := readValidityByIndex[index]
		validity.Invalidated = true
		validity.Superseded = false
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
				ToolName:        toolName,
				Meta:            meta,
				Content:         messages[i].Content,
				ToolStatus:      messages[i].ToolStatus,
				FileState:       messages[i].FileState,
				Age:             age,
				Policy:          policy,
				ToolResults:     toolResults,
				ReadInvalidated: validity.Invalidated,
				ReadSuperseded:  validity.Superseded,
			}
			reduced, rule, ok := reduceRequestToolOutput(requestReductionReadLike, ctx)
			if ok {
				proposals = append(proposals, reductionProposal{
					index:      i,
					class:      requestReductionReadLike,
					toolName:   toolName,
					rule:       rule,
					reduced:    reduced,
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
				ToolName:        toolName,
				Meta:            meta,
				Content:         messages[i].Content,
				ToolStatus:      messages[i].ToolStatus,
				FileState:       messages[i].FileState,
				Age:             age,
				Policy:          policy,
				ToolResults:     toolResults,
				ReadInvalidated: true,
			}
			if reduced, rule, ok := reduceRequestToolOutput(requestReductionReadLike, ctx); ok {
				proposals = append(proposals, reductionProposal{
					index:      i,
					class:      requestReductionReadLike,
					toolName:   toolName,
					rule:       rule,
					reduced:    reduced,
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
			ToolName:        toolName,
			Meta:            meta,
			Content:         prepared[i].Content,
			ToolStatus:      prepared[i].ToolStatus,
			FileState:       prepared[i].FileState,
			Age:             age,
			Policy:          policy,
			Repeated:        repeated[i],
			ToolResults:     toolResults,
			ShellReadOnly:   toolName == tools.NameShell && a.shellCommandReadOnly(prepared[i].ToolCallID, meta.Args),
			ReadInvalidated: validity.Invalidated,
			ReadSuperseded:  validity.Superseded,
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
		class := classifyRequestReductionToolOutput(ctx)
		if class == requestReductionNone {
			nextReviewAge[i] = nextContextReductionReviewAge(ctx)
			if age < policy.HighRiskProtectAgeTurns && isHighRiskToolOutput(ctx) {
				noteSkip(contextReductionSkipRecentHighRisk)
			} else if len(prepared[i].Content) > policy.StaleOutputBytes {
				noteSkip(contextReductionSkipLargeUnreduced)
			}
			if discardedID, discardedBefore := discardedInputs[inputKey]; discardedBefore && discardedID != prepared[i].ToolCallID {
				if contentFetch {
					noteRecalledInput(inputKey)
				}
				if contextReductionIsReadLike(toolName) {
					noteOverCompression(contextReductionOverCompressionReread)
					if toolName == tools.NameRead {
						previousRevision := discardedReadRevisions[inputKey]
						currentRevision := reductionReadRevision(&meta, prepared[i].FileState)
						if previousRevision != "" && currentRevision != "" {
							if previousRevision == currentRevision {
								noteOverCompression(contextReductionOverCompressionRereadSameRevision)
							} else {
								noteOverCompression(contextReductionOverCompressionRereadChangedRevision)
							}
						}
					}
				} else if looksLikeSearchResult(ctx) {
					noteOverCompression(contextReductionOverCompressionResearch)
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
			saved := ctxmgr.EstimateMessageTokens(message.Message{Content: prepared[p.index].Content}) -
				ctxmgr.EstimateMessageTokens(message.Message{Content: p.reduced})
			if saved > 0 {
				pendingSaved += saved
			}
		}
		if earliestBoundary >= 0 {
			cacheInvalidAnyway := modelSnapshot.ProjectedModelRunLength <= 1
			tailTokens := ctxmgr.EstimateMessagesTokens(prepared[earliestBoundary:])
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
	}

	if a != nil {
		stats.EvidenceRebuildDurationUS = evidenceStats.DurationUS
		stats.EvidenceFiles = evidenceStats.Files
		stats.EvidenceObservations = evidenceStats.Observations
		stats.EvidenceCurrent = evidenceStats.Current
		stats.EvidenceStale = evidenceStats.Stale
		stats.EvidenceSuperseded = evidenceStats.Superseded
		stats.TokensAfter = ctxmgr.EstimateMessagesTokens(prepared)
		a.setCurrentRequestSurface(&stats, prepared)
		if stats.TokensSaved == 0 && stats.TokensBefore > stats.TokensAfter {
			stats.TokensSaved = stats.TokensBefore - stats.TokensAfter
		}
		if !semanticRefresh && wrapUpGraceActive && stats.TokensSaved < policy.MinIncrementalTokens {
			preserved := ContextReductionStats{
				TokensBefore:  stats.TokensBefore,
				TokensAfter:   stats.TokensBefore,
				Protected:     true,
				ProtectReason: contextProtectReasonWrapUpGrace,
			}
			preserved.fillModelContinuity(modelSnapshot)
			a.setCurrentRequestSurface(&preserved, messages)
			a.setContextReductionStats(preserved)
			if rememberPrepared {
				a.rememberPreparedLLMRequest(a.currentTurnID(), messages, messages, nil, nextReviewAge, toolResults, policy)
			}
			return messages
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
						reusedStats := highLevelContextReductionStats(messages, prepared)
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
					hash, exists, err := verifiedCurrentFileHash(path)
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
// differs from the one used by the previous prepared LLM request. The frozen
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
// comparison is O(1) and allocation-free. Without a source (loop-frozen
// surfaces) it falls back to hashing the current prefix.
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
	tailTokens := ctxmgr.EstimateMessagesTokens(messages[len(previous.Messages):])
	if tailTokens >= policy.MinIncrementalTokens {
		return nil, ContextReductionStats{}, false
	}
	reused, compatible := reuseStableReductionPrefix(previous, messages, messages)
	if !compatible {
		return nil, ContextReductionStats{}, false
	}
	stats := highLevelContextReductionStats(messages, reused)
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

func highLevelContextReductionStats(original, reduced []message.Message) ContextReductionStats {
	stats := ContextReductionStats{
		TokensBefore: ctxmgr.EstimateMessagesTokens(original),
		TokensAfter:  ctxmgr.EstimateMessagesTokens(reduced),
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

func topContextContributors(messages []message.Message, limit int) []contextContributor {
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
			Tokens: ctxmgr.EstimateMessageTokens(msg),
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

func (a *MainAgent) clearLoopFrozenReductionPrefix() {
	a.clearLoopReductionCache(true)
}

func (a *MainAgent) clearLoopReductionCache(clearVisibleStats bool) {
	if a == nil {
		return
	}
	a.loopReductionMu.Lock()
	defer a.loopReductionMu.Unlock()
	a.loopState.FrozenReductionPrefix = nil
	a.loopState.FrozenReductionShape = nil
	a.loopState.FrozenReductionReducedIndices = nil
	a.loopState.FrozenReductionNextReviewAge = nil
	a.loopState.FrozenReductionToolResults = 0
	a.loopState.FrozenReductionPolicy = contextReductionPolicy{}
	a.loopState.FrozenReductionToolDefHash = [sha256.Size]byte{}
	a.loopState.FrozenReductionStats = ContextReductionStats{}
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
		len(stats.OverCompression) == 0
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

func (a *MainAgent) freezeLoopReductionPrefixForCurrentTurn() {
	if a == nil {
		return
	}
	turnID := a.currentTurnID()
	if turnID == 0 {
		return
	}
	a.loopReductionMu.Lock()
	defer a.loopReductionMu.Unlock()
	if a.lastPreparedLLMTurnID != turnID || len(a.lastPreparedLLMRequestPrefix) == 0 {
		a.lastPreparedLLMRequestShape = nil
		a.lastPreparedLLMShapeSource = nil
		a.lastPreparedLLMRequestPrefix = nil
		a.lastPreparedLLMReducedIndices = nil
		// lastPreparedLLMDiscardedInputs survives: it is session-scoped recall
		// evidence, not a property of the invalidated request snapshot.
		a.lastPreparedLLMToolDefHash = [sha256.Size]byte{}
		a.lastPreparedReductionStats = ContextReductionStats{}
		return
	}
	a.loopState.FrozenReductionShape = append([]stableReductionMessageShape(nil), a.lastPreparedLLMRequestShape...)
	a.loopState.FrozenReductionPrefix = cloneMessageSliceForRequestShape(a.lastPreparedLLMRequestPrefix)
	a.loopState.FrozenReductionReducedIndices = append([]bool(nil), a.lastPreparedLLMReducedIndices...)
	a.loopState.FrozenReductionNextReviewAge = append([]int(nil), a.lastPreparedLLMNextReviewAge...)
	a.loopState.FrozenReductionToolResults = a.lastPreparedLLMToolResults
	a.loopState.FrozenReductionPolicy = a.lastPreparedReductionPolicy
	a.loopState.FrozenReductionToolDefHash = a.lastPreparedLLMToolDefHash
	a.loopState.FrozenReductionStats = cloneContextReductionStats(a.lastPreparedReductionStats)
	a.contextReductionStats = cloneContextReductionStats(a.lastPreparedReductionStats)
}

func (a *MainAgent) contextSurfaceReductionSnapshot() (enabled bool, frozen stableReductionSurface) {
	if a == nil {
		return false, stableReductionSurface{}
	}
	if !a.shouldFreezeLLMContextSurface() {
		return false, stableReductionSurface{}
	}
	turnID := a.currentTurnID()
	a.loopReductionMu.Lock()
	defer a.loopReductionMu.Unlock()
	if len(a.loopState.FrozenReductionPrefix) == 0 && turnID != 0 && a.lastPreparedLLMTurnID == turnID {
		a.loopState.FrozenReductionShape = append([]stableReductionMessageShape(nil), a.lastPreparedLLMRequestShape...)
		a.loopState.FrozenReductionPrefix = cloneMessageSliceForRequestShape(a.lastPreparedLLMRequestPrefix)
		a.loopState.FrozenReductionReducedIndices = append([]bool(nil), a.lastPreparedLLMReducedIndices...)
		a.loopState.FrozenReductionNextReviewAge = append([]int(nil), a.lastPreparedLLMNextReviewAge...)
		a.loopState.FrozenReductionToolResults = a.lastPreparedLLMToolResults
		a.loopState.FrozenReductionPolicy = a.lastPreparedReductionPolicy
		a.loopState.FrozenReductionToolDefHash = a.lastPreparedLLMToolDefHash
		a.loopState.FrozenReductionStats = cloneContextReductionStats(a.lastPreparedReductionStats)
	}
	return true, stableReductionSurface{
		Messages:       cloneMessageSliceForRequestShape(a.loopState.FrozenReductionPrefix),
		Shape:          append([]stableReductionMessageShape(nil), a.loopState.FrozenReductionShape...),
		Stats:          cloneContextReductionStats(a.loopState.FrozenReductionStats),
		ReducedIndices: append([]bool(nil), a.loopState.FrozenReductionReducedIndices...),
		NextReviewAge:  append([]int(nil), a.loopState.FrozenReductionNextReviewAge...),
		ToolResults:    a.loopState.FrozenReductionToolResults,
		Policy:         a.loopState.FrozenReductionPolicy,
		ToolDefHash:    a.loopState.FrozenReductionToolDefHash,
	}
}

func (a *MainAgent) allowContextSurfaceRefreshAtUserBoundary() {
	if a == nil {
		return
	}
	a.contextSurfaceRefreshAllowed.Store(true)
}

func (a *MainAgent) noteContextSurfaceIdentityChanged() {
	if a == nil {
		return
	}
	a.clearLoopFrozenReductionPrefix()
	a.resetLLMModelRun()
	a.contextSurfaceRefreshAllowed.Store(true)
}

func (a *MainAgent) consumeContextSurfaceRefreshAllowance() bool {
	if a == nil {
		return false
	}
	return a.contextSurfaceRefreshAllowed.Swap(false)
}

func (a *MainAgent) shouldFreezeLLMContextSurface() bool {
	if a == nil {
		return false
	}
	if a.contextSurfaceRefreshAllowed.Load() {
		return false
	}
	providerName := a.mainRateLimitProviderName()
	if providerName == "" || !a.providerUsesCodexRateLimit(providerName) {
		return false
	}
	return codexQuotaRemainingAtMostTenPercent(a.mainRateLimitSnapshot())
}

func codexQuotaRemainingAtMostTenPercent(snap *ratelimit.KeyRateLimitSnapshot) bool {
	if snap == nil {
		return false
	}
	for _, window := range []*ratelimit.RateLimitWindow{snap.Primary, snap.Secondary} {
		if window == nil {
			continue
		}
		used := window.UsedPercent()
		if used >= 90 {
			return true
		}
	}
	return false
}

func (a *MainAgent) applyLoopFrozenReductionPrefix(prepared []message.Message, frozen stableReductionSurface) []message.Message {
	if a == nil {
		return prepared
	}
	if len(frozen.Messages) == 0 {
		stats := ContextReductionStats{}
		a.setCurrentRequestSurface(&stats, prepared)
		a.setContextReductionStats(stats)
		return prepared
	}
	original := cloneMessageSliceForRequestShape(prepared)
	reused, compatible := reuseStableReductionPrefix(frozen, prepared, prepared)
	if !compatible {
		stats := ContextReductionStats{}
		a.setCurrentRequestSurface(&stats, prepared)
		a.setContextReductionStats(stats)
		return prepared
	}
	prepared = reused
	updatedStats := highLevelContextReductionStats(original, prepared)
	if len(updatedStats.ByToolAndRule) == 0 {
		updatedStats.ByToolAndRule = cloneContextReductionBuckets(frozen.Stats.ByToolAndRule)
	}
	updatedStats.Protected = frozen.Stats.Protected
	updatedStats.ReusedStable = true
	a.setCurrentRequestSurface(&updatedStats, prepared)
	a.setContextReductionStats(updatedStats)
	a.setPreparedStablePrefixLen(len(frozen.Messages))
	return prepared
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

func detectRepeatedToolOutputs(messages []message.Message, meta map[string]toolCallMeta) map[int]bool {
	repeated := make(map[int]bool)
	seen := make(map[string]bool)
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
		if seen[key] {
			repeated[i] = true
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
			seen[key] = true
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

func normalizeMessagesForSummary(messages []message.Message) []message.Message {
	normalized := make([]message.Message, len(messages))
	copy(normalized, messages)
	for i := range normalized {
		if len(normalized[i].Parts) == 0 {
			continue
		}
		var textParts []string
		imageCount := 0
		pdfCount := 0
		for _, part := range normalized[i].Parts {
			switch part.Type {
			case "text":
				if strings.TrimSpace(part.Text) != "" {
					textParts = append(textParts, part.Text)
				}
			case "image":
				imageCount++
			case "pdf":
				pdfCount++
			}
		}
		if imageCount > 0 {
			textParts = append(textParts, fmt.Sprintf("[User included %d image attachment(s)]", imageCount))
		}
		if pdfCount > 0 {
			textParts = append(textParts, fmt.Sprintf("[User included %d PDF attachment(s)]", pdfCount))
		}
		normalized[i].Parts = nil
		joined := strings.TrimSpace(strings.Join(textParts, "\n"))
		if joined != "" {
			normalized[i].Content = joined
		}
	}
	return normalized
}

func trimMessagesToBudget(messages []message.Message, targetTokens int) ([]message.Message, int) {
	if len(messages) == 0 || targetTokens <= 0 {
		return nil, len(messages)
	}
	if ctxmgr.EstimateMessagesTokens(messages) <= targetTokens {
		out := make([]message.Message, len(messages))
		copy(out, messages)
		return out, 0
	}

	start := len(messages)
	remaining := targetTokens
	for i, message := range slices.Backward(messages) {
		cost := ctxmgr.EstimateMessageTokens(message)
		if remaining-cost < 0 {
			break
		}
		remaining -= cost
		start = i
	}
	start = ctxmgr.SafeKeepBoundary(messages, start)
	if start <= 0 || start >= len(messages) {
		return nil, len(messages)
	}

	out := make([]message.Message, 0, len(messages[start:])+1)
	omitted := start
	out = append(out, message.Message{
		Role: message.RoleUser,
		Content: fmt.Sprintf(
			"[system] The earliest %d messages from the compacted history were omitted from the summary input to fit the compression model budget. The exported history file remains authoritative for those details.",
			omitted,
		),
	})
	out = append(out, messages[start:]...)
	return out, omitted
}

func compactionInputBudget(contextLimit int) int {
	if contextLimit <= 0 {
		return 0
	}
	reservedOutput := min(compactReservedOutput, contextLimit/8)
	reservedOutput = max(reservedOutput, 2048)
	preflightBuffer := max(contextLimit/compactPreflightBufferRatio, compactPreflightBufferMin)
	budget := contextLimit - compactPromptOverhead - reservedOutput - preflightBuffer
	budget = max(budget, contextLimit/compactBudgetRatio)
	return budget
}

type compactionInput struct {
	Transcript       string
	OmittedMessages  int
	EvidenceItems    []evidenceItem
	RecentTail       []message.Message
	RecentTailAnchor string
	GoalAnchor       string
	ConstraintAnchor string
	DecisionAnchor   string
	ProgressAnchor   string
}

func buildCompactionInputWithOptions(head []message.Message, contextLimit int, evidenceItems []evidenceItem, recentTail []message.Message, autoRecentTail bool) (*compactionInput, error) {
	pruned := (&MainAgent{}).prepareMessagesForLLM(head)
	normalized := normalizeMessagesForSummary(pruned)
	budget := compactionInputBudget(contextLimit)
	trimmed, omittedMessages := trimMessagesToBudget(normalized, budget)
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("compaction input too large even after truncation")
	}
	exported, err := session.Export(trimmed, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("build compaction transcript: %w", err)
	}
	if len(evidenceItems) == 0 {
		evidenceItems = selectEvidenceItems(normalized, contextLimit)
	}
	if autoRecentTail && len(recentTail) == 0 {
		recentTail = selectRecentTailMessages(normalized, compactRecentTailTurns, compactRecentTailMaxTokens)
	}
	return &compactionInput{
		Transcript:       session.ExportToMarkdown(exported),
		OmittedMessages:  omittedMessages,
		EvidenceItems:    evidenceItems,
		RecentTail:       recentTail,
		RecentTailAnchor: formatRecentTailAnchor(recentTail),
		GoalAnchor:       buildGoalAnchor(normalized),
		ConstraintAnchor: buildConstraintAnchor(evidenceItems),
		DecisionAnchor:   buildDecisionAnchor(normalized),
		ProgressAnchor:   buildProgressAnchor(normalized, evidenceItems),
	}, nil
}

func trimMessagesToBudgetWithReservedTail(messages []message.Message, targetTokens int, reserveTail int) ([]message.Message, int) {
	if reserveTail <= 0 {
		return trimMessagesToBudget(messages, targetTokens)
	}
	trimmed, omitted := trimMessagesToBudget(messages, targetTokens-reserveTail)
	if len(trimmed) > 0 {
		return trimmed, omitted
	}
	return trimMessagesToBudget(messages, targetTokens)
}

func compactionPromptTokenEstimate(input *compactionInput, relHistoryPath string, keyFiles []string, todos []tools.TodoItem, subAgents []SubAgentInfo, backgroundObjects []recovery.BackgroundObjectState) int {
	prompt := buildCompactionPromptWithKeyFiles(input, relHistoryPath, keyFiles, todos, subAgents, backgroundObjects)
	return max(1, len(prompt)/3)
}

func fitCompactionInputToContextLimit(head []message.Message, input *compactionInput, contextLimit int, relHistoryPath string, keyFiles []string, todos []tools.TodoItem, subAgents []SubAgentInfo, backgroundObjects []recovery.BackgroundObjectState, maxOutputTokens int) (*compactionInput, error) {
	if input == nil {
		return nil, fmt.Errorf("compaction input is nil")
	}
	if contextLimit <= 0 {
		return input, nil
	}
	preflightBuffer := max(contextLimit/compactPreflightBufferRatio, compactPreflightBufferMin)
	allowedInput := contextLimit - maxOutputTokens - preflightBuffer
	if allowedInput <= 0 {
		return nil, fmt.Errorf("compaction context limit too small after reserving output (%d)", contextLimit)
	}
	if compactionPromptTokenEstimate(input, relHistoryPath, keyFiles, todos, subAgents, backgroundObjects) <= allowedInput {
		return input, nil
	}
	pruned := (&MainAgent{}).prepareMessagesForLLM(head)
	normalized := normalizeMessagesForSummary(pruned)
	budget := compactionInputBudget(contextLimit)
	for attempts := range 6 {
		trimmed, omittedMessages := trimMessagesToBudgetWithReservedTail(normalized, budget, attempts*512)
		if len(trimmed) == 0 {
			continue
		}
		exported, err := session.Export(trimmed, nil, nil)
		if err != nil {
			return nil, fmt.Errorf("build compaction transcript during fit: %w", err)
		}
		candidate := *input
		candidate.Transcript = session.ExportToMarkdown(exported)
		candidate.OmittedMessages = omittedMessages
		if compactionPromptTokenEstimate(&candidate, relHistoryPath, keyFiles, todos, subAgents, backgroundObjects) <= allowedInput {
			return &candidate, nil
		}
		budget -= max(512, budget/8)
		if budget <= contextLimit/8 {
			break
		}
	}
	return nil, fmt.Errorf("compaction prompt still exceeds reserved context budget")
}

func buildGoalAnchor(messages []message.Message) string {
	for _, msg := range slices.Backward(messages) {

		if !message.IsUserAuthored(msg) {
			continue
		}
		text := strings.TrimSpace(msg.Content)
		if len(msg.Parts) > 0 {
			normalized := normalizeMessagesForSummary([]message.Message{msg})
			if len(normalized) > 0 {
				text = strings.TrimSpace(normalized[0].Content)
			}
		}
		if !isPlainUserRequestForCompaction(text) {
			continue
		}
		return "- " + strings.ReplaceAll(compactTextSnippet(text, 300), "\n", " ")
	}
	return "- (not confidently recoverable from retained head)"
}

func buildConstraintAnchor(items []evidenceItem) string {
	var lines []string
	for _, item := range items {
		if item.Kind != evidenceUserCorrection {
			continue
		}
		lines = append(lines, "- "+strings.ReplaceAll(compactTextSnippet(item.Excerpt, 220), "\n", " "))
	}
	if len(lines) == 0 {
		return "- (none extracted)"
	}
	return strings.Join(lines, "\n")
}

func buildDecisionAnchor(messages []message.Message) string {
	var lines []string
	for _, msg := range messages {
		if msg.Role != message.RoleAssistant {
			continue
		}
		text := strings.TrimSpace(msg.Content)
		if text == "" {
			continue
		}
		lower := strings.ToLower(text)
		if strings.Contains(lower, "decid") || strings.Contains(lower, "plan") || strings.Contains(text, "方案") || strings.Contains(text, "决定") {
			lines = append(lines, "- "+strings.ReplaceAll(compactTextSnippet(text, 220), "\n", " "))
			if len(lines) >= 3 {
				break
			}
		}
	}
	if len(lines) == 0 {
		return "- (none explicitly extracted; infer from progress and evidence)"
	}
	return strings.Join(lines, "\n")
}

func buildProgressAnchor(messages []message.Message, items []evidenceItem) string {
	var lines []string
	for _, item := range items {
		if item.Kind == evidenceToolDiff || item.Kind == evidenceToolError || item.Kind == evidenceEscalate || item.Kind == evidenceSubAgentDone {
			lines = append(lines, "- "+item.Title+": "+strings.ReplaceAll(compactTextSnippet(item.Excerpt, 180), "\n", " "))
		}
		if len(lines) >= 4 {
			break
		}
	}
	if len(lines) == 0 {
		for i := len(messages) - 1; i >= 0 && len(lines) < 3; i-- {
			msg := messages[i]
			if msg.Role != message.RoleAssistant && msg.Role != message.RoleTool {
				continue
			}
			text := strings.TrimSpace(msg.Content)
			if text == "" {
				continue
			}
			lines = append(lines, "- "+strings.ReplaceAll(compactTextSnippet(text, 180), "\n", " "))
		}
	}
	if len(lines) == 0 {
		return "- (none extracted)"
	}
	return strings.Join(lines, "\n")
}

func formatCompactionAnchorsForPrompt(input *compactionInput) string {
	if input == nil {
		return "Latest user request anchor:\n- (none)\n\nConstraint anchor:\n- (none)\n\nDecision anchor:\n- (none)\n\nRecent progress anchor:\n- (none)"
	}
	return strings.Join([]string{
		"Latest user request anchor:\n" + input.GoalAnchor,
		"Constraint anchor:\n" + input.ConstraintAnchor,
		"Decision anchor:\n" + input.DecisionAnchor,
		"Recent progress anchor:\n" + input.ProgressAnchor,
	}, "\n\n")
}

func validateCompactionSummary(summary string) error {
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return fmt.Errorf("compaction model returned empty summary")
	}
	if containsThinkingTag(summary) {
		return fmt.Errorf("compaction summary contains private thinking tags")
	}
	if len([]rune(summary)) < compactSummaryMinChars {
		return fmt.Errorf("compaction summary too short (%d chars)", len([]rune(summary)))
	}
	positions := compactionHeadingPositions(summary)
	matched := len(positions)
	if matched < len(compactionRequiredHeadings) {
		return fmt.Errorf("compaction summary missing required sections (%d/%d)", matched, len(compactionRequiredHeadings))
	}
	for i := 1; i < len(positions); i++ {
		if positions[i] <= positions[i-1] {
			return fmt.Errorf("compaction summary sections out of order")
		}
	}
	if strings.TrimSpace(summary[:positions[0]]) != "" {
		return fmt.Errorf("compaction summary has content before first required section")
	}
	if err := validateCompactionNextStep(summary, positions[len(positions)-1]); err != nil {
		return err
	}
	if err := validateCompactionTodoState(summary); err != nil {
		return err
	}
	return nil
}

var (
	compactionMarkdownHeadingLineRe      = regexp.MustCompile(`(?m)^##\s+`)
	compactionRequiredHeadingLineRegexps = buildCompactionRequiredHeadingLineRegexps()
)

func buildCompactionRequiredHeadingLineRegexps() map[string]*regexp.Regexp {
	patterns := make(map[string]*regexp.Regexp, len(compactionRequiredHeadings))
	for _, heading := range compactionRequiredHeadings {
		patterns[heading] = regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(heading) + `\s*$`)
	}
	return patterns
}

func validateCompactionNextStep(summary string, nextStepPos int) error {
	if nextStepPos < 0 || nextStepPos >= len(summary) {
		return fmt.Errorf("compaction summary missing next step")
	}
	section := strings.TrimSpace(summary[nextStepPos+len("## Next Step"):])
	if section == "" {
		return fmt.Errorf("compaction summary next step is empty")
	}
	if isVagueCompactionNextStep(section) {
		return fmt.Errorf("compaction summary next step is too vague")
	}
	return nil
}

func isVagueCompactionNextStep(section string) bool {
	normalized := strings.ToLower(strings.TrimSpace(section))
	for strings.HasPrefix(normalized, "-") {
		normalized = strings.TrimSpace(strings.TrimPrefix(normalized, "-"))
	}
	normalized = strings.Trim(strings.TrimSuffix(normalized, "."), " ")
	return slices.Contains([]string{
		"continue",
		"continue working",
		"continue the task",
		"keep working",
		"proceed",
		"resume",
		"resume work",
		"carry on",
	}, normalized)
}

func validateCompactionTodoState(summary string) error {
	section, ok := markdownSection(summary, "## Todo State")
	if !ok {
		return nil
	}
	active := todoSubsectionLines(section, "Active/relevant to latest request")
	completed := todoSubsectionLines(section, "Completed/background")
	stale := todoSubsectionLines(section, "Stale/superseded")
	if len(active) == 0 {
		return nil
	}
	inactive := append(completed, stale...)
	for _, activeLine := range active {
		activeKey := normalizeTodoStateLine(activeLine)
		if activeKey == "" || activeKey == "none" {
			continue
		}
		for _, inactiveLine := range inactive {
			inactiveKey := normalizeTodoStateLine(inactiveLine)
			if inactiveKey == "" || inactiveKey == "none" {
				continue
			}
			if activeKey == inactiveKey {
				return fmt.Errorf("compaction summary marks completed or stale todo as active: %s", activeLine)
			}
		}
	}
	return nil
}

func markdownSection(summary, heading string) (string, bool) {
	pos := findMarkdownHeadingLine(summary, heading)
	if pos < 0 {
		return "", false
	}
	start := pos + len(heading)
	rest := summary[start:]
	if loc := compactionMarkdownHeadingLineRe.FindStringIndex(rest); loc != nil {
		rest = rest[:loc[0]]
	}
	return strings.TrimSpace(rest), true
}

func todoSubsectionLines(section, label string) []string {
	var lines []string
	inGroup := false
	for raw := range strings.SplitSeq(section, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		bullet := strings.TrimSpace(strings.TrimPrefix(line, "-"))
		if strings.HasPrefix(bullet, label+":") {
			inGroup = true
			rest := strings.TrimSpace(strings.TrimPrefix(bullet, label+":"))
			if rest != "" {
				lines = append(lines, rest)
			}
			continue
		}
		if strings.HasPrefix(bullet, "Active/relevant to latest request:") || strings.HasPrefix(bullet, "Completed/background:") || strings.HasPrefix(bullet, "Stale/superseded:") {
			inGroup = false
			continue
		}
		if inGroup {
			lines = append(lines, bullet)
		}
	}
	return lines
}

func normalizeTodoStateLine(line string) string {
	line = strings.ToLower(strings.TrimSpace(line))
	line = strings.TrimPrefix(line, "-")
	line = strings.TrimSpace(line)
	line = strings.Trim(line, "`*_ .")
	line = strings.Trim(line, "()")
	return line
}

func containsThinkingTag(summary string) bool {
	lower := strings.ToLower(summary)
	return strings.Contains(lower, "<think>") || strings.Contains(lower, "</think>")
}

func compactionHeadingPositions(summary string) []int {
	positions := make([]int, 0, len(compactionRequiredHeadings))
	searchStart := 0
	for _, heading := range compactionRequiredHeadings {
		pos := findMarkdownHeadingLine(summary[searchStart:], heading)
		if pos < 0 {
			break
		}
		absolute := searchStart + pos
		positions = append(positions, absolute)
		searchStart = absolute + len(heading)
	}
	return positions
}

func findMarkdownHeadingLine(summary, heading string) int {
	pattern := compactionRequiredHeadingLineRegexps[heading]
	if pattern == nil {
		return -1
	}
	loc := pattern.FindStringIndex(summary)
	if loc == nil {
		return -1
	}
	return loc[0]
}

func renderEvidenceItemsForPrompt(items []evidenceItem) string {
	if len(items) == 0 {
		return "- (none)"
	}
	var sb strings.Builder
	for _, item := range items {
		fmt.Fprintf(&sb, "- %s", item.Title)
		if item.WhyNeeded != "" {
			fmt.Fprintf(&sb, " | why: %s", item.WhyNeeded)
		}
		if item.Excerpt != "" {
			fmt.Fprintf(&sb, "\n  excerpt: %s", strings.ReplaceAll(item.Excerpt, "\n", " "))
		}
		sb.WriteByte('\n')
	}
	return strings.TrimRight(sb.String(), "\n")
}

func recentTailTokenBudget(contextLimit int) int {
	if contextLimit <= 0 {
		return compactRecentTailMinTokens
	}
	b := contextLimit / 50
	b = max(b, compactRecentTailMinTokens)
	b = min(b, compactRecentTailMaxTokens)
	return b
}

func selectRecentTailMessages(messages []message.Message, userTurns int, maxTokens int) []message.Message {
	if len(messages) == 0 || userTurns <= 0 || maxTokens <= 0 {
		return nil
	}
	for turns := userTurns; turns >= 1; turns-- {
		usersSeen := 0
		start := len(messages)
		for i, message0 := range slices.Backward(messages) {
			start = i
			if message0.Role == message.RoleUser {
				usersSeen++
				if usersSeen >= turns {
					break
				}
			}
		}
		start = ctxmgr.SafeKeepBoundary(messages, start)
		if start <= 0 || start >= len(messages) {
			continue
		}
		tail := append([]message.Message(nil), messages[start:]...)
		if ctxmgr.EstimateMessagesTokens(tail) <= maxTokens {
			return tail
		}
	}
	return nil
}

func formatRecentTailAnchor(messages []message.Message) string {
	if len(messages) == 0 {
		return "- (none)"
	}
	var sb strings.Builder
	for _, msg := range messages {
		text := strings.TrimSpace(msg.Content)
		if len(msg.Parts) > 0 {
			norm := normalizeMessagesForSummary([]message.Message{msg})
			if len(norm) > 0 {
				text = strings.TrimSpace(norm[0].Content)
			}
		}
		if text == "" {
			continue
		}
		fmt.Fprintf(&sb, "- %s: %s\n", msg.Role, compactTextSnippet(text, 220))
	}
	out := strings.TrimRight(sb.String(), "\n")
	if out == "" {
		return "- (none)"
	}
	return out
}

// fallbackSummarySection is a heading/body pair rendered by
// renderFallbackSummarySections. The body is TrimSpaced before writing.
type fallbackSummarySection struct {
	heading string
	body    string
}

// renderFallbackSummarySections renders heading + body pairs separated by blank
// lines, then appends a preserved background-objects footer when present. Used
// by both the structured-fallback and truncate-only summary builders.
func renderFallbackSummarySections(sections []fallbackSummarySection, backgroundObjects []recovery.BackgroundObjectState) string {
	var sb strings.Builder
	for i, sec := range sections {
		if i > 0 {
			sb.WriteString("\n\n")
		}
		sb.WriteString(sec.heading)
		sb.WriteString("\n")
		sb.WriteString(strings.TrimSpace(sec.body))
	}
	if len(backgroundObjects) > 0 {
		sb.WriteString("\n\n<!-- Background objects preserved:\n")
		sb.WriteString(formatBackgroundObjectsForPrompt(backgroundObjects))
		sb.WriteString("\n-->")
	}
	return strings.TrimSpace(sb.String())
}

func buildStructuredFallbackSummary(relHistoryPath string, input *compactionInput, summarizeErr error, keyFiles []string, todos []tools.TodoItem, subAgents []SubAgentInfo, backgroundObjects []recovery.BackgroundObjectState) string {
	anchor := fallbackContinuationAnchorForInput(input)
	return renderFallbackSummarySections([]fallbackSummarySection{
		{"## Current User Request", fallbackCurrentUserRequestSection(input)},
		{"## Active Objective", fallbackActiveObjectiveSection(anchor)},
		{"## Background Goals", "- Earlier goals are background until confirmed relevant to the latest preserved user request."},
		{"## User Constraints", renderEvidenceKindForFallback(input, evidenceUserCorrection, "- No preserved user constraints.")},
		{"## Progress", fallbackProgressSection(input)},
		{"## Key Decisions", "- Earlier durable decisions should be read from the archived history file if needed.\n- Preserve the recent continuation direction and evidence below."},
		{"## Files and Evidence", fallbackFilesAndEvidenceSection(relHistoryPath, input, keyFiles)},
		{"## Todo State", formatTodosAsRelevanceBullets(todos, anchor)},
		{"## SubAgent State", formatSubAgentsAsBullets(subAgents)},
		{"## Open Problems", fallbackOpenProblemsSection(input, summarizeErr)},
		{"## Next Step", fallbackNextStepSection(input)},
	}, backgroundObjects)
}

func fallbackCurrentUserRequestSection(input *compactionInput) string {
	anchor := fallbackContinuationAnchorForInput(input)
	if anchor.Kind != "" {
		return "- " + anchor.Label + ": " + strings.ReplaceAll(anchor.Text, "\n", " ")
	}
	return "- Unknown: model summarization was unavailable and no reliable latest-request anchor was preserved. Do not infer the active task from completed or stale todos."
}

func compactionSummaryHasUnknownUserRequest(content string) bool {
	const heading = "## Current User Request"
	_, section, ok := strings.Cut(content, heading)
	if !ok {
		return false
	}
	if before, _, found := strings.Cut(section, "\n## "); found {
		section = before
	}
	return strings.HasPrefix(strings.TrimSpace(section), "- Unknown:")
}

type fallbackAnchor struct {
	Kind  string
	Label string
	Text  string
}

func fallbackContinuationAnchorForInput(input *compactionInput) fallbackAnchor {
	if reason, ok := latestDoneRejectedReason(input); ok {
		return fallbackAnchor{Kind: "done_rejected", Label: "Latest Done rejected reason", Text: reason}
	}
	if input != nil {
		if strings.TrimSpace(input.RecentTailAnchor) != "" && input.RecentTailAnchor != "- (none)" {
			return fallbackAnchor{Kind: "recent_tail", Label: "Latest preserved recent context", Text: input.RecentTailAnchor}
		}
		if usableFallbackAnchor(input.GoalAnchor) {
			return fallbackAnchor{Kind: "goal_anchor", Label: "Latest recoverable user request from durable anchors", Text: input.GoalAnchor}
		}
	}
	return fallbackAnchor{}
}

func fallbackActiveObjectiveSection(anchor fallbackAnchor) string {
	if anchor.Kind != "" {
		return "- Serve only the latest preserved request: " + fallbackAnchorSnippet(anchor) + "\n- Do not restart older completed/background or stale/superseded todos unless that request explicitly reopens them."
	}
	return "- No active objective can be recovered safely from fallback data. First identify the latest user request from preserved recent context/evidence before acting; do not restart completed/background or stale/superseded todos."
}

func fallbackAnchorSnippet(anchor fallbackAnchor) string {
	text := strings.TrimSpace(anchor.Text)
	if text == "" {
		return "unknown"
	}
	return strings.ReplaceAll(compactTextSnippet(text, 260), "\n", " ")
}

func usableFallbackAnchor(anchor string) bool {
	anchor = strings.TrimSpace(anchor)
	return anchor != "" && anchor != "- (none)" && !strings.Contains(anchor, "not confidently recoverable")
}

func latestDoneRejectedReason(input *compactionInput) (string, bool) {
	if input == nil {
		return "", false
	}
	for _, item := range input.EvidenceItems {
		if item.Kind == evidenceDoneRejected && strings.TrimSpace(item.Excerpt) != "" {
			return item.Excerpt, true
		}
	}
	return "", false
}

func renderEvidenceKindForFallback(input *compactionInput, kind evidenceKind, empty string) string {
	if input == nil || len(input.EvidenceItems) == 0 {
		return empty
	}
	var lines []string
	for _, item := range input.EvidenceItems {
		if item.Kind != kind {
			continue
		}
		lines = append(lines, "- "+strings.ReplaceAll(item.Excerpt, "\n", " "))
	}
	if len(lines) == 0 {
		return empty
	}
	return strings.Join(lines, "\n")
}

func fallbackProgressSection(input *compactionInput) string {
	if input == nil {
		return "- Archived history was compacted."
	}
	lines := []string{"- Archived history was compacted into a durable checkpoint."}
	if input.RecentTailAnchor != "- (none)" {
		lines = append(lines, "- Recent continuation context was preserved.")
	}
	if usableFallbackAnchor(input.GoalAnchor) {
		lines = append(lines, "- Latest recoverable user request was preserved in durable anchors.")
	}
	if len(input.EvidenceItems) > 0 {
		lines = append(lines, fmt.Sprintf("- Preserved %d high-priority evidence item(s).", len(input.EvidenceItems)))
	}
	return strings.Join(lines, "\n")
}

func fallbackFilesAndEvidenceSection(relHistoryPath string, input *compactionInput, keyFiles []string) string {
	lines := []string{
		"- Archived history for this compaction: " + relHistoryPath,
		"- Checkpoint wrapper may list additional archived history files for the full session history chain.",
	}
	for _, path := range keyFiles {
		lines = append(lines, "- "+path)
	}
	if input != nil {
		for _, item := range input.EvidenceItems {
			if item.Kind == evidenceToolDiff || item.Kind == evidenceToolError {
				lines = append(lines, "- "+item.Title+": "+strings.ReplaceAll(compactTextSnippet(item.Excerpt, 160), "\n", " "))
			}
		}
	}
	return strings.Join(lines, "\n")
}

func fallbackOpenProblemsSection(input *compactionInput, summarizeErr error) string {
	var lines []string
	if summarizeErr != nil {
		lines = append(lines, "- Summary quality fallback reason: "+summarizeErr.Error())
	}
	if input != nil {
		for _, item := range input.EvidenceItems {
			if item.Kind == evidenceToolError || item.Kind == evidenceEscalate {
				lines = append(lines, "- "+strings.ReplaceAll(compactTextSnippet(item.Excerpt, 180), "\n", " "))
			}
		}
	}
	if len(lines) == 0 {
		return "- Read the archived history if additional unresolved issues are needed."
	}
	return strings.Join(lines, "\n")
}

func fallbackNextStepSection(input *compactionInput) string {
	anchor := fallbackContinuationAnchorForInput(input)
	if anchor.Kind != "" {
		return "- Choose the immediate next action only from this latest preserved request: " + fallbackAnchorSnippet(anchor) + "\n- Ignore completed/background and stale/superseded todos unless that request explicitly reopens them."
	}
	return "- Before modifying files or continuing old work, recover or ask for the latest user request; do not act on completed/background or stale/superseded todos."
}

func formatTodosAsRelevanceBullets(todos []tools.TodoItem, anchor fallbackAnchor) string {
	lines := []string{
		"- Active/relevant to latest request:",
		"  - (none reliably classified by fallback)",
		"- Completed/background:",
		"  - (none classified by fallback)",
		"- Stale/superseded:",
		"  - (none classified by fallback)",
	}
	if strings.TrimSpace(anchor.Text) != "" {
		lines = []string{
			"- Active/relevant to latest request:",
			"  - " + anchor.Label + ": " + strings.ReplaceAll(anchor.Text, "\n", " "),
			"- Completed/background:",
			"  - (none classified by fallback)",
			"- Stale/superseded:",
		}
		if len(todos) == 0 {
			lines = append(lines, "  - (none)")
			return strings.Join(lines, "\n")
		}
		for _, todo := range todos {
			lines = append(lines, fmt.Sprintf("  - [%s] %s: %s", todo.Status, todo.ID, todo.Content))
		}
		return strings.Join(lines, "\n")
	}
	if len(todos) == 0 {
		return strings.Join(lines, "\n")
	}
	for _, todo := range todos {
		lines = append(lines, fmt.Sprintf("  - [%s] %s: %s", todo.Status, todo.ID, todo.Content))
	}
	return strings.Join(lines, "\n")
}

func subAgentStateNeedsPromptContext(state string) bool {
	switch strings.TrimSpace(state) {
	case string(SubAgentStateRunning), string(SubAgentStateIdle), string(SubAgentStateWaitingMain), string(SubAgentStateWaitingDescendant):
		return true
	default:
		return false
	}
}

func subAgentsForCompactionPrompt(subAgents []SubAgentInfo) (visible []SubAgentInfo, omitted int) {
	if len(subAgents) == 0 {
		return nil, 0
	}
	visible = make([]SubAgentInfo, 0, min(len(subAgents), compactPromptSubAgentLimit))
	for _, sub := range subAgents {
		if !subAgentStateNeedsPromptContext(sub.State) {
			omitted++
			continue
		}
		if len(visible) >= compactPromptSubAgentLimit {
			omitted++
			continue
		}
		copySub := sub
		copySub.TaskDesc = strings.ReplaceAll(compactTextSnippet(strings.TrimSpace(copySub.TaskDesc), compactPromptDescMaxChars), "\n", " ")
		copySub.LastSummary = strings.ReplaceAll(compactTextSnippet(strings.TrimSpace(copySub.LastSummary), compactPromptSummaryMaxChars), "\n", " ")
		visible = append(visible, copySub)
	}
	return visible, omitted
}

func formatSubAgentsAsBullets(subAgents []SubAgentInfo) string {
	visible, omitted := subAgentsForCompactionPrompt(subAgents)
	if len(visible) == 0 {
		if omitted > 0 {
			return fmt.Sprintf("- (none active; %d historical or completed task(s) omitted)", omitted)
		}
		return "- (none active)"
	}
	var lines []string
	for _, sub := range visible {
		running := sub.RunningRef
		if running == "" {
			running = sub.SelectedRef
		}
		line := fmt.Sprintf("- %s | task=%s | state=%s | agent=%s | model=%s | desc=%s", sub.InstanceID, sub.TaskID, blankToDefault(sub.State, "unknown"), sub.AgentDefName, running, sub.TaskDesc)
		if strings.TrimSpace(sub.LastSummary) != "" {
			line += " | summary=" + sub.LastSummary
		}
		lines = append(lines, line)
	}
	if omitted > 0 {
		lines = append(lines, fmt.Sprintf("- (%d historical or completed task(s) omitted from compaction prompt)", omitted))
	}
	return strings.Join(lines, "\n")
}

func formatBackgroundObjectsForPrompt(jobs []recovery.BackgroundObjectState) string {
	if len(jobs) == 0 {
		return "- (none)"
	}
	var sb strings.Builder
	for _, job := range jobs {
		fmt.Fprintf(&sb, "- %s | agent=%s | kind=%s | status=%s | started=%s | desc=%s", job.ID, backgroundObjectPromptAgent(job.AgentID), backgroundObjectPromptKind(job.Kind), job.Status, job.StartedAt.Format(time.DateTime), backgroundObjectPromptDescription(job.Description, job.Command))
		if job.MaxRuntimeSec > 0 {
			fmt.Fprintf(&sb, " | max_runtime=%ds", job.MaxRuntimeSec)
		}
		if !job.FinishedAt.IsZero() {
			fmt.Fprintf(&sb, " | finished=%s", job.FinishedAt.Format(time.DateTime))
		}
		sb.WriteByte('\n')
	}
	return strings.TrimRight(sb.String(), "\n")
}

func backgroundObjectPromptAgent(agentID string) string {
	if strings.TrimSpace(agentID) == "" {
		return "main"
	}
	return agentID
}

func backgroundObjectPromptKind(kind string) string {
	if strings.TrimSpace(kind) == "" {
		return "job"
	}
	return kind
}

func backgroundObjectPromptDescription(description, fallbackCommand string) string {
	if strings.TrimSpace(description) != "" {
		return description
	}
	return fallbackCommand
}

func buildCompactionPromptWithKeyFiles(input *compactionInput, relHistoryPath string, keyFiles []string, todos []tools.TodoItem, subAgents []SubAgentInfo, backgroundObjects []recovery.BackgroundObjectState) string {
	var sb strings.Builder
	sb.WriteString("Summarize the earlier conversation transcript below so the main coding agent can continue work.\n")
	sb.WriteString("Treat this as a durable checkpoint for the next coding turn, not as a narrative recap. Focus on current objective, constraints, decisions, progress, blockers, and concrete next steps.\n")
	sb.WriteString("A small raw evidence pack and recent raw tail may be kept after this summary, so focus on durable context from the archived head rather than duplicating those verbatim excerpts.\n\n")
	fmt.Fprintf(&sb, "Full archived history file for this compaction: %s\n", relHistoryPath)
	sb.WriteString("If this is not the first compaction, the checkpoint wrapper also lists all archived history files for the full session history chain.\n")
	if input != nil && input.OmittedMessages > 0 {
		fmt.Fprintf(&sb, "Compression note: the earliest %d archived message(s) were omitted from the summary input to fit the utility model budget. The archived history file is authoritative for those details.\n", input.OmittedMessages)
	}
	sb.WriteString("\nDurable anchors extracted before summarization:\n")
	sb.WriteString(formatCompactionAnchorsForPrompt(input))
	sb.WriteString("\n\nKey file candidates:\n")
	sb.WriteString(formatKeyFileCandidatesForPrompt(keyFiles))
	sb.WriteString("\n\nHigh-priority extracted evidence:\n")
	if input != nil {
		sb.WriteString(renderEvidenceItemsForPrompt(input.EvidenceItems))
	} else {
		sb.WriteString("- (none)")
	}
	sb.WriteString("\n\nPreserved recent continuation anchor:\n")
	if input != nil {
		sb.WriteString(input.RecentTailAnchor)
	} else {
		sb.WriteString("- (none)")
	}
	sb.WriteString("\n\nCurrent todo list from the pre-compaction agent. These todos are not automatically authoritative after compaction. Evaluate each item against the latest user request and classify it as active/relevant, completed/background, or stale/superseded in the summary:\n")
	sb.WriteString(formatTodosForPrompt(todos))
	sb.WriteString("\n\nCurrent sub-agent state:\n")
	sb.WriteString(formatSubAgentsForPrompt(subAgents))
	sb.WriteString("\n\nCurrent background objects:\n")
	sb.WriteString(formatBackgroundObjectsForPrompt(backgroundObjects))
	sb.WriteString("\n\nConversation transcript to summarize:\n\n")
	if input != nil {
		sb.WriteString(input.Transcript)
	}
	return sb.String()
}

func formatTodosForPrompt(todos []tools.TodoItem) string {
	if len(todos) == 0 {
		return "- (none)"
	}
	var sb strings.Builder
	for _, todo := range todos {
		line := fmt.Sprintf("- [%s] %s: %s", todo.Status, todo.ID, todo.Content)
		if todo.ActiveForm != "" {
			line += " | active: " + todo.ActiveForm
		}
		sb.WriteString(line)
		sb.WriteByte('\n')
	}
	return strings.TrimRight(sb.String(), "\n")
}

func formatSubAgentsForPrompt(subAgents []SubAgentInfo) string {
	visible, omitted := subAgentsForCompactionPrompt(subAgents)
	if len(visible) == 0 {
		if omitted > 0 {
			return fmt.Sprintf("- (none active; %d historical or completed task(s) omitted)", omitted)
		}
		return "- (none active)"
	}
	var sb strings.Builder
	for _, sub := range visible {
		running := sub.RunningRef
		if running == "" {
			running = sub.SelectedRef
		}
		fmt.Fprintf(&sb, "- %s | task=%s | state=%s | agent=%s | model=%s | desc=%s",
			sub.InstanceID, sub.TaskID, blankToDefault(sub.State, "unknown"), sub.AgentDefName, running, sub.TaskDesc)
		if strings.TrimSpace(sub.LastSummary) != "" {
			fmt.Fprintf(&sb, " | summary=%s", sub.LastSummary)
		}
		sb.WriteByte('\n')
	}
	if omitted > 0 {
		fmt.Fprintf(&sb, "- (%d historical or completed task(s) omitted from compaction prompt)\n", omitted)
	}
	return strings.TrimRight(sb.String(), "\n")
}

func blankToDefault(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}

func buildTruncateOnlySummary(relHistoryPath string, summarizeErr error, keyFiles []string, todos []tools.TodoItem, subAgents []SubAgentInfo, backgroundObjects []recovery.BackgroundObjectState) string {
	return renderFallbackSummarySections([]fallbackSummarySection{
		{"## Current User Request", "- Latest request was not relevance-filtered because model summarization was unavailable; read the preserved recent context and archived history before acting."},
		{"## Active Objective", "- Continue from the latest preserved user request; do not assume older todos remain active without checking relevance."},
		{"## Background Goals", "- Earlier goals are background until confirmed relevant to the latest preserved user request."},
		{"## User Constraints", "- Constraints may be incomplete because truncate-only fallback skipped model-generated summarization."},
		{"## Progress", "- Earlier history was compacted in truncate-only mode.\n- Use the archived history and key files below as the durable checkpoint."},
		{"## Key Decisions", "- Model-based context summarization was unavailable.\n- Continue from the archived history, key files, and preserved recent context instead of inventing missing decisions."},
		{"## Files and Evidence", fallbackFilesAndEvidenceSection(relHistoryPath, nil, keyFiles)},
		{"## Todo State", formatTodosAsRelevanceBullets(todos, fallbackAnchor{})},
		{"## SubAgent State", formatSubAgentsAsBullets(subAgents)},
		{"## Open Problems", fallbackOpenProblemsSection(nil, summarizeErr)},
		{"## Next Step", "- Continue from the latest preserved user request, archived history, and listed key files."},
	}, backgroundObjects)
}
