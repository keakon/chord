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
	if !keyMatches(key, m.keyMap.ServiceTier) {
		return false
	}
	if m.agent == nil {
		return true
	}
	tier := m.serviceTier()
	next := config.ServiceTierFast
	switch tier {
	case config.ServiceTierStandard:
		next = config.ServiceTierFast
	case config.ServiceTierFast:
		next = config.ServiceTierSlow
	case config.ServiceTierSlow:
		next = config.ServiceTierStandard
	}
	m.recordTUIDiagnostic("agent-command", "shortcut:%s /tier %s", key, next)
	m.agent.SendUserMessage("/tier " + string(next))
	return true
}
