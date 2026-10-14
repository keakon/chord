package imagecache

import (
	"bytes"
	"image"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestWarmPinnedFileSourceDoesNotRepeatIOOrDecode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sample.png")
	data := makeTestPNG(t)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	source := Source{Path: path, MIME: "image/png"}
	var err error
	source.Key, err = FreshKey(source)
	if err != nil {
		t.Fatal(err)
	}
	store := New()
	entry, err := store.Lookup(source, false)
	if err != nil {
		t.Fatal(err)
	}
	config, format, err := entry.Config(source)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := entry.Raw(source)
	if err != nil || !bytes.Equal(raw, data) {
		t.Fatalf("initial raw data differs: %v", err)
	}
	encoded, size, err := entry.Base64PNG(source)
	if err != nil || encoded == "" || size == 0 {
		t.Fatalf("initial transport is unavailable: %v", err)
	}

	stat, open, read := imageCacheStat, imageCacheOpen, imageCacheReadFile
	decodeConfig, decode, encode := imageCacheDecodeConfig, imageCacheDecode, imageCacheEncodePNG
	defer func() {
		imageCacheStat, imageCacheOpen, imageCacheReadFile = stat, open, read
		imageCacheDecodeConfig, imageCacheDecode, imageCacheEncodePNG = decodeConfig, decode, encode
	}()
	imageCacheStat = func(string) (os.FileInfo, error) {
		t.Fatal("warm pinned source performed stat")
		return nil, nil
	}
	imageCacheOpen = func(string) (*os.File, error) {
		t.Fatal("warm pinned source opened a file header")
		return nil, nil
	}
	imageCacheReadFile = func(string) ([]byte, error) {
		t.Fatal("warm pinned source read file bytes")
		return nil, nil
	}
	imageCacheDecodeConfig = func(io.Reader) (image.Config, string, error) {
		t.Fatal("warm pinned source decoded its header")
		return image.Config{}, "", nil
	}
	imageCacheDecode = func([]byte) (image.Image, string, error) {
		t.Fatal("warm pinned source decoded pixels")
		return nil, "", nil
	}
	imageCacheEncodePNG = func(io.Writer, image.Image) error {
		t.Fatal("warm pinned source encoded transport PNG")
		return nil
	}

	for range 2 {
		cached, err := store.Lookup(source, false)
		if err != nil || cached != entry {
			t.Fatalf("warm lookup did not reuse entry: %v", err)
		}
		gotConfig, gotFormat, err := cached.Config(source)
		if err != nil || gotConfig.Width != config.Width || gotConfig.Height != config.Height || gotFormat != format {
			t.Fatalf("warm config changed: %+v %q %v", gotConfig, gotFormat, err)
		}
		gotRaw, err := cached.Raw(source)
		if err != nil || !bytes.Equal(gotRaw, data) {
			t.Fatalf("warm raw bytes changed: %v", err)
		}
		gotEncoded, gotSize, err := cached.Base64PNG(source)
		if err != nil || gotEncoded != encoded || gotSize != size {
			t.Fatalf("warm transport changed: size=%d err=%v", gotSize, err)
		}
	}
}
