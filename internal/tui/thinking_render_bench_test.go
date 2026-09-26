package tui

import (
	"strings"
	"testing"
)

// Interleaved cards exercise redraws while several streams remain visible.
func BenchmarkRenderThinkingStreamingInterleaved(b *testing.B) {
	ApplyTheme(DefaultTheme())
	content := strings.Repeat("**Inspecting**\nA short paragraph with context.\n\n", 200)
	blocks := []*Block{
		{Type: BlockThinking, Streaming: true, Content: content + "First pending paragraph"},
		{Type: BlockThinking, Streaming: true, Content: content + "Second pending paragraph"},
	}
	for _, block := range blocks {
		_ = block.renderThinking(70)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		for _, block := range blocks {
			_ = block.renderThinking(70)
		}
	}
}
