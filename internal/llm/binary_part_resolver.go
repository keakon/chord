package llm

import (
	"sync/atomic"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/message"
)

// BinaryPartResolver loads the payload of a binary part that carries only a
// file reference (ImagePath set, Data empty). Restored sessions keep
// attachment blobs on disk instead of holding every image/PDF in memory from
// startup; the wire converter resolves them through this hook at request
// time. The agent installs a process-wide resolver at construction.
type BinaryPartResolver func(part message.ContentPart) ([]byte, error)

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

// binaryPartPayload returns the wire bytes for a binary part: the inline data
// when present, otherwise the payload resolved from the persisted file. A
// missing resolver or a failed read returns nil and logs a warning, which
// converts to an empty data URL — the same shape a part with no bytes has
// always produced, and the same degradation an unreadable attachment file
// produced before payloads were resolved lazily.
func binaryPartPayload(part message.ContentPart) []byte {
	if len(part.Data) > 0 {
		return part.Data
	}
	if part.ImagePath == "" {
		return nil
	}
	resolver := binaryPartResolver.Load()
	if resolver == nil {
		log.Warnf("no binary part resolver installed for attachment path=%v mime=%v", part.ImagePath, part.MimeType)
		return nil
	}
	data, err := (*resolver)(part)
	if err != nil {
		log.Warnf("failed to resolve binary part payload path=%v mime=%v error=%v", part.ImagePath, part.MimeType, err)
		return nil
	}
	return data
}
