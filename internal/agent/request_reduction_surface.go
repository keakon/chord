package agent

import (
	"crypto/sha256"
	"maps"

	"github.com/keakon/chord/internal/agent/requestshape"
	"github.com/keakon/chord/internal/agent/requesttrim"
	"github.com/keakon/chord/internal/message"
)

func (a *MainAgent) rememberPreparedLLMRequest(turnID uint64, original, prepared []message.Message, discardedInputs map[string]string, nextReviewAge []int, toolResults int, policy requesttrim.Policy) {
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
	a.lastPreparedLLMRequestPrefix = cowRequestShapeSlice(a.lastPreparedLLMRequestPrefix, prepared)
	a.lastPreparedLLMReducedIndices = reducedIndices
	if discardedInputs != nil {
		a.lastPreparedLLMDiscardedInputs = maps.Clone(discardedInputs)
	}
	a.lastPreparedLLMNextReviewAge = append([]int(nil), nextReviewAge...)
	a.lastPreparedLLMToolResults = toolResults
	a.lastPreparedReductionPolicy = policy
	a.lastPreparedLLMToolDefHash = toolDefHash
	a.lastPreparedReductionStats = cloneContextReductionStats(a.contextReductionStats)
	// Fold this request's retention signals into the session and window
	// aggregators. The request's stats are final here — every
	// rememberPrepared=true exit of prepareMessagesForLLMWithOptions stores
	// them into a.contextReductionStats before this call, so aggregation runs
	// exactly once per prepared main request.
	a.mergeRequestRetentionSignalsLocked(a.contextReductionStats)
}

// incrementalMessageShapesLocked computes shapes for original plus the source
// copy to store alongside them, reusing stored entries for the leading run of
// messages that are field-equal to the previous shape source. In the steady
// state (unchanged history) both return values are the previously stored
// slices and nothing is hashed or allocated; with an append-only history only
// the new tail is hashed. Caller must hold loopReductionMu.
func (a *MainAgent) incrementalMessageShapesLocked(original []message.Message) ([]requestshape.Shape, []message.Message) {
	if len(original) == 0 {
		return nil, nil
	}
	prevSource := a.lastPreparedLLMShapeSource
	prevShape := a.lastPreparedLLMRequestShape
	reusable := 0
	if len(prevSource) == len(prevShape) {
		reusable = requestshape.ReusablePrefixLen(prevSource, original)
	}
	if reusable == len(original) && len(prevSource) == len(original) {
		return prevShape, prevSource
	}
	source := append([]message.Message(nil), original...)
	if reusable == 0 {
		return requestshape.Shapes(original), source
	}
	shapes := make([]requestshape.Shape, len(original))
	copy(shapes, prevShape[:reusable])
	for i := reusable; i < len(original); i++ {
		shapes[i] = requestshape.ShapeOf(&original[i])
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
		return requestshape.ToolDefinitionsHash(a.mainLLMToolDefinitions())
	}
	if memo := a.toolDefHashMemo.Load(); memo != nil && memo.defs == frozen {
		return memo.hash
	}
	hash := requestshape.ToolDefinitionsHash(*frozen)
	a.toolDefHashMemo.Store(&toolDefHashMemoEntry{defs: frozen, hash: hash})
	return hash
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
		a.lastPreparedLLMRequestShape = requestshape.Shapes(prepared)
		a.lastPreparedLLMShapeSource = append([]message.Message(nil), prepared...)
	}
	a.lastPreparedLLMTurnID = turnID
	a.lastPreparedLLMRequestPrefix = cowRequestShapeSlice(a.lastPreparedLLMRequestPrefix, prepared)
	a.lastPreparedReductionStats = cloneContextReductionStats(a.contextReductionStats)
}

type stableReductionSurface struct {
	Messages []message.Message
	Shape    []requestshape.Shape
	// ShapeSource holds shallow copies of the original messages Shape was
	// computed from, when available. It enables direct field-equality
	// compatibility checks that skip content re-hashing. May be nil (e.g.
	// loop-frozen surfaces); callers must fall back to hash comparison.
	ShapeSource    []message.Message
	Stats          ContextReductionStats
	ReducedIndices []bool
	NextReviewAge  []int
	ToolResults    int
	Policy         requesttrim.Policy
	ToolDefHash    [sha256.Size]byte
}

// toolDefHashMemoEntry caches the hash of one frozen tool-definition snapshot.
// defs is the exact pointer stored in MainAgent.frozenToolDefs; snapshots are
// immutable after freeze, so pointer identity implies hash validity.
type toolDefHashMemoEntry struct {
	defs *[]message.ToolDefinition
	hash [sha256.Size]byte
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
	// current is caller-owned request storage. Reuse writes the stored reduced
	// prefix into it and leaves the fresh tail in place, avoiding a second full
	// message-slice allocation. Prefix compatibility already compares every
	// tool-call identity and argument, so replacing it cannot introduce a new
	// orphan tool result; the old defensive full-slice comparison repeated that
	// invariant after cloning.
	out := current
	for i := range previousMessages {
		out[i] = previousMessages[i].Clone()
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
		return requestshape.MessagesEquivalent(previous.ShapeSource, currentPrefix)
	}
	return requestshape.ShapesCompatible(previous.Shape, currentPrefix)
}

func (a *MainAgent) tryReuseStableReductionSurfaceBeforeFullScan(messages []message.Message, policy requesttrim.Policy, scan *requesttrim.Scan, currentBatch uint64, externalInvalidated map[int]bool) ([]message.Message, ContextReductionStats, bool) {
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
	// The reuse helper writes into its current slice. Own the copy first so the
	// durable request surface supplied by the caller is never modified.
	owned := append([]message.Message(nil), messages...)
	reused, compatible := reuseStableReductionPrefix(previous, owned, messages)
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
