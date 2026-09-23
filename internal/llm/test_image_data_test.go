package llm

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
)

// tinyPNG is a minimal valid PNG used by wire-conversion tests. The send-time
// boundary sniffs and validates image content, so tests can no longer feed
// arbitrary placeholder bytes for an image part: those would be dropped as
// unrecognized data instead of reaching the provider payload.
var tinyPNG = func() []byte {
	img := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	img.SetNRGBA(0, 0, color.NRGBA{R: 10, G: 20, B: 30, A: 255})
	img.SetNRGBA(1, 0, color.NRGBA{R: 40, G: 50, B: 60, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		panic(err)
	}
	return buf.Bytes()
}()

// tinyPNGBase64 is the base64 encoding of tinyPNG, matching what the wire
// encoders produce for an unmodified PNG part. It is precomputed so tests can
// compare against the literal payload the provider would receive.
var tinyPNGBase64 = base64.StdEncoding.EncodeToString(tinyPNG)
