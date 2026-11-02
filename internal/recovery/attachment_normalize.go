package recovery

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/privatefs"
)

// attachmentExt maps an attachment MIME type to the file extension used for its
// session-owned copy. Unknown types fall back to .bin so the file still has a
// stable name and never inherits an extension from the source path.
func attachmentExt(mimeType string) string {
	switch mimeType {
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "application/pdf":
		return ".pdf"
	default:
		return ".bin"
	}
}

// NormalizeAttachmentParts moves every binary part's payload into the session's
// images directory and returns parts that reference it: Data is cleared,
// ImagePath is a session-owned file, and DataBytes records the size.
//
// Parts that already point inside images/ are validated in place rather than
// rewritten, so calling this twice is idempotent. A part whose only copy is an
// external path (a pasted or dropped file) is read and re-homed: an external
// path is never treated as durable, because the user's file can move or vanish.
//
// On any failure the ORIGINAL parts are returned unchanged together with the
// error. Callers must keep the inline bytes in that case — Data is the only
// copy, and dropping it to save memory would lose the attachment.
func (r *RecoveryManager) NormalizeAttachmentParts(parts []message.ContentPart) ([]message.ContentPart, error) {
	if r == nil || len(parts) == 0 {
		return parts, nil
	}
	hasBinary := false
	for _, part := range parts {
		if part.IsBinary() {
			hasBinary = true
			break
		}
	}
	if !hasBinary {
		return parts, nil
	}

	out := make([]message.ContentPart, len(parts))
	copy(out, parts)
	imagesDir := r.imagesDir()
	wrote := false
	for i := range out {
		part := &out[i]
		if !part.IsBinary() {
			continue
		}
		if part.ImagePath != "" && pathWithin(imagesDir, part.ImagePath) {
			info, err := privatefs.Stat(r.sessionDir, part.ImagePath)
			if err != nil {
				return parts, fmt.Errorf("stat session attachment: %w", err)
			}
			if !info.Mode().IsRegular() {
				return parts, fmt.Errorf("session attachment is not a regular file")
			}
			part.DataBytes = info.Size()
			// The validated session file is the durable copy; keeping an inline
			// duplicate would defeat the memory accounting this normalization
			// exists to guarantee.
			part.Data = nil
			continue
		}
		data := part.Data
		if len(data) == 0 {
			if part.ImagePath == "" {
				return parts, fmt.Errorf("binary part has neither inline data nor a path")
			}
			read, err := os.ReadFile(part.ImagePath)
			if err != nil {
				return parts, fmt.Errorf("read external attachment: %w", err)
			}
			data = read
		}
		if len(data) == 0 {
			return parts, fmt.Errorf("attachment is empty")
		}
		name := fmt.Sprintf("%d-%d%s", time.Now().UnixNano(), i, attachmentExt(part.MimeType))
		path := filepath.Join(imagesDir, name)
		if err := privatefs.WriteFileAtomic(r.sessionDir, path, data); err != nil {
			return parts, fmt.Errorf("write session attachment: %w", err)
		}
		wrote = true
		part.Data = nil
		part.ImagePath = path
		part.DataBytes = int64(len(data))
	}
	if wrote {
		// The rename that publishes each file must survive a crash; the file
		// contents were already synced by WriteFileAtomic.
		if err := privatefs.SyncDir(imagesDir); err != nil {
			return parts, fmt.Errorf("sync attachment directory: %w", err)
		}
	}
	return out, nil
}
