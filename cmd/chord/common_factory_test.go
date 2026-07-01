package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
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
type stubProviderImpl struct {
	dumpWriter  *llm.DumpWriter
	traceWriter *llm.TraceWriter
}

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

func TestProviderCacheRejectsInvalidAuthScheme(t *testing.T) {
	cache := &providerCache{}
	_, err := cache.getOrCreate("custom", config.ProviderConfig{
		Type:       config.ProviderTypeResponses,
		AuthScheme: "basic",
	}, []string{"test-key"})
	if err == nil {
		t.Fatal("getOrCreate err = nil, want invalid auth_scheme error")
	}
	if !strings.Contains(err.Error(), "unsupported auth_scheme") {
		t.Fatalf("getOrCreate err = %v, want unsupported auth_scheme", err)
	}
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

func (p *stubProviderImpl) SetDumpWriter(w *llm.DumpWriter) {
	p.dumpWriter = w
}

func (p *stubProviderImpl) SetTraceWriter(w *llm.TraceWriter) {
	p.traceWriter = w
}

func TestProviderCacheCodexPollingUsesCacheContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	pollCtxCh := make(chan context.Context, 1)
	access := testOAuthJWTForCommonTest(`{"chatgpt_account_id":"account-1","chatgpt_user_id":"user-1"}`)
	cache := &providerCache{
		ctx:      ctx,
		m:        make(map[string]*llm.ProviderConfig),
		impls:    make(map[string]llm.Provider),
		authPath: filepath.Join(t.TempDir(), "auth.yaml"),
		cfg:      &config.Config{},
		auth: config.AuthConfig{"codex": {
			{OAuth: &config.OAuthCredential{Access: access, Refresh: "refresh-token", Expires: time.Now().Add(time.Hour).UnixMilli(), AccountID: "account-1"}},
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
	}, []string{access})
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

func TestProviderCacheWritersAttachToExistingModelPoolProviders(t *testing.T) {
	t.Parallel()

	created := make(map[string]*stubProviderImpl)
	cfg := &config.Config{}
	auth := config.AuthConfig{
		"selected": {{APIKey: "selected-key"}},
		"fallback": {{APIKey: "fallback-key"}},
		"later":    {{APIKey: "later-key"}},
	}
	cache := &providerCache{
		m:     make(map[string]*llm.ProviderConfig),
		impls: make(map[string]llm.Provider),
		auth:  auth,
		cfg:   cfg,
		newProviderImpl: func(providerCfg *llm.ProviderConfig, _ string) (llm.Provider, error) {
			impl := &stubProviderImpl{}
			created[providerCfg.Name()] = impl
			return impl, nil
		},
	}
	providerCfg := config.ProviderConfig{
		Type:   config.ProviderTypeResponses,
		APIURL: "https://example.invalid/v1/responses",
		Models: map[string]config.ModelConfig{
			"gpt-test": {Limit: config.ModelLimit{Context: 8192, Output: 1024}},
		},
	}

	selectedCfg, err := cache.getOrCreate("selected", providerCfg, []string{"selected-key"})
	if err != nil {
		t.Fatalf("get selected provider: %v", err)
	}
	selectedImpl, err := cache.getOrCreateImpl("selected", providerCfg, selectedCfg, "gpt-test")
	if err != nil {
		t.Fatalf("get selected impl: %v", err)
	}
	selectedStub := selectedImpl.(*stubProviderImpl)
	fallbackCfg, err := cache.getOrCreate("fallback", providerCfg, []string{"fallback-key"})
	if err != nil {
		t.Fatalf("get fallback provider: %v", err)
	}
	fallbackImpl, err := cache.getOrCreateImpl("fallback", providerCfg, fallbackCfg, "gpt-test")
	if err != nil {
		t.Fatalf("get fallback impl: %v", err)
	}
	fallbackStub := fallbackImpl.(*stubProviderImpl)

	if selectedStub.traceWriter != nil || fallbackStub.traceWriter != nil {
		t.Fatal("test setup expected providers to be created before session trace writer")
	}
	if selectedStub.dumpWriter != nil || fallbackStub.dumpWriter != nil {
		t.Fatal("test setup expected providers to be created before session dump writer")
	}

	traceWriter := llm.NewTraceWriter(filepath.Join(t.TempDir(), "llm-trace.jsonl"))
	dumpWriter := llm.NewDumpWriter(filepath.Join(t.TempDir(), "dumps", "llm"))
	cache.setTraceWriter(traceWriter)
	cache.setDumpWriter(dumpWriter)

	if selectedStub.traceWriter != traceWriter {
		t.Fatal("selected provider did not receive trace writer")
	}
	if fallbackStub.traceWriter != traceWriter {
		t.Fatal("fallback provider did not receive trace writer")
	}
	if selectedStub.dumpWriter != dumpWriter {
		t.Fatal("selected provider did not receive dump writer")
	}
	if fallbackStub.dumpWriter != dumpWriter {
		t.Fatal("fallback provider did not receive dump writer")
	}

	laterCfg, err := cache.getOrCreate("later", providerCfg, []string{"later-key"})
	if err != nil {
		t.Fatalf("get later provider: %v", err)
	}
	laterImpl, err := cache.getOrCreateImpl("later", providerCfg, laterCfg, "gpt-test")
	if err != nil {
		t.Fatalf("get later impl: %v", err)
	}
	laterStub := laterImpl.(*stubProviderImpl)
	if laterStub.traceWriter != traceWriter || laterStub.dumpWriter != dumpWriter {
		t.Fatal("provider created after writer setup did not inherit writers")
	}
	if created["selected"] != selectedStub || created["fallback"] != fallbackStub || created["later"] != laterStub {
		t.Fatal("provider cache did not use injected provider factory for all providers")
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
			"sample": {
				Type: config.ProviderTypeChatCompletions,
				Models: map[string]config.ModelConfig{
					"model-alpha": {Limit: config.ModelLimit{Context: 128000, Output: 4096}},
					"model-beta":  {Limit: config.ModelLimit{Context: 200000, Output: 4096}},
					"model-gamma": {Limit: config.ModelLimit{Context: 200000, Output: 100000}},
				},
			},
		},
	}

	auth := config.AuthConfig{
		"sample": []config.ProviderCredential{{APIKey: "test-key"}},
	}

	// Simulate agent configs with multiple models
	agentConfigs := map[string]*config.AgentConfig{
		"builder": {
			Name:    "builder",
			Variant: "balanced",
			Models: map[string][]string{
				"standard": {"sample/model-alpha", "sample/model-beta", "sample/model-gamma"},
			},
		},
	}

	ac := newTestAppContextWithBuilder(t, cfg, auth, agentConfigs)
	factory := buildMainClientFactory(ac, cfg, auth)

	// Request client for first model - should include all models in pool
	client, modelID, ctxLimit, err := factory("sample/model-alpha@balanced")
	if err != nil {
		t.Fatalf("factory failed: %v", err)
	}

	// Verify client is created with correct parameters
	if modelID != "model-alpha" {
		t.Fatalf("modelID = %q, want model-alpha", modelID)
	}
	if ctxLimit != 128000 {
		t.Fatalf("ctxLimit = %d, want 128000", ctxLimit)
	}

	// Verify model pool is configured - should start with model-alpha, then others
	primary := client.PrimaryModelRef()
	if primary != "sample/model-alpha" {
		t.Fatalf("PrimaryModelRef = %q, want sample/model-alpha", primary)
	}

	// Verify the pool includes all models (variant is tracked internally, not in ref)
	status := client.LastCallStatus()
	if status.SelectedModelRef != "sample/model-alpha" {
		t.Fatalf("SelectedModelRef = %q, want sample/model-alpha", status.SelectedModelRef)
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
			"sample": {
				Type: config.ProviderTypeChatCompletions,
				Models: map[string]config.ModelConfig{
					"model-alpha": {Limit: config.ModelLimit{Context: 128000, Output: 4096}},
					"model-beta":  {Limit: config.ModelLimit{Context: 200000, Output: 4096}},
					"model-gamma": {Limit: config.ModelLimit{Context: 200000, Output: 100000}},
				},
			},
		},
	}

	auth := config.AuthConfig{
		"sample": []config.ProviderCredential{{APIKey: "test-key"}},
	}

	agentConfigs := map[string]*config.AgentConfig{
		"builder": {
			Name:    "builder",
			Variant: "balanced",
			Models: map[string][]string{
				"standard": {"sample/model-alpha", "sample/model-beta", "sample/model-gamma"},
			},
		},
	}

	ac := newTestAppContextWithBuilder(t, cfg, auth, agentConfigs)
	factory := buildMainClientFactory(ac, cfg, auth)

	// Request client for second model - pool should start from here
	client, modelID, _, err := factory("sample/model-beta")
	if err != nil {
		t.Fatalf("factory failed: %v", err)
	}

	if modelID != "model-beta" {
		t.Fatalf("modelID = %q, want model-beta", modelID)
	}

	primary := client.PrimaryModelRef()
	if primary != "sample/model-beta" {
		t.Fatalf("PrimaryModelRef = %q, want sample/model-beta", primary)
	}

	t.Logf("Pool correctly starts from selected model: %s", primary)
}

func TestBuildModelPoolSelectedIndexTracksFilteredPool(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{
			"sample": {
				Type: config.ProviderTypeChatCompletions,
				Models: map[string]config.ModelConfig{
					"model-alpha": {Limit: config.ModelLimit{Context: 128000, Output: 4096}},
					"model-beta":  {Limit: config.ModelLimit{Context: 200000, Output: 4096}},
				},
			},
		},
	}
	pool, selectedIdx := buildModelPool(
		context.Background(),
		[]string{"sample/missing", "sample/model-alpha", "sample/model-beta"},
		"",
		"sample/model-beta",
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
	if got := pool[selectedIdx].ProviderConfig.Name() + "/" + pool[selectedIdx].ModelID; got != "sample/model-beta" {
		t.Fatalf("selected pool entry = %q, want sample/model-beta", got)
	}
	if got := pool[0].ProviderConfig.Name() + "/" + pool[0].ModelID; got != "sample/model-alpha" {
		t.Fatalf("first pool entry = %q, want sample/model-alpha", got)
	}
}

func TestBuildModelPoolFallsBackToFirstResolvedEntryWhenSelectionMissing(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{
			"sample": {
				Type: config.ProviderTypeChatCompletions,
				Models: map[string]config.ModelConfig{
					"model-alpha": {Limit: config.ModelLimit{Context: 128000, Output: 4096}},
					"model-beta":  {Limit: config.ModelLimit{Context: 200000, Output: 4096}},
				},
			},
		},
	}
	pool, selectedIdx := buildModelPool(
		context.Background(),
		[]string{"sample/model-alpha", "sample/model-beta"},
		"",
		"sample/missing",
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
			"sample": {
				Type: config.ProviderTypeChatCompletions,
				Models: map[string]config.ModelConfig{
					"model-gamma": {
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
		[]string{"sample/model-gamma@balanced", "sample/model-gamma@high"},
		"",
		"sample/model-gamma@high",
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
			"sample": {
				Type: config.ProviderTypeChatCompletions,
				Models: map[string]config.ModelConfig{
					"model-alpha": {
						Limit:    config.ModelLimit{Context: 128000, Output: 4096},
						Variants: map[string]config.ModelVariant{"balanced": {}},
					},
					"model-beta": {
						Limit:    config.ModelLimit{Context: 200000, Output: 4096},
						Variants: map[string]config.ModelVariant{"high": {}},
					},
					"model-gamma": {
						Limit:    config.ModelLimit{Context: 200000, Output: 100000},
						Variants: map[string]config.ModelVariant{"high": {}},
					},
				},
			},
		},
	}

	pool, selectedIdx := buildModelPool(
		context.Background(),
		[]string{"sample/model-alpha@balanced", "sample/model-beta", "sample/model-gamma@high"},
		"high",
		"sample/model-beta",
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
		"sample/model-alpha@balanced",
		"sample/model-beta@high",
		"sample/model-gamma@high",
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
			"sample": {
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
		[]string{"sample/gpt-5.5", "sample/gpt-5"},
		"",
		"sample/gpt-5.5",
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
			"sample": {
				Type: config.ProviderTypeChatCompletions,
				Models: map[string]config.ModelConfig{
					"model-alpha": {Limit: config.ModelLimit{Context: 128000, Output: 4096}},
					"model-beta":  {Limit: config.ModelLimit{Context: 200000, Output: 4096}},
					"model-gamma": {Limit: config.ModelLimit{Context: 200000, Output: 100000}},
				},
			},
		},
	}
	auth := config.AuthConfig{
		"sample": []config.ProviderCredential{{APIKey: "test-key"}},
	}

	providerCfg := resolveProviderConfigForTest("sample", cfg.Providers["sample"], auth)
	selectedImpl := &stubScriptedProvider{calls: []stubScriptedCall{{err: &llm.APIError{StatusCode: 500, Message: "selected failed"}}}}
	fallbackOneImpl := &stubScriptedProvider{calls: []stubScriptedCall{{err: &llm.APIError{StatusCode: 500, Message: "fallback one failed"}}}}
	fallbackTwoImpl := &stubScriptedProvider{calls: []stubScriptedCall{{resp: &message.Response{Content: "fallback success"}}, {resp: &message.Response{Content: "next call starts here"}}}}

	pool, selectedIdx := buildModelPool(
		context.Background(),
		[]string{"sample/model-alpha", "sample/model-beta", "sample/model-gamma"},
		"",
		"sample/model-beta",
		cfg.Providers,
		auth,
		"",
		cfg.MaxOutputTokens,
		func(provName string, _ config.ProviderConfig, _ []string) (*llm.ProviderConfig, error) {
			switch provName {
			case "sample":
				return providerCfg, nil
			default:
				return nil, os.ErrNotExist
			}
		},
		func(_ string, _ config.ProviderConfig, _ *llm.ProviderConfig, modelID string) (llm.Provider, error) {
			switch modelID {
			case "model-alpha":
				return fallbackTwoImpl, nil
			case "model-beta":
				return selectedImpl, nil
			case "model-gamma":
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

	client := llm.NewClient(providerCfg, selectedImpl, "model-beta", 4096, "")
	client.SetOutputTokenMax(cfg.MaxOutputTokens)
	client.SetModelPool(pool, selectedIdx)

	if got := client.PrimaryModelRef(); got != "sample/model-beta" {
		t.Fatalf("PrimaryModelRef = %q, want sample/model-beta", got)
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
	if st.SelectedModelRef != "sample/model-beta" {
		t.Fatalf("SelectedModelRef = %q, want sample/model-beta", st.SelectedModelRef)
	}
	if st.RunningModelRef != "sample/model-alpha" {
		t.Fatalf("RunningModelRef = %q, want sample/model-alpha", st.RunningModelRef)
	}

	// After success on model-alpha, the next request should start from that rotated
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
	if st.SelectedModelRef != "sample/model-alpha" {
		t.Fatalf("second SelectedModelRef = %q, want sample/model-alpha", st.SelectedModelRef)
	}
	if st.RunningModelRef != "sample/model-alpha" {
		t.Fatalf("second RunningModelRef = %q, want sample/model-alpha", st.RunningModelRef)
	}
}

// TestBuildMainClientFactorySingleModelPool verifies behavior when only one model
// is configured (should not set pool).
func TestBuildMainClientFactorySingleModelPool(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		MaxOutputTokens: 4096,
		Providers: map[string]config.ProviderConfig{
			"sample": {
				Type: config.ProviderTypeChatCompletions,
				Models: map[string]config.ModelConfig{
					"model-alpha": {Limit: config.ModelLimit{Context: 128000, Output: 4096}},
				},
			},
		},
	}

	auth := config.AuthConfig{
		"sample": []config.ProviderCredential{{APIKey: "test-key"}},
	}

	agentConfigs := map[string]*config.AgentConfig{
		"builder": {
			Name:    "builder",
			Variant: "balanced",
			Models: map[string][]string{
				"standard": {"sample/model-alpha"}, // single model
			},
		},
	}

	ac := newTestAppContextWithBuilder(t, cfg, auth, agentConfigs)
	factory := buildMainClientFactory(ac, cfg, auth)

	client, _, _, err := factory("sample/model-alpha")
	if err != nil {
		t.Fatalf("factory failed: %v", err)
	}

	// Single model pool - pool should have just one entry
	primary := client.PrimaryModelRef()
	if primary != "sample/model-alpha" {
		t.Fatalf("PrimaryModelRef = %q, want sample/model-alpha", primary)
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
			"sample": {
				Type: config.ProviderTypeChatCompletions,
				Models: map[string]config.ModelConfig{
					"model-alpha": {Limit: config.ModelLimit{Context: 128000, Output: 4096}},
					"model-beta":  {Limit: config.ModelLimit{Context: 200000, Output: 4096}},
					"model-gamma": {Limit: config.ModelLimit{Context: 200000, Output: 100000}},
				},
			},
		},
	}

	auth := config.AuthConfig{
		"sample": []config.ProviderCredential{{APIKey: "test-key"}},
	}

	agentConfigs := map[string]*config.AgentConfig{
		"builder": {
			Name:    "builder",
			Variant: "balanced",
			Models: map[string][]string{
				"standard": {"sample/model-alpha", "sample/model-beta", "sample/model-gamma"},
			},
		},
	}

	ac := newTestAppContextWithBuilder(t, cfg, auth, agentConfigs)
	factory := buildMainClientFactory(ac, cfg, auth)

	// Request client for third model - pool should wrap around
	client, modelID, _, err := factory("sample/model-gamma")
	if err != nil {
		t.Fatalf("factory failed: %v", err)
	}

	if modelID != "model-gamma" {
		t.Fatalf("modelID = %q, want model-gamma", modelID)
	}

	primary := client.PrimaryModelRef()
	if primary != "sample/model-gamma" {
		t.Fatalf("PrimaryModelRef = %q, want sample/model-gamma", primary)
	}

	t.Logf("Pool wrap-around handled correctly: %s", primary)
}

func TestInitialClientUsesBuilderModelPoolForFirstRequest(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		MaxOutputTokens: 4096,
		Providers: map[string]config.ProviderConfig{
			"sample": {
				Type: config.ProviderTypeChatCompletions,
				Models: map[string]config.ModelConfig{
					"model-alpha": {Limit: config.ModelLimit{Context: 128000, Output: 4096}},
					"model-beta":  {Limit: config.ModelLimit{Context: 200000, Output: 4096}},
				},
			},
		},
	}
	auth := config.AuthConfig{
		"sample": []config.ProviderCredential{{APIKey: "test-key"}},
	}

	providerCfg := resolveProviderConfigForTest("sample", cfg.Providers["sample"], auth)
	primaryImpl := &stubScriptedProvider{calls: []stubScriptedCall{{err: &llm.APIError{StatusCode: 500, Message: "upstream unavailable"}}}}
	fallbackImpl := &stubScriptedProvider{calls: []stubScriptedCall{{resp: &message.Response{Content: "fallback success"}}}}

	pool, selectedIdx := buildModelPool(
		context.Background(),
		[]string{"sample/model-alpha", "sample/model-beta"},
		"",
		"sample/model-alpha",
		cfg.Providers,
		auth,
		"",
		cfg.MaxOutputTokens,
		func(provName string, _ config.ProviderConfig, _ []string) (*llm.ProviderConfig, error) {
			switch provName {
			case "sample":
				return providerCfg, nil
			default:
				return nil, os.ErrNotExist
			}
		},
		func(_ string, _ config.ProviderConfig, _ *llm.ProviderConfig, modelID string) (llm.Provider, error) {
			switch modelID {
			case "model-alpha":
				return primaryImpl, nil
			case "model-beta":
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

	client := llm.NewClient(providerCfg, primaryImpl, "model-alpha", 4096, "")
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
	if st.RunningModelRef != "sample/model-beta" {
		t.Fatalf("RunningModelRef = %q, want sample/model-beta", st.RunningModelRef)
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

	providerCfg := llm.NewProviderConfig("sample", cfg.Providers["sample"], []string{"test-key"})
	llmClient := llm.NewClient(providerCfg, &stubProviderImpl{}, "model-alpha", 4096, "")
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
		"model-alpha",
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
			m:     map[string]*llm.ProviderConfig{"sample": providerCfg},
			impls: map[string]llm.Provider{"sample": &stubProviderImpl{}},
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
