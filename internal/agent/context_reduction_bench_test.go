package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

// benchmarkProtectedShellMessages builds one long turn of large shell results.
// They all stay complete under the high-risk protection, so the pass runs the
// protection chain over every one of them and renders nothing.
func benchmarkProtectedShellMessages(results int) []message.Message {
	messages := make([]message.Message, 0, results*2+1)
	messages = append(messages, message.Message{Role: message.RoleUser, Content: "run the pipeline"})
	body := strings.Repeat("step failed while processing a record\n", 200)
	for i := range results {
		id := fmt.Sprintf("call-%d", i)
		args, _ := json.Marshal(map[string]any{"command": fmt.Sprintf("./pipeline --stage %d", i)})
		messages = append(messages,
			message.Message{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: id, Name: tools.NameShell, Args: args}}},
			message.Message{Role: message.RoleTool, ToolCallID: id, Content: body, ToolStatus: "success"},
		)
	}
	return messages
}

func BenchmarkPrepareMessagesForLLMProtectedShell(b *testing.B) {
	a := &MainAgent{projectConfig: config.DefaultConfig(), turn: &Turn{ID: 1}}
	a.freezeToolSurfaceFromDefinitions(nil)
	messages := benchmarkProtectedShellMessages(200)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		prepared := a.prepareMessagesForLLMWithOptions(messages, false)
		if len(prepared) != len(messages) {
			b.Fatalf("prepared messages = %d, want %d", len(prepared), len(messages))
		}
	}
}
