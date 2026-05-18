package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/ctxmgr"
	"github.com/keakon/chord/internal/hook"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/mcp"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/ratelimit"
	"github.com/keakon/chord/internal/tools"
)

// stubProviderImpl is a minimal Provider implementation for testing.
type stubProviderImpl struct{}

type stubScriptedCall struct {
	resp *message.Response
	err  error
}

type stubScriptedProvider struct {
	calls []stubScriptedCall
}

func (p *stubScriptedProvider) CompleteStream(
	_ context.Context,
	_ string, _ string, _ string,
	_ []message.Message, _ []message.ToolDefinition,
	_ int, _ llm.RequestTuning,
	_ llm.StreamCallback,
) (*message.Response, error) {
	if len(p.calls) == 0 {
		return &message.Response{Content: "ok", StopReason: "stop"}, nil
	}
	call := p.calls[0]
	p.calls = p.calls[1:]
	if call.err != nil {
		return nil, call.err
	}
	if call.resp != nil {
		return call.resp, nil
	}
	return &message.Response{Content: "ok", StopReason: "stop"}, nil
}

func resolveProviderConfigForTest(provName string, cfg config.ProviderConfig, auth config.AuthConfig) *llm.ProviderConfig {
	return llm.NewProviderConfig(provName, cfg, config.ExtractAPIKeys(auth[provName]))
}

func (p *stubProviderImpl) CompleteStream(
	_ context.Context,
	_ string, _ string, _ string,
	_ []message.Message, _ []message.ToolDefinition,
	_ int, _ llm.RequestTuning,
	_ llm.StreamCallback,
) (*message.Response, error) {
	return &message.Response{Content: "ok", StopReason: "stop"}, nil
}

func TestProviderCacheCodexPollingUsesCacheContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	pollCtxCh := make(chan context.Context, 1)
	cache := &providerCache{
		ctx:      ctx,
		m:        make(map[string]*llm.ProviderConfig),
		impls:    make(map[string]llm.Provider),
		authPath: filepath.Join(t.TempDir(), "auth.yaml"),
		cfg:      &config.Config{},
		auth: config.AuthConfig{"codex": {
			{OAuth: &config.OAuthCredential{Access: "access-token", Refresh: "refresh-token", Expires: time.Now().Add(time.Hour).UnixMilli(), AccountID: "account-1"}},
		}},
		fetchCodexUsage: func(ctx context.Context, _ *llm.ProviderConfig, _, _ string) ([]*ratelimit.KeyRateLimitSnapshot, error) {
			pollCtxCh <- ctx
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}

	prov, err := cache.getOrCreate("codex", config.ProviderConfig{
		Preset: config.ProviderPresetCodex,
		Models: map[string]config.ModelConfig{"gpt-5": {}},
	}, []string{"access-token"})
	if err != nil {
		t.Fatalf("getOrCreate: %v", err)
	}
	if _, _, err := prov.SelectKeyWithContext(context.Background()); err != nil {
		t.Fatalf("SelectKeyWithContext: %v", err)
	}

	pollCtx := <-pollCtxCh
	if err := pollCtx.Err(); err != nil {
		t.Fatalf("poll context already cancelled before cache context cancellation: %v", err)
	}
	cancel()
	select {
	case <-pollCtx.Done():
		if !errors.Is(pollCtx.Err(), context.Canceled) {
			t.Fatalf("poll context error = %v, want context.Canceled", pollCtx.Err())
		}
	case <-time.After(time.Second):
		t.Fatal("poll context was not cancelled after cache context cancellation")
	}
}

// TestBuildMainClientFactoryWithModelPool verifies that buildMainClientFactory
// creates clients with properly configured model pools.
func TestBuildMainClientFactoryWithModelPool(t *testing.T) {
	t.Parallel()

	// Create test config with multiple models in builder agent
	cfg := &config.Config{
		MaxOutputTokens: 4096,
		Providers: map[string]config.ProviderConfig{
			"openai": {
				Type: config.ProviderTypeChatCompletions,
				Models: map[string]config.ModelConfig{
					"gpt-4o":  {Limit: config.ModelLimit{Context: 128000, Output: 4096}},
					"gpt-4.1": {Limit: config.ModelLimit{Context: 200000, Output: 4096}},
					"o3-pro":  {Limit: config.ModelLimit{Context: 200000, Output: 100000}},
				},
			},
		},
	}

	auth := config.AuthConfig{
		"openai": []config.ProviderCredential{{APIKey: "test-key"}},
	}

	// Simulate agent configs with multiple models
	agentConfigs := map[string]*config.AgentConfig{
		"builder": {
			Name:    "builder",
			Variant: "balanced",
			Models: map[string][]string{
				"standard": {"openai/gpt-4o", "openai/gpt-4.1", "openai/o3-pro"},
			},
		},
	}

	ac := newTestAppContextWithBuilder(t, cfg, auth, agentConfigs)
	factory := buildMainClientFactory(ac, cfg, auth)

	// Request client for first model - should include all models in pool
	client, modelID, ctxLimit, err := factory("openai/gpt-4o@balanced")
	if err != nil {
		t.Fatalf("factory failed: %v", err)
	}

	// Verify client is created with correct parameters
	if modelID != "gpt-4o" {
		t.Fatalf("modelID = %q, want gpt-4o", modelID)
	}
	if ctxLimit != 128000 {
		t.Fatalf("ctxLimit = %d, want 128000", ctxLimit)
	}

	// Verify model pool is configured - should start with gpt-4o, then others
	primary := client.PrimaryModelRef()
	if primary != "openai/gpt-4o" {
		t.Fatalf("PrimaryModelRef = %q, want openai/gpt-4o", primary)
	}

	// Verify the pool includes all models (variant is tracked internally, not in ref)
	status := client.LastCallStatus()
	if status.SelectedModelRef != "openai/gpt-4o" {
		t.Fatalf("SelectedModelRef = %q, want openai/gpt-4o", status.SelectedModelRef)
	}

	t.Logf("Model pool configured correctly: primary=%s", primary)
}

// TestBuildMainClientFactorySelectsCorrectPoolEntry verifies that when requesting
// a non-first model from the pool, the factory correctly identifies it as the
// selected index and includes all other models as fallbacks.
func TestBuildMainClientFactorySelectsCorrectPoolEntry(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		MaxOutputTokens: 4096,
		Providers: map[string]config.ProviderConfig{
			"openai": {
				Type: config.ProviderTypeChatCompletions,
				Models: map[string]config.ModelConfig{
					"gpt-4o":  {Limit: config.ModelLimit{Context: 128000, Output: 4096}},
					"gpt-4.1": {Limit: config.ModelLimit{Context: 200000, Output: 4096}},
					"o3-pro":  {Limit: config.ModelLimit{Context: 200000, Output: 100000}},
				},
			},
		},
	}

	auth := config.AuthConfig{
		"openai": []config.ProviderCredential{{APIKey: "test-key"}},
	}

	agentConfigs := map[string]*config.AgentConfig{
		"builder": {
			Name:    "builder",
			Variant: "balanced",
			Models: map[string][]string{
				"standard": {"openai/gpt-4o", "openai/gpt-4.1", "openai/o3-pro"},
			},
		},
	}

	ac := newTestAppContextWithBuilder(t, cfg, auth, agentConfigs)
	factory := buildMainClientFactory(ac, cfg, auth)

	// Request client for second model - pool should start from here
	client, modelID, _, err := factory("openai/gpt-4.1")
	if err != nil {
		t.Fatalf("factory failed: %v", err)
	}

	if modelID != "gpt-4.1" {
		t.Fatalf("modelID = %q, want gpt-4.1", modelID)
	}

	primary := client.PrimaryModelRef()
	if primary != "openai/gpt-4.1" {
		t.Fatalf("PrimaryModelRef = %q, want openai/gpt-4.1", primary)
	}

	t.Logf("Pool correctly starts from selected model: %s", primary)
}

func TestBuildModelPoolSelectedIndexTracksFilteredPool(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{
			"openai": {
				Type: config.ProviderTypeChatCompletions,
				Models: map[string]config.ModelConfig{
					"gpt-4o":  {Limit: config.ModelLimit{Context: 128000, Output: 4096}},
					"gpt-4.1": {Limit: config.ModelLimit{Context: 200000, Output: 4096}},
				},
			},
		},
	}
	pool, selectedIdx := buildModelPool(
		context.Background(),
		[]string{"openai/missing", "openai/gpt-4o", "openai/gpt-4.1"},
		"",
		"openai/gpt-4.1",
		cfg.Providers,
		nil,
		"",
		0,
		nil,
		nil,
		"test",
	)
	if len(pool) != 2 {
		t.Fatalf("pool len = %d, want 2", len(pool))
	}
	if selectedIdx != 1 {
		t.Fatalf("selectedIdx = %d, want 1", selectedIdx)
	}
	if got := pool[selectedIdx].ProviderConfig.Name() + "/" + pool[selectedIdx].ModelID; got != "openai/gpt-4.1" {
		t.Fatalf("selected pool entry = %q, want openai/gpt-4.1", got)
	}
	if got := pool[0].ProviderConfig.Name() + "/" + pool[0].ModelID; got != "openai/gpt-4o" {
		t.Fatalf("first pool entry = %q, want openai/gpt-4o", got)
	}
}

func TestBuildModelPoolFallsBackToFirstResolvedEntryWhenSelectionMissing(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{
			"openai": {
				Type: config.ProviderTypeChatCompletions,
				Models: map[string]config.ModelConfig{
					"gpt-4o":  {Limit: config.ModelLimit{Context: 128000, Output: 4096}},
					"gpt-4.1": {Limit: config.ModelLimit{Context: 200000, Output: 4096}},
				},
			},
		},
	}
	pool, selectedIdx := buildModelPool(
		context.Background(),
		[]string{"openai/gpt-4o", "openai/gpt-4.1"},
		"",
		"openai/missing",
		cfg.Providers,
		nil,
		"",
		0,
		nil,
		nil,
		"test",
	)
	if len(pool) != 2 {
		t.Fatalf("pool len = %d, want 2", len(pool))
	}
	if selectedIdx != 0 {
		t.Fatalf("selectedIdx = %d, want 0", selectedIdx)
	}
}

func TestBuildModelPoolSelectsMatchingVariantForDuplicateBaseModel(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{
			"openai": {
				Type: config.ProviderTypeChatCompletions,
				Models: map[string]config.ModelConfig{
					"o3-pro": {
						Limit: config.ModelLimit{Context: 200000, Output: 100000},
						Variants: map[string]config.ModelVariant{
							"balanced": {},
							"high":     {},
						},
					},
				},
			},
		},
	}

	pool, selectedIdx := buildModelPool(
		context.Background(),
		[]string{"openai/o3-pro@balanced", "openai/o3-pro@high"},
		"",
		"openai/o3-pro@high",
		cfg.Providers,
		nil,
		"",
		0,
		nil,
		nil,
		"test",
	)
	if len(pool) != 2 {
		t.Fatalf("pool len = %d, want 2", len(pool))
	}
	if selectedIdx != 1 {
		t.Fatalf("selectedIdx = %d, want 1", selectedIdx)
	}
	if got := pool[selectedIdx].Variant; got != "high" {
		t.Fatalf("selected variant = %q, want high", got)
	}
}

func TestBuildModelPoolPreservesConfiguredOrderAndVariants(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{
			"openai": {
				Type: config.ProviderTypeChatCompletions,
				Models: map[string]config.ModelConfig{
					"gpt-4o": {
						Limit:    config.ModelLimit{Context: 128000, Output: 4096},
						Variants: map[string]config.ModelVariant{"balanced": {}},
					},
					"gpt-4.1": {
						Limit:    config.ModelLimit{Context: 200000, Output: 4096},
						Variants: map[string]config.ModelVariant{"high": {}},
					},
					"o3-pro": {
						Limit:    config.ModelLimit{Context: 200000, Output: 100000},
						Variants: map[string]config.ModelVariant{"high": {}},
					},
				},
			},
		},
	}

	pool, selectedIdx := buildModelPool(
		context.Background(),
		[]string{"openai/gpt-4o@balanced", "openai/gpt-4.1", "openai/o3-pro@high"},
		"high",
		"openai/gpt-4.1",
		cfg.Providers,
		nil,
		"",
		0,
		nil,
		nil,
		"test",
	)
	if len(pool) != 3 {
		t.Fatalf("pool len = %d, want 3", len(pool))
	}
	if selectedIdx != 1 {
		t.Fatalf("selectedIdx = %d, want 1", selectedIdx)
	}

	gotRefs := []string{
		pool[0].ProviderConfig.Name() + "/" + pool[0].ModelID + "@" + pool[0].Variant,
		pool[1].ProviderConfig.Name() + "/" + pool[1].ModelID + "@" + pool[1].Variant,
		pool[2].ProviderConfig.Name() + "/" + pool[2].ModelID + "@" + pool[2].Variant,
	}
	wantRefs := []string{
		"openai/gpt-4o@balanced",
		"openai/gpt-4.1@high",
		"openai/o3-pro@high",
	}
	for i := range wantRefs {
		if gotRefs[i] != wantRefs[i] {
			t.Fatalf("pool[%d] = %q, want %q", i, gotRefs[i], wantRefs[i])
		}
	}
}

func TestBuildModelPoolMarksDerivedInputBudgetsDynamic(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{
			"openai": {
				Type: config.ProviderTypeChatCompletions,
				Models: map[string]config.ModelConfig{
					"gpt-5.5": {Limit: config.ModelLimit{Context: 400000, Output: 128000}},
					"gpt-5":   {Limit: config.ModelLimit{Context: 400000, Input: 272000, Output: 128000}},
				},
			},
		},
	}

	pool, selectedIdx := buildModelPool(
		context.Background(),
		[]string{"openai/gpt-5.5", "openai/gpt-5"},
		"",
		"openai/gpt-5.5",
		cfg.Providers,
		nil,
		"",
		0,
		nil,
		nil,
		"test",
	)
	if selectedIdx != 0 {
		t.Fatalf("selectedIdx = %d, want 0", selectedIdx)
	}
	if len(pool) != 2 {
		t.Fatalf("pool len = %d, want 2", len(pool))
	}
	if !pool[0].DeriveInputLimit {
		t.Fatal("expected split-less model pool entry to mark input budget as dynamic")
	}
	if got := pool[0].InputLimit; got != 368000 {
		t.Fatalf("derived pool[0] InputLimit = %d, want 368000 cached fallback", got)
	}
	if pool[1].DeriveInputLimit {
		t.Fatal("expected explicit limit.input model pool entry to keep fixed input budget")
	}
	if got := pool[1].InputLimit; got != 272000 {
		t.Fatalf("explicit pool[1] InputLimit = %d, want 272000", got)
	}
}

func TestSetModelPoolRotatesFallbackOrderFromSelectedEntry(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		MaxOutputTokens: 4096,
		Providers: map[string]config.ProviderConfig{
			"openai": {
				Type: config.ProviderTypeChatCompletions,
				Models: map[string]config.ModelConfig{
					"gpt-4o":  {Limit: config.ModelLimit{Context: 128000, Output: 4096}},
					"gpt-4.1": {Limit: config.ModelLimit{Context: 200000, Output: 4096}},
					"o3-pro":  {Limit: config.ModelLimit{Context: 200000, Output: 100000}},
				},
			},
		},
	}
	auth := config.AuthConfig{
		"openai": []config.ProviderCredential{{APIKey: "test-key"}},
	}

	providerCfg := resolveProviderConfigForTest("openai", cfg.Providers["openai"], auth)
	selectedImpl := &stubScriptedProvider{calls: []stubScriptedCall{{err: &llm.APIError{StatusCode: 500, Message: "selected failed"}}}}
	fallbackOneImpl := &stubScriptedProvider{calls: []stubScriptedCall{{err: &llm.APIError{StatusCode: 500, Message: "fallback one failed"}}}}
	fallbackTwoImpl := &stubScriptedProvider{calls: []stubScriptedCall{{resp: &message.Response{Content: "fallback success"}}, {resp: &message.Response{Content: "next call starts here"}}}}

	pool, selectedIdx := buildModelPool(
		context.Background(),
		[]string{"openai/gpt-4o", "openai/gpt-4.1", "openai/o3-pro"},
		"",
		"openai/gpt-4.1",
		cfg.Providers,
		auth,
		"",
		cfg.MaxOutputTokens,
		func(provName string, _ config.ProviderConfig, _ []string) (*llm.ProviderConfig, error) {
			switch provName {
			case "openai":
				return providerCfg, nil
			default:
				return nil, os.ErrNotExist
			}
		},
		func(_ string, _ config.ProviderConfig, _ *llm.ProviderConfig, modelID string) (llm.Provider, error) {
			switch modelID {
			case "gpt-4o":
				return fallbackTwoImpl, nil
			case "gpt-4.1":
				return selectedImpl, nil
			case "o3-pro":
				return fallbackOneImpl, nil
			default:
				return nil, os.ErrNotExist
			}
		},
		"test",
	)
	if len(pool) != 3 {
		t.Fatalf("pool len = %d, want 3", len(pool))
	}
	if selectedIdx != 1 {
		t.Fatalf("selectedIdx = %d, want 1", selectedIdx)
	}

	client := llm.NewClient(providerCfg, selectedImpl, "gpt-4.1", 4096, "")
	client.SetOutputTokenMax(cfg.MaxOutputTokens)
	client.SetModelPool(pool, selectedIdx)

	if got := client.PrimaryModelRef(); got != "openai/gpt-4.1" {
		t.Fatalf("PrimaryModelRef = %q, want openai/gpt-4.1", got)
	}

	resp, err := client.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err != nil {
		t.Fatalf("CompleteStream() error = %v", err)
	}
	if resp == nil || resp.Content != "fallback success" {
		t.Fatalf("response = %#v, want fallback success", resp)
	}
	st := client.LastCallStatus()
	if !st.FallbackTriggered {
		t.Fatal("expected fallback across rotated model pool")
	}
	if st.SelectedModelRef != "openai/gpt-4.1" {
		t.Fatalf("SelectedModelRef = %q, want openai/gpt-4.1", st.SelectedModelRef)
	}
	if st.RunningModelRef != "openai/gpt-4o" {
		t.Fatalf("RunningModelRef = %q, want openai/gpt-4o", st.RunningModelRef)
	}

	// After success on gpt-4o, the next request should start from that rotated
	// pool entry even though the client object still stores the original selected
	// head as its primary config surface.
	resp, err = client.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "again"}}, nil, nil)
	if err != nil {
		t.Fatalf("second CompleteStream() error = %v", err)
	}
	if resp == nil || resp.Content != "next call starts here" {
		t.Fatalf("second response = %#v, want next call starts here", resp)
	}
	st = client.LastCallStatus()
	if st.SelectedModelRef != "openai/gpt-4o" {
		t.Fatalf("second SelectedModelRef = %q, want openai/gpt-4o", st.SelectedModelRef)
	}
	if st.RunningModelRef != "openai/gpt-4o" {
		t.Fatalf("second RunningModelRef = %q, want openai/gpt-4o", st.RunningModelRef)
	}
}

// TestBuildMainClientFactorySingleModelPool verifies behavior when only one model
// is configured (should not set pool).
func TestBuildMainClientFactorySingleModelPool(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		MaxOutputTokens: 4096,
		Providers: map[string]config.ProviderConfig{
			"openai": {
				Type: config.ProviderTypeChatCompletions,
				Models: map[string]config.ModelConfig{
					"gpt-4o": {Limit: config.ModelLimit{Context: 128000, Output: 4096}},
				},
			},
		},
	}

	auth := config.AuthConfig{
		"openai": []config.ProviderCredential{{APIKey: "test-key"}},
	}

	agentConfigs := map[string]*config.AgentConfig{
		"builder": {
			Name:    "builder",
			Variant: "balanced",
			Models: map[string][]string{
				"standard": {"openai/gpt-4o"}, // single model
			},
		},
	}

	ac := newTestAppContextWithBuilder(t, cfg, auth, agentConfigs)
	factory := buildMainClientFactory(ac, cfg, auth)

	client, _, _, err := factory("openai/gpt-4o")
	if err != nil {
		t.Fatalf("factory failed: %v", err)
	}

	// Single model pool - pool should have just one entry
	primary := client.PrimaryModelRef()
	if primary != "openai/gpt-4o" {
		t.Fatalf("PrimaryModelRef = %q, want openai/gpt-4o", primary)
	}

	t.Logf("Single model pool handled correctly: %s", primary)
}

// TestBuildMainClientFactoryWrapAround verifies that the model pool wraps around
// correctly when the selected model is not the first in the pool.
func TestBuildMainClientFactoryWrapAround(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		MaxOutputTokens: 4096,
		Providers: map[string]config.ProviderConfig{
			"openai": {
				Type: config.ProviderTypeChatCompletions,
				Models: map[string]config.ModelConfig{
					"gpt-4o":  {Limit: config.ModelLimit{Context: 128000, Output: 4096}},
					"gpt-4.1": {Limit: config.ModelLimit{Context: 200000, Output: 4096}},
					"o3-pro":  {Limit: config.ModelLimit{Context: 200000, Output: 100000}},
				},
			},
		},
	}

	auth := config.AuthConfig{
		"openai": []config.ProviderCredential{{APIKey: "test-key"}},
	}

	agentConfigs := map[string]*config.AgentConfig{
		"builder": {
			Name:    "builder",
			Variant: "balanced",
			Models: map[string][]string{
				"standard": {"openai/gpt-4o", "openai/gpt-4.1", "openai/o3-pro"},
			},
		},
	}

	ac := newTestAppContextWithBuilder(t, cfg, auth, agentConfigs)
	factory := buildMainClientFactory(ac, cfg, auth)

	// Request client for third model - pool should wrap around
	client, modelID, _, err := factory("openai/o3-pro")
	if err != nil {
		t.Fatalf("factory failed: %v", err)
	}

	if modelID != "o3-pro" {
		t.Fatalf("modelID = %q, want o3-pro", modelID)
	}

	primary := client.PrimaryModelRef()
	if primary != "openai/o3-pro" {
		t.Fatalf("PrimaryModelRef = %q, want openai/o3-pro", primary)
	}

	t.Logf("Pool wrap-around handled correctly: %s", primary)
}

func TestInitialClientUsesBuilderModelPoolForFirstRequest(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		MaxOutputTokens: 4096,
		Providers: map[string]config.ProviderConfig{
			"openai": {
				Type: config.ProviderTypeChatCompletions,
				Models: map[string]config.ModelConfig{
					"gpt-4o":  {Limit: config.ModelLimit{Context: 128000, Output: 4096}},
					"gpt-4.1": {Limit: config.ModelLimit{Context: 200000, Output: 4096}},
				},
			},
		},
	}
	auth := config.AuthConfig{
		"openai": []config.ProviderCredential{{APIKey: "test-key"}},
	}

	providerCfg := resolveProviderConfigForTest("openai", cfg.Providers["openai"], auth)
	primaryImpl := &stubScriptedProvider{calls: []stubScriptedCall{{err: &llm.APIError{StatusCode: 500, Message: "primary failed"}}}}
	fallbackImpl := &stubScriptedProvider{calls: []stubScriptedCall{{resp: &message.Response{Content: "fallback success"}}}}

	pool, selectedIdx := buildModelPool(
		context.Background(),
		[]string{"openai/gpt-4o", "openai/gpt-4.1"},
		"",
		"openai/gpt-4o",
		cfg.Providers,
		auth,
		"",
		cfg.MaxOutputTokens,
		func(provName string, _ config.ProviderConfig, _ []string) (*llm.ProviderConfig, error) {
			switch provName {
			case "openai":
				return providerCfg, nil
			default:
				return nil, os.ErrNotExist
			}
		},
		func(_ string, _ config.ProviderConfig, _ *llm.ProviderConfig, modelID string) (llm.Provider, error) {
			switch modelID {
			case "gpt-4o":
				return primaryImpl, nil
			case "gpt-4.1":
				return fallbackImpl, nil
			default:
				return nil, os.ErrNotExist
			}
		},
		"builder startup",
	)
	if len(pool) != 2 {
		t.Fatalf("pool len = %d, want 2", len(pool))
	}
	if selectedIdx != 0 {
		t.Fatalf("selectedIdx = %d, want 0", selectedIdx)
	}

	client := llm.NewClient(providerCfg, primaryImpl, "gpt-4o", 4096, "")
	client.SetOutputTokenMax(cfg.MaxOutputTokens)
	client.SetModelPool(pool, selectedIdx)

	resp, err := client.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err != nil {
		t.Fatalf("CompleteStream() error = %v", err)
	}
	if resp == nil || resp.Content != "fallback success" {
		t.Fatalf("response = %#v, want fallback success", resp)
	}
	st := client.LastCallStatus()
	if !st.FallbackTriggered {
		t.Fatal("expected first request to trigger fallback across builder model pool")
	}
	if st.RunningModelRef != "openai/gpt-4.1" {
		t.Fatalf("RunningModelRef = %q, want openai/gpt-4.1", st.RunningModelRef)
	}
}

// newTestAppContextWithBuilder creates an AppContext with a MainAgent configured
// with builder agent configs for testing model pool factory.
func newTestAppContextWithBuilder(
	t *testing.T,
	cfg *config.Config,
	auth config.AuthConfig,
	agentConfigs map[string]*config.AgentConfig,
) *AppContext {
	t.Helper()

	projectRoot := t.TempDir()
	sessionDir := projectRoot + "/.chord/sessions/test"
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", sessionDir, err)
	}

	providerCfg := llm.NewProviderConfig("openai", cfg.Providers["openai"], []string{"test-key"})
	llmClient := llm.NewClient(providerCfg, &stubProviderImpl{}, "gpt-4o", 4096, "")
	ctxMgr := ctxmgr.NewManager(128000, 0)

	// Create pool policy
	poolPolicy := agent.NewRuntimeModelPoolPolicy()

	// Create MainAgent with pool policy
	mainAgent := agent.NewMainAgent(
		context.Background(),
		llmClient,
		ctxMgr,
		tools.NewRegistry(),
		&hook.NoopEngine{},
		sessionDir,
		"gpt-4o",
		projectRoot,
		cfg,
		nil,
		mcp.ClientInfo{Name: "chord-test", Version: "test"},
	)
	mainAgent.SetModelPoolPolicy(poolPolicy, "")

	// Configure builder agent with its model pool
	if builderCfg, ok := agentConfigs["builder"]; ok {
		mainAgent.SetAgentConfigs(map[string]*config.AgentConfig{"builder": builderCfg})
	}

	ac := &AppContext{
		Ctx:         context.Background(),
		ProjectRoot: projectRoot,
		SessionDir:  sessionDir,
		CtxMgr:      ctxMgr,
		MainAgent:   mainAgent,
		ProviderCache: &providerCache{
			m:     map[string]*llm.ProviderConfig{"openai": providerCfg},
			impls: map[string]llm.Provider{"openai": &stubProviderImpl{}},
			auth:  auth,
			cfg:   cfg,
		},
	}

	// Wire up pool policy with agent configs for test
	for name, agentCfg := range agentConfigs {
		if len(agentCfg.Models) > 0 {
			for poolName := range agentCfg.Models {
				poolPolicy.SetAgentOverride(name, poolName)
				break // use first pool
			}
		}
	}

	return ac
}
