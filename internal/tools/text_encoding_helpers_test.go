package tools

import "strings"

// knownTextEncoding maps encoding names to textEncoding values for tests that
// drive the encoding pipeline by name. Production code resolves encodings via
// detectLegacyEncoding* / decodeText* paths that don't go through this map.
var knownTextEncoding = map[string]textEncoding{
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
