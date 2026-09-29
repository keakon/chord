package tui

import (
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
)

func TestWrapCompactionTypedStateForDisplayWrapsPayload(t *testing.T) {
	payload := `{"decisions":["keep the machine payload verbatim"],"stage_id":"display"}`
	body := "## Current User Request\n- continue\n\n" + message.CompactionTypedStateHeading + "\n- " + payload + "\n\n## Next Step\n- ship"
	want := "## Current User Request\n- continue\n\n" + message.CompactionTypedStateHeading + "\n```json\n" + payload + "\n```\n\n## Next Step\n- ship"

	if got := wrapCompactionTypedStateForDisplay(body); got != want {
		t.Fatalf("wrapped body = %q, want %q", got, want)
	}
}

func TestWrapCompactionTypedStateForDisplayKeepsProse(t *testing.T) {
	cases := map[string]string{
		"payload is not JSON":    message.CompactionTypedStateHeading + "\n- see the archived history",
		"no payload":             message.CompactionTypedStateHeading + "\n",
		"heading mid-line":       "- the card shows " + message.CompactionTypedStateHeading + " for machine state",
		"heading inside a fence": "```\n" + message.CompactionTypedStateHeading + "\n- {\"stage_id\":\"x\"}\n```",
	}
	for name, body := range cases {
		if got := wrapCompactionTypedStateForDisplay(body); got != body {
			t.Fatalf("%s: body was rewritten to %q", name, got)
		}
	}
}

// The card renders the machine payload inside a code block: the payload stays
// verbatim on one line instead of being re-wrapped as a prose bullet.
func TestCompactionCardRendersTypedStateAsCodeBlock(t *testing.T) {
	payload := `{"decisions":["keep the payload verbatim"],"evidence_refs":["ev-8ce7b70efe45"]}`
	content := message.CompactionSummaryHeader +
		"## Current User Request\n- continue the display work\n\n" +
		message.CompactionTypedStateHeading + "\n- " + payload + "\n" +
		message.CompactionCompressedTag +
		"\nEarlier conversation was compacted.\nArchived history files:\n- history-1.md"
	block := &Block{ID: 0, Type: BlockCompactionSummary, Content: content, CompactionSummaryMode: message.CompactionSummaryModeModelDriven}
	plain := stripANSI(strings.Join(block.Render(120, ""), "\n"))

	if !strings.Contains(plain, payload) {
		t.Fatalf("typed state payload is not rendered intact:\n%s", plain)
	}
	if strings.Contains(plain, "• "+payload) {
		t.Fatalf("typed state payload is still rendered as a prose bullet:\n%s", plain)
	}
	if !strings.Contains(plain, "JSON") {
		t.Fatalf("typed state code block label missing:\n%s", plain)
	}
}
