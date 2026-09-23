package imageutil

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"image"
	"image/color"
	"strings"
	"testing"

	"golang.org/x/image/webp"
)

// The fixtures are real libwebp output, embedded so the tests never depend on
// the webp tools or the network.
const (
	// A still 3x4 lossy image, used as an animated frame payload.
	lossyStaticWebPBase64 = "UklGRmAAAABXRUJQVlA4IFQAAAAQAgCdASoDAAQAAgA0JbACdC0Zga/2YmwAAP7qXtN+oPd/gvF9Cty3/8wcNgdPgCCf/GgvP/3+ueGP65nn+zl9/v8EVv+Co/QVopxwKRjxUy2YAAA="

	// An 8x8 animation whose first frame is a full-canvas lossless red frame
	// with the blending bit set; a second frame repaints the middle 3x3 blue.
	animatedLosslessWebPBase64 = "UklGRoQAAABXRUJQVlA4WAoAAAACAAAABwAABwAAQU5JTQYAAAD/////AABBTk1GKAAAAAAAAAAAAAcAAAcAAGQAAAJWUDhMDwAAAC8HwAEABxD9j/4HIqL/AQBBTk1GKAAAAAEAAAEAAAIAAAIAAGQAAABWUDhMDwAAAC8CgAAABxDR//4HIqL/AQA="

	// An 8x8 animation whose first frame covers only the right half (4,0 with
	// a 4x8 size) and whose ANIM background is opaque white.
	animatedBackgroundWebPBase64 = "UklGRoQAAABXRUJQVlA4WAoAAAACAAAABwAABwAAQU5JTQYAAAD/////AABBTk1GKAAAAAIAAAAAAAMAAAcAAGQAAANWUDhMDwAAAC8DwAEAB1DkiP4HIqL/AQBBTk1GKAAAAAAAAAAAAAMAAAcAAGQAAABWUDhMDwAAAC8DwAEAB1DkiP4HIqL/AQA="
)

// Container flags and chunks are spelled out here rather than taken from the
// package constants: a fixture must encode what RFC 9649 says, not what the
// code under test believes.
const (
	testWebPAnimationFlag = 0x02
	testWebPAlphaFlag     = 0x10
	testWebPNoBlendFlag   = 0x02
)

// testWebPBackground is the BGRA background color of the hand-built fixtures,
// chosen so every channel can be told apart.
var testWebPBackground = [4]byte{0x11, 0x22, 0x33, 0xff}

func decodeBase64Fixture(t *testing.T, encoded string) []byte {
	t.Helper()
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	return data
}

// webpTestChunk appends one container chunk with its padding byte.
func webpTestChunk(dst []byte, id string, payload []byte) []byte {
	dst = append(dst, id...)
	dst = binary.LittleEndian.AppendUint32(dst, uint32(len(payload)))
	dst = append(dst, payload...)
	if len(payload)&1 != 0 {
		dst = append(dst, 0)
	}
	return dst
}

// webpTestContainer wraps chunks into a RIFF/WEBP container.
func webpTestContainer(chunks ...[]byte) []byte {
	var body []byte
	for _, chunk := range chunks {
		body = append(body, chunk...)
	}
	out := []byte("RIFF")
	out = binary.LittleEndian.AppendUint32(out, uint32(4+len(body)))
	out = append(out, "WEBP"...)
	return append(out, body...)
}

func webpTestVP8X(canvasWidth, canvasHeight int, flags byte) []byte {
	payload := make([]byte, 10)
	payload[0] = flags
	putU24(payload[4:7], canvasWidth-1)
	putU24(payload[7:10], canvasHeight-1)
	return webpTestChunk(nil, "VP8X", payload)
}

func webpTestANIM(background [4]byte) []byte {
	payload := append([]byte(nil), background[:]...)
	return webpTestChunk(nil, "ANIM", append(payload, 0, 0))
}

func webpTestANMF(frameX, frameY, frameWidth, frameHeight int, flags byte, frameData []byte) []byte {
	payload := make([]byte, 16, 16+len(frameData))
	putU24(payload[0:3], frameX/2)
	putU24(payload[3:6], frameY/2)
	putU24(payload[6:9], frameWidth-1)
	putU24(payload[9:12], frameHeight-1)
	putU24(payload[12:15], 100) // frame duration
	payload[15] = flags
	payload = append(payload, frameData...)
	return webpTestChunk(nil, "ANMF", payload)
}

// webpTestBitstream returns the bitstream payload of a single-chunk still WebP
// fixture.
func webpTestBitstream(t *testing.T, container []byte) []byte {
	t.Helper()
	if len(container) < 20 {
		t.Fatalf("fixture is %d bytes, want a chunk header", len(container))
	}
	id := string(container[12:16])
	if id != "VP8 " && id != "VP8L" {
		t.Fatalf("fixture chunk = %q, want a bitstream", id)
	}
	return container[20:]
}

// webpTestALPH builds a raw, unfiltered ALPH payload: one method byte followed
// by one alpha value per pixel.
func webpTestALPH(alphas []byte) []byte {
	return append([]byte{0x00}, alphas...)
}

func assertPixelNRGBA(t *testing.T, img image.Image, x, y int, want color.NRGBA) {
	t.Helper()
	got := color.NRGBAModel.Convert(img.At(x, y)).(color.NRGBA)
	if got != want {
		t.Fatalf("pixel (%d,%d) = %v, want %v", x, y, got, want)
	}
}

func TestNormalizeImageBytesUsesFirstFrameOfAnimatedWebP(t *testing.T) {
	data, mimeType, err := NormalizeImageBytes(decodeBase64Fixture(t, animatedLosslessWebPBase64), "image/webp")
	if err != nil {
		t.Fatalf("NormalizeImageBytes(animated webp): %v", err)
	}
	if mimeType != "image/png" {
		t.Fatalf("mime = %q, want image/png", mimeType)
	}
	img := decodeTestImage(t, data)
	if bounds := img.Bounds(); bounds.Dx() != 8 || bounds.Dy() != 8 {
		t.Fatalf("bounds = %v, want 8x8", bounds)
	}
	red := color.NRGBA{R: 255, A: 255}
	for y := range 8 {
		for x := range 8 {
			// The second frame repaints the middle 3x3 blue; only the first
			// frame may reach the output.
			assertPixelNRGBA(t, img, x, y, red)
		}
	}

	again, againMime, err := NormalizeImageBytes(data, mimeType)
	if err != nil {
		t.Fatalf("second normalization: %v", err)
	}
	if againMime != mimeType || !bytes.Equal(again, data) {
		t.Fatalf("animated output is not idempotent: mime %q -> %q", mimeType, againMime)
	}
}

func TestNormalizeImageBytesFillsAnimatedWebPCanvasBackground(t *testing.T) {
	data, mimeType, err := NormalizeImageBytes(decodeBase64Fixture(t, animatedBackgroundWebPBase64), "image/webp")
	if err != nil {
		t.Fatalf("NormalizeImageBytes(animated webp): %v", err)
	}
	if mimeType != "image/png" {
		t.Fatalf("mime = %q, want image/png", mimeType)
	}
	img := decodeTestImage(t, data)
	if bounds := img.Bounds(); bounds.Dx() != 8 || bounds.Dy() != 8 {
		t.Fatalf("bounds = %v, want 8x8", bounds)
	}

	// The first frame starts at x=4, so the left half keeps the ANIM
	// background color.
	white := color.NRGBA{R: 255, G: 255, B: 255, A: 255}
	assertPixelNRGBA(t, img, 0, 0, white)
	assertPixelNRGBA(t, img, 3, 7, white)
	green := color.NRGBA{G: 200, A: 255}
	assertPixelNRGBA(t, img, 4, 0, green)
	assertPixelNRGBA(t, img, 7, 7, green)
}

func TestNormalizeImageBytesRendersAnimatedWebPVP8Frame(t *testing.T) {
	static := decodeBase64Fixture(t, lossyStaticWebPBase64)
	bitstream := webpTestBitstream(t, static)
	frame, err := webp.Decode(bytes.NewReader(static))
	if err != nil {
		t.Fatalf("decode static fixture: %v", err)
	}
	frameChunk := webpTestChunk(nil, "VP8 ", bitstream)

	assertFramePixels := func(t *testing.T, img image.Image, offsetX, offsetY int) {
		t.Helper()
		for y := range 4 {
			for x := range 3 {
				want := color.NRGBAModel.Convert(frame.At(x, y)).(color.NRGBA)
				assertPixelNRGBA(t, img, offsetX+x, offsetY+y, want)
			}
		}
	}

	t.Run("full canvas", func(t *testing.T) {
		// The odd-sized metadata chunk only pads the container: it must not
		// knock the following chunks out of alignment.
		container := webpTestContainer(
			webpTestVP8X(3, 4, testWebPAnimationFlag),
			webpTestChunk(nil, "XMP ", []byte("hello")),
			webpTestANIM(testWebPBackground),
			webpTestANMF(0, 0, 3, 4, testWebPNoBlendFlag, frameChunk),
		)
		data, mimeType, err := NormalizeImageBytes(container, "image/webp")
		if err != nil {
			t.Fatalf("NormalizeImageBytes: %v", err)
		}
		if mimeType != "image/png" && mimeType != "image/jpeg" {
			t.Fatalf("mime = %q", mimeType)
		}
		img := decodeTestImage(t, data)
		if bounds := img.Bounds(); bounds.Dx() != 3 || bounds.Dy() != 4 {
			t.Fatalf("bounds = %v, want 3x4", bounds)
		}
		assertFramePixels(t, img, 0, 0)
	})

	t.Run("partial frame keeps the background", func(t *testing.T) {
		container := webpTestContainer(
			webpTestVP8X(6, 6, testWebPAnimationFlag),
			webpTestANIM(testWebPBackground),
			webpTestANMF(2, 2, 3, 4, testWebPNoBlendFlag, frameChunk),
		)
		data, _, err := NormalizeImageBytes(container, "image/webp")
		if err != nil {
			t.Fatalf("NormalizeImageBytes: %v", err)
		}
		img := decodeTestImage(t, data)
		if bounds := img.Bounds(); bounds.Dx() != 6 || bounds.Dy() != 6 {
			t.Fatalf("bounds = %v, want 6x6", bounds)
		}
		background := color.NRGBA{R: testWebPBackground[2], G: testWebPBackground[1], B: testWebPBackground[0], A: testWebPBackground[3]}
		assertPixelNRGBA(t, img, 0, 0, background)
		assertPixelNRGBA(t, img, 5, 5, background)
		assertFramePixels(t, img, 2, 2)
	})
}

func TestNormalizeImageBytesCompositesAnimatedWebPAlphaFrame(t *testing.T) {
	static := decodeBase64Fixture(t, lossyStaticWebPBase64)
	bitstream := webpTestBitstream(t, static)
	frame, err := webp.Decode(bytes.NewReader(static))
	if err != nil {
		t.Fatalf("decode static fixture: %v", err)
	}

	// Pixel (0,0) is transparent, every other pixel is opaque. The odd-sized
	// ALPH payload puts a padding byte between the two frame chunks.
	alphas := bytes.Repeat([]byte{0xff}, 3*4)
	alphas[0] = 0
	frameData := webpTestChunk(nil, "ALPH", webpTestALPH(alphas))
	frameData = append(frameData, webpTestChunk(nil, "VP8 ", bitstream)...)
	opaque := color.NRGBAModel.Convert(frame.At(1, 0)).(color.NRGBA)
	background := color.NRGBA{R: testWebPBackground[2], G: testWebPBackground[1], B: testWebPBackground[0], A: testWebPBackground[3]}

	t.Run("alpha blending shows the background", func(t *testing.T) {
		container := webpTestContainer(
			webpTestVP8X(3, 4, testWebPAnimationFlag|testWebPAlphaFlag),
			webpTestANIM(testWebPBackground),
			webpTestANMF(0, 0, 3, 4, 0, frameData),
		)
		data, _, err := NormalizeImageBytes(container, "image/webp")
		if err != nil {
			t.Fatalf("NormalizeImageBytes: %v", err)
		}
		img := decodeTestImage(t, data)
		assertPixelNRGBA(t, img, 0, 0, background)
		assertPixelNRGBA(t, img, 1, 0, opaque)
	})

	t.Run("no blending overwrites the rectangle", func(t *testing.T) {
		container := webpTestContainer(
			webpTestVP8X(3, 4, testWebPAnimationFlag|testWebPAlphaFlag),
			webpTestANIM(testWebPBackground),
			webpTestANMF(0, 0, 3, 4, testWebPNoBlendFlag, frameData),
		)
		data, _, err := NormalizeImageBytes(container, "image/webp")
		if err != nil {
			t.Fatalf("NormalizeImageBytes: %v", err)
		}
		img := decodeTestImage(t, data)
		if got := color.NRGBAModel.Convert(img.At(0, 0)).(color.NRGBA); got.A != 0 {
			t.Fatalf("transparent frame pixel = %v, want alpha 0", got)
		}
		assertPixelNRGBA(t, img, 1, 0, opaque)
	})
}

func TestNormalizeImageBytesKeepsStillWebPOnLibraryPath(t *testing.T) {
	static := decodeBase64Fixture(t, lossyStaticWebPBase64)
	bitstream := webpTestBitstream(t, static)
	alphas := bytes.Repeat([]byte{0xff}, 3*4)
	alphas[0] = 0

	// A still extended container: no animation flag, so nothing is composited
	// and the transparent pixel stays transparent.
	container := webpTestContainer(
		webpTestVP8X(3, 4, testWebPAlphaFlag),
		webpTestChunk(nil, "ALPH", webpTestALPH(alphas)),
		webpTestChunk(nil, "VP8 ", bitstream),
	)
	data, _, err := NormalizeImageBytes(container, "image/webp")
	if err != nil {
		t.Fatalf("NormalizeImageBytes(still webp): %v", err)
	}
	img := decodeTestImage(t, data)
	if got := color.NRGBAModel.Convert(img.At(0, 0)).(color.NRGBA); got.A != 0 {
		t.Fatalf("still webp pixel (0,0) = %v, want alpha 0", got)
	}
}

func TestNormalizeImageBytesRejectsMalformedAnimatedWebP(t *testing.T) {
	lossyStatic := decodeBase64Fixture(t, lossyStaticWebPBase64)
	lossyFrame := webpTestChunk(nil, "VP8 ", webpTestBitstream(t, lossyStatic))
	losslessFrame := webpTestChunk(nil, "VP8L", webpTestBitstream(t, decodeBase64Fixture(t, tinyWebPBase64)))
	alphaChunk := webpTestChunk(nil, "ALPH", webpTestALPH(bytes.Repeat([]byte{0xff}, 3*4)))
	background := webpTestANIM(testWebPBackground)

	truncated := webpTestContainer(
		webpTestVP8X(3, 4, testWebPAnimationFlag),
		background,
		webpTestANMF(0, 0, 3, 4, 0, lossyFrame),
	)
	truncated = truncated[:len(truncated)-4]

	cases := []struct {
		name string
		data []byte
	}{
		{
			name: "no ANMF frame",
			data: webpTestContainer(webpTestVP8X(3, 4, testWebPAnimationFlag), background),
		},
		{
			name: "two VP8X chunks",
			data: webpTestContainer(
				webpTestVP8X(3, 4, testWebPAnimationFlag),
				webpTestVP8X(3, 4, testWebPAnimationFlag),
				webpTestANMF(0, 0, 3, 4, 0, lossyFrame),
			),
		},
		{
			name: "malformed VP8X",
			data: webpTestContainer(
				webpTestChunk(nil, "VP8X", make([]byte, 9)),
				webpTestANMF(0, 0, 3, 4, 0, lossyFrame),
			),
		},
		{
			name: "truncated ANMF chunk",
			data: truncated,
		},
		{
			name: "frame outside the canvas",
			data: webpTestContainer(
				webpTestVP8X(2, 2, testWebPAnimationFlag),
				background,
				webpTestANMF(0, 0, 3, 4, 0, lossyFrame),
			),
		},
		{
			name: "frame header too short",
			data: webpTestContainer(webpTestVP8X(3, 4, testWebPAnimationFlag), webpTestChunk(nil, "ANMF", make([]byte, 15))),
		},
		{
			name: "ALPH after the bitstream",
			data: webpTestContainer(
				webpTestVP8X(3, 4, testWebPAnimationFlag),
				webpTestANMF(0, 0, 3, 4, 0, append(append([]byte(nil), lossyFrame...), alphaChunk...)),
			),
		},
		{
			name: "ALPH with a lossless bitstream",
			data: webpTestContainer(
				webpTestVP8X(8, 8, testWebPAnimationFlag),
				webpTestANMF(0, 0, 8, 8, 0, append(append([]byte(nil), alphaChunk...), losslessFrame...)),
			),
		},
		{
			name: "frame without a bitstream",
			data: webpTestContainer(
				webpTestVP8X(3, 4, testWebPAnimationFlag),
				webpTestANMF(0, 0, 3, 4, 0, alphaChunk),
			),
		},
		{
			name: "unknown frame chunk",
			data: webpTestContainer(
				webpTestVP8X(3, 4, testWebPAnimationFlag),
				webpTestANMF(0, 0, 3, 4, 0, webpTestChunk(nil, "JUNK", []byte{1, 2, 3})),
			),
		},
		{
			name: "bitstream size mismatch",
			data: webpTestContainer(
				webpTestVP8X(3, 4, testWebPAnimationFlag),
				background,
				webpTestANMF(0, 0, 3, 3, 0, lossyFrame),
			),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := NormalizeImageBytes(tc.data, "image/webp")
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), "WebP") {
				t.Fatalf("error %q does not name the format", err)
			}
		})
	}
}
