package tui

import (
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
)

func TestWrapCompactionTypedStateForDisplayWrapsPayload(t *testing.T) {
	payload := `{"decisions":["keep the machine payload verbatim"],"stage_id":"display"}`
	body := "## Current User Request\n- continue\n\n" + message.CompactionTypedStateHeading + "\n- " + payload + "\n\n## Next Step\n- ship"
	want := "## Current User Request\n- continue\n\n" + message.CompactionTypedStateHeading +
		"\n### Stage\n- ID: display\n- Status: (not recorded)\n- Checkpoint kind: (not recorded)\n\n" +
		"### Completed\n- None recorded\n\n### Decisions\n- keep the machine payload verbatim\n\n" +
		"### Open Issues\n- None recorded\n\n### Carried Open Issues\n- None carried\n\n" +
		"### Evidence References\n- None recorded\n\n### Claims\n- None recorded\n\n## Next Step\n- ship"

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

func TestWrapCompactionTypedStateForDisplayKeepsUnprojectableJSONRaw(t *testing.T) {
	// A payload that parses as JSON but cannot be projected has no structured
	// form, so it keeps rendering as a fenced JSON block.
	for _, payload := range []string{
		`{"decisions":"not a list","stage_id":"display"}`,
		`[1,2]`,
		`null`,
	} {
		body := message.CompactionTypedStateHeading + "\n- " + payload
		got := wrapCompactionTypedStateForDisplay(body)
		if !strings.Contains(got, "```json\n"+payload+"\n```") {
			t.Fatalf("unprojectable typed state %s should remain readable as raw JSON:\n%s", payload, got)
		}
		if strings.Contains(got, "### Decisions") {
			t.Fatalf("unprojectable typed state %s should not render a misleading structured section:\n%s", payload, got)
		}
	}
}

func TestWrapCompactionTypedStateForDisplayRendersClaimsDeterministically(t *testing.T) {
	payload := `{"claims":{"z claim":{"kind":"derived","evidence_refs":["ev-z"],"status":"stale"},"a claim":{"kind":"observed","evidence_refs":["ev-a","ev-b"],"status":"active"}},"open_issues":["check | parser"],"checkpoint_kind":"provisional","stage_status":"active","stage_id":"aug-batch"}`
	body := message.CompactionTypedStateHeading + "\n- " + payload

	got := wrapCompactionTypedStateForDisplay(body)
	a := strings.Index(got, "- a claim")
	z := strings.Index(got, "- z claim")
	if a < 0 || z < 0 || a > z {
		t.Fatalf("claims are not rendered in stable order:\n%s", got)
	}
	for _, want := range []string{
		"### Stage",
		"- ID: aug\\-batch",
		"- Status: active",
		"- Checkpoint kind: provisional",
		"### Open Issues",
		"- check \\| parser",
		"- Kind: observed",
		"- Status: active",
		"- Evidence: ev\\-a, ev\\-b",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("display missing %q:\n%s", want, got)
		}
	}
}

func TestTypedCheckpointDisplayPreservesMultilineValues(t *testing.T) {
	payload := `{"completed":["first line\nsecond line"],"stage_id":"display"}`
	body := message.CompactionTypedStateHeading + "\n- " + payload

	got := wrapCompactionTypedStateForDisplay(body)
	if !strings.Contains(got, "- first line ⏎ second line") {
		t.Fatalf("display missing the flattened multiline value:\n%s", got)
	}
	if strings.Contains(got, "### Raw JSON") || strings.Contains(got, payload) {
		t.Fatalf("structured display must not repeat the raw payload:\n%s", got)
	}
}

func TestCompactionTypedStateDisplayDoesNotChangeCopiedCheckpoint(t *testing.T) {
	content := message.CompactionSummaryHeader +
		message.CompactionTypedStateHeading + "\n- " +
		`{"stage_id":"display","decisions":["keep raw state"]}` +
		message.CompactionCompressedTag + "\nArchived history files:\n- history-1.md"
	block := &Block{
		ID:                   0,
		Type:                 BlockCompactionSummary,
		Content:              content,
		CompactionSummaryRaw: content,
	}
	block.Render(100, "")
	if got := blockPlainContent(block); got != content {
		t.Fatalf("copied checkpoint = %q, want original content %q", got, content)
	}
}

// The card renders the structured projection of the machine payload instead of
// displaying the JSON document itself.
func TestCompactionCardRendersStructuredTypedState(t *testing.T) {
	payload := `{"decisions":["keep the payload verbatim"],"evidence_refs":["ev-8ce7b70efe45"]}`
	content := message.CompactionSummaryHeader +
		"## Current User Request\n- continue the display work\n\n" +
		message.CompactionTypedStateHeading + "\n- " + payload + "\n" +
		message.CompactionCompressedTag +
		"\nEarlier conversation was compacted.\nArchived history files:\n- history-1.md"
	block := &Block{ID: 0, Type: BlockCompactionSummary, Content: content, CompactionSummaryMode: message.CompactionSummaryModeModelDriven}
	plain := stripANSI(strings.Join(block.Render(120, ""), "\n"))

	if strings.Contains(plain, payload) {
		t.Fatalf("typed state payload is still rendered as raw text:\n%s", plain)
	}
	for _, want := range []string{"Stage", "Decisions", "keep the payload verbatim"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("structured typed state label %q missing:\n%s", want, plain)
		}
	}
}
