package llm

import (
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"net/http"
	"sync"

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
	compressed, contentEncoding, ok := compressBodyBytes(bodyBytes, encoding)
	if !ok {
		return req, bodyBytes
	}
	newReq, err := applyCompressedBody(req, compressed, contentEncoding)
	if err != nil {
		log.Warnf("failed to create compressed request, sending uncompressed error=%v", err)
		return req, bodyBytes
	}
	return newReq, compressed
}

// compressBodyBytes compresses the body for the provider's configured
// encoding. ok=false means compression failed or did not reduce the size, and
// the body must be sent uncompressed; the outcome is deterministic in (body
// bytes, encoding), which is what lets requestBodyReuse cache it.
func compressBodyBytes(bodyBytes []byte, encoding string) (compressed []byte, contentEncoding string, ok bool) {
	var compress func([]byte) ([]byte, error)
	switch encoding {
	case config.RequestCompressionGzip:
		compress, contentEncoding = gzipCompress, config.RequestCompressionGzip
	case config.RequestCompressionZstd:
		compress, contentEncoding = zstdCompress, config.RequestCompressionZstd
	default:
		return nil, "", false
	}

	out, err := compress(bodyBytes)
	if err != nil {
		log.Warnf("%s compression failed, sending uncompressed request error=%v", contentEncoding, err)
		return nil, "", false
	}
	if len(out) >= len(bodyBytes) {
		log.Debugf("%s did not reduce body size, sending uncompressed original=%v compressed=%v", contentEncoding, len(bodyBytes), len(out))
		return nil, "", false
	}
	log.Debugf("request body compressed algorithm=%v original_bytes=%v compressed_bytes=%v ratio=%v", contentEncoding, len(bodyBytes), len(out), fmt.Sprintf("%.1f%%", float64(len(out))/float64(len(bodyBytes))*100))
	return out, contentEncoding, true
}

// applyCompressedBody rebuilds req around a compressed body and sets the
// Content-Encoding headers. The Accept-Encoding header keeps advertising gzip
// so the response direction behaves exactly as without request compression.
func applyCompressedBody(req *http.Request, compressed []byte, contentEncoding string) (*http.Request, error) {
	newReq, err := http.NewRequestWithContext(req.Context(), req.Method, req.URL.String(), bytes.NewReader(compressed))
	if err != nil {
		return nil, err
	}
	for k, vv := range req.Header {
		for _, v := range vv {
			newReq.Header.Add(k, v)
		}
	}
	newReq.Header.Set(headerAcceptEncoding, headerValueGzip)
	newReq.Header.Set(headerContentEncoding, contentEncoding)
	return newReq, nil
}

// errZstdEncoderUnavailable is returned when the process-wide encoder could not
// be constructed; the caller falls back to an uncompressed body.
var errZstdEncoderUnavailable = errors.New("shared encoder unavailable")

// compressedBodyEstimate guesses the output size of a request body so the
// destination buffer is allocated once. Chat request bodies are JSON envelopes
// around prose and compress to well under a third; over-reserving costs one
// short-lived allocation, under-reserving costs a copy per growth step.
func compressedBodyEstimate(size int) int {
	return size/3 + 1024
}

// gzipWriterPool reuses the gzip window and Huffman tables across requests.
// A fresh writer allocates them per call, which on a large request body is the
// dominant cost of compressing it.
var gzipWriterPool = sync.Pool{New: func() any { return gzip.NewWriter(nil) }}

// gzipCompress compresses data using gzip at the default compression level.
func gzipCompress(data []byte) ([]byte, error) {
	buf := bytes.NewBuffer(make([]byte, 0, compressedBodyEstimate(len(data))))
	w, _ := gzipWriterPool.Get().(*gzip.Writer)
	w.Reset(buf)
	defer gzipWriterPool.Put(w)
	if _, err := w.Write(data); err != nil {
		return nil, fmt.Errorf("gzip write: %w", err)
	}
	if err := w.Close(); err != nil {
		return nil, fmt.Errorf("gzip close: %w", err)
	}
	return buf.Bytes(), nil
}

// sharedZstdEncoder is the process-wide zstd encoder used for one-shot request
// bodies. Constructing an encoder starts a worker per CPU and allocates the
// whole window up front — tens of megabytes on a large body — so it is built
// once. EncodeAll is safe for concurrent use and does not use those workers, so
// a single encoder serves every in-flight request.
var sharedZstdEncoder = sync.OnceValue(func() *zstd.Encoder {
	// Concurrency 1 keeps the one-time construction from spawning a goroutine
	// per CPU that EncodeAll would never use.
	enc, err := zstd.NewWriter(nil, zstd.WithEncoderConcurrency(1))
	if err != nil {
		log.Errorf("zstd encoder unavailable, request bodies will be sent uncompressed error=%v", err)
		return nil
	}
	return enc
})

// zstdCompress compresses data using zstd at the default level
// (SpeedDefault, roughly zstd level 3) — the same codec and tier the Codex
// client uses for codex-backend request bodies.
func zstdCompress(data []byte) ([]byte, error) {
	enc := sharedZstdEncoder()
	if enc == nil {
		return nil, fmt.Errorf("zstd writer: %w", errZstdEncoderUnavailable)
	}
	return enc.EncodeAll(data, make([]byte, 0, compressedBodyEstimate(len(data)))), nil
}
