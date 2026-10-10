package agent

import (
	"strings"

	"github.com/keakon/chord/internal/llm"
)

// FocusedModelState captures one target identity and resolves all model/provider
// display data from it. Preparing a new request does not change context budgets.
func (a *MainAgent) FocusedModelState() FocusedModelState {
	target := a.focusedAgentSnapshot()
	client, state := a.modelStateForTarget(target)
	state.PoolName, state.PoolNames = a.focusedModelPools(target)
	if client != nil {
		state.KeysConfirmed, state.KeysTotal = client.KeyStatsForRef(state.DisplayRef)
		state.ServiceTier = client.ServiceTier()
		state.EffectiveTier = a.effectiveDisplayServiceTier(client, state.DisplayRef)
		if a.providerUsesCodexRateLimit(providerNameFromModelRef(state.DisplayRef)) {
			state.RateLimit = client.CurrentRateLimitSnapshotForRef(state.DisplayRef)
		}
	}
	return state
}

// modelStateForTarget is a non-routing helper: all reads refer to the target
// captured by the caller, including parked and settled tasks.
func (a *MainAgent) modelStateForTarget(target focusedAgentSnapshot) (*llm.Client, FocusedModelState) {
	state := FocusedModelState{}
	var client *llm.Client
	if target.sub != nil {
		client, _ = target.sub.llmSnapshot()
		if client != nil {
			state.SelectedRef = client.PrimaryModelRef()
			if variant := client.ActiveVariant(); variant != "" {
				state.SelectedRef += "@" + variant
			}
			state.RunningRef = client.RunningModelRef()
		}
	} else if (target.parked || target.settled) && target.task != nil {
		state.SelectedRef = a.restoredSubAgentModelRef(target.task)
		state.RunningRef = restoredRunningModelRef(target.task, state.SelectedRef)
		state.DisplayRef = state.RunningRef
		return nil, state
	} else {
		a.llmMu.RLock()
		client = a.llmClient
		state.SelectedRef = strings.TrimSpace(a.providerModelRef)
		state.RunningRef = strings.TrimSpace(a.runningModelRef)
		a.llmMu.RUnlock()
	}
	if state.RunningRef == "" {
		state.RunningRef = state.SelectedRef
	}
	if client != nil {
		ref, active := client.DisplayModelRef()
		state.DisplayRef = ref
		if !active {
			state.DisplayRef = a.previewModelPoolRef(target, state.SelectedRef, ref)
		}
	}
	if state.DisplayRef == "" {
		state.DisplayRef = state.RunningRef
	}
	return client, state
}

// previewModelPoolRef mirrors pool installation's selection without creating a
// client or changing tool surfaces. A pending pool can name a provider absent
// from the installed client; its unavailable key/limit data stays empty.
func (a *MainAgent) previewModelPoolRef(target focusedAgentSnapshot, selected, cursor string) string {
	if a.modelPoolPolicy == nil {
		return cursor
	}
	cfg := a.currentActiveConfig()
	a.stateMu.RLock()
	pending := a.pendingMainModelPoolSwitch
	if target.sub != nil {
		cfg = a.agentConfigs[target.sub.agentDefName]
		_, pending = a.pendingAgentModelPoolSwitch[target.sub.agentDefName]
	}
	a.stateMu.RUnlock()
	if !pending || cfg == nil {
		return cursor
	}
	refs := a.modelPoolPolicy.EffectiveModels(cfg.Name, cfg)
	if len(refs) == 0 {
		return cursor
	}
	if modelRefInPool(selected, refs, cfg) {
		// Main pool installation rebuilds the client at the selected model.
		// Workers retain their client when its primary remains in the pool.
		if target.sub != nil {
			return cursor
		}
		return canonicalPoolMembershipRef(selected, cfg)
	}
	return a.modelPoolPolicy.ResolveInitialModelRef(cfg.Name, cfg)
}
