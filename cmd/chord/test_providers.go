package main

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
)

func testProviders(providerFilter string) error {
	// Load config
	cfg, err := config.LoadConfig()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	// Load auth
	authPath, err := config.AuthPath()
	if err != nil {
		return fmt.Errorf("get auth path: %w", err)
	}
	auth, err := config.LoadAuthConfig(authPath)
	if err != nil {
		return fmt.Errorf("load auth: %w", err)
	}

	fmt.Printf("Testing %d providers...\n\n", len(cfg.Providers))

	for provName, provCfg := range cfg.Providers {
		// Filter by provider flag if specified
		if providerFilter != "" && provName != providerFilter {
			continue
		}

		fmt.Printf("=== Provider: %s ===\n", provName)

		// Normalize provider config to resolve auto-detected type
		normalizedCfg, err := normalizeProviderConfig(provName, provCfg, nil)
		if err != nil {
			fmt.Printf("  ❌ Failed to normalize config: %v\n\n", err)
			continue
		}

		// Get credentials
		creds := auth[provName]
		apiKeys := config.ExtractAPIKeys(creds)
		if len(apiKeys) == 0 {
			fmt.Printf("  ⚠️  No API keys found for provider %q\n\n", provName)
			continue
		}

		// Use first available key
		testKey := apiKeys[0]
		fmt.Printf("  Using key: %s...%s\n", testKey[:min(8, len(testKey))], testKey[max(0, len(testKey)-4):])

		// Create provider config
		providerConfig := llm.NewProviderConfig(provName, normalizedCfg, []string{testKey})

		// Pick first model
		var modelID string
		for m := range normalizedCfg.Models {
			modelID = m
			break
		}
		if modelID == "" {
			fmt.Printf("  ⚠️  No models configured for provider %q\n\n", provName)
			continue
		}
		fmt.Printf("  Testing model: %s\n", modelID)
		fmt.Printf("  Detected type: %s\n", normalizedCfg.Type)

		// Resolve proxy (provider-level proxy overrides global)
		proxyURL := cfg.Proxy // default to global proxy
		if normalizedCfg.Proxy != nil {
			proxyURL = *normalizedCfg.Proxy // provider proxy overrides global
		}
		if proxyURL != "" {
			fmt.Printf("  Using proxy: %s\n", proxyURL)
		}
		if tokenURL, clientID, ok, err := resolveProviderOAuthSettings(normalizedCfg, creds); err != nil {
			fmt.Printf("  ❌ Failed to resolve OAuth settings: %v\n\n", err)
			continue
		} else if ok {
			authCopy := auth
			var authMu sync.Mutex
			oauthMap, _ := oauthCredentialMap(creds)
			providerConfig.SetOAuthRefresher(tokenURL, clientID, authPath, &authCopy, &authMu, oauthMap, proxyURL)
		}

		// Create provider instance
		var provider llm.Provider
		switch normalizedCfg.Type {
		case config.ProviderTypeMessages:
			p, err := llm.NewAnthropicProvider(providerConfig, proxyURL)
			if err != nil {
				fmt.Printf("  ❌ Failed to create %s provider: %v\n\n", config.ProviderTypeMessages, err)
				continue
			}
			provider = p
		case config.ProviderTypeChatCompletions:
			p, err := llm.NewOpenAIProvider(providerConfig, proxyURL)
			if err != nil {
				fmt.Printf("  ❌ Failed to create %s provider: %v\n\n", config.ProviderTypeChatCompletions, err)
				continue
			}
			provider = p
		case config.ProviderTypeResponses:
			p, err := llm.NewResponsesProvider(providerConfig, proxyURL)
			if err != nil {
				fmt.Printf("  ❌ Failed to create %s provider: %v\n\n", config.ProviderTypeResponses, err)
				continue
			}
			provider = p
		default:
			fmt.Printf("  ⚠️  Unknown provider type: %q (allowed values: %s, %s, %s)\n\n",
				normalizedCfg.Type,
				config.ProviderTypeChatCompletions,
				config.ProviderTypeMessages,
				config.ProviderTypeResponses)
			continue
		}

		if err := runTestProviderRequest(provider, testKey, modelID); err != nil {
			fmt.Printf("  ❌ Request failed: %v\n\n", err)
			continue
		}
	}

	return nil
}

func runTestProviderRequest(provider llm.Provider, testKey, modelID string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	messages := []message.Message{{Role: "user", Content: "Say hello in one word."}}

	fmt.Printf("  Sending test request...\n")
	start := time.Now()

	var responseText strings.Builder
	textChunkCount := 0

	resp, err := provider.CompleteStream(
		ctx,
		testKey,
		modelID,
		"You are a helpful assistant.", // Non-empty system prompt for API compatibility
		messages,
		nil, // tools
		128, // maxTokens
		llm.RequestTuning{},
		func(delta message.StreamDelta) {
			if delta.Type == "text" {
				responseText.WriteString(delta.Text)
				textChunkCount++
			}
		},
	)
	if err != nil {
		return err
	}

	elapsed := time.Since(start)
	if responseText.Len() == 0 {
		fmt.Printf("  ⚠️  No response content received\n\n")
		return nil
	}
	if responsesProvider, ok := provider.(*llm.ResponsesProvider); ok {
		if transport := responsesProvider.LastTransportUsed(); transport != "" {
			fmt.Printf("  Transport: %s\n", transport)
		}
	}
	fmt.Printf("  ✅ Success! (%.2fs, %d text chunks)\n", elapsed.Seconds(), textChunkCount)
	fmt.Printf("  Response: %q\n", strings.TrimSpace(responseText.String()))
	if resp.Usage != nil {
		fmt.Printf("  Usage: input=%d, output=%d\n", resp.Usage.InputTokens, resp.Usage.OutputTokens)
	}
	fmt.Println()
	return nil
}
