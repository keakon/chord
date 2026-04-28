package tui

import (
	"testing"

	"github.com/mattn/go-runewidth"

	"github.com/keakon/chord/internal/tui/modelref"
)

func TestModelRefPackageEnsureHelpers(t *testing.T) {
	if got := modelref.EnsureRefShowsProvider("gpt-5.5@xhigh", "sample/gpt-5.5@xhigh"); got != "sample/gpt-5.5@xhigh" {
		t.Fatalf("EnsureRefShowsProvider = %q", got)
	}
	if got := modelref.EnsureRefShowsVariant("sample/gpt-5.5", "xhigh"); got != "sample/gpt-5.5@xhigh" {
		t.Fatalf("EnsureRefShowsVariant = %q", got)
	}
	if got := modelref.EnsureRefShowsMatchingVariant("sample/gpt-5.5", "sample/gpt-5.5@xhigh", "xhigh"); got != "sample/gpt-5.5@xhigh" {
		t.Fatalf("EnsureRefShowsMatchingVariant same base = %q", got)
	}
	if got := modelref.EnsureRefShowsMatchingVariant("sample/glm-5.1", "sample/gpt-5.5@xhigh", "xhigh"); got != "sample/glm-5.1" {
		t.Fatalf("EnsureRefShowsMatchingVariant fallback leak = %q", got)
	}
	// When runningRef already includes variant, it should be returned as-is
	// (this is the post-fix path where RunningModelRef carries the variant).
	if got := modelref.EnsureRefShowsMatchingVariant("sample/gpt-5.5@xhigh", "sample/gpt-5.5@xhigh", "xhigh"); got != "sample/gpt-5.5@xhigh" {
		t.Fatalf("EnsureRefShowsMatchingVariant already has variant = %q", got)
	}
	// When runningRef has variant but it differs from activeVariant, keep runningRef's variant.
	if got := modelref.EnsureRefShowsMatchingVariant("sample/gpt-5.5@low", "sample/gpt-5.5@high", "high"); got != "sample/gpt-5.5@low" {
		t.Fatalf("EnsureRefShowsMatchingVariant mismatched variant = %q", got)
	}
}

func TestTruncateRunningModelRefUnchangedWhenFits(t *testing.T) {
	ref := "sample/gpt-5.5@xhigh"
	got := modelref.TruncateRunningModelRef(ref, runewidth.StringWidth(ref)+10)
	if got != ref {
		t.Fatalf("got %q, want %q", got, ref)
	}
}

func TestTruncateRunningModelRefOpenRouterCascade(t *testing.T) {
	ref := "openrouter/anthropic/claude-opus-4.6@high"
	wFull := runewidth.StringWidth(ref)
	wDropAnthropic := runewidth.StringWidth("openrouter/claude-opus-4.6@high")
	wDropVariant := runewidth.StringWidth("openrouter/claude-opus-4.6")
	wBareModel := runewidth.StringWidth("claude-opus-4.6")

	if got := modelref.TruncateRunningModelRef(ref, wFull); got != ref {
		t.Fatalf("full budget: got %q, want %q", got, ref)
	}
	if got := modelref.TruncateRunningModelRef(ref, wDropAnthropic); got != "openrouter/claude-opus-4.6@high" {
		t.Fatalf("drop anthropic segment: got %q", got)
	}
	if got := modelref.TruncateRunningModelRef(ref, wDropVariant); got != "openrouter/claude-opus-4.6" {
		t.Fatalf("drop variant: got %q", got)
	}
	if got := modelref.TruncateRunningModelRef(ref, wBareModel); got != "claude-opus-4.6" {
		t.Fatalf("drop provider: got %q", got)
	}
}

func TestTruncateRunningModelRefStripsMultipleModelPathSegments(t *testing.T) {
	ref := "pv/a/b/c@x"
	// One segment at a time: c@x -> b/c@x -> ... actually after first strip model becomes b/c, variant x
	// pv/a/b/c@x -> provider pv, model a/b/c, variant x
	// strip -> b/c@x join pv/b/c@x
	want1 := "pv/b/c@x"
	max1 := runewidth.StringWidth(want1)
	if got := modelref.TruncateRunningModelRef(ref, max1); got != want1 {
		t.Fatalf("first strip: got %q, want %q", got, want1)
	}
}

func TestTruncateRunningModelRefNoProviderSlash(t *testing.T) {
	ref := "claude-opus-4.6@high"
	w := runewidth.StringWidth("claude-opus-4.6")
	if got := modelref.TruncateRunningModelRef(ref, w); got != "claude-opus-4.6" {
		t.Fatalf("got %q, want claude-opus-4.6", got)
	}
}

func TestTruncateRunningModelRefSingleSegmentDropsVariantBeforeProvider(t *testing.T) {
	ref := "sample/gpt-5.5@xhigh"
	want := "sample/gpt-5.5"
	max := runewidth.StringWidth(want)
	if got := modelref.TruncateRunningModelRef(ref, max); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// FormatRunningModelRefForDisplay is the end-to-end path used by both the
// sidebar info panel and the narrow status-bar model pill. When the LLM
// client now returns RunningModelRef with @variant baked in, the display
// pipeline should still work correctly — including truncation that drops
// variant and provider when space is tight.
func TestFormatRunningModelRefForDisplayWithBuiltinVariant(t *testing.T) {
	// Full display: variant comes from runningRef itself (post-fix path).
	if got := modelref.FormatRunningModelRefForDisplay("openai/gpt-5.5@high", "openai/gpt-5.5@high", "high", 80); got != "openai/gpt-5.5@high" {
		t.Fatalf("full display = %q, want openai/gpt-5.5@high", got)
	}
	// Narrow: drop variant first, keep provider/model.
	wantNoVariant := "openai/gpt-5.5"
	if got := modelref.FormatRunningModelRefForDisplay("openai/gpt-5.5@high", "openai/gpt-5.5@high", "high", runewidth.StringWidth(wantNoVariant)); got != wantNoVariant {
		t.Fatalf("narrow (drop variant) = %q, want %q", got, wantNoVariant)
	}
	// Very narrow: drop provider too, keep bare model.
	wantBare := "gpt-5.5"
	if got := modelref.FormatRunningModelRefForDisplay("openai/gpt-5.5@high", "openai/gpt-5.5@high", "high", runewidth.StringWidth(wantBare)); got != wantBare {
		t.Fatalf("very narrow (drop provider) = %q, want %q", got, wantBare)
	}
}
