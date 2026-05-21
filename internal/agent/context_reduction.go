package agent

import "github.com/keakon/chord/internal/config"

type ContextReductionStats struct {
	Messages int
	Bytes    int
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
}
