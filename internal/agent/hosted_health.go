package agent

import (
	"time"

	"github.com/keakon/chord/internal/llm"
)

const (
	hostedNoCallCooldown  = 30 * time.Second
	hostedNoCallThreshold = 2
)

type hostedHealthKey struct {
	source   hostedRouteSource
	tool     string
	provider *llm.ProviderConfig
	model    string
	variant  string
}

type hostedHealth struct {
	generation uint64
	failures   int
	until      time.Time
	probe      bool
}

func hostedTargetHealthKey(plan hostedRoutePlan, tool string, target llm.FallbackModel) hostedHealthKey {
	return hostedHealthKey{source: plan.source, tool: tool, provider: target.ProviderConfig, model: target.ModelID, variant: target.Variant}
}

// acquireHostedProbe leaves static capability untouched. Only repeated missing
// executions cool this specific tool; ordinary chat responses cannot heal it.
func (b *hostedBackend) acquireHostedProbe(key hostedHealthKey) (uint64, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	state, exists := b.health[key]
	if state.failures >= hostedNoCallThreshold {
		if time.Until(state.until) > 0 || state.probe {
			return 0, &llm.HostedTargetCoolingError{Tool: key.tool, Until: state.until, ProbeInFlight: state.probe}
		}
		state.probe = true
		exists = false // A new probe invalidates earlier in-flight completions.
	}
	if !exists {
		b.nextHealthGeneration++
		state.generation = b.nextHealthGeneration
	}
	b.health[key] = state
	return state.generation, nil
}

func (b *hostedBackend) finishHostedProbe(caller *hostedCaller, key hostedHealthKey, generation uint64, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	state, exists := b.health[key]
	if !exists || state.generation != generation {
		return
	}
	state.probe = false
	// Release the probe even when its caller retired while using a shared pool.
	b.health[key] = state
	binding, ok := b.callerBindings[caller.id]
	if !ok || binding.source != caller.source || !b.callerActive(caller) {
		return
	}
	if err == nil {
		delete(b.health, key)
		return
	}
	if llm.ClassifyHostedFailure(err) == llm.HostedFailureNotObserved {
		state.failures++
		if state.failures >= hostedNoCallThreshold {
			state.until = time.Now().Add(hostedNoCallCooldown)
		}
	}
	if state.failures > 0 {
		b.health[key] = state
	} else {
		delete(b.health, key)
	}
}
