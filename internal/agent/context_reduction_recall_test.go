package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/agent/requesttrim"
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

func TestCanonicalRepeatedToolCallArgsPreservesLargeIntegerIdentity(t *testing.T) {
	left := canonicalRepeatedToolCallArgs(json.RawMessage(`{"limit":9007199254740992}`))
	right := canonicalRepeatedToolCallArgs(json.RawMessage(`{"limit":9007199254740993}`))
	if left == right {
		t.Fatalf("distinct JSON integers collapsed to the same input key: %q", left)
	}
}

func TestPrepareMessagesForLLMRecallProtectsNewestOutput(t *testing.T) {
	a := &MainAgent{parentCtx: context.Background()}
	a.newTurn("")
	args := json.RawMessage(`{"pattern":"callSite","paths":["internal"]}`)
	output := bigSearchOutput(80, 2)
	if len(output) <= requesttrim.DefaultPolicy().ReadLikeOutputBytes {
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
		t.Fatalf("re-fetched output must stay unreduced, got %q", requesttrim.TextSnippet(prepared[6].Content, 120))
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
		t.Fatalf("recalled input lost protection on a later request: %q", requesttrim.TextSnippet(prepared[6].Content, 120))
	}
}

func TestRecordDiscardedInputEvidenceIsBounded(t *testing.T) {
	evidence := make(map[string]string, reductionRecallProtectMaxKeys)
	for i := range reductionRecallProtectMaxKeys {
		recordDiscardedInputEvidence(evidence, fmt.Sprintf("key-%d", i), fmt.Sprintf("call-%d", i))
	}
	recordDiscardedInputEvidence(evidence, "new-key", "new-call")
	if len(evidence) != reductionRecallProtectMaxKeys {
		t.Fatalf("discarded input evidence size = %d, want cap %d", len(evidence), reductionRecallProtectMaxKeys)
	}
	// Updating existing evidence remains allowed after the cap so a current
	// call ID is not made stale merely because the session is large.
	recordDiscardedInputEvidence(evidence, "key-0", "new-call-0")
	if got := evidence["key-0"]; got != "new-call-0" {
		t.Fatalf("existing evidence = %q, want updated call ID", got)
	}
}

func TestPrepareMessagesForLLMRepeatedCollapseIsNotRecallEvidence(t *testing.T) {
	a := &MainAgent{parentCtx: context.Background()}
	a.newTurn("")
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
		t.Fatalf("older duplicate should collapse to the repeated marker: %q", requesttrim.TextSnippet(prepared[2].Content, 120))
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
		t.Fatalf("mutating shell rerun should still reduce, got %q", requesttrim.TextSnippet(prepared[4].Content, 120))
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
		t.Fatalf("read-only shell output reduced before its protection age: %q", requesttrim.TextSnippet(got, 120))
	}
	msgs = append(msgs, message.Message{Role: "user", Content: "u4"})
	if got := a.prepareMessagesForLLM(msgs)[2].Content; !strings.Contains(got, "success summarized") {
		t.Fatalf("read-only shell output should reduce at its protection age: %q", requesttrim.TextSnippet(got, 120))
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

func TestShellCommandReadOnlyMapsExpandedQueryCommands(t *testing.T) {
	// The recall-protection and content-fetch mapping: these expanded-table
	// commands must enjoy read-like treatment, while writes must not. The
	// boolean-to-protection mechanism itself is covered by fixture tests;
	// this pins the command-to-boolean mapping it consumes.
	registry := tools.NewRegistry()
	registry.Register(tools.NewShellTool("bash"))
	a := &MainAgent{tools: registry}
	readonly := []string{
		`{"command":"rg --no-config -n pat --glob '*.go'"}`,
		`{"command":"find . -name '*.go'"}`,
		`{"command":"git blame f"}`,
		`{"command":"sed -n '1,20p' f"}`,
		`{"command":"git log | head -20"}`,
	}
	for i, args := range readonly {
		// Unique ToolCallIDs: the memo keys on call ID, so reusing one
		// would return the first verdict for every later command.
		if got := a.shellCommandReadOnly(fmt.Sprintf("ro-%d", i), args); !got {
			t.Fatalf("shellCommandReadOnly(%s) = false, want true", args)
		}
	}
	mutating := []string{
		`{"command":"rm -rf ./tmp"}`,
		`{"command":"touch other"}`,
		`{"command":"sort {-o,out} in"}`,
	}
	for i, args := range mutating {
		if a.shellCommandReadOnly(fmt.Sprintf("rw-%d", i), args) {
			t.Fatalf("shellCommandReadOnly(%s) = true, want false", args)
		}
	}
}
