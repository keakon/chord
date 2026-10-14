package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/analytics"
	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/contextdiag"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
)

const eventContinuationUsage = "continuation_usage"

type continuationUsagePayload struct {
	target *walltimeTarget
	event  analytics.UsageEvent
}
type continuationWalltimeKey struct{}

func requestUsageTarget(request *requestWallclock) *walltimeTarget {
	if request == nil {
		return nil
	}
	return request.target
}

func (a *MainAgent) continuationUsageObserver(client *llm.Client, target *walltimeTarget, kind, name, purpose, selected string, turnID uint64) func(llm.ContinuationUsage) {
	prices := make(map[string]*config.ModelCost)
	pool, _ := client.ModelPoolSnapshot()
	for _, item := range pool {
		ref := item.ProviderConfig.Name() + "/" + item.ModelID
		if item.Variant != "" {
			ref += "@" + item.Variant
		}
		// Freeze nested price tiers and service multipliers as well as the top-level value.
		model, _ := item.ProviderConfig.GetModel(item.ModelID)
		if source := model.Cost; source != nil {
			data, _ := json.Marshal(source)
			var cost config.ModelCost
			if json.Unmarshal(data, &cost) == nil {
				prices[ref] = &cost
			}
		}
	}
	id := "main"
	if target != nil {
		id = target.agentID
		name = target.agentName
		turnID = target.turnID
	}
	return func(receipt llm.ContinuationUsage) {
		var usage message.TokenUsage
		if receipt.Usage != nil {
			usage = *receipt.Usage
		}
		raw := analytics.UsageSnapshotFromTokenUsage(usage)
		billing := analytics.NormalizeBillingUsage(raw)
		price := prices[receipt.ModelRef]
		state := receipt.UsageState
		if receipt.Usage == nil {
			state = contextdiag.Missing
		}
		evt := analytics.UsageEvent{EventID: fmt.Sprintf("continuation:%s:%d", receipt.ChainID, receipt.Request), OccurredAt: time.Now(), AgentID: id, AgentKind: kind, AgentName: name, Purpose: purpose, TurnID: turnID, SelectedModelRef: selected, RunningModelRef: receipt.ModelRef, UsageRaw: raw, BillingUsage: billing, Cost: analytics.CalculateUsageCost(price, billing, receipt.ServiceTier), PricingSnapshot: analytics.PricingSnapshotFromCost(price, billing, receipt.ServiceTier), Diagnostic: map[string]string{"chain": receipt.ChainID, "request": fmt.Sprint(receipt.Request), "outcome": receipt.Outcome, "usage_state": state}}
		payload := continuationUsagePayload{target: target, event: evt}
		if target != nil && target.ledger != nil {
			err := target.ledger.AppendEvent(evt)
			if err != nil {
				log.Warnf("failed to append continuation usage: %v", err)
			}
		}
		a.sendEvent(Event{Type: eventContinuationUsage, Payload: payload})
	}
}
func (a *MainAgent) handleContinuationUsage(payload continuationUsagePayload) {
	if payload.target != nil && !a.walltime.isCurrentTarget(payload.target) {
		return
	}
	if a.usageTracker != nil {
		a.usageTracker.AddUsageEvent(payload.event)
	}
	if a.usageEventSink != nil {
		a.usageEventSink(payload.event)
	}
}
func (r *walltimeRecorder) isCurrentTarget(target *walltimeTarget) bool {
	if r == nil || target == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return target.recorder == r && target.generation == r.generation && target.ledger == r.ledger
}
func (r *walltimeRecorder) compactionTarget(planID uint64) *walltimeTarget {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.compaction[planID].target
}
func withCompactionUsageTarget(ctx context.Context, r *walltimeRecorder, id uint64) context.Context {
	return context.WithValue(ctx, continuationWalltimeKey{}, r.compactionTarget(id))
}
