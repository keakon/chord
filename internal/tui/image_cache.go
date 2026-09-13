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
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type imageRuntimeCacheStore struct {
	mu      sync.Mutex
	entries map[string]*imageRuntimeCacheEntry
}

// imageRuntimeCacheBudget bounds the resident payload bytes (raw + transport
// PNG + base64) the runtime cache keeps across all entries. Entries are
// derived data — every field re-derives from the part on demand — so eviction
// is always safe; the next render of an evicted image re-encodes once.
const imageRuntimeCacheBudget = 64 << 20

// imagePartKeyRef identifies a part for key memoization: the backing array of
// inline data plus the path. Renders repeat the same part several times per
// frame (layout, protocol command, kitty ID), and the key is byte-proportional
// for inline payloads, so repeated computation is pure waste.
type imagePartKeyRef struct {
	dataRef *byte
	dataLen int
	path    string
	mime    string
}

func imagePartKeyRefFor(part BlockImagePart) imagePartKeyRef {
	ref := imagePartKeyRef{dataLen: len(part.Data), path: part.ImagePath, mime: part.MimeType}
	if len(part.Data) > 0 {
		ref.dataRef = &part.Data[0]
	}
	return ref
}

var imageRuntimeKeyMemo = struct {
	mu      sync.Mutex
	entries map[imagePartKeyRef]imagePartKeyEntry
}{entries: make(map[imagePartKeyRef]imagePartKeyEntry)}

type imagePartKeyEntry struct {
	key string
	err error
}

const imageRuntimeKeyMemoMaxEntries = 256

// imageRuntimeCacheKeyCached memoizes imageRuntimeCacheKey per part identity.
func imageRuntimeCacheKeyCached(part BlockImagePart) (string, error) {
	ref := imagePartKeyRefFor(part)
	imageRuntimeKeyMemo.mu.Lock()
	defer imageRuntimeKeyMemo.mu.Unlock()
	if entry, ok := imageRuntimeKeyMemo.entries[ref]; ok {
		return entry.key, entry.err
	}
	key, err := imageRuntimeCacheKey(part)
	if len(imageRuntimeKeyMemo.entries) >= imageRuntimeKeyMemoMaxEntries {
		imageRuntimeKeyMemo.entries = make(map[imagePartKeyRef]imagePartKeyEntry)
	}
	imageRuntimeKeyMemo.entries[ref] = imagePartKeyEntry{key: key, err: err}
	return key, err
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

	// lastAccess drives budget eviction; guarded by the store mutex.
	lastAccess int64
}

// approxBytes reports the resident payload bytes held by the entry.
func (e *imageRuntimeCacheEntry) approxBytes() int64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	total := int64(len(e.rawData)) + int64(len(e.pngData)) + int64(len(e.base64PNG))
	return total
}

var imageRuntimeCache = imageRuntimeCacheStore{entries: make(map[string]*imageRuntimeCacheEntry)}

var (
	imageCacheReadFile     = os.ReadFile
	imageCacheDecodeConfig = func(r io.Reader) (image.Config, string, error) { return image.DecodeConfig(r) }
	imageCacheDecode       = func(r io.Reader) (image.Image, string, error) { return image.Decode(r) }
	imageCacheEncodePNG    = func(w io.Writer, m image.Image) error { return png.Encode(w, m) }
)

func imageRuntimeEntryForPart(part BlockImagePart) (*imageRuntimeCacheEntry, error) {
	key, err := imageRuntimeCacheKeyCached(part)
	if err != nil {
		return nil, err
	}
	now := time.Now().UnixNano()
	imageRuntimeCache.mu.Lock()
	defer imageRuntimeCache.mu.Unlock()
	if entry, ok := imageRuntimeCache.entries[key]; ok {
		entry.lastAccess = now
		return entry, nil
	}
	entry := &imageRuntimeCacheEntry{lastAccess: now}
	imageRuntimeCache.entries[key] = entry
	imageRuntimeCache.enforceBudgetLocked()
	return entry, nil
}

// enforceBudgetLocked evicts least-recently-accessed entries until resident
// payload bytes fit the budget. Callers hold the store mutex; entry locks are
// taken inside, which is the consistent store→entry order.
func (s *imageRuntimeCacheStore) enforceBudgetLocked() {
	const minEntriesBeforeEnforce = 8
	if len(s.entries) < minEntriesBeforeEnforce {
		return
	}
	type resident struct {
		key   string
		entry *imageRuntimeCacheEntry
		bytes int64
	}
	total := int64(0)
	residents := make([]resident, 0, len(s.entries))
	for key, entry := range s.entries {
		cost := entry.approxBytes()
		total += cost
		residents = append(residents, resident{key: key, entry: entry, bytes: cost})
	}
	if total <= imageRuntimeCacheBudget {
		return
	}
	sort.Slice(residents, func(i, j int) bool {
		return residents[i].entry.lastAccess < residents[j].entry.lastAccess
	})
	for _, r := range residents {
		if total <= imageRuntimeCacheBudget {
			break
		}
		delete(s.entries, r.key)
		total -= r.bytes
	}
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
