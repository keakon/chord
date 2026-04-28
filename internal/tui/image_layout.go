package tui

import (
	"fmt"
	"image/color"
	"strings"

	xansi "github.com/charmbracelet/x/ansi"
	xkitty "github.com/charmbracelet/x/ansi/kitty"
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
	entry, err := imageRuntimeEntryForPart(part)
	if err != nil {
		return 0, 0, err
	}
	cfg, _, err := entry.decodeConfig(part)
	if err != nil {
		return 0, 0, err
	}
	if cfg.Width <= 0 || cfg.Height <= 0 {
		return 0, 0, fmt.Errorf("image has invalid dimensions")
	}

	available := width - imagePlaceholderMargin
	if available < imagePlaceholderMinCols {
		available = imagePlaceholderMinCols
	}
	cols = min(available, imagePlaceholderMaxCols)
	if cols < imagePlaceholderMinCols {
		cols = imagePlaceholderMinCols
	}
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
	labelWidth := width - imagePlaceholderMargin
	if labelWidth < 1 {
		labelWidth = 1
	}
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

func renderImageBlock(part BlockImagePart, width int, cardBG string, caps TerminalImageCapabilities) ([]string, int, int, error) {
	if width <= 0 {
		width = 1
	}
	if caps.Backend == ImageBackendNone || !caps.SupportsInline {
		return renderImageFallback(part, width), visibleImageLabelWidth(part, width), 1, nil
	}
	cols, totalRows, err := imageRenderSize(part, width, caps)
	if err != nil {
		return renderImageFallback(part, width), visibleImageLabelWidth(part, width), 1, nil
	}
	bodyRows := totalRows - 1
	if bodyRows < 1 {
		bodyRows = 1
	}
	useKittyBackend := caps.Backend == ImageBackendKitty
	imageID := 0
	if useKittyBackend {
		imageID, err = kittyImageIDForVariant(part, fmt.Sprintf("inline:%d:%d", cols, bodyRows))
		if err != nil {
			return renderImageFallback(part, width), visibleImageLabelWidth(part, width), 1, nil
		}
	}
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
	labelWidth := width - imagePlaceholderMargin
	if labelWidth < 1 {
		labelWidth = 1
	}
	name := strings.TrimSpace(part.FileName)
	if name == "" {
		name = "image"
	}
	label := runewidthTruncateLabel("📷 "+name, labelWidth)
	lines = append(lines, DimStyle.Render("  "+label))
	return lines, cols, bodyRows, nil
}

func renderImageFallback(part BlockImagePart, width int) []string {
	labelWidth := width - imagePlaceholderMargin
	if labelWidth < 1 {
		labelWidth = 1
	}
	name := strings.TrimSpace(part.FileName)
	if name == "" {
		name = "image"
	}
	label := runewidthTruncateLabel("📷 "+name, labelWidth)
	return []string{DimStyle.Render("  " + label)}
}

func runewidthTruncateLabel(s string, width int) string {
	if width <= 0 {
		return ""
	}
	return truncateOneLine(s, width)
}

func kittyPlaceholderRow(row, cols int) string {
	if cols <= 0 {
		return ""
	}
	var sb strings.Builder
	for col := range cols {
		sb.WriteRune(xkitty.Placeholder)
		if col == 0 {
			sb.WriteRune(xkitty.Diacritic(row))
		} else if col == 1 {
			sb.WriteRune(xkitty.Diacritic(row))
			sb.WriteRune(xkitty.Diacritic(col))
		}
	}
	return sb.String()
}

func indexedColor(spec string) color.Color {
	r, g, b, _ := parseColor(spec)
	return color.RGBA{R: uint8(r), G: uint8(g), B: uint8(b), A: 0xff}
}

func encodeKittyTransmit(part BlockImagePart, imageID int) (string, error) {
	entry, err := imageRuntimeEntryForPart(part)
	if err != nil {
		return "", err
	}
	payload, _, err := entry.base64TransportPNG(part)
	if err != nil {
		return "", err
	}
	opts := (&xkitty.Options{
		Action: xkitty.Transmit,
		Quite:  2,
		ID:     imageID,
		Format: xkitty.PNG,
		Chunk:  true,
	}).Options()

	var sb strings.Builder
	chunk := xkitty.MaxChunkSize
	for start := 0; start < len(payload); start += chunk {
		end := start + chunk
		if end > len(payload) {
			end = len(payload)
		}
		chunkOpts := opts
		if start > 0 {
			chunkOpts = []string{"q=2"}
		}
		if end < len(payload) {
			chunkOpts = append(chunkOpts, "m=1")
		} else if start > 0 || len(payload) > chunk {
			chunkOpts = append(chunkOpts, "m=0")
		}
		sb.WriteString(xansi.KittyGraphics([]byte(payload[start:end]), chunkOpts...))
	}
	if len(payload) == 0 {
		sb.WriteString(xansi.KittyGraphics(nil, opts...))
	}
	return sb.String(), nil
}

func encodeKittyVirtualPlacement(imageID, cols, rows int) string {
	return xansi.KittyGraphics(nil, (&xkitty.Options{
		Action:           xkitty.Put,
		Quite:            2,
		ID:               imageID,
		Columns:          cols,
		Rows:             rows,
		VirtualPlacement: true,
	}).Options()...)
}

func encodeKittyDisplayPlacement(imageID, placementID, cols, rows, zIndex, offsetX, offsetY int) string {
	opts := []string{
		"q=2",
		fmt.Sprintf("i=%d", imageID),
		fmt.Sprintf("p=%d", placementID),
		"C=1",
		fmt.Sprintf("c=%d", cols),
		fmt.Sprintf("r=%d", rows),
		"a=p",
	}
	if offsetX > 0 {
		opts = append(opts, fmt.Sprintf("X=%d", offsetX))
	}
	if offsetY > 0 {
		opts = append(opts, fmt.Sprintf("Y=%d", offsetY))
	}
	if zIndex != 0 {
		opts = append(opts, fmt.Sprintf("z=%d", zIndex))
	}
	return xansi.KittyGraphics(nil, opts...)
}

func encodeKittyDeletePlacement(imageID, placementID int) string {
	return xansi.KittyGraphics(nil, (&xkitty.Options{
		Action:          xkitty.Delete,
		Quite:           2,
		ID:              imageID,
		PlacementID:     placementID,
		Delete:          xkitty.DeleteID,
		DeleteResources: false,
	}).Options()...)
}

func encodeKittyDeleteImage(imageID int) string {
	return xansi.KittyGraphics(nil, (&xkitty.Options{
		Action:          xkitty.Delete,
		Quite:           2,
		ID:              imageID,
		Delete:          xkitty.DeleteID,
		DeleteResources: true,
	}).Options()...)
}

func fnv32a(data []byte) uint32 {
	const (
		offset32 = 2166136261
		prime32  = 16777619
	)
	h := uint32(offset32)
	for _, b := range data {
		h ^= uint32(b)
		h *= prime32
	}
	return h
}
