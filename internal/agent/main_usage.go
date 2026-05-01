package agent

import (
	"github.com/keakon/golog/log"
	"strings"

	"github.com/keakon/chord/internal/analytics"
	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
)

func (a *MainAgent) lookupModelCost(modelRef string) *config.ModelCost {
	providerName, modelID := analytics.SplitModelRef(modelRef)
	for _, cfg := range []*config.Config{a.projectConfig, a.globalConfig} {
		if cfg == nil {
			continue
		}
		if providerName != "" {
			if prov, ok := cfg.Providers[providerName]; ok {
				if mc, ok := prov.Models[modelID]; ok && mc.Cost != nil {
					return mc.Cost
				}
			}
		}
	}

	if modelID == "" {
		modelID = strings.TrimSpace(modelRef)
	}
	if modelID == "" {
		return nil
	}

	for _, cfg := range []*config.Config{a.projectConfig, a.globalConfig} {
		if cfg == nil {
			continue
		}
		for _, prov := range cfg.Providers {
			if mc, ok := prov.Models[modelID]; ok && mc.Cost != nil {
				return mc.Cost
			}
		}
	}
	return nil
}

func (a *MainAgent) currentAgentName() string {
	if role := strings.TrimSpace(a.CurrentRole()); role != "" {
		return role
	}
	return "builder"
}

func (a *MainAgent) SetUsageEventSink(fn func(event analytics.UsageEvent)) {
	a.usageEventSink = fn
}

func (a *MainAgent) emitUsageEvent(event analytics.UsageEvent) {
	if a.usageTracker != nil {
		a.usageTracker.AddUsageEvent(event)
	}
	if sink := a.usageEventSink; sink != nil {
		sink(event)
	}
	if a.usageLedger == nil {
		return
	}
	if err := a.usageLedger.AppendEvent(event); err != nil {
		log.Warnf("failed to append usage ledger event agent_id=%v purpose=%v running_model_ref=%v error=%v", event.AgentID, event.Purpose, event.RunningModelRef, err)
	}
}

func (a *MainAgent) recordCompactionPolicyAnalyticsEvent(detail string) {
	a.emitUsageEvent(analytics.UsageEvent{
		AgentID:          "main",
		AgentKind:        "main",
		AgentName:        a.currentAgentName(),
		Purpose:          compactionPolicyAnalyticsPurpose + "/" + detail,
		SelectedModelRef: a.ProviderModelRef(),
		RunningModelRef:  a.RunningModelRef(),
		TurnID:           a.currentTurnID(),
	})
}

func (a *MainAgent) recordCompactionFailureAnalyticsEvent(err error, class compactionFailureClass, stage string) {
	if err == nil {
		return
	}
	diagnostic := map[string]string{
		"class":  string(class),
		"reason": shortCompactionFailureReason(err),
	}
	if strings.TrimSpace(stage) != "" {
		diagnostic["stage"] = strings.TrimSpace(stage)
	}
	diagnostic["trigger"] = a.compactionState.trigger.analyticsName()
	a.emitUsageEvent(analytics.UsageEvent{
		AgentID:          "main",
		AgentKind:        "main",
		AgentName:        a.currentAgentName(),
		Purpose:          compactionFailureAnalyticsPurpose,
		SelectedModelRef: a.compactionModelRef(),
		RunningModelRef:  a.compactionModelRef(),
		TurnID:           a.currentTurnID(),
		Diagnostic:       diagnostic,
	})
}

func (a *MainAgent) recordUsage(
	agentID string,
	agentKind string,
	agentName string,
	purpose string,
	selectedModelRef string,
	runningModelRef string,
	turnID uint64,
	usage *message.TokenUsage,
) {
	if runningModelRef == "" {
		runningModelRef = selectedModelRef
	}
	costCfg := a.lookupModelCost(runningModelRef)

	var u message.TokenUsage
	if usage != nil {
		u = *usage
	}

	rawUsage := analytics.UsageSnapshotFromTokenUsage(u)
	billingUsage := analytics.NormalizeBillingUsage(rawUsage)
	a.emitUsageEvent(analytics.UsageEvent{
		AgentID:          agentID,
		AgentKind:        agentKind,
		AgentName:        agentName,
		Purpose:          purpose,
		TurnID:           turnID,
		SelectedModelRef: selectedModelRef,
		RunningModelRef:  runningModelRef,
		UsageRaw:         rawUsage,
		BillingUsage:     billingUsage,
		Cost:             analytics.CalculateUsageCost(costCfg, billingUsage),
		PricingSnapshot:  analytics.PricingSnapshotFromCost(costCfg),
	})
}
