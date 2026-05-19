package agent

import (
	"strings"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/llm"
)

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

func (a *MainAgent) configuredContextReductionModelRefs() ([]string, bool, error) {
	if a == nil {
		return nil, false, nil
	}
	if a.projectConfig != nil {
		if pool := strings.TrimSpace(a.projectConfig.Context.Reduction.ModelPool); pool != "" {
			refs, err := a.resolveConfiguredModelPool(pool)
			if err != nil {
				return nil, true, err
			}
			return refs, true, nil
		}
	}
	if a.globalConfig != nil {
		if pool := strings.TrimSpace(a.globalConfig.Context.Reduction.ModelPool); pool != "" {
			refs, err := a.resolveConfiguredModelPool(pool)
			if err != nil {
				return nil, true, err
			}
			return refs, true, nil
		}
	}
	return nil, false, nil
}

func (a *MainAgent) contextReductionModelRefs() []string {
	refs, _, err := a.configuredContextReductionModelRefs()
	if err != nil {
		return nil
	}
	return refs
}

func (a *MainAgent) newContextReductionClient() (*llm.Client, bool, error) {
	refs, configured, err := a.configuredContextReductionModelRefs()
	if err != nil || !configured {
		return nil, configured, err
	}
	client, err := a.newAuxModelPoolClient(refs, 1*time.Minute, 1024)
	if err != nil {
		return nil, true, err
	}
	client.SetStreamRetryRounds(1)
	return client, true, nil
}
