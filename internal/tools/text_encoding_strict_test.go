package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGoStrictUTF8Path(t *testing.T) {
	if !goStrictUTF8Path("foo.go") {
		t.Fatal("expected .go suffix")
	}
	if !goStrictUTF8Path("FOO.GO") {
		t.Fatal("expected case-insensitive .go")
	}
	if !goStrictUTF8Path(filepath.Join("pkg", "go.mod")) {
		t.Fatal("expected go.mod")
	}
	if goStrictUTF8Path("readme.md") {
		t.Fatal("markdown should not be strict")
	}
}

func TestDecodeTextBytesGoSourceSkipsRegionalEncoding(t *testing.T) {
	raw := mustEncodeForTest("这是中文源码", "gb18030")
	_, err := decodeTextBytes(raw, filepath.Join("tmp", "x.go"))
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "skipped regional encoding detection") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDecodeTextBytesSupportsRegionalEncodings(t *testing.T) {
	tests := []struct {
		name string
		text string
	}{
		{name: "gb18030", text: "这是一个中文文件，包含页面、编码、读取和写入。"},
		{name: "big5", text: "這是一個繁體中文檔案，包含頁面、編碼、讀取和寫入。"},
		{name: "shift-jis", text: "これは日本語のファイルです。カタカナとひらがなを含みます。"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := mustEncodeForTest(tt.text, tt.name)
			decoded, err := decodeTextBytes(raw, filepath.Join("tmp", "notes.txt"))
			if err != nil {
				t.Fatalf("decodeTextBytes() error = %v", err)
			}
			if decoded.Text != tt.text {
				t.Fatalf("decoded text = %q, want %q", decoded.Text, tt.text)
			}
			if decoded.Encoding.Name != tt.name {
				t.Fatalf("encoding = %q, want %q", decoded.Encoding.Name, tt.name)
			}
			encoded, err := encodeString(decoded.Text, decoded.Encoding)
			if err != nil {
				t.Fatalf("encodeString() error = %v", err)
			}
			if string(encoded) != string(raw) {
				t.Fatal("regional encoding did not round-trip")
			}
		})
	}
}

func TestReadDecodedTextFileCachesRegionalEncodingButGoPathRechecks(t *testing.T) {
	dir := t.TempDir()
	nonGo := filepath.Join(dir, "notes.txt")
	goFile := filepath.Join(dir, "x.go")
	raw := mustEncodeForTest("这是中文文件，包含编码和读取。", "gb18030")
	if err := os.WriteFile(nonGo, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	decoded, err := ReadDecodedTextFile(nonGo)
	if err != nil {
		t.Fatalf("ReadDecodedTextFile(nonGo) error = %v", err)
	}
	if decoded.Encoding.Name != "gb18030" {
		t.Fatalf("encoding = %q, want gb18030", decoded.Encoding.Name)
	}
	if err := os.WriteFile(goFile, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = ReadDecodedTextFile(goFile)
	if err == nil {
		t.Fatal("expected Go path to reject cached regional decode")
	}
	if !strings.Contains(err.Error(), "skipped regional encoding detection") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestWarmDecodedFileCacheDoesNotCreateStalePathMapping(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "demo.txt")
	oldText := "before"
	newText := "extern"
	if len(oldText) != len(newText) {
		t.Fatal("test texts must have identical length to exercise stale path-cache risk")
	}
	if err := os.WriteFile(path, []byte(oldText), 0o644); err != nil {
		t.Fatal(err)
	}
	decoded, err := ReadDecodedTextFile(path)
	if err != nil {
		t.Fatalf("ReadDecodedTextFile(initial) error = %v", err)
	}
	if decoded.Text != oldText {
		t.Fatalf("initial decoded text = %q, want %q", decoded.Text, oldText)
	}

	warmDecodedFileCache(path, []byte(oldText), decodedText{Text: oldText, Encoding: utf8Encoding})
	if err := os.WriteFile(path, []byte(newText), 0o644); err != nil {
		t.Fatal(err)
	}

	decoded, err = ReadDecodedTextFile(path)
	if err != nil {
		t.Fatalf("ReadDecodedTextFile(after external write) error = %v", err)
	}
	if decoded.Text != newText {
		t.Fatalf("decoded text after external write = %q, want %q", decoded.Text, newText)
	}
	if entry, ok := getPathCache(path); ok {
		if entry.Hash == cacheKeyForBytes([]byte(oldText)) {
			t.Fatal("path cache should not retain stale hash for external content")
		}
	}
}
