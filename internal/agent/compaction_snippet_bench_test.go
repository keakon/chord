package agent

import (
	"fmt"
	"strings"
	"testing"
)

// BenchmarkCompactTextSnippet measures the excerpt path used by checkpoint
// anchors and evidence summaries, with a fixed 220-rune output budget.
func BenchmarkCompactTextSnippet(b *testing.B) {
	for _, sample := range []struct {
		name string
		line string
	}{
		{name: "ascii", line: "source line from tool output\n"},
		{name: "unicode", line: "工具输出内容🙂\n"},
	} {
		for _, size := range []int{4 << 10, 1 << 20, 16 << 20} {
			text := strings.Repeat(sample.line, size/len(sample.line))
			b.Run(fmt.Sprintf("%s/%d_KiB", sample.name, size>>10), func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					if compactTextSnippet(text, 220) == "" {
						b.Fatal("missing excerpt")
					}
				}
			})
		}
	}
}
