package tui

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	uitheme "github.com/keakon/chord/internal/tui/theme"
)

func TestStreamingProseMatchesFullRender(t *testing.T) {
	ApplyTheme(uitheme.Default())
	samples := []string{
		"First **bold** paragraph.\n\nSecond *italic* paragraph with `code`.\n\nThird paragraph.\n\nTail",
		"# Heading\n\nFirst paragraph.\n\n## Heading\n\nAnother paragraph.\n\nTail",
		"First paragraph.\n\n- First item\n\n- Second item\n  continued\n\nTail",
		"First paragraph.\n\nTerm\n\n: Definition\n\nTail",
		"First paragraph.\n\n[reference][label]\n\n[label]: https://example.invalid\n\nTail",
		"First paragraph.\n\nName | Value\n--- | ---\nOne | Two\n\nTail",
		"First paragraph.\n\n> Quote\n>\n> continued\n\nTail",
		"First paragraph.\n\n```go\nvar value = 1\n```\n\nTail",
		"First paragraph.\n\nSetext heading\n---\n\nTail",
		"First paragraph.\n\n<div>\n\nbody\n\n</div>\n\nTail",
		"First **unclosed\n\nSecond paragraph.\n\nThird **closed**.\n\nTail",
		"First paragraph.  \nHard break.\n\nSecond paragraph.\n\nTail",
		"Unicode 中文 **粗体**。\n\nSecond paragraph.\n\nTail",
		"First paragraph.\r\n\r\nSecond paragraph.\r\n\r\nTail",
		"First paragraph.\n \nSecond paragraph.\n\nTail",
	}
	for sampleIndex, text := range samples {
		for _, width := range []int{40, 90} {
			t.Run(fmt.Sprintf("%d/%d", sampleIndex, width), func(t *testing.T) {
				block := &Block{ID: 1, Type: BlockAssistant, Streaming: true}
				var snapshots, copies [][]string
				for end := 1; end <= len(text); end++ {
					block.Content = text[:end]
					block.InvalidateCache()
					got := block.Render(width, "")
					reference := &Block{ID: 1, Type: BlockAssistant, Streaming: true, Content: text[:end]}
					want := reference.Render(width, "")
					if !slices.Equal(got, want) || !slices.Equal(block.renderSyntheticPrefixWidths, reference.renderSyntheticPrefixWidths) ||
						!slices.Equal(block.renderSoftWrapContinuations, reference.renderSoftWrapContinuations) {
						t.Fatalf("at byte %d:\ngot %q\nwant %q", end, got, want)
					}
					snapshots = append(snapshots, got)
					copies = append(copies, slices.Clone(got))
				}
				for i := range snapshots {
					if !slices.Equal(snapshots[i], copies[i]) {
						t.Fatalf("snapshot %d changed after append", i)
					}
				}
			})
		}
	}
}

func TestStreamingProseReuseAndReplacement(t *testing.T) {
	ApplyTheme(uitheme.Default())
	block := &Block{Type: BlockAssistant, Streaming: true}
	for _, content := range []string{
		"First paragraph.\n\nSecond paragraph.\n\n",
		"First paragraph.\n\nSecond paragraph.\n\nThird paragraph.\n\n",
		"Replacement paragraph.\n\nAnother paragraph.\n\n",
	} {
		block.Content = content
		block.InvalidateCache()
		got := block.Render(90, "")
		reference := &Block{Type: BlockAssistant, Streaming: true, Content: content}
		if !slices.Equal(got, reference.Render(90, "")) {
			t.Fatal("render changed")
		}
		if block.streamProse.blocked || block.streamProse.tailStart == 0 || block.streamProse.tailLine == 0 {
			t.Fatalf("paragraph prefix was not retained: %+v", block.streamProse)
		}
	}
}

func BenchmarkStreamingGrowingCard(b *testing.B) {
	for _, kind := range []BlockType{BlockAssistant, BlockThinking} {
		for _, count := range []int{64, 256} {
			b.Run(fmt.Sprintf("%d/%d", kind, count), func(b *testing.B) {
				var builder strings.Builder
				snapshots := make([]string, count)
				for i := range snapshots {
					builder.WriteString("**Considering the options**\nA paragraph with **bold**, `inline code`, and ordinary text.\n\n")
					snapshots[i] = builder.String()
				}
				ApplyTheme(uitheme.Default())
				b.ReportAllocs()
				b.ResetTimer()
				for b.Loop() {
					block := &Block{Type: kind, Streaming: true}
					for _, content := range snapshots {
						block.Content = content
						block.InvalidateCache()
						block.Render(100, "")
					}
				}
			})
		}
	}
}
