package main

import (
	"context"
	"fmt"
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

func fallbackInputLimitConfig(providerCfg *llm.ProviderConfig, modelID string, contextLimit, outputTokenMax int) (inputLimit int, derive bool) {
	if providerCfg != nil {
		if mc, ok := providerCfg.GetModel(modelID); ok {
			if mc.Limit.Input > 0 {
				return mc.Limit.Input, false
			}
			return mc.Limit.EffectiveInputBudget(outputTokenMax, llm.DefaultOutputTokenMax), true
		}
	}
	return contextLimit, false
}

func buildModelPool(
	parentCtx context.Context,
	modelRefs []string,
	defaultVariant string,
	selectedRef string,
	allProviders map[string]config.ProviderConfig,
	auth config.AuthConfig,
	credDecls config.CredentialDeclarations,
	globalProxy string,
	outputTokenMax int,
	getProvider getProviderFunc,
	getProviderImpl getProviderImplFunc,
	logLabel string,
) ([]llm.FallbackModel, int, error) {
	if parentCtx == nil {
		parentCtx = context.Background()
	}
	if len(modelRefs) == 0 {
		return nil, -1, nil
	}

	if err := config.ValidateConfiguredModelRefs(allProviders, modelRefs, defaultVariant); err != nil {
		return nil, -1, fmt.Errorf("%s model pool: %w", logLabel, err)
	}

	selectedCanonicalRef := ""
	if strings.TrimSpace(selectedRef) != "" {
		selectedBaseRef, selectedVariant := parseRoleModelRef(selectedRef, defaultVariant)
		if selectedProvider, selectedModelID, _, _, _, err := config.ResolveConfiguredModelRef(allProviders, selectedBaseRef); err == nil {
			selectedCanonicalRef = config.CanonicalModelRef(selectedProvider, selectedModelID, selectedVariant)
		}
	}
	pool := make([]llm.FallbackModel, 0, len(modelRefs))
	selectedIdx := -1
	for _, ref := range modelRefs {
		fbBaseRef, fbVariant := parseRoleModelRef(ref, defaultVariant)
		fbRef := strings.TrimSpace(fbBaseRef)
		if fbVariant != "" {
			fbRef += "@" + fbVariant
		}
		fbProvCfg, fbImpl, fbModelID, fbMaxTokens, fbCtxLimit, fbErr := resolveModelRef(
			parentCtx,
			fbRef, allProviders, auth, credDecls, globalProxy, getProvider, getProviderImpl,
		)
		if fbErr != nil {
			return nil, -1, fmt.Errorf("resolve %s model %q: %w", logLabel, ref, fbErr)
		}
		if selectedCanonicalRef != "" && config.CanonicalModelRef(fbProvCfg.Name(), fbModelID, fbVariant) == selectedCanonicalRef && selectedIdx < 0 {
			selectedIdx = len(pool)
		}
		inputLimit, deriveInputLimit := fallbackInputLimitConfig(fbProvCfg, fbModelID, fbCtxLimit, outputTokenMax)
		pool = append(pool, llm.FallbackModel{
			ProviderConfig:   fbProvCfg,
			ProviderImpl:     fbImpl,
			ModelID:          fbModelID,
			MaxTokens:        fbMaxTokens,
			ContextLimit:     fbCtxLimit,
			InputLimit:       inputLimit,
			DeriveInputLimit: deriveInputLimit,
			Variant:          fbVariant,
		})
	}
	if len(pool) == 0 {
		return nil, -1, nil
	}
	if selectedIdx < 0 {
		selectedIdx = 0
	}
	return pool, selectedIdx, nil
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
			c.SetStreamRetryRounds(cfg.StreamRetryRounds)
			c.SetVariant(variant)
			return c
		}

		parentCtx := ac.Ctx
		if parentCtx == nil {
			parentCtx = context.Background()
		}

		pool, _, err := buildModelPool(parentCtx, agentModels, variant, agentModels[0], cfg.Providers,
			auth, ac.CredDecls, cfg.Proxy, cfg.MaxOutputTokens, ac.GetOrCreateProvider, ac.GetOrCreateProviderImpl, "sub-agent")
		if err != nil {
			log.Warnf("cannot create agent model pool: %v", err)
			return nil
		}
		first := pool[0]
		client := llm.NewClient(first.ProviderConfig, first.ProviderImpl, first.ModelID, first.MaxTokens, systemPrompt)
		client.SetOutputTokenMax(cfg.MaxOutputTokens)
		client.SetStreamRetryRounds(cfg.StreamRetryRounds)
		client.SetVariant(first.Variant)
		client.SetModelPool(pool, 0)

		return client
	}
}

// buildMainClientFactory returns the model-switch factory for MainAgent used
// when a client is rebuilt for a model ref at runtime (model command, role
// switch, SubAgent pool rebuild, deferred policy rebuild). The role pool whose
// fallback chain the new client should carry arrives with each call as
// poolRefs/poolVariant, resolved by the agent from the role the client will
// run under — never from whatever role happens to be active at call time.
func buildMainClientFactory(
	ac *AppContext,
	cfg *config.Config,
	auth config.AuthConfig,
) func(providerModel string, poolRefs []string, poolVariant string) (*llm.Client, string, int, error) {
	return func(providerModel string, poolRefs []string, poolVariant string) (*llm.Client, string, int, error) {
		parentCtx := ac.Ctx
		if parentCtx == nil {
			parentCtx = context.Background()
		}
		if err := config.ValidateConfiguredModelRefs(cfg.Providers, poolRefs, poolVariant); err != nil {
			return nil, "", 0, err
		}
		if err := config.ValidateConfiguredModelRefs(cfg.Providers, []string{providerModel}, ""); err != nil {
			return nil, "", 0, err
		}
		_, selectedVariant := config.ParseModelRef(providerModel)
		pProvCfg, pImpl, pModelID, pMaxTokens, pCtxLimit, pErr := resolveModelRef(
			parentCtx,
			providerModel, cfg.Providers, auth, ac.CredDecls, cfg.Proxy, ac.GetOrCreateProvider, ac.GetOrCreateProviderImpl,
		)
		if pErr != nil {
			return nil, "", 0, pErr
		}

		client := llm.NewClient(pProvCfg, pImpl, pModelID, pMaxTokens, "")
		client.SetOutputTokenMax(cfg.MaxOutputTokens)
		client.SetStreamRetryRounds(cfg.StreamRetryRounds)
		client.SetVariant(selectedVariant)

		pool, selectedIdx, poolErr := buildModelPool(
			parentCtx,
			poolRefs,
			poolVariant,
			providerModel,
			cfg.Providers,
			auth,
			ac.CredDecls,
			cfg.Proxy,
			cfg.MaxOutputTokens,
			ac.GetOrCreateProvider,
			ac.GetOrCreateProviderImpl,
			"main-agent",
		)
		if poolErr != nil {
			return nil, "", 0, poolErr
		}
		if len(pool) > 0 && selectedIdx >= 0 && selectedIdx < len(pool) && config.CanonicalModelRef(pool[selectedIdx].ProviderConfig.Name(), pool[selectedIdx].ModelID, pool[selectedIdx].Variant) == config.CanonicalModelRef(pProvCfg.Name(), pModelID, selectedVariant) {
			client.SetModelPool(pool, selectedIdx)
		}

		return client, pModelID, pCtxLimit, nil
	}
}
