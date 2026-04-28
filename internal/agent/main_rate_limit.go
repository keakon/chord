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
	ref = strings.TrimSpace(a.runningModelRef)
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
// active provider when that provider uses preset: codex. Otherwise it returns nil.
// Display precedence is:
//  1. current provider-scoped inline snapshot cache (cleared on key switch)
//  2. client-selected key inline snapshot
//  3. provider/account-scoped polled usage snapshot
func (a *MainAgent) CurrentRateLimitSnapshot() *ratelimit.KeyRateLimitSnapshot {
	providerName := a.currentRateLimitProviderName()
	if providerName == "" || !a.providerUsesCodexRateLimit(providerName) {
		return nil
	}
	a.rateLimitMu.RLock()
	snap := a.rateLimitSnaps[providerName]
	a.rateLimitMu.RUnlock()
	if snap != nil {
		return snap
	}
	client, ref := a.tuiFocusedLLMAndRef()
	if client == nil {
		return nil
	}
	return client.CurrentRateLimitSnapshotForRef(ref)
}
