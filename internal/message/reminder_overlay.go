package message

import "strings"

// SystemReminderOverlayOpen and SystemReminderOverlayClose delimit the marker
// the content-trust prompt defines for harness-injected user text: everything
// between them is runtime state, not user speech.
const (
	SystemReminderOverlayOpen  = "<system-reminder>"
	SystemReminderOverlayClose = "</system-reminder>"
)

// SystemReminderOverlayText wraps harness-injected user text in the
// <system-reminder> marker so every audience — a durable transcript row or a
// SubAgent owner's pending message alike — can tell runtime messages apart
// from user-authored ones. Callers must pass already-trimmed content.
func SystemReminderOverlayText(content string) string {
	return SystemReminderOverlayOpen + "\n" + content + "\n" + SystemReminderOverlayClose
}

// StripSystemReminderOverlay removes a system-reminder wrapper from text that
// carries one, returning the input unchanged when it is not wrapped. Display
// and external-delivery paths use it on durable rows that keep the wrapper for
// the model.
func StripSystemReminderOverlay(text string) string {
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, SystemReminderOverlayOpen) || !strings.HasSuffix(trimmed, SystemReminderOverlayClose) {
		return text
	}
	inner := strings.TrimSuffix(strings.TrimPrefix(trimmed, SystemReminderOverlayOpen), SystemReminderOverlayClose)
	return strings.TrimSpace(inner)
}
