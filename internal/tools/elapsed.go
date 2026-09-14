package tools

import (
	"fmt"
	"time"
)

// FormatElapsed renders a duration for user-visible text: whole seconds below
// a minute, zero-padded seconds above it, and hours once the duration passes
// one, so a long job never reads as "62m03s". The tool result text and the TUI
// share it so the same duration cannot print two ways.
func FormatElapsed(d time.Duration) string {
	if d < time.Second {
		return "0s"
	}
	total := int(d.Truncate(time.Second).Seconds())
	if total < 60 {
		return fmt.Sprintf("%ds", total)
	}
	if total < 3600 {
		return fmt.Sprintf("%dm%02ds", total/60, total%60)
	}
	return fmt.Sprintf("%dh%02dm%02ds", total/3600, total/60%60, total%60)
}
