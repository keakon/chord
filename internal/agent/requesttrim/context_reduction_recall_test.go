package requesttrim

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func bigSearchOutput(files, matchesPerFile int) string {
	var sb strings.Builder
	for i := range files {
		for j := range matchesPerFile {
			fmt.Fprintf(&sb, "internal/agent/file%d.go:%d: callSite(target%d)\n", i, 10+j, i)
		}
	}
	return sb.String()
}

func TestClassifyReadOnlyShellWaitsForReadOnlyAge(t *testing.T) {
	policy := DefaultPolicy()
	content := strings.Repeat("plain command output describing repository layout\n", 120)
	if len(content) <= policy.ShellSuccessBytes {
		t.Fatal("fixture must exceed the shell success byte threshold")
	}
	for _, tc := range []struct {
		age  int
		want Class
	}{
		{age: policy.ShellSuccessAgeTurns, want: None},
		{age: policy.ShellReadOnlyAgeTurns - 1, want: None},
		{age: policy.ShellReadOnlyAgeTurns, want: requestReductionShellOK},
	} {
		ctx := Context{
			ToolName:      tools.NameShell,
			Content:       content,
			Age:           tc.age,
			Policy:        policy,
			ShellReadOnly: true,
		}
		if got := Classify(ctx).Class; got != tc.want {
			t.Fatalf("age %d: class = %q, want %q", tc.age, got, tc.want)
		}
	}
	// A mutating shell command keeps the original, more aggressive age.
	ctx := Context{
		ToolName: tools.NameShell,
		Content:  content,
		Age:      policy.ShellSuccessAgeTurns,
		Policy:   policy,
	}
	if got := Classify(ctx).Class; got != requestReductionShellOK {
		t.Fatalf("mutating shell class = %q, want shell success", got)
	}
}

func TestClassifyJSONDocumentWaitsForStaleAge(t *testing.T) {
	policy := DefaultPolicy()
	doc := `{"name":"example","values":[` + strings.Repeat(`"padding entry",`, 400) + `"tail"]}`
	if len(doc) <= policy.ShellSuccessBytes {
		t.Fatal("fixture must exceed the shell success byte threshold")
	}
	for _, tc := range []struct {
		age  int
		want Class
	}{
		{age: policy.ShellSuccessAgeTurns, want: None},
		{age: policy.StaleAgeTurns - 1, want: None},
		{age: policy.StaleAgeTurns, want: requestReductionJSON},
	} {
		ctx := Context{
			ToolName: tools.NameShell,
			Content:  doc,
			Age:      tc.age,
			Policy:   policy,
		}
		if got := Classify(ctx).Class; got != tc.want {
			t.Fatalf("age %d: class = %q, want %q", tc.age, got, tc.want)
		}
	}
	// NDJSON log streams (go test -json) get no extended JSON retention.
	ndjson := strings.Repeat(`{"Action":"pass","Package":"example"}`+"\n", 200)
	ctx := Context{
		ToolName: tools.NameShell,
		Content:  ndjson,
		Age:      policy.ShellSuccessAgeTurns,
		Policy:   policy,
	}
	if got := Classify(ctx).Class; got != requestReductionJSON {
		t.Fatalf("ndjson class = %q, want json", got)
	}
}

func TestClassifyStaleWebFetchJSONKeepsReadLikeSummary(t *testing.T) {
	policy := DefaultPolicy()
	doc := `{"payload":"` + strings.Repeat("x", policy.StaleOutputBytes) + `"}`
	if len(doc) <= policy.StaleOutputBytes || len(doc) > policy.ReadLikeOutputBytes {
		t.Fatalf("fixture bytes = %d, want (%d, %d]", len(doc), policy.StaleOutputBytes, policy.ReadLikeOutputBytes)
	}
	ctx := Context{
		ToolName:    tools.NameWebFetch,
		Content:     doc,
		Age:         policy.StaleAgeTurns,
		Policy:      policy,
		ToolResults: policy.MinToolResultsPrune,
	}
	if got := Classify(ctx).Class; got != ReadLike {
		t.Fatalf("web fetch class = %q, want read-like", got)
	}
}

func TestSummarizeSearchResultLocationsKeepsAllPathsWithinBudget(t *testing.T) {
	content := bigSearchOutput(12, 2)
	lines := summarizeSearchResultLocations(content, searchSummaryByteBudget)
	if len(lines) != 12 {
		t.Fatalf("expected one line per file, got %d: %v", len(lines), lines)
	}
	for i := range 12 {
		want := fmt.Sprintf("internal/agent/file%d.go: 10, 11", i)
		if !strings.Contains(lines[i], want) {
			t.Fatalf("line %d missing locations %q: %q", i, want, lines[i])
		}
	}
	if !strings.Contains(lines[0], "callSite(target0)") {
		t.Fatalf("first group should keep a snippet: %q", lines[0])
	}
	if strings.Contains(lines[len(lines)-1], "omitted") {
		t.Fatalf("no omission tail expected for 12 files: %q", lines[len(lines)-1])
	}

	huge := summarizeSearchResultLocations(bigSearchOutput(300, 1), searchSummaryByteBudget)
	last := huge[len(huge)-1]
	if !strings.Contains(last, "matches omitted") {
		t.Fatalf("expected omission tail for 300 files: %q", last)
	}
	if len(huge) <= 7 {
		t.Fatalf("budgeted listing should keep far more than the old 6 groups, got %d lines", len(huge))
	}
	if got := len(strings.Join(huge, "\n")); got > searchSummaryByteBudget {
		t.Fatalf("summary bytes = %d, budget = %d", got, searchSummaryByteBudget)
	}
}

func TestSummarizeSearchResultLocationsCountsPlainLinesInTail(t *testing.T) {
	// A mixed output: path:line matches plus a tool-level truncation note.
	content := bigSearchOutput(4, 2) + "(showing first 8 matches within 64 KiB; narrow the pattern for more)\n"
	lines := summarizeSearchResultLocations(content, searchSummaryByteBudget)
	if len(lines) == 0 {
		t.Fatal("expected a rendered summary")
	}
	last := lines[len(lines)-1]
	if !strings.Contains(last, "1 other lines omitted") {
		t.Fatalf("the dropped truncation note must be accounted for in the tail: %q", last)
	}

	// When groups themselves overflow the budget, the note joins the file tail.
	huge := summarizeSearchResultLocations(bigSearchOutput(300, 1)+"note: pattern was reinterpreted\n", searchSummaryByteBudget)
	tail := huge[len(huge)-1]
	if !strings.Contains(tail, "matches, 1 other lines omitted") {
		t.Fatalf("overflow tail must carry the other-lines count: %q", tail)
	}
}

func TestSummarizeSearchResultLocationsMakesRoomForPlainLineTail(t *testing.T) {
	path := strings.Repeat("segment/", 8) + "file.go"
	match := fmt.Sprintf("%s:42: %s\n", path, strings.Repeat("x", 100))
	group := summarizeSearchResultLocations(match, searchSummaryByteBudget)
	if len(group) != 1 {
		t.Fatalf("expected one rendered group, got %v", group)
	}
	budget := len(group[0]) + 1

	lines := summarizeSearchResultLocations(match+"tool-level truncation note\n", budget)
	if len(lines) != 1 || !strings.Contains(lines[0], "1 files, 1 matches, 1 other lines omitted") {
		t.Fatalf("the summary must trade a group for a complete omission marker: %v", lines)
	}
	if got := len(strings.Join(lines, "\n")); got > budget {
		t.Fatalf("summary bytes = %d, budget = %d", got, budget)
	}
}

func TestSummarizeSearchResultLocationsListsPlainPaths(t *testing.T) {
	var sb strings.Builder
	for i := range 40 {
		fmt.Fprintf(&sb, "internal/tools/generated_%d.go\n", i)
	}
	lines := summarizeSearchResultLocations(sb.String(), 400)
	if len(lines) == 0 {
		t.Fatal("expected plain path listing")
	}
	if !strings.Contains(lines[0], "internal/tools/generated_0.go") {
		t.Fatalf("first path missing: %q", lines[0])
	}
	if !strings.Contains(lines[len(lines)-1], "omitted") {
		t.Fatalf("expected omission tail once the budget is exhausted: %q", lines[len(lines)-1])
	}
	if got := len(strings.Join(lines, "\n")); got > 400 {
		t.Fatalf("plain summary bytes = %d, budget = 400", got)
	}
}

func TestDetectRepeatedToolOutputsIgnoresFailedRerun(t *testing.T) {
	args := json.RawMessage(`{"pattern":"callSite"}`)
	msgs := []message.Message{
		{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "tc1", Name: tools.NameGrep, Args: args}}},
		{Role: "tool", ToolCallID: "tc1", Content: "a.go:1: callSite()"},
		{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "tc2", Name: tools.NameGrep, Args: args}}},
		{Role: "tool", ToolCallID: "tc2", Content: "grep failed", ToolStatus: message.ToolStatusError},
	}
	if repeated := detectRepeatedToolOutputs(msgs, BuildToolCallMeta(msgs), nil); len(repeated) != 0 {
		t.Fatalf("a failed rerun must not mark the earlier success repeated: %v", repeated)
	}
	msgs[3].ToolStatus = ""
	msgs[3].Content = "Error: grep failed"
	if repeated := detectRepeatedToolOutputs(msgs, BuildToolCallMeta(msgs), nil); len(repeated) != 0 {
		t.Fatalf("a legacy rendered error must not mark the earlier success repeated: %v", repeated)
	}
	msgs[3].ToolStatus = message.ToolStatusSuccess
	msgs[3].Content = "a.go:1: callSite()"
	if repeated := detectRepeatedToolOutputs(msgs, BuildToolCallMeta(msgs), nil); !repeated[1] {
		t.Fatal("a successful rerun must mark the earlier output repeated")
	}
	msgs[3].Content = "a.go:1: callSite() // Error: wrapped by caller"
	msgs[1].Content = msgs[3].Content
	if repeated := detectRepeatedToolOutputs(msgs, BuildToolCallMeta(msgs), nil); !repeated[1] {
		t.Fatal("an explicit success mentioning Error: mid-output must still establish the fresher copy")
	}
	msgs[3].ToolStatus = ""
	if repeated := detectRepeatedToolOutputs(msgs, BuildToolCallMeta(msgs), nil); !repeated[1] {
		t.Fatal("a status-less result not rendered as an error must still establish the fresher copy")
	}
}

// A repeated marker asserts that an identical later call carries the same
// content. For a time-varying command (git status, go test, ls) that assertion
// is false, and the older output is the "before" state the model may still
// need, so only a byte-identical later copy may collapse it.
func TestDetectRepeatedToolOutputsRequiresIdenticalContent(t *testing.T) {
	args := json.RawMessage(`{"command":"git status --short"}`)
	msgs := []message.Message{
		{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "tc1", Name: tools.NameShell, Args: args}}},
		{Role: "tool", ToolCallID: "tc1", Content: " M internal/agent/a.go", ToolStatus: message.ToolStatusSuccess},
		{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "tc2", Name: tools.NameShell, Args: args}}},
		{Role: "tool", ToolCallID: "tc2", Content: " M internal/agent/a.go\n M internal/agent/b.go", ToolStatus: message.ToolStatusSuccess},
	}
	if repeated := detectRepeatedToolOutputs(msgs, BuildToolCallMeta(msgs), nil); repeated[1] {
		t.Fatal("a later call with the same arguments but different output must not collapse the earlier state")
	}
	msgs[3].Content = msgs[1].Content
	if repeated := detectRepeatedToolOutputs(msgs, BuildToolCallMeta(msgs), nil); !repeated[1] {
		t.Fatal("a byte-identical later copy must still collapse the earlier output")
	}
}

// The newest trustworthy copy stays the comparison base, so an intermediate
// differing copy neither shields an older identical one nor gets collapsed.
func TestDetectRepeatedToolOutputsComparesAgainstNewestCopy(t *testing.T) {
	args := json.RawMessage(`{"command":"go test ./..."}`)
	call := func(id string) message.Message {
		return message.Message{Role: "assistant", ToolCalls: []message.ToolCall{{ID: id, Name: tools.NameShell, Args: args}}}
	}
	result := func(id, content string) message.Message {
		return message.Message{Role: "tool", ToolCallID: id, Content: content, ToolStatus: message.ToolStatusSuccess}
	}
	msgs := []message.Message{
		call("tc1"), result("tc1", "ok"),
		call("tc2"), result("tc2", "FAIL agent"),
		call("tc3"), result("tc3", "ok"),
	}
	repeated := detectRepeatedToolOutputs(msgs, BuildToolCallMeta(msgs), nil)
	if !repeated[1] {
		t.Fatal("the oldest copy matches the newest content and must collapse")
	}
	if repeated[3] {
		t.Fatal("the failing middle run differs from the newest copy and must be preserved")
	}
	if repeated[5] {
		t.Fatal("the newest copy is never repeated")
	}
}
