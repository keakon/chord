package markdownrender

import (
	"errors"
	"strings"
	"testing"

	"github.com/keakon/glamour/v2"

	"github.com/keakon/chord/internal/tui/terminaltext"
	uitheme "github.com/keakon/chord/internal/tui/theme"
)

func TestRenderMarkdownContentSupportsAdjacentStrong(t *testing.T) {
	r := New(uitheme.Default())
	lines := r.Render("**a****b**", 40)
	joined := strings.Join(lines, "\n")
	plain := terminaltext.StripANSI(joined)
	if strings.Contains(plain, "****") || !strings.Contains(plain, "ab") {
		t.Fatalf("adjacent strong rendered incorrect text: %q", plain)
	}
	if strings.Count(joined, "\x1b[") < 2 {
		t.Fatalf("expected ANSI styling for both adjacent strong spans: %q", joined)
	}
}

func TestRenderMarkdownContentRepairsUnterminatedFence(t *testing.T) {
	r := New(uitheme.Default())
	lines := r.Render("## Sample\n\n```md\nbody\n", 40)
	joined := terminaltext.StripANSI(strings.Join(lines, "\n"))
	if !strings.Contains(joined, "Sample") {
		t.Fatalf("expected heading text, got %q", joined)
	}
	if !strings.Contains(joined, "body") {
		t.Fatalf("expected fenced content text, got %q", joined)
	}
}

func TestRenderMarkdownContentRecoversFromRendererPanic(t *testing.T) {
	r := New(uitheme.Default())
	originalRender := r.render
	r.render = func(_ *glamour.TermRenderer, _ string) (string, error) {
		panic("boom")
	}
	t.Cleanup(func() {
		r.render = originalRender
		r.reset()
	})

	lines := r.Render("## Summary\n\nbody", 40)
	joined := terminaltext.StripANSI(strings.Join(lines, "\n"))
	if !strings.Contains(joined, "Summary") || !strings.Contains(joined, "body") {
		t.Fatalf("expected plain markdown fallback content, got %q", joined)
	}

	if r.renderer != nil || r.width != 0 {
		t.Fatal("panic left a poisoned cached renderer")
	}

	r.render = func(_ *glamour.TermRenderer, _ string) (string, error) {
		return "ok", nil
	}
	lines = r.Render("ok", 40)
	if r.renderer == nil || r.width != 40 {
		t.Fatal("renderer was not rebuilt after panic")
	}
	if joined = terminaltext.StripANSI(strings.Join(lines, "\n")); joined != "ok" {
		t.Fatalf("renderer did not recover after panic fallback, got %q", joined)
	}
}

func TestRenderMarkdownContentFallsBackOnRendererError(t *testing.T) {
	r := New(uitheme.Default())
	originalRender := r.render
	r.render = func(_ *glamour.TermRenderer, _ string) (string, error) {
		return "", errors.New("render failed")
	}
	t.Cleanup(func() {
		r.render = originalRender
		r.reset()
	})

	lines := r.Render("## Summary\n\nbody", 40)
	joined := terminaltext.StripANSI(strings.Join(lines, "\n"))
	if !strings.Contains(joined, "Summary") || !strings.Contains(joined, "body") {
		t.Fatalf("expected plain markdown fallback content, got %q", joined)
	}
}

func TestRenderMarkdownContentFallbackHidesAdjacentStrongMarker(t *testing.T) {
	r := New(uitheme.Default())
	originalRender := r.render
	r.render = func(_ *glamour.TermRenderer, _ string) (string, error) {
		return "", errors.New("render failed")
	}
	t.Cleanup(func() {
		r.render = originalRender
		r.reset()
	})

	lines := r.Render("**a****b**", 40)
	joined := terminaltext.StripANSI(strings.Join(lines, "\n"))
	if strings.Contains(joined, "chord-adjacent-strong") {
		t.Fatalf("fallback leaked the internal marker: %q", joined)
	}
	if !strings.Contains(joined, "**a****b**") {
		t.Fatalf("fallback must show the original text, got %q", joined)
	}
}

func TestRendererReusesOnlyMatchingWidth(t *testing.T) {
	r := New(uitheme.Default())
	r.Render("sample", 40)
	first := r.renderer
	if first == nil || r.width != 40 {
		t.Fatal("renderer not initialized for requested width")
	}
	r.Render("other", 40)
	if r.renderer != first {
		t.Fatal("same-width render replaced cached renderer")
	}
	r.Render("sample", 20)
	if r.renderer == nil || r.renderer == first || r.width != 20 {
		t.Fatal("width change did not replace renderer")
	}
	r.Render("sample", 0)
	if r.width != 80 {
		t.Fatal("non-positive width did not use default")
	}
}

func TestRendererCapturesStyleSnapshot(t *testing.T) {
	palette := uitheme.Default()
	palette.ToolResultExpandedFg = "196"
	r := New(palette)
	captured := StyleConfig(palette)
	palette.ToolResultExpandedFg = "197"
	if *r.style.Text.Color != "196" || *captured.Text.Color != "196" {
		t.Fatal("palette mutation changed captured style")
	}
	*captured.Text.Color = "198"
	if *r.style.Text.Color != "196" {
		t.Fatal("separate style config mutated renderer")
	}
	if r.style.Text.BackgroundColor != nil || r.style.Document.BackgroundColor != nil {
		t.Fatal("body style must remain transparent")
	}
}
