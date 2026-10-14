package tools

import (
	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/japanese"
	"golang.org/x/text/encoding/simplifiedchinese"
	textunicode "golang.org/x/text/encoding/unicode"
	"golang.org/x/text/transform"
)

func mustEncodeForTest(s string, encName string) []byte {
	var enc encoding.Encoding
	var bom []byte
	switch encName {
	case "utf-16le":
		enc = textunicode.UTF16(textunicode.LittleEndian, textunicode.IgnoreBOM)
		bom = []byte{0xff, 0xfe}
	case "gb18030":
		enc = simplifiedchinese.GB18030
	case "shift-jis":
		enc = japanese.ShiftJIS
	default:
		panic("unsupported test encoding: " + encName)
	}
	data, _, err := transform.Bytes(enc.NewEncoder(), []byte(s))
	if err != nil {
		panic(err)
	}
	return append(bom, data...)
}
