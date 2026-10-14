package agent

import (
	"github.com/keakon/chord/internal/agent/requesttrim"
	"github.com/keakon/chord/internal/config"
)

type ContextReductionStats struct {
	Messages        int
	Bytes           int
	CurrentBytes    int
	CurrentMessages int
	TokensBefore    int
	TokensAfter     int
	TokensSaved     int
	Protected       bool
	ReusedStable    bool
	ProtectReason   string
	ReuseReason     string
	SavedDelta      int
	PreviousModel   string
	ModelChanged    bool
	ModelRunLength  int
	ByToolAndRule   map[string]ContextReductionBucket
	SkippedByReason map[string]int
	OverCompression map[string]int
	// OverCompressionByTool breaks the same events down by the tool that
	// produced the re-fetched output. It is a separate map so OverCompression
	// stays summable and bounded: tool names include dynamic MCP names, so
	// mixing the two would make any total double-count and give the key space
	// no ceiling.
	OverCompressionByTool map[string]int
	// ByRetentionLevel counts tool results per retention level, including the
	// ones kept complete. ByToolAndRule only records what was reduced, so it
	// cannot answer how much of the request is still full-fidelity evidence —
	// the question the valid-read retention policy has to be decided on.
	//
	// This field and the four below make up the retention ledger, which is only
	// filled in when the request debug log is enabled: they answer questions a
	// reader asks, not questions the request surface branches on.
	ByRetentionLevel map[string]int
	// ProtectedReadTokens is what current, non-superseded reads cost in this
	// request. Reclaiming them is the single largest change the retention
	// policy could make, so its price is measured before it is considered.
	ProtectedReadTokens int
	// UnrecoverableReductions counts lossy renderings that left neither key
	// fields nor an address behind. It must stay zero: it is the layer's
	// destructive failure mode, not a tuning knob.
	UnrecoverableReductions int
	// ArchiveReads / ArchiveReadFailures count how often the model actually
	// followed a recovery address, and how often that read failed. A recovery
	// route nobody can use is indistinguishable from a dropped payload until
	// these are measured.
	ArchiveReads              int
	ArchiveReadFailures       int
	EvidenceRebuildDurationUS int64
	EvidenceFiles             int
	EvidenceObservations      int
	EvidenceCurrent           int
	EvidenceStale             int
	EvidenceSuperseded        int
}

type ContextReductionBucket struct {
	Messages    int
	Bytes       int
	TokensSaved int
}

const (
	contextReductionSkipRecentHighRisk = "recent_high_risk"
	contextReductionSkipLargeUnreduced = "large_but_unreduced"
	contextReductionSkipFrozenReduced  = "frozen_reduced"
	contextReductionSkipDeferredCache  = "deferred_for_cache"
	contextReductionSkipDeferredReview = "deferred_for_review"
	contextReductionSkipRecalledInput  = "recalled_input_protect"

	contextReductionOverCompressionReread                = "reread_after_reduction"
	contextReductionOverCompressionRereadSameRevision    = "reread_same_revision_after_reduction"
	contextReductionOverCompressionRereadChangedRevision = "reread_changed_revision_after_reduction"
	contextReductionOverCompressionResearch              = "research_after_reduction"
)

func (a *MainAgent) contextReductionPolicy() requesttrim.Policy {
	policy := requesttrim.DefaultPolicy()
	if a == nil {
		return policy
	}
	for _, cfg := range []*config.Config{a.globalConfig, a.projectConfig} {
		if cfg == nil {
			continue
		}
		policy.ApplyConfig(cfg.Context.Reduction)
	}
	return policy
}

func reuseStableReductionSurfaceReason(p requesttrim.Policy, stats, previous ContextReductionStats) (string, int) {
	if p.MinIncrementalTokens <= 0 || stats.TokensSaved <= 0 || previous.TokensSaved <= 0 {
		return contextReuseReasonNoPreviousSavings, 0
	}
	delta := stats.TokensSaved - previous.TokensSaved
	if delta < p.MinIncrementalTokens {
		return contextReuseReasonBelowIncrementalMin, delta
	}
	return contextReuseReasonNone, delta
}

const (
	contextProtectReasonWrapUpGrace = "wrap_up_grace"

	contextReuseReasonNone                = ""
	contextReuseReasonBelowIncrementalMin = "below_incremental_min"
	contextReuseReasonNoPreviousSavings   = "no_previous_savings"
)
