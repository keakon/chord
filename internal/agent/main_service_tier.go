package agent

import (
	"fmt"
	"strings"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/llm"
)

func (a *MainAgent) ServiceTier() config.ServiceTier {
	client, _, _, _ := a.llmSnapshot()
	if client == nil {
		return config.ServiceTierStandard
	}
	return client.ServiceTier()
}

func (a *MainAgent) EffectiveServiceTier() config.ServiceTier {
	client, _, _, runningRef := a.llmSnapshot()
	if client == nil {
		return config.ServiceTierStandard
	}
	return client.EffectiveServiceTierForModelRef(runningRef)
}

func (a *MainAgent) SupportedServiceTiers() []config.ServiceTier {
	client, _, _, runningRef := a.llmSnapshot()
	if client == nil {
		return []config.ServiceTier{config.ServiceTierStandard}
	}
	return client.SupportedServiceTiersForModelRef(runningRef)
}

func (a *MainAgent) applyServiceTierToClient(client *llm.Client) {
	if a == nil || client == nil {
		return
	}
	client.SetServiceTier(a.ServiceTier())
}

func (a *MainAgent) syncSubAgentServiceTier(tier config.ServiceTier) {
	if a == nil {
		return
	}
	a.mu.RLock()
	targets := make([]*SubAgent, 0, len(a.subAgents))
	for _, sub := range a.subAgents {
		if sub != nil {
			targets = append(targets, sub)
		}
	}
	a.mu.RUnlock()
	for _, sub := range targets {
		sub.setServiceTier(tier)
	}
}

func (a *MainAgent) handleTierCommand(content string, busy bool) {
	arg := strings.TrimSpace(strings.TrimPrefix(content, "/tier"))
	client, _, _, _ := a.llmSnapshot()
	if client == nil {
		a.emitToTUI(ToastEvent{Message: "Service tier unavailable: no LLM client", Level: "error"})
		if !busy {
			a.setIdleAndDrainPending()
		}
		return
	}

	if arg == "" {
		a.emitToTUI(ToastEvent{Message: "Usage: /tier standard | /tier fast | /tier slow", Level: "info"})
		if !busy {
			a.setIdleAndDrainPending()
		}
		return
	}

	var tier config.ServiceTier
	switch strings.ToLower(arg) {
	case string(config.ServiceTierStandard):
		tier = config.ServiceTierStandard
	case string(config.ServiceTierFast):
		tier = config.ServiceTierFast
	case string(config.ServiceTierSlow):
		tier = config.ServiceTierSlow
	default:
		a.emitToTUI(ToastEvent{Message: "Usage: /tier standard | /tier fast | /tier slow", Level: "info"})
		if !busy {
			a.setIdleAndDrainPending()
		}
		return
	}
	supported := client.SupportedServiceTiersForModelRef(client.RunningModelRef())
	supportedTier := false
	for _, candidate := range supported {
		if candidate == tier {
			supportedTier = true
			break
		}
	}
	if !supportedTier {
		a.emitToTUI(ToastEvent{Message: fmt.Sprintf("Service tier %s is not supported by the current model", tier), Level: "error"})
		if !busy {
			a.setIdleAndDrainPending()
		}
		return
	}
	client.SetServiceTier(tier)
	a.syncSubAgentServiceTier(tier)

	a.emitToTUI(ToastEvent{Message: fmt.Sprintf("Tier %s", tier), Level: "info"})
	if !busy {
		a.setIdleAndDrainPending()
	}
}
