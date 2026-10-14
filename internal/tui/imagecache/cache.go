package imagecache

import (
	"bytes"
	"container/list"
	"encoding/base64"
	"fmt"
	"hash/maphash"
	"image"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"weak"

	"golang.org/x/image/draw"

	"github.com/keakon/chord/internal/imageutil"
)

// Store owns bounded, reconstructible payloads shared by image consumers.
type Store struct {
	mu      sync.Mutex
	entries map[string]*Entry
}

// imageRuntimeCacheBudget bounds the resident payload bytes (raw + transport
// PNG + base64) the runtime cache keeps across all entries. Entries are
// derived data — every field re-derives from the part on demand — so eviction
// is always safe; the next render of an evicted image re-encodes once.
const imageRuntimeCacheBudget = 64 << 20

const imageRuntimeCacheMaxEntries = 256

// Identity identifies a part for key memoization: the backing array of
// inline data plus the path. The weak identity never keeps the bytes alive.
// Renders repeat the same part several times per
// frame (layout, protocol command, kitty ID), and the key is byte-proportional
// for inline payloads, so repeated computation is pure waste.
type Identity struct {
	dataRef weak.Pointer[byte]
	dataLen int
	path    string
	mime    string
}

func IdentityOf(part Source) Identity {
	ref := Identity{dataLen: len(part.Data), path: part.Path, mime: part.MIME}
	if len(part.Data) > 0 {
		ref.dataRef = weak.Make(&part.Data[0])
	}
	return ref
}

var imageRuntimeKeyMemo = struct {
	mu      sync.Mutex
	entries map[Identity]*list.Element
	order   list.List
}{entries: make(map[Identity]*list.Element)}

type imagePartKeyEntry struct {
	ref Identity
	key string
	err error
}

const imageRuntimeKeyMemoMaxEntries = 256

// Key memoizes FreshKey per part identity.
// Only inline payloads are memoized: their key hashes the whole
// payload, which the weak part identity identifies exactly. A path-backed key
// folds in the file's size and mtime so a rewritten file re-encodes, and the part
// identity cannot see that. Background commands inspect paths; foreground
// layout must use previously accepted metadata instead.
func Key(part Source) (string, error) {
	if part.Key != "" {
		return part.Key, nil
	}
	if len(part.Data) == 0 {
		return FreshKey(part)
	}
	ref := IdentityOf(part)
	imageRuntimeKeyMemo.mu.Lock()
	if elem, ok := imageRuntimeKeyMemo.entries[ref]; ok {
		imageRuntimeKeyMemo.order.MoveToBack(elem)
		entry := elem.Value.(imagePartKeyEntry)
		imageRuntimeKeyMemo.mu.Unlock()
		return entry.key, entry.err
	}
	imageRuntimeKeyMemo.mu.Unlock()
	// Scan source bytes without blocking unrelated foreground or background hits.
	key, err := FreshKey(part)
	imageRuntimeKeyMemo.mu.Lock()
	defer imageRuntimeKeyMemo.mu.Unlock()
	if elem, ok := imageRuntimeKeyMemo.entries[ref]; ok {
		imageRuntimeKeyMemo.order.MoveToBack(elem)
		entry := elem.Value.(imagePartKeyEntry)
		return entry.key, entry.err
	}
	if len(imageRuntimeKeyMemo.entries) >= imageRuntimeKeyMemoMaxEntries {
		oldest := imageRuntimeKeyMemo.order.Front()
		delete(imageRuntimeKeyMemo.entries, oldest.Value.(imagePartKeyEntry).ref)
		imageRuntimeKeyMemo.order.Remove(oldest)
	}
	imageRuntimeKeyMemo.entries[ref] = imageRuntimeKeyMemo.order.PushBack(imagePartKeyEntry{ref: ref, key: key, err: err})
	return key, err
}

// Entry synchronizes derived payloads for a source version. Accessors must
// receive the same source version used by Lookup.
type Entry struct {
	store *Store
	mu    sync.Mutex

	// Published independently of construction so budget checks never wait for I/O.
	residentBytes atomic.Int64
	// Payload use is independent of layout lookups; guarded by mu.
	lastPayloadAccess time.Time

	rawLoaded bool
	rawData   []byte
	rawErr    error

	cfgLoaded bool
	cfgReady  atomic.Pointer[imageRuntimeConfig]
	cfg       image.Config
	cfgFormat string
	cfgErr    error

	pngLoaded bool
	pngData   []byte
	pngWidth  int
	pngHeight int
	pngErr    error
	preview   bool

	base64Loaded bool
	base64PNG    string
	base64Err    error

	// lastAccess drives budget eviction; guarded by the store mutex.
	lastAccess int64
}

// New creates an independent runtime cache. A TUI process shares one store
// across all models, so viewer and inline preparation reuse the same payloads.
func New() *Store { return &Store{entries: make(map[string]*Entry)} }

// EnforceBudget drops reconstructible entries without waiting for decoding.
func (s *Store) EnforceBudget() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.enforceBudgetLocked()
}

func (e *Entry) enforceBudgetAfter(before int64) {
	if e.residentBytes.Load() == before {
		return
	}
	e.store.EnforceBudget()
}

var (
	imageCacheReadFile     = imageutil.ReadImageSource
	imageCacheStat         = os.Stat
	imageCacheOpen         = os.Open
	imageCacheDecodeConfig = func(r io.Reader) (image.Config, string, error) { return image.DecodeConfig(r) }
	imageCacheDecode       = imageutil.DecodeImage
	imageCacheEncodePNG    = func(w io.Writer, m image.Image) error { return png.Encode(w, m) }
)

// Lookup reuses the source version and optionally builds a bounded preview.
// Unpinned file sources require filesystem metadata and belong on background paths.
func (s *Store) Lookup(part Source, preview bool) (*Entry, error) {
	key, err := Key(part)
	if err != nil {
		return nil, err
	}
	if preview {
		key += ":preview"
	}
	now := time.Now().UnixNano()
	s.mu.Lock()
	defer s.mu.Unlock()
	if entry, ok := s.entries[key]; ok {
		entry.lastAccess = now
		return entry, nil
	}
	entry := &Entry{store: s, lastAccess: now, preview: preview}
	s.entries[key] = entry
	s.enforceBudgetLocked()
	return entry, nil
}

// enforceBudgetLocked evicts least-recently-accessed entries until resident
// payload bytes fit the budget. Callers hold only the store mutex; entry locks
// are never taken here: background decoding must not stall foreground lookups.
func (s *Store) enforceBudgetLocked() {
	type resident struct {
		key   string
		entry *Entry
		bytes int64
	}
	total := int64(0)
	for _, entry := range s.entries {
		total += entry.residentBytes.Load()
	}
	if total <= imageRuntimeCacheBudget && len(s.entries) <= imageRuntimeCacheMaxEntries {
		return
	}
	residents := make([]resident, 0, len(s.entries))
	for key, entry := range s.entries {
		residents = append(residents, resident{key: key, entry: entry, bytes: entry.residentBytes.Load()})
	}
	sort.Slice(residents, func(i, j int) bool {
		return residents[i].entry.lastAccess < residents[j].entry.lastAccess
	})
	for _, r := range residents {
		if total <= imageRuntimeCacheBudget && len(s.entries) <= imageRuntimeCacheMaxEntries {
			break
		}
		delete(s.entries, r.key)
		total -= r.bytes
	}
}

// FreshKey inspects the current source, ignoring any captured key.
// File inspection must run outside the UI loop.
func FreshKey(part Source) (string, error) {
	if len(part.Data) > 0 {
		return "data:" + part.MIME + ":" + strconv.Itoa(len(part.Data)) + ":" + hashImageCacheBytes(part.Data), nil
	}
	path := strings.TrimSpace(part.Path)
	if path == "" {
		return "", fmt.Errorf("image data unavailable")
	}
	cleanPath := path
	if abs, err := filepath.Abs(path); err == nil {
		cleanPath = abs
	}
	info, err := imageCacheStat(path)
	if err != nil {
		return "path:" + cleanPath + ":missing:" + part.MIME, nil
	}
	return fmt.Sprintf("path:%s:%s:%d:%d", cleanPath, part.MIME, info.Size(), info.ModTime().UnixNano()), nil
}

var imageCacheHashSeed = maphash.MakeSeed()

func hashImageCacheBytes(data []byte) string {
	return strconv.FormatUint(maphash.Bytes(imageCacheHashSeed, data), 16)
}

func (e *Entry) Raw(part Source) ([]byte, error) {
	e.mu.Lock()
	before := e.residentBytes.Load()
	defer func() {
		e.mu.Unlock()
		e.enforceBudgetAfter(before)
	}()
	defer func() { e.lastPayloadAccess = time.Now() }()
	return e.ensureRawUnlocked(part)
}

// Config returns published dimensions without waiting for transport work.
// On a cold entry it reads the source header.
func (e *Entry) Config(part Source) (image.Config, string, error) {
	if cfg := e.cfgReady.Load(); cfg != nil {
		return cfg.config, cfg.format, cfg.err
	}
	e.mu.Lock()
	before := e.residentBytes.Load()
	defer func() {
		e.mu.Unlock()
		e.enforceBudgetAfter(before)
	}()
	return e.ensureDecodeConfigUnlocked(part)
}

type imageRuntimeConfig struct {
	config image.Config
	format string
	err    error
}

func (e *Entry) Base64PNG(part Source) (string, int, error) {
	e.mu.Lock()
	before := e.residentBytes.Load()
	defer func() {
		e.mu.Unlock()
		e.enforceBudgetAfter(before)
	}()
	defer func() { e.lastPayloadAccess = time.Now() }()
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
	e.residentBytes.Add(int64(len(e.base64PNG)))
	e.base64Loaded = true
	return e.base64PNG, len(pngData), nil
}

func (e *Entry) ensureRawUnlocked(part Source) ([]byte, error) {
	if e.rawLoaded {
		return e.rawData, e.rawErr
	}
	e.rawLoaded = true
	if e.preview {
		original, err := e.store.Lookup(part, false)
		if err == nil {
			e.rawData, err = original.Raw(part)
		}
		e.rawErr = err
		e.residentBytes.Add(int64(cap(e.rawData)))
		return e.rawData, err
	}
	if len(part.Data) > 0 {
		if len(part.Data) > imageutil.MaxImageSourceBytes {
			e.rawErr = fmt.Errorf("image source exceeds byte limit")
			return nil, e.rawErr
		}
		e.rawData = part.Data
		e.residentBytes.Add(int64(cap(e.rawData)))
		return e.rawData, nil
	}
	path := strings.TrimSpace(part.Path)
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
	e.residentBytes.Add(int64(cap(e.rawData)))
	return e.rawData, nil
}

func (e *Entry) ensureDecodeConfigUnlocked(part Source) (image.Config, string, error) {
	if e.cfgLoaded {
		return e.cfg, e.cfgFormat, e.cfgErr
	}
	e.cfgLoaded = true
	defer func() {
		e.cfgReady.Store(&imageRuntimeConfig{config: e.cfg, format: e.cfgFormat, err: e.cfgErr})
	}()
	// Layout needs only a bounded header read, never a file-sized allocation.
	if !e.rawLoaded && len(part.Data) == 0 {
		f, err := imageCacheOpen(part.Path)
		if err != nil {
			e.cfgErr = err
			return image.Config{}, "", err
		}
		defer f.Close()
		e.cfg, e.cfgFormat, e.cfgErr = imageCacheDecodeConfig(io.LimitReader(f, imageutil.MaxImageSourceBytes))
		if e.cfgErr == nil {
			e.cfgErr = imageutil.CheckImageDimensions(e.cfg)
		}
		return e.cfg, e.cfgFormat, e.cfgErr
	}
	data := part.Data
	if len(data) == 0 {
		var err error
		data, err = e.ensureRawUnlocked(part)
		if err != nil {
			e.cfgErr = err
			return image.Config{}, "", err
		}
	}
	cfg, format, err := imageCacheDecodeConfig(bytes.NewReader(data))
	if err == nil {
		err = imageutil.CheckImageDimensions(cfg)
	}
	if err != nil {
		e.cfgErr = fmt.Errorf("decode image config: %w", err)
		return image.Config{}, "", e.cfgErr
	}
	e.cfg = cfg
	e.cfgFormat = format
	return e.cfg, e.cfgFormat, nil
}

func (e *Entry) ensureTransportPNGUnlocked(part Source) ([]byte, int, int, error) {
	if e.pngLoaded {
		return e.pngData, e.pngWidth, e.pngHeight, e.pngErr
	}
	e.pngLoaded = true
	cfg, format, err := e.ensureDecodeConfigUnlocked(part)
	if err != nil {
		e.pngErr = err
		return nil, 0, 0, err
	}
	data, err := e.ensureRawUnlocked(part)
	if err != nil {
		e.pngErr = err
		return nil, 0, 0, err
	}
	actual, actualFormat, err := imageCacheDecodeConfig(bytes.NewReader(data))
	if err == nil {
		err = imageutil.CheckImageDimensions(actual)
	}
	if err != nil || actual.Width != cfg.Width || actual.Height != cfg.Height || actualFormat != format {
		e.pngErr = fmt.Errorf("image changed or has invalid dimensions")
		return nil, 0, 0, e.pngErr
	}
	const previewEdge = 1024
	needsResize := e.preview && max(cfg.Width, cfg.Height) > previewEdge
	if format == "png" && !needsResize && imageutil.HasVerifiedImage(data) {
		e.pngData, e.pngWidth, e.pngHeight = data, cfg.Width, cfg.Height
		return e.pngData, e.pngWidth, e.pngHeight, nil
	}
	img, _, err := imageCacheDecode(data)
	if err != nil {
		e.pngErr = fmt.Errorf("decode image: %w", err)
		return nil, 0, 0, e.pngErr
	}
	bounds := img.Bounds()
	if bounds.Dx() <= 0 || bounds.Dy() <= 0 {
		e.pngErr = fmt.Errorf("image has invalid dimensions")
		return nil, 0, 0, e.pngErr
	}
	if format == "png" && !needsResize {
		e.pngData, e.pngWidth, e.pngHeight = data, cfg.Width, cfg.Height
		return e.pngData, e.pngWidth, e.pngHeight, nil
	}
	if needsResize {
		edge := max(bounds.Dx(), bounds.Dy())
		width, height := max(1, bounds.Dx()*previewEdge/edge), max(1, bounds.Dy()*previewEdge/edge)
		dst := image.NewRGBA(image.Rect(0, 0, width, height))
		draw.ApproxBiLinear.Scale(dst, dst.Bounds(), img, bounds, draw.Src, nil)
		img, bounds = dst, dst.Bounds()
	}
	var buf bytes.Buffer
	if err := imageCacheEncodePNG(&buf, img); err != nil {
		e.pngErr = fmt.Errorf("encode png: %w", err)
		return nil, 0, 0, e.pngErr
	}
	e.pngData = buf.Bytes()
	e.residentBytes.Add(int64(cap(e.pngData)))
	e.pngWidth = bounds.Dx()
	e.pngHeight = bounds.Dy()
	return e.pngData, e.pngWidth, e.pngHeight, nil
}
