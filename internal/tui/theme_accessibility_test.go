package tui

import (
	"math"
	"strconv"
	"testing"
)

// The built-in palette uses xterm's 256-color table. This checks nominal text
// contrast; terminal profiles may remap these colors.
func themeColorLuminance(t *testing.T, color string) float64 {
	t.Helper()
	index, err := strconv.Atoi(color)
	if err != nil || index < 16 || index > 255 {
		t.Fatalf("expected 256-color palette entry, got %q", color)
	}
	rgb := [3]int{}
	if index >= 232 {
		for i := range rgb {
			rgb[i] = 8 + 10*(index-232)
		}
	} else {
		levels := [6]int{0, 95, 135, 175, 215, 255}
		n := index - 16
		rgb = [3]int{levels[n/36], levels[n/6%6], levels[n%6]}
	}
	weights := [3]float64{.2126, .7152, .0722}
	luminance := 0.0
	for i, v := range rgb {
		c := float64(v) / 255
		if c <= .04045 {
			c /= 12.92
		} else {
			c = math.Pow((c+.055)/1.055, 2.4)
		}
		luminance += c * weights[i]
	}
	return luminance
}

func TestDefaultThemeReadableTextContrast(t *testing.T) {
	theme := DefaultTheme()
	for _, pair := range []struct{ name, fg, bg string }{
		{"tool error", theme.ErrorFg, theme.ToolCallBg},
		{"error body", theme.ErrorCardFg, theme.ErrorCardBg},
		{"dialog deny", theme.DialogDangerFg, theme.DialogBg},
		{"key hint", theme.ConfirmToolFg, theme.DialogBg},
		{"primary action", theme.DialogPrimaryFg, theme.DialogBg},
		{"danger action", theme.DialogDangerFg, theme.DialogBg},
		{"assistant badge", theme.LabelBadgeFg, theme.AssistantLabelBg},
		{"thinking body", theme.ThinkingCardFg, theme.ThinkingCardBg},
	} {
		fg, bg := themeColorLuminance(t, pair.fg), themeColorLuminance(t, pair.bg)
		contrast := (max(fg, bg) + .05) / (min(fg, bg) + .05)
		if contrast < 4.5 {
			t.Errorf("%s contrast=%.2f, want at least 4.5", pair.name, contrast)
		}
	}
}
