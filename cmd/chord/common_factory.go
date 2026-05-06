package main

import (
	"strings"

	"github.com/keakon/golog/log"

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
			log.Warnf("failed to resolve agent first model-pool entry, falling back to default model_ref=%v error=%v", agentModels[0], pErr)
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
					log.Warnf("failed to resolve agent fallback model, skipping model_ref=%v error=%v", ref, fbErr)
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
// when the user switches the current cursor-head model at runtime. Resolves a
// role-model pool to build the fallback chain.
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

		roleModels := ac.MainAgent.CurrentRoleModelRefs()
		roleDefaultVariant := ""
		if roleCfg := ac.MainAgent.CurrentRoleConfig(); roleCfg != nil {
			roleDefaultVariant = strings.TrimSpace(roleCfg.Variant)
		}
		selectedBaseRef := config.NormalizeModelRef(providerModel)

		pool := make([]llm.FallbackModel, 0, len(roleModels))
		selectedIdx := -1
		for _, ref := range roleModels {
			fbBaseRef, fbVariant := parseRoleModelRef(ref, roleDefaultVariant)
			fbProvCfg, fbImpl, fbModelID, fbMaxTokens, fbCtxLimit, fbErr := resolveModelRef(
				fbBaseRef, cfg.Providers, auth, cfg.Proxy, ac.GetOrCreateProvider, ac.GetOrCreateProviderImpl,
			)
			if fbErr != nil {
				log.Warnf("failed to resolve main-agent model, skipping model_ref=%v error=%v", ref, fbErr)
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

		return client, pModelID, pCtxLimit, nil
	}
}
