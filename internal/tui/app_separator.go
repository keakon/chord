package tui

import (
	"fmt"
	"math"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
)

// Status bar horizontal margins so content is not flush against edges or covered by scrollbar.
const (
	statusBarLeftMargin             = 1
	statusBarRightMargin            = 2
	statusBarActivityPathGap        = "  ·  "
	statusBarSessionMinWidth        = 8
	statusBarSessionMinVisibleCols  = 90
	statusBarSpacePad               = "                                                                "
	animatedInputSeparatorBandWidth = 18
	separatorPhaseOverscan          = 20
	separatorDemoFrameWidth         = 48
)

func normalizeSeparatorVariant(variant string) string {
	switch variant {
	case "", "neon":
		return "neon"
	case "dual", "comet", "pulse":
		return variant
	default:
		return "neon"
	}
}

func renderAnimatedSeparatorVariant(width int, theme Theme, variant string, busy bool, insertMode bool) string {
	if width <= 0 {
		return ""
	}
	sep := strings.Repeat(SectionSeparator, width)
	if !busy {
		if insertMode {
			return InputSeparatorStyle.Render(sep)
		}
		return InputSeparatorDimmedStyle.Render(sep)
	}

	variant = normalizeSeparatorVariant(variant)
	switch variant {
	case "neon":
		return renderNeonSeparator(width)
	case "dual", "comet", "pulse":
	default:
		return renderNeonSeparator(width)
	}

	plain := []rune(sep)
	baseColor := Gradient(theme.SeparatorFg, theme.ModeSearchFg, 0.18)
	accentStart := theme.ModeSearchFg
	accentMid := Gradient(theme.ModeSearchFg, theme.AccentGradientFromFg, 0.55)
	accentEnd := theme.AccentGradientToFg
	flashColor := Gradient(accentStart, accentEnd, 0.92)
	baseStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(baseColor))
	styles := make([]lipgloss.Style, len(plain))

	phaseFast := Pulse(1700 * time.Millisecond)
	phaseSlow := Pulse(2600 * time.Millisecond)
	centerFast := phaseFast * float64(len(plain)+animatedInputSeparatorBandWidth)
	centerSlow := phaseSlow * float64(len(plain)+animatedInputSeparatorBandWidth)
	bandRadius := float64(animatedInputSeparatorBandWidth) / 2
	if bandRadius < 1 {
		bandRadius = 1
	}
	blinkOn := (time.Now().UnixMilli()/180)%2 == 0

	for i := range plain {
		styles[i] = baseStyle
		fastDist := math.Abs(float64(i) - centerFast)
		slowDist := math.Abs(float64(i) - centerSlow)
		fastIntensity := 1 - fastDist/bandRadius
		if fastIntensity < 0 {
			fastIntensity = 0
		}
		slowIntensity := 1 - slowDist/(bandRadius*1.7)
		if slowIntensity < 0 {
			slowIntensity = 0
		}

		switch variant {
		case "dual":
			intensity := maxFloat(fastIntensity, slowIntensity*0.65)
			color := Gradient(accentStart, accentMid, minFloat(intensity*1.15, 1))
			if intensity > 0.62 {
				color = Gradient(accentMid, accentEnd, minFloat((intensity-0.62)/0.38, 1))
			}
			styles[i] = lipgloss.NewStyle().Foreground(lipgloss.Color(color)).Bold(intensity > 0.50)
			if blinkOn && fastDist < 1.2 {
				styles[i] = lipgloss.NewStyle().Foreground(lipgloss.Color(flashColor)).Bold(true)
			}
		case "comet":
			trail := 1 - fastDist/(bandRadius*2.2)
			if trail < 0 {
				trail = 0
			}
			head := 1 - fastDist/(bandRadius*0.8)
			if head < 0 {
				head = 0
			}
			intensity := maxFloat(trail*0.7, head)
			color := Gradient(baseColor, accentEnd, intensity)
			styles[i] = lipgloss.NewStyle().Foreground(lipgloss.Color(color)).Bold(head > 0.55)
		case "pulse":
			wave := maxFloat(fastIntensity*0.75, slowIntensity)
			color := Gradient(baseColor, accentMid, wave)
			styles[i] = lipgloss.NewStyle().Foreground(lipgloss.Color(color)).Bold(wave > 0.58)
		default:
			intensity := fastIntensity
			color := Gradient(accentStart, accentEnd, intensity)
			styles[i] = lipgloss.NewStyle().Foreground(lipgloss.Color(color)).Bold(intensity > 0.45)
			if blinkOn && len(plain) > 0 {
				head := int(centerFast)
				for _, idx := range []int{head - 1, head, head + 1} {
					if i == idx && idx >= 0 && idx < len(plain) {
						styles[i] = lipgloss.NewStyle().Foreground(lipgloss.Color(flashColor)).Bold(true)
					}
				}
			}
		}
	}

	var b strings.Builder
	for i, r := range plain {
		style := styles[i]
		if style.GetForeground() == nil {
			style = baseStyle
		}
		b.WriteString(style.Render(string(r)))
	}
	return b.String()
}

// renderNeonSeparator creates a "flowing energy band" separator using the neon
// multi-stop color ramp with dual glow bands and hotspot.
func renderNeonSeparator(width int) string {
	ramp := cachedNeonRamp(width)
	primaryPhase := AnimationFloat(2500 * time.Millisecond)
	secondaryPhase := AnimationFloat(3800 * time.Millisecond)
	offset := int(primaryPhase * float64(len(ramp)))
	colors := ShiftRamp(ramp, offset, width)
	primaryCenter := primaryPhase * float64(width+separatorPhaseOverscan)
	glowRadius := float64(width) * 0.35
	if glowRadius < 8 {
		glowRadius = 8
	}
	hotColor := "#ffd9ff"
	colors = ApplyGlowBand(colors, primaryCenter, glowRadius, 0.55, hotColor)
	secondaryCenter := secondaryPhase * float64(width+separatorPhaseOverscan)
	colors = ApplyGlowBand(colors, secondaryCenter, glowRadius*1.5, 0.30, hotColor)
	hsWidth := max(4, width/20)
	colors = ApplyHotspot(colors, primaryCenter, hsWidth, hotColor)

	var b strings.Builder
	b.Grow(width * 22)
	sepRune := SectionSeparator
	var prevR, prevG, prevB uint32 = 256, 256, 256
	for i := range width {
		r, g, bv, _ := parseColor(colors[i])
		if r != prevR || g != prevG || bv != prevB {
			fmt.Fprintf(&b, "\x1b[38;2;%d;%d;%dm", r, g, bv)
			prevR, prevG, prevB = r, g, bv
		}
		b.WriteString(sepRune)
	}
	b.WriteString("\x1b[0m")
	return b.String()
}

func maxFloat(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

func minFloat(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

func (m *Model) renderAnimatedInputSeparator(width int) string {
	busy := m.isFocusedAgentBusy()
	insertMode := m.mode == ModeInsert
	themeName := m.theme.Name
	frame := time.Now().UnixMilli() / 150
	if m.cachedSepWidth == width && m.cachedSepTheme == themeName && m.cachedSepBusy == busy &&
		m.cachedSepInsert == insertMode && m.cachedSepFrame == frame {
		return m.cachedSepResult
	}
	result := renderAnimatedSeparatorVariant(width, m.theme, "neon", busy, insertMode)
	m.cachedSepWidth = width
	m.cachedSepTheme = themeName
	m.cachedSepBusy = busy
	m.cachedSepInsert = insertMode
	m.cachedSepFrame = frame
	m.cachedSepResult = result
	return result
}
