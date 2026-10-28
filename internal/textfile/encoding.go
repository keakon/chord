// Package textfile detects file encodings and caches decoded text while
// preserving the encoding needed to write modified content back.
package textfile

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	ristretto "github.com/dgraph-io/ristretto/v2"
	lru "github.com/hashicorp/golang-lru/v2/expirable"
	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/japanese"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/traditionalchinese"
	textunicode "golang.org/x/text/encoding/unicode"
	"golang.org/x/text/encoding/unicode/utf32"
	"golang.org/x/text/transform"

	"github.com/keakon/chord/internal/pathutil"
)

// ErrBinaryFile identifies content detected as binary.
var ErrBinaryFile = errors.New("binary file")

const (
	BinarySampleBytes          = 4096
	pathCacheEntries           = 4096
	pathCacheTTL               = 30 * time.Minute
	decodedCacheTTL            = 15 * time.Minute
	decodedCacheMaxCost  int64 = 64 << 20 // 64 MiB
	decodedEntryMaxCost  int64 = 2 << 20  // 2 MiB
	decodedCacheCounters int64 = 20000
)

var (
	utf8BOM    = []byte{0xEF, 0xBB, 0xBF}
	utf16LEBOM = []byte{0xFF, 0xFE}
	utf16BEBOM = []byte{0xFE, 0xFF}
	utf32LEBOM = []byte{0xFF, 0xFE, 0x00, 0x00}
	utf32BEBOM = []byte{0x00, 0x00, 0xFE, 0xFF}
)

// Encoding describes a supported on-disk text encoding. Its codec and BOM
// remain private so callers can carry the descriptor without mutating shared data.
type Encoding struct {
	Name string
	enc  encoding.Encoding
	bom  []byte
}

var (
	utf8Encoding      = Encoding{Name: "utf-8"}
	utf8BOMEncoding   = Encoding{Name: "utf-8", bom: utf8BOM}
	utf16LEEncoding   = Encoding{Name: "utf-16le", enc: textunicode.UTF16(textunicode.LittleEndian, textunicode.IgnoreBOM), bom: utf16LEBOM}
	utf16BEEncoding   = Encoding{Name: "utf-16be", enc: textunicode.UTF16(textunicode.BigEndian, textunicode.IgnoreBOM), bom: utf16BEBOM}
	utf32LEEncoding   = Encoding{Name: "utf-32le", enc: utf32.UTF32(utf32.LittleEndian, utf32.IgnoreBOM), bom: utf32LEBOM}
	utf32BEEncoding   = Encoding{Name: "utf-32be", enc: utf32.UTF32(utf32.BigEndian, utf32.IgnoreBOM), bom: utf32BEBOM}
	gb18030Encoding   = Encoding{Name: "gb18030", enc: simplifiedchinese.GB18030}
	big5Encoding      = Encoding{Name: "big5", enc: traditionalchinese.Big5}
	shiftJISEncoding  = Encoding{Name: "shift-jis", enc: japanese.ShiftJIS}
	regionalEncodings = []Encoding{gb18030Encoding, big5Encoding, shiftJISEncoding}
)

var (
	simplifiedHintRunes  = runeSet("这为发后里会个来时实点线码页写读错档夹并处让将与关开无页档样显")
	traditionalHintRunes = runeSet("這為發後裡會個來時實點線碼頁寫讀錯檔夾並處讓將與關開無頁檔樣顯")
)

// Decoded preserves the logical text plus the encoding that should be used
// when writing back to disk.
type Decoded struct {
	Text     string
	Encoding Encoding
}

type encodingCacheEntry struct {
	Hash    [32]byte
	Decoded Decoded
	Binary  bool
	Valid   bool
}

type pathCacheEntry struct {
	Size    int64
	ModTime int64
	Hash    [32]byte
}

type decodedCacheValue struct {
	Entry     encodingCacheEntry
	ExpiresAt time.Time
}

var (
	pathDetectionCacheMu sync.RWMutex
	pathDetectionCache   *lru.LRU[string, pathCacheEntry]

	decodedCacheOnce sync.Once
	decodedCache     *ristretto.Cache[string, decodedCacheValue]
)

func init() {
	pathDetectionCache = lru.NewLRU[string, pathCacheEntry](pathCacheEntries, nil, pathCacheTTL)
}

func getDecodedCache() *ristretto.Cache[string, decodedCacheValue] {
	decodedCacheOnce.Do(func() {
		cache, err := ristretto.NewCache(&ristretto.Config[string, decodedCacheValue]{
			NumCounters: decodedCacheCounters,
			MaxCost:     decodedCacheMaxCost,
			BufferItems: 64,
		})
		if err != nil {
			panic(fmt.Errorf("create decoded cache: %w", err))
		}
		decodedCache = cache
	})
	return decodedCache
}

func cacheKeyForBytes(data []byte) [32]byte {
	return sha256.Sum256(data)
}

func hashKey(hash [32]byte) string {
	return string(hash[:])
}

func normalizeCachePath(path string) string {
	if expanded, err := pathutil.ExpandTilde(path); err == nil {
		if abs, err := filepath.Abs(filepath.Clean(expanded)); err == nil {
			return filepath.Clean(abs)
		}
	}
	if expanded, err := pathutil.ExpandTilde(path); err == nil {
		return filepath.Clean(expanded)
	}
	return filepath.Clean(path)
}

func getPathCache(path string) (pathCacheEntry, bool) {
	key := normalizeCachePath(path)
	pathDetectionCacheMu.RLock()
	defer pathDetectionCacheMu.RUnlock()
	return pathDetectionCache.Get(key)
}

func setPathCache(path string, entry pathCacheEntry) {
	key := normalizeCachePath(path)
	pathDetectionCacheMu.Lock()
	defer pathDetectionCacheMu.Unlock()
	pathDetectionCache.Add(key, entry)
}

// Invalidate removes a file path mapping before its contents are changed.
func Invalidate(path string) {
	key := normalizeCachePath(path)
	pathDetectionCacheMu.Lock()
	defer pathDetectionCacheMu.Unlock()
	pathDetectionCache.Remove(key)
}

// Warm primes the content cache after a successful file write and discards
// the path mapping so a concurrent external replacement is not hidden.
func Warm(path string, encodedBytes []byte, decoded Decoded) {
	Invalidate(path)
	cacheSuccess(cacheKeyForBytes(encodedBytes), decoded)
}

// Read reads and decodes a text file, reusing the two-level cache.
// On a path+hash cache hit it returns without re-reading the file body.
func Read(path string) (Decoded, error) {
	d, _, _, err := readDecodedTextFile(path, false)
	return d, err
}

// ReadWithBytes reads a text file and returns both decoded text and raw bytes.
// Use this only when the caller truly needs the raw bytes (for example, to report byte counts).
func ReadWithBytes(path string) (Decoded, []byte, error) {
	d, b, _, err := readDecodedTextFile(path, true)
	return d, b, err
}

// ReadWithHash reads and decodes a text file and returns the SHA-256 of the
// on-disk bytes backing the returned text. On a path+hash cache hit the digest
// comes from the path cache, so a caller that only needs version identity does
// not force a full re-read and a second hash of the same file.
func ReadWithHash(path string) (Decoded, [32]byte, error) {
	d, _, h, err := readDecodedTextFile(path, false)
	return d, h, err
}

// readDecodedTextFile loads and decodes path. When returnRaw is true, the raw
// file bytes are returned (one ReadFile on cache miss; one ReadFile on cache
// hit when raw bytes are needed). The digest always describes those bytes.
func readDecodedTextFile(path string, returnRaw bool) (Decoded, []byte, [32]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return Decoded{}, nil, [32]byte{}, err
	}
	if entry, ok := getPathCache(path); ok {
		if entry.Size == info.Size() && entry.ModTime == info.ModTime().UnixNano() {
			if dec, ok := loadDecodedFromHash(entry.Hash); ok {
				if goStrictUTF8Path(path) && isRegionalEncoding(dec.Encoding) {
					// Miss: same bytes may have been cached through a regional fallback.
				} else if !returnRaw {
					return dec, nil, entry.Hash, nil
				} else {
					data, rerr := os.ReadFile(path)
					if rerr != nil {
						return Decoded{}, nil, [32]byte{}, rerr
					}
					hash := cacheKeyForBytes(data)
					if hash == entry.Hash {
						return dec, data, hash, nil
					}
					decoded, derr := Decode(data, path)
					if derr != nil {
						return Decoded{}, nil, [32]byte{}, derr
					}
					setPathCache(path, pathCacheEntry{Size: int64(len(data)), ModTime: info.ModTime().UnixNano(), Hash: hash})
					return decoded, data, hash, nil
				}
			}
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Decoded{}, nil, [32]byte{}, err
	}
	decoded, err := Decode(data, path)
	if err != nil {
		return Decoded{}, nil, [32]byte{}, err
	}
	hash := cacheKeyForBytes(data)
	setPathCache(path, pathCacheEntry{Size: info.Size(), ModTime: info.ModTime().UnixNano(), Hash: hash})
	if !returnRaw {
		return decoded, nil, hash, nil
	}
	return decoded, data, hash, nil
}

func loadDecodedFromHash(hash [32]byte) (Decoded, bool) {
	cached, ok := getDecodedCache().Get(hashKey(hash))
	if !ok {
		return Decoded{}, false
	}
	if time.Now().After(cached.ExpiresAt) {
		getDecodedCache().Del(hashKey(hash))
		return Decoded{}, false
	}
	entry := cached.Entry
	if !entry.Valid || entry.Binary {
		return Decoded{}, false
	}
	return entry.Decoded, true
}

func goStrictUTF8Path(path string) bool {
	base := strings.ToLower(filepath.Base(path))
	switch base {
	case "go.mod", "go.sum", "go.work":
		return true
	default:
		return strings.HasSuffix(strings.ToLower(path), ".go")
	}
}

// Decode decodes file bytes to logical Unicode text. filePath should be the
// on-disk path when decoding a named file.
func Decode(data []byte, filePath string) (Decoded, error) {
	if len(data) == 0 {
		return Decoded{Encoding: utf8Encoding}, nil
	}

	hash := cacheKeyForBytes(data)
	if cached, ok := getDecodedCache().Get(hashKey(hash)); ok {
		if time.Now().After(cached.ExpiresAt) {
			getDecodedCache().Del(hashKey(hash))
		} else if cached.Entry.Valid {
			if cached.Entry.Binary {
				return Decoded{}, fmt.Errorf("%w: content appears to be binary", ErrBinaryFile)
			}
			dec := cached.Entry.Decoded
			if filePath != "" && goStrictUTF8Path(filePath) && isRegionalEncoding(dec.Encoding) {
				// Do not reuse a regional fallback when reading Go module/source paths.
			} else {
				return dec, nil
			}
		}
	}

	if enc, ok := detectBOMEncoding(data); ok {
		decoded, err := decodeWithEncoding(data, enc)
		cacheDecoded(hash, decoded, err)
		return decoded, err
	}
	if LooksBinary(data) {
		cacheBinary(hash)
		return Decoded{}, fmt.Errorf("%w: content appears to be binary", ErrBinaryFile)
	}
	if utf8.Valid(data) {
		decoded := Decoded{Text: string(data), Encoding: utf8Encoding}
		cacheSuccess(hash, decoded)
		return decoded, nil
	}
	if filePath != "" && goStrictUTF8Path(filePath) {
		return Decoded{}, fmt.Errorf("invalid UTF-8 in Go source file %q (Go requires UTF-8; skipped regional encoding detection)", filepath.Clean(filePath))
	}
	if decoded, ok := detectRegionalEncoding(data); ok {
		cacheSuccess(hash, decoded)
		return decoded, nil
	}
	if filePath != "" {
		return Decoded{}, fmt.Errorf("file %q is not valid UTF-8/BOM Unicode and no supported regional text encoding matched", filepath.Clean(filePath))
	}
	return Decoded{}, fmt.Errorf("content is not valid UTF-8/BOM Unicode and no supported regional text encoding matched")
}

func decodedEntryCost(entry encodingCacheEntry) int64 {
	if entry.Binary {
		return 1
	}
	return int64(len(entry.Decoded.Text)) + 128
}

func cacheSuccess(hash [32]byte, decoded Decoded) {
	entry := encodingCacheEntry{Hash: hash, Decoded: decoded, Valid: true}
	cost := decodedEntryCost(entry)
	if cost > decodedEntryMaxCost {
		return
	}
	getDecodedCache().SetWithTTL(hashKey(hash), decodedCacheValue{Entry: entry, ExpiresAt: time.Now().Add(decodedCacheTTL)}, cost, decodedCacheTTL)
}

func cacheBinary(hash [32]byte) {
	getDecodedCache().SetWithTTL(hashKey(hash), decodedCacheValue{Entry: encodingCacheEntry{Hash: hash, Binary: true, Valid: true}, ExpiresAt: time.Now().Add(decodedCacheTTL)}, 1, decodedCacheTTL)
}

func cacheDecoded(hash [32]byte, decoded Decoded, err error) {
	if err != nil {
		return
	}
	cacheSuccess(hash, decoded)
}

func detectBOMEncoding(data []byte) (Encoding, bool) {
	switch {
	case bytes.HasPrefix(data, utf32LEBOM):
		return utf32LEEncoding, true
	case bytes.HasPrefix(data, utf32BEBOM):
		return utf32BEEncoding, true
	case bytes.HasPrefix(data, utf8BOM):
		return utf8BOMEncoding, true
	case bytes.HasPrefix(data, utf16LEBOM):
		return utf16LEEncoding, true
	case bytes.HasPrefix(data, utf16BEBOM):
		return utf16BEEncoding, true
	default:
		return Encoding{}, false
	}
}

func decodeWithEncoding(data []byte, enc Encoding) (Decoded, error) {
	body := data
	if len(enc.bom) > 0 && bytes.HasPrefix(body, enc.bom) {
		body = body[len(enc.bom):]
	}
	var text string
	if enc.enc == nil {
		if !utf8.Valid(body) {
			return Decoded{}, fmt.Errorf("text is not valid UTF-8")
		}
		text = string(body)
	} else {
		decoded, _, err := transform.Bytes(enc.enc.NewDecoder(), body)
		if err != nil {
			return Decoded{}, err
		}
		text = string(decoded)
	}
	if err := validateDecodedText(text, isRegionalEncoding(enc)); err != nil {
		return Decoded{}, err
	}
	if _, err := Encode(text, enc); err != nil {
		return Decoded{}, err
	}
	return Decoded{Text: text, Encoding: enc}, nil
}

func validateDecodedText(text string, checkReplacement bool) error {
	if strings.ContainsRune(text, rune(0)) {
		return fmt.Errorf("decoded text contains NUL bytes")
	}
	if strings.ContainsRune(text, rune(0x1a)) {
		return fmt.Errorf("decoded text contains SUB control bytes")
	}
	if !checkReplacement {
		return nil
	}
	replacements := strings.Count(text, "�")
	if replacements == 0 {
		return nil
	}
	runes := len([]rune(text))
	if replacements > max(1, runes/100) || replacements*10 >= runes {
		return fmt.Errorf("decoded text contains too many replacement runes")
	}
	return nil
}

// Encode converts text back to the supplied encoding, preserving its BOM.
func Encode(text string, enc Encoding) ([]byte, error) {
	var body []byte
	if enc.enc == nil {
		if !utf8.ValidString(text) {
			return nil, fmt.Errorf("text is not valid UTF-8")
		}
		body = []byte(text)
	} else {
		encoded, _, err := transform.Bytes(enc.enc.NewEncoder(), []byte(text))
		if err != nil {
			return nil, err
		}
		body = encoded
	}
	if len(enc.bom) == 0 {
		return body, nil
	}
	out := make([]byte, 0, len(enc.bom)+len(body))
	out = append(out, enc.bom...)
	out = append(out, body...)
	return out, nil
}

func isRegionalEncoding(enc Encoding) bool {
	return enc.Name == gb18030Encoding.Name || enc.Name == big5Encoding.Name || enc.Name == shiftJISEncoding.Name
}

func detectRegionalEncoding(data []byte) (Decoded, bool) {
	bestScore := -1 << 30
	secondBest := -1 << 30
	var best Decoded
	for _, enc := range regionalEncodings {
		decoded, err := decodeWithEncoding(data, enc)
		if err != nil {
			continue
		}
		score := scoreDecodedText(decoded.Text, enc)
		if score > bestScore {
			secondBest = bestScore
			bestScore = score
			best = decoded
		} else if score > secondBest {
			secondBest = score
		}
	}
	if bestScore <= 0 {
		return Decoded{}, false
	}
	if secondBest > bestScore-10 {
		return Decoded{}, false
	}
	return best, true
}

func scoreDecodedText(text string, enc Encoding) int {
	var kana, halfwidthKana, halfwidthPunct, han int
	var simplifiedHits, traditionalHits int
	for _, r := range text {
		switch {
		case r >= 0xFF66 && r <= 0xFF9F:
			halfwidthKana++
		case r >= 0xFF61 && r <= 0xFF65:
			halfwidthPunct++
		case unicode.Is(unicode.Hiragana, r), unicode.Is(unicode.Katakana, r):
			kana++
		case unicode.Is(unicode.Han, r):
			han++
		}
		if _, ok := simplifiedHintRunes[r]; ok {
			simplifiedHits++
		}
		if _, ok := traditionalHintRunes[r]; ok {
			traditionalHits++
		}
	}
	var score int
	score += han / 4
	switch enc.Name {
	case shiftJISEncoding.Name:
		if kana == 0 && halfwidthKana+halfwidthPunct > 2 {
			return -1 << 20
		}
		score += kana * 10
		if kana > 0 {
			score += 24
		}
		if kana == 0 && halfwidthKana > 0 {
			score -= halfwidthKana * 16
		} else {
			score += halfwidthKana
		}
	case gb18030Encoding.Name:
		score += simplifiedHits * 8
		score -= traditionalHits * 2
		score -= kana * 2
		score -= halfwidthKana * 3
		score -= halfwidthPunct * 2
	case big5Encoding.Name:
		score += traditionalHits * 8
		score -= simplifiedHits * 2
		score -= kana * 2
		score -= halfwidthKana * 3
		score -= halfwidthPunct * 2
	}
	return score
}

// LooksBinary reports whether a bounded sample contains binary content.
func LooksBinary(data []byte) bool {
	sample := limitBytes(data, BinarySampleBytes)
	if len(sample) == 0 {
		return false
	}
	if _, ok := detectBOMEncoding(sample); ok {
		return false
	}
	if bytes.IndexByte(sample, 0) >= 0 {
		return true
	}
	contentType := http.DetectContentType(sample)
	if isKnownBinaryContentType(contentType) {
		return true
	}
	control := 0
	for _, b := range sample {
		if b < 0x20 && b != '\n' && b != '\r' && b != '\t' && b != '\f' {
			control++
		}
	}
	return control*100 > len(sample)*30
}

func isKnownBinaryContentType(contentType string) bool {
	if strings.HasPrefix(contentType, "text/") {
		return false
	}
	switch {
	case strings.HasPrefix(contentType, "image/") && contentType != "image/svg+xml":
		return true
	case strings.HasPrefix(contentType, "audio/"), strings.HasPrefix(contentType, "video/"), strings.HasPrefix(contentType, "font/"):
		return true
	}
	switch contentType {
	case "application/octet-stream", "application/zip", "application/x-gzip", "application/pdf", "application/x-rar-compressed", "application/vnd.rar":
		return true
	default:
		return false
	}
}

func limitBytes(data []byte, maxBytes int) []byte {
	if len(data) <= maxBytes {
		return data
	}
	return data[:maxBytes]
}

func runeSet(chars string) map[rune]struct{} {
	out := make(map[rune]struct{}, len([]rune(chars)))
	for _, r := range chars {
		out[r] = struct{}{}
	}
	return out
}

// UTF8 constructs decoded text that is written as UTF-8 without a BOM.
func UTF8(text string) Decoded {
	return Decoded{Text: text, Encoding: utf8Encoding}
}
