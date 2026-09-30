package llm

import (
	"testing"

	"github.com/keakon/chord/internal/config"
)

func TestPromptCacheSettingsForModelRefResolvesPoolTuning(t *testing.T) {
	primary := NewProviderConfig("sample", config.ProviderConfig{
		Type: config.ProviderTypeMessages, APIURL: "https://example.invalid/v1/messages",
		Models: map[string]config.ModelConfig{"model": {
			PromptCache: &config.PromptCacheConfig{Mode: "explicit", TTL: "5m"},
			Variants:    map[string]config.ModelVariant{"long": {PromptCache: &config.PromptCacheConfig{TTL: "1h"}}},
		}},
	}, nil)
	fallback := NewProviderConfig("backup", config.ProviderConfig{
		Type: config.ProviderTypeMessages, APIURL: "https://example.invalid/v1/messages",
		Models: map[string]config.ModelConfig{"model": {PromptCache: &config.PromptCacheConfig{Mode: "auto", TTL: "1h"}}},
	}, nil)
	client := NewClient(primary, nil, "model", 4096, "")
	client.SetFallbackModels([]FallbackModel{{ProviderConfig: fallback, ModelID: "model"}})
	for _, tc := range []struct{ ref, want string }{{"sample/model", "5m"}, {"backup/model", "1h"}, {"missing/model", ""}} {
		if _, got := client.PromptCacheSettingsForModelRef(tc.ref); got != tc.want {
			t.Fatalf("TTL for %s = %q, want %q", tc.ref, got, tc.want)
		}
	}
	client.SetVariant("long")
	if _, got := client.PromptCacheSettingsForModelRef("sample/model@long"); got != "1h" {
		t.Fatalf("variant TTL = %q, want 1h", got)
	}
	client.SetNextRequestTuningOverride(RequestTuning{Anthropic: AnthropicTuning{PromptCacheTTL: "5m"}})
	if _, got := client.PromptCacheSettingsForModelRef("sample/model@long"); got != "5m" {
		t.Fatalf("request override TTL = %q, want 5m", got)
	}
	if _, got := client.PromptCacheSettingsForModelRef("backup/model"); got != "1h" {
		t.Fatalf("primary override leaked to fallback TTL = %q", got)
	}
	client.SetNextRequestTuningOverride(RequestTuning{Anthropic: AnthropicTuning{PromptCacheMode: "off", PromptCacheTTL: "1h"}})
	if mode, ttl := client.PromptCacheSettingsForModelRef("sample/model"); mode != "off" || ttl != "1h" {
		t.Fatalf("disabled cache settings = %q %q", mode, ttl)
	}
}

func TestPromptCacheSettingsForModelRefIgnoresNonMessagesTransport(t *testing.T) {
	provider := NewProviderConfig("sample", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions, APIURL: "https://example.invalid/v1/chat/completions",
		Models: map[string]config.ModelConfig{"model": {PromptCache: &config.PromptCacheConfig{TTL: "1h"}}},
	}, nil)
	client := NewClient(provider, nil, "model", 4096, "")
	if _, got := client.PromptCacheSettingsForModelRef("sample/model"); got != "" {
		t.Fatalf("non-Messages cache TTL = %q, want empty", got)
	}
	var absent *Client
	if _, got := absent.PromptCacheSettingsForModelRef("sample/model"); got != "" {
		t.Fatalf("absent client TTL = %q, want empty", got)
	}
}

func TestPromptCacheSettingsPreferExactVariantPoolTarget(t *testing.T) {
	provider := NewProviderConfig("sample", config.ProviderConfig{
		Type: config.ProviderTypeMessages, APIURL: "https://example.invalid/v1/messages",
		Models: map[string]config.ModelConfig{"model": {Variants: map[string]config.ModelVariant{
			"short": {PromptCache: &config.PromptCacheConfig{TTL: "5m"}},
			"long":  {PromptCache: &config.PromptCacheConfig{TTL: "1h"}},
		}}},
	}, nil)
	client := NewClient(provider, nil, "model", 4096, "")
	client.SetModelPool([]FallbackModel{
		{ProviderConfig: provider, ModelID: "model", Variant: "short"},
		{ProviderConfig: provider, ModelID: "model", Variant: "long"},
	}, 0)
	if _, ttl := client.PromptCacheSettingsForModelRef("sample/model@long"); ttl != "1h" {
		t.Fatalf("exact fallback variant used the primary variant's TTL: %q", ttl)
	}
}
