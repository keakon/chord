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

func (m *Model) maybeServiceTierShortcut(key string) bool {
	next := config.ServiceTierFast
	switch m.serviceTier() {
	case config.ServiceTierStandard:
		next = config.ServiceTierFast
	case config.ServiceTierFast:
		next = config.ServiceTierSlow
	case config.ServiceTierSlow:
		next = config.ServiceTierStandard
	}
	return m.sendSlashShortcut(key, m.keyMap.ServiceTier, "/tier "+string(next))
}
