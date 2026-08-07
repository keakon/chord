package llm

import (
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
)

func TestNormalizeResponseUsageHandlesProtocolAndCompatSemantics(t *testing.T) {
	tests := []struct {
		name         string
		providerType string
		compat       *config.UsageCompatConfig
		usage        message.TokenUsage
		wantInput    int
	}{
		{
			name: "anthropic separates reads and writes", providerType: config.ProviderTypeMessages,
			usage:     message.TokenUsage{InputTokens: 100, CacheReadTokens: 70, CacheWriteTokens: 20, InputSemanticsKnown: true},
			wantInput: 170,
		},
		{
			name: "gemini prompt includes cached content", providerType: config.ProviderTypeGenerateContent,
			usage:     message.TokenUsage{InputTokens: 100, CacheReadTokens: 70, InputSemanticsKnown: true, InputIncludesCacheRead: true},
			wantInput: 100,
		},
		{
			name: "openai includes read and write", providerType: config.ProviderTypeResponses,
			usage:     message.TokenUsage{InputTokens: 1000, CacheReadTokens: 600, CacheWriteTokens: 100, InputSemanticsKnown: true, InputIncludesCacheRead: true, InputIncludesCacheWrite: true},
			wantInput: 900,
		},
		{
			name: "unambiguous split compatible gateway", providerType: config.ProviderTypeResponses,
			usage:     message.TokenUsage{InputTokens: 2, CacheReadTokens: 30_000, InputSemanticsKnown: true, InputIncludesCacheRead: true, InputIncludesCacheWrite: true},
			wantInput: 30_002,
		},
		{
			name: "ambiguous split gateway uses explicit override", providerType: config.ProviderTypeResponses,
			compat:    &config.UsageCompatConfig{InputIncludesCacheRead: new(false), InputIncludesCacheWrite: new(false)},
			usage:     message.TokenUsage{InputTokens: 100_000, CacheReadTokens: 50_000, CacheWriteTokens: 10_000, InputSemanticsKnown: true, InputIncludesCacheRead: true, InputIncludesCacheWrite: true},
			wantInput: 150_000,
		},
		{
			name: "anthropic compatible inclusive gateway uses override", providerType: config.ProviderTypeMessages,
			compat:    &config.UsageCompatConfig{InputIncludesCacheRead: new(true)},
			usage:     message.TokenUsage{InputTokens: 100_000, CacheReadTokens: 50_000, InputSemanticsKnown: true},
			wantInput: 100_000,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := NewProviderConfig("provider", config.ProviderConfig{
				Type:   tt.providerType,
				Compat: &config.ProviderCompatConfig{Usage: tt.compat},
			}, nil)
			resp := &message.Response{Usage: &tt.usage}
			normalizeResponseUsage(provider, resp)
			if resp.Usage.InputTokens != tt.wantInput {
				t.Fatalf("InputTokens = %d, want %d", resp.Usage.InputTokens, tt.wantInput)
			}
			fullInput := resp.Usage.InputTokens + resp.Usage.CacheWriteTokens
			if resp.Usage.CacheReadTokens > fullInput {
				t.Fatalf("cache read %d exceeds full input %d", resp.Usage.CacheReadTokens, fullInput)
			}
		})
	}
}
