package markdownrender

import (
	"testing"

	uitheme "github.com/keakon/chord/internal/tui/theme"
)

func BenchmarkRendererSettledCJKStrong(b *testing.B) {
	r := New(uitheme.Default())
	content := "**阅读提示：**正文介绍图书目录、章节顺序与示例，读者可以按主题查找内容，也可以从第一页开始阅读；当前进度 **81.8%**。每个章节都附有练习。\n\n请参考**《设计原则》**一书，这是**「重要」**的内容。"
	b.ReportAllocs()
	for b.Loop() {
		r.Render(content, 120)
	}
}

func BenchmarkRendererSettledPlainProse(b *testing.B) {
	r := New(uitheme.Default())
	content := "The library has a **reading room**, a `catalog` terminal, and a collection of illustrated books. Readers can browse the shelves, reserve a title, or ask at the front desk for a list of recommended books."
	b.ReportAllocs()
	for b.Loop() {
		r.Render(content, 120)
	}
}
