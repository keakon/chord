package agent

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
)

type auxModelPoolStubProvider struct{}

func (auxModelPoolStubProvider) CompleteStream(
	_ context.Context,
	_ string,
	_ string,
	_ string,
	_ []message.Message,
	_ []message.ToolDefinition,
	_ int,
	_ llm.RequestTuning,
	_ llm.StreamCallback,
) (*message.Response, error) {
	return &message.Response{Content: "ok", StopReason: "stop"}, nil
}

func newAuxModelPoolTestClient(providerName, modelID string) *llm.Client {
	providerCfg := llm.NewProviderConfig(providerName, config.ProviderConfig{
		Type: config.ProviderTypeMessages,
		Models: map[string]config.ModelConfig{
			modelID: {Limit: config.ModelLimit{Context: 8192, Output: 1024}},
		},
	}, []string{"test-key"})
	return llm.NewClient(providerCfg, auxModelPoolStubProvider{}, modelID, 1024, "")
}

func TestResolveConfiguredModelPoolFallsBackFromProjectToGlobal(t *testing.T) {
	a := &MainAgent{
		projectConfig: &config.Config{
			ThinkingTranslation: &config.ThinkingTranslationConfig{TargetLanguage: "zh-Hans", ModelPool: "fast"},
			ModelPools: map[string][]string{
				"local": {"project/local"},
			},
		},
		globalConfig: &config.Config{
			ModelPools: map[string][]string{
				"fast": {" global/fast-one ", "", "global/fast-two"},
			},
		},
	}

	refs, err := a.resolveConfiguredModelPool("fast")
	if err != nil {
		t.Fatalf("resolveConfiguredModelPool: %v", err)
	}
	want := []string{"global/fast-one", "global/fast-two"}
	if !reflect.DeepEqual(refs, want) {
		t.Fatalf("refs = %#v, want %#v", refs, want)
	}
}

func TestResolveConfiguredModelPoolPrefersProjectPool(t *testing.T) {
	a := &MainAgent{
		projectConfig: &config.Config{ModelPools: map[string][]string{"fast": {"project/fast"}}},
		globalConfig:  &config.Config{ModelPools: map[string][]string{"fast": {"global/fast"}}},
	}

	refs, err := a.resolveConfiguredModelPool("fast")
	if err != nil {
		t.Fatalf("resolveConfiguredModelPool: %v", err)
	}
	want := []string{"project/fast"}
	if !reflect.DeepEqual(refs, want) {
		t.Fatalf("refs = %#v, want %#v", refs, want)
	}
}

func TestNewThinkingTranslatorReusesAuxClientCursor(t *testing.T) {
	a := &MainAgent{
		projectConfig: &config.Config{ModelPools: map[string][]string{"fast": {"first/ref"}}},
	}
	calls := 0
	client := newAuxModelPoolTestClient("first", "ref")
	a.modelSwitchFactory = func(providerModel string) (*llm.Client, string, int, error) {
		calls++
		if providerModel != "first/ref" {
			return nil, "", 0, fmt.Errorf("unexpected ref %q", providerModel)
		}
		return client, providerModel, 0, nil
	}

	translator, err := a.newThinkingTranslator("fast")
	if err != nil {
		t.Fatalf("newThinkingTranslator() error = %v", err)
	}
	first, err := translator.NewClient()
	if err != nil {
		t.Fatalf("first NewClient() error = %v", err)
	}
	second, err := translator.NewClient()
	if err != nil {
		t.Fatalf("second NewClient() error = %v", err)
	}
	if first != second {
		t.Fatal("NewClient returned different clients; want shared cursor client")
	}
	if first.PrimaryModelRef() != "first/ref" {
		t.Fatalf("NewClient primary ref = %q, want first/ref", first.PrimaryModelRef())
	}
	if calls != 1 {
		t.Fatalf("modelSwitchFactory calls = %d, want 1", calls)
	}
}

func TestCompactionModelRefUsesMainModelWhenPoolMissing(t *testing.T) {
	a := &MainAgent{
		projectConfig: &config.Config{},
		globalConfig: &config.Config{
			Context:    config.ContextConfig{Compaction: config.CompactionConfig{}},
			Providers:  map[string]config.ProviderConfig{},
			ModelPools: map[string][]string{},
		},
	}

	if got := a.compactionModelRef(); got != "" {
		t.Fatalf("compactionModelRef() = %q, want empty without main model", got)
	}
}

func TestNewCompactionClientInheritsMainModelPoolWhenUnconfigured(t *testing.T) {
	a := &MainAgent{}
	first := newAuxModelPoolTestClient("first", "ref")
	second := newAuxModelPoolTestClient("second", "ref")
	mainClient := newAuxClientFromPool([]llm.FallbackModel{
		first.PrimaryModelEntry(),
		second.PrimaryModelEntry(),
	}, 1, 0, config.ServiceTierSlow)
	a.llmClient = mainClient

	client, contextLimit, err := a.newCompactionClient("")
	if err != nil {
		t.Fatalf("newCompactionClient() error = %v", err)
	}
	if got := client.PrimaryModelRef(); got != "second/ref" {
		t.Fatalf("compaction primary ref = %q, want inherited cursor second/ref", got)
	}
	pool, selectedIdx := client.ModelPoolSnapshot()
	if len(pool) != 2 {
		t.Fatalf("inherited pool len = %d, want 2", len(pool))
	}
	if selectedIdx != 0 {
		t.Fatalf("snapshot selectedIdx = %d, want 0 for cloned cursor head", selectedIdx)
	}
	if got := client.ServiceTier(); got != config.ServiceTierSlow {
		t.Fatalf("compaction service tier = %q, want %q", got, config.ServiceTierSlow)
	}
	if contextLimit != 8192 {
		t.Fatalf("contextLimit = %d, want 8192", contextLimit)
	}
}

func TestNewCompactionClientFailsWhenConfiguredPoolRefFails(t *testing.T) {
	a := &MainAgent{
		projectConfig: &config.Config{
			Context:    config.ContextConfig{Compaction: config.CompactionConfig{ModelPool: "compact"}},
			ModelPools: map[string][]string{"compact": {"bad/ref"}},
		},
	}
	a.SetProviderModelRef("main/ref")
	calls := make([]string, 0, 1)
	a.modelSwitchFactory = func(providerModel string) (*llm.Client, string, int, error) {
		calls = append(calls, providerModel)
		return nil, "", 0, fmt.Errorf("configured compaction model failed")
	}

	_, _, err := a.newCompactionClient("")
	if err == nil {
		t.Fatal("newCompactionClient() error = nil, want configured pool failure")
	}
	if !reflect.DeepEqual(calls, []string{"bad/ref"}) {
		t.Fatalf("modelSwitchFactory calls = %#v, want configured ref only", calls)
	}
}

func TestNewAuxModelPoolClientAppliesServiceTierToDirectFallback(t *testing.T) {
	a := &MainAgent{}
	mainClient := newAuxClientFromPool([]llm.FallbackModel{{
		ProviderConfig: llm.NewProviderConfig("main", config.ProviderConfig{
			Type: config.ProviderTypeMessages,
			Models: map[string]config.ModelConfig{
				"model": {Limit: config.ModelLimit{Context: 8192, Output: 1024}},
			},
		}, []string{"key"}),
		ProviderImpl: auxModelPoolStubProvider{},
		ModelID:      "model",
		MaxTokens:    1024,
	}}, 0, 0, config.ServiceTierSlow)
	a.llmClient = mainClient

	directClient := llm.NewClient(llm.NewProviderConfig("aux", config.ProviderConfig{
		Type: config.ProviderTypeMessages,
		Models: map[string]config.ModelConfig{
			"model": {Limit: config.ModelLimit{Context: 8192, Output: 1024}},
		},
	}, []string{"key"}), nil, "model", 1024, "")
	a.modelSwitchFactory = func(providerModel string) (*llm.Client, string, int, error) {
		if providerModel != "aux/model" {
			t.Fatalf("providerModel = %q, want aux/model", providerModel)
		}
		return directClient, providerModel, 0, nil
	}

	client, err := a.newAuxModelPoolClient([]string{"aux/model"}, 0, 2048)
	if err != nil {
		t.Fatalf("newAuxModelPoolClient() error = %v", err)
	}
	if client == nil {
		t.Fatal("newAuxModelPoolClient() returned nil client")
	}
	if got := client.ServiceTier(); got != config.ServiceTierSlow {
		t.Fatalf("direct fallback service tier = %q, want %q", got, config.ServiceTierSlow)
	}
	if got := client.OutputTokenMax(); got != 2048 {
		t.Fatalf("direct fallback output max = %d, want 2048", got)
	}
}
