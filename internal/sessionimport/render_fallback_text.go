package sessionimport

import "strings"

const importedReasoningMarker = "[Imported reasoning]"

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
