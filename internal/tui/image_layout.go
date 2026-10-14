package tui

import (
	"fmt"
	"image"
	"image/color"
	"strings"

	xansi "github.com/keakon/x/ansi"

	"github.com/keakon/chord/internal/tui/terminaltext"
)

const (
	imagePlaceholderMargin  = 2
	imagePlaceholderMinCols = 12
	imagePlaceholderMaxCols = 48
	imagePlaceholderMaxRows = 12
	// Terminal cells are typically taller than they are wide (roughly 2:1 for
	// many monospace fonts). Compensate so requested image boxes match the real
	// on-screen aspect ratio instead of reserving too many rows.
	imageCellWidthOverHeight    = 0.5
	imageViewerHint             = "←/→ switch"
	imageViewerSingleHint       = ""
	imageViewerCloseBadge       = "[Esc/q]"
	imageViewerFallbackHint     = "Terminal image protocol unavailable"
	imageViewerMaxScale         = 2.0
	imageViewerInnerPadX        = 2
	imageViewerInnerPadY        = 1
	imageViewerMinReservedLines = 3
)

func imageRenderSize(part BlockImagePart, width int, caps TerminalImageCapabilities) (cols, rows int, err error) {
	if width <= 0 {
		width = 1
	}
	labelWidth := visibleImageLabelWidth(part, width)
	if caps.Backend == ImageBackendNone || !caps.SupportsInline {
		return labelWidth, 1, nil
	}
	cfg := image.Config{Width: 1, Height: 1}
	if len(part.Data) == 0 {
		if metadata, ok := imagePathMetadataSnapshot(part); ok {
			if metadata.err != nil {
				return 0, 0, metadata.err
			}
			cfg = metadata.config
		}
	} else {
		entry, err := imageCache.Lookup(imageSource(part), false)
		if err != nil {
			return 0, 0, err
		}
		cfg, _, err = entry.Config(imageSource(part))
		if err != nil {
			return 0, 0, err
		}
	}
	if cfg.Width <= 0 || cfg.Height <= 0 {
		return 0, 0, fmt.Errorf("image has invalid dimensions")
	}

	available := max(width-imagePlaceholderMargin, imagePlaceholderMinCols)
	cols = max(min(available, imagePlaceholderMaxCols), imagePlaceholderMinCols)
	rows = max(1, int((float64(cfg.Height)*float64(cols)*imageCellWidthOverHeight)/float64(cfg.Width)+0.5))
	if rows > imagePlaceholderMaxRows {
		scale := float64(imagePlaceholderMaxRows) / float64(rows)
		cols = max(imagePlaceholderMinCols, int(float64(cols)*scale+0.5))
		rows = imagePlaceholderMaxRows
	}
	if rows < 1 {
		rows = 1
	}
	return cols, rows + 1, nil // + label row
}

func visibleImageLabelWidth(part BlockImagePart, width int) int {
	labelWidth := max(width-imagePlaceholderMargin, 1)
	name := strings.TrimSpace(part.FileName)
	if name == "" {
		name = "image"
	}
	label := "📷 " + name
	w := xansi.StringWidth(label)
	if w > labelWidth {
		return labelWidth
	}
	if w < 1 {
		return 1
	}
	return w
}

func renderImageBlock(part *BlockImagePart, width int, cardBG string, caps TerminalImageCapabilities) ([]string, int, int, error) {
	part.RenderImageID = 0
	if width <= 0 {
		width = 1
	}
	if caps.Backend == ImageBackendNone || !caps.SupportsInline {
		return renderImageFallback(*part, width), visibleImageLabelWidth(*part, width), 1, nil
	}
	cols, totalRows, err := imageRenderSize(*part, width, caps)
	if err != nil {
		return renderImageFallback(*part, width), visibleImageLabelWidth(*part, width), 1, nil
	}
	bodyRows := max(totalRows-1, 1)
	useKittyBackend := caps.Backend == ImageBackendKitty
	imageID := 0
	if useKittyBackend {
		imageID, err = kittyRenderImageID(*part, cols, bodyRows)
		if err != nil {
			return renderImageFallback(*part, width), visibleImageLabelWidth(*part, width), 1, nil
		}
	}
	part.RenderImageID = imageID
	lines := make([]string, 0, totalRows)
	if useKittyBackend {
		lines = append(lines, kittyStyledPlaceholderLines(imageID, cols, bodyRows, cardBG)...)
	} else {
		blank := strings.Repeat(" ", cols)
		bgStyle := xansi.Style{}.BackgroundColor(indexedColor(cardBG))
		for range bodyRows {
			lines = append(lines, bgStyle.Styled("  "+blank))
		}
	}
	labelWidth := max(width-imagePlaceholderMargin, 1)
	name := strings.TrimSpace(part.FileName)
	if name == "" {
		name = "image"
	}
	label := runewidthTruncateLabel("📷 "+name, labelWidth)
	lines = append(lines, currentStyles.DimStyle.Render("  "+label))
	return lines, cols, bodyRows, nil
}

func renderImageFallback(part BlockImagePart, width int) []string {
	labelWidth := max(width-imagePlaceholderMargin, 1)
	name := strings.TrimSpace(part.FileName)
	if name == "" {
		name = "image"
	}
	label := runewidthTruncateLabel("📷 "+name, labelWidth)
	return []string{currentStyles.DimStyle.Render("  " + label)}
}

func runewidthTruncateLabel(s string, width int) string {
	if width <= 0 {
		return ""
	}
	return terminaltext.TruncateOneLine(s, width)
}

func indexedColor(spec string) color.Color {
	r, g, b, _ := parseColor(spec)
	return color.RGBA{R: uint8(r), G: uint8(g), B: uint8(b), A: 0xff}
}
