package agent

import (
	"fmt"
	"github.com/keakon/golog/log"
	"maps"
	"path/filepath"
	"sort"
	"strings"

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

// SetAvailableModelsFn sets the callback that enumerates models available for
// runtime switching. Must be called before Run.
func (a *MainAgent) SetAvailableModelsFn(fn func() []ModelOption) {
	a.availableModelsFn = fn
}

// AvailableModels returns the list of models the user can switch to.
//
// When a callback is installed via SetAvailableModelsFn, it is used.
// Otherwise, it falls back to enumerating the merged global+project config's
// provider/model entries.
func (a *MainAgent) AvailableModels() []ModelOption {
	if a.availableModelsFn != nil {
		return a.availableModelsFn()
	}

	// Fallback: enumerate configured providers/models from config.
	providers := make(map[string]config.ProviderConfig)
	if a.globalConfig != nil {
		maps.Copy(providers, a.globalConfig.Providers)
	}
	if a.projectConfig != nil {
		maps.Copy(providers, a.projectConfig.Providers) // project overrides global
	}
	if len(providers) == 0 {
		return nil
	}

	providerNames := make([]string, 0, len(providers))
	for name := range providers {
		providerNames = append(providerNames, name)
	}
	sort.Strings(providerNames)

	out := make([]ModelOption, 0)
	for _, providerName := range providerNames {
		prov := providers[providerName]
		if len(prov.Models) == 0 {
			continue
		}
		modelIDs := make([]string, 0, len(prov.Models))
		for id := range prov.Models {
			modelIDs = append(modelIDs, id)
		}
		sort.Strings(modelIDs)
		for _, modelID := range modelIDs {
			mc := prov.Models[modelID]
			out = append(out, ModelOption{
				ProviderModel: providerName + "/" + modelID,
				ProviderName:  providerName,
				ModelID:       modelID,
				ContextLimit:  mc.Limit.Context,
				OutputLimit:   mc.Limit.Output,
			})
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
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
	a.ctxMgr.SetMaxTokens(contextLimit)

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

// SwitchModel switches the MainAgent to a different model at runtime. The
// providerModel string is in "provider/model" or "provider/model@variant"
// format. Thread-safe.
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
	if sid := strings.TrimSpace(filepath.Base(a.sessionDir)); sid != "" && sid != "." {
		client.SetSessionID(sid)
	}

	a.swapLLMClientWithRef(client, modelName, ctxLimit, providerModel)
	a.mainModelPolicyDirty.Store(false)
	effectiveRunningRef := client.RunningModelRef()
	if effectiveRunningRef == "" {
		effectiveRunningRef = client.PrimaryModelRef()
	}
	if effectiveRunningRef == "" {
		effectiveRunningRef = providerModel
	}
	a.emitToTUI(RunningModelChangedEvent{
		AgentID:          a.instanceID,
		ProviderModelRef: providerModel,
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

// handleModelCommand processes the /model slash command.
//   - "/model" (no args): emits ModelSelectEvent so the TUI opens the selector.
//   - "/model provider/model": switches to the specified model directly.
func (a *MainAgent) handleModelCommand(content string) {
	arg := strings.TrimSpace(strings.TrimPrefix(content, "/model"))
	if arg == "" {
		a.emitToTUI(ModelSelectEvent{})
		a.setIdleAndDrainPending()
		return
	}
	if err := a.SwitchModel(arg); err != nil {
		a.emitToTUI(ErrorEvent{Err: fmt.Errorf("/model: %w", err)})
		a.setIdleAndDrainPending()
		return
	}
	a.setIdleAndDrainPending()
}
