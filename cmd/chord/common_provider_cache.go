package main

import (
	"context"
	"fmt"
	"sync"
	"time"

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
	mu              sync.Mutex
	m               map[string]*llm.ProviderConfig
	impls           map[string]llm.Provider
	ctx             context.Context
	auth            config.AuthConfig
	authPath        string
	authMu          sync.Mutex
	cfg             *config.Config
	dumpWriter      *llm.DumpWriter
	traceWriter     *llm.TraceWriter
	newProviderImpl func(*llm.ProviderConfig, string) (llm.Provider, error)
	fetchCodexUsage func(context.Context, *llm.ProviderConfig, string, string) ([]*ratelimit.KeyRateLimitSnapshot, error)
}

func normalizeProviderConfig(provName string, cfg config.ProviderConfig, _ []config.ProviderCredential) (config.ProviderConfig, error) {
	normalized, _, err := config.NormalizeProviderPreset(cfg)
	if err != nil {
		return cfg, fmt.Errorf("normalize provider %q: %w", provName, err)
	}
	if err := config.ValidateProviderKeySelection(provName, normalized); err != nil {
		return cfg, err
	}

	// Auto-detect type from api_url when preset normalization did not set it.
	if normalized.Type == "" {
		switch {
		case config.APIURLPathHasSuffix(normalized.APIURL, "/responses"):
			normalized.Type = config.ProviderTypeResponses
		case config.APIURLPathHasSuffix(normalized.APIURL, "/chat/completions"):
			normalized.Type = config.ProviderTypeChatCompletions
		case config.APIURLPathHasSuffix(normalized.APIURL, "/messages"):
			normalized.Type = config.ProviderTypeMessages
		case config.APIURLPathHasSuffix(normalized.APIURL, "/models"):
			normalized.Type = config.ProviderTypeGenerateContent
		default:
			return cfg, fmt.Errorf("could not auto-detect type for provider %q, please explicitly set 'type' field (allowed: %s, %s, %s, %s)",
				provName, config.ProviderTypeChatCompletions, config.ProviderTypeMessages, config.ProviderTypeResponses, config.ProviderTypeGenerateContent)
		}
	}

	// Validate type is one of the allowed values
	validType := false
	switch normalized.Type {
	case config.ProviderTypeChatCompletions, config.ProviderTypeMessages, config.ProviderTypeResponses, config.ProviderTypeGenerateContent:
		validType = true
	}
	if !validType {
		return cfg, fmt.Errorf("invalid provider type %q for %q (allowed values: %s, %s, %s, %s)",
			normalized.Type, provName,
			config.ProviderTypeChatCompletions, config.ProviderTypeMessages, config.ProviderTypeResponses, config.ProviderTypeGenerateContent)
	}

	if normalized.Type == config.ProviderTypeGenerateContent {
		if !config.APIURLPathHasSuffix(normalized.APIURL, "/models") {
			return cfg, fmt.Errorf("provider %q type %q requires api_url path ending in /models", provName, config.ProviderTypeGenerateContent)
		}
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
		authStatePath, statePathErr := config.AuthStatePath()
		if statePathErr != nil {
			return nil, fmt.Errorf("resolve auth state path: %w", statePathErr)
		}
		effectiveProxy := llm.ResolveEffectiveProxy(normalizedCfg.Proxy, globalProxy)
		oauthMap := oauthCredentialMapFast(creds)
		p.SetOAuthRefresher(tokenURL, clientID, c.authPath, authStatePath, &c.auth, &c.authMu, oauthMap, effectiveProxy)
		backfillCtx := c.ctx
		if backfillCtx == nil {
			backfillCtx = context.Background()
		}
		startOAuthMetadataBackfill(backfillCtx, p, c.authPath, &c.auth, &c.authMu, provName, creds)
		p.StartCodexRateLimitPolling(func(key, accountID string) ([]*ratelimit.KeyRateLimitSnapshot, error) {
			pollParentCtx := c.ctx
			if pollParentCtx == nil {
				pollParentCtx = context.Background()
			}
			pollCtx, cancel := context.WithTimeout(pollParentCtx, 30*time.Second)
			defer cancel()
			fetchFn := c.fetchCodexUsage
			if fetchFn == nil {
				fetchFn = llm.FetchCodexUsageSnapshot
			}
			return fetchFn(pollCtx, p, key, accountID)
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

	newProviderImpl := c.newProviderImpl
	if newProviderImpl == nil {
		newProviderImpl = llm.NewProviderImpl
	}
	impl, err := newProviderImpl(providerCfg, effectiveProxy)
	if err != nil {
		return nil, fmt.Errorf("create %s provider for %q: %w", normalizedCfg.Type, provName, err)
	}
	if c.dumpWriter != nil {
		llm.SetProviderDumpWriter(impl, c.dumpWriter)
	}
	if c.traceWriter != nil {
		llm.SetProviderTraceWriter(impl, c.traceWriter)
	}
	c.impls[provName] = impl
	return impl, nil
}

func (c *providerCache) setTraceWriter(w *llm.TraceWriter) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.traceWriter = w
	for _, impl := range c.impls {
		llm.SetProviderTraceWriter(impl, w)
	}
}

func (c *providerCache) setDumpWriter(w *llm.DumpWriter) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.dumpWriter = w
	for _, impl := range c.impls {
		llm.SetProviderDumpWriter(impl, w)
	}
}

func (c *providerCache) close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, p := range c.m {
		if p != nil {
			p.Close()
		}
	}
}
