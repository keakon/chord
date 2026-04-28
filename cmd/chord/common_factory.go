package main

import (
	"log/slog"
	"sort"
	"strings"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/llm"
)

func parseRoleModelRef(ref, defaultVariant string) (baseRef, variant string) {
	baseRef, variant = config.ParseModelRef(ref)
	if variant == "" {
		variant = strings.TrimSpace(defaultVariant)
	}
	return baseRef, variant
}

// buildSubAgentLLMFactory returns the LLM factory used by MainAgent when
// spawning SubAgents. Captures AppContext for provider/impl caching and
// config/auth for per-ref resolution.
func buildSubAgentLLMFactory(
	ac *AppContext,
	providerCfg *llm.ProviderConfig,
	llmProvider llm.Provider,
	modelID string,
	modelCfg config.ModelConfig,
	cfg *config.Config,
	auth config.AuthConfig,
) func(string, []string, string) *llm.Client {
	return func(systemPrompt string, agentModels []string, variant string) *llm.Client {
		if len(agentModels) == 0 {
			c := llm.NewClient(
				providerCfg,
				llmProvider,
				modelID,
				modelCfg.Limit.Output,
				systemPrompt,
			)
			c.SetOutputTokenMax(cfg.MaxOutputTokens)
			c.SetVariant(variant)
			return c
		}

		// Parse per-model variant from the first model-pool ref (e.g. "provider/model@high").
		// Inline @variant takes precedence over the global AgentConfig.Variant.
		firstRef, firstVariant := config.ParseModelRef(agentModels[0])
		if firstVariant == "" {
			firstVariant = variant
		}

		pProvCfg, pImpl, pModelID, pMaxTokens, _, pErr := resolveModelRef(
			firstRef, cfg.Providers, auth, cfg.Proxy, ac.GetOrCreateProvider, ac.GetOrCreateProviderImpl,
		)
		if pErr != nil {
			slog.Warn("failed to resolve agent first model-pool entry, falling back to default",
				"model_ref", agentModels[0], "error", pErr)
			c := llm.NewClient(providerCfg, llmProvider, modelID,
				modelCfg.Limit.Output, systemPrompt)
			c.SetOutputTokenMax(cfg.MaxOutputTokens)
			c.SetVariant(firstVariant)
			return c
		}

		client := llm.NewClient(pProvCfg, pImpl, pModelID, pMaxTokens, systemPrompt)
		client.SetOutputTokenMax(cfg.MaxOutputTokens)
		client.SetVariant(firstVariant)

		if len(agentModels) > 1 {
			var fallbacks []llm.FallbackModel
			for _, ref := range agentModels[1:] {
				// Parse per-model variant for each fallback ref.
				fbBaseRef, fbVariant := config.ParseModelRef(ref)
				if fbVariant == "" {
					fbVariant = variant
				}
				fbProvCfg, fbImpl, fbModelID, fbMaxTokens, fbCtxLimit, fbErr := resolveModelRef(
					fbBaseRef, cfg.Providers, auth, cfg.Proxy, ac.GetOrCreateProvider, ac.GetOrCreateProviderImpl,
				)
				if fbErr != nil {
					slog.Warn("failed to resolve agent fallback model, skipping",
						"model_ref", ref, "error", fbErr)
					continue
				}
				fallbacks = append(fallbacks, llm.FallbackModel{
					ProviderConfig: fbProvCfg,
					ProviderImpl:   fbImpl,
					ModelID:        fbModelID,
					MaxTokens:      fbMaxTokens,
					ContextLimit:   fbCtxLimit,
					Variant:        fbVariant,
				})
			}
			if len(fallbacks) > 0 {
				client.SetFallbackModels(fallbacks)
			}
		}

		return client
	}
}

// buildMainClientFactory returns the model-switch factory for MainAgent used
// when the user switches the current cursor-head model at runtime. Resolves a role-model pool
// when available, otherwise falls back across all configured models.
func buildMainClientFactory(
	ac *AppContext,
	cfg *config.Config,
	auth config.AuthConfig,
) func(providerModel string) (*llm.Client, string, int, error) {
	return func(providerModel string) (*llm.Client, string, int, error) {
		baseRef, selectedVariant := config.ParseModelRef(providerModel)
		pProvCfg, pImpl, pModelID, pMaxTokens, pCtxLimit, pErr := resolveModelRef(
			baseRef, cfg.Providers, auth, cfg.Proxy, ac.GetOrCreateProvider, ac.GetOrCreateProviderImpl,
		)
		if pErr != nil {
			return nil, "", 0, pErr
		}

		client := llm.NewClient(pProvCfg, pImpl, pModelID, pMaxTokens, "")
		client.SetOutputTokenMax(cfg.MaxOutputTokens)
		client.SetVariant(selectedVariant)

		// MainAgent uses a model pool: all role models form an ordered list, and
		// retry wraps around from the selected position. When no role models are
		// defined, fall back across all configured models.
		roleModels := ac.MainAgent.CurrentRoleModelRefs()
		roleDefaultVariant := ""
		if roleCfg := ac.MainAgent.CurrentRoleConfig(); roleCfg != nil {
			roleDefaultVariant = strings.TrimSpace(roleCfg.Variant)
		}
		selectedBaseRef := config.NormalizeModelRef(providerModel)
		if len(roleModels) > 0 {
			// Build the pool from the role models list and select only entries that
			// belong to that role. External provider/model overrides are intentionally
			// not mixed into role model pools.
			pool := make([]llm.FallbackModel, 0, len(roleModels))
			selectedIdx := -1
			for _, ref := range roleModels {
				fbBaseRef, fbVariant := parseRoleModelRef(ref, roleDefaultVariant)
				fbProvCfg, fbImpl, fbModelID, fbMaxTokens, fbCtxLimit, fbErr := resolveModelRef(
					fbBaseRef, cfg.Providers, auth, cfg.Proxy, ac.GetOrCreateProvider, ac.GetOrCreateProviderImpl,
				)
				if fbErr != nil {
					slog.Warn("failed to resolve main-agent model, skipping",
						"model_ref", ref, "error", fbErr)
					continue
				}
				if config.NormalizeModelRef(ref) == selectedBaseRef && selectedIdx < 0 {
					selectedIdx = len(pool)
				}
				pool = append(pool, llm.FallbackModel{
					ProviderConfig: fbProvCfg,
					ProviderImpl:   fbImpl,
					ModelID:        fbModelID,
					MaxTokens:      fbMaxTokens,
					ContextLimit:   fbCtxLimit,
					Variant:        fbVariant,
				})
			}
			if len(pool) > 0 {
				if selectedIdx < 0 {
					selectedIdx = 0
				}
				client.SetModelPool(pool, selectedIdx)
			}
		} else {
			// No role models list: enumerate all configured models as fallbacks.
			provNames := make([]string, 0, len(cfg.Providers))
			for name := range cfg.Providers {
				provNames = append(provNames, name)
			}
			sort.Strings(provNames)
			var fallbackRefs []string
			for _, pName := range provNames {
				prov := cfg.Providers[pName]
				if len(auth[pName]) == 0 {
					continue
				}
				mNames := make([]string, 0, len(prov.Models))
				for mName := range prov.Models {
					mNames = append(mNames, mName)
				}
				sort.Strings(mNames)
				for _, mName := range mNames {
					ref := pName + "/" + mName
					if ref == providerModel {
						continue
					}
					fallbackRefs = append(fallbackRefs, ref)
				}
			}
			if len(fallbackRefs) > 0 {
				fallbacks := make([]llm.FallbackModel, 0, len(fallbackRefs))
				for _, ref := range fallbackRefs {
					fbBaseRef, fbVariant := config.ParseModelRef(ref)
					fbProvCfg, fbImpl, fbModelID, fbMaxTokens, fbCtxLimit, fbErr := resolveModelRef(
						fbBaseRef, cfg.Providers, auth, cfg.Proxy, ac.GetOrCreateProvider, ac.GetOrCreateProviderImpl,
					)
					if fbErr != nil {
						slog.Warn("failed to resolve main-agent fallback model, skipping",
							"model_ref", ref, "error", fbErr)
						continue
					}
					fallbacks = append(fallbacks, llm.FallbackModel{
						ProviderConfig: fbProvCfg,
						ProviderImpl:   fbImpl,
						ModelID:        fbModelID,
						MaxTokens:      fbMaxTokens,
						ContextLimit:   fbCtxLimit,
						Variant:        fbVariant,
					})
				}
				client.SetFallbackModels(fallbacks)
			}
		}

		return client, pModelID, pCtxLimit, nil
	}
}

// buildAvailableModelsFn returns the ModelOption source used by MainAgent to
// populate the model picker. Uses the active role's ordered models list when
// present, otherwise enumerates all configured providers alphabetically.
func buildAvailableModelsFn(
	ac *AppContext,
	cfg *config.Config,
	auth config.AuthConfig,
) func() []agent.ModelOption {
	return func() []agent.ModelOption {
		var options []agent.ModelOption

		// If the active role has an ordered models list, use it as the authoritative order.
		roleModels := ac.MainAgent.CurrentRoleModelRefs()
		roleDefaultVariant := ""
		if roleCfg := ac.MainAgent.CurrentRoleConfig(); roleCfg != nil {
			roleDefaultVariant = strings.TrimSpace(roleCfg.Variant)
		}
		if len(roleModels) > 0 {
			for _, ref := range roleModels {
				baseRef, variant := parseRoleModelRef(ref, roleDefaultVariant)
				parts := strings.SplitN(baseRef, "/", 2)
				if len(parts) != 2 {
					continue
				}
				pName, mName := parts[0], parts[1]
				prov, ok := cfg.Providers[pName]
				if !ok {
					continue
				}
				if keys := auth[pName]; len(keys) == 0 {
					continue
				}
				mc, ok := prov.Models[mName]
				if !ok {
					continue
				}
				providerModel := baseRef
				if variant != "" {
					providerModel = baseRef + "@" + variant
				}
				options = append(options, agent.ModelOption{
					ProviderModel: providerModel,
					ProviderName:  pName,
					ModelID:       mName,
					ContextLimit:  mc.Limit.Context,
					OutputLimit:   mc.Limit.Output,
				})
			}
			return options
		}

		// No builder agent config: enumerate all providers alphabetically.
		provNames := make([]string, 0, len(cfg.Providers))
		for name := range cfg.Providers {
			provNames = append(provNames, name)
		}
		sort.Strings(provNames)

		for _, pName := range provNames {
			prov := cfg.Providers[pName]
			if keys := auth[pName]; len(keys) == 0 {
				continue
			}
			modelNames := make([]string, 0, len(prov.Models))
			for mName := range prov.Models {
				modelNames = append(modelNames, mName)
			}
			sort.Strings(modelNames)

			for _, mName := range modelNames {
				mc := prov.Models[mName]
				options = append(options, agent.ModelOption{
					ProviderModel: pName + "/" + mName,
					ProviderName:  pName,
					ModelID:       mName,
					ContextLimit:  mc.Limit.Context,
					OutputLimit:   mc.Limit.Output,
				})
			}
		}
		return options
	}
}
