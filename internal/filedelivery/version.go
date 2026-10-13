package filedelivery

import "github.com/keakon/chord/internal/message"

type VersionStatus uint8

const (
	VersionVerified VersionStatus = iota
	VersionContentChanged
	VersionMetadataChanged
	VersionUnverified
)

// CompareVersion never infers equal contents from size and modification time.
func CompareVersion(declared, current message.DeliveredFile) VersionStatus {
	if declared.SHA256 != "" && current.SHA256 != "" {
		if declared.SHA256 != current.SHA256 {
			return VersionContentChanged
		}
		return VersionVerified
	}
	if declared.SizeBytes != current.SizeBytes || declared.ModTimeUnixNano != current.ModTimeUnixNano {
		return VersionMetadataChanged
	}
	return VersionUnverified
}
