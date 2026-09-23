package llm

import (
	"fmt"
	"sync/atomic"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/imageutil"
	"github.com/keakon/chord/internal/message"
)

// BinaryPartResolver loads the payload of a binary part that carries only a
// file reference (ImagePath set, Data empty) and returns the provider-ready
// bytes together with the matching MIME type. Restored sessions keep
// attachment blobs on disk instead of holding every image/PDF in memory from
// startup; the wire converter resolves them through this hook at request
// time, and the resolver owns the read cache and any normalization so the wire
// layer only ever sees a validated (data, mime) pair.
type BinaryPartResolver func(part message.ContentPart) (data []byte, mimeType string, err error)

var binaryPartResolver atomic.Pointer[BinaryPartResolver]

// SetBinaryPartResolver installs the process-wide resolver. Passing nil
// removes it (tests, headless probes).
func SetBinaryPartResolver(resolver BinaryPartResolver) {
	if resolver == nil {
		binaryPartResolver.Store(nil)
		return
	}
	binaryPartResolver.Store(&resolver)
}

// BinaryPartDropReporter is notified when a binary part is omitted from a
// request because it could not be turned into a provider-ready payload. The
// agent installs it to map drops to its own counting and user-visible toasts;
// internal/llm never emits UI on its own.
type BinaryPartDropReporter func(part message.ContentPart, err error)

var binaryPartDropReporter atomic.Pointer[BinaryPartDropReporter]

// SetBinaryPartDropReporter installs the process-wide drop reporter. Passing
// nil removes it.
func SetBinaryPartDropReporter(reporter BinaryPartDropReporter) {
	if reporter == nil {
		binaryPartDropReporter.Store(nil)
		return
	}
	binaryPartDropReporter.Store(&reporter)
}

func reportBinaryPartDrop(part message.ContentPart, err error) {
	log.Warnf("omitting binary part from LLM request type=%v mime=%v path=%v error=%v", part.Type, part.MimeType, part.ImagePath, err)
	if reporter := binaryPartDropReporter.Load(); reporter != nil {
		(*reporter)(part, err)
	}
}

// binaryPartContent returns the raw wire bytes for a binary part together with
// the MIME type that describes them: the inline data when present, otherwise
// the payload resolved from the persisted file. An unreadable part is an error
// rather than a silent empty payload, so callers can omit it instead of
// emitting an empty data URL.
func binaryPartContent(part message.ContentPart) ([]byte, string, error) {
	if len(part.Data) > 0 {
		return part.Data, part.MimeType, nil
	}
	if part.ImagePath == "" {
		return nil, "", fmt.Errorf("binary part has neither inline data nor a file path")
	}
	resolver := binaryPartResolver.Load()
	if resolver == nil {
		return nil, "", fmt.Errorf("no binary part resolver installed")
	}
	return (*resolver)(part)
}

// binaryPartForWire returns the bytes and the matching MIME type for a binary
// part destined for a provider, and reports whether the part may be sent. This
// is the send-time defense boundary: image content is sniffed instead of
// trusting part.MimeType, and anything that is not a decodable PNG/JPEG is
// normalized or dropped rather than sent as an empty data URL or in a format
// the provider rejects. PDFs are passed through unchanged.
func binaryPartForWire(part message.ContentPart) ([]byte, string, bool) {
	data, mime, err := binaryPartContent(part)
	if err != nil {
		reportBinaryPartDrop(part, err)
		return nil, "", false
	}
	if len(data) == 0 {
		reportBinaryPartDrop(part, fmt.Errorf("binary part resolved to zero bytes"))
		return nil, "", false
	}
	if part.Type != message.ContentPartImage {
		return data, mime, true
	}
	return normalizeImageForWire(data, mime, part)
}

// normalizeImageForWire validates an image payload and returns its provider
// ready form, memoized by the digest of the source bytes so the same history
// part is not decoded and re-encoded on every request or fallback attempt.
func normalizeImageForWire(source []byte, mime string, part message.ContentPart) ([]byte, string, bool) {
	key := partDigest(source)
	if entry, ok := imageWireCache.get(key); ok {
		return entry.data, entry.mime, true
	}
	if imageWireCache.isKnownFailure(key) {
		return nil, "", false
	}

	normalized, normalizedMime, err := imageutil.NormalizeImageBytes(source, mime)
	if err != nil {
		imageWireCache.putFailure(key)
		reportBinaryPartDrop(part, err)
		return nil, "", false
	}
	imageWireCache.put(key, normalized, normalizedMime)
	return normalized, normalizedMime, true
}
