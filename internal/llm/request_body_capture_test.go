package llm

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"

	"github.com/klauspost/compress/zstd"

	"github.com/keakon/chord/internal/config"
)

// readRequestBody returns a request body as the provider sent it before any
// request compression: a codex preset zstd-compresses bodies the way the Codex
// client does, so a test server must undo the Content-Encoding before it can
// decode the JSON. Uncompressed bodies pass through unchanged.
func readRequestBody(r *http.Request) ([]byte, error) {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	var reader io.Reader
	switch r.Header.Get("Content-Encoding") {
	case config.RequestCompressionZstd:
		dec, err := zstd.NewReader(bytes.NewReader(raw))
		if err != nil {
			return nil, err
		}
		defer dec.Close()
		reader = dec
	case config.RequestCompressionGzip:
		dec, err := gzip.NewReader(bytes.NewReader(raw))
		if err != nil {
			return nil, err
		}
		defer dec.Close()
		reader = dec
	default:
		return raw, nil
	}
	plain, err := io.ReadAll(reader)
	if err != nil {
		return nil, err
	}
	return plain, nil
}
