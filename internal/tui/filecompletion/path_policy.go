package filecompletion

import (
	"mime"
	"path/filepath"
	"strings"

	"github.com/keakon/chord/internal/filefilter"
	"github.com/keakon/chord/internal/message"
)

// skipIndexedPath reports whether a discovered path is excluded from completion.
// Attachments remain eligible even when their extensions identify binary files.
func skipIndexedPath(path string) bool {
	if path == "" {
		return true
	}
	for part := range strings.SplitSeq(path, "/") {
		if part == "" || strings.HasPrefix(part, ".") || filefilter.IsSkippedDirName(part) {
			return true
		}
		switch part {
		case "node_modules", ".idea", ".vscode", "vendor", "dist", "build":
			return true
		}
	}
	return filefilter.IsBinaryExtension(path) && AttachmentKindForPath(path) == ""
}

// AttachmentKindForPath classifies references by extension using the MIME registry.
// An empty kind means the path is handled as a text file reference.
func AttachmentKindForPath(path string) message.ContentPartType {
	ext := strings.ToLower(filepath.Ext(path))
	if ext == ".pdf" {
		return message.ContentPartPDF
	}
	mimeType := mime.TypeByExtension(ext)
	if strings.HasPrefix(mimeType, "image/") {
		return message.ContentPartImage
	}
	return ""
}
