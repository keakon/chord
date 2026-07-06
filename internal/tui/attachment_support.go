package tui

import (
	"mime"
	"os"
	"path/filepath"
	"strings"

	"github.com/keakon/chord/internal/imageutil"
	"github.com/keakon/chord/internal/message"
)

type inputSupportProvider interface {
	SupportsInput(modality string) bool
}

func (m *Model) supportsAttachmentInput(kind message.ContentPartType) bool {
	if m == nil || m.agent == nil {
		return false
	}
	provider, ok := m.agent.(inputSupportProvider)
	if !ok {
		return false
	}
	return provider.SupportsInput(kind.String())
}

func attachmentKindForMimeType(mimeType string) message.ContentPartType {
	if strings.HasPrefix(mimeType, "image/") {
		return message.ContentPartImage
	}
	if mimeType == "application/pdf" {
		return message.ContentPartPDF
	}
	return ""
}

func attachmentKindForPath(path string) message.ContentPartType {
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

func (m *Model) markAttachmentSupport(a Attachment) Attachment {
	kind := attachmentKindForMimeType(a.MimeType)
	if kind != "" && !m.supportsAttachmentInput(kind) {
		a.Unsupported = true
	}
	if a.MimeType == "application/pdf" && imageutil.PDFAppearsEncrypted(a.Data) {
		a.Encrypted = true
	}
	return a
}

func (m *Model) unsupportedAttachmentWarning(a Attachment) string {
	if !a.Unsupported {
		return ""
	}
	switch attachmentKindForMimeType(a.MimeType) {
	case message.ContentPartImage:
		return "Current model does not support image input; this attachment will be ignored unless you switch models"
	case message.ContentPartPDF:
		return "Current model does not support PDF input; this attachment will be ignored unless you switch models"
	default:
		return "Current model does not support this attachment; it will be ignored unless you switch models"
	}
}

func (m Model) atMentionAttachmentPreviews() []Attachment {
	refs := dedupeAtMentionFileRefs(atMentionStructuredFileRefs([]string{m.input.Value()}, m.workingDir), m.workingDir)
	if len(refs) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(m.attachments))
	for _, attachment := range m.attachments {
		if attachment.ImagePath != "" {
			seen[filepath.Clean(attachment.ImagePath)] = true
		}
	}
	previews := make([]Attachment, 0, len(refs))
	for _, ref := range refs {
		if ref.Lines.IsSet() {
			continue
		}
		resolved := m.resolveFileRefPath(ref.Path)
		if attachmentKindForPath(resolved) == "" {
			continue
		}
		clean := filepath.Clean(resolved)
		if seen[clean] {
			continue
		}
		info, err := os.Stat(clean)
		if err != nil || info.IsDir() {
			continue
		}
		mimeType := mime.TypeByExtension(strings.ToLower(filepath.Ext(clean)))
		if attachmentKindForPath(clean) == message.ContentPartPDF {
			mimeType = "application/pdf"
		}
		previews = append(previews, (&m).markAttachmentSupport(Attachment{
			FileName:  filepath.Base(clean),
			MimeType:  mimeType,
			SizeBytes: int(info.Size()),
			ImagePath: clean,
		}))
	}
	return previews
}
