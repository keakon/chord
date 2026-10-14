package agent

import (
	"strings"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
)

func (a *MainAgent) defaultRoleModelRef(cfg *config.AgentConfig) string {
	if cfg == nil || len(cfg.Models) == 0 {
		return ""
	}
	if a.modelPoolPolicy != nil {
		return a.modelPoolPolicy.ResolveInitialModelRef(cfg.Name, cfg)
	}
	poolNames := cfg.PoolNames()
	if len(poolNames) == 0 {
		return ""
	}
	refs := cfg.PoolModels(poolNames[0])
	if len(refs) == 0 {
		return ""
	}
	ref := strings.TrimSpace(refs[0])
	if ref == "" {
		return ""
	}
	if _, variant := config.ParseModelRef(ref); variant != "" {
		return ref
	}
	if variant := strings.TrimSpace(cfg.Variant); variant != "" {
		return ref + "@" + variant
	}
	return ref
}

// ProxyInUseForRef reports whether the given provider/model ref uses a proxy.
// ref is "providerName/modelID"; if empty, the main agent's ProviderModelRef is used.
// Used by the TUI status bar to show a proxy indicator for the current (or focused) agent.
func (a *MainAgent) ProxyInUseForRef(ref string) bool {
	if a.globalConfig == nil {
		return false
	}
	if ref == "" {
		a.llmMu.RLock()
		ref = a.providerModelRef
		a.llmMu.RUnlock()
	}
	if ref == "" {
		return false
	}
	ref = config.NormalizeModelRef(ref)
	parts := strings.SplitN(ref, "/", 2)
	if len(parts) == 0 || parts[0] == "" {
		return false
	}
	providerName := parts[0]
	prov, ok := a.globalConfig.Providers[providerName]
	if !ok {
		return false
	}
	effective := llm.ResolveEffectiveProxy(prov.Proxy, a.globalConfig.Proxy)
	return effective != "" && effective != "direct"
}

// GetTokenUsage returns cumulative token usage for the TUI-focused agent, in
// the same frame as GetSidebarUsageStats and the usage panel: the billing
// split, where InputTokens is the whole prompt side including the cached
// prefix.
//
// It reads the usage ledger rather than the focused agent's context manager
// even for a live agent. A context manager accumulates the provider's raw
// numbers, whose input field includes the cache-read prefix on some providers
// and excludes it on others, so the same session showed one prompt-side total
// while an agent was live and a different one after it parked (the parked path
// has always read the ledger). The ledger normalizes that split once, at
// record time, for every agent.
func (a *MainAgent) GetTokenUsage() message.TokenUsage {
	return tokenUsageFromSessionStats(a.GetSidebarUsageStats())
}
