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
