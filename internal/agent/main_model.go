package agent

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/llm"
)

// ModelName returns the name of the model the agent is using.
func (a *MainAgent) ModelName() string {
	a.llmMu.RLock()
	defer a.llmMu.RUnlock()
	return a.modelName
}

// ProviderModelRef returns the selected model reference string for unique
// identification. It may include an inline @variant suffix when the selected
// model was configured that way, which lets the TUI distinguish model presets
// that share the same base provider/model.
func (a *MainAgent) ProviderModelRef() string {
	a.llmMu.RLock()
	defer a.llmMu.RUnlock()
	return a.providerModelRef
}

// RunningModelRef returns the effective provider/model for the TUI sidebar
// (focused SubAgent if any, else MainAgent). It may differ from
// ProviderModelRef() while fallback is in effect on that agent's client.
func (a *MainAgent) RunningModelRef() string {
	if sub := a.validFocusedSubAgent(); sub != nil && sub.llmClient != nil {
		ref := strings.TrimSpace(sub.llmClient.RunningModelRef())
		if ref == "" {
			ref = strings.TrimSpace(sub.llmClient.PrimaryModelRef())
		}
		return ref
	}
	a.llmMu.RLock()
	defer a.llmMu.RUnlock()
	if a.runningModelRef == "" {
		return a.providerModelRef
	}
	return a.runningModelRef
}

// NextRequestModelRef returns the provider/model ref the focused agent will use
// to start its next LLM request.
func (a *MainAgent) NextRequestModelRef() string {
	if sub := a.validFocusedSubAgent(); sub != nil && sub.llmClient != nil {
		ref := strings.TrimSpace(sub.llmClient.NextRequestModelRef())
		if ref == "" {
			ref = strings.TrimSpace(sub.llmClient.RunningModelRef())
		}
		if ref == "" {
			ref = strings.TrimSpace(sub.llmClient.PrimaryModelRef())
		}
		return ref
	}
	a.llmMu.RLock()
	client := a.llmClient
	providerRef := a.providerModelRef
	runningRef := a.runningModelRef
	a.llmMu.RUnlock()
	if client != nil {
		if ref := strings.TrimSpace(client.NextRequestModelRef()); ref != "" {
			return ref
		}
	}
	if strings.TrimSpace(providerRef) != "" {
		return strings.TrimSpace(providerRef)
	}
	return strings.TrimSpace(runningRef)
}

// RunningVariant returns the active variant name for the running model
// (focused SubAgent if any, else MainAgent), or empty string if none.
func (a *MainAgent) RunningVariant() string {
	if sub := a.validFocusedSubAgent(); sub != nil && sub.llmClient != nil {
		return sub.llmClient.ActiveVariant()
	}
	a.llmMu.RLock()
	defer a.llmMu.RUnlock()
	if a.llmClient == nil {
		return ""
	}
	return a.llmClient.ActiveVariant()
}

// SetProviderModelRef sets the initial selected model reference for the agent.
// The ref is usually "provider/model" and may optionally include an inline
// @variant suffix. Called from startup/model-switch wiring after construction.
func (a *MainAgent) SetProviderModelRef(ref string) {
	a.llmMu.Lock()
	defer a.llmMu.Unlock()
	a.providerModelRef = ref
	// Keep sidebar/source-of-truth aligned before the first successful LLM round.
	// Otherwise runningModelRef can stay as a bare model id (without provider).
	a.runningModelRef = ref
}

// SetModelSwitchFactory sets the factory used by SwitchModel to create a new
// LLM client from a "provider/model" reference string. Must be called before
// Run. The factory returns (client, displayModelName, contextLimit, error).
func (a *MainAgent) SetModelSwitchFactory(fn func(providerModel string) (*llm.Client, string, int, error)) {
	a.modelSwitchFactory = fn
	a.mainModelPolicyDirty.Store(true)
}

// SetModelPoolPolicy installs the runtime model pool policy. Must be called
// before Run. statePath is the per-project file for persisting pool selections.
func (a *MainAgent) SetModelPoolPolicy(policy *RuntimeModelPoolPolicy, statePath string) {
	a.modelPoolPolicy = policy
	a.modelPoolStatePath = statePath
}

// ModelPoolPolicy returns the current runtime model pool policy (read-only).
func (a *MainAgent) ModelPoolPolicy() *RuntimeModelPoolPolicy {
	return a.modelPoolPolicy
}

// SwapLLMClient atomically replaces the MainAgent's LLM client, model name,
// and context manager token budget. Thread-safe: called from the TUI goroutine
// while the event loop may be reading llmClient.
func (a *MainAgent) SwapLLMClient(newClient *llm.Client, modelName string, contextLimit int) {
	a.swapLLMClientWithRef(newClient, modelName, contextLimit, "")
}

// swapLLMClientWithRef is the internal implementation of SwapLLMClient that
// also atomically updates providerModelRef when non-empty.
func (a *MainAgent) swapLLMClientWithRef(newClient *llm.Client, modelName string, contextLimit int, providerModelRef string) {
	a.llmMu.Lock()
	oldClient := a.llmClient
	a.llmClient = newClient
	a.modelName = modelName
	if providerModelRef != "" {
		a.providerModelRef = providerModelRef
		a.runningModelRef = providerModelRef
	} else if a.providerModelRef != "" {
		a.runningModelRef = a.providerModelRef
	}
	a.installedSysPrompt = ""
	a.llmMu.Unlock()
	if oldClient != nil && oldClient != newClient {
		oldClient.InvalidateRouting("model_client_swapped")
	}
	a.noteContextSurfaceIdentityChanged()
	if newClient != nil {
		a.ctxMgr.SetTokenBudgets(contextLimit, newClient.InputLimitForModelRef(providerModelRef), a.effectiveCompactionReservedInput())
	} else {
		a.ctxMgr.SetMaxTokens(contextLimit)
	}

	// Wire the polled-rate-limit callback so that background /wham/usage poll
	// results push a RateLimitUpdatedEvent to the TUI immediately, instead of
	// waiting for the next unrelated render trigger.
	if provCfg := newClient.ProviderConfig(); provCfg != nil {
		provCfg.SetOnPolledRateLimitUpdated(func() {
			a.emitToTUI(RateLimitUpdatedEvent{Snapshot: nil})
		})
	}

	if n := a.ctxMgr.RepairOrphanToolMessagesInPlace(); n > 0 {
		log.Debugf("repaired orphan tool messages after LLM client swap dropped=%v model=%v", n, modelName)
	}

	// Re-install the already-built stable system prompt on the new LLM client
	// so the model-side state matches ctxMgr. This does not rebuild the prompt

	if prompt := a.ctxMgr.SystemPrompt().Content; prompt != "" {
		newClient.SetSystemPrompt(prompt)
	}

	log.Debugf("swapped LLM client model=%v context_limit=%v", modelName, contextLimit)
}

// SwitchModel switches the MainAgent to a different model at runtime. It is kept
// for internal tests and pool-driven client rebuilds; user-facing commands should
// switch pools via /models rather than choosing provider/model refs directly.
func (a *MainAgent) SwitchModel(providerModel string) error {
	return a.switchModel(providerModel, true)
}

// ApplyInitialModel applies the given model (e.g. from config or recovery) without
// showing a "Switched model" toast. Used at startup.
func (a *MainAgent) ApplyInitialModel(providerModel string) error {
	return a.switchModel(providerModel, false)
}

func (a *MainAgent) switchModel(providerModel string, showToast bool) error {
	if a.modelSwitchFactory == nil {
		return fmt.Errorf("model switch not configured")
	}

	client, modelName, ctxLimit, err := a.modelSwitchFactory(providerModel)
	if err != nil {
		return fmt.Errorf("create LLM client for %q: %w", providerModel, err)
	}
	a.applyServiceTierToClient(client)
	if sid := strings.TrimSpace(filepath.Base(a.sessionDir)); sid != "" && sid != "." {
		client.SetSessionID(sid)
	}

	selectedRef := strings.TrimSpace(client.NextRequestModelRef())
	if selectedRef == "" {
		selectedRef = strings.TrimSpace(client.PrimaryModelRef())
	}
	if selectedRef == "" {
		selectedRef = providerModel
	}
	a.swapLLMClientWithRef(client, modelName, ctxLimit, selectedRef)
	a.mainModelPolicyDirty.Store(false)
	if a.modelPoolPolicy != nil {
		cfg := a.currentActiveConfig()
		if cfg != nil {
			effectivePool := a.modelPoolPolicy.EffectivePool(cfg.Name, cfg)
			if effectivePool != "" {
				a.modelPoolPolicy.SetLastPicked(cfg.Name, effectivePool, selectedRef)
			}
		}
	}
	effectiveRunningRef := client.RunningModelRef()
	if effectiveRunningRef == "" {
		effectiveRunningRef = client.PrimaryModelRef()
	}
	if effectiveRunningRef == "" {
		effectiveRunningRef = selectedRef
	}
	a.emitToTUI(RunningModelChangedEvent{
		AgentID:          a.instanceID,
		ProviderModelRef: selectedRef,
		RunningModelRef:  effectiveRunningRef,
	})
	if showToast {
		displayRef := client.PrimaryModelRef()
		if displayRef == "" {
			displayRef = providerModel
		}
		displayRef = formatModelRefForNotification(displayRef, a.ProviderModelRef(), client.ActiveVariant())
		a.emitToTUI(ToastEvent{
			Message: fmt.Sprintf("Switched model to %s", displayRef),
			Level:   "info",
		})
	}
	return nil
}

// handleModelsCommand processes the /models slash command.
//   - "/models": emits ModelSelectEvent so the TUI opens the current-view selector.
//   - "/models status": shows current pool status as text.
//   - "/models <pool>": sets the current view's pool (main role or focused SubAgent).
//   - "/models --agent <name> <pool>": sets the named agent's pool.
//
// busy reports whether an active turn is in flight. When true, handlers skip
// setIdleAndDrainPending — clearing a.turn mid-retry corrupts turn state and
// breaks esc-cancel.
func (a *MainAgent) handleModelsCommand(content string, busy bool) {
	arg := strings.TrimSpace(strings.TrimPrefix(content, "/models"))
	if arg == "" {
		a.emitToTUI(ModelSelectEvent{Target: ModelPoolSelectorTarget{Kind: ModelPoolSelectorTargetCurrentView}})
		if !busy {
			a.setIdleAndDrainPending()
		}
		return
	}
	if arg == "status" {
		a.handleModelsStatus()
		if !busy {
			a.setIdleAndDrainPending()
		}
		return
	}
	if after, ok := strings.CutPrefix(arg, "--agent "); ok {
		a.handleModelsSetAgent(strings.TrimSpace(after))
		if !busy {
			a.setIdleAndDrainPending()
		}
		return
	}
	a.handleModelsSetCurrentView(arg)
	if !busy {
		a.setIdleAndDrainPending()
	}
}

func (a *MainAgent) ModelsStatusText() string {
	if a.modelPoolPolicy == nil {
		return "Model pool policy not configured"
	}
	var sb strings.Builder
	currentModelPool := a.modelPoolPolicy.CurrentModelPool()
	currentModelPoolStatus := ""
	if currentModelPool != "" {
		// Model pool is only applied to non-subagent roles.
		currentModelPoolDefined := false
		for _, cfg := range a.agentConfigs {
			if cfg != nil && !cfg.IsSubAgent() && cfg.HasPool(currentModelPool) {
				currentModelPoolDefined = true
				break
			}
		}
		if !currentModelPoolDefined {
			currentModelPoolStatus = " (missing)"
		}
	}
	sb.WriteString(fmt.Sprintf("Model pool: %s%s\n", currentModelPool, currentModelPoolStatus))
	overrides := a.modelPoolPolicy.Overrides()
	if len(overrides) > 0 {
		sb.WriteString("Fixed agent pools:\n")
		agentNames := make([]string, 0, len(overrides))
		for name := range overrides {
			agentNames = append(agentNames, name)
		}
		sort.Strings(agentNames)
		for _, name := range agentNames {
			pool := overrides[name]
			cfg := a.agentConfigs[name]
			status := ""
			if cfg != nil && !cfg.HasPool(pool) {
				status = " (missing)"
			}
			sb.WriteString(fmt.Sprintf("  %s: %s%s\n", name, pool, status))
		}
	}
	sb.WriteString("\nAgent effective pools:\n")
	agentNames := make([]string, 0, len(a.agentConfigs))
	for name := range a.agentConfigs {
		agentNames = append(agentNames, name)
	}
	sort.Strings(agentNames)
	for _, name := range agentNames {
		cfg := a.agentConfigs[name]
		pool := a.modelPoolPolicy.EffectivePool(name, cfg)
		models := a.modelPoolPolicy.EffectiveModels(name, cfg)
		if pool == "" {
			sb.WriteString(fmt.Sprintf("  %s: (no pool)\n", name))
		} else {
			sb.WriteString(fmt.Sprintf("  %s: %s (%d model(s))\n", name, pool, len(models)))
		}
	}
	return sb.String()
}

func (a *MainAgent) notifyMainRoutingChanged(reason string) {
	a.llmMu.RLock()
	client := a.llmClient
	a.llmMu.RUnlock()
	if client != nil {
		client.InvalidateRouting(reason)
	}
}

func (a *MainAgent) notifySubAgentRoutingChanged(agentName, reason string) {
	a.mu.RLock()
	var targets []*SubAgent
	for _, sub := range a.subAgents {
		if sub != nil && sub.agentDefName == agentName {
			targets = append(targets, sub)
		}
	}
	a.mu.RUnlock()
	for _, sub := range targets {
		sub.llmMu.RLock()
		client := sub.llmClient
		sub.llmMu.RUnlock()
		if client != nil {
			client.InvalidateRouting(reason)
		}
	}
}

func (a *MainAgent) handleModelsStatus() {
	a.emitToTUI(InfoEvent{Message: a.ModelsStatusText()})
}

func (a *MainAgent) handleModelsSetCurrentView(pool string) {
	if sub := a.validFocusedSubAgent(); sub != nil {
		if err := a.setAgentModelPool(sub.agentDefName, pool, false); err != nil {
			a.emitToTUI(ErrorEvent{Err: err})
		}
		return
	}
	if err := a.setCurrentModelPool(pool, true); err != nil {
		a.emitToTUI(ErrorEvent{Err: err})
	}
}

func (a *MainAgent) handleModelsSetAgent(rest string) {
	parts := strings.Fields(rest)
	if len(parts) != 2 {
		a.emitToTUI(ErrorEvent{Err: fmt.Errorf("/models --agent: usage: /models --agent <name> <pool>")})
		return
	}
	if err := a.setAgentModelPool(parts[0], parts[1], true); err != nil {
		a.emitToTUI(ErrorEvent{Err: err})
	}
}

func canonicalPoolMembershipRef(ref string, cfg *config.AgentConfig) string {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ""
	}
	baseRef, variant := config.ParseModelRef(ref)
	baseRef = strings.TrimSpace(baseRef)
	variant = strings.TrimSpace(variant)
	if variant == "" && cfg != nil {
		variant = strings.TrimSpace(cfg.Variant)
	}
	if variant == "" {
		return baseRef
	}
	return baseRef + "@" + variant
}

func modelRefInPool(current string, refs []string, cfg *config.AgentConfig) bool {
	currentCanonical := canonicalPoolMembershipRef(current, cfg)
	if currentCanonical == "" {
		return false
	}
	for _, ref := range refs {
		if canonicalPoolMembershipRef(ref, cfg) == currentCanonical {
			return true
		}
	}
	return false
}

func (a *MainAgent) switchMainModelForPoolIfNeeded(cfg *config.AgentConfig, pool string, oldPool string) error {
	if cfg == nil || a.modelPoolPolicy == nil {
		return nil
	}
	refs := a.modelPoolPolicy.EffectiveModels(cfg.Name, cfg)
	if len(refs) == 0 {
		return nil
	}
	current := a.ProviderModelRef()
	if pool == oldPool && modelRefInPool(current, refs, cfg) {
		return nil
	}
	if modelRefInPool(current, refs, cfg) {
		if a.modelSwitchFactory == nil {
			a.modelPoolPolicy.SetLastPicked(cfg.Name, pool, current)
			a.mainModelPolicyDirty.Store(true)
			return nil
		}
		return a.switchModel(current, false)
	}
	ref := a.modelPoolPolicy.ResolveInitialModelRef(cfg.Name, cfg)
	if ref == "" {
		return nil
	}
	if err := a.switchModel(ref, true); err != nil {
		return err
	}
	return nil
}

func (a *MainAgent) setCurrentModelPool(pool string, emitToast bool) error {
	if a.modelPoolPolicy == nil {
		return fmt.Errorf("/models: model pool policy not configured")
	}
	pool = strings.TrimSpace(pool)
	if pool == "" {
		return fmt.Errorf("/models: pool name required")
	}

	activeCfg := a.currentActiveConfig()
	if activeCfg == nil {
		return fmt.Errorf("/models: no active agent")
	}
	if !activeCfg.HasPool(pool) {
		return fmt.Errorf("/models: agent %q does not define pool %q", activeCfg.Name, pool)
	}

	oldPool := a.modelPoolPolicy.CurrentModelPool()
	if a.mainLLMRequestInFlight.Load() {
		a.capturePendingModelPoolRollback()
	}
	a.modelPoolPolicy.SetCurrentModelPool(pool)

	cfg := a.currentActiveConfig()
	if cfg != nil && a.mainLLMRequestInFlight.Load() {
		a.markMainModelPoolSwitchPending()
		a.notifyMainRoutingChanged("model_pool_changed")
	} else if cfg != nil {
		effectivePool := a.modelPoolPolicy.EffectivePool(cfg.Name, cfg)
		if err := a.switchMainModelForPoolIfNeeded(cfg, effectivePool, oldPool); err != nil {
			a.modelPoolPolicy.SetCurrentModelPool(oldPool)
			return fmt.Errorf("/models: switch model: %w", err)
		}
		a.notifyMainRoutingChanged("model_pool_changed")
	}
	a.saveModelPoolState()

	if emitToast {
		a.emitToTUI(ToastEvent{Message: fmt.Sprintf("Model pool set to %q", pool), Level: "info"})
	}
	return nil
}

func (a *MainAgent) switchActiveSubAgentsForPoolIfNeeded(agentName string, cfg *config.AgentConfig, pool string) error {
	if a.modelPoolPolicy == nil || a.modelSwitchFactory == nil || cfg == nil {
		return nil
	}
	refs := a.modelPoolPolicy.EffectiveModels(agentName, cfg)
	if len(refs) == 0 {
		return nil
	}
	a.mu.RLock()
	var targets []*SubAgent
	for _, sub := range a.subAgents {
		if sub != nil && sub.agentDefName == agentName {
			targets = append(targets, sub)
		}
	}
	a.mu.RUnlock()
	for _, sub := range targets {
		current := ""
		if sub.llmClient != nil {
			current = sub.llmClient.PrimaryModelRef()
		}
		if modelRefInPool(current, refs, cfg) {
			a.modelPoolPolicy.SetLastPicked(agentName, pool, current)
			continue
		}
		ref := a.modelPoolPolicy.ResolveInitialModelRef(agentName, cfg)
		if ref == "" {
			continue
		}
		client, modelName, ctxLimit, err := a.modelSwitchFactory(ref)
		if err != nil {
			return fmt.Errorf("create LLM client for %q: %w", ref, err)
		}
		a.applyServiceTierToClient(client)
		if sid := strings.TrimSpace(filepath.Base(a.sessionDir)); sid != "" && sid != "." {
			client.SetSessionID(sid)
		}
		sub.switchModel(client, modelName, ctxLimit)
		a.modelPoolPolicy.SetLastPicked(agentName, pool, client.PrimaryModelRef())
	}
	return nil
}

func (a *MainAgent) setAgentModelPool(agentName, pool string, emitToast bool) error {
	if a.modelPoolPolicy == nil {
		return fmt.Errorf("/models: model pool policy not configured")
	}
	cfg, ok := a.agentConfigs[agentName]
	if !ok {
		return fmt.Errorf("/models: unknown agent %q", agentName)
	}
	pool = strings.TrimSpace(pool)
	if pool == "" {
		return fmt.Errorf("/models: pool name required")
	}
	if !cfg.HasPool(pool) {
		return fmt.Errorf("/models: agent %q does not define pool %q", agentName, pool)
	}

	prev, hadOverride := a.modelPoolPolicy.AgentOverride(agentName)
	agentInFlight := a.agentModelPoolSwitchInFlight(agentName)
	if a.mainLLMRequestInFlight.Load() || agentInFlight {
		a.capturePendingModelPoolRollback()
	}
	a.modelPoolPolicy.SetAgentOverride(agentName, pool)

	if cfg.Name == a.CurrentRole() && a.mainLLMRequestInFlight.Load() {
		a.markMainModelPoolSwitchPending()
		a.notifyMainRoutingChanged("agent_model_pool_changed")
	} else if cfg.Name == a.CurrentRole() {
		effectivePool := a.modelPoolPolicy.EffectivePool(cfg.Name, cfg)
		if err := a.switchMainModelForPoolIfNeeded(cfg, effectivePool, prev); err != nil {
			if hadOverride {
				a.modelPoolPolicy.SetAgentOverride(agentName, prev)
			} else {
				a.modelPoolPolicy.ClearAgentOverride(agentName)
			}
			return fmt.Errorf("/models: switch model: %w", err)
		}
		a.notifyMainRoutingChanged("agent_model_pool_changed")
	} else if a.mainLLMRequestInFlight.Load() || agentInFlight {
		a.markAgentModelPoolSwitchPending(agentName)
		a.notifySubAgentRoutingChanged(agentName, "agent_model_pool_changed")
	} else if err := a.switchActiveSubAgentsForPoolIfNeeded(agentName, cfg, pool); err != nil {
		if hadOverride {
			a.modelPoolPolicy.SetAgentOverride(agentName, prev)
		} else {
			a.modelPoolPolicy.ClearAgentOverride(agentName)
		}
		return fmt.Errorf("/models: switch model: %w", err)
	} else {
		a.notifySubAgentRoutingChanged(agentName, "agent_model_pool_changed")
	}
	a.saveModelPoolState()

	if emitToast {
		a.emitToTUI(ToastEvent{Message: fmt.Sprintf("Agent %q pool set to %q", agentName, pool), Level: "info"})
	}
	return nil
}
