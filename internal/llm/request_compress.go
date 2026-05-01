package llm

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"github.com/keakon/golog/log"
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
		log.Warnf("gzip compression failed, sending uncompressed request error=%v", err)
		return req, bodyBytes
	}
	if len(compressed) >= len(bodyBytes) {
		log.Debugf("gzip did not reduce body size, sending uncompressed original=%v compressed=%v", len(bodyBytes), len(compressed))
		return req, bodyBytes
	}
	log.Debugf("request body compressed algorithm=%v original_bytes=%v compressed_bytes=%v ratio=%v", "gzip", len(bodyBytes), len(compressed), fmt.Sprintf("%.1f%%", float64(len(compressed))/float64(len(bodyBytes))*100))
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
