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
	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/jsonx"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
)

const eventRequestUsage = "request_usage"

type requestUsagePayload struct {
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

func (a *MainAgent) continuationUsageObserver(client *llm.Client, target *walltimeTarget, attr usageAttribution) func(llm.ContinuationUsage) {
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
			data, _ := jsonx.Marshal(source)
			var cost config.ModelCost
			if json.Unmarshal(data, &cost) == nil {
				prices[ref] = &cost
			}
		}
	}
	attr = requestUsageAttribution(target, attr)
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
		evt := analytics.UsageEvent{EventID: fmt.Sprintf("continuation:%s:%d", receipt.ChainID, receipt.Request), OccurredAt: time.Now(), AgentID: attr.AgentID, AgentKind: attr.AgentKind, AgentName: attr.AgentName, TaskID: attr.TaskID, Purpose: attr.Purpose, TurnID: attr.TurnID, SelectedModelRef: attr.SelectedModelRef, RunningModelRef: receipt.ModelRef, UsageRaw: raw, BillingUsage: billing, Cost: analytics.CalculateUsageCost(price, billing, receipt.ServiceTier), PricingSnapshot: analytics.PricingSnapshotFromCost(price, billing, receipt.ServiceTier), RequestNs: receipt.RequestNs, FirstOutputNs: receipt.FirstOutputNs, Diagnostic: map[string]string{"chain": receipt.ChainID, "request": fmt.Sprint(receipt.Request), "outcome": receipt.Outcome, "usage_state": state}}
		a.publishRequestUsage(target, evt)
	}
}

func (a *MainAgent) discardedFirstOutputObserver(target *walltimeTarget, attr usageAttribution) func(string, int64) {
	attr = requestUsageAttribution(target, attr)
	return func(modelRef string, latencyNs int64) {
		a.publishRequestUsage(target, analytics.UsageEvent{
			EventID:          makeRequestID(),
			OccurredAt:       time.Now(),
			AgentID:          attr.AgentID,
			AgentKind:        attr.AgentKind,
			AgentName:        attr.AgentName,
			TaskID:           attr.TaskID,
			Purpose:          attr.Purpose,
			TurnID:           attr.TurnID,
			SelectedModelRef: attr.SelectedModelRef,
			RunningModelRef:  modelRef,
			FirstOutputNs:    latencyNs,
			FirstOutputOnly:  true,
		})
	}
}

func requestUsageAttribution(target *walltimeTarget, attr usageAttribution) usageAttribution {
	if target != nil {
		attr.AgentID = target.agentID
		attr.AgentName = target.agentName
		attr.TurnID = target.turnID
	} else if attr.AgentID == "" {
		attr.AgentID = identity.MainAgentID
	}
	return attr
}

func (a *MainAgent) publishRequestUsage(target *walltimeTarget, event analytics.UsageEvent) {
	if target != nil && target.ledger != nil {
		if err := target.ledger.AppendEvent(event); err != nil {
			log.Warnf("failed to append request usage: %v", err)
		}
	}
	a.sendEvent(Event{Type: eventRequestUsage, Payload: requestUsagePayload{target: target, event: event}})
}

func (a *MainAgent) handleRequestUsage(payload requestUsagePayload) {
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
