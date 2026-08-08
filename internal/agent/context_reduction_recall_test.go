package agent

import (
	"context"
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
	policy := defaultContextReductionPolicy()
	content := strings.Repeat("plain command output describing repository layout\n", 120)
	if len(content) <= policy.ShellSuccessBytes {
		t.Fatal("fixture must exceed the shell success byte threshold")
	}
	for _, tc := range []struct {
		age  int
		want requestReductionClass
	}{
		{age: policy.ShellSuccessAgeTurns, want: requestReductionNone},
		{age: policy.ShellReadOnlyAgeTurns - 1, want: requestReductionNone},
		{age: policy.ShellReadOnlyAgeTurns, want: requestReductionShellOK},
	} {
		ctx := requestReductionContext{
			ToolName:      tools.NameShell,
			Content:       content,
			Age:           tc.age,
			Policy:        policy,
			ShellReadOnly: true,
		}
		if got := classifyRequestReductionToolOutput(ctx); got != tc.want {
			t.Fatalf("age %d: class = %q, want %q", tc.age, got, tc.want)
		}
	}
	// A mutating shell command keeps the original, more aggressive age.
	ctx := requestReductionContext{
		ToolName: tools.NameShell,
		Content:  content,
		Age:      policy.ShellSuccessAgeTurns,
		Policy:   policy,
	}
	if got := classifyRequestReductionToolOutput(ctx); got != requestReductionShellOK {
		t.Fatalf("mutating shell class = %q, want shell success", got)
	}
}

func TestClassifyJSONDocumentWaitsForStaleAge(t *testing.T) {
	policy := defaultContextReductionPolicy()
	doc := `{"name":"example","values":[` + strings.Repeat(`"padding entry",`, 400) + `"tail"]}`
	if len(doc) <= policy.ShellSuccessBytes {
		t.Fatal("fixture must exceed the shell success byte threshold")
	}
	for _, tc := range []struct {
		age  int
		want requestReductionClass
	}{
		{age: policy.ShellSuccessAgeTurns, want: requestReductionNone},
		{age: policy.StaleAgeTurns - 1, want: requestReductionNone},
		{age: policy.StaleAgeTurns, want: requestReductionJSON},
	} {
		ctx := requestReductionContext{
			ToolName: tools.NameShell,
			Content:  doc,
			Age:      tc.age,
			Policy:   policy,
		}
		if got := classifyRequestReductionToolOutput(ctx); got != tc.want {
			t.Fatalf("age %d: class = %q, want %q", tc.age, got, tc.want)
		}
	}
	// NDJSON log streams (go test -json) get no extended JSON retention.
	ndjson := strings.Repeat(`{"Action":"pass","Package":"example"}`+"\n", 200)
	ctx := requestReductionContext{
		ToolName: tools.NameShell,
		Content:  ndjson,
		Age:      policy.ShellSuccessAgeTurns,
		Policy:   policy,
	}
	if got := classifyRequestReductionToolOutput(ctx); got != requestReductionJSON {
		t.Fatalf("ndjson class = %q, want json", got)
	}
}

func TestClassifyStaleWebFetchJSONKeepsReadLikeSummary(t *testing.T) {
	policy := defaultContextReductionPolicy()
	doc := `{"payload":"` + strings.Repeat("x", policy.StaleOutputBytes) + `"}`
	if len(doc) <= policy.StaleOutputBytes || len(doc) > policy.ReadLikeOutputBytes {
		t.Fatalf("fixture bytes = %d, want (%d, %d]", len(doc), policy.StaleOutputBytes, policy.ReadLikeOutputBytes)
	}
	ctx := requestReductionContext{
		ToolName:    tools.NameWebFetch,
		Content:     doc,
		Age:         policy.StaleAgeTurns,
		Policy:      policy,
		ToolResults: policy.MinToolResultsPrune,
	}
	if got := classifyRequestReductionToolOutput(ctx); got != requestReductionReadLike {
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
		{Role: "tool", ToolCallID: "tc2", Content: "grep failed", ToolStatus: string(ToolResultStatusError)},
	}
	if repeated := detectRepeatedToolOutputs(msgs, buildToolCallMeta(msgs)); len(repeated) != 0 {
		t.Fatalf("a failed rerun must not mark the earlier success repeated: %v", repeated)
	}
	msgs[3].ToolStatus = ""
	msgs[3].Content = "Error: grep failed"
	if repeated := detectRepeatedToolOutputs(msgs, buildToolCallMeta(msgs)); len(repeated) != 0 {
		t.Fatalf("a legacy rendered error must not mark the earlier success repeated: %v", repeated)
	}
	msgs[3].ToolStatus = string(ToolResultStatusSuccess)
	msgs[3].Content = "a.go:1: callSite()"
	if repeated := detectRepeatedToolOutputs(msgs, buildToolCallMeta(msgs)); !repeated[1] {
		t.Fatal("a successful rerun must mark the earlier output repeated")
	}
	msgs[3].Content = "a.go:1: callSite() // Error: wrapped by caller"
	if repeated := detectRepeatedToolOutputs(msgs, buildToolCallMeta(msgs)); !repeated[1] {
		t.Fatal("an explicit success mentioning Error: mid-output must still establish the fresher copy")
	}
	msgs[3].ToolStatus = ""
	if repeated := detectRepeatedToolOutputs(msgs, buildToolCallMeta(msgs)); !repeated[1] {
		t.Fatal("a status-less result not rendered as an error must still establish the fresher copy")
	}
}

func TestCanonicalRepeatedToolCallArgsPreservesLargeIntegerIdentity(t *testing.T) {
	left := canonicalRepeatedToolCallArgs(json.RawMessage(`{"limit":9007199254740992}`))
	right := canonicalRepeatedToolCallArgs(json.RawMessage(`{"limit":9007199254740993}`))
	if left == right {
		t.Fatalf("distinct JSON integers collapsed to the same input key: %q", left)
	}
}

func TestPrepareMessagesForLLMRecallProtectsNewestOutput(t *testing.T) {
	a := &MainAgent{parentCtx: context.Background()}
	a.newTurn()
	args := json.RawMessage(`{"pattern":"callSite","paths":["internal"]}`)
	output := bigSearchOutput(80, 2)
	if len(output) <= defaultContextReductionPolicy().ReadLikeOutputBytes {
		t.Fatal("fixture must exceed the read-like byte threshold")
	}

	// Pass 1: the only copy ages out and is genuinely summarized away. This is
	// the discard the later re-issue proves was premature.
	msgs := []message.Message{
		{Role: "user", Content: "u1"},
		{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "tc1", Name: tools.NameGrep, Args: args}}},
		{Role: "tool", ToolCallID: "tc1", Content: output},
		{Role: "user", Content: "u2"},
		{Role: "user", Content: "u3"},
	}
	prepared := a.prepareMessagesForLLM(msgs)
	if prepared[2].Content == output {
		t.Fatalf("fixture must be summarized on the first pass, got full output")
	}

	// Pass 2: the model re-issues the identical call. The discarded-input
	// evidence must survive across passes and protect the newest output.
	msgs = append(msgs,
		message.Message{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "tc2", Name: tools.NameGrep, Args: args}}},
		message.Message{Role: "tool", ToolCallID: "tc2", Content: output},
		message.Message{Role: "user", Content: "u4"},
		message.Message{Role: "user", Content: "u5"},
	)
	prepared = a.prepareMessagesForLLM(msgs)
	if prepared[6].Content != output {
		t.Fatalf("re-fetched output must stay unreduced, got %q", compactTextSnippet(prepared[6].Content, 120))
	}
	stats := a.GetContextReductionStats()
	if stats.SkippedByReason[contextReductionSkipRecalledInput] == 0 {
		t.Fatalf("expected recalled-input skip to be recorded: %v", stats.SkippedByReason)
	}
	if len(a.recalledReductionInputsSnapshot()) != 1 {
		t.Fatalf("expected one recalled input key, got %v", a.recalledReductionInputsSnapshot())
	}

	// The protection is durable session state: on a later, older request the
	// newest copy still survives while an ordinary search of that age would
	// have been reduced.
	msgs = append(msgs, message.Message{Role: "user", Content: "u6"}, message.Message{Role: "user", Content: "u7"})
	prepared = a.prepareMessagesForLLM(msgs)
	if prepared[6].Content != output {
		t.Fatalf("recalled input lost protection on a later request: %q", compactTextSnippet(prepared[6].Content, 120))
	}
}

func TestPrepareMessagesForLLMRepeatedCollapseIsNotRecallEvidence(t *testing.T) {
	a := &MainAgent{parentCtx: context.Background()}
	a.newTurn()
	args := json.RawMessage(`{"pattern":"callSite","paths":["internal"]}`)
	output := bigSearchOutput(80, 2)
	// Two identical live copies in one pass: the older collapses to a repeated
	// marker, which leaves the fresher full copy in context — nothing was
	// discarded, so the redundant re-issue must not mint recall protection.
	msgs := []message.Message{
		{Role: "user", Content: "u1"},
		{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "tc1", Name: tools.NameGrep, Args: args}}},
		{Role: "tool", ToolCallID: "tc1", Content: output},
		{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "tc2", Name: tools.NameGrep, Args: args}}},
		{Role: "tool", ToolCallID: "tc2", Content: output},
		{Role: "user", Content: "u2"},
		{Role: "user", Content: "u3"},
	}
	prepared := a.prepareMessagesForLLM(msgs)
	if !strings.Contains(prepared[2].Content, "Repeated") {
		t.Fatalf("older duplicate should collapse to the repeated marker: %q", compactTextSnippet(prepared[2].Content, 120))
	}
	if prepared[4].Content == output {
		t.Fatal("newest copy must follow the ordinary rules, not gain recall protection from a repeated collapse")
	}
	if got := a.recalledReductionInputsSnapshot(); len(got) != 0 {
		t.Fatalf("model redundancy must not register recall evidence, got %v", got)
	}
}

func TestPrepareMessagesForLLMMutatingShellGetsNoRecallProtection(t *testing.T) {
	a := &MainAgent{}
	args := json.RawMessage(`{"command":"go test ./..."}`)
	output := strings.Repeat("internal/agent/file.go:10: callSite()\n", 160)
	msgs := []message.Message{
		{Role: "user", Content: "u1"},
		{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "tc1", Name: tools.NameShell, Args: args}}},
		{Role: "tool", ToolCallID: "tc1", Content: output},
		{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "tc2", Name: tools.NameShell, Args: args}}},
		{Role: "tool", ToolCallID: "tc2", Content: output},
		{Role: "user", Content: "u2"},
		{Role: "user", Content: "u3"},
	}

	prepared := a.prepareMessagesForLLM(msgs)
	if prepared[4].Content == output || !strings.Contains(prepared[4].Content, "summarized") {
		t.Fatalf("mutating shell rerun should still reduce, got %q", compactTextSnippet(prepared[4].Content, 120))
	}
	if recalled := a.recalledReductionInputsSnapshot(); len(recalled) != 0 {
		t.Fatalf("mutating shell rerun must not enter recall protection: %v", recalled)
	}
}

func TestPrepareMessagesForLLMReadOnlyShellKeepsOutputUntilReadOnlyAge(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(tools.ShellTool{})
	a := &MainAgent{tools: reg}
	output := strings.Repeat("repository layout description without risky markers\n", 120)
	msgs := []message.Message{
		{Role: "user", Content: "u1"},
		{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "tc1", Name: tools.NameShell, Args: json.RawMessage(`{"command":"cat docs/layout.md"}`)}}},
		{Role: "tool", ToolCallID: "tc1", Content: output},
		{Role: "user", Content: "u2"},
		{Role: "user", Content: "u3"},
	}
	if got := a.prepareMessagesForLLM(msgs)[2].Content; got != output {
		t.Fatalf("read-only shell output reduced before its protection age: %q", compactTextSnippet(got, 120))
	}
	msgs = append(msgs, message.Message{Role: "user", Content: "u4"})
	if got := a.prepareMessagesForLLM(msgs)[2].Content; !strings.Contains(got, "success summarized") {
		t.Fatalf("read-only shell output should reduce at its protection age: %q", compactTextSnippet(got, 120))
	}
}

func TestShellCommandReadOnlyMemoizesVerdictPerToolCallID(t *testing.T) {
	a := &MainAgent{}
	if a.shellCommandReadOnly("tc1", `{"command":"rm -rf ./tmp"}`) {
		t.Fatal("a mutating command must not classify read-only")
	}
	if verdict, ok := a.shellReadOnlyClass.verdicts["tc1"]; !ok || verdict {
		t.Fatalf("verdict for tc1 must be memoized as false, got ok=%v verdict=%v", ok, verdict)
	}
	// A memoized verdict wins without re-parsing the args.
	a.shellReadOnlyClass.verdicts["tc1"] = true
	if !a.shellCommandReadOnly("tc1", `{"command":"rm -rf ./tmp"}`) {
		t.Fatal("the memoized verdict must be returned as-is")
	}
	// Calls without a ToolCallID classify directly and stay out of the memo.
	if a.shellCommandReadOnly("", `{"command":"rm -rf ./tmp"}`) {
		t.Fatal("an empty ToolCallID must classify directly")
	}
	if len(a.shellReadOnlyClass.verdicts) != 1 {
		t.Fatalf("empty ToolCallID must not be memoized: %v", a.shellReadOnlyClass.verdicts)
	}
}
