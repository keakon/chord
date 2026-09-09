package tui

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/keakon/chord/internal/tools"
)

// Every labelled row in a card is produced by the field-row helpers in
// block_tool_common.go, which give a row three levels: a quiet connector, a
// bold label, and a plain value. These tests pin that grammar on the two cards
// that used to hand-roll it — notify and the sub-agent mailbox card — so the
// renderers cannot drift back to ad-hoc literals.
func TestNotifyCardUsesFieldRowGrammar(t *testing.T) {
	b := &Block{
		ID:            0,
		Type:          BlockToolCall,
		ToolName:      tools.NameNotify,
		Content:       `{"message":"cross-check the checkpoint tree","kind":"progress"}`,
		ResultContent: "Owner coordination chain has been notified. Continue working.",
		ResultDone:    true,
	}
	plain := stripANSI(strings.Join(b.Render(100, ""), "\n"))

	// "kind" used to be a lowercase 4-space row with no connector, and the
	// status word "Sent" used to be a bare colon-less row.
	for _, want := range []string{
		"↳ Kind: progress",
		"↳ Message:",
		"↳ Status: Sent",
		"↳ Result:",
	} {
		if !strings.Contains(plain, want) {
			t.Fatalf("notify card missing %q; got:\n%s", want, plain)
		}
	}
	if strings.Contains(plain, "kind: progress") {
		t.Fatalf("notify card still renders the lowercase un-connected kind row:\n%s", plain)
	}
	if strings.Contains(plain, "↳ Sent\n") {
		t.Fatalf("notify card still renders a bare colon-less status row:\n%s", plain)
	}
}

func TestAgentMessageCardRendersSenderAndKindAsFieldRows(t *testing.T) {
	b := &Block{
		ID:          0,
		Type:        BlockStatus,
		StatusTitle: "AGENT MESSAGE",
		StatusFrom:  "reviewer-4",
		StatusKind:  "risk_alert",
		Content:     "the tree disagrees with the CN copy",
	}
	plain := stripANSI(strings.Join(b.Render(100, ""), "\n"))

	for _, want := range []string{"↳ From: reviewer-4", "↳ Kind: risk_alert"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("agent message card missing %q; got:\n%s", want, plain)
		}
	}
	// The sender and kind must not also be flattened into the prose.
	if strings.Contains(plain, "[reviewer-4]") {
		t.Fatalf("agent message body repeats the sender it shows as a field row:\n%s", plain)
	}
}

// The body of an AGENT MESSAGE/COMPLETE card nests under the From/Kind field
// rows: their labels sit at visual column 4 (2-space lead + "↳ "), and the
// body must align to that column instead of the lead/connector column. The
// previous 2-space indent left the body visually flush-left of the labels.
func TestAgentMessageCardBodyNestsUnderFieldRowLabels(t *testing.T) {
	b := &Block{
		ID:          0,
		Type:        BlockStatus,
		StatusTitle: "AGENT MESSAGE",
		StatusFrom:  "reviewer-4",
		StatusKind:  "risk_alert",
		Content:     "the tree disagrees with the CN copy",
	}
	plain := stripANSI(strings.Join(b.Render(100, ""), "\n"))

	// Compare the *visual* column of the body and the field-row label. The
	// rail "│" and the connector "↳" are multi-byte UTF-8, so a byte-offset
	// comparison would falsely report them as misaligned by 2 columns even
	// when they line up in the terminal. Rune counts give the true terminal
	// column.
	fieldPos, bodyPos := -1, -1
	for _, line := range strings.Split(plain, "\n") {
		switch {
		case fieldPos < 0 && strings.Contains(line, "From: reviewer-4"):
			fieldPos = utf8.RuneCountInString(line[:strings.Index(line, "From: reviewer-4")])
		case bodyPos < 0 && strings.Contains(line, "the tree disagrees"):
			bodyPos = utf8.RuneCountInString(line[:strings.Index(line, "the tree disagrees")])
		}
	}
	if fieldPos < 0 || bodyPos < 0 {
		t.Fatalf("could not locate field row and body line in rendered output:\n%s", plain)
	}
	if fieldPos != bodyPos {
		t.Fatalf("body left edge at col %d, field-row label at col %d, want equal (body must align with label, not connector); output:\n%s", bodyPos, fieldPos, plain)
	}
}

// The Delegate (task) result is a JSON handle that the runtime commonly
// appends a prose note to (e.g. "Note: ignored unrecognized parameter(s): …").
// The previous parser used json.Unmarshal, which rejects trailing text and
// silently fell through to dumping the whole payload — a raw-JSON blob the
// user had to read past. The tolerant parser now decodes the first JSON
// value, returns the rest for the caller to render as a note, and the
// Worker section renders the handle's fields as structured rows. This pins
// all three: structured fields (including the ones the old local struct
// silently dropped — plan_task_ref, semantic_task_key, expected_write_scope),
// the trailing note rendered separately, and the absence of the raw JSON.
func TestDelegateWorkerRendersHandleFieldsAndTrailingNote(t *testing.T) {
	b := &Block{
		ID:            0,
		Type:          BlockToolCall,
		ToolName:      tools.NameDelegate,
		Content:       `{"description":"review the card styles","agent_type":"reviewer"}`,
		ResultContent: `{"status":"started","task_id":"adhoc-7","agent_id":"expert-12","message":"running in background","plan_task_ref":"view-switch","semantic_task_key":"tui-view-switch-streaming-card-order","expected_write_scope":{"path_prefix":["internal/tui"]}}` + "\n" + `Note: ignored unrecognized parameter(s): args.expected_write_scope.verification_commands`,
		ResultDone:    true,
	}
	plain := stripANSI(strings.Join(b.Render(120, ""), "\n"))

	// Every non-empty handle field renders as a "↳ Label: value" row in the
	// expanded field layer, so the task_id carries the full machine-readable
	// handle, adhoc- prefix included. The write scope is condensed to a
	// single compact value, not re-expanded as JSON.
	for _, want := range []string{
		"↳ Status: started",
		"↳ Agent id: expert-12",
		"↳ Task id: adhoc-7",
		"↳ Plan task ref: view-switch",
		"↳ Semantic task key: tui-view-switch-streaming-card-order",
		"↳ Expected write scope: path_prefix=[internal/tui]",
		"↳ Message: running in background",
		"Note: ignored unrecognized parameter(s): args.expected_write_scope.verification_commands",
	} {
		if !strings.Contains(plain, want) {
			t.Fatalf("delegate Worker section missing %q; got:\n%s", want, plain)
		}
	}
	// The raw JSON must not leak back into the body. Both the top-level
	// payload and the nested write scope object are checked so a future
	// regression that falls through to the raw-text path is caught.
	for _, banned := range []string{
		`"status":"started"`,
		`"expected_write_scope":{`,
	} {
		if strings.Contains(plain, banned) {
			t.Fatalf("delegate Worker section leaks raw JSON %q; got:\n%s", banned, plain)
		}
	}
	// The full machine-readable task id appears on exactly one field row:
	// compact/title surfaces shorten an ad-hoc handle to "#N", and a second
	// field row would mean the handle section rendered twice. Counting the
	// bare id would misfire when the args body or the runtime note legally
	// mentions the same id, so the row itself is the assertion target.
	if got := strings.Count(plain, "↳ Task id: adhoc-7"); got != 1 {
		t.Fatalf("delegate Worker section should render exactly one task id field row; got %d:\n%s", got, plain)
	}
}
