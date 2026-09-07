package llm

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/keakon/chord/internal/config"
)

func TestCompressRequestBody_Disabled(t *testing.T) {
	body := []byte(`{"model":"test"}`)
	req := httptest.NewRequest(http.MethodPost, "/test", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	outReq, outBody := compressRequestBody(req, body, "")
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

	outReq, outBody := compressRequestBody(req, body, config.RequestCompressionGzip)

	if outReq == req {
		t.Fatal("request should be replaced when gzip is enabled")
	}
	if len(outBody) >= len(body) {
		t.Fatalf("compressed body (%d) should be smaller than original (%d)", len(outBody), len(body))
	}

	// Verify Content-Encoding header
	if got := outReq.Header.Get("Content-Encoding"); got != config.RequestCompressionGzip {
		t.Errorf("Content-Encoding = %q, want %q", got, config.RequestCompressionGzip)
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

func TestCompressRequestBody_Zstd(t *testing.T) {
	// Use a body large enough that zstd will actually reduce size.
	body := bytes.Repeat([]byte(`{"model":"test","content":"`+`a"`), 200)
	req := httptest.NewRequest(http.MethodPost, "/test", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-key")

	outReq, outBody := compressRequestBody(req, body, config.RequestCompressionZstd)

	if outReq == req {
		t.Fatal("request should be replaced when zstd is enabled")
	}
	if len(outBody) >= len(body) {
		t.Fatalf("compressed body (%d) should be smaller than original (%d)", len(outBody), len(body))
	}

	// Verify Content-Encoding header
	if got := outReq.Header.Get("Content-Encoding"); got != config.RequestCompressionZstd {
		t.Errorf("Content-Encoding = %q, want %q", got, config.RequestCompressionZstd)
	}

	// Verify original headers are preserved and the response direction still
	// advertises gzip only (zstd responses are neither requested nor decoded).
	if got := outReq.Header.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want %q", got, "application/json")
	}
	if got := outReq.Header.Get("Authorization"); got != "Bearer test-key" {
		t.Errorf("Authorization = %q, want %q", got, "Bearer test-key")
	}
	if got := outReq.Header.Get("Accept-Encoding"); got != "gzip" {
		t.Errorf("Accept-Encoding = %q, want %q", got, "gzip")
	}

	// Verify the compressed body decompresses correctly.
	reader, err := zstd.NewReader(bytes.NewReader(outBody))
	if err != nil {
		t.Fatalf("zstd reader error: %v", err)
	}
	decompressed, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("zstd read error: %v", err)
	}
	reader.Close()

	if string(decompressed) != string(body) {
		t.Errorf("decompressed body does not match original")
	}
}

func TestCompressRequestBody_SmallBody(t *testing.T) {
	// Small body — gzip may not reduce size, should send uncompressed
	body := []byte(`{}`)
	req := httptest.NewRequest(http.MethodPost, "/test", bytes.NewReader(body))

	_, outBody := compressRequestBody(req, body, config.RequestCompressionGzip)

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

func TestZstdCompress(t *testing.T) {
	data := bytes.Repeat([]byte("hello world "), 1000)
	compressed, err := zstdCompress(data)
	if err != nil {
		t.Fatalf("zstdCompress error: %v", err)
	}
	if len(compressed) >= len(data) {
		t.Fatalf("compressed (%d) should be smaller than original (%d)", len(compressed), len(data))
	}

	reader, err := zstd.NewReader(bytes.NewReader(compressed))
	if err != nil {
		t.Fatalf("zstd reader error: %v", err)
	}
	decompressed, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("zstd read error: %v", err)
	}
	reader.Close()

	if !bytes.Equal(decompressed, data) {
		t.Error("decompressed data does not match original")
	}
}

func TestDefaultHTTPClientSendsOnlyGzipAcceptEncoding(t *testing.T) {
	var gotAcceptEncoding string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAcceptEncoding = r.Header.Get("Accept-Encoding")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	client, err := NewHTTPClientWithProxy("direct", 5*time.Second)
	if err != nil {
		t.Fatalf("NewHTTPClientWithProxy returned error: %v", err)
	}

	req, err := http.NewRequest(http.MethodPost, server.URL, bytes.NewReader([]byte(`{"model":"test"}`)))
	if err != nil {
		t.Fatalf("NewRequest returned error: %v", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("client.Do returned error: %v", err)
	}
	resp.Body.Close()

	if gotAcceptEncoding != "gzip" {
		t.Fatalf("Accept-Encoding = %q, want %q", gotAcceptEncoding, "gzip")
	}
}

func TestProviderConfig_RequestCompression(t *testing.T) {
	p := NewProviderConfig("test", config.ProviderConfig{Type: config.ProviderTypeChatCompletions}, []string{"key"})

	if enc := p.RequestCompression(); enc != "" {
		t.Errorf("default RequestCompression = %q, want empty", enc)
	}

	p.SetRequestCompression(config.RequestCompressionZstd)
	if enc := p.RequestCompression(); enc != config.RequestCompressionZstd {
		t.Errorf("after SetRequestCompression(zstd), got %q, want %q", enc, config.RequestCompressionZstd)
	}

	p.SetRequestCompression("")
	if enc := p.RequestCompression(); enc != "" {
		t.Errorf("after SetRequestCompression(\"\"), got %q, want empty", enc)
	}
}

func TestProviderConfig_RequestCompressionFromProviderConfig(t *testing.T) {
	p := NewProviderConfig("test", config.ProviderConfig{Type: config.ProviderTypeChatCompletions, Compress: config.RequestCompressionZstd}, []string{"key"})
	if enc := p.RequestCompression(); enc != config.RequestCompressionZstd {
		t.Fatalf("RequestCompression = %q, want %q", enc, config.RequestCompressionZstd)
	}
}
