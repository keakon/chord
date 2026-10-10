package ctxmgr

import (
	"encoding/base64"
	"encoding/json"

	"github.com/keakon/chord/internal/imagegen"
	"github.com/keakon/chord/internal/message"
)

// Native image bytes are rehydrated into result at request time. Charge image
// tokens separately from those base64 bytes, just as for ordinary image parts.
func nativeImageAccounting(native *message.NativeToolHistory) (payloadBytes, tokens int) {
	images, err := native.ImageReplayItems()
	if err != nil {
		return 0, 0
	} // Invalid receipts cannot be dispatched.
	for _, image := range images {
		part, confirmed := image.OriginalPart()
		if !confirmed {
			continue
		}
		payloadBytes += base64.StdEncoding.EncodedLen(int(part.PayloadBytes()))
		allowance := imagePartEstimateTokens
		var summary struct {
			Images []struct {
				Width  int64 `json:"width"`
				Height int64 `json:"height"`
			} `json:"images"`
		}
		if json.Unmarshal(image.Call.Result, &summary) == nil && len(summary.Images) == 1 {
			img := summary.Images[0]
			// Native replay preserves originals instead of normalizing to 2000px.
			// This is a conservative planning allowance, not a billing formula.
			if img.Width > 0 && img.Height > 0 && img.Width <= imagegen.MaxImageDimension && img.Height <= imagegen.MaxImageDimension && img.Width*img.Height <= imagegen.MaxPixels {
				allowance = max(allowance, int((img.Width*img.Height+749)/750))
			}
		}
		tokens += allowance
	}
	return payloadBytes, tokens
}
