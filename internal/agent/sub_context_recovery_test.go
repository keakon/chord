package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
)

func TestSubAgentContextLengthRecoveryCompressesAndRetriesOnce(t *testing.T) {
	provider := &blockingStreamProvider{calls: []scriptedStreamCall{
		{err: &llm.APIError{StatusCode: 400, Code: "context_length_exceeded", Message: "input is too long"}},
		{resp: &message.Response{Content: "recovered", StopReason: "stop"}},
	}}
	providerCfg := llm.NewProviderConfig("test", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"model": {Limit: config.ModelLimit{Context: 12000, Input: 10000, Output: 1024}},
		},
	}, []string{"key"})
	client := llm.NewClient(providerCfg, provider, "model", 1024, "")

	parent, sub := newMixedBatchTestSubAgent(t)
	sub.llmMu.Lock()
	sub.llmClient = client
	sub.llmMu.Unlock()
	sub.ctxMgr.SetTokenBudgets(12000, 10000, 0)
	messages := []message.Message{{Role: message.RoleUser, Content: "task"}}
	for range 14 {
		messages = append(messages,
			message.Message{Role: message.RoleAssistant, Content: strings.Repeat("analysis ", 420)},
			message.Message{Role: message.RoleUser, Content: "continue"},
		)
	}
	sub.ctxMgr.RestoreMessages(messages)
	sub.llmRequestInFlight.Store(true)
	sub.asyncCallLLMWithFlightMarked(sub.turn, sub.ctxMgr.Snapshot())

	deadline := time.After(3 * time.Second)
	for sub.turn.SubAgentContextRecoveryCount == 0 {
		select {
		case result := <-sub.llmCh:
			sub.finishLLMRequest()
			sub.handleLLMResponse(result)
		case <-deadline:
			t.Fatal("timed out waiting for context recovery")
		}
	}
	select {
	case result := <-sub.llmCh:
		sub.finishLLMRequest()
		sub.handleLLMResponse(result)
	case <-deadline:
		t.Fatal("timed out waiting for context recovery retry")
	}

	got := sub.ctxMgr.Snapshot()
	if len(got) >= len(messages) {
		t.Fatalf("recovered message count = %d, want less than %d", len(got), len(messages))
	}
	checkpointFound := false
	archiveRef := ""
	for _, msg := range got {
		if strings.Contains(msg.Content, "SubAgent context checkpoint") {
			checkpointFound = true
			if idx := strings.Index(msg.Content, "Full pre-checkpoint history: "); idx >= 0 {
				archiveRef = strings.TrimSuffix(msg.Content[idx+len("Full pre-checkpoint history: "):], ".")
			}
			break
		}
	}
	if !checkpointFound {
		t.Fatal("context checkpoint message not found")
	}
	if archiveRef == "" || archiveRef == "unavailable" {
		t.Fatalf("context checkpoint archive ref = %q, want durable artifact path", archiveRef)
	}
	archivePath := filepath.Join(sub.sessionDir, filepath.FromSlash(archiveRef))
	archiveData, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatalf("ReadFile(context archive): %v", err)
	}
	if !strings.Contains(string(archiveData), strings.Repeat("analysis ", 100)) {
		t.Fatal("context archive does not contain pre-checkpoint history")
	}
	if sub.turn.SubAgentContextRecoveryCount != 1 {
		t.Fatalf("context recovery count = %d, want 1", sub.turn.SubAgentContextRecoveryCount)
	}
	select {
	case evt := <-parent.eventCh:
		if evt.Type == EventAgentError {
			t.Fatalf("unexpected agent error after successful recovery: %v", evt.Payload)
		}
	default:
	}
}

func TestSubAgentProactiveContextCompressionRecordsReductionStats(t *testing.T) {
	parent, sub := newMixedBatchTestSubAgent(t)
	sub.taskDesc = "preserve the task contract"
	sub.ownerAgentID = "main"
	sub.compactUsage = 0.5
	sub.ctxMgr.SetTokenBudgets(3000, 2400, 0)
	messages := []message.Message{{Role: message.RoleUser, Content: "task"}}
	for range 12 {
		messages = append(messages,
			message.Message{Role: message.RoleAssistant, Content: strings.Repeat("analysis ", 160)},
			message.Message{Role: message.RoleUser, Content: "continue"},
		)
	}
	sub.ctxMgr.RestoreMessages(messages)

	prepared := sub.prepareContextForLLM(messages)
	if len(prepared) >= len(messages) {
		t.Fatalf("prepared message count = %d, want less than %d", len(prepared), len(messages))
	}
	stats := sub.GetContextReductionStats()
	if stats.Messages <= 0 || stats.TokensSaved <= 0 {
		t.Fatalf("reduction stats = %+v, want positive savings", stats)
	}
	orchestration := parent.OrchestrationStats()
	if orchestration.SubAgentCompactions != 1 || orchestration.SubAgentTokensSaved == 0 {
		t.Fatalf("orchestration stats = %+v, want one compaction with savings", orchestration)
	}
	foundCheckpoint := false
	for _, msg := range prepared {
		if strings.Contains(msg.Content, "Preserve the task contract") && strings.Contains(msg.Content, "Full pre-checkpoint history") {
			foundCheckpoint = true
			break
		}
	}
	if !foundCheckpoint {
		t.Fatal("proactive compression checkpoint did not preserve task contract and archive reference")
	}
	sub.cancel()
}

func TestSubAgentContextLengthRecoveryIsBounded(t *testing.T) {
	parent, sub := newMixedBatchTestSubAgent(t)
	sub.ctxMgr.SetTokenBudgets(12000, 10000, 0)
	sub.turn.SubAgentContextRecoveryCount = 1
	err := &llm.ContextLengthExceededError{ProviderMessage: "too long"}
	if sub.recoverFromContextLength(err) {
		t.Fatal("second context recovery attempt should be rejected")
	}
	sub.handleLLMResponse(&llmResult{turnID: sub.turn.ID, err: err})
	select {
	case evt := <-parent.eventCh:
		if evt.Type != EventAgentError {
			t.Fatalf("event type = %q, want %q", evt.Type, EventAgentError)
		}
	default:
		t.Fatal("bounded context recovery did not report AgentError")
	}
	if sub.turn.SubAgentTerminalRecoveryCount != 0 {
		t.Fatalf("terminal recovery count = %d, want 0 for context-length error", sub.turn.SubAgentTerminalRecoveryCount)
	}
}

func TestSubAgentContextLengthRecoveryPreservesToolPairs(t *testing.T) {
	_, sub := newMixedBatchTestSubAgent(t)
	sub.ctxMgr.SetTokenBudgets(12000, 10000, 0)
	messages := []message.Message{{Role: message.RoleUser, Content: "task"}}
	for i := range 10 {
		callID := "call-" + string(rune('a'+i))
		messages = append(messages,
			message.Message{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: callID, Name: "Read"}}},
			message.Message{Role: message.RoleTool, ToolCallID: callID, Content: strings.Repeat("output ", 600)},
		)
	}
	sub.ctxMgr.RestoreMessages(messages)
	if !sub.recoverFromContextLength(&llm.ContextLengthExceededError{ProviderMessage: "too long"}) {
		t.Fatal("recoverFromContextLength() = false, want true")
	}
	if _, dropped := message.RepairOrphanToolResults(sub.ctxMgr.Snapshot()); dropped != 0 {
		t.Fatalf("context recovery left %d orphan tool results", dropped)
	}
	sub.cancel()
}

func TestSubAgentRejectsExcessiveToolCalls(t *testing.T) {
	parent, sub := newMixedBatchTestSubAgent(t)
	calls := make([]message.ToolCall, maxToolCallsPerResponse+1)
	for i := range calls {
		calls[i] = message.ToolCall{ID: fmt.Sprintf("call-%d", i), Name: "Read", Args: json.RawMessage(`{"path":"README.md"}`)}
	}
	sub.handleLLMResponse(&llmResult{
		turnID: sub.turn.ID,
		resp:   &message.Response{ToolCalls: calls, StopReason: "tool_use"},
	})
	select {
	case evt := <-parent.eventCh:
		if evt.Type != EventAgentError {
			t.Fatalf("event type = %q, want %q", evt.Type, EventAgentError)
		}
	case <-time.After(time.Second):
		t.Fatal("oversized SubAgent response did not report an error")
	}
	if got := len(sub.ctxMgr.Snapshot()); got != 0 {
		t.Fatalf("persisted messages = %d, want 0 for rejected response", got)
	}
}
