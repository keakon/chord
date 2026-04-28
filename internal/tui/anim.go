package tui

import (
	"fmt"
	"math"
	"strconv"
	"time"
)

// ---------------------------------------------------------------------------
// Basic gradient helpers (existing)
// ---------------------------------------------------------------------------

// Gradient returns a color spec string from a linear gradient ramp.
// start and end are color spec strings (e.g. "#FFFFFF" or "236"); result is a hex string.
func Gradient(start, end string, percent float64) string {
	if percent < 0 {
		percent = 0
	}
	if percent > 1 {
		percent = 1
	}

	sr, sg, sb, _ := parseColor(start)
	er, eg, eb, _ := parseColor(end)

	r := float64(sr) + percent*(float64(er)-float64(sr))
	g := float64(sg) + percent*(float64(eg)-float64(sg))
	b := float64(sb) + percent*(float64(eb)-float64(sb))

	return fmt.Sprintf("#%02x%02x%02x", int(r), int(g), int(b))
}

// ansi16Colors maps the standard 16 ANSI colors to RGB.
var ansi16Colors = [16][3]uint8{
	{0, 0, 0},       // 0 black
	{128, 0, 0},     // 1 red
	{0, 128, 0},     // 2 green
	{128, 128, 0},   // 3 yellow
	{0, 0, 128},     // 4 blue
	{128, 0, 128},   // 5 magenta
	{0, 128, 128},   // 6 cyan
	{192, 192, 192}, // 7 white
	{128, 128, 128}, // 8 bright black
	{255, 0, 0},     // 9 bright red
	{0, 255, 0},     // 10 bright green
	{255, 255, 0},   // 11 bright yellow
	{0, 0, 255},     // 12 bright blue
	{255, 0, 255},   // 13 bright magenta
	{0, 255, 255},   // 14 bright cyan
	{255, 255, 255}, // 15 bright white
}

// parseColor extracts RGB from a color spec string.
// Supports "#RRGGBB" hex and ANSI 256-color indices ("63", "205", etc.).
func parseColor(c string) (r, g, b, a uint32) {
	if len(c) == 7 && c[0] == '#' {
		fmt.Sscanf(c[1:], "%02x%02x%02x", &r, &g, &b)
		return r, g, b, 255
	}
	if n, err := strconv.Atoi(c); err == nil && n >= 0 && n <= 255 {
		switch {
		case n < 16:
			return uint32(ansi16Colors[n][0]), uint32(ansi16Colors[n][1]), uint32(ansi16Colors[n][2]), 255
		case n < 232:
			// 6×6×6 color cube: each component 0-5 maps to 0,51,102,153,204,255
			idx := n - 16
			return uint32((idx / 36) * 51), uint32(((idx / 6) % 6) * 51), uint32((idx % 6) * 51), 255
		default:
			// Grayscale ramp: 24 shades from 8 to 238
			gray := uint32((n-232)*10 + 8)
			return gray, gray, gray, 255
		}
	}
	return 128, 128, 128, 255
}

// Pulse returns a value between 0 and 1 that oscillates over time.
func Pulse(duration time.Duration) float64 {
	ms := time.Now().UnixNano() / int64(time.Millisecond)
	period := int64(duration / time.Millisecond)
	if period == 0 {
		return 1.0
	}
	val := math.Sin(2 * math.Pi * float64(ms%period) / float64(period))
	return (val + 1) / 2
}

// SineWave returns a shifted value for staggered animations.
func SineWave(offset, period, amplitude float64) float64 {
	ms := float64(time.Now().UnixNano() / int64(time.Millisecond))
	return math.Sin((ms+offset)/period) * amplitude
}

// ---------------------------------------------------------------------------
// Multi-stop gradient ramp (inspired by crush)
// ---------------------------------------------------------------------------

// ColorStop defines a position (0..1) and color for multi-stop gradients.
type ColorStop struct {
	Pos   float64
	Color string // hex "#RRGGBB" or ANSI "63"
}

// NeonRamp is a pre-computed array of hex color strings for a given width.
type NeonRamp []string

// GradientStops interpolates among multiple color stops.
// percent is in [0,1]. Returns a hex color string.
func GradientStops(stops []ColorStop, percent float64) string {
	if len(stops) == 0 {
		return "#808080"
	}
	if percent <= stops[0].Pos || len(stops) == 1 {
		return colorToHex(stops[0].Color)
	}
	if percent >= stops[len(stops)-1].Pos {
		return colorToHex(stops[len(stops)-1].Color)
	}
	// Find the two bounding stops.
	for i := 1; i < len(stops); i++ {
		if percent <= stops[i].Pos {
			span := stops[i].Pos - stops[i-1].Pos
			if span <= 0 {
				return colorToHex(stops[i].Color)
			}
			t := (percent - stops[i-1].Pos) / span
			return Gradient(stops[i-1].Color, stops[i].Color, t)
		}
	}
	return colorToHex(stops[len(stops)-1].Color)
}

// colorToHex normalises a color spec to hex, ensuring consistent format.
func colorToHex(c string) string {
	r, g, b, _ := parseColor(c)
	return fmt.Sprintf("#%02x%02x%02x", r, g, b)
}

// BuildNeonRamp generates a static color ramp of the given width from stops.
// Each element is a hex color string.
// The provided stops must be sorted by Pos ascending and stay within [0,1].
func BuildNeonRamp(width int, stops []ColorStop) NeonRamp {
	total := width
	if total <= 0 {
		return nil
	}
	if total == 1 {
		return NeonRamp{GradientStops(stops, 0)}
	}
	ramp := make(NeonRamp, total)
	for i := range ramp {
		t := float64(i) / float64(total-1)
		ramp[i] = GradientStops(stops, t)
	}
	return ramp
}

// ShiftRamp returns a window of `width` colors from the ramp, offset by `offset`.
// If ramp is shorter than needed, it wraps around.
func ShiftRamp(ramp NeonRamp, offset, width int) NeonRamp {
	n := len(ramp)
	if n == 0 || width <= 0 {
		return nil
	}
	out := make(NeonRamp, width)
	for i := range out {
		idx := (i + offset) % n
		if idx < 0 {
			idx += n
		}
		out[i] = ramp[idx]
	}
	return out
}

// ---------------------------------------------------------------------------
// Unified phase helpers
// ---------------------------------------------------------------------------

// AnimationPhase returns an integer phase index cycling through [0, cycle)
// with the given period.
func AnimationPhase(period time.Duration, cycle int) int {
	if cycle <= 0 || period <= 0 {
		return 0
	}
	ms := time.Now().UnixNano() / int64(time.Millisecond)
	periodMs := int64(period / time.Millisecond)
	return int(ms % periodMs * int64(cycle) / periodMs)
}

// AnimationFloat returns a float64 in [0,1) that cycles with the given period.
func AnimationFloat(period time.Duration) float64 {
	if period <= 0 {
		return 0
	}
	ms := time.Now().UnixNano() / int64(time.Millisecond)
	periodMs := int64(period / time.Millisecond)
	return float64(ms%periodMs) / float64(periodMs)
}

// ---------------------------------------------------------------------------
// Glow / Hotspot helpers
// ---------------------------------------------------------------------------

// ApplyGlowBand modifies a ramp in-place by blending a glow toward hotColor
// at positions near `center`. `radius` is the half-width of the glow band.
// `intensity` controls the maximum blend factor (0..1).
func ApplyGlowBand(ramp NeonRamp, center, radius, intensity float64, hotColor string) NeonRamp {
	if len(ramp) == 0 || radius <= 0 || intensity <= 0 {
		return ramp
	}
	out := make(NeonRamp, len(ramp))
	copy(out, ramp)
	for i := range out {
		dist := math.Abs(float64(i) - center)
		if dist >= radius {
			continue
		}
		// Smooth falloff (cosine bell).
		factor := intensity * (1 + math.Cos(math.Pi*dist/radius)) / 2
		out[i] = Gradient(out[i], hotColor, factor)
	}
	return out
}

// ApplyHotspot blends a narrow peak of hotColor at the given center position.
// width controls the total hotspot width (not radius).
func ApplyHotspot(ramp NeonRamp, center float64, hsWidth int, hotColor string) NeonRamp {
	if len(ramp) == 0 || hsWidth <= 0 {
		return ramp
	}
	out := make(NeonRamp, len(ramp))
	copy(out, ramp)
	halfW := float64(hsWidth) / 2
	for i := range out {
		dist := math.Abs(float64(i) - center)
		if dist >= halfW {
			continue
		}
		// Triangle falloff for a sharp peak.
		factor := 1 - dist/halfW
		out[i] = Gradient(out[i], hotColor, factor)
	}
	return out
}

// ---------------------------------------------------------------------------
// Neon color presets
// ---------------------------------------------------------------------------

// NeonDarkStops returns the "neon energy band" color stops for dark themes.
var NeonDarkStops = []ColorStop{
	{Pos: 0.0, Color: "#34dfff"},  // NeonCyan
	{Pos: 0.25, Color: "#4f7cff"}, // NeonBlue
	{Pos: 0.5, Color: "#8b5cf6"},  // NeonPurple
	{Pos: 0.75, Color: "#ff4fd8"}, // NeonPink
	{Pos: 1.0, Color: "#ffd9ff"},  // NeonHot
}

// NeonAccentColor returns a single neon accent color cycling over time.
// Useful for icon / text accent that should share the neon palette feel.
func NeonAccentColor(period time.Duration) string {
	phase := AnimationFloat(period)
	return GradientStops(NeonDarkStops, phase)
}

// ---------------------------------------------------------------------------
// Ramp cache (keyed by width)
// ---------------------------------------------------------------------------

type rampCacheKey struct {
	width   int
	palette string // currently only "neon"; kept explicit so cache semantics stay clear when more ramps are added.
}

var neonRampCache = make(map[rampCacheKey]NeonRamp)

// cachedNeonRamp returns a cached NeonRamp for the given width, building one
// if it doesn't exist yet. The cache is intentionally scoped to the neon
// palette; width is the actual rendered width, not the terminal width.
func cachedNeonRamp(width int) NeonRamp {
	key := rampCacheKey{width: width, palette: "neon"}
	if ramp, ok := neonRampCache[key]; ok {
		return ramp
	}
	// Build a ramp that is 2x the width to allow smooth shifting.
	ramp := BuildNeonRamp(width*2, NeonDarkStops)
	neonRampCache[key] = ramp
	return ramp
}
