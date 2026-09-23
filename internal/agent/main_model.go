package agent

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
)

// ModelOption describes a model available for runtime switching.
type ModelOption struct {
	ProviderModel string // e.g. "anthropic-main/claude-opus-4.7" or "anthropic-main/claude-opus-4.7@high"
	ProviderName  string // e.g. "anthropic-main"
	ModelID       string // e.g. "claude-opus-4.7"
	ContextLimit  int
	OutputLimit   int
}

// SupportsInput reports whether the active main-agent model accepts the given
// input modality (e.g. "image", "pdf").
func (a *MainAgent) SupportsInput(modality string) bool {
	if a == nil {
		return false
	}
	a.llmMu.RLock()
	client := a.llmClient
	a.llmMu.RUnlock()
	return client != nil && client.SupportsInput(modality)
}

// SupportsViewImageTool reports whether the stable primary model for this agent
// can expose view_image. It intentionally follows the model-pool primary rather
// than the current fallback cursor so the tool surface does not change as
// fallback routing moves between candidates.
func (a *MainAgent) SupportsViewImageTool() bool {
	if a == nil {
		return false
	}
	a.llmMu.RLock()
	client := a.llmClient
	a.llmMu.RUnlock()
	return client != nil && client.PrimarySupportsViewImageTool()
}

// filterUnsupportedParts removes image/pdf parts that the current model does
// not support. When parts are removed, a toast is emitted to notify the user.
// If all non-text parts are removed, the result falls back to plain text.
func (a *MainAgent) filterUnsupportedParts(content string, parts []message.ContentPart) (string, []message.ContentPart) {
	if len(parts) == 0 {
		return content, parts
	}

	a.llmMu.RLock()
	client := a.llmClient
	modelName := a.modelName
	a.llmMu.RUnlock()
	if client == nil {
		return content, parts
	}

	var filtered []message.ContentPart
	var dropped []string
	for _, p := range parts {
		switch p.Type {
		case message.ContentPartImage:
			if !client.SupportsInput("image") {
				dropped = append(dropped, "image")
				continue
			}
		case message.ContentPartPDF:
			if !client.SupportsInput("pdf") {
				dropped = append(dropped, "pdf")
				continue
			}
		}
		filtered = append(filtered, p)
	}

	if len(dropped) == 0 {
		return content, parts
	}

	// Deduplicate dropped types for the toast message.
	seen := map[string]bool{}
	var unique []string
	for _, d := range dropped {
		if !seen[d] {
			seen[d] = true
			unique = append(unique, d)
		}
	}
	if a.unsupportedPartToast.first(modelName, toastCategoryInput, droppedSummary(unique)) {
		a.emitToTUI(ToastEvent{
			Message: "The current model does not support " + strings.Join(unique, "/") + " input; attachments were ignored",
			Level:   "warn",
		})
	}

	// If only text parts remain, collapse to plain content.
	if len(filtered) == 0 {
		return content, nil
	}
	allText := true
	for _, p := range filtered {
		if p.Type != message.ContentPartText {
			allText = false
			break
		}
	}
	if allText && len(filtered) == 1 {
		return filtered[0].Text, nil
	}
	return content, filtered
}

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
	if sub := a.validFocusedSubAgent(); sub != nil {
		client, _ := sub.llmSnapshot()
		if client == nil {
			return ""
		}
		ref := strings.TrimSpace(client.RunningModelRef())
		if ref == "" {
			ref = strings.TrimSpace(client.PrimaryModelRef())
		}
		return ref
	}
	if rec := a.focusedDurableTask(); rec != nil {
		return ""
	}
	a.llmMu.RLock()
	defer a.llmMu.RUnlock()
	if a.runningModelRef == "" {
		return a.providerModelRef
	}
	return a.runningModelRef
}

// applyRunningModelRefIfCurrent applies a captured client's model identity and
// budgets only while that client is still installed. A nil capture applies
// unconditionally. modelUpdateMu serializes the state change and its event with
// other request callbacks and model installations; llmMu protects readers but
// is released before sending the event so output backpressure cannot block them.
func (a *MainAgent) applyRunningModelRefIfCurrent(llmClient *llm.Client, ref string, contextLimit, inputLimit int) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return
	}
	if llmClient != nil {
		if contextLimit <= 0 {
			contextLimit = llmClient.ContextLimitForModelRef(ref)
		}
		if inputLimit <= 0 {
			inputLimit = llmClient.InputLimitForModelRef(ref)
		}
	}
	a.modelUpdateMu.Lock()
	defer a.modelUpdateMu.Unlock()
	a.llmMu.Lock()
	if llmClient != nil && a.llmClient != llmClient {
		a.llmMu.Unlock()
		return
	}
	if a.ctxMgr != nil && contextLimit > 0 {
		a.ctxMgr.SetTokenBudgets(contextLimit, inputLimit, a.effectiveCompactionReservedInput())
	}
	prev := a.runningModelRef
	a.runningModelRef = ref
	provRef := a.providerModelRef
	a.llmMu.Unlock()
	if ref == prev {
		return
	}
	a.emitToTUI(RunningModelChangedEvent{
		AgentID:          identity.MainAgentID,
		ProviderModelRef: provRef,
		RunningModelRef:  ref,
	})
}

// setUsageObservationModelRef records the model whose window produced the
// current ctxmgr size observation ("" when no observation is recorded).
func (a *MainAgent) setUsageObservationModelRef(modelRef string) {
	if a == nil {
		return
	}
	a.llmMu.Lock()
	a.usageObservationModelRef = strings.TrimSpace(modelRef)
	a.llmMu.Unlock()
}

// clearUsageObservation drops the size observation together with its model
// ownership, so the next decision starts from unknown and no stale owner can
// keep a later invalidation from running.
func (a *MainAgent) clearUsageObservation() {
	if a == nil || a.ctxMgr == nil {
		return
	}
	a.ctxMgr.ClearLastTokenUsage()
	a.setUsageObservationModelRef("")
}

// syncRunningModelRefToCursorHead realigns the sidebar with the sticky model
// cursor after a request ended without a confirmed switch. The cursor head is the
// model the next request will start from, so keeping a failed attempt's target
// would show that model's name with the cursor model's keys, window, and limits.
// The cursor is read from the captured client before locking; if a concurrent
// switch supersedes it, the currency check under the same lock discards the
// stale read instead of overwriting the new identity.
func (a *MainAgent) syncRunningModelRefToCursorHead(llmClient *llm.Client) {
	if llmClient == nil {
		return
	}
	ref := strings.TrimSpace(llmClient.NextRequestModelRef())
	if ref == "" {
		return
	}
	a.applyRunningModelRefIfCurrent(llmClient, ref, 0, 0)
}

// FocusedModelState returns an atomic-by-target model view for the TUI. Values
// for parked SubAgents come from durable state and current agent configuration;
// callers do not need separate live/parked fallbacks.
func (a *MainAgent) FocusedModelState() FocusedModelState {
	target := a.focusedAgentSnapshot()
	if target.sub != nil {
		client, _ := target.sub.llmSnapshot()
		selected, running, variant := "", "", ""
		if client != nil {
			selected = strings.TrimSpace(client.PrimaryModelRef())
			variant = strings.TrimSpace(client.ActiveVariant())
			if variant != "" && selected != "" {
				selected += "@" + variant
			}
			running = formatModelRefForNotification(client.RunningModelRef(), selected, variant)
		}
		pool, pools := a.focusedModelPools(target)
		return FocusedModelState{SelectedRef: selected, RunningRef: running, Variant: variant, PoolName: pool, PoolNames: pools}
	}
	if (target.parked || target.settled) && target.task != nil {
		selected := a.restoredSubAgentModelRef(target.task)
		running := restoredRunningModelRef(target.task, selected)
		_, variant := config.ParseModelRef(selected)
		pool, pools := a.focusedModelPools(target)
		return FocusedModelState{SelectedRef: selected, RunningRef: running, Variant: variant, PoolName: pool, PoolNames: pools}
	}
	a.llmMu.RLock()
	selected := strings.TrimSpace(a.providerModelRef)
	running := strings.TrimSpace(a.runningModelRef)
	variant := ""
	if a.llmClient != nil {
		variant = strings.TrimSpace(a.llmClient.ActiveVariant())
	}
	a.llmMu.RUnlock()
	if running == "" {
		running = selected
	}
	pool, pools := a.focusedModelPools(target)
	return FocusedModelState{SelectedRef: selected, RunningRef: running, Variant: variant, PoolName: pool, PoolNames: pools}
}

func (a *MainAgent) focusedModelPools(target focusedAgentSnapshot) (string, []string) {
	if a.modelPoolPolicy == nil {
		return "", nil
	}
	cfg := a.currentActiveConfig()
	if target.sub != nil {
		a.stateMu.RLock()
		cfg = a.agentConfigs[target.sub.agentDefName]
		a.stateMu.RUnlock()
	} else if (target.parked || target.settled) && target.task != nil {
		a.stateMu.RLock()
		cfg = a.agentConfigs[target.task.AgentDefName]
		a.stateMu.RUnlock()
	}
	if cfg == nil {
		return "", nil
	}
	return a.modelPoolPolicy.EffectivePool(cfg.Name, cfg), cfg.PoolNames()
}

// NextRequestModelRef returns the provider/model ref the focused agent will use
// to start its next LLM request.
func (a *MainAgent) NextRequestModelRef() string {
	if sub := a.validFocusedSubAgent(); sub != nil {
		client, _ := sub.llmSnapshot()
		if client == nil {
			return ""
		}
		ref := strings.TrimSpace(client.NextRequestModelRef())
		if ref == "" {
			ref = strings.TrimSpace(client.RunningModelRef())
		}
		if ref == "" {
			ref = strings.TrimSpace(client.PrimaryModelRef())
		}
		return ref
	}
	if rec := a.focusedDurableTask(); rec != nil {
		return ""
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
	if sub := a.validFocusedSubAgent(); sub != nil {
		client, _ := sub.llmSnapshot()
		if client == nil {
			return ""
		}
		return client.ActiveVariant()
	}
	if rec := a.focusedDurableTask(); rec != nil {
		return ""
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
	a.modelUpdateMu.Lock()
	defer a.modelUpdateMu.Unlock()
	a.llmMu.Lock()
	defer a.llmMu.Unlock()
	a.providerModelRef = ref
	// Keep sidebar/source-of-truth aligned before the first successful LLM round.
	// Otherwise runningModelRef can stay as a bare model id (without provider).
	a.runningModelRef = ref
}

// SetModelSwitchFactory sets the factory used to create MainAgent LLM clients
// for model switches, role switches, SubAgent pool rebuilds, and deferred
// main-model policy rebuilds. Must be called before Run. Each call supplies
// the model chain and default variant of the role the new client will run
// under, and the factory must build its fallback pool from those rather than
// from whichever role happens to be active at call time. The factory returns
// (client, displayModelName, contextLimit, error).
func (a *MainAgent) SetModelSwitchFactory(fn func(providerModel string, poolRefs []string, poolVariant string) (*llm.Client, string, int, error)) {
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
	a.modelUpdateMu.Lock()
	defer a.modelUpdateMu.Unlock()
	a.swapLLMClientWithRefLocked(newClient, modelName, contextLimit, providerModelRef)
}

func (a *MainAgent) swapLLMClientWithRefLocked(newClient *llm.Client, modelName string, contextLimit int, providerModelRef string) {
	a.llmMu.Lock()
	oldClient := a.llmClient
	oldRunningRef := a.runningModelRef
	a.llmClient = newClient
	a.modelName = modelName
	if providerModelRef != "" {
		a.providerModelRef = providerModelRef
		a.runningModelRef = providerModelRef
	} else if a.providerModelRef != "" {
		a.runningModelRef = a.providerModelRef
	}
	newRunningRef := a.runningModelRef
	a.installedSysPrompt = ""
	if newClient != nil {
		a.ctxMgr.SetTokenBudgets(contextLimit, newClient.InputLimitForModelRef(providerModelRef), a.effectiveCompactionReservedInput())
	} else {
		a.ctxMgr.SetMaxTokens(contextLimit)
	}
	a.llmMu.Unlock()
	if oldClient != nil && oldClient != newClient {
		oldClient.Close()
	}
	if oldRunningRef != "" && oldRunningRef != newRunningRef {
		// A real model switch invalidates prompt-cache reuse, so cache-friendly
		// dynamic MCP mounts cannot carry over to the new model's request surface.
		a.forceFullMCPToolInjection()
	} else {
		a.resetMCPToolMountState()
		a.clearFrozenToolSurface()
		a.markRuntimeSurfaceDirty()
	}
	a.noteContextSurfaceIdentityChanged()

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

	// Cache-aware routing: rank interchangeable fallback providers by observed
	// prompt-cache quality and warmth so retries prefer providers whose cache
	// still holds our prefix.
	newClient.SetCandidateScorer(a.cacheAwareCandidateScore)

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
	prepared, err := a.prepareMainModel(providerModel)
	if err != nil {
		return err
	}
	a.installPreparedMainModel(prepared)

	if showToast {
		client := prepared.client
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

type preparedMainModel struct {
	client       *llm.Client
	modelName    string
	contextLimit int
	selectedRef  string
	runningRef   string
}

// prepareMainModel prepares a client for the active role: the fallback pool is
// resolved from the role currently installed, which is what model switches and
// startup restores run under.
func (a *MainAgent) prepareMainModel(providerModel string) (*preparedMainModel, error) {
	return a.prepareMainModelForRole(providerModel, a.currentActiveConfig())
}

// prepareMainModelForRole prepares a client that will run under cfg while cfg
// may not be the active role yet (role switches, plan-execution staging). The
// fallback pool chain comes from cfg itself, never from the role still active
// when the prepare runs.
func (a *MainAgent) prepareMainModelForRole(providerModel string, cfg *config.AgentConfig) (*preparedMainModel, error) {
	poolRefs, poolVariant := a.roleModelPoolSource(cfg)
	return a.prepareMainModelWithPool(providerModel, poolRefs, poolVariant)
}

func (a *MainAgent) prepareMainModelWithPool(providerModel string, poolRefs []string, poolVariant string) (*preparedMainModel, error) {
	if a.modelSwitchFactory == nil {
		return nil, fmt.Errorf("model switch not configured")
	}

	client, modelName, ctxLimit, err := a.modelSwitchFactory(providerModel, poolRefs, poolVariant)
	if err != nil {
		return nil, fmt.Errorf("create LLM client for %q: %w", providerModel, err)
	}
	a.applyServiceTierToClient(client)

	selectedRef := strings.TrimSpace(client.NextRequestModelRef())
	if selectedRef == "" {
		selectedRef = strings.TrimSpace(client.PrimaryModelRef())
	}
	if selectedRef == "" {
		selectedRef = providerModel
	}
	runningRef := strings.TrimSpace(client.RunningModelRef())
	if runningRef == "" {
		runningRef = strings.TrimSpace(client.PrimaryModelRef())
	}
	if runningRef == "" {
		runningRef = selectedRef
	}
	return &preparedMainModel{
		client:       client,
		modelName:    modelName,
		contextLimit: ctxLimit,
		selectedRef:  selectedRef,
		runningRef:   runningRef,
	}, nil
}

func (a *MainAgent) installPreparedMainModel(prepared *preparedMainModel) {
	if prepared == nil || prepared.client == nil {
		return
	}
	a.modelUpdateMu.Lock()
	defer a.modelUpdateMu.Unlock()
	if sid := strings.TrimSpace(filepath.Base(a.sessionDir)); sid != "" && sid != "." {
		prepared.client.SetSessionID(sid)
	}
	a.swapLLMClientWithRefLocked(prepared.client, prepared.modelName, prepared.contextLimit, prepared.selectedRef)
	a.mainModelPolicyDirty.Store(false)
	if a.modelPoolPolicy != nil {
		cfg := a.currentActiveConfig()
		if cfg != nil {
			effectivePool := a.modelPoolPolicy.EffectivePool(cfg.Name, cfg)
			if effectivePool != "" {
				a.modelPoolPolicy.SetLastPicked(cfg.Name, effectivePool, prepared.selectedRef)
			}
		}
	}
	a.emitToTUI(RunningModelChangedEvent{
		AgentID:          identity.MainAgentID,
		ProviderModelRef: prepared.selectedRef,
		RunningModelRef:  prepared.runningRef,
	})
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

// ModelsStatusText formats the current model pool and its overrides for the
// TUI's NOTICE card. The output contract is Markdown (list items, blank-line
// separated sections); headless consumers receive it verbatim in the JSON
// `status` field, so it is presentational text, not a machine-readable record.
func (a *MainAgent) ModelsStatusText() string {
	if a.modelPoolPolicy == nil {
		return "Model pool policy not configured"
	}
	sb := new(strings.Builder)
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
	fmt.Fprintf(sb, "Model pool: %s%s\n", currentModelPool, currentModelPoolStatus)
	overrides := a.modelPoolPolicy.Overrides()
	if len(overrides) > 0 {
		// The TUI renders this report as Markdown inside a NOTICE card, so each
		// pool is a list item: indented plain text would reflow into one
		// paragraph.
		sb.WriteString("\nFixed agent pools:\n")
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
			fmt.Fprintf(sb, "- %s: %s%s\n", name, pool, status)
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
			fmt.Fprintf(sb, "- %s: (no pool)\n", name)
		} else {
			fmt.Fprintf(sb, "- %s: %s (%d model(s))\n", name, pool, len(models))
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
	a.subs.mu.RLock()
	var targets []*SubAgent
	for _, sub := range a.subs.subAgents {
		if sub != nil && sub.agentDefName == agentName {
			targets = append(targets, sub)
		}
	}
	a.subs.mu.RUnlock()
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
		if err := a.setAgentModelPool(sub.agentDefName, pool); err != nil {
			a.emitToTUI(ErrorEvent{Err: err})
		}
		return
	}
	if err := a.setCurrentModelPool(pool); err != nil {
		a.emitToTUI(ErrorEvent{Err: err})
	}
}

func (a *MainAgent) handleModelsSetAgent(rest string) {
	parts := strings.Fields(rest)
	if len(parts) != 2 {
		a.emitToTUI(ErrorEvent{Err: fmt.Errorf("/models --agent: usage: /models --agent <name> <pool>")})
		return
	}
	if err := a.setAgentModelPool(parts[0], parts[1]); err != nil {
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
	// Pool switches are configuration changes, not manual model picks: skip the
	// generic "Switched model" toast.
	if err := a.switchModel(ref, false); err != nil {
		return err
	}
	return nil
}

func (a *MainAgent) setCurrentModelPool(pool string) error {
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
	// A switch requested while the running request or the tool calls produced by
	// its response are still in progress stays pending: the running model and
	// its per-model tools only change once the next request is prepared.
	deferred := a.mainRequestWindowActive()
	if deferred {
		a.capturePendingModelPoolRollback()
	}
	a.modelPoolPolicy.SetCurrentModelPool(pool)

	cfg := a.currentActiveConfig()
	if cfg != nil && deferred {
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

	// The not-in-flight branch above switched the running model immediately
	// (ctxmgr budgets already reflect the new model's limits). Apply the new
	// model's threshold and reminder window right away too, so the armed
	// usage-driven request is re-evaluated against the new line before the next
	// message is composed. The compaction itself starts at the next
	// pre-request gate, in parallel with that round; nothing is deferred behind
	// it. A busy or deferred switch is handled at the next pre-request gate
	// instead (beginMainLLMAfterPreparation).
	if a.turn == nil && !a.mainLLMRequestInFlight.Load() {
		a.applyModelCompactionConfig()
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
	a.subs.mu.RLock()
	var targets []*SubAgent
	for _, sub := range a.subs.subAgents {
		if sub != nil && sub.agentDefName == agentName {
			targets = append(targets, sub)
		}
	}
	a.subs.mu.RUnlock()
	for _, sub := range targets {
		client, _ := sub.llmSnapshot()
		current := ""
		if client != nil {
			current = client.PrimaryModelRef()
		}
		if modelRefInPool(current, refs, cfg) {
			a.modelPoolPolicy.SetLastPicked(agentName, pool, current)
			continue
		}
		ref := a.modelPoolPolicy.ResolveInitialModelRef(agentName, cfg)
		if ref == "" {
			continue
		}
		client, modelName, ctxLimit, err := a.modelSwitchFactory(ref, refs, strings.TrimSpace(cfg.Variant))
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

func (a *MainAgent) setAgentModelPool(agentName, pool string) error {
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
	mainWindowActive := a.mainRequestWindowActive()
	if mainWindowActive || agentInFlight {
		a.capturePendingModelPoolRollback()
	}
	a.modelPoolPolicy.SetAgentOverride(agentName, pool)

	if cfg.Name == a.CurrentRole() && mainWindowActive {
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

	return nil
}
