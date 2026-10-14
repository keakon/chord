package agent

import (
	"crypto/sha256"

	"github.com/keakon/chord/internal/agent/requesttrim"
)

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
		a.lastPreparedReductionPolicy = requesttrim.Policy{}
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
		// The reduction memo's verdicts are content-derived and would stay
		// correct, but restore/compaction/model changes retire most of the
		// history they were computed over; reset to reclaim the retained
		// args strings and bound the maps.
		a.reductionMemo.Reset()
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

func (a *MainAgent) clearStageCompletionCandidate() {
	if a != nil {
		a.stageCompletionCandidateTurnID = 0
		a.stageCompletionCandidatePending = false
		a.stageCompletionCandidatePromptDelivered = false
	}
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
