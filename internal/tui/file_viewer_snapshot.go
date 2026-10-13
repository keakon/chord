package tui

import (
	"github.com/keakon/chord/internal/filedelivery"
	"github.com/keakon/chord/internal/message"
)

// Invalidate both active and queued work before changing the viewed identity.
func (m *Model) invalidateFilePreview() {
	v := &m.fileViewer
	if v.cancel != nil {
		v.cancel()
		v.cancel = nil
	}
	m.fileViewerNext = nil
	m.fileViewerGeneration++
	v.generation = m.fileViewerGeneration
	v.raw = nil
	v.lines = nil
	v.plain = nil
	v.note = ""
	v.offset = 0
	v.wrapOffset = 0
	v.horizontal = 0
}
func (m *Model) selectFileViewerFile(index int) {
	v := &m.fileViewer
	if index < 0 || index >= len(v.files) || index == v.selected {
		return
	}
	m.invalidateFilePreview()
	v.selected = index
	v.apps = nil
}
func fileVersionNote(declared, current message.DeliveredFile) string {
	switch filedelivery.CompareVersion(declared, current) {
	case filedelivery.VersionContentChanged:
		return "File changed since delivery"
	case filedelivery.VersionMetadataChanged:
		return "File metadata changed since delivery · content version not verified"
	case filedelivery.VersionUnverified:
		return "Content version not verified"
	default:
		return ""
	}
}
