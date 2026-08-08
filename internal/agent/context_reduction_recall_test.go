package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

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
