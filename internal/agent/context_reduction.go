package agent

import "github.com/keakon/chord/internal/config"

type ContextReductionStats struct {
	Messages      int
	Bytes         int
	TokensBefore  int
	TokensAfter   int
	TokensSaved   int
	Protected     bool
	ReusedStable  bool
	ByToolAndRule map[string]ContextReductionBucket
}

type ContextReductionBucket struct {
	Messages    int
	Bytes       int
	TokensSaved int
}

type contextReductionPolicy struct {
	ConfirmAgeTurns      int
	ErrorAgeTurns        int
	ShellSuccessAgeTurns int
	ReadLikeAgeTurns     int
	StaleAgeTurns        int
	ShellSuccessBytes    int
	ReadLikeOutputBytes  int
	StaleOutputBytes     int
	MinToolResultsPrune  int
	CacheAwareMinUsage   float64
	WarmupMessageLimit   int
	MinIncrementalTokens int
	HighPressureUsage    float64
	ForcePruneUsage      float64
}

func defaultContextReductionPolicy() contextReductionPolicy {
	return contextReductionPolicy{
		ConfirmAgeTurns:      compactConfirmAgeTurns,
		ErrorAgeTurns:        compactErrorAgeTurns,
		ShellSuccessAgeTurns: compactBashSuccessAgeTurns,
		ReadLikeAgeTurns:     compactReadLikeAgeTurns,
		StaleAgeTurns:        compactStaleAgeTurns,
		ShellSuccessBytes:    compactBashSuccessBytes,
		ReadLikeOutputBytes:  compactReadLikeOutputBytes,
		StaleOutputBytes:     compactStaleOutputBytes,
		MinToolResultsPrune:  compactMinToolResultsPrune,
		CacheAwareMinUsage:   0.75,
		WarmupMessageLimit:   32,
		MinIncrementalTokens: 4096,
		HighPressureUsage:    0.80,
		ForcePruneUsage:      0.90,
	}
}

func (a *MainAgent) contextReductionPolicy() contextReductionPolicy {
	policy := defaultContextReductionPolicy()
	if a == nil {
		return policy
	}
	for _, cfg := range []*config.Config{a.globalConfig, a.projectConfig} {
		if cfg == nil {
			continue
		}
		policy.applyConfig(cfg.Context.Reduction)
	}
	return policy
}

func (p *contextReductionPolicy) applyConfig(cfg config.ContextReductionConfig) {
	if cfg.ConfirmAgeTurns > 0 {
		p.ConfirmAgeTurns = cfg.ConfirmAgeTurns
	}
	if cfg.ErrorAgeTurns > 0 {
		p.ErrorAgeTurns = cfg.ErrorAgeTurns
	}
	if cfg.ShellSuccessAgeTurns > 0 {
		p.ShellSuccessAgeTurns = cfg.ShellSuccessAgeTurns
	}
	if cfg.ReadLikeAgeTurns > 0 {
		p.ReadLikeAgeTurns = cfg.ReadLikeAgeTurns
	}
	if cfg.StaleAgeTurns > 0 {
		p.StaleAgeTurns = cfg.StaleAgeTurns
	}
	if cfg.ShellSuccessBytes > 0 {
		p.ShellSuccessBytes = cfg.ShellSuccessBytes
	}
	if cfg.ReadLikeOutputBytes > 0 {
		p.ReadLikeOutputBytes = cfg.ReadLikeOutputBytes
	}
	if cfg.StaleOutputBytes > 0 {
		p.StaleOutputBytes = cfg.StaleOutputBytes
	}
	if cfg.MinToolResultsPrune > 0 {
		p.MinToolResultsPrune = cfg.MinToolResultsPrune
	}
	if cfg.CacheAwareMinUsage > 0 {
		p.CacheAwareMinUsage = cfg.CacheAwareMinUsage
	}
	if cfg.WarmupMessageLimit > 0 {
		p.WarmupMessageLimit = cfg.WarmupMessageLimit
	}
	if cfg.MinIncrementalTokens > 0 {
		p.MinIncrementalTokens = cfg.MinIncrementalTokens
	}
	if cfg.HighPressureUsage > 0 {
		p.HighPressureUsage = cfg.HighPressureUsage
	}
	if cfg.ForcePruneUsage > 0 {
		p.ForcePruneUsage = cfg.ForcePruneUsage
	}
}

func (p contextReductionPolicy) shouldProtectCachedContext(messageCount, estimatedTokens, inputBudget int) bool {
	if inputBudget <= 0 || p.CacheAwareMinUsage <= 0 || p.WarmupMessageLimit <= 0 {
		return false
	}
	if messageCount > p.WarmupMessageLimit {
		return false
	}
	return float64(estimatedTokens)/float64(inputBudget) < p.CacheAwareMinUsage
}

func (p contextReductionPolicy) contextUsage(estimatedTokens, inputBudget int) float64 {
	if inputBudget <= 0 {
		return 1
	}
	return float64(estimatedTokens) / float64(inputBudget)
}

func (p contextReductionPolicy) shouldReuseStableReductionSurface(stats, previous ContextReductionStats, estimatedTokens, inputBudget int) bool {
	if p.MinIncrementalTokens <= 0 || stats.TokensSaved <= 0 || previous.TokensSaved <= 0 {
		return false
	}
	usage := p.contextUsage(estimatedTokens, inputBudget)
	if p.ForcePruneUsage > 0 && usage >= p.ForcePruneUsage {
		return false
	}
	if p.HighPressureUsage > 0 && usage >= p.HighPressureUsage {
		return false
	}
	return stats.TokensSaved-previous.TokensSaved < p.MinIncrementalTokens
}
