package tui

import (
	"strings"
	"testing"
	"time"
)

func TestGradientStopsBasic(t *testing.T) {
	stops := []ColorStop{
		{Pos: 0.0, Color: "#000000"},
		{Pos: 1.0, Color: "#ffffff"},
	}
	// At 0%, should be black
	c0 := GradientStops(stops, 0)
	if c0 != "#000000" {
		t.Fatalf("GradientStops(0) = %q, want #000000", c0)
	}
	// At 100%, should be white
	c1 := GradientStops(stops, 1)
	if c1 != "#ffffff" {
		t.Fatalf("GradientStops(1) = %q, want #ffffff", c1)
	}
	// At 50%, should be approximately gray
	c50 := GradientStops(stops, 0.5)
	if !strings.HasPrefix(c50, "#") {
		t.Fatalf("GradientStops(0.5) = %q, expected hex color", c50)
	}
}

func TestGradientStopsMultiple(t *testing.T) {
	stops := NeonDarkStops
	// Should not panic for any position
	for i := 0; i <= 10; i++ {
		p := float64(i) / 10
		c := GradientStops(stops, p)
		if !strings.HasPrefix(c, "#") || len(c) != 7 {
			t.Fatalf("GradientStops(%f) = %q, expected #RRGGBB", p, c)
		}
	}
}

func TestGradientStopsEmpty(t *testing.T) {
	c := GradientStops(nil, 0.5)
	if c != "#808080" {
		t.Fatalf("GradientStops(nil, 0.5) = %q, want #808080", c)
	}
}

func TestGradientStopsSingle(t *testing.T) {
	c := GradientStops([]ColorStop{{Pos: 0, Color: "#ff0000"}}, 0.5)
	if c != "#ff0000" {
		t.Fatalf("GradientStops(single, 0.5) = %q, want #ff0000", c)
	}
}

func TestBuildNeonRamp(t *testing.T) {
	ramp := BuildNeonRamp(20, NeonDarkStops)
	if len(ramp) != 20 {
		t.Fatalf("BuildNeonRamp(20) len = %d, want 20", len(ramp))
	}
	for i, c := range ramp {
		if !strings.HasPrefix(c, "#") || len(c) != 7 {
			t.Fatalf("ramp[%d] = %q, expected #RRGGBB", i, c)
		}
	}
}

func TestBuildNeonRampZero(t *testing.T) {
	ramp := BuildNeonRamp(0, NeonDarkStops)
	if ramp != nil {
		t.Fatalf("BuildNeonRamp(0) should return nil, got %v", ramp)
	}
}

func TestShiftRamp(t *testing.T) {
	ramp := BuildNeonRamp(10, NeonDarkStops)
	shifted := ShiftRamp(ramp, 3, 10)
	if len(shifted) != 10 {
		t.Fatalf("ShiftRamp len = %d, want 10", len(shifted))
	}
	// First element of shifted should equal ramp[3]
	if shifted[0] != ramp[3] {
		t.Fatalf("ShiftRamp[0] = %q, want ramp[3] = %q", shifted[0], ramp[3])
	}
}

func TestShiftRampWraps(t *testing.T) {
	ramp := BuildNeonRamp(5, NeonDarkStops)
	shifted := ShiftRamp(ramp, 4, 5)
	// shifted[0] = ramp[4], shifted[1] = ramp[0] (wrapped)
	if shifted[0] != ramp[4] {
		t.Fatalf("ShiftRamp[0] = %q, want ramp[4] = %q", shifted[0], ramp[4])
	}
	if shifted[1] != ramp[0] {
		t.Fatalf("ShiftRamp[1] = %q, want ramp[0] = %q", shifted[1], ramp[0])
	}
}

func TestShiftRampEmpty(t *testing.T) {
	if got := ShiftRamp(nil, 0, 10); got != nil {
		t.Fatalf("ShiftRamp(nil) should return nil, got %v", got)
	}
}

func TestAnimationPhase(t *testing.T) {
	phase := AnimationPhase(time.Second, 10)
	if phase < 0 || phase >= 10 {
		t.Fatalf("AnimationPhase = %d, want [0,10)", phase)
	}
}

func TestAnimationPhaseZero(t *testing.T) {
	if got := AnimationPhase(0, 10); got != 0 {
		t.Fatalf("AnimationPhase(0, 10) = %d, want 0", got)
	}
	if got := AnimationPhase(time.Second, 0); got != 0 {
		t.Fatalf("AnimationPhase(1s, 0) = %d, want 0", got)
	}
}

func TestAnimationFloat(t *testing.T) {
	f := AnimationFloat(time.Second)
	if f < 0 || f >= 1 {
		t.Fatalf("AnimationFloat = %f, want [0,1)", f)
	}
}

func TestAnimationFloatZero(t *testing.T) {
	if got := AnimationFloat(0); got != 0 {
		t.Fatalf("AnimationFloat(0) = %f, want 0", got)
	}
}

func TestApplyGlowBand(t *testing.T) {
	ramp := BuildNeonRamp(20, NeonDarkStops)
	original := make(NeonRamp, len(ramp))
	copy(original, ramp)

	result := ApplyGlowBand(ramp, 10, 5, 0.8, "#ffffff")
	if len(result) != len(ramp) {
		t.Fatalf("ApplyGlowBand changed length: %d -> %d", len(ramp), len(result))
	}
	// Center should be modified
	if result[10] == original[10] {
		t.Fatal("ApplyGlowBand should modify center position")
	}
	// Original should be unmodified
	if ramp[10] != original[10] {
		t.Fatal("ApplyGlowBand should not modify input ramp")
	}
	// Far-away positions should be unchanged
	if result[0] != original[0] {
		t.Fatalf("ApplyGlowBand[0] = %q, want unchanged %q", result[0], original[0])
	}
}

func TestApplyGlowBandNoOp(t *testing.T) {
	ramp := BuildNeonRamp(10, NeonDarkStops)
	result := ApplyGlowBand(ramp, 5, 0, 0.5, "#ffffff")
	for i := range ramp {
		if result[i] != ramp[i] {
			t.Fatalf("ApplyGlowBand(radius=0) should be no-op, pos %d differs", i)
		}
	}
}

func TestApplyHotspot(t *testing.T) {
	ramp := BuildNeonRamp(20, NeonDarkStops)
	original := make(NeonRamp, len(ramp))
	copy(original, ramp)

	result := ApplyHotspot(ramp, 10, 4, "#ffffff")
	if len(result) != len(ramp) {
		t.Fatalf("ApplyHotspot changed length: %d -> %d", len(ramp), len(result))
	}
	// Center should be modified
	if result[10] == original[10] {
		t.Fatal("ApplyHotspot should modify center position")
	}
}

func TestNeonAccentColor(t *testing.T) {
	c := NeonAccentColor(time.Second)
	if !strings.HasPrefix(c, "#") || len(c) != 7 {
		t.Fatalf("NeonAccentColor = %q, expected #RRGGBB", c)
	}
}

func TestCachedNeonRamp(t *testing.T) {
	ramp1 := cachedNeonRamp(50)
	ramp2 := cachedNeonRamp(50)
	if len(ramp1) != len(ramp2) {
		t.Fatalf("cached ramp lengths differ: %d vs %d", len(ramp1), len(ramp2))
	}
	// Should be the same slice (pointer equality for cached)
	for i := range ramp1 {
		if ramp1[i] != ramp2[i] {
			t.Fatalf("cached ramp[%d] differs", i)
		}
	}
}

func TestCachedNeonRampDifferentWidths(t *testing.T) {
	ramp20 := cachedNeonRamp(20)
	ramp40 := cachedNeonRamp(40)
	if len(ramp20) != 40 {
		t.Fatalf("cachedNeonRamp(20) len = %d, want 40", len(ramp20))
	}
	if len(ramp40) != 80 {
		t.Fatalf("cachedNeonRamp(40) len = %d, want 80", len(ramp40))
	}
	if len(ramp20) == len(ramp40) {
		t.Fatal("different widths should not reuse identical ramp sizes")
	}
}

func TestRenderNeonSeparatorNonEmpty(t *testing.T) {
	got := renderNeonSeparator(24)
	plain := stripANSI(got)
	want := strings.Repeat(SectionSeparator, 24)
	if plain != want {
		t.Fatalf("neon separator plain = %q, want %q", plain, want)
	}
	// Should contain ANSI styling
	if !strings.Contains(got, "38;2;") && !strings.Contains(got, "38;5;") {
		t.Fatalf("neon separator should contain ANSI color codes, got %q", got)
	}
}

func TestRenderNeonSeparatorWidthStable(t *testing.T) {
	for _, w := range []int{10, 40, 80, 160} {
		got := renderNeonSeparator(w)
		plain := stripANSI(got)
		want := strings.Repeat(SectionSeparator, w)
		if plain != want {
			t.Fatalf("neon separator width=%d: plain len=%d, want=%d", w, len(plain), len(want))
		}
	}
}

func TestNormalizeSeparatorVariant(t *testing.T) {
	cases := map[string]string{
		"":        "neon",
		"neon":    "neon",
		"dual":    "dual",
		"comet":   "comet",
		"pulse":   "pulse",
		"unknown": "neon",
	}
	for in, want := range cases {
		if got := normalizeSeparatorVariant(in); got != want {
			t.Fatalf("normalizeSeparatorVariant(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRenderAnimatedSeparatorUnknownVariantFallsBackToNeon(t *testing.T) {
	theme := DefaultTheme()
	unknown := renderAnimatedSeparatorVariant(24, theme, "unknown", true, true)
	neon := renderAnimatedSeparatorVariant(24, theme, "neon", true, true)
	if stripANSI(unknown) != stripANSI(neon) {
		t.Fatal("unknown separator variant should fall back to neon plain output")
	}
}
