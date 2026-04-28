package agent

import "testing"

func TestFormatModelRefForNotificationPreservesSelectedVariant(t *testing.T) {
	if got := formatModelRefForNotification("sample/gpt-5.5", "sample/gpt-5.5@xhigh", "xhigh"); got != "sample/gpt-5.5@xhigh" {
		t.Fatalf("formatModelRefForNotification same base = %q", got)
	}
}

func TestFormatModelRefForNotificationDoesNotLeakVariantToFallback(t *testing.T) {
	if got := formatModelRefForNotification("sample/glm-5.1", "sample/gpt-5.5@xhigh", "xhigh"); got != "sample/glm-5.1" {
		t.Fatalf("formatModelRefForNotification fallback = %q", got)
	}
}
