package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	textunicode "golang.org/x/text/encoding/unicode"
	"golang.org/x/text/encoding/unicode/utf32"
)

func clearEncodingCaches() {
	pathDetectionCacheMu.Lock()
	pathDetectionCache.Purge()
	pathDetectionCacheMu.Unlock()
	if decodedCache != nil {
		decodedCache.Clear()
	}
}

func TestWriteToolDescriptionClarifiesFullReplacementWithoutDelete(t *testing.T) {
	desc := (WriteTool{}).Description()
	for _, want := range []string{
		"Prefer Edit for localized changes to existing files.",
		"use Write directly rather than deleting it first",
		"use Delete only when the file should no longer exist",
	} {
		if !strings.Contains(desc, want) {
			t.Fatalf("Description() missing %q in %q", want, desc)
		}
	}
}

func TestEditToolRejectsBinaryFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "broken.bin")
	data := append([]byte("prefix"), 0x00, 0x01, 0x02)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	args, err := json.Marshal(map[string]any{
		"path":       path,
		"old_string": "prefix",
		"new_string": "fixed",
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	_, err = (EditTool{}).Execute(context.Background(), args)
	if err == nil || !strings.Contains(err.Error(), "binary file") {
		t.Fatalf("err = %v, want binary-file refusal", err)
	}
}

func TestWriteToolResultOmitsUTF8Encoding(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.txt")
	args, err := json.Marshal(map[string]any{
		"path":    path,
		"content": "ab",
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	got, err := (WriteTool{}).Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("WriteTool.Execute: %v", err)
	}
	want := "Successfully wrote 2 bytes"
	if got != want {
		t.Fatalf("result = %q, want %q", got, want)
	}
	if strings.Contains(strings.ToLower(got), "encoding") {
		t.Fatalf("result should not mention encoding for UTF-8: %q", got)
	}
}

func TestEditToolResultOmitsUTF8Encoding(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "utf8.txt")
	if err := os.WriteFile(path, []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	args, err := json.Marshal(map[string]any{
		"path":       path,
		"old_string": "hello",
		"new_string": "hi",
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	got, err := (EditTool{}).Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("EditTool.Execute: %v", err)
	}
	if !strings.HasPrefix(got, "Replaced 1 occurrence (") {
		t.Fatalf("unexpected prefix: %q", got)
	}
	if strings.Contains(got, "encoding=") {
		t.Fatalf("utf-8 edit should omit encoding in result: %q", got)
	}
}

func TestEditToolResultIncludesNonUTF8Encoding(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gb18030.txt")
	data := mustEncodeForTest("第一行\n第二行\n", "gb18030")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	args, err := json.Marshal(map[string]any{
		"path":       path,
		"old_string": "第二行",
		"new_string": "第三行",
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	got, err := (EditTool{}).Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("EditTool.Execute: %v", err)
	}
	if !strings.Contains(got, ", encoding=gb18030") {
		t.Fatalf("expected non-utf8 encoding in result, got %q", got)
	}
}

func TestReadToolDecodesUTF16LEBOMFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "utf16le.txt")
	enc := textEncoding{Name: "utf-16le", Enc: textunicode.UTF16(textunicode.LittleEndian, textunicode.IgnoreBOM), BOM: []byte{0xFF, 0xFE}}
	data, err := encodeString("hello\n世界\n", enc)
	if err != nil {
		t.Fatalf("encodeString: %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	args, err := json.Marshal(map[string]any{"path": path})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	got, err := (ReadTool{}).Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("ReadTool.Execute: %v", err)
	}
	if !strings.Contains(got, "(encoding: utf-16le)") {
		t.Fatalf("expected utf-16le header, got %q", got)
	}
	if !strings.Contains(got, "世界") {
		t.Fatalf("expected decoded UTF-16 text, got %q", got)
	}
}

func TestReadToolDecodesUTF32BEBOMFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "utf32be.txt")
	enc := textEncoding{Name: "utf-32be", Enc: utf32.UTF32(utf32.BigEndian, utf32.IgnoreBOM), BOM: []byte{0x00, 0x00, 0xFE, 0xFF}}
	data, err := encodeString("alpha\nβeta\n", enc)
	if err != nil {
		t.Fatalf("encodeString: %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	args, err := json.Marshal(map[string]any{"path": path})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	got, err := (ReadTool{}).Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("ReadTool.Execute: %v", err)
	}
	if !strings.Contains(got, "(encoding: utf-32be)") {
		t.Fatalf("expected utf-32be header, got %q", got)
	}
	if !strings.Contains(got, "βeta") {
		t.Fatalf("expected decoded UTF-32 text, got %q", got)
	}
}

func TestReadToolDecodesGB18030File(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gb18030.txt")
	wantText := "第一行\n第二行\n"
	data := mustEncodeForTest(wantText, "gb18030")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	args, err := json.Marshal(map[string]any{"path": path})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	got, err := (ReadTool{}).Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("ReadTool.Execute: %v", err)
	}
	if !strings.Contains(got, "(encoding: gb18030)") {
		t.Fatalf("expected encoding header, got %q", got)
	}
	if !strings.Contains(got, "第一行") || !strings.Contains(got, "第二行") {
		t.Fatalf("expected decoded GB18030 text, got %q", got)
	}
}

func TestReadToolDecodesBig5File(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "big5.txt")
	wantText := "第一行\n第二行\n"
	data := mustEncodeForTest(wantText, "big5")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	args, err := json.Marshal(map[string]any{"path": path})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	got, err := (ReadTool{}).Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("ReadTool.Execute: %v", err)
	}
	if !strings.Contains(got, "(encoding: big5)") {
		t.Fatalf("expected encoding header, got %q", got)
	}
	if !strings.Contains(got, "第一行") || !strings.Contains(got, "第二行") {
		t.Fatalf("expected decoded Big5 text, got %q", got)
	}
}

func TestReadToolDecodesShiftJISFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "shiftjis.txt")
	wantText := "これは日本語のテキストです。設定ファイルを読み込み、必要な項目を確認してください。\nもう一行追加します。\n"
	data := mustEncodeForTest(wantText, "shift-jis")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	args, err := json.Marshal(map[string]any{"path": path})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	got, err := (ReadTool{}).Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("ReadTool.Execute: %v", err)
	}
	if !strings.Contains(got, "(encoding: shift-jis)") {
		t.Fatalf("expected encoding header, got %q", got)
	}
	if !strings.Contains(got, "日本語") || !strings.Contains(got, "必要な項目") {
		t.Fatalf("expected decoded Shift-JIS text, got %q", got)
	}
}

func TestReadDecodedTextFileUsesPathSnapshotCache(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gb18030.txt")
	data := mustEncodeForTest("第一行\n第二行\n", "gb18030")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	hash := cacheKeyForBytes(data)
	clearEncodingCaches()

	decoded, err := ReadDecodedTextFile(path)
	if err != nil {
		t.Fatalf("ReadDecodedTextFile first: %v", err)
	}
	if decoded.Encoding.Name != "gb18030" {
		t.Fatalf("encoding = %q, want gb18030", decoded.Encoding.Name)
	}
	cached, ok := getPathCache(path)
	if !ok {
		t.Fatal("expected path cache entry")
	}
	entry := cached
	if entry.Size != info.Size() || entry.ModTime != info.ModTime().UnixNano() || entry.Hash != hash {
		t.Fatalf("unexpected path cache entry: %#v", entry)
	}

	decoded2, err := ReadDecodedTextFile(path)
	if err != nil {
		t.Fatalf("ReadDecodedTextFile second: %v", err)
	}
	if decoded2.Encoding.Name != "gb18030" || decoded2.Text != decoded.Text {
		t.Fatalf("second decode = %#v, want %#v", decoded2, decoded)
	}
}

func TestReadAndDecodeTextFileReturnsBytesWhenNeeded(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bytes.txt")
	data := []byte("hello\nworld\n")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	decoded, gotBytes, err := ReadAndDecodeTextFile(path)
	if err != nil {
		t.Fatalf("ReadAndDecodeTextFile: %v", err)
	}
	if decoded.Text != "hello\nworld\n" {
		t.Fatalf("decoded text = %q", decoded.Text)
	}
	if string(gotBytes) != string(data) {
		t.Fatalf("bytes mismatch: got %q want %q", string(gotBytes), string(data))
	}
}

func TestWriteToolWarmsPathCacheAsync(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "new.txt")
	clearEncodingCaches()
	args, err := json.Marshal(map[string]any{
		"path":    path,
		"content": "hello\nworld\n",
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if _, err := (WriteTool{}).Execute(context.Background(), args); err != nil {
		t.Fatalf("WriteTool.Execute: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		if cached, ok := getPathCache(path); ok {
			entry := cached
			if decoded, ok := loadDecodedFromHash(entry.Hash); ok && decoded.Text == "hello\nworld\n" {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for async cache warmup")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestEditToolWarmsPathCacheAsync(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "legacy.txt")
	data := mustEncodeForTest("第一行\n第二行\n", "gb18030")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	clearEncodingCaches()
	args, err := json.Marshal(map[string]any{
		"path":       path,
		"old_string": "第二行",
		"new_string": "第三行",
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if _, err := (EditTool{}).Execute(context.Background(), args); err != nil {
		t.Fatalf("EditTool.Execute: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		if cached, ok := getPathCache(path); ok {
			entry := cached
			if decoded, ok := loadDecodedFromHash(entry.Hash); ok && decoded.Text == "第一行\n第三行\n" && decoded.Encoding.Name == "gb18030" {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for async edit cache warmup")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
