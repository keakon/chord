package llm

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"net/http"

	"github.com/keakon/golog/log"
	"github.com/klauspost/compress/zstd"

	"github.com/keakon/chord/internal/config"
)

// compressRequestBody conditionally compresses the request body with the
// provider's configured encoding. encoding is "" (disabled), "gzip", or
// "zstd".
//
// If compression fails or doesn't reduce the size, the request is sent
// uncompressed. Compression errors are logged but do not fail the request.
// The Accept-Encoding header keeps advertising gzip so the response direction
// behaves exactly as without request compression.
func compressRequestBody(req *http.Request, bodyBytes []byte, encoding string) (*http.Request, []byte) {
	var compress func([]byte) ([]byte, error)
	var name, contentEncoding string
	switch encoding {
	case config.RequestCompressionGzip:
		compress, name, contentEncoding = gzipCompress, "gzip", config.RequestCompressionGzip
	case config.RequestCompressionZstd:
		compress, name, contentEncoding = zstdCompress, "zstd", config.RequestCompressionZstd
	default:
		return req, bodyBytes
	}

	compressed, err := compress(bodyBytes)
	if err != nil {
		log.Warnf("%s compression failed, sending uncompressed request error=%v", name, err)
		return req, bodyBytes
	}
	if len(compressed) >= len(bodyBytes) {
		log.Debugf("%s did not reduce body size, sending uncompressed original=%v compressed=%v", name, len(bodyBytes), len(compressed))
		return req, bodyBytes
	}
	log.Debugf("request body compressed algorithm=%v original_bytes=%v compressed_bytes=%v ratio=%v", name, len(bodyBytes), len(compressed), fmt.Sprintf("%.1f%%", float64(len(compressed))/float64(len(bodyBytes))*100))
	newReq, err := http.NewRequestWithContext(req.Context(), req.Method, req.URL.String(), bytes.NewReader(compressed))
	if err != nil {
		log.Warnf("failed to create compressed request, sending uncompressed error=%v", err)
		return req, bodyBytes
	}
	for k, vv := range req.Header {
		for _, v := range vv {
			newReq.Header.Add(k, v)
		}
	}
	newReq.Header.Set(headerAcceptEncoding, headerValueGzip)
	newReq.Header.Set(headerContentEncoding, contentEncoding)
	return newReq, compressed
}

// gzipCompress compresses data using gzip at the default compression level.
func gzipCompress(data []byte) ([]byte, error) {
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	if _, err := w.Write(data); err != nil {
		w.Close()
		return nil, fmt.Errorf("gzip write: %w", err)
	}
	if err := w.Close(); err != nil {
		return nil, fmt.Errorf("gzip close: %w", err)
	}
	return buf.Bytes(), nil
}

// zstdCompress compresses data using zstd at the default level
// (SpeedDefault, roughly zstd level 3) — the same codec and tier the Codex
// client uses for codex-backend request bodies.
func zstdCompress(data []byte) ([]byte, error) {
	var buf bytes.Buffer
	w, err := zstd.NewWriter(&buf)
	if err != nil {
		return nil, fmt.Errorf("zstd writer: %w", err)
	}
	if _, err := w.Write(data); err != nil {
		w.Close()
		return nil, fmt.Errorf("zstd write: %w", err)
	}
	if err := w.Close(); err != nil {
		return nil, fmt.Errorf("zstd close: %w", err)
	}
	return buf.Bytes(), nil
}
