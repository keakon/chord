package main

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/ratelimit"
)

// providerCache caches ProviderConfig and Provider implementations by provider
// name so key cooldown state, Responses WebSocket sticky-disable state, and any
// transport-level runtime session state are shared across sessions and agents.
//
// This is intentionally stronger than caching ProviderConfig alone: the Codex
// WebSocket transport binds incremental state, sticky WS disablement, and the
// live WSS connection itself to the provider implementation instance. Reusing
// the impl keeps model-policy rebuilds, fallback clients, and subagents on the
// same per-provider runtime channel semantics.
type providerCache struct {
	mu         sync.Mutex
	m          map[string]*llm.ProviderConfig
	impls      map[string]llm.Provider
	auth       config.AuthConfig
	authPath   string
	authMu     sync.Mutex
	cfg        *config.Config
	dumpWriter *llm.DumpWriter
}

func normalizeProviderConfig(provName string, cfg config.ProviderConfig, _ []config.ProviderCredential) (config.ProviderConfig, error) {
	normalized, _, err := config.NormalizeOpenAICodexProvider(cfg, false)
	if err != nil {
		return cfg, fmt.Errorf("normalize provider %q: %w", provName, err)
	}

	// Auto-detect and set type if not configured
	if normalized.Type == "" {
		// Preset codex takes highest priority
		if normalized.Preset == "codex" {
			normalized.Type = config.ProviderTypeResponses
		} else {
			apiURL := normalized.APIURL
			path := strings.TrimSuffix(apiURL, "/")
			switch {
			case strings.HasSuffix(path, "/responses"):
				normalized.Type = config.ProviderTypeResponses
			case strings.HasSuffix(path, "/chat/completions"):
				normalized.Type = config.ProviderTypeChatCompletions
			case strings.HasSuffix(path, "/messages"):
				normalized.Type = config.ProviderTypeMessages
			default:
				return cfg, fmt.Errorf("could not auto-detect type for provider %q, please explicitly set 'type' field (allowed: %s, %s, %s)",
					provName, config.ProviderTypeChatCompletions, config.ProviderTypeMessages, config.ProviderTypeResponses)
			}
		}
	}

	// Validate type is one of the allowed values
	validType := false
	switch normalized.Type {
	case config.ProviderTypeChatCompletions, config.ProviderTypeMessages, config.ProviderTypeResponses:
		validType = true
	}
	if !validType {
		return cfg, fmt.Errorf("invalid provider type %q for %q (allowed values: %s, %s, %s)",
			normalized.Type, provName,
			config.ProviderTypeChatCompletions, config.ProviderTypeMessages, config.ProviderTypeResponses)
	}

	return normalized, nil
}

func (c *providerCache) getOrCreate(provName string, cfg config.ProviderConfig, apiKeys []string) (*llm.ProviderConfig, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = make(map[string]*llm.ProviderConfig)
	}
	if p := c.m[provName]; p != nil {
		return p, nil
	}

	creds := c.auth[provName]
	normalizedCfg, err := normalizeProviderConfig(provName, cfg, creds)
	if err != nil {
		return nil, err
	}

	p := llm.NewProviderConfig(provName, normalizedCfg, apiKeys)
	if tokenURL, clientID, ok, err := resolveProviderOAuthSettings(normalizedCfg, creds); err != nil {
		return nil, fmt.Errorf("resolve OAuth settings for provider %q: %w", provName, err)
	} else if ok {
		var globalProxy string
		if c.cfg != nil {
			globalProxy = c.cfg.Proxy
		}
		effectiveProxy := llm.ResolveEffectiveProxy(normalizedCfg.Proxy, globalProxy)
		oauthMap, backfills := oauthCredentialMap(creds)
		p.SetOAuthRefresher(tokenURL, clientID, c.authPath, &c.auth, &c.authMu, oauthMap, effectiveProxy)
		if len(backfills) > 0 {
			if saveErr := persistOAuthMetadataBackfills(c.authPath, &c.auth, &c.authMu, provName, backfills); saveErr != nil {
				log.Warnf("failed to persist backfilled OAuth email/account_id provider=%v error=%v", provName, saveErr)
			}
		}
		p.StartCodexRateLimitPolling(func(key, accountID string) ([]*ratelimit.KeyRateLimitSnapshot, error) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			return llm.FetchCodexUsageSnapshot(ctx, p, key, accountID)
		})
	}

	c.m[provName] = p
	return p, nil
}

func (c *providerCache) getOrCreateImpl(provName string, cfg config.ProviderConfig, providerCfg *llm.ProviderConfig, modelID string) (llm.Provider, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.impls == nil {
		c.impls = make(map[string]llm.Provider)
	}
	if impl := c.impls[provName]; impl != nil {
		return impl, nil
	}

	normalizedCfg, err := normalizeProviderConfig(provName, cfg, c.auth[provName])
	if err != nil {
		return nil, err
	}
	var globalProxy string
	if c.cfg != nil {
		globalProxy = c.cfg.Proxy
	}
	effectiveProxy := llm.ResolveEffectiveProxy(normalizedCfg.Proxy, globalProxy)

	var impl llm.Provider
	providerType := normalizedCfg.Type

	switch providerType {
	case config.ProviderTypeChatCompletions:
		p, pErr := llm.NewOpenAIProvider(providerCfg, effectiveProxy)
		if pErr != nil {
			return nil, fmt.Errorf("create %s provider for %q: %w", config.ProviderTypeChatCompletions, provName, pErr)
		}
		impl = p
	case config.ProviderTypeResponses:
		p, pErr := llm.NewResponsesProvider(providerCfg, effectiveProxy)
		if pErr != nil {
			return nil, fmt.Errorf("create %s provider for %q: %w", config.ProviderTypeResponses, provName, pErr)
		}
		impl = p
	case config.ProviderTypeMessages:
		p, pErr := llm.NewAnthropicProvider(providerCfg, effectiveProxy)
		if pErr != nil {
			return nil, fmt.Errorf("create %s provider for %q: %w", config.ProviderTypeMessages, provName, pErr)
		}
		impl = p
	default:
		return nil, fmt.Errorf("unsupported provider type %q for %q (allowed: %s, %s, %s)",
			providerType, provName,
			config.ProviderTypeChatCompletions, config.ProviderTypeMessages, config.ProviderTypeResponses)
	}
	if c.dumpWriter != nil {
		llm.SetProviderDumpWriter(impl, c.dumpWriter)
	}
	c.impls[provName] = impl
	return impl, nil
}

func (c *providerCache) close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, p := range c.m {
		if p != nil {
			p.StopCodexRateLimitPolling()
		}
	}
}
