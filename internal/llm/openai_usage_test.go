package llm

import "testing"

func TestNormalizedOpenAIInputTokensHandlesProviderCacheShapes(t *testing.T) {
	tests := []struct {
		name                 string
		input, cacheRead     int
		cacheWrite, expected int
	}{
		{name: "standard input includes cache read", input: 1000, cacheRead: 600, cacheWrite: 100, expected: 900},
		{name: "split compatible gateway", input: 2, cacheRead: 30000, expected: 30002},
		{name: "cache write is separated", input: 1000, cacheWrite: 100, expected: 900},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizedOpenAIInputTokens(tt.input, tt.cacheRead, tt.cacheWrite); got != tt.expected {
				t.Fatalf("normalizedOpenAIInputTokens() = %d, want %d", got, tt.expected)
			}
		})
	}
}
