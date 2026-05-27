package tui

import (
	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/config"
)

func (m *Model) serviceTier() config.ServiceTier {
	if m == nil || m.agent == nil {
		return config.ServiceTierStandard
	}
	reporter, ok := m.agent.(agent.ServiceTierReporter)
	if !ok {
		return config.ServiceTierStandard
	}
	return reporter.ServiceTier()
}

func (m *Model) effectiveServiceTier() config.ServiceTier {
	if m == nil || m.agent == nil {
		return config.ServiceTierStandard
	}
	reporter, ok := m.agent.(agent.ServiceTierReporter)
	if !ok {
		return config.ServiceTierStandard
	}
	return reporter.EffectiveServiceTier()
}

func (m *Model) supportedServiceTiers() []config.ServiceTier {
	if m == nil || m.agent == nil {
		return []config.ServiceTier{config.ServiceTierStandard}
	}
	reporter, ok := m.agent.(agent.ServiceTierReporter)
	if !ok {
		return []config.ServiceTier{config.ServiceTierStandard}
	}
	tiers := reporter.SupportedServiceTiers()
	if len(tiers) == 0 {
		return []config.ServiceTier{config.ServiceTierStandard}
	}
	return tiers
}

func nextServiceTier(current config.ServiceTier, supported []config.ServiceTier) (config.ServiceTier, bool) {
	if len(supported) == 0 {
		return "", false
	}
	current = config.NormalizeServiceTier(string(current))
	for i, tier := range supported {
		if tier == current {
			if len(supported) == 1 {
				return "", false
			}
			return supported[(i+1)%len(supported)], true
		}
	}
	return supported[0], true
}

func (m *Model) maybeServiceTierShortcut(key string) bool {
	if !keyMatches(key, m.keyMap.ServiceTier) {
		return false
	}
	next, ok := nextServiceTier(m.serviceTier(), m.supportedServiceTiers())
	if !ok {
		return true
	}
	return m.sendSlashShortcut(key, m.keyMap.ServiceTier, "/tier "+string(next))
}
