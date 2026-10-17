package sessionimport

import "strings"

const importedReasoningMarker = "[reasoning]"

// deleteReasonFallback stands in for a delete call whose source recorded no
// reason. Chord's delete schema requires the field and the card shows it, so
// the stand-in must read as a structural fact about the record rather than as
// import metadata the reader was never meant to see.
const deleteReasonFallback = "no reason recorded"

func renderImportedFallbackBlock(header string, fields ...string) string {
	parts := []string{strings.TrimSpace(header)}
	for _, field := range fields {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		parts = append(parts, field)
	}
	return strings.Join(parts, "\n")
}
