package llm

import (
	"bytes"
	"image"
	"image/color"
	"image/gif"
	"testing"

	"github.com/keakon/chord/internal/message"
)

// tinyGIF is a minimal single-frame GIF, the shape a legacy session part may
// still carry before the send-time boundary normalizes it.
var tinyGIF = func() []byte {
	img := image.NewPaletted(image.Rect(0, 0, 2, 2), color.Palette{color.Black, color.White})
	var buf bytes.Buffer
	if err := gif.Encode(&buf, img, nil); err != nil {
		panic(err)
	}
	return buf.Bytes()
}()

func TestBinaryPartForWireNormalizesLegacyGIF(t *testing.T) {
	part := message.ContentPart{Type: message.ContentPartImage, MimeType: "image/gif", Data: tinyGIF}

	data, mime, ok := binaryPartForWire(part)
	if !ok {
		t.Fatal("GIF part dropped at the wire boundary, want it normalized")
	}
	if mime != "image/png" {
		t.Fatalf("mime = %q, want image/png", mime)
	}
	if !bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")) {
		t.Fatalf("payload is not a PNG: % x", data[:8])
	}

	// The result is memoized by the source digest: a repeat conversion must
	// return the very same payload instead of decoding and re-encoding again.
	again, againMime, ok := binaryPartForWire(part)
	if !ok {
		t.Fatal("second conversion dropped the part")
	}
	if againMime != mime || !bytes.Equal(again, data) {
		t.Fatalf("second conversion = %q %d bytes, want the cached %q %d bytes", againMime, len(again), mime, len(data))
	}
}

func TestBinaryPartForWireDropsUnrecognizedImage(t *testing.T) {
	part := message.ContentPart{Type: message.ContentPartImage, MimeType: "image/png", Data: []byte("not an image")}
	if data, mime, ok := binaryPartForWire(part); ok {
		t.Fatalf("unrecognized image accepted: %q %d bytes", mime, len(data))
	}
}
