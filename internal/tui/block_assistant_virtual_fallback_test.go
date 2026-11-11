package tui

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/tui/terminaltext"
	uitheme "github.com/keakon/chord/internal/tui/theme"
)

func TestAssistantVirtualWrapParity(t *testing.T) {
	ApplyTheme(uitheme.Default())
	for name, unit := range map[string]string{
		"hyphens": "This is a well-known, state-of-the-art approach used in real-world systems for long-running tasks and follow-up work.",
		"spaces":  "First sentence ends here.  Second sentence follows two spaces as many writers type it.  Third one too.",
		"nbsp":    "Words\u00a0joined\u00a0by\u00a0non\u00a0breaking\u00a0spaces cross multiple lines at narrow widths.",
	} {
		t.Run(name, func(t *testing.T) {
			var content strings.Builder
			for i := range 300 {
				if i%9 == 0 {
					fmt.Fprintf(&content, "## Section %d heading\n\n", i)
				} else {
					fmt.Fprintf(&content, "Paragraph %d. %s\n\n", i, unit)
				}
			}
			for width := 30; width <= 120; width++ {
				b := &Block{Type: BlockAssistant, Content: content.String()}
				full := (&Block{Type: BlockAssistant, Content: b.Content}).renderAssistant(width)
				if got := b.LineCount(width); got != len(full) {
					t.Fatalf("width %d: predicted %d lines, want %d", width, got, len(full))
				}
				if got := b.RenderRange(width, "", 0, len(full)); !slices.Equal(got, full) {
					t.Fatalf("width %d: virtual lines differ from full render", width)
				}
				if b.virtualAssistant == nil || !b.virtualAssistant.valid {
					t.Fatalf("width %d: plain text unexpectedly fell back", width)
				}
			}
		})
	}
}

func TestAssistantVirtualRejectsTransformedMarkdown(t *testing.T) {
	ApplyTheme(uitheme.Default())
	for name, unit := range map[string]string{
		"definition":       "A term with ordinary words\n: A definition with enough ordinary words to wrap over several lines at narrow terminal widths.",
		"loose definition": "A term with ordinary words\n\n: A definition with enough ordinary words to wrap over several lines at narrow terminal widths.",
		"www paragraph":    "Visit www.example.invalid/long/path/for/details with enough ordinary words in this paragraph",
		"www heading":      "## Visit www.example.invalid/long/path/for/details with enough ordinary words in this heading",
	} {
		t.Run(name, func(t *testing.T) {
			content := strings.Repeat(unit+"\n\n", 400)
			for _, width := range []int{40, 100} {
				b := &Block{Type: BlockAssistant, Content: content}
				if _, ok := b.assistantVirtualLayout(width); ok {
					t.Fatal("transformed Markdown enabled virtual rendering")
				}
				full := (&Block{Type: BlockAssistant, Content: content}).renderAssistant(width)
				if got := b.Render(width, ""); !slices.Equal(got, full) {
					t.Fatal("fallback differs from full render")
				}
			}
		})
	}
}

func TestAssistantVirtualUnicodeWhitespaceParity(t *testing.T) {
	ApplyTheme(uitheme.Default())
	content := virtualAssistantTestContent(220)
	paragraphs := strings.Split(strings.TrimSuffix(content, "\n\n"), "\n\n")
	boundary := assistantVirtualChunkSegments
	head := strings.Join(paragraphs[:boundary], "\n\n")
	tail := strings.Join(paragraphs[boundary:], "\n\n")
	for name, space := range map[string]string{
		"nbsp":              "\u00a0",
		"em space":          "\u2003",
		"ideographic space": "\u3000",
	} {
		for position, source := range map[string]string{
			"leading paragraph":       space + "\n\n" + content,
			"paragraph at chunk edge": head + "\n\n" + space + "\n\n" + tail,
			"line at chunk edge":      head + "\n" + space + "\n\n" + tail,
		} {
			for _, width := range []int{30, 65} {
				t.Run(fmt.Sprintf("%s/%s/%d", name, position, width), func(t *testing.T) {
					full := (&Block{ID: 1, Type: BlockAssistant, Content: source}).renderAssistant(width)
					b := &Block{ID: 1, Type: BlockAssistant, Content: source}
					if count := b.LineCount(width); count != len(full) {
						t.Errorf("line count = %d, want %d", count, len(full))
					}
					if got := b.RenderRange(width, "", 0, b.LineCount(width)); !slices.Equal(got, full) {
						t.Error("rendered lines differ from full Markdown render")
					}
					v := NewViewport(width, 18)
					t.Cleanup(func() { _ = v.Close() })
					v.AppendBlock(&Block{ID: 1, Type: BlockAssistant, Content: source})
					v.AppendBlock(&Block{ID: 2, Type: BlockUser, Content: "Unique search target"})
					if offset, ok := v.LineOffsetForBlockID(2); !ok || offset != len(full) {
						t.Errorf("following card offset = %d, found=%v, want %d", offset, ok, len(full))
					}
					matches := FindMatchesAtWidth(v.blocks, "Unique search target", width)
					if len(matches) != 1 || matches[0].BlockID != 2 || matches[0].LineOffset != len(full) {
						t.Errorf("search matches = %+v, want following card at line %d", matches, len(full))
					}
				})
			}
		}
	}
}

func TestAssistantVirtualASCIIBlankLinesKeepVirtualLayout(t *testing.T) {
	ApplyTheme(uitheme.Default())
	content := strings.ReplaceAll(virtualAssistantTestContent(220), "\n\n", "\n \t \n")
	b := &Block{Type: BlockAssistant, Content: content}
	full := (&Block{Type: BlockAssistant, Content: content}).renderAssistant(40)
	if count := b.LineCount(40); count != len(full) {
		t.Fatalf("line count = %d, want %d", count, len(full))
	}
	if got := b.RenderRange(40, "", 0, len(full)); !slices.Equal(got, full) {
		t.Fatal("ASCII blank lines changed the rendered content")
	}
	if b.virtualAssistant == nil || !b.virtualAssistant.valid {
		t.Fatal("ASCII blank lines unexpectedly disabled virtual rendering")
	}
}

// Change one chunk's prediction while leaving a valid monotonic layout.
// This exercises both directions of future renderer/predictor drift.
func driftAssistantVirtualLayout(t *testing.T, b *Block, width, delta int) {
	t.Helper()
	layout, ok := b.assistantVirtualLayout(width)
	if !ok {
		t.Fatal("expected virtual layout")
	}
	layout.segments[len(layout.segments)-1].span += delta
	layout.chunks[len(layout.chunks)-1].endLine += delta
	layout.totalBody += delta
	layout.totalCard += delta
	b.lineCacheWidth = 0
}

func TestAssistantVirtualFallbackRetiresPredictedCount(t *testing.T) {
	ApplyTheme(uitheme.Default())
	const width = 40
	content := virtualAssistantTestContent(220)
	full := (&Block{Type: BlockAssistant, Content: content}).renderAssistant(width)
	for _, delta := range []int{-1, 1} {
		for _, entry := range []string{"render", "range", "iterate"} {
			t.Run(fmt.Sprintf("%s/%d", entry, delta), func(t *testing.T) {
				b := &Block{Type: BlockAssistant, Content: content}
				driftAssistantVirtualLayout(t, b, width, delta)
				predicted := b.LineCount(width)
				var got []string
				switch entry {
				case "render":
					got = b.Render(width, "")
				case "range":
					_ = b.RenderRange(width, "", predicted-30, predicted)
					got = b.RenderRange(width, "", 0, b.LineCount(width))
				case "iterate":
					for _, line := range b.renderedLineRange(width, 0, predicted) {
						got = append(got, line)
					}
				}
				if !slices.Equal(got, full) || b.LineCount(width) != len(full) {
					t.Fatalf("fallback returned %d lines, count %d, want %d", len(got), b.LineCount(width), len(full))
				}
				if b.virtualAssistant.valid {
					t.Fatal("mismatched prediction remained valid")
				}
			})
		}
	}
}

func TestViewportAssistantVirtualFallbackRepairsPositions(t *testing.T) {
	ApplyTheme(uitheme.Default())
	const width, height = 40, 18
	content := virtualAssistantTestContent(220)
	for _, delta := range []int{-1, 1} {
		for _, sticky := range []bool{false, true} {
			for _, outsideRender := range []bool{false, true} {
				t.Run(fmt.Sprintf("%d/sticky=%v/external=%v", delta, sticky, outsideRender), func(t *testing.T) {
					v, want := NewViewport(width, height), NewViewport(width, height)
					t.Cleanup(func() { _ = v.Close(); _ = want.Close() })
					v.SetSticky(sticky)
					want.SetSticky(sticky)
					b := &Block{ID: 1, Type: BlockAssistant, Content: content}
					driftAssistantVirtualLayout(t, b, width, delta)
					v.AppendBlock(b)
					fullBlock := &Block{ID: 1, Type: BlockAssistant, Content: content}
					fullBlock.cacheRenderedLines(width, fullBlock.renderAssistant(width))
					want.AppendBlock(fullBlock)
					if !sticky {
						v.offset = want.TotalLines() - height
						want.offset = v.offset
					}
					if outsideRender {
						_ = b.Render(width, "")
					}
					if got, expected := v.Render("", nil, -1, 0, ""), want.Render("", nil, -1, 0, ""); got != expected {
						t.Fatal("fallback frame differs from full render")
					}
					if v.TotalLines() != want.TotalLines() || v.offset != want.offset || v.lastBlockSpan != want.lastBlockSpan ||
						!slices.Equal(v.blockStarts(), want.blockStarts()) || !slices.Equal(v.blockSpansCache, want.blockSpansCache) {
						t.Fatal("viewport retained predicted positions")
					}
					v.SetSize(width, height)
					want.SetSize(width, height)
					if got, expected := v.Render("", nil, -1, 0, ""), want.Render("", nil, -1, 0, ""); got != expected {
						t.Fatal("resized fallback frame differs from full render")
					}
				})
			}
		}
	}
}

func TestAssistantVirtualFallbackSnapshotIsDetached(t *testing.T) {
	ApplyTheme(uitheme.Default())
	b := &Block{Type: BlockAssistant, Content: virtualAssistantTestContent(220)}
	b.renderLineCountChanged = func(int) { t.Fatal("snapshot notified the source viewport") }
	clone := cloneBlockForDeferredSource(b)
	driftAssistantVirtualLayout(t, clone, 40, -1)
	_ = clone.Render(40, "")
	if !strings.Contains(terminaltext.StripANSI(strings.Join(clone.Render(40, ""), "\n")), "Paragraph 219") {
		t.Fatal("snapshot fallback lost the final paragraph")
	}
}

func TestAssistantVirtualFallbackSearchAndSelection(t *testing.T) {
	ApplyTheme(uitheme.Default())
	const width = 40
	content := virtualAssistantTestContent(220)
	for _, delta := range []int{-1, 1} {
		for _, entry := range []string{"search", "selection"} {
			t.Run(fmt.Sprintf("%s/%d", entry, delta), func(t *testing.T) {
				v, want := NewViewport(width, 18), NewViewport(width, 18)
				t.Cleanup(func() { _ = v.Close(); _ = want.Close() })
				b := &Block{ID: 1, Type: BlockAssistant, Content: content}
				driftAssistantVirtualLayout(t, b, width, delta)
				v.AppendBlock(b)
				v.AppendBlock(&Block{ID: 2, Type: BlockUser, Content: "Paragraph 219 is the final paragraph."})
				full := &Block{ID: 1, Type: BlockAssistant, Content: content}
				full.cacheRenderedLines(width, full.renderAssistant(width))
				want.AppendBlock(full)
				want.AppendBlock(&Block{ID: 2, Type: BlockUser, Content: "Paragraph 219 is the final paragraph."})
				switch entry {
				case "search":
					got := FindMatchesAtWidth(v.blocks, "Paragraph 219", width)
					expected := FindMatchesAtWidth(want.blocks, "Paragraph 219", width)
					if len(got) != 2 || !slices.Equal(got, expected) {
						t.Fatalf("search positions = %+v, want %+v", got, expected)
					}
				case "selection":
					sel := SelectionRange{StartBlockID: 1, EndBlockID: 2, EndLine: 20, EndCol: width}
					if got, expected := v.ExtractSelectionText(sel), want.ExtractSelectionText(sel); got != expected {
						for i := range min(len(got), len(expected)) {
							if got[i] != expected[i] {
								t.Fatalf("selection differs at %d: got %q, want %q", i, got[max(0, i-30):min(i+80, len(got))], expected[max(0, i-30):min(i+80, len(expected))])
							}
						}
						t.Fatalf("selection lengths differ: got %d, want %d", len(got), len(expected))
					}
				}
				if v.TotalLines() != want.TotalLines() || !slices.Equal(v.blockStarts(), want.blockStarts()) {
					t.Fatal("inspection retained predicted viewport positions")
				}
			})
		}
	}
}
