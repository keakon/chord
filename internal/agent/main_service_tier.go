package agent

import (
	"fmt"
	"slices"
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
	client, runningRef := a.tuiFocusedLLMAndRef()
	if client == nil {
		return config.ServiceTierStandard
	}
	return a.effectiveDisplayServiceTier(client, runningRef)
}

func (a *MainAgent) SupportedServiceTiers() []config.ServiceTier {
	client, runningRef := a.tuiFocusedLLMAndRef()
	if client == nil {
		return []config.ServiceTier{config.ServiceTierStandard}
	}
	return a.supportedDisplayServiceTiers(client, runningRef)
}

// A pending pool target may not exist in the installed client. Its tier
// capabilities are still known from configuration, without installing it early.
func (a *MainAgent) supportedDisplayServiceTiers(client *llm.Client, ref string) []config.ServiceTier {
	if client != nil && client.ProviderForModelRef(ref) != nil {
		return client.SupportedServiceTiersForModelRef(ref)
	}
	base, _ := config.ParseModelRef(ref)
	provider, modelID, _ := strings.Cut(base, "/")
	cfg, ok := a.providerConfigByName(provider)
	if !ok {
		return []config.ServiceTier{config.ServiceTierStandard}
	}
	model := cfg.Models[modelID]
	set := model.SupportedServiceTierSet(cfg.Preset, cfg.SupportedServiceTiers)
	tiers := []config.ServiceTier{config.ServiceTierStandard}
	for _, tier := range []config.ServiceTier{config.ServiceTierFast, config.ServiceTierSlow} {
		if set[tier] {
			tiers = append(tiers, tier)
		}
	}
	return tiers
}

func (a *MainAgent) effectiveDisplayServiceTier(client *llm.Client, ref string) config.ServiceTier {
	if client == nil {
		return config.ServiceTierStandard
	}
	if client.ProviderForModelRef(ref) != nil {
		return client.EffectiveServiceTierForModelRef(ref)
	}
	tier := client.ServiceTier()
	if slices.Contains(a.supportedDisplayServiceTiers(client, ref), tier) {
		return tier
	}
	return config.ServiceTierStandard
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
	for _, sub := range a.subs.snapshotSubAgents() {
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
	supported := a.SupportedServiceTiers()
	supportedTier := slices.Contains(supported, tier)
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
