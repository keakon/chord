package tui

import (
	"fmt"

	xansi "github.com/charmbracelet/x/ansi"
)

func kittyImageIDForVariant(part BlockImagePart, variant string) (int, error) {
	key, err := imageRuntimeCacheKey(part)
	if err != nil {
		return 0, err
	}
	// Kitty Unicode placeholders encode the image ID in the placeholder's
	// foreground truecolor value, which only carries 24 bits directly. Keep the
	// generated IDs within that range so Kitty/Ghostty can resolve placeholders
	// to the same image ID used in the virtual placement command.
	hash := fnv32a([]byte(key+":"+variant)) & 0x00FFFFFF
	if hash == 0 {
		hash = 1
	}
	return int(hash), nil
}

func kittyStyledPlaceholderLines(imageID, cols, rows int, cardBG string) []string {
	if cols <= 0 || rows <= 0 {
		return nil
	}
	lines := make([]string, 0, rows)
	fg := imageIDToColor(imageID)
	bg := indexedColor(cardBG)
	for row := range rows {
		placeholder := kittyPlaceholderRow(row, cols)
		style := xansi.Style{}.
			ForegroundColor(fg).
			BackgroundColor(bg)
		lines = append(lines, style.Styled("  "+placeholder))
	}
	return lines
}

func imageIDToColor(imageID int) xansi.Color {
	if imageID < 0 {
		imageID = -imageID
	}
	if imageID == 0 {
		imageID = 1
	}
	return xansi.RGBColor{R: uint8((imageID >> 16) & 0xFF), G: uint8((imageID >> 8) & 0xFF), B: uint8(imageID & 0xFF)}
}

func kittyInlineSequence(part BlockImagePart, cols, rows int, alreadyPlaced bool) (string, int, error) {
	imageID, err := kittyImageIDForVariant(part, fmt.Sprintf("inline:%d:%d", cols, rows))
	if err != nil {
		return "", 0, err
	}
	var seq string
	if !alreadyPlaced {
		seq, err = encodeKittyTransmit(part, imageID)
		if err != nil {
			return "", 0, err
		}
		seq += encodeKittyVirtualPlacement(imageID, cols, rows)
	}
	return seq, imageID, nil
}

func kittyViewerSequence(part BlockImagePart, placementID, cols, rows, zIndex, offsetX, offsetY int) (string, int, error) {
	imageID, err := kittyImageIDForVariant(part, fmt.Sprintf("viewer:%d:%d", cols, rows))
	if err != nil {
		return "", 0, err
	}
	seq, err := encodeKittyTransmit(part, imageID)
	if err != nil {
		return "", 0, err
	}
	seq += encodeKittyDisplayPlacement(imageID, placementID, cols, rows, zIndex, offsetX, offsetY)
	return seq, imageID, nil
}

func kittyDeleteSequenceForPlacement(imageID, placementID int) string {
	if imageID <= 0 {
		return ""
	}
	if placementID > 0 {
		return encodeKittyDeletePlacement(imageID, placementID)
	}
	return encodeKittyDeleteImage(imageID)
}

func kittyRenderImageID(part BlockImagePart, cols, rows int) (int, error) {
	return kittyImageIDForVariant(part, fmt.Sprintf("inline:%d:%d", cols, rows))
}

func kittyViewerLines(imageID, cols, rows int, bg string) []string {
	return kittyStyledPlaceholderLines(imageID, cols, rows, bg)
}
