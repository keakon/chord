package filepreview

import (
	"github.com/keakon/chord/internal/filedelivery"
	"github.com/keakon/chord/internal/message"
)

// VersionNote describes a change or an unverified delivered file version.
func VersionNote(declared, current message.DeliveredFile) string {
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
