package tools

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
	"github.com/wlynxg/chardet"
	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/japanese"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/traditionalchinese"
	textunicode "golang.org/x/text/encoding/unicode"
	"golang.org/x/text/encoding/unicode/utf32"
	"golang.org/x/text/transform"
)

var ErrBinaryFile = errors.New("binary file")

const (
	binarySampleBytes          = 4096
	chardetSampleBytes         = 64 * 1024
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

// textEncoding represents a supported on-disk text encoding.
type textEncoding struct {
	Name string
	Enc  encoding.Encoding
	BOM  []byte
}

var (
	utf8Encoding      = textEncoding{Name: "utf-8"}
	utf8BOMEncoding   = textEncoding{Name: "utf-8", BOM: utf8BOM}
	utf16LEEncoding   = textEncoding{Name: "utf-16le", Enc: textunicode.UTF16(textunicode.LittleEndian, textunicode.IgnoreBOM), BOM: utf16LEBOM}
	utf16BEEncoding   = textEncoding{Name: "utf-16be", Enc: textunicode.UTF16(textunicode.BigEndian, textunicode.IgnoreBOM), BOM: utf16BEBOM}
	utf32LEEncoding   = textEncoding{Name: "utf-32le", Enc: utf32.UTF32(utf32.LittleEndian, utf32.IgnoreBOM), BOM: utf32LEBOM}
	utf32BEEncoding   = textEncoding{Name: "utf-32be", Enc: utf32.UTF32(utf32.BigEndian, utf32.IgnoreBOM), BOM: utf32BEBOM}
	gb18030Encoding   = textEncoding{Name: "gb18030", Enc: simplifiedchinese.GB18030}
	big5Encoding      = textEncoding{Name: "big5", Enc: traditionalchinese.Big5}
	shiftJISEncoding  = textEncoding{Name: "shift-jis", Enc: japanese.ShiftJIS}
	legacyEncodings   = []textEncoding{gb18030Encoding, big5Encoding, shiftJISEncoding}
	knownTextEncoding = map[string]textEncoding{
		"utf-8":     utf8Encoding,
		"utf-8-bom": utf8BOMEncoding,
		"utf-16le":  utf16LEEncoding,
		"utf-16be":  utf16BEEncoding,
		"utf-32le":  utf32LEEncoding,
		"utf-32be":  utf32BEEncoding,
		"gb18030":   gb18030Encoding,
		"big5":      big5Encoding,
		"shift-jis": shiftJISEncoding,
	}
)

var (
	simplifiedHintRunes  = runeSet("这为发后里会个来时实点线码页写读错档夹并处让将与关开无页档样显")
	traditionalHintRunes = runeSet("這為發後裡會個來時實點線碼頁寫讀錯檔夾並處讓將與關開無頁檔樣顯")
)

// decodedText preserves the logical text plus the encoding that should be used
// when writing back to disk.
type decodedText struct {
	Text     string
	Encoding textEncoding
}

type encodingCacheEntry struct {
	Hash    [32]byte
	Decoded decodedText
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
	if abs, err := filepath.Abs(path); err == nil {
		return filepath.Clean(abs)
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

func invalidatePathCache(path string) {
	key := normalizeCachePath(path)
	pathDetectionCacheMu.Lock()
	defer pathDetectionCacheMu.Unlock()
	pathDetectionCache.Remove(key)
}

func warmDecodedFileCacheAsync(path string, encodedBytes []byte, decoded decodedText) {
	invalidatePathCache(path)
	go func() {
		hash := cacheKeyForBytes(encodedBytes)
		cacheSuccess(hash, decoded)
		info, err := os.Stat(path)
		if err != nil {
			return
		}
		setPathCache(path, pathCacheEntry{Size: info.Size(), ModTime: info.ModTime().UnixNano(), Hash: hash})
	}()
}

// ReadDecodedTextFile reads and decodes a text file, reusing the two-level cache.
// On path+hash cache hit it returns without re-reading the file body.
func ReadDecodedTextFile(path string) (decodedText, error) {
	d, _, err := readDecodedTextFile(path, false)
	return d, err
}

// ReadAndDecodeTextFile reads a text file and returns both decoded text and raw bytes.
// Use this only when the caller truly needs the raw bytes (for example, to report byte counts).
func ReadAndDecodeTextFile(path string) (decodedText, []byte, error) {
	return readDecodedTextFile(path, true)
}

// readDecodedTextFile loads and decodes path. When returnRaw is true, the raw file bytes
// are returned (single ReadFile on cache miss; one ReadFile on cache hit when raw needed).
func readDecodedTextFile(path string, returnRaw bool) (decodedText, []byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return decodedText{}, nil, err
	}
	if entry, ok := getPathCache(path); ok {
		if entry.Size == info.Size() && entry.ModTime == info.ModTime().UnixNano() {
			if dec, ok := loadDecodedFromHash(entry.Hash); ok {
				if goStrictUTF8Path(path) && isLegacyEncoding(dec.Encoding) {
					// Miss: same bytes may have been cached via a non-strict read (legacy decode).
				} else if !returnRaw {
					return dec, nil, nil
				} else {
					data, rerr := os.ReadFile(path)
					if rerr != nil {
						return decodedText{}, nil, rerr
					}
					return dec, data, nil
				}
			}
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return decodedText{}, nil, err
	}
	decoded, err := decodeTextBytes(data, path)
	if err != nil {
		return decodedText{}, nil, err
	}
	hash := cacheKeyForBytes(data)
	setPathCache(path, pathCacheEntry{Size: info.Size(), ModTime: info.ModTime().UnixNano(), Hash: hash})
	if !returnRaw {
		return decoded, nil, nil
	}
	return decoded, data, nil
}

func loadDecodedFromHash(hash [32]byte) (decodedText, bool) {
	cached, ok := getDecodedCache().Get(hashKey(hash))
	if !ok {
		return decodedText{}, false
	}
	if time.Now().After(cached.ExpiresAt) {
		getDecodedCache().Del(hashKey(hash))
		return decodedText{}, false
	}
	entry := cached.Entry
	if !entry.Valid || entry.Binary {
		return decodedText{}, false
	}
	return entry.Decoded, true
}

// goStrictUTF8Path reports paths for which the Go toolchain expects UTF-8 text; we skip
// legacy encoding probes so invalid UTF-8 fails fast instead of mis-decoding.
func goStrictUTF8Path(path string) bool {
	base := strings.ToLower(filepath.Base(path))
	switch base {
	case "go.mod", "go.sum", "go.work":
		return true
	default:
		return strings.HasSuffix(strings.ToLower(path), ".go")
	}
}

// decodeTextBytes decodes file bytes to logical Unicode text. filePath should be the
// on-disk path when decoding a named file (enables Go-source UTF-8 fast-fail); use ""
// for raw bytes without path semantics (legacy probe allowed).
func decodeTextBytes(data []byte, filePath string) (decodedText, error) {
	if len(data) == 0 {
		return decodedText{Encoding: utf8Encoding}, nil
	}

	hash := cacheKeyForBytes(data)
	if cached, ok := getDecodedCache().Get(hashKey(hash)); ok {
		if time.Now().After(cached.ExpiresAt) {
			getDecodedCache().Del(hashKey(hash))
		} else if cached.Entry.Valid {
			if cached.Entry.Binary {
				return decodedText{}, fmt.Errorf("%w: content appears to be binary", ErrBinaryFile)
			}
			dec := cached.Entry.Decoded
			if filePath != "" && goStrictUTF8Path(filePath) && isLegacyEncoding(dec.Encoding) {
				// Do not reuse a legacy decode when reading Go module/source paths.
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
	if looksBinary(data) {
		cacheBinary(hash)
		return decodedText{}, fmt.Errorf("%w: content appears to be binary", ErrBinaryFile)
	}
	if utf8.Valid(data) {
		decoded := decodedText{Text: string(data), Encoding: utf8Encoding}
		cacheSuccess(hash, decoded)
		return decoded, nil
	}
	if filePath != "" && goStrictUTF8Path(filePath) {
		return decodedText{}, fmt.Errorf("invalid UTF-8 in Go source file %q (Go requires UTF-8; skipped legacy encoding detection)", filepath.Clean(filePath))
	}
	if decoded, ok := detectLegacyEncoding(data); ok {
		cacheSuccess(hash, decoded)
		return decoded, nil
	}
	return decodedText{}, fmt.Errorf("file is not valid UTF-8/BOM Unicode and no supported legacy text encoding matched")
}

func decodedEntryCost(entry encodingCacheEntry) int64 {
	if entry.Binary {
		return 1
	}
	return int64(len(entry.Decoded.Text)) + 128
}

func cacheSuccess(hash [32]byte, decoded decodedText) {
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

func cacheDecoded(hash [32]byte, decoded decodedText, err error) {
	if err != nil {
		return
	}
	cacheSuccess(hash, decoded)
}

// DecodeTextBytesForAgent exposes file decoding for agent-side diff helpers.
func DecodeTextBytesForAgent(data []byte) (string, error) {
	decoded, err := decodeTextBytes(data, "")
	if err != nil {
		return "", err
	}
	return decoded.Text, nil
}

func decodeToolStringArg(raw string) (string, error) {
	if !utf8.ValidString(raw) {
		return "", fmt.Errorf("tool argument is not valid UTF-8")
	}
	return raw, nil
}

// DecodeToolStringArgForAgent exposes tool-argument decoding for agent-side helpers.
func DecodeToolStringArgForAgent(raw string) (string, error) {
	return decodeToolStringArg(raw)
}

func detectBOMEncoding(data []byte) (textEncoding, bool) {
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
		return textEncoding{}, false
	}
}

func decodeWithEncoding(data []byte, enc textEncoding) (decodedText, error) {
	body := data
	if len(enc.BOM) > 0 && bytes.HasPrefix(body, enc.BOM) {
		body = body[len(enc.BOM):]
	}
	var text string
	if enc.Enc == nil {
		if !utf8.Valid(body) {
			return decodedText{}, fmt.Errorf("text is not valid UTF-8")
		}
		text = string(body)
	} else {
		decoded, _, err := transform.Bytes(enc.Enc.NewDecoder(), body)
		if err != nil {
			return decodedText{}, err
		}
		text = string(decoded)
	}
	if err := validateDecodedText(text, isLegacyEncoding(enc)); err != nil {
		return decodedText{}, err
	}
	if _, err := encodeString(text, enc); err != nil {
		return decodedText{}, err
	}
	return decodedText{Text: text, Encoding: enc}, nil
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

func encodeString(text string, enc textEncoding) ([]byte, error) {
	var body []byte
	if enc.Enc == nil {
		if !utf8.ValidString(text) {
			return nil, fmt.Errorf("text is not valid UTF-8")
		}
		body = []byte(text)
	} else {
		encoded, _, err := transform.Bytes(enc.Enc.NewEncoder(), []byte(text))
		if err != nil {
			return nil, err
		}
		body = encoded
	}
	if len(enc.BOM) == 0 {
		return body, nil
	}
	out := make([]byte, 0, len(enc.BOM)+len(body))
	out = append(out, enc.BOM...)
	out = append(out, body...)
	return out, nil
}

func isLegacyEncoding(enc textEncoding) bool {
	return enc.Name == gb18030Encoding.Name || enc.Name == big5Encoding.Name || enc.Name == shiftJISEncoding.Name
}

func detectLegacyEncoding(data []byte) (decodedText, bool) {
	if enc, ok := detectLegacyEncodingByChardet(data); ok {
		if decoded, err := decodeWithEncoding(data, enc); err == nil {
			return decoded, true
		}
	}
	enc, ok := detectLegacyEncodingByHeuristic(data)
	if !ok {
		return decodedText{}, false
	}
	decoded, err := decodeWithEncoding(data, enc)
	if err != nil {
		return decodedText{}, false
	}
	return decoded, true
}

func detectLegacyEncodingByChardet(data []byte) (textEncoding, bool) {
	results := chardet.DetectAll(limitBytes(data, chardetSampleBytes))
	bestConfidence := 0.0
	var best textEncoding
	for _, result := range results {
		enc, ok := mapDetectedEncoding(result.Encoding)
		if !ok {
			continue
		}
		if result.Confidence > bestConfidence {
			bestConfidence = result.Confidence
			best = enc
		}
	}
	if bestConfidence >= 0.85 {
		return best, true
	}
	return textEncoding{}, false
}

func detectLegacyEncodingByHeuristic(data []byte) (textEncoding, bool) {
	bestScore := -1 << 30
	secondBest := -1 << 30
	var best textEncoding
	for _, enc := range legacyEncodings {
		decoded, err := decodeWithEncoding(data, enc)
		if err != nil {
			continue
		}
		score := scoreDecodedText(decoded.Text, enc)
		if score > bestScore {
			secondBest = bestScore
			bestScore = score
			best = enc
		} else if score > secondBest {
			secondBest = score
		}
	}
	if bestScore <= 0 {
		return textEncoding{}, false
	}
	if secondBest > bestScore-10 {
		return textEncoding{}, false
	}
	return best, true
}

func scoreDecodedText(text string, enc textEncoding) int {
	var kana, halfwidthKana, han int
	var simplifiedHits, traditionalHits int
	for _, r := range text {
		switch {
		case unicode.Is(unicode.Hiragana, r), unicode.Is(unicode.Katakana, r):
			kana++
		case r >= 0xFF66 && r <= 0xFF9F:
			halfwidthKana++
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
		score += kana*8 + halfwidthKana*6
		if kana+halfwidthKana > 0 {
			score += 24
		}
	case gb18030Encoding.Name:
		score += simplifiedHits * 8
		score -= traditionalHits * 2
		score -= kana * 2
		score -= halfwidthKana * 3
	case big5Encoding.Name:
		score += traditionalHits * 8
		score -= simplifiedHits * 2
		score -= kana * 2
		score -= halfwidthKana * 3
	}
	return score
}

func mapDetectedEncoding(name string) (textEncoding, bool) {
	switch strings.ToUpper(strings.TrimSpace(name)) {
	case "GB2312", "GBK", "GB18030":
		return gb18030Encoding, true
	case "BIG5":
		return big5Encoding, true
	case "SHIFT_JIS", "CP932":
		return shiftJISEncoding, true
	default:
		return textEncoding{}, false
	}
}

func looksBinary(data []byte) bool {
	sample := limitBytes(data, binarySampleBytes)
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

func textEncodingByName(name string) (textEncoding, bool) {
	enc, ok := knownTextEncoding[strings.ToLower(name)]
	return enc, ok
}

func mustEncodeForTest(s string, encName string) []byte {
	enc, ok := textEncodingByName(encName)
	if !ok {
		panic("unsupported test encoding: " + encName)
	}
	data, err := encodeString(s, enc)
	if err != nil {
		panic(err)
	}
	return data
}
