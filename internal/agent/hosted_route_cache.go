package agent

import (
	"fmt"
	"slices"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/tools"
)

type hostedRouteCacheKey struct {
	source hostedRouteSource
	tool   string
}

type hostedDiagnosticKey struct {
	hostedRouteCacheKey
	reason string
}

type hostedCallerBinding struct {
	client *llm.Client
	source hostedRouteSource
}

// bindCaller snapshots the pool while the caller's llmMu is held. Model
// replacement invalidates the binding under the same lock, so an old request
// cannot republish a route after the replacement, even for identical contents.
func (b *hostedBackend) bindCaller(caller *hostedCaller) (*hostedCaller, error) {
	if caller.client == nil || caller.client.IsClosed() {
		b.mu.Lock()
		b.forgetCallerLocked(caller.id)
		b.mu.Unlock()
		return nil, fmt.Errorf("hosted tool caller %q has no active model client", viewCallerLabel(caller.id))
	}
	caller.pool, caller.cursor = caller.client.ModelPoolSnapshot()
	generation := hostedTargetsGeneration(caller.pool)
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.callerActive(caller) {
		b.forgetCallerLocked(caller.id)
		return nil, fmt.Errorf("hosted tool caller %q is no longer active", viewCallerLabel(caller.id))
	}
	binding, ok := b.callerBindings[caller.id]
	if !ok || binding.client != caller.client || binding.source.generation != generation {
		b.forgetCallerLocked(caller.id)
		b.nextCallerEpoch++
		binding = hostedCallerBinding{
			client: caller.client,
			source: hostedRouteSource{kind: hostedRouteSourceCaller, id: caller.id, generation: generation, epoch: b.nextCallerEpoch},
		}
		b.callerBindings[caller.id] = binding
	}
	caller.source = binding.source
	return caller, nil
}

// callerActive must not acquire llmMu: binding/cache writes already serialize
// with model replacement through llmMu -> b.mu. Registry lookups happen in
// resolveCaller before either lock; shutdown may hold the registry lock while
// closing clients, so cache operations must never acquire it.
func (b *hostedBackend) callerActive(caller *hostedCaller) bool {
	if caller.client == nil || caller.client.IsClosed() || b.agent.shuttingDown.Load() {
		return false
	}
	if b.agent.parentCtx != nil && b.agent.parentCtx.Err() != nil {
		return false
	}
	if caller.id == "" {
		return true
	}
	sub := caller.sub
	return sub != nil && !isTerminalSubAgentState(sub.State()) &&
		(sub.parentCtx == nil || sub.parentCtx.Err() == nil)
}

func (b *hostedBackend) preferredTargets(plan hostedRoutePlan, tool string) []llm.FallbackModel {
	b.mu.Lock()
	route, ok := b.routes[hostedRouteCacheKey{source: plan.source, tool: tool}]
	b.mu.Unlock()
	if ok {
		for i, target := range plan.targets {
			if target.ProviderConfig == route.provider && target.ModelID == route.model && target.Variant == route.variant {
				if i > 0 {
					return slices.Concat(plan.targets[i:], plan.targets[:i])
				}
				break
			}
		}
	}
	return plan.targets
}

// rememberTarget validates the live binding, rather than recomputing a plan
// from the captured client. Named routes stay shared across callers; invalid
// callers cannot recreate either kind of route after teardown.
func (b *hostedBackend) rememberTarget(caller *hostedCaller, spec tools.HostedToolSpec, plan hostedRoutePlan, tool string, target llm.FallbackModel) {
	current, err := b.resolveCaller(caller.id, nil)
	if err != nil {
		return
	}
	currentPlan := b.planRoute(current, spec)
	if currentPlan.source != plan.source {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	binding, ok := b.callerBindings[current.id]
	if !ok || binding.client != current.client || binding.source != current.source || !b.callerActive(current) {
		return
	}
	if plan.source.kind == hostedRouteSourceCaller && current.source != caller.source {
		return
	}
	targetStillRoutable := false
	for _, candidate := range currentPlan.targets {
		if candidate.ProviderConfig == target.ProviderConfig && candidate.ModelID == target.ModelID && candidate.Variant == target.Variant {
			targetStillRoutable = true
			break
		}
	}
	if !targetStillRoutable {
		return
	}
	b.routes[hostedRouteCacheKey{source: plan.source, tool: tool}] = hostedRoute{
		provider: target.ProviderConfig, model: target.ModelID, variant: target.Variant,
	}
}

func (b *hostedBackend) diagnoseOnce(source hostedRouteSource, tool, reason string) {
	if reason == "" {
		return
	}
	key := hostedDiagnosticKey{source: source, tool: tool, reason: reason}
	b.mu.Lock()
	if source.kind == hostedRouteSourceCaller && b.callerBindings[source.id].source != source {
		b.mu.Unlock()
		return
	}
	_, done := b.seenDiag[key]
	if !done {
		b.seenDiag[key] = struct{}{}
	}
	b.mu.Unlock()
	if !done {
		log.Warnf("hosted tool %q routing source %q: %v", tool, source.key(), reason)
	}
}

func (b *hostedBackend) forgetCallerLocked(callerID string) {
	delete(b.callerBindings, callerID)
	for key := range b.routes {
		if key.source.kind == hostedRouteSourceCaller && key.source.id == callerID {
			delete(b.routes, key)
		}
	}
	for key := range b.seenDiag {
		if key.source.kind == hostedRouteSourceCaller && key.source.id == callerID {
			delete(b.seenDiag, key)
		}
	}
}

// forgetHostedCaller is called under llmMu when replacing a client, and after
// releasing registry/state locks when ending a runtime. Named-pool affinity
// belongs to the immutable pool and survives individual caller teardown.
func (a *MainAgent) forgetHostedCaller(callerID string) {
	if b, ok := a.hostedBackend.(*hostedBackend); ok {
		b.mu.Lock()
		b.forgetCallerLocked(callerID)
		b.mu.Unlock()
	}
}

func (a *MainAgent) resetHostedCallers() {
	if b, ok := a.hostedBackend.(*hostedBackend); ok {
		b.mu.Lock()
		clear(b.callerBindings)
		for key := range b.routes {
			if key.source.kind == hostedRouteSourceCaller {
				delete(b.routes, key)
			}
		}
		for key := range b.seenDiag {
			if key.source.kind == hostedRouteSourceCaller {
				delete(b.seenDiag, key)
			}
		}
		b.mu.Unlock()
	}
}
