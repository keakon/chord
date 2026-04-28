package llm

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/keakon/chord/internal/config"
)

func TestCompressRequestBody_Disabled(t *testing.T) {
	body := []byte(`{"model":"test"}`)
	req := httptest.NewRequest(http.MethodPost, "/test", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	outReq, outBody := compressRequestBody(req, body, false)
	if outReq != req {
		t.Error("request should be unchanged when compression is disabled")
	}
	if string(outBody) != string(body) {
		t.Error("body should be unchanged when compression is disabled")
	}
}

func TestCompressRequestBody_Gzip(t *testing.T) {
	// Use a body large enough that gzip will actually reduce size.
	body := bytes.Repeat([]byte(`{"model":"test","content":"`), 100)
	req := httptest.NewRequest(http.MethodPost, "/test", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-key")

	outReq, outBody := compressRequestBody(req, body, true)

	if outReq == req {
		t.Fatal("request should be replaced when gzip is enabled")
	}
	if len(outBody) >= len(body) {
		t.Fatalf("compressed body (%d) should be smaller than original (%d)", len(outBody), len(body))
	}

	// Verify Content-Encoding header
	if got := outReq.Header.Get("Content-Encoding"); got != "gzip" {
		t.Errorf("Content-Encoding = %q, want %q", got, "gzip")
	}

	// Verify original headers are preserved
	if got := outReq.Header.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want %q", got, "application/json")
	}
	if got := outReq.Header.Get("Authorization"); got != "Bearer test-key" {
		t.Errorf("Authorization = %q, want %q", got, "Bearer test-key")
	}

	// Verify the compressed body decompresses correctly
	reader, err := gzip.NewReader(bytes.NewReader(outBody))
	if err != nil {
		t.Fatalf("gzip reader error: %v", err)
	}
	decompressed, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("gzip read error: %v", err)
	}
	reader.Close()

	if string(decompressed) != string(body) {
		t.Errorf("decompressed body does not match original")
	}
}

func TestCompressRequestBody_Enabled(t *testing.T) {
	// Use a large enough body that gzip reduces size
	body := bytes.Repeat([]byte(`{"model":"test","content":"`+`a"`), 50)
	req := httptest.NewRequest(http.MethodPost, "/test", bytes.NewReader(body))

	outReq, outBody := compressRequestBody(req, body, true)
	if outReq == req {
		t.Error("request should be replaced when compression is enabled")
	}
	if len(outBody) >= len(body) {
		t.Error("compressed body should be smaller")
	}
}

func TestCompressRequestBody_SmallBody(t *testing.T) {
	// Small body — gzip may not reduce size, should send uncompressed
	body := []byte(`{}`)
	req := httptest.NewRequest(http.MethodPost, "/test", bytes.NewReader(body))

	_, outBody := compressRequestBody(req, body, true)

	// Small body likely won't benefit from gzip; function should fall back
	if len(outBody) > len(body) {
		t.Error("should not send a larger compressed body")
	}
}

func TestGzipCompress(t *testing.T) {
	data := bytes.Repeat([]byte("hello world "), 1000)
	compressed, err := gzipCompress(data)
	if err != nil {
		t.Fatalf("gzipCompress error: %v", err)
	}
	if len(compressed) >= len(data) {
		t.Fatalf("compressed (%d) should be smaller than original (%d)", len(compressed), len(data))
	}

	reader, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		t.Fatalf("gzip reader error: %v", err)
	}
	decompressed, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("gzip read error: %v", err)
	}
	reader.Close()

	if !bytes.Equal(decompressed, data) {
		t.Error("decompressed data does not match original")
	}
}

func TestProviderConfig_CompressEnabled(t *testing.T) {
	p := NewProviderConfig("test", config.ProviderConfig{Type: config.ProviderTypeChatCompletions}, []string{"key"})

	if enabled := p.CompressEnabled(); enabled {
		t.Errorf("default CompressEnabled = true, want false")
	}

	p.SetCompressEnabled(true)
	if enabled := p.CompressEnabled(); !enabled {
		t.Errorf("after SetCompressEnabled(true), got false, want true")
	}

	p.SetCompressEnabled(false)
	if enabled := p.CompressEnabled(); enabled {
		t.Errorf("after SetCompressEnabled(false), got true, want false")
	}
}

func TestProviderConfig_CompressFromProviderConfig(t *testing.T) {
	p := NewProviderConfig("test", config.ProviderConfig{Type: config.ProviderTypeChatCompletions, Compress: true}, []string{"key"})
	if enabled := p.CompressEnabled(); !enabled {
		t.Fatalf("CompressEnabled = false, want true")
	}
}
