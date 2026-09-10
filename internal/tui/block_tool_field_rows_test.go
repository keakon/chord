package tui

import (
	"encoding/json"
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
	for line := range strings.SplitSeq(plain, "\n") {
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
		ResultContent: `{"status":"started","task_id":"adhoc-7","agent_id":"expert-12","message":"running in background","plan_task_ref":"view-switch","semantic_task_key":"tui-view-switch-streaming-card-order","expected_write_scope":{"path_prefix":["internal/tui"]}}` + "\n" + `Note: ignored unrecognized parameter(s): args.expected_write_scope.extra`,
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
		"Note: ignored unrecognized parameter(s): args.expected_write_scope.extra",
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

// Long task-handle values must stay fully visible: a single overflowing row
// is cut at the card edge by the width-limited wrapper, silently dropping the
// tail of an expected_write_scope with many paths or a verbose overlap
// suggestion. The shared field-row helper wraps such values on continuation
// lines aligned under the value column, so the end of the value shows on both
// a normal card and a narrow one.
func TestDelegateWorkerWrapsLongHandleValues(t *testing.T) {
	handle := tools.TaskHandle{
		Status:  "started",
		TaskID:  "adhoc-9",
		AgentID: "expert-3",
		ExpectedWriteScope: tools.WriteScope{
			PathPrefix: []string{
				"internal/tui",
				"internal/tui/views",
				"internal/agent",
				"internal/agent/coordination",
				"internal/tui/block_tool_render_special.go",
				"internal/tools/task.go",
			},
			Files: []string{"internal/agent/main_subagent.go"},
		},
		Message:         "checking the sibling task for a write-scope overlap before starting",
		ScopeConflict:   true,
		SuggestedAction: "narrow the delegate's expected_write_scope away from the sibling's paths and keep only the files this task actually edits",
	}
	payload, err := json.Marshal(handle)
	if err != nil {
		t.Fatal(err)
	}
	b := &Block{
		ID:            0,
		Type:          BlockToolCall,
		ToolName:      tools.NameDelegate,
		Content:       `{"description":"review the card styles","agent_type":"reviewer"}`,
		ResultContent: string(payload),
		ResultDone:    true,
	}
	// The last scope path, the files list that closes the scope row, and the
	// end of the suggested action are the tails a width truncation would cut
	// (the pre-fix card cut each row at the card edge, dropping everything
	// past roughly the first line). Tail tokens are probed per width: on a
	// wide card a full path stays on one line, while a narrow card can
	// hard-break one path across two lines, so there the tail is asserted by
	// the fragments that survive the wrap intact.
	tailProbes := map[int][]string{
		120: {
			"internal/tools/task.go",
			"internal/agent/main_subagent.go",
			"the files this task actually edits",
		},
		70: {
			"internal/tools/task.go",
			"nt.go]",
			"actually edits",
		},
	}
	for _, width := range []int{120, 70} {
		plain := stripANSI(strings.Join(b.Render(width, ""), "\n"))
		for _, want := range tailProbes[width] {
			if !strings.Contains(plain, want) {
				t.Fatalf("delegate Worker section lost the tail of a long handle value at width %d: missing %q; got:\n%s", width, want, plain)
			}
		}
		// The scope row must actually wrap: the last scope path has to sit on
		// a later line than the scope label, not share one overflowing row.
		rows := strings.Split(plain, "\n")
		scopeRow := -1
		for i, row := range rows {
			if strings.Contains(row, "Expected write scope:") {
				scopeRow = i
				break
			}
		}
		tailRow := -1
		for i, row := range rows {
			if strings.Contains(row, "internal/tools/task.go") {
				tailRow = i
				break
			}
		}
		if scopeRow < 0 || tailRow <= scopeRow {
			t.Fatalf("delegate Worker section should wrap the scope value below its label at width %d; got:\n%s", width, plain)
		}
	}
}
