package agent

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/llm"
)

func (a *MainAgent) resolveConfiguredModelPool(poolName string) ([]string, error) {
	poolName = strings.TrimSpace(poolName)
	if poolName == "" {
		return nil, fmt.Errorf("model pool name is required")
	}
	var sawConfig bool
	for _, cfg := range []*config.Config{a.projectConfig, a.globalConfig} {
		if cfg == nil {
			continue
		}
		sawConfig = true
		refs := trimModelPoolRefs(cfg.ModelPools[poolName])
		if len(refs) > 0 {
			return refs, nil
		}
	}
	if !sawConfig {
		return nil, fmt.Errorf("config not available for model pool %q", poolName)
	}
	return nil, fmt.Errorf("model pool %q is not defined or empty", poolName)
}

func trimModelPoolRefs(refs []string) []string {
	out := make([]string, 0, len(refs))
	for _, ref := range refs {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			continue
		}
		out = append(out, ref)
	}
	return out
}

func (a *MainAgent) newAuxModelPoolClient(refs []string, timeout time.Duration, outputMax int) (*llm.Client, error) {
	if len(refs) == 0 {
		return nil, fmt.Errorf("empty model pool")
	}
	if a.modelSwitchFactory == nil {
		return nil, fmt.Errorf("model switch factory is not configured")
	}
	var constructionErrs []error
	pool := make([]llm.FallbackModel, 0, len(refs))
	for _, selectedRef := range refs {
		selectedRef = strings.TrimSpace(selectedRef)
		if selectedRef == "" {
			continue
		}
		client, _, _, err := a.modelSwitchFactory(selectedRef)
		if err != nil {
			constructionErrs = append(constructionErrs, fmt.Errorf("%s: %w", selectedRef, err))
			continue
		}
		if client == nil {
			constructionErrs = append(constructionErrs, fmt.Errorf("%s: produced nil client", selectedRef))
			continue
		}
		if timeout > 0 {
			if rebuilt, err := rebuildClientWithTotalTimeout(client, timeout); err == nil && rebuilt != nil {
				client = rebuilt
			}
		}
		entry := client.PrimaryModelEntry()
		if entry.ProviderConfig == nil || entry.ProviderImpl == nil || strings.TrimSpace(entry.ModelID) == "" {
			constructionErrs = append(constructionErrs, fmt.Errorf("%s: produced unusable client", selectedRef))
			continue
		}
		pool = append(pool, entry)
	}
	if len(pool) == 0 {
		if len(constructionErrs) > 0 {
			return nil, errors.Join(constructionErrs...)
		}
		return nil, fmt.Errorf("model pool has no usable refs")
	}
	return newAuxClientFromPool(pool, 0, outputMax, a.ServiceTier()), nil
}

func newAuxClientFromPool(pool []llm.FallbackModel, selectedIdx int, outputMax int, serviceTier config.ServiceTier) *llm.Client {
	if len(pool) == 0 {
		return nil
	}
	if selectedIdx < 0 || selectedIdx >= len(pool) {
		selectedIdx = 0
	}
	selected := pool[selectedIdx]
	client := llm.NewClient(selected.ProviderConfig, selected.ProviderImpl, selected.ModelID, selected.MaxTokens, "")
	client.SetModelPool(pool, selectedIdx)
	client.SetServiceTier(serviceTier)
	if outputMax > 0 {
		client.SetOutputTokenMax(outputMax)
	}
	if selected.Variant != "" {
		client.SetVariant(selected.Variant)
	}
	return client
}

// rebuildClientWithTotalTimeout rebuilds the client with a custom http.Client.Timeout
// while keeping connection and header timeouts at their default values (15s dial, 25s header).
func rebuildClientWithTotalTimeout(client *llm.Client, totalTimeout time.Duration) (*llm.Client, error) {
	if client == nil {
		return nil, nil
	}
	if totalTimeout <= 0 {
		return client, nil
	}
	providerCfg := client.ProviderConfig()
	if providerCfg == nil {
		return nil, nil
	}
	proxyURL := providerCfg.EffectiveProxyURL()
	httpClient, err := llm.NewHTTPClientWithProxy(proxyURL, totalTimeout)
	if err != nil {
		return nil, err
	}
	var impl llm.Provider
	switch providerCfg.Type() {
	case config.ProviderTypeChatCompletions:
		impl, err = llm.NewOpenAIProviderWithClient(providerCfg, httpClient, proxyURL)
	case config.ProviderTypeResponses:
		impl, err = llm.NewResponsesProviderWithClient(providerCfg, httpClient, proxyURL)
	case config.ProviderTypeMessages:
		impl, err = llm.NewAnthropicProviderWithClient(providerCfg, httpClient, proxyURL)
	case config.ProviderTypeGenerateContent:
		impl, err = llm.NewGeminiProviderWithClient(providerCfg, httpClient, proxyURL)
	default:
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	rebuilt := llm.NewClient(providerCfg, impl, client.ModelID(), client.MaxTokens(), "")
	rebuilt.SetOutputTokenMax(client.OutputTokenMax())
	rebuilt.SetVariant(client.Variant())
	return rebuilt, nil
}
