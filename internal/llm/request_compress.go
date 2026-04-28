package llm

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"log/slog"
	"net/http"
)

// compressRequestBody conditionally compresses the request body when enabled.
// When enabled, the body is gzip compressed and the Content-Encoding header is set.
//
// If compression fails or doesn't reduce size, the request is sent uncompressed.
// Compression errors are logged but do not fail the request.
func compressRequestBody(req *http.Request, bodyBytes []byte, enabled bool) (*http.Request, []byte) {
	if !enabled {
		return req, bodyBytes
	}

	compressed, err := gzipCompress(bodyBytes)
	if err != nil {
		slog.Warn("gzip compression failed, sending uncompressed request", "error", err)
		return req, bodyBytes
	}
	if len(compressed) >= len(bodyBytes) {
		slog.Debug("gzip did not reduce body size, sending uncompressed",
			"original", len(bodyBytes),
			"compressed", len(compressed),
		)
		return req, bodyBytes
	}
	slog.Debug("request body compressed",
		"algorithm", "gzip",
		"original_bytes", len(bodyBytes),
		"compressed_bytes", len(compressed),
		"ratio", fmt.Sprintf("%.1f%%", float64(len(compressed))/float64(len(bodyBytes))*100),
	)
	newReq, err := http.NewRequestWithContext(req.Context(), req.Method, req.URL.String(), bytes.NewReader(compressed))
	if err != nil {
		slog.Warn("failed to create compressed request, sending uncompressed", "error", err)
		return req, bodyBytes
	}
	for k, vv := range req.Header {
		for _, v := range vv {
			newReq.Header.Add(k, v)
		}
	}
	newReq.Header.Set("Accept-Encoding", "gzip")
	newReq.Header.Set("Content-Encoding", "gzip")
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
