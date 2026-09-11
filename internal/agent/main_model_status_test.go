package agent

import (
	"strings"
	"testing"
)

// The TUI renders /models status inside a NOTICE card as Markdown, so every
// pool and agent line must be its own list item: indented plain text would
// reflow into a single unreadable paragraph.
func TestModelsStatusTextRendersListItems(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	installPoolPolicyForTest(t, a)
	a.modelPoolPolicy.SetAgentOverride("worker", "fast")

	text := a.ModelsStatusText()
	for _, item := range []string{
		"\nFixed agent pools:\n- worker: fast\n",
		"\nAgent effective pools:\n- test: base (1 model(s))\n",
	} {
		if !strings.Contains(text, item) {
			t.Fatalf("ModelsStatusText() = %q, want %q", text, item)
		}
	}
}
