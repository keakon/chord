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
	regionalEncodings = []textEncoding{gb18030Encoding, big5Encoding, shiftJISEncoding}
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
	if abs, err := resolveToolPathAbs(path); err == nil {
		return filepath.Clean(abs)
	}
	if resolved, err := resolveToolPath(path); err == nil {
		return resolved
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

func warmDecodedFileCache(path string, encodedBytes []byte, decoded decodedText) {
	invalidatePathCache(path)
	cacheSuccess(cacheKeyForBytes(encodedBytes), decoded)
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
				if goStrictUTF8Path(path) && isRegionalEncoding(dec.Encoding) {
					// Miss: same bytes may have been cached through a regional fallback.
				} else if !returnRaw {
					return dec, nil, nil
				} else {
					data, rerr := os.ReadFile(path)
					if rerr != nil {
						return decodedText{}, nil, rerr
					}
					hash := cacheKeyForBytes(data)
					if hash == entry.Hash {
						return dec, data, nil
					}
					decoded, derr := decodeTextBytes(data, path)
					if derr != nil {
						return decodedText{}, nil, derr
					}
					setPathCache(path, pathCacheEntry{Size: int64(len(data)), ModTime: info.ModTime().UnixNano(), Hash: hash})
					return decoded, data, nil
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
// on-disk path when decoding a named file.
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
		return decodedText{}, fmt.Errorf("invalid UTF-8 in Go source file %q (Go requires UTF-8; skipped regional encoding detection)", filepath.Clean(filePath))
	}
	if decoded, ok := detectRegionalEncoding(data); ok {
		cacheSuccess(hash, decoded)
		return decoded, nil
	}
	if filePath != "" {
		return decodedText{}, fmt.Errorf("file %q is not valid UTF-8/BOM Unicode and no supported regional text encoding matched", filepath.Clean(filePath))
	}
	return decodedText{}, fmt.Errorf("content is not valid UTF-8/BOM Unicode and no supported regional text encoding matched")
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

func decodeToolStringArg(raw string) (string, error) {
	if !utf8.ValidString(raw) {
		return "", fmt.Errorf("tool argument is not valid UTF-8")
	}
	return raw, nil
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
	if err := validateDecodedText(text, isRegionalEncoding(enc)); err != nil {
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

func isRegionalEncoding(enc textEncoding) bool {
	return enc.Name == gb18030Encoding.Name || enc.Name == big5Encoding.Name || enc.Name == shiftJISEncoding.Name
}

func detectRegionalEncoding(data []byte) (decodedText, bool) {
	bestScore := -1 << 30
	secondBest := -1 << 30
	var best decodedText
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
		return decodedText{}, false
	}
	if secondBest > bestScore-10 {
		return decodedText{}, false
	}
	return best, true
}

func scoreDecodedText(text string, enc textEncoding) int {
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

// StripOrphanVariationSelectors drops variation selectors (U+FE0E/U+FE0F) that
// are not preceded by a base character able to carry an emoji or text
// presentation. Models occasionally emit these orphans (e.g. "️0" instead of
// "0"): inside edit/apply_patch arguments they make matching fail because the
// file content does not contain the selector, and inside rendered text they
// are charged one display column by the width library while the terminal
// paints them zero-width, under-filling card backgrounds by that column.
//
// Stripping a selector that was *not* orphaned costs a column in the other
// direction — "1️⃣" measures 2 and paints 2, while a stripped "1⃣" measures 1
// and still paints 2 — so canTakeVariationSelector follows Unicode's own list
// of bases rather than a looser guess.
func StripOrphanVariationSelectors(s string) string {
	if !strings.ContainsRune(s, '\ufe0f') && !strings.ContainsRune(s, '\ufe0e') {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	prev := rune(-1)
	for i, r := range s {
		if r == '\ufe0f' || r == '\ufe0e' {
			// Keycap bases carry a selector only as part of the full sequence,
			// so they need the one rune of lookahead the category test cannot
			// do: "1\ufe0f\u20e3" is an emoji, a bare "1\ufe0f" is a defective
			// keycap and in practice an orphan.
			keycap := r == '\ufe0f' && isKeycapBase(prev) &&
				strings.HasPrefix(s[i+utf8.RuneLen(r):], string(combiningEnclosingKeycap))
			if !keycap && !canTakeVariationSelector(prev) {
				continue
			}
		}
		b.WriteRune(r)
		prev = r
	}
	return b.String()
}

// combiningEnclosingKeycap completes a keycap emoji: base + U+FE0F + U+20E3.
const combiningEnclosingKeycap = '\u20e3'

// validateWritableText rejects text that cannot be written to a plain text
// file as-is: NUL and the C0 control characters other than the whitespace set
// ordinary text files use (\t \n \f \r). Tool arguments are JSON strings, so
// models cannot send real binary data — when they emit control characters it
// is malfunction residue, and silently cleaning them could just as well
// corrupt intended content. Rejecting routes the model to a shell command or
// script, which is the correct channel for binary files.
func validateWritableText(s string) error {
	for _, r := range s {
		if r < 0x20 && r != '\t' && r != '\n' && r != '\f' && r != '\r' {
			return fmt.Errorf("contains control character %#U; binary or control-character content cannot be written with the text file tools — write it with a shell command or script instead", r)
		}
	}
	return nil
}

// isKeycapBase reports whether r is one of the twelve bases of a keycap emoji
// (# * 0-9 followed by U+FE0F U+20E3). They are the only ASCII characters
// Unicode gives an emoji variation sequence, and canTakeVariationSelector
// deliberately excludes them because they are valid only with the U+20E3.
func isKeycapBase(r rune) bool {
	return r == '#' || r == '*' || (r >= '0' && r <= '9')
}

// canTakeVariationSelector reports whether r may legitimately be followed by an
// emoji/text presentation selector. Unicode standardizes 371 such bases in
// emoji-variation-sequences.txt; 354 of them are symbols, so the symbol
// categories carry the rule — So and Sk cover U+26A0 WARNING SIGN in "⚠️" and
// the rest of the misc-symbols bases, and Sm is required too because the arrow
// emoji are split across categories (U+2195 "↕️" is So while U+2194 "↔️" is Sm).
// Of the seventeen non-symbol bases, twelve are the keycap ones the caller
// handles and the five listed here are classed as punctuation, a dash, or even
// a letter (U+2139 "ℹ️").
//
// ASCII is rejected outright: its only bases are the keycap ones, so a selector
// after Sm members such as "+" or "=" is an orphan — "+ ️1", a dropped emoji
// next to a plus, is one of the shapes actually observed.
//
// The rule still over-allows, since So/Sk/Sm hold far more members than the 354
// symbol bases: a selector orphaned after "°" or "±" survives. That is the
// deliberate direction. Both mistakes cost a column — a kept orphan is measured
// one wider than it paints, a wrongly stripped selector one narrower — but the
// kept orphan leaves the text intact, while an over-eager strip rewrites what
// the model wrote.
func canTakeVariationSelector(r rune) bool {
	if r < 0x80 {
		return false
	}
	switch r {
	case '‼', '⁉', 'ℹ', '〰', '〽':
		return true
	}
	return unicode.Is(unicode.So, r) || unicode.Is(unicode.Sk, r) || unicode.Is(unicode.Sm, r)
}
