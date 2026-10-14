package composer

import (
	"fmt"
	"path/filepath"
	"strings"
)

func ImagePartDisplayName(fileName, imagePath, mimeType string, imageIndex int) string {
	name := strings.TrimSpace(fileName)
	if name != "" {
		base := strings.TrimSpace(filepath.Base(name))
		ext := strings.ToLower(filepath.Ext(base))
		if strings.EqualFold(base, "clipboard.png") || strings.EqualFold(base, "clipboard.jpg") || strings.EqualFold(base, "clipboard.jpeg") || strings.HasPrefix(strings.ToLower(base), "clipboard-") {
			return fmt.Sprintf("image%d%s", imageIndex, AttachmentExtForMimeType(mimeType))
		}
		if base != "" && base != "." && base != string(filepath.Separator) {
			if ext == "" {
				return base + AttachmentExtForMimeType(mimeType)
			}
			return base
		}
	}
	base := filepath.Base(strings.TrimSpace(imagePath))
	if base != "" && base != "." && base != string(filepath.Separator) {
		if strings.EqualFold(base, "clipboard.png") || strings.EqualFold(base, "clipboard.jpg") || strings.EqualFold(base, "clipboard.jpeg") || strings.HasPrefix(strings.ToLower(base), "clipboard-") {
			return fmt.Sprintf("image%d%s", imageIndex, AttachmentExtForMimeType(mimeType))
		}
		return base
	}
	return fmt.Sprintf("image%d%s", imageIndex, AttachmentExtForMimeType(mimeType))
}
