package tui

import (
	"fmt"
	"strings"
	"testing"

	uitheme "github.com/keakon/chord/internal/tui/theme"
)

func BenchmarkLargeCardWindow(b *testing.B) {
	ApplyTheme(uitheme.Default())
	for _, paragraphs := range []int{1024, 8192} {
		content := strings.Repeat("A short paragraph with enough words to represent a streaming response.\n\n", paragraphs)
		b.Run(fmt.Sprintf("%d/first_window", paragraphs), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				block := &Block{Type: BlockAssistant, Content: content}
				_ = block.RenderRange(100, "", 0, 40)
			}
		})
		b.Run(fmt.Sprintf("%d/scroll_window", paragraphs), func(b *testing.B) {
			block := &Block{Type: BlockAssistant, Content: content}
			count := block.LineCount(100)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; b.Loop(); i++ {
				start := i * 97 % (count - 40)
				_ = block.RenderRange(100, "", start, start+40)
			}
		})
	}
}
