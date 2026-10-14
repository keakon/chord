// Package imageprotocol prepares terminal image transport sequences without
// owning terminal output, screen placement state, or image source lifetime.
// Payload preparation belongs in background commands; callers own cancellation
// and decode admission. Identity, placement, and placeholder encoding are pure.
package imageprotocol

import (
	"fmt"
	"strings"

	xansi "github.com/keakon/x/ansi"
	xkitty "github.com/keakon/x/ansi/kitty"

	"github.com/keakon/chord/internal/tui/imagecache"
)

func imageIDForVariant(part imagecache.Source, variant string) (int, error) {
	key, err := imagecache.Key(part)
	if err != nil {
		return 0, err
	}
	return KittyImageID(key, variant), nil
}

func KittyImageID(key, variant string) int {
	// Kitty Unicode placeholders encode the image ID in the placeholder's
	// foreground truecolor value, which only carries 24 bits directly. Keep the
	// generated IDs within that range so Kitty/Ghostty can resolve placeholders
	// to the same image ID used in the virtual placement command.
	hash := fnv32a([]byte(key+":"+variant)) & 0x00FFFFFF
	if hash == 0 {
		hash = 1
	}
	return int(hash)
}

func KittyImageIDColor(imageID int) xansi.Color {
	if imageID < 0 {
		imageID = -imageID
	}
	if imageID == 0 {
		imageID = 1
	}
	return xansi.RGBColor{R: uint8((imageID >> 16) & 0xFF), G: uint8((imageID >> 8) & 0xFF), B: uint8(imageID & 0xFF)}
}

// KittyInline prepares a preview upload and virtual placement. A placed image
// only needs its stable identity; source decoding is skipped.
func KittyInline(cache *imagecache.Store, part imagecache.Source, cols, rows int, alreadyPlaced bool) (string, int, error) {
	imageID, err := imageIDForVariant(part, fmt.Sprintf("inline:%d:%d", cols, rows))
	if err != nil {
		return "", 0, err
	}
	var seq string
	if !alreadyPlaced {
		seq, err = kittyTransmit(cache, part, imageID, true)
		if err != nil {
			return "", 0, err
		}
		seq += kittyVirtualPlacement(imageID, cols, rows)
	}
	return seq, imageID, nil
}

// KittyViewer prepares original image bytes and an explicit screen placement.
func KittyViewer(cache *imagecache.Store, part imagecache.Source, placementID, cols, rows, zIndex, offsetX, offsetY int) (string, int, error) {
	imageID, err := imageIDForVariant(part, fmt.Sprintf("viewer:%d:%d", cols, rows))
	if err != nil {
		return "", 0, err
	}
	seq, err := kittyTransmit(cache, part, imageID, false)
	if err != nil {
		return "", 0, err
	}
	seq += KittyDisplayPlacement(imageID, placementID, cols, rows, zIndex, offsetX, offsetY)
	return seq, imageID, nil
}

// KittyDelete releases an image, or only its specified placement.
func KittyDelete(imageID, placementID int) string {
	if imageID <= 0 {
		return ""
	}
	if placementID > 0 {
		return kittyDeletePlacement(imageID, placementID)
	}
	return KittyDeleteImage(imageID)
}

// KittyPlaceholderRow encodes the Unicode placeholder row and column markers.
func KittyPlaceholderRow(row, cols int) string {
	if cols <= 0 {
		return ""
	}
	var sb strings.Builder
	for col := range cols {
		sb.WriteRune(xkitty.Placeholder)
		switch col {
		case 0:
			sb.WriteRune(xkitty.Diacritic(row))
		case 1:
			sb.WriteRune(xkitty.Diacritic(row))
			sb.WriteRune(xkitty.Diacritic(col))
		}
	}
	return sb.String()
}

func kittyTransmit(cache *imagecache.Store, part imagecache.Source, imageID int, preview bool) (string, error) {
	entry, err := cache.Lookup(part, preview)
	if err != nil {
		return "", err
	}
	payload, _, err := entry.Base64PNG(part)
	if err != nil {
		return "", err
	}
	opts := (&xkitty.Options{
		Action: xkitty.Transmit,
		Quiet:  2,
		ID:     imageID,
		Format: xkitty.PNG,
		Chunk:  true,
	}).Options()

	var sb strings.Builder
	chunk := xkitty.MaxChunkSize
	sb.Grow(len(payload) + (len(payload)/chunk+1)*128)
	for start := 0; start < len(payload); start += chunk {
		end := min(start+chunk, len(payload))
		chunkOpts := opts
		if start > 0 {
			chunkOpts = []string{"q=2"}
		}
		if end < len(payload) {
			chunkOpts = append(chunkOpts, "m=1")
		} else if start > 0 || len(payload) > chunk {
			chunkOpts = append(chunkOpts, "m=0")
		}
		sb.WriteString("\x1b_G")
		sb.WriteString(strings.Join(chunkOpts, ","))
		sb.WriteByte(';')
		sb.WriteString(payload[start:end])
		sb.WriteString("\x1b\\")
	}
	if len(payload) == 0 {
		sb.WriteString(xansi.KittyGraphics(nil, opts...))
	}
	return sb.String(), nil
}

func kittyVirtualPlacement(imageID, cols, rows int) string {
	return xansi.KittyGraphics(nil, (&xkitty.Options{
		Action:           xkitty.Put,
		Quiet:            2,
		ID:               imageID,
		Columns:          cols,
		Rows:             rows,
		VirtualPlacement: true,
	}).Options()...)
}

// KittyDisplayPlacement places a prepared image without moving the cursor.
func KittyDisplayPlacement(imageID, placementID, cols, rows, zIndex, offsetX, offsetY int) string {
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

func kittyDeletePlacement(imageID, placementID int) string {
	return xansi.KittyGraphics(nil, (&xkitty.Options{
		Action:          xkitty.Delete,
		Quiet:           2,
		ID:              imageID,
		PlacementID:     placementID,
		Delete:          xkitty.DeleteID,
		DeleteResources: true,
	}).Options()...)
}

// KittyDeleteImage releases all placements and resources for an image.
func KittyDeleteImage(imageID int) string {
	return xansi.KittyGraphics(nil, (&xkitty.Options{
		Action:          xkitty.Delete,
		Quiet:           2,
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
