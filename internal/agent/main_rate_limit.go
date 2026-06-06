package agent

import (
	"strings"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/ratelimit"
)

func providerNameFromModelRef(ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ""
	}
	if prov, _, ok := strings.Cut(ref, "/"); ok {
		return prov
	}
	return ref
}

func (a *MainAgent) currentRateLimitProviderName() string {
	client, ref := a.tuiFocusedLLMAndRef()
	ref = strings.TrimSpace(ref)
	if client != nil && strings.Contains(ref, "/") {
		return providerNameFromModelRef(ref)
	}
	a.llmMu.RLock()
	runningRef := strings.TrimSpace(a.runningModelRef)
	selectedRef := strings.TrimSpace(a.providerModelRef)
	a.llmMu.RUnlock()
	if strings.Contains(runningRef, "/") {
		return providerNameFromModelRef(runningRef)
	}
	return providerNameFromModelRef(selectedRef)
}

// mainLLMAndRef returns the MainAgent LLM client and provider/model ref,
// ignoring any TUI-focused SubAgent.
func (a *MainAgent) mainLLMAndRef() (client *llm.Client, ref string) {
	a.llmMu.RLock()
	client = a.llmClient
	ref = strings.TrimSpace(a.runningModelRef)
	if ref == "" {
		ref = strings.TrimSpace(a.providerModelRef)
	}
	a.llmMu.RUnlock()
	return client, ref
}

func (a *MainAgent) mainRateLimitProviderName() string {
	_, ref := a.mainLLMAndRef()
	return providerNameFromModelRef(ref)
}

func (a *MainAgent) mainRateLimitSnapshot() *ratelimit.KeyRateLimitSnapshot {
	providerName := a.mainRateLimitProviderName()
	if providerName == "" || !a.providerUsesCodexRateLimit(providerName) {
		return nil
	}
	client, ref := a.mainLLMAndRef()
	if client != nil {
		if snap := client.CurrentRateLimitSnapshotForRef(ref); snap != nil {
			return snap
		}
	}
	a.rateLimitMu.Lock()
	snap := a.rateLimitSnaps[providerName]
	a.rateLimitMu.Unlock()
	return snap
}

// tuiFocusedLLMAndRef returns the LLM client and provider/model ref for the
// agent currently shown in the TUI (focused SubAgent, else MainAgent). Used by
// sidebar MODEL/Keys and Codex rate-limit snapshot selection.
func (a *MainAgent) tuiFocusedLLMAndRef() (client *llm.Client, ref string) {
	if sub := a.validFocusedSubAgent(); sub != nil {
		if sub.llmClient == nil {
			return nil, ""
		}
		c := sub.llmClient
		ref = strings.TrimSpace(c.RunningModelRef())
		if ref == "" {
			ref = strings.TrimSpace(c.PrimaryModelRef())
		}
		return c, ref
	}
	a.llmMu.RLock()
	client = a.llmClient
	if !a.mainLLMRequestInFlight.Load() && client != nil {
		ref = strings.TrimSpace(client.NextRequestModelRef())
	}
	if ref == "" {
		ref = strings.TrimSpace(a.runningModelRef)
	}
	if ref == "" {
		ref = strings.TrimSpace(a.providerModelRef)
	}
	a.llmMu.RUnlock()
	return client, ref
}

func (a *MainAgent) providerConfigByName(providerName string) (config.ProviderConfig, bool) {
	providerName = strings.TrimSpace(providerName)
	if providerName == "" {
		return config.ProviderConfig{}, false
	}
	if a.projectConfig != nil {
		if prov, ok := a.projectConfig.Providers[providerName]; ok {
			return prov, true
		}
	}
	if a.globalConfig != nil {
		if prov, ok := a.globalConfig.Providers[providerName]; ok {
			return prov, true
		}
	}
	return config.ProviderConfig{}, false
}

func (a *MainAgent) providerUsesCodexRateLimit(providerName string) bool {
	prov, ok := a.providerConfigByName(providerName)
	if !ok {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(prov.Preset), config.ProviderPresetCodex)
}

func (a *MainAgent) clearCurrentRateLimitSnapshot() {
	providerName := a.currentRateLimitProviderName()
	if providerName == "" || !a.providerUsesCodexRateLimit(providerName) {
		return
	}

	a.rateLimitMu.Lock()
	prev := a.rateLimitSnaps[providerName]
	if prev != nil && prev.Source == ratelimit.SnapshotSourceInlineKey {
		delete(a.rateLimitSnaps, providerName)
	} else {
		prev = nil
	}
	a.rateLimitMu.Unlock()
	if prev != nil {
		a.emitToTUI(RateLimitUpdatedEvent{Snapshot: nil})
	}
}

func (a *MainAgent) updateRateLimitSnapshot(snap *ratelimit.KeyRateLimitSnapshot) {
	if snap == nil {
		return
	}
	providerName := strings.TrimSpace(snap.Provider)
	if providerName == "" {
		providerName = a.currentRateLimitProviderName()
		if providerName == "" {
			return
		}
		snap.Provider = providerName
	}
	if !a.providerUsesCodexRateLimit(providerName) {
		return
	}

	a.rateLimitMu.Lock()
	if a.rateLimitSnaps == nil {
		a.rateLimitSnaps = make(map[string]*ratelimit.KeyRateLimitSnapshot)
	}
	prev := a.rateLimitSnaps[providerName]
	a.rateLimitSnaps[providerName] = snap
	a.rateLimitMu.Unlock()
	if prev != snap {
		a.emitToTUI(RateLimitUpdatedEvent{Snapshot: snap})
	}
}

// CurrentRateLimitSnapshot returns the latest rate-limit snapshot for the
// active provider's currently selected key/account when that provider uses
// preset: codex. Otherwise it returns nil.
//
// The sidebar must not reuse provider-scoped snapshots across key switches: once
// a different key is selected, it should show that key's cached inline snapshot,
// that key/account's polled /wham/usage snapshot, or nothing until fresh data is
// available.
func (a *MainAgent) CurrentRateLimitSnapshot() *ratelimit.KeyRateLimitSnapshot {
	providerName := a.currentRateLimitProviderName()
	if providerName == "" || !a.providerUsesCodexRateLimit(providerName) {
		return nil
	}

	client, ref := a.tuiFocusedLLMAndRef()
	if client == nil {
		return nil
	}
	return client.CurrentRateLimitSnapshotForRef(ref)
}

// WakeCodexRateLimitPolling triggers an on-demand /wham/usage poll for the
// currently focused agent's provider, when configured with preset: codex.
// It is a best-effort hint used by the TUI when a reset timestamp is reached.
func (a *MainAgent) WakeCodexRateLimitPolling() {
	client, _ := a.tuiFocusedLLMAndRef()
	if client == nil {
		return
	}
	if prov := client.ProviderConfig(); prov != nil {
		prov.WakeCodexRateLimitPolling()
	}
}
