package tui

import (
	"reflect"
	"strings"
	"testing"
)

// TestRenderThinkingStreamingIncrementalMatchesFreshStreaming drives a
// streaming thinking part through incremental flush updates (each flush
// appends a chunk at a blank-line boundary, mimicking the real opus-5 delta
// pattern) and asserts the accumulated output equals a fresh one-shot render
// of the same content through the same streaming code path. This is the
// correctness contract for the incremental cache: per-flush rendering must
// converge to exactly what a single render of the final content produces.
func TestRenderThinkingStreamingIncrementalMatchesFreshStreaming(t *testing.T) {
	ApplyTheme(DefaultTheme())
	chunks := []string{
		"## Analysis\n\n",
		"First paragraph with **bold** and `code`.\n\n",
		"Second paragraph\n- list item one\n- list item two\n\n",
		"Third paragraph with [link](https://example.invalid).\n\n",
		"```go\nfunc main() {}\n```\n\n",
		"Final tail paragraph without trailing blank.",
	}
	full := strings.Join(chunks, "")

	var content string
	block := &Block{Type: BlockThinking, Streaming: true}
	for _, ch := range chunks {
		content += ch
		block.Content = content
		block.renderThinking(70)
	}
	incremental := stripANSILines(block.renderThinking(70))

	fresh := stripANSILines((&Block{Type: BlockThinking, Streaming: true, Content: full}).renderThinking(70))

	if !reflect.DeepEqual(incremental, fresh) {
		t.Fatalf("incremental render differs from fresh streaming render:\n--- incremental ---\n%s\n--- fresh ---\n%s", strings.Join(incremental, "\n"), strings.Join(fresh, "\n"))
	}
	// The incremental cache must have been populated.
	if len(block.thinkingStreamSettled) != 1 {
		t.Fatalf("thinking stream settled cache parts = %d, want 1", len(block.thinkingStreamSettled))
	}
	if block.thinkingStreamSettled[0].styledUpTo <= 0 {
		t.Fatalf("expected incremental styling to have advanced past 0, got styledUpTo=%d", block.thinkingStreamSettled[0].styledUpTo)
	}
}

// TestRenderThinkingStreamingIncrementalMatchesFullRender asserts that for
// content without code fences the streaming settled+tail render is
// byte-identical to the one-shot full glamour render, and that the
// incremental accumulation reproduces it exactly.
func TestRenderThinkingStreamingIncrementalMatchesFullRender(t *testing.T) {
	ApplyTheme(DefaultTheme())
	chunks := []string{
		"## Analysis\n\n",
		"First paragraph with **bold** and `code`.\n\n",
		"Second paragraph\n- list item one\n- list item two\n\n",
		"Third paragraph with [link](https://example.invalid).\n\n",
		"Final paragraph settled before the tail.\n\n",
		"Tail paragraph without trailing blank.",
	}
	full := strings.Join(chunks, "")

	var content string
	block := &Block{Type: BlockThinking, Streaming: true}
	for _, ch := range chunks {
		content += ch
		block.Content = content
		_ = block.renderThinking(70)
	}
	incremental := block.renderThinking(70)
	incrementalPlain := strings.Join(stripANSILines(incremental), "\n")

	oneShot := (&Block{Type: BlockThinking, Content: full}).renderThinking(70)
	oneShotPlain := strings.Join(stripANSILines(oneShot), "\n")

	if incrementalPlain != oneShotPlain {
		t.Fatalf("incremental render differs from one-shot full render:\n--- incremental ---\n%s\n--- one-shot ---\n%s", incrementalPlain, oneShotPlain)
	}
	if len(incremental) != len(oneShot) {
		t.Fatalf("line count differs: incremental=%d one-shot=%d", len(incremental), len(oneShot))
	}
}

// TestRenderThinkingStreamingIncrementalMatchesFullRenderLargeParagraphs drives
// a long single-paragraph thinking stream (no blank lines, so the frontier
// stays at 0 and the whole content is tail) and verifies the full-tail path
// still matches the one-shot render.
func TestRenderThinkingStreamingIncrementalMatchesFullRenderLargeParagraphs(t *testing.T) {
	ApplyTheme(DefaultTheme())
	words := []string{}
	for i := range 200 {
		words = append(words, "word"+string(rune('a'+i%26))+string(rune('a'+(i/26)%26)))
	}
	full := strings.Join(words, " ") + "."
	var content string
	block := &Block{Type: BlockThinking, Streaming: true}
	for i := 0; i < len(full); i += 40 {
		end := min(i+40, len(full))
		content = full[:end]
		block.Content = content
		_ = block.renderThinking(70)
	}
	incremental := block.renderThinking(70)
	incrementalPlain := strings.Join(stripANSILines(incremental), "\n")
	oneShot := (&Block{Type: BlockThinking, Content: full}).renderThinking(70)
	oneShotPlain := strings.Join(stripANSILines(oneShot), "\n")
	if incrementalPlain != oneShotPlain {
		t.Fatalf("tail-only incremental render differs from one-shot:\n--- incremental ---\n%s\n--- one-shot ---\n%s", incrementalPlain, oneShotPlain)
	}
}

// TestRenderThinkingIncrementalStyledMatchesFullStyles verifies the incremental
// styled-lines cache (styledUpTo) produces byte-identical output to styling the
// full settled prefix in one pass, including paragraph title/content transitions.
func TestRenderThinkingIncrementalStyledMatchesFullStyles(t *testing.T) {
	ApplyTheme(DefaultTheme())
	part := "## Plan\n\n- stable item\n\nstill arriving"

	fullLines := strings.Split(part, "\n")
	fullStyled := styleStreamingThinkingSettledLines(fullLines)

	var incremental []string
	styledUpTo := 0
	paraStart := true
	for i := 1; i <= len(fullLines); i++ {
		incremental, paraStart = styleStreamingThinkingSettledLinesIncremental(fullLines, styledUpTo, i, paraStart, incremental)
		styledUpTo = i
	}
	if styledUpTo != len(fullLines) {
		t.Fatalf("styledUpTo = %d, want %d", styledUpTo, len(fullLines))
	}
	if !reflect.DeepEqual(incremental, fullStyled) {
		t.Fatalf("incremental styling differs from full styling:\n--- incremental ---\n%#v\n--- full ---\n%#v", incremental, fullStyled)
	}
}
