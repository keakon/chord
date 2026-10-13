package filedelivery

import (
	"testing"

	"github.com/keakon/chord/internal/message"
)

func TestCompareVersion(t *testing.T) {
	declared := message.DeliveredFile{SHA256: "a", SizeBytes: 5, ModTimeUnixNano: 1}
	for _, tt := range []struct {
		name    string
		current message.DeliveredFile
		want    VersionStatus
	}{
		{"equal digest", message.DeliveredFile{SHA256: "a", SizeBytes: 5, ModTimeUnixNano: 2}, VersionVerified},
		{"changed digest", message.DeliveredFile{SHA256: "b", SizeBytes: 5, ModTimeUnixNano: 1}, VersionContentChanged},
		{"unknown", message.DeliveredFile{SizeBytes: 5, ModTimeUnixNano: 1}, VersionUnverified},
		{"size", message.DeliveredFile{SizeBytes: 6, ModTimeUnixNano: 1}, VersionMetadataChanged},
		{"mtime", message.DeliveredFile{SizeBytes: 5, ModTimeUnixNano: 2}, VersionMetadataChanged},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := CompareVersion(declared, tt.current); got != tt.want {
				t.Fatalf("got %v want %v", got, tt.want)
			}
		})
	}
}
