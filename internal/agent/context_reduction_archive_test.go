package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

// The full pipeline: a one-shot output routed through prepareMessagesForLLM
// with an archive dir becomes an archived marker with a readable file.
func TestPrepareMessagesForLLM_ArchivesIrreducibleJobOutput(t *testing.T) {
	t.Parallel()
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.projectConfig = &config.Config{Context: config.ContextConfig{Reduction: config.ContextReductionConfig{
		MinToolResultsPrune: 1,
		StaleAgeTurns:       1,
		StaleOutputBytes:    40,
	}}}
	a.newTurn("")
	content := strings.Repeat("job output log\n", 60)
	msgs := []message.Message{
		{Role: message.RoleUser, Content: "u1"},
		{Role: message.RoleAssistant, RequestBatch: 1, ToolCalls: []message.ToolCall{{ID: "sp1", Name: tools.NameJobOutput, Args: json.RawMessage(`{"job_id":"job-1"}`)}}},
		{Role: message.RoleTool, ToolCallID: "sp1", ToolStatus: "success", Content: content},
		{Role: message.RoleUser, Content: "u2"},
		{Role: message.RoleUser, Content: "u3"},
	}
	setTestRequestBatch(a, msgs, 2)
	prepared := a.prepareMessagesForLLM(msgs)
	if !strings.Contains(prepared[2].Content, "archived at ") {
		t.Fatalf("job output not archived through the pipeline: %q", prepared[2].Content)
	}
}
