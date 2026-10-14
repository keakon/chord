package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

// A time-varying command (git status, go test, ls) reissued with identical
// arguments produces a different output, so the earlier result is the "before"
// state rather than a duplicate. It must reach the shape-based summarizers
// instead of collapsing to a marker asserting an identical later call.
func TestTimeVaryingShellRerunIsSummarizedNotCollapsedAsRepeated(t *testing.T) {
	a := &MainAgent{}
	args := json.RawMessage(`{"command":"go test ./..."}`)
	before := "FAIL\tgithub.com/keakon/chord/internal/agent\n" + strings.Repeat("--- FAIL: TestOld (0.01s)\n", 200)
	after := "ok\tgithub.com/keakon/chord/internal/agent\t1.2s\n"
	msgs := []message.Message{
		{Role: message.RoleUser, Content: "u1"},
		{Role: message.RoleAssistant, RequestBatch: 1, ToolCalls: []message.ToolCall{{ID: "tc1", Name: tools.NameShell, Args: args}}},
		{Role: message.RoleTool, ToolCallID: "tc1", ToolStatus: "success", Content: before},
		{Role: message.RoleAssistant, RequestBatch: 2, ToolCalls: []message.ToolCall{{ID: "tc2", Name: tools.NameShell, Args: args}}},
		{Role: message.RoleTool, ToolCallID: "tc2", ToolStatus: "success", Content: after},
	}
	setTestRequestBatch(a, msgs, 6)
	prepared := a.prepareMessagesForLLM(msgs)
	if strings.Contains(prepared[2].Content, "Repeated ") {
		t.Fatalf("differing earlier output must not collapse to the repeated marker: %q", prepared[2].Content)
	}
	if len(prepared[2].Content) >= len(before) {
		t.Fatalf("the earlier output should still be summarized once past its age gates: %q", prepared[2].Content)
	}
	if !strings.Contains(prepared[2].Content, "FAIL") {
		t.Fatalf("the before-state summary must keep its failing evidence: %q", prepared[2].Content)
	}
	if prepared[4].Content != after {
		t.Fatalf("the newest output must stay verbatim: %q", prepared[4].Content)
	}

	// A byte-identical rerun still collapses: nothing is lost because the
	// later copy carries the same content. The rerun rewrites this fixture's
	// tool result in place, which production history never does (a real rerun
	// appends a new call ID), so drop the per-call verdicts the previous
	// prepare cached for the old content first.
	a.clearReductionCache(true)
	msgs[4].Content = before
	prepared = a.prepareMessagesForLLM(msgs)
	if !strings.Contains(prepared[2].Content, "Repeated ") {
		t.Fatalf("identical rerun must still collapse: %q", prepared[2].Content)
	}
}
