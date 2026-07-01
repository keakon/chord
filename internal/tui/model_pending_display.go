package tui

import (
	"strings"

	"github.com/keakon/chord/internal/tui/modelref"
)

func formatModelRefForRequestState(runningRef, selectedRef, activeVariant string, maxLen int) string {
	runningRef = strings.TrimSpace(runningRef)
	if runningRef != "" {
		return modelref.FormatRunningModelRefForDisplay(runningRef, selectedRef, activeVariant, maxLen)
	}
	selectedRef = strings.TrimSpace(selectedRef)
	if selectedRef != "" {
		return modelref.FormatRunningModelRefForDisplay(selectedRef, selectedRef, "", maxLen)
	}
	return modelref.FormatRunningModelRefForDisplay(runningRef, selectedRef, activeVariant, maxLen)
}
