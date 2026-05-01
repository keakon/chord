package tui

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"hash/fnv"
	"image"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

type imageRuntimeCacheStore struct {
	mu      sync.Mutex
	entries map[string]*imageRuntimeCacheEntry
}

type imageRuntimeCacheEntry struct {
	mu sync.Mutex

	rawLoaded bool
	rawData   []byte
	rawErr    error

	cfgLoaded bool
	cfg       image.Config
	cfgFormat string
	cfgErr    error

	pngLoaded bool
	pngData   []byte
	pngWidth  int
	pngHeight int
	pngErr    error

	base64Loaded bool
	base64PNG    string
	base64Err    error
}

var imageRuntimeCache = imageRuntimeCacheStore{entries: make(map[string]*imageRuntimeCacheEntry)}

var (
	imageCacheReadFile     = os.ReadFile
	imageCacheDecodeConfig = func(r io.Reader) (image.Config, string, error) { return image.DecodeConfig(r) }
	imageCacheDecode       = func(r io.Reader) (image.Image, string, error) { return image.Decode(r) }
	imageCacheEncodePNG    = func(w io.Writer, m image.Image) error { return png.Encode(w, m) }
)

func imageRuntimeEntryForPart(part BlockImagePart) (*imageRuntimeCacheEntry, error) {
	key, err := imageRuntimeCacheKey(part)
	if err != nil {
		return nil, err
	}
	imageRuntimeCache.mu.Lock()
	defer imageRuntimeCache.mu.Unlock()
	if entry, ok := imageRuntimeCache.entries[key]; ok {
		return entry, nil
	}
	entry := &imageRuntimeCacheEntry{}
	imageRuntimeCache.entries[key] = entry
	return entry, nil
}

func imageRuntimeCacheKey(part BlockImagePart) (string, error) {
	if len(part.Data) > 0 {
		return "data:" + part.MimeType + ":" + strconv.Itoa(len(part.Data)) + ":" + hashImageCacheBytes(part.Data), nil
	}
	path := strings.TrimSpace(part.ImagePath)
	if path == "" {
		return "", fmt.Errorf("image data unavailable")
	}
	cleanPath := path
	if abs, err := filepath.Abs(path); err == nil {
		cleanPath = abs
	}
	info, err := os.Stat(path)
	if err != nil {
		return "path:" + cleanPath + ":missing:" + part.MimeType, nil
	}
	return fmt.Sprintf("path:%s:%s:%d:%d", cleanPath, part.MimeType, info.Size(), info.ModTime().UnixNano()), nil
}

func hashImageCacheBytes(data []byte) string {
	h := fnv.New64a()
	_, _ = h.Write(data)
	return fmt.Sprintf("%016x", h.Sum64())
}

func (e *imageRuntimeCacheEntry) raw(part BlockImagePart) ([]byte, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.ensureRawUnlocked(part)
}

func (e *imageRuntimeCacheEntry) decodeConfig(part BlockImagePart) (image.Config, string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.ensureDecodeConfigUnlocked(part)
}

func (e *imageRuntimeCacheEntry) base64TransportPNG(part BlockImagePart) (string, int, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.base64Loaded {
		return e.base64PNG, len(e.pngData), e.base64Err
	}
	pngData, _, _, err := e.ensureTransportPNGUnlocked(part)
	if err != nil {
		e.base64Loaded = true
		e.base64Err = err
		return "", 0, err
	}
	e.base64PNG = base64.StdEncoding.EncodeToString(pngData)
	e.base64Loaded = true
	return e.base64PNG, len(pngData), nil
}

func (e *imageRuntimeCacheEntry) ensureRawUnlocked(part BlockImagePart) ([]byte, error) {
	if e.rawLoaded {
		return e.rawData, e.rawErr
	}
	e.rawLoaded = true
	if len(part.Data) > 0 {
		e.rawData = part.Data
		return e.rawData, nil
	}
	path := strings.TrimSpace(part.ImagePath)
	if path == "" {
		e.rawErr = fmt.Errorf("image data unavailable")
		return nil, e.rawErr
	}
	data, err := imageCacheReadFile(path)
	if err != nil {
		e.rawErr = fmt.Errorf("read image file: %w", err)
		return nil, e.rawErr
	}
	e.rawData = data
	return e.rawData, nil
}

func (e *imageRuntimeCacheEntry) ensureDecodeConfigUnlocked(part BlockImagePart) (image.Config, string, error) {
	if e.cfgLoaded {
		return e.cfg, e.cfgFormat, e.cfgErr
	}
	e.cfgLoaded = true
	data, err := e.ensureRawUnlocked(part)
	if err != nil {
		e.cfgErr = err
		return image.Config{}, "", err
	}
	cfg, format, err := imageCacheDecodeConfig(bytes.NewReader(data))
	if err != nil {
		e.cfgErr = fmt.Errorf("decode image config: %w", err)
		return image.Config{}, "", e.cfgErr
	}
	e.cfg = cfg
	e.cfgFormat = format
	return e.cfg, e.cfgFormat, nil
}

func (e *imageRuntimeCacheEntry) ensureTransportPNGUnlocked(part BlockImagePart) ([]byte, int, int, error) {
	if e.pngLoaded {
		return e.pngData, e.pngWidth, e.pngHeight, e.pngErr
	}
	e.pngLoaded = true
	data, err := e.ensureRawUnlocked(part)
	if err != nil {
		e.pngErr = err
		return nil, 0, 0, err
	}
	img, _, err := imageCacheDecode(bytes.NewReader(data))
	if err != nil {
		e.pngErr = fmt.Errorf("decode image: %w", err)
		return nil, 0, 0, e.pngErr
	}
	bounds := img.Bounds()
	if bounds.Dx() <= 0 || bounds.Dy() <= 0 {
		e.pngErr = fmt.Errorf("image has invalid dimensions")
		return nil, 0, 0, e.pngErr
	}
	var buf bytes.Buffer
	if err := imageCacheEncodePNG(&buf, img); err != nil {
		e.pngErr = fmt.Errorf("encode png: %w", err)
		return nil, 0, 0, e.pngErr
	}
	e.pngData = buf.Bytes()
	e.pngWidth = bounds.Dx()
	e.pngHeight = bounds.Dy()
	return e.pngData, e.pngWidth, e.pngHeight, nil
}
