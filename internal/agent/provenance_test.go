package agent

import (
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/modelcompat"
)

func TestMainAssistantProvenanceUsesProviderTypeWireFamily(t *testing.T) {
	a := newReadyTestMainAgent(t)
	providerCfg := llm.NewProviderConfig("deepseek", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"deepseek-v4-pro": {Limit: config.ModelLimit{Context: 128000, Output: 4096}},
		},
	}, []string{"test-key"})
	client := llm.NewClient(providerCfg, stubProvider{}, "deepseek-v4-pro", 4096, "sys")
	a.swapLLMClientWithRef(client, "deepseek-v4-pro", 128000, "deepseek/deepseek-v4-pro")

	prov := mainAssistantProvenance(a)
	if prov == nil {
		t.Fatal("expected non-nil provenance")
	}
	if prov.ProviderID != "deepseek" {
		t.Fatalf("ProviderID = %q, want deepseek", prov.ProviderID)
	}
	if prov.ModelID != "deepseek-v4-pro" {
		t.Fatalf("ModelID = %q, want deepseek-v4-pro", prov.ModelID)
	}
	if prov.WireFamily != modelcompat.WireFamilyOpenAIChat {
		t.Fatalf("WireFamily = %q, want %q", prov.WireFamily, modelcompat.WireFamilyOpenAIChat)
	}
}

func TestMainAssistantProvenanceUsesRunningFallbackWireFamily(t *testing.T) {
	a := newReadyTestMainAgent(t)
	primaryCfg := llm.NewProviderConfig("chat-primary", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"deepseek-v4-pro": {Limit: config.ModelLimit{Context: 128000, Output: 4096}},
		},
	}, []string{"test-key"})
	fallbackCfg := llm.NewProviderConfig("messages-fallback", config.ProviderConfig{
		Type: config.ProviderTypeMessages,
		Models: map[string]config.ModelConfig{
			"deepseek-v4-pro": {Limit: config.ModelLimit{Context: 128000, Output: 4096}},
		},
	}, []string{"test-key"})
	client := llm.NewClient(primaryCfg, stubProvider{}, "deepseek-v4-pro", 4096, "sys")
	client.SetFallbackModels([]llm.FallbackModel{{
		ProviderConfig: fallbackCfg,
		ProviderImpl:   stubProvider{},
		ModelID:        "deepseek-v4-pro",
		MaxTokens:      4096,
	}})
	a.swapLLMClientWithRef(client, "deepseek-v4-pro", 128000, "chat-primary/deepseek-v4-pro")
	a.llmMu.Lock()
	a.runningModelRef = "messages-fallback/deepseek-v4-pro"
	a.llmMu.Unlock()

	prov := mainAssistantProvenance(a)
	if prov == nil {
		t.Fatal("expected non-nil provenance")
	}
	if prov.ProviderID != "messages-fallback" || prov.ModelID != "deepseek-v4-pro" {
		t.Fatalf("running target provenance = %+v", prov)
	}
	if prov.WireFamily != modelcompat.WireFamilyAnthropic {
		t.Fatalf("WireFamily = %q, want %q", prov.WireFamily, modelcompat.WireFamilyAnthropic)
	}
}
