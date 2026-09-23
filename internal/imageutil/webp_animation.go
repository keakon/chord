package imageutil

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	"io"

	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/webp"
)

// Animated WebP files keep every frame in an ANMF chunk that the library
// decoder skips, so a full decode of such a file fails with "invalid format".
// The first frame is extracted here instead and rebuilt as a still WebP
// container: animation is never forwarded, matching GIF and TIFF.
//
// Flag and chunk identifiers come from RFC 9649.
const (
	webpFlagAnimation = 0x02 // VP8X: the file is animated
	webpFlagAlpha     = 0x10 // VP8X: the file carries transparency

	// webpFrameNoBlend is ANMF flag bit 1: the frame rectangle is copied
	// instead of alpha-blended onto the canvas. Bit 0 is the disposal method,
	// which only affects the frames after the first and is never read here.
	webpFrameNoBlend = 0x02

	webpChunkVP8X = "VP8X"
	webpChunkANIM = "ANIM"
	webpChunkANMF = "ANMF"
	webpChunkALPH = "ALPH"
	webpChunkVP8  = "VP8 "
	webpChunkVP8L = "VP8L"
)

// decodeWebPImage decodes a WebP image. A still image goes to the library
// decoder unchanged; an animated container is reduced to its first frame.
func decodeWebPImage(r io.Reader) (image.Image, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("read WebP image: %w", err)
	}
	frame, err := parseWebPAnimationFrame(data)
	if err != nil {
		return nil, err
	}
	if frame == nil {
		return webp.Decode(bytes.NewReader(data))
	}
	return frame.decode()
}

// webpChunk is one RIFF chunk of a WebP container.
type webpChunk struct {
	id      string
	payload []byte
}

// webpChunkReader walks the chunks of a WebP container. Sizes are validated
// against the slice, so a malformed size cannot read past its end.
type webpChunkReader struct {
	data []byte
	off  int
}

// next returns the next chunk. ok is false at the end of the payload; a
// trailing fragment shorter than a chunk header ends the walk because readers
// may ignore data past the RIFF size.
func (r *webpChunkReader) next() (webpChunk, bool, error) {
	if r.off+8 > len(r.data) {
		return webpChunk{}, false, nil
	}
	id := string(r.data[r.off : r.off+4])
	size := int64(binary.LittleEndian.Uint32(r.data[r.off+4 : r.off+8]))
	end := int64(r.off) + 8 + size
	if end > int64(len(r.data)) {
		return webpChunk{}, false, fmt.Errorf("truncated WebP chunk %q", id)
	}
	chunk := webpChunk{id: id, payload: r.data[r.off+8 : end]}
	r.off = int(end) + int(size&1)
	return chunk, true, nil
}

// webpAnimationFrame is the first frame of an animated WebP with everything
// needed to render it on its canvas.
type webpAnimationFrame struct {
	canvasWidth  int
	canvasHeight int
	background   color.NRGBA

	frameX      int
	frameY      int
	frameWidth  int
	frameHeight int
	noBlend     bool

	alpha       []byte // ALPH payload; nil for frames without a separate alpha channel
	bitstreamID string // webpChunkVP8 or webpChunkVP8L
	bitstream   []byte
}

// parseWebPAnimationFrame parses the first frame of an animated WebP. It
// returns (nil, nil) for anything that is not an animated container, so still
// images stay with the library decoder; once the animation flag is seen, a
// malformed container is reported as an error.
func parseWebPAnimationFrame(data []byte) (*webpAnimationFrame, error) {
	if !matchWebP(data) {
		return nil, nil
	}

	reader := webpChunkReader{data: data, off: 12}
	var (
		seenVP8X                  bool
		animated                  bool
		canvasWidth, canvasHeight int
		background                color.NRGBA
	)
	for {
		chunk, ok, err := reader.next()
		if err != nil {
			if !animated {
				// A malformed still image is reported by the library decoder.
				return nil, nil
			}
			return nil, err
		}
		if !ok {
			break
		}
		switch chunk.id {
		case webpChunkVP8X:
			if seenVP8X {
				return nil, errors.New("WebP container has more than one VP8X chunk")
			}
			seenVP8X = true
			if len(chunk.payload) != 10 || chunk.payload[0]&webpFlagAnimation == 0 {
				return nil, nil
			}
			animated = true
			canvasWidth = u24(chunk.payload[4:7]) + 1
			canvasHeight = u24(chunk.payload[7:10]) + 1
		case webpChunkANIM:
			if !animated {
				continue
			}
			if len(chunk.payload) < 6 {
				return nil, errors.New("malformed WebP ANIM chunk")
			}
			// Background color, converted from the container's BGRA order.
			background = color.NRGBA{
				R: chunk.payload[2],
				G: chunk.payload[1],
				B: chunk.payload[0],
				A: chunk.payload[3],
			}
		case webpChunkANMF:
			if !animated {
				return nil, nil
			}
			return parseWebPFrame(chunk.payload, canvasWidth, canvasHeight, background)
		}
	}
	if !animated {
		return nil, nil
	}
	return nil, errors.New("animated WebP image has no ANMF frame")
}

// parseWebPFrame parses one ANMF payload into its first frame.
func parseWebPFrame(payload []byte, canvasWidth, canvasHeight int, background color.NRGBA) (*webpAnimationFrame, error) {
	if len(payload) < 16 {
		return nil, errors.New("animated WebP frame header is truncated")
	}
	frame := &webpAnimationFrame{
		canvasWidth:  canvasWidth,
		canvasHeight: canvasHeight,
		background:   background,
		frameX:       2 * u24(payload[0:3]),
		frameY:       2 * u24(payload[3:6]),
		frameWidth:   u24(payload[6:9]) + 1,
		frameHeight:  u24(payload[9:12]) + 1,
		noBlend:      payload[15]&webpFrameNoBlend != 0,
	}
	if frame.frameX+frame.frameWidth > canvasWidth || frame.frameY+frame.frameHeight > canvasHeight {
		return nil, fmt.Errorf("animated WebP frame %dx%d at (%d,%d) is outside the %dx%d canvas",
			frame.frameWidth, frame.frameHeight, frame.frameX, frame.frameY, canvasWidth, canvasHeight)
	}

	reader := webpChunkReader{data: payload, off: 16}
	for {
		chunk, ok, err := reader.next()
		if err != nil {
			return nil, err
		}
		if !ok {
			break
		}
		switch chunk.id {
		case webpChunkALPH:
			if frame.alpha != nil {
				return nil, errors.New("animated WebP frame has more than one ALPH chunk")
			}
			if frame.bitstreamID != "" {
				return nil, errors.New("animated WebP frame places its ALPH chunk after the bitstream")
			}
			frame.alpha = chunk.payload
		case webpChunkVP8, webpChunkVP8L:
			if frame.bitstreamID != "" {
				return nil, errors.New("animated WebP frame has more than one bitstream chunk")
			}
			if chunk.id == webpChunkVP8L && frame.alpha != nil {
				return nil, errors.New("animated WebP frame mixes an ALPH chunk with a lossless bitstream")
			}
			frame.bitstreamID = chunk.id
			frame.bitstream = chunk.payload
		default:
			return nil, fmt.Errorf("unexpected %q chunk in animated WebP frame data", chunk.id)
		}
	}
	if frame.bitstreamID == "" {
		return nil, errors.New("animated WebP frame has no bitstream chunk")
	}
	return frame, nil
}

// decode rebuilds the frame as a still WebP, decodes it, and renders it on a
// canvas filled with the animation's background color.
func (f *webpAnimationFrame) decode() (image.Image, error) {
	still := f.stillWebP()
	cfg, err := webp.DecodeConfig(bytes.NewReader(still))
	if err != nil {
		return nil, fmt.Errorf("decode animated WebP frame: %w", err)
	}
	// The frame header and the bitstream both declare the frame size; a
	// mismatch is rejected before the pixel buffers are allocated.
	if cfg.Width != f.frameWidth || cfg.Height != f.frameHeight {
		return nil, fmt.Errorf("animated WebP frame bitstream is %dx%d, the frame header declares %dx%d",
			cfg.Width, cfg.Height, f.frameWidth, f.frameHeight)
	}
	img, err := webp.Decode(bytes.NewReader(still))
	if err != nil {
		return nil, fmt.Errorf("decode animated WebP frame: %w", err)
	}

	canvas := image.NewNRGBA(image.Rect(0, 0, f.canvasWidth, f.canvasHeight))
	xdraw.Draw(canvas, canvas.Bounds(), image.NewUniform(f.background), image.Point{}, xdraw.Src)
	// Blending method 1 overwrites the rectangle; method 0 alpha-blends the
	// frame onto the background that was just painted.
	op := xdraw.Over
	if f.noBlend {
		op = xdraw.Src
	}
	rect := image.Rect(f.frameX, f.frameY, f.frameX+f.frameWidth, f.frameY+f.frameHeight)
	xdraw.Draw(canvas, rect, img, img.Bounds().Min, op)
	return canvas, nil
}

// stillWebP repacks the frame as a still WebP container the library decoder
// accepts. A frame with an ALPH chunk needs a VP8X chunk first: the decoder
// only reads ALPH after a VP8X canvas and checks the VP8 dimensions against
// that canvas, so the canvas is declared at the frame size.
func (f *webpAnimationFrame) stillWebP() []byte {
	var body []byte
	if f.alpha != nil {
		vp8x := make([]byte, 10)
		vp8x[0] = webpFlagAlpha
		putU24(vp8x[4:7], f.frameWidth-1)
		putU24(vp8x[7:10], f.frameHeight-1)
		body = appendWebPChunk(body, webpChunkVP8X, vp8x)
		body = appendWebPChunk(body, webpChunkALPH, f.alpha)
	}
	body = appendWebPChunk(body, f.bitstreamID, f.bitstream)

	out := make([]byte, 0, 12+len(body))
	out = append(out, "RIFF"...)
	out = binary.LittleEndian.AppendUint32(out, uint32(4+len(body)))
	out = append(out, "WEBP"...)
	return append(out, body...)
}

// appendWebPChunk appends one RIFF chunk and its padding byte to a WebP
// container body.
func appendWebPChunk(dst []byte, id string, payload []byte) []byte {
	dst = append(dst, id...)
	dst = binary.LittleEndian.AppendUint32(dst, uint32(len(payload)))
	dst = append(dst, payload...)
	if len(payload)&1 != 0 {
		dst = append(dst, 0)
	}
	return dst
}

// u24 reads a little-endian 24-bit integer.
func u24(b []byte) int {
	return int(b[0]) | int(b[1])<<8 | int(b[2])<<16
}

// putU24 writes a little-endian 24-bit integer.
func putU24(b []byte, v int) {
	b[0], b[1], b[2] = byte(v), byte(v>>8), byte(v>>16)
}
