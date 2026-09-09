package agent

import (
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func newMixedBatchTestSubAgentForCheckpoint(t *testing.T) *SubAgent {
	_, sub := newMixedBatchTestSubAgent(t)
	return sub
}

func TestSubAgentStructuredCheckpointExtractsAuthoritativeState(t *testing.T) {
	sub := newMixedBatchTestSubAgentForCheckpoint(t)
	sub.taskDesc = "Refactor the parser and keep the CLI output format unchanged"
	sub.ownerAgentID = "main"
	sub.ownerTaskID = "task-7"
	sub.writeScope = tools.WriteScope{Files: []string{"internal/parser/parser.go"}, ReadOnly: false}
	sub.taskChangesMu.Lock()
	sub.actualChangedFiles = map[string]struct{}{"internal/parser/parser.go": {}, "internal/parser/lexer.go": {}}
	sub.taskChangesMu.Unlock()
	sub.runtimeState.set(SubAgentStateRunning, "")

	messages := []message.Message{
		{Role: message.RoleUser, Content: "开始重构"},
		{Role: message.RoleTool, ToolCallID: "call-e1", Content: "command failed\n\nError: exit code 1", ToolStatus: string(ToolResultStatusError)},
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "call-e1", Name: tools.NameShell}}},
		{Role: message.RoleTool, ToolCallID: "call-e2", Content: "edited 3 lines", ToolStatus: string(ToolResultStatusSuccess)},
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "call-e2", Name: tools.NameEdit}}},
		{Role: message.RoleUser, Content: "输出格式保持不变，只调整内部实现"},
	}
	checkpoint := buildSubAgentStructuredCheckpoint(sub, messages, 40, "context_length_exceeded", "archives/sub-1.md")

	for _, want := range []string{
		"SubAgent context checkpoint: 40 earlier messages were removed for context_length_exceeded",
		"Task: Refactor the parser and keep the CLI output format unchanged",
		"Owner: main/task-7",
		"Write scope: files=internal/parser/parser.go",
		"Latest owner/user instruction: 输出格式保持不变，只调整内部实现 (constraint)",
		"Files read or changed: internal/parser/lexer.go, internal/parser/parser.go",
		"Completed actions: edit: edited 3 lines",
		"Known failures: shell: command failed",
		"Full pre-checkpoint history: archives/sub-1.md.",
	} {
		if !strings.Contains(checkpoint, want) {
			t.Errorf("checkpoint missing %q:\n%s", want, checkpoint)
		}
	}
	sub.cancel()
}

// Fields with no derivable source must be marked "unknown", never guessed.
func TestSubAgentStructuredCheckpointMarksUnknownFields(t *testing.T) {
	_, sub := newMixedBatchTestSubAgent(t)
	sub.taskDesc = ""
	sub.runtimeState.set(SubAgentStateRunning, "")
	checkpoint := buildSubAgentStructuredCheckpoint(sub, nil, 5, "proactive", "archives/sub-2.md")
	for _, field := range []string{"Task", "Owner", "Write scope", "Latest owner/user instruction", "Files read or changed", "Completed actions", "Known failures", "Open blocker"} {
		if !strings.Contains(checkpoint, "- "+field+": unknown") {
			t.Errorf("field %q should be 'unknown' when no source exists:\n%s", field, checkpoint)
		}
	}
	sub.cancel()
}

// The checkpoint must not propose redoing a failed approach: the explicit
// failure is surfaced verbatim in Known failures.
func TestSubAgentStructuredCheckpointDoesNotRepeatFailedApproach(t *testing.T) {
	_, sub := newMixedBatchTestSubAgent(t)
	sub.taskDesc = "Add pagination"
	messages := []message.Message{
		{Role: message.RoleTool, ToolCallID: "call-f", Content: "undefined: x\n\nError: exit code 2", ToolStatus: string(ToolResultStatusError)},
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "call-f", Name: tools.NameShell}}},
	}
	checkpoint := buildSubAgentStructuredCheckpoint(sub, messages, 3, "proactive", "archives/sub-3.md")
	if !strings.Contains(checkpoint, "Known failures: shell: undefined: x") {
		t.Errorf("known failure lost: %s", checkpoint)
	}
	sub.cancel()
}

// An explicit open blocker from the runtime state must survive compression.
func TestSubAgentStructuredCheckpointPreservesOpenBlocker(t *testing.T) {
	_, sub := newMixedBatchTestSubAgent(t)
	sub.runtimeState.set(SubAgentStateWaitingMain, "waiting for owner to approve the write scope change")
	checkpoint := buildSubAgentStructuredCheckpoint(sub, nil, 4, "proactive", "archives/sub-4.md")
	if !strings.Contains(checkpoint, "Open blocker: waiting_main: waiting for owner to approve the write scope change") {
		t.Errorf("open blocker lost: %s", checkpoint)
	}
	sub.cancel()
}
