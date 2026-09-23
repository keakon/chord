package imageutil

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"math/rand/v2"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/image/bmp"
	"golang.org/x/image/tiff"
)

func writePNG(t *testing.T, path string, w, h int) {
	t.Helper()
	if err := os.WriteFile(path, encodeTestPNG(t, gradientImage(w, h)), 0o600); err != nil {
		t.Fatalf("write png: %v", err)
	}
}

func writeJPEG(t *testing.T, path string, w, h int) {
	t.Helper()
	if err := os.WriteFile(path, encodeTestJPEG(t, gradientImage(w, h)), 0o600); err != nil {
		t.Fatalf("write jpeg: %v", err)
	}
}

func gradientImage(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 200, A: 255})
		}
	}
	return img
}

func encodeTestPNG(t *testing.T, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

func encodeTestJPEG(t *testing.T, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatalf("encode jpeg: %v", err)
	}
	return buf.Bytes()
}

func decodeTestImage(t *testing.T, data []byte) image.Image {
	t.Helper()
	img, format, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("decode image: %v", err)
	}
	if format != "png" && format != "jpeg" {
		t.Fatalf("normalized output format = %q, want png or jpeg", format)
	}
	return img
}

func assertNormalizedMIME(t *testing.T, mimeType string) {
	t.Helper()
	if mimeType != "image/png" && mimeType != "image/jpeg" {
		t.Fatalf("normalized mime = %q, want image/png or image/jpeg", mimeType)
	}
}

func TestNormalizeImageBytesPassesThroughConformingPNGAndJPEG(t *testing.T) {
	pngData := encodeTestPNG(t, gradientImage(64, 64))
	data, mimeType, err := NormalizeImageBytes(pngData, "image/png")
	if err != nil {
		t.Fatalf("NormalizeImageBytes(png): %v", err)
	}
	if mimeType != "image/png" || !bytes.Equal(data, pngData) {
		t.Fatalf("conforming PNG was re-encoded: mime=%q equal=%v", mimeType, bytes.Equal(data, pngData))
	}

	jpegData := encodeTestJPEG(t, gradientImage(32, 32))
	data, mimeType, err = NormalizeImageBytes(jpegData, "image/jpeg")
	if err != nil {
		t.Fatalf("NormalizeImageBytes(jpeg): %v", err)
	}
	if mimeType != "image/jpeg" || !bytes.Equal(data, jpegData) {
		t.Fatalf("conforming JPEG was re-encoded: mime=%q equal=%v", mimeType, bytes.Equal(data, jpegData))
	}
}

func TestNormalizeImageBytesConvertsAcceptedFormats(t *testing.T) {
	img := gradientImage(6, 4)

	var bmpBuf bytes.Buffer
	if err := bmp.Encode(&bmpBuf, img); err != nil {
		t.Fatal(err)
	}
	var tiffBuf bytes.Buffer
	if err := tiff.Encode(&tiffBuf, img, nil); err != nil {
		t.Fatal(err)
	}
	var gifBuf bytes.Buffer
	if err := gif.Encode(&gifBuf, img, nil); err != nil {
		t.Fatal(err)
	}
	webpData, err := base64.StdEncoding.DecodeString(tinyWebPBase64)
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name         string
		data         []byte
		declaredMime string
	}{
		{name: "bmp", data: bmpBuf.Bytes(), declaredMime: "image/bmp"},
		{name: "tiff", data: tiffBuf.Bytes(), declaredMime: "image/tiff"},
		{name: "gif", data: gifBuf.Bytes(), declaredMime: "image/gif"},
		{name: "webp", data: webpData, declaredMime: "image/webp"},
		{name: "content wins over extension", data: encodeTestPNG(t, img), declaredMime: "image/heic"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data, mimeType, err := NormalizeImageBytes(tc.data, tc.declaredMime)
			if err != nil {
				t.Fatalf("NormalizeImageBytes: %v", err)
			}
			assertNormalizedMIME(t, mimeType)
			decodeTestImage(t, data)
		})
	}
}

func TestNormalizeImageBytesUsesFirstGIFAndTIFFFrame(t *testing.T) {
	first := image.NewPaletted(image.Rect(0, 0, 2, 2), color.Palette{color.Black, color.White})
	first.SetColorIndex(0, 0, 1)
	second := image.NewPaletted(image.Rect(0, 0, 2, 2), color.Palette{color.Black, color.White})
	second.SetColorIndex(0, 0, 0)

	var buf bytes.Buffer
	if err := gif.EncodeAll(&buf, &gif.GIF{Image: []*image.Paletted{first, second}, Delay: []int{0, 100}}); err != nil {
		t.Fatal(err)
	}
	data, mimeType, err := NormalizeImageBytes(buf.Bytes(), "image/gif")
	if err != nil {
		t.Fatalf("NormalizeImageBytes(animated gif): %v", err)
	}
	assertNormalizedMIME(t, mimeType)
	img := decodeTestImage(t, data)
	if img.Bounds().Dx() != 2 || img.Bounds().Dy() != 2 {
		t.Fatalf("decoded bounds = %v", img.Bounds())
	}
	if r, _, _, _ := img.At(0, 0).RGBA(); r < 0x8000 {
		t.Fatalf("first frame pixel = %v, want light", img.At(0, 0))
	}
}

func TestNormalizeImageBytesRejectsUnsupportedFormats(t *testing.T) {
	heic := []byte("\x00\x00\x00\x18ftypheic\x00\x00\x00\x00mif1heic")
	avif := []byte("\x00\x00\x00\x20ftypavif\x00\x00\x00\x00avifmif1")
	svg := []byte("<?xml version=\"1.0\"?>\n<svg xmlns=\"http://www.w3.org/2000/svg\"></svg>")

	cases := []struct {
		name         string
		data         []byte
		declaredMime string
		wantSubstr   string
	}{
		{name: "heic", data: heic, declaredMime: "image/heic", wantSubstr: "HEIC/HEIF"},
		{name: "avif", data: avif, declaredMime: "image/avif", wantSubstr: "AVIF"},
		{name: "svg", data: svg, declaredMime: "image/svg+xml", wantSubstr: "SVG"},
		{name: "unknown", data: []byte("not an image"), declaredMime: "image/png", wantSubstr: "declared image/png"},
		{name: "empty", data: nil, declaredMime: "", wantSubstr: "empty"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := NormalizeImageBytes(tc.data, tc.declaredMime)
			if err == nil {
				t.Fatal("expected error")
			}
			if !bytes.Contains([]byte(err.Error()), []byte(tc.wantSubstr)) {
				t.Fatalf("error %q does not mention %q", err, tc.wantSubstr)
			}
		})
	}
}

func TestNormalizeImageBytesEnforcesSourceBudget(t *testing.T) {
	_, _, err := NormalizeImageBytes(make([]byte, MaxImageSourceBytes+1), "image/bmp")
	if err == nil || !bytes.Contains([]byte(err.Error()), []byte("too large")) {
		t.Fatalf("oversized source error = %v", err)
	}
}

func TestNormalizeImageBytesRejectsTruncatedBody(t *testing.T) {
	data := encodeTestPNG(t, gradientImage(32, 32))
	if _, _, err := NormalizeImageBytes(data[:len(data)-8], "image/png"); err == nil {
		t.Fatal("expected error for truncated PNG body")
	}
}

func TestNormalizeImageBytesRejectsOversizedDimensions(t *testing.T) {
	_, _, err := NormalizeImageBytes(craftPNGHeader(30_000, 30_000), "image/png")
	if err == nil || !bytes.Contains([]byte(err.Error()), []byte("megapixel")) {
		t.Fatalf("oversized dimension error = %v", err)
	}
}

// craftPNGHeader builds a PNG whose IHDR declares the given size but that has
// no image data, so DecodeConfig succeeds and Decode fails.
func craftPNGHeader(width, height int) []byte {
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:4], uint32(width))
	binary.BigEndian.PutUint32(ihdr[4:8], uint32(height))
	ihdr[8] = 8 // bit depth
	ihdr[9] = 6 // RGBA
	chunk := append([]byte("IHDR"), ihdr...)
	sum := crc32.ChecksumIEEE(chunk)
	chunk = binary.BigEndian.AppendUint32(chunk, sum)
	out := []byte("\x89PNG\r\n\x1a\n")
	out = binary.BigEndian.AppendUint32(out, uint32(len(ihdr)))
	out = append(out, chunk...)
	return out
}

func TestNormalizeImageBytesScalesDown(t *testing.T) {
	cases := []struct {
		name       string
		width      int
		height     int
		wantWidth  int
		wantHeight int
	}{
		{name: "wide", width: 4000, height: 100, wantWidth: 2000, wantHeight: 50},
		{name: "tall", width: 100, height: 4000, wantWidth: 50, wantHeight: 2000},
		{name: "long screenshot", width: 1200, height: 15_000, wantWidth: 160, wantHeight: 2000},
		{name: "single pixel edge", width: 1, height: 5000, wantWidth: 1, wantHeight: 2000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			img := image.NewRGBA(image.Rect(0, 0, tc.width, tc.height))
			data, _, err := NormalizeImageBytes(encodeTestPNG(t, img), "image/png")
			if err != nil {
				t.Fatalf("NormalizeImageBytes: %v", err)
			}
			decoded := decodeTestImage(t, data)
			if bounds := decoded.Bounds(); bounds.Dx() != tc.wantWidth || bounds.Dy() != tc.wantHeight {
				t.Fatalf("scaled bounds = %v, want %dx%d", bounds, tc.wantWidth, tc.wantHeight)
			}
		})
	}
}

func TestNormalizeImageBytesDoesNotScaleUp(t *testing.T) {
	data := encodeTestPNG(t, gradientImage(20, 10))
	out, _, err := NormalizeImageBytes(data, "image/png")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out, data) {
		t.Fatal("small image was re-encoded")
	}
}

func TestNormalizeImageBytesIsIdempotent(t *testing.T) {
	var bmpBuf bytes.Buffer
	if err := bmp.Encode(&bmpBuf, gradientImage(3000, 1500)); err != nil {
		t.Fatal(err)
	}
	first, firstMime, err := NormalizeImageBytes(bmpBuf.Bytes(), "image/bmp")
	if err != nil {
		t.Fatalf("first normalization: %v", err)
	}
	assertNormalizedMIME(t, firstMime)

	second, secondMime, err := NormalizeImageBytes(first, firstMime)
	if err != nil {
		t.Fatalf("second normalization: %v", err)
	}
	if secondMime != firstMime || !bytes.Equal(second, first) {
		t.Fatalf("normalization is not idempotent: mime %q -> %q, equal=%v", firstMime, secondMime, bytes.Equal(second, first))
	}
}

func TestNormalizeImageBytesFallsBackToJPEGOverBudget(t *testing.T) {
	img := noisyTransparentImage(t, 1500, 1500)
	pngData := encodeTestPNG(t, img)
	if len(pngData) <= MaxImageBytes {
		t.Fatalf("test fixture PNG is only %d bytes, needs to exceed %d", len(pngData), MaxImageBytes)
	}

	data, mimeType, err := NormalizeImageBytes(pngData, "image/png")
	if err != nil {
		t.Fatalf("NormalizeImageBytes: %v", err)
	}
	if mimeType != "image/jpeg" {
		t.Fatalf("mime = %q, want image/jpeg", mimeType)
	}
	if len(data) > MaxImageBytes {
		t.Fatalf("normalized image is %d bytes, over budget", len(data))
	}
	decoded := decodeTestImage(t, data)
	// The transparent block must be flattened onto white instead of black.
	r, g, b, _ := decoded.At(750, 750).RGBA()
	if r < 0x9000 || g < 0x9000 || b < 0x9000 {
		t.Fatalf("transparent area became dark: %v %v %v", r, g, b)
	}
}

func TestNormalizeImageShrinksJPEGOverBudget(t *testing.T) {
	// A conforming JPEG that needs no transform but still exceeds the upload
	// budget is shrunk instead of dropped: the PNG branch already fell back to
	// a re-encode, so both conforming sources degrade the same way now.
	img := noisyTransparentImage(t, 1800, 1800)
	jpegData := encodeTestJPEGWithQuality(t, img, 100)
	if len(jpegData) <= MaxImageBytes {
		t.Fatalf("test fixture JPEG is only %d bytes, needs to exceed %d", len(jpegData), MaxImageBytes)
	}

	out, err := NormalizeImage(jpegData, "image/jpeg")
	if err != nil {
		t.Fatalf("NormalizeImage: %v", err)
	}
	if out.MimeType != "image/jpeg" {
		t.Fatalf("mime = %q, want image/jpeg", out.MimeType)
	}
	if len(out.Data) > MaxImageBytes {
		t.Fatalf("normalized image is %d bytes, over budget", len(out.Data))
	}
	if !out.WasScaled() {
		t.Fatalf("over-budget JPEG was not shrunk: %dx%d", out.Width, out.Height)
	}
	if out.Width >= 1800 || out.Height >= 1800 {
		t.Fatalf("shrunk size = %dx%d, want smaller than the 1800x1800 source", out.Width, out.Height)
	}
}

func TestEncodeJPEGFlattensTransparency(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 2, 1))
	img.SetNRGBA(0, 0, color.NRGBA{R: 200, G: 0, B: 0, A: 0})   // fully transparent
	img.SetNRGBA(1, 0, color.NRGBA{R: 0, G: 0, B: 200, A: 128}) // half transparent

	data, err := encodeJPEG(img)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	r, g, b, _ := decoded.At(0, 0).RGBA()
	if r < 0xE000 || g < 0xE000 || b < 0xE000 {
		t.Fatalf("fully transparent pixel = %v %v %v, want white", r, g, b)
	}
	r, g, b, _ = decoded.At(1, 0).RGBA()
	if b < 0xA000 || r < 0x4000 {
		t.Fatalf("half transparent pixel = %v %v %v, want lightened blue", r, g, b)
	}
}

func noisyTransparentImage(t *testing.T, width, height int) image.Image {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	rng := rand.New(rand.NewPCG(1, 2))
	for y := range height {
		for x := range width {
			img.SetNRGBA(x, y, color.NRGBA{
				R: uint8(rng.Uint32()),
				G: uint8(rng.Uint32()),
				B: uint8(rng.Uint32()),
				A: 255,
			})
		}
	}
	// A transparent block would turn black without white flattening.
	for y := 700; y < 800; y++ {
		for x := 700; x < 800; x++ {
			pixel := img.NRGBAAt(x, y)
			pixel.A = 0
			img.SetNRGBA(x, y, pixel)
		}
	}
	return img
}

func TestJPEGOrientationParsing(t *testing.T) {
	base := encodeTestJPEG(t, gradientImage(8, 4))
	for _, order := range []binary.AppendByteOrder{binary.LittleEndian, binary.BigEndian} {
		for _, orientation := range []uint16{1, 2, 3, 4, 5, 6, 7, 8} {
			data := insertExifOrientation(base, order, orientation)
			if got := jpegOrientation(data); got != int(orientation) {
				t.Fatalf("order=%T orientation=%d: got %d", order, orientation, got)
			}
		}
	}

	if got := jpegOrientation(base); got != 1 {
		t.Fatalf("JPEG without EXIF: got %d, want 1", got)
	}

	malformed := [][]byte{
		{},
		[]byte("\xff\xd8"),
		[]byte("\xff\xd8\xff\xe1\x00\x02"),
		[]byte("\xff\xd8\xff\xe1\xff\xffExif\x00\x00II*\x00\x08\x00\x00\x00"),
		insertExifSegments(base, []byte("Exif\x00\x00II*\x00"), 0),
		insertExifSegments(base, []byte("Exif\x00\x00II*\x00\x08\x00\x00"), 1),
		insertExifSegments(base, []byte("Exif\x00\x00II*\x00\x08\x00\x00\x00\x01\x01\x12\x00\x03\x00\x00\x00\x01\x00\x00\x00"), 1),
	}
	for i, data := range malformed {
		if got := jpegOrientation(data); got != 1 {
			t.Fatalf("malformed case %d: got %d, want 1", i, got)
		}
	}
}

func TestNormalizeImageBytesAppliesEXIFOrientation(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 6, 3))
	for y := range 3 {
		for x := range 6 {
			fill := color.RGBA{A: 255}
			if x >= 3 {
				fill = color.RGBA{R: 255, G: 255, B: 255, A: 255}
			}
			img.Set(x, y, fill)
		}
	}
	base := encodeTestJPEGWithQuality(t, img, 95)

	plain, mimeType, err := NormalizeImageBytes(base, "image/jpeg")
	if err != nil {
		t.Fatal(err)
	}
	if mimeType != "image/jpeg" || decodeTestImage(t, plain).Bounds().Dx() != 6 {
		t.Fatalf("plain JPEG changed: mime=%q", mimeType)
	}

	rotated := insertExifOrientation(base, binary.LittleEndian, 6)
	data, mimeType, err := NormalizeImageBytes(rotated, "image/jpeg")
	if err != nil {
		t.Fatal(err)
	}
	if mimeType != "image/jpeg" {
		t.Fatalf("mime = %q, want image/jpeg", mimeType)
	}
	decoded := decodeTestImage(t, data)
	if bounds := decoded.Bounds(); bounds.Dx() != 3 || bounds.Dy() != 6 {
		t.Fatalf("oriented bounds = %v, want 3x6", bounds)
	}
	if r, _, _, _ := decoded.At(0, 1).RGBA(); r > 0x8000 {
		t.Fatalf("top-left after 90° rotation = %v, want dark", decoded.At(0, 1))
	}
	if r, _, _, _ := decoded.At(0, 4).RGBA(); r < 0x8000 {
		t.Fatalf("bottom-left after 90° rotation = %v, want light", decoded.At(0, 4))
	}

	// The normalized output carries no EXIF and must stay stable.
	again, againMime, err := NormalizeImageBytes(data, mimeType)
	if err != nil {
		t.Fatal(err)
	}
	if againMime != mimeType || !bytes.Equal(again, data) {
		t.Fatal("oriented output is not idempotent")
	}
}

func TestNormalizeImageBytesOrientationWithScaling(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 3000, 1500))
	base := encodeTestJPEG(t, img)
	rotated := insertExifOrientation(base, binary.BigEndian, 8)

	data, mimeType, err := NormalizeImageBytes(rotated, "image/jpeg")
	if err != nil {
		t.Fatal(err)
	}
	if mimeType != "image/jpeg" {
		t.Fatalf("mime = %q, want image/jpeg", mimeType)
	}
	decoded := decodeTestImage(t, data)
	if bounds := decoded.Bounds(); bounds.Dx() != 1000 || bounds.Dy() != 2000 {
		t.Fatalf("bounds = %v, want 1000x2000", bounds)
	}
}

func TestApplyOrientationMirrorsAllValues(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 2, 3))
	img.SetNRGBA(0, 0, color.NRGBA{R: 255, A: 255}) // marker pixel
	want := map[int][2]int{
		1: {0, 0},
		2: {1, 0},
		3: {1, 2},
		4: {0, 2},
		5: {0, 0},
		6: {2, 0},
		7: {2, 1},
		8: {0, 1},
	}
	for orientation, position := range want {
		out := applyOrientation(img, orientation)
		r, _, _, _ := out.At(position[0], position[1]).RGBA()
		if r < 0x8000 {
			t.Fatalf("orientation %d: marker not at %v", orientation, position)
		}
	}
}

func TestReadImageFile(t *testing.T) {
	dir := t.TempDir()

	pngPath := filepath.Join(dir, "p.png")
	writePNG(t, pngPath, 64, 64)
	out, err := ReadImageFile(pngPath)
	if err != nil {
		t.Fatalf("ReadImageFile(png): %v", err)
	}
	if out.MimeType != "image/png" || len(out.Data) == 0 {
		t.Fatalf("png result = %q, %d bytes", out.MimeType, len(out.Data))
	}

	jpgPath := filepath.Join(dir, "p.jpg")
	writeJPEG(t, jpgPath, 32, 32)
	out, err = ReadImageFile(jpgPath)
	if err != nil {
		t.Fatalf("ReadImageFile(jpg): %v", err)
	}
	if out.MimeType != "image/jpeg" {
		t.Fatalf("jpeg mime = %q, want image/jpeg", out.MimeType)
	}

	txtPath := filepath.Join(dir, "note.txt")
	if err := os.WriteFile(txtPath, []byte("hi"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadImageFile(txtPath); err == nil {
		t.Fatal("expected error for unsupported image format")
	}
}

func TestNormalizeImageReportsScaling(t *testing.T) {
	scaled, err := NormalizeImage(encodeTestPNG(t, gradientImage(3000, 1500)), "image/png")
	if err != nil {
		t.Fatal(err)
	}
	if !scaled.WasScaled() {
		t.Fatal("expected WasScaled for a 3000x1500 image")
	}
	if scaled.OriginalWidth != 3000 || scaled.OriginalHeight != 1500 || scaled.Width != 2000 || scaled.Height != 1000 {
		t.Fatalf("size report = %dx%d -> %dx%d", scaled.OriginalWidth, scaled.OriginalHeight, scaled.Width, scaled.Height)
	}

	plain, err := NormalizeImage(encodeTestPNG(t, gradientImage(20, 10)), "image/png")
	if err != nil {
		t.Fatal(err)
	}
	if plain.WasScaled() {
		t.Fatalf("20x10 image reported as scaled: %dx%d", plain.Width, plain.Height)
	}
}

func TestReadImageFileTrustsContentOverExtension(t *testing.T) {
	dir := t.TempDir()

	mislabeled := filepath.Join(dir, "photo.heic")
	if err := os.WriteFile(mislabeled, encodeTestPNG(t, gradientImage(4, 4)), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := ReadImageFile(mislabeled)
	if err != nil {
		t.Fatalf("ReadImageFile(mislabeled png): %v", err)
	}
	if out.MimeType != "image/png" || len(out.Data) == 0 {
		t.Fatalf("mislabeled result = %q, %d bytes", out.MimeType, len(out.Data))
	}

	heic := filepath.Join(dir, "real.heic")
	if err := os.WriteFile(heic, []byte("\x00\x00\x00\x18ftypheic\x00\x00\x00\x00mif1heic"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadImageFile(heic); err == nil {
		t.Fatal("expected HEIC rejection")
	}
}

func TestReadImageFileRejectsOversizedFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "big.png")
	if err := os.WriteFile(path, bytes.Repeat([]byte("a"), MaxImageSourceBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := ReadImageFile(path)
	if err == nil || !bytes.Contains([]byte(err.Error()), []byte("too large")) {
		t.Fatalf("oversized file error = %v", err)
	}
}

func TestReadPDFFile(t *testing.T) {
	dir := t.TempDir()

	pdfPath := filepath.Join(dir, "doc.pdf")
	raw := []byte("%PDF-1.7\nfake pdf bytes")
	if err := os.WriteFile(pdfPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	data, mime, err := ReadPDFFile(pdfPath)
	if err != nil {
		t.Fatalf("ReadPDFFile: %v", err)
	}
	if mime != "application/pdf" {
		t.Fatalf("mime = %q, want application/pdf", mime)
	}
	if !bytes.Equal(data, raw) {
		t.Fatal("pdf data not returned verbatim (should not be compressed)")
	}

	// Wrong extension is rejected even with PDF-looking content.
	wrong := filepath.Join(dir, "doc.bin")
	if err := os.WriteFile(wrong, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadPDFFile(wrong); err == nil {
		t.Fatal("expected error for non-.pdf extension")
	}

	// Oversize PDF is rejected.
	bigPath := filepath.Join(dir, "big.pdf")
	if err := os.WriteFile(bigPath, make([]byte, MaxPDFBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadPDFFile(bigPath); err == nil {
		t.Fatal("expected error for oversize pdf")
	}
}

func TestReadAttachmentFileDispatch(t *testing.T) {
	dir := t.TempDir()

	pdfPath := filepath.Join(dir, "doc.pdf")
	if err := os.WriteFile(pdfPath, []byte("%PDF-1.7"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, mime, err := ReadAttachmentFile(pdfPath)
	if err != nil || mime != "application/pdf" {
		t.Fatalf("ReadAttachmentFile(pdf) = %q, %v", mime, err)
	}

	pngPath := filepath.Join(dir, "p.png")
	writePNG(t, pngPath, 8, 8)
	_, mime, err = ReadAttachmentFile(pngPath)
	if err != nil {
		t.Fatalf("ReadAttachmentFile(png): %v", err)
	}
	if mime != "image/png" {
		t.Fatalf("png attachment mime = %q", mime)
	}
}

func TestCheckPDFSize(t *testing.T) {
	if err := CheckPDFSize(make([]byte, MaxPDFBytes)); err != nil {
		t.Fatalf("CheckPDFSize(at limit): %v", err)
	}
	if err := CheckPDFSize(make([]byte, MaxPDFBytes+1)); err == nil {
		t.Fatal("expected error over limit")
	}
}

func TestPDFAppearsEncrypted(t *testing.T) {
	if !PDFAppearsEncrypted([]byte("%PDF-1.7 /Encrypt 5 0 R")) {
		t.Fatal("expected encrypted marker to be detected")
	}
	if PDFAppearsEncrypted([]byte("%PDF-1.7 plain")) {
		t.Fatal("unexpected encrypted marker")
	}
}

// insertExifOrientation inserts an APP1/EXIF segment carrying only the
// Orientation tag right after the JPEG SOI marker.
func insertExifOrientation(jpegData []byte, order binary.AppendByteOrder, orientation uint16) []byte {
	payload := []byte("Exif\x00\x00")
	if order == binary.BigEndian {
		payload = append(payload, 'M', 'M')
	} else {
		payload = append(payload, 'I', 'I')
	}
	tiff := make([]byte, 0, 26)
	tiff = order.AppendUint16(tiff, 42)
	tiff = order.AppendUint32(tiff, 8) // IFD0 offset
	tiff = order.AppendUint16(tiff, 1) // one entry
	tiff = order.AppendUint16(tiff, 0x0112)
	tiff = order.AppendUint16(tiff, 3) // SHORT
	tiff = order.AppendUint32(tiff, 1)
	value := order.AppendUint16(make([]byte, 0, 4), orientation) // left-justified short
	value = append(value, 0, 0)
	tiff = append(tiff, value...)
	tiff = order.AppendUint32(tiff, 0) // no next IFD

	return insertExifSegments(jpegData, append(payload, tiff...), 0)
}

// insertExifSegments wraps payload into a raw APP1 segment, padding it to
// declaredLen so callers can craft malformed lengths.
func insertExifSegments(jpegData, payload []byte, declaredLen int) []byte {
	if declaredLen <= 0 {
		declaredLen = len(payload) + 2
	}
	for len(payload)+2 < declaredLen {
		payload = append(payload, 0)
	}
	segment := []byte{0xFF, 0xE1, byte(declaredLen >> 8), byte(declaredLen)}
	segment = append(segment, payload...)
	out := make([]byte, 0, len(jpegData)+len(segment))
	out = append(out, jpegData[:2]...)
	out = append(out, segment...)
	out = append(out, jpegData[2:]...)
	return out
}

func encodeTestJPEGWithQuality(t *testing.T, img image.Image, quality int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func FuzzJPEGOrientation(f *testing.F) {
	base := gradientImage(8, 4)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, base, &jpeg.Options{Quality: 85}); err != nil {
		f.Fatal(err)
	}
	f.Add(buf.Bytes())
	f.Add(insertExifOrientation(buf.Bytes(), binary.LittleEndian, 6))
	f.Add([]byte("\xff\xd8\xff\xe1\x00\x10Exif\x00\x00II*\x00\x08\x00\x00\x00"))
	f.Fuzz(func(t *testing.T, data []byte) {
		orientation := jpegOrientation(data)
		if orientation < 1 || orientation > 8 {
			t.Fatalf("orientation out of range: %d", orientation)
		}
		if orientation > 1 {
			if img, err := jpeg.Decode(bytes.NewReader(data)); err == nil {
				out := applyOrientation(img, orientation)
				if out.Bounds().Dx() <= 0 || out.Bounds().Dy() <= 0 {
					t.Fatalf("orientation %d produced empty image", orientation)
				}
			}
		}
	})
}

const tinyWebPBase64 = "UklGRrIBAABXRUJQVlA4TKUBAAAvSsAYAA8w//M///MfeJAkbXvaSG7m8Q3GfYSBJekwQztm/IcZlgwnmWImn2BK7aFmBtnVir6q//8VOkFE/xm4baTIu8c48ArEo6+B3zFKYln3pqClSCKX0begFTAXFOLXHSyF8cCNcZEG4OywuA4KVVfJCiArU7GAgJI8+lJP/OKMT/fBAjevg1cYB7YVkFuWga2lyPi5I0HFy5YTpWIHg0RZpkniRVW9odHAKOwosWuOGdxIyn2OvaCDvhg/we6TwadPBPbqBV58MsLmMJ8yZnOWk8SRz4N+QoyPL+MnamzMvcE1rHNEr91F9GKZPVUcS9w7PhhH36suB9qPeYb/oLk6cuTiJ0wOK3m5h1cKjW6EVZCYMK7dxcKCBdgP9HkKr9gkAO2P8GKZGWVdIAatQa+1IDpt6qyorVwdy01xdW8Jkfk6xjEXmVQQ+HQdFr6OKhIN34dXWq0+0qr6EJSCeeVLH9+gvGTLyqM65PQ44ihzlTXxQKjKbAvshXgir7Lil9w4L2bvMycmjQcqXaMCO6BlY28i+FOLzbfI1vEqxAhotocAAA=="
