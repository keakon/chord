package imageprotocol

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"strings"
	"testing"

	xansi "github.com/keakon/x/ansi"
	xiterm2 "github.com/keakon/x/ansi/iterm2"
	xkitty "github.com/keakon/x/ansi/kitty"

	"github.com/keakon/chord/internal/tui/imagecache"
)

func TestKittyFramingPreservesPayloadAndChunkMetadata(t *testing.T) {
	for _, extra := range []int{0, 15000} {
		t.Run(fmt.Sprint(extra), func(t *testing.T) {
			resetImageCache()
			defer resetImageCache()
			data := append(makeTestPNG(t), bytes.Repeat([]byte{1}, extra)...)
			part := imagecache.Source{Data: data, MIME: "image/png"}
			encoded, err := kittyTransmit(imageCache, part, 73, false)
			if err != nil {
				t.Fatal(err)
			}
			chunks := strings.Split(strings.TrimSuffix(encoded, "\x1b\\"), "\x1b\\")
			var payload strings.Builder
			for i, chunk := range chunks {
				if !strings.HasPrefix(chunk, "\x1b_G") {
					t.Fatal("invalid APC prefix")
				}
				options, content, ok := strings.Cut(strings.TrimPrefix(chunk, "\x1b_G"), ";")
				if !ok || len(content) > xkitty.MaxChunkSize {
					t.Fatal("invalid chunk size or separator")
				}
				if i == 0 && (!strings.Contains(options, "i=73") || !strings.Contains(options, "f=100")) {
					t.Fatal("first chunk lost image metadata")
				}
				if !strings.Contains(options, "q=2") {
					t.Fatal("chunk lost quiet mode")
				}
				if i < len(chunks)-1 && !strings.HasSuffix(options, "m=1") || i == len(chunks)-1 && len(chunks) > 1 && !strings.HasSuffix(options, "m=0") {
					t.Fatal("invalid continuation metadata")
				}
				if i > 0 && strings.Contains(options, "i=") {
					t.Fatal("continuation repeated image identity")
				}
				payload.WriteString(content)
			}
			decoded, err := base64.StdEncoding.DecodeString(payload.String())
			if err != nil || !bytes.Equal(decoded, data) {
				t.Fatal("chunk framing changed image bytes", err)
			}
		})
	}
}

func TestITerm2FramingMatchesLibraryEncoding(t *testing.T) {
	resetImageCache()
	defer resetImageCache()
	part := imagecache.Source{Data: makeTestPNG(t), MIME: "image/png"}
	got, err := ITerm2(imageCache, part, "sample photo.png", 31, 9, false)
	if err != nil {
		t.Fatal(err)
	}
	expected := xansi.ITerm2(xiterm2.File{Name: "sample photo.png", Size: int64(len(part.Data)), Width: xiterm2.Cells(31), Height: xiterm2.Cells(9), Inline: true, Content: []byte(base64.StdEncoding.EncodeToString(part.Data))})
	if got != expected {
		t.Fatal("framing differs from the library's wire representation")
	}
}
