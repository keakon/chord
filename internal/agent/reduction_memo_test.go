package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

// The memo turns the per-request whole-history scans (call metadata, output
// digests, input keys, trust verdicts, shell parses) into per-ToolCallID
// lookups. The scans run on every LLM request through the stable-surface
// review even when the reuse path skips the full reduction pass, and a fresh
// scan is built per request, so this benchmark reconstructs the scan each
// iteration against a shared agent memo — the production steady state.
// BenchmarkPrepareMessagesForLLMCold covers the one-time population cost.
func BenchmarkReductionHistoryScan(b *testing.B) {
	for _, toolResults := range []int{100, 1000} {
		b.Run(fmt.Sprintf("tool_results_%d", toolResults), func(b *testing.B) {
			a := benchmarkContextReductionAgent()
			messages := benchmarkContextReductionMessages(toolResults)
			a.prepareMessagesForLLMWithOptions(messages, false)
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				scan := newReductionHistoryScanForAgent(a, messages)
				scan.callMeta()
				scan.repeatedOutputs()
			}
		})
	}
}

func memoEquivalenceMessages() []message.Message {
	output := strings.Repeat("line from a build log\n", 80)
	msgs := make([]message.Message, 0, 13)
	for i := range 4 {
		id := fmt.Sprintf("shell-%d", i)
		args, _ := json.Marshal(map[string]string{"command": fmt.Sprintf("git log --oneline -%d", i+3)})
		msgs = append(msgs,
			message.Message{Role: message.RoleUser, Content: fmt.Sprintf("run log %d", i)},
			message.Message{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: id, Name: tools.NameShell, Args: args}}},
			message.Message{Role: message.RoleTool, ToolCallID: id, ToolStatus: "success", Content: output},
		)
	}
	// A repeated pair with identical args and content must collapse the same
	// way with and without the memo.
	args, _ := json.Marshal(map[string]string{"command": "git log --oneline -3"})
	msgs = append(msgs,
		message.Message{Role: message.RoleUser, Content: "run log again"},
		message.Message{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "shell-repeat", Name: tools.NameShell, Args: args}}},
		message.Message{Role: message.RoleTool, ToolCallID: "shell-repeat", ToolStatus: "success", Content: output},
		message.Message{Role: message.RoleUser, Content: "continue"},
	)
	return msgs
}

// The memo must be invisible to the reduction output: preparing the same
// history on an agent whose verdicts are already cached must produce bytes
// identical to a fresh agent computing every verdict for the first time.
func TestReductionMemoProducesIdenticalSurface(t *testing.T) {
	warm := benchmarkContextReductionAgent()
	cold := benchmarkContextReductionAgent()
	messages := memoEquivalenceMessages()

	// Warm the memo over the full history plus a shorter prefix, so both
	// cached and freshly computed verdicts feed the final prepare.
	warm.prepareMessagesForLLMWithOptions(messages[:6], false)

	got := warm.prepareMessagesForLLM(messages)
	want := cold.prepareMessagesForLLM(messages)
	if len(got) != len(want) {
		t.Fatalf("prepared length = %d, want %d", len(got), len(want))
	}
	for i := range got {
		if got[i].Content != want[i].Content {
			t.Fatalf("message %d content diverges with warm memo:\nwarm: %q\ncold: %q", i, got[i].Content, want[i].Content)
		}
	}
}

// Memo maps are bounded: crossing the cap drops the map wholesale and the
// next prepare rebuilds it without changing the reduction output.
func TestReductionMemoCapDropsWholesale(t *testing.T) {
	a := benchmarkContextReductionAgent()
	messages := memoEquivalenceMessages()
	want := benchmarkContextReductionAgent().prepareMessagesForLLM(messages)

	for i := 0; i < reductionToolCallMemoMaxEntries; i++ {
		a.reductionMemo.toolResultDigest(fmt.Sprintf("cap-%d", i), "x")
	}
	if got := a.prepareMessagesForLLM(messages); len(got) != len(want) {
		t.Fatalf("prepared length after cap reset = %d, want %d", len(got), len(want))
	}
	if len(a.reductionMemo.digests) > reductionToolCallMemoMaxEntries {
		t.Fatalf("digest memo exceeded cap: %d", len(a.reductionMemo.digests))
	}
}
