package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/keakon/chord/internal/agent/requesttrim"
	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func TestPrepareMessagesForLLM_DefersUnchangedToolUntilFrontier(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.projectConfig = &config.Config{Context: config.ContextConfig{Reduction: config.ContextReductionConfig{
		MinToolResultsPrune:  1,
		StaleAgeTurns:        2,
		StaleOutputBytes:     40,
		ReadLikeAgeTurns:     9,
		ReadLikeOutputBytes:  1 << 20,
		ShellSuccessAgeTurns: 9,
		ShellSuccessBytes:    1 << 20,
		MinIncrementalTokens: 1,
	}}}
	a.newTurn("")
	messages := []message.Message{
		{Role: message.RoleUser, Content: "start"},
		{Role: message.RoleAssistant, RequestBatch: 1, ToolCalls: []message.ToolCall{{ID: "custom-1", Name: "custom_tool", Args: json.RawMessage(`{"query":"x"}`)}}},
		{Role: message.RoleTool, ToolCallID: "custom-1", Content: strings.Repeat("large unchanged output\n", 20)},
	}
	setTestRequestBatch(a, nil, 1)
	first := a.prepareMessagesForLLM(messages)
	if first[2].Content != messages[2].Content {
		t.Fatal("batch 1 should not reduce output before stale frontier")
	}

	secondMessages := append(append([]message.Message(nil), messages...), message.Message{Role: message.RoleUser, Content: "more detail"})
	second := a.prepareMessagesForLLM(secondMessages)
	if second[2].Content != messages[2].Content {
		t.Fatal("unchanged same-age output should remain intact")
	}
	if a.GetContextReductionStats().SkippedByReason[contextReductionSkipDeferredReview] == 0 {
		t.Fatal("unchanged output did not use the reduction frontier")
	}

	setTestRequestBatch(a, nil, 3)
	thirdMessages := append(append([]message.Message(nil), secondMessages...), message.Message{Role: message.RoleAssistant, RequestBatch: 3, Content: "continue"})
	third := a.prepareMessagesForLLM(thirdMessages)
	if third[2].Content == messages[2].Content {
		t.Fatal("output was not re-evaluated at the stale frontier")
	}
}

func TestPrepareMessagesForLLM_PrunesRepeatedAndErrorOutputs(t *testing.T) {
	a := &MainAgent{}

	msgs := []message.Message{
		{Role: "user", Content: "u1"},
		{Role: "assistant", ToolCalls: []message.ToolCall{
			{ID: "tc1", Name: "read", Args: json.RawMessage(`{"path":"a.go"}`)},
		}},
		{Role: "tool", ToolCallID: "tc1", Content: "very old read output"},
		{Role: "user", Content: "u2"},
		{Role: "assistant", ToolCalls: []message.ToolCall{
			{ID: "tc2", Name: "read", Args: json.RawMessage(`{"path":"a.go"}`)},
		}},
		{Role: "tool", ToolCallID: "tc2", Content: "new read output"},
		{Role: "user", Content: "u3"},
		{Role: "assistant", ToolCalls: []message.ToolCall{
			{ID: "tc3", Name: "shell", Args: json.RawMessage(`{"cmd":"bad"}`)},
		}},
		{Role: "tool", ToolCallID: "tc3", Content: "Error: command failed"},
		{Role: "user", Content: "u4"},
		{Role: "user", Content: "u5"},
		{Role: "user", Content: "u6"},
		{Role: "user", Content: "u7"},
		{Role: "user", Content: "u8"},
	}

	prepared := a.prepareMessagesForLLM(msgs)
	// The older identical read is superseded by the newer one, so it renders
	// as the validity-marked read summary (stable across incremental and full
	// scans) rather than the repeated marker.
	if !strings.Contains(prepared[2].Content, "A newer read of this range appears later") {
		t.Fatalf("expected superseded read output to be trimmed with guidance, got %q", prepared[2].Content)
	}
	if strings.Contains(prepared[2].Content, "Repeated ") {
		t.Fatalf("superseded read must not use the repeated marker, got %q", prepared[2].Content)
	}
	if !strings.Contains(prepared[8].Content, "Older shell error summarized") || !strings.Contains(prepared[8].Content, "Error: command failed") {
		t.Fatalf("expected old error output to be summarized, got %q", prepared[8].Content)
	}
}

func TestPrepareMessagesForLLM_PrunesOldReadLikeOutput(t *testing.T) {
	a := &MainAgent{projectConfig: &config.Config{Context: config.ContextConfig{Reduction: config.ContextReductionConfig{}}}}
	msgs := []message.Message{
		{Role: "user", Content: "u1"},
		{Role: "assistant", ToolCalls: []message.ToolCall{
			{ID: "tc1", Name: "read", Args: json.RawMessage(`{"path":"a.go"}`)},
		}},
		{Role: "tool", ToolCallID: "tc1", Content: strings.Repeat("large read output ", 400)},
		{Role: "user", Content: "u2"},
		{Role: "assistant", ToolCalls: []message.ToolCall{
			{ID: "tc2", Name: "edit", Args: json.RawMessage(`{"path":"a.go","old_string":"x","new_string":"y"}`)},
		}},
		{Role: "tool", ToolCallID: "tc2", ToolStatus: "success", Content: "edited"},
		{Role: "user", Content: "u3"},
	}

	prepared := a.prepareMessagesForLLM(msgs)
	if !strings.Contains(prepared[2].Content, "Older "+tools.NameRead+" output truncated for this request to save context; path=\"a.go\"") {
		t.Fatalf("expected invalidated read output to keep path hint, got %q", prepared[2].Content)
	}
	if !strings.Contains(prepared[2].Content, "large read output") {
		t.Fatalf("expected invalidated read output to keep a small excerpt, got %q", prepared[2].Content)
	}
	if !strings.Contains(prepared[2].Content, "File modified after this read") {
		t.Fatalf("expected stale note directing a re-read, got %q", prepared[2].Content)
	}
}

func TestPrepareMessagesForLLM_DoesNotMutateOriginalMessages(t *testing.T) {
	a := &MainAgent{projectConfig: &config.Config{Context: config.ContextConfig{Reduction: config.ContextReductionConfig{
		ReadLikeAgeTurns:     1,
		ReadLikeOutputBytes:  1,
		StaleAgeTurns:        99,
		StaleOutputBytes:     1,
		MinToolResultsPrune:  1,
		ShellSuccessAgeTurns: 99,
		ShellSuccessBytes:    1,
	}}}}
	original := []message.Message{
		{Role: "user", Content: "u1"},
		{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "tc1", Name: tools.NameWebFetch, Args: json.RawMessage(`{"url":"https://example.com/README.md"}`)}}},
		{Role: "tool", ToolCallID: "tc1", Content: strings.Repeat("line\n", 500)},
		{Role: "user", Content: "u2"},
		{Role: "user", Content: "u3"},
	}
	beforeBytes, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("json.Marshal(before): %v", err)
	}
	prepared := a.prepareMessagesForLLM(original)
	if reflect.DeepEqual(prepared, original) {
		t.Fatal("expected prepared messages to differ after reduction")
	}
	afterBytes, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("json.Marshal(after): %v", err)
	}
	if !bytes.Equal(beforeBytes, afterBytes) {
		t.Fatalf("prepareMessagesForLLM mutated original messages:\nbefore=%s\nafter=%s", beforeBytes, afterBytes)
	}
}

// TestPrepareMessagesForLLM_DisabledSkipsReduction pins the `reduction: false`
// boolean shorthand: with reduction disabled every message reaches the request
// untouched, including outputs every rule would otherwise trim.
func TestPrepareMessagesForLLM_DisabledSkipsReduction(t *testing.T) {
	var disabledCfg config.ContextReductionConfig
	if err := yaml.Unmarshal([]byte("false"), &disabledCfg); err != nil {
		t.Fatalf("unmarshal boolean shorthand: %v", err)
	}
	if !disabledCfg.DisabledValue() {
		t.Fatal("boolean shorthand false should disable reduction")
	}
	a := &MainAgent{projectConfig: &config.Config{Context: config.ContextConfig{Reduction: disabledCfg}}}
	msgs := []message.Message{
		{Role: "user", Content: "u1"},
		{Role: "assistant", ToolCalls: []message.ToolCall{
			{ID: "tc1", Name: "web_fetch", Args: json.RawMessage(`{"url":"https://example.com"}`)},
		}},
		{Role: "tool", ToolCallID: "tc1", Content: strings.Repeat("large web output ", 400)},
		{Role: "user", Content: "u2"},
		{Role: "user", Content: "u3"},
	}

	prepared := a.prepareMessagesForLLM(msgs)
	for i := range msgs {
		if prepared[i].Content != msgs[i].Content {
			t.Fatalf("disabled reduction modified message %d: %q", i, prepared[i].Content)
		}
	}
}

func TestPrepareMessagesForLLM_PrunesOldWebFetchOutput(t *testing.T) {
	a := &MainAgent{}
	msgs := []message.Message{
		{Role: "user", Content: "u1"},
		{Role: "assistant", ToolCalls: []message.ToolCall{
			{ID: "tc1", Name: "web_fetch", Args: json.RawMessage(`{"url":"https://example.com"}`)},
		}},
		{Role: "tool", ToolCallID: "tc1", Content: strings.Repeat("large web output ", 400)},
		{Role: "user", Content: "u2"},
		{Role: "user", Content: "u3"},
	}

	prepared := a.prepareMessagesForLLM(msgs)
	if !strings.Contains(prepared[2].Content, "Older "+tools.NameWebFetch+" output truncated for this request to save context") ||
		!strings.Contains(prepared[2].Content, `url="https://example.com"`) {
		t.Fatalf("expected web_fetch output compaction, got %q", prepared[2].Content)
	}
	if !strings.Contains(prepared[2].Content, "large web output") {
		t.Fatalf("expected compacted web_fetch output to keep a small excerpt, got %q", prepared[2].Content)
	}
}

func TestPrepareMessagesForLLM_PrunesStaleToolOutputWithinSingleUserTurn(t *testing.T) {
	a := &MainAgent{projectConfig: &config.Config{Context: config.ContextConfig{Reduction: config.ContextReductionConfig{}}}}
	var (
		largeShellOutputLines = requesttrim.DefaultPolicy().ShellSuccessBytes
		largeReadOutputCopies = requesttrim.DefaultPolicy().ReadLikeOutputBytes
		fillerRequestBatches  = requesttrim.DefaultPolicy().ShellSuccessAgeTurns
	)
	largeShellOutput := strings.Repeat("test output line\n", largeShellOutputLines)
	largeReadOutput := strings.Repeat("large read output ", largeReadOutputCopies)
	msgs := []message.Message{
		{Role: "user", Content: "u1"},
		{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "tc1", Name: tools.NameShell, Args: json.RawMessage(`{"command":"npm test"}`)}}},
		{Role: "tool", ToolCallID: "tc1", Content: largeShellOutput},
		{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "tc2", Name: tools.NameWebFetch, Args: json.RawMessage(`{"url":"https://example.com/a.go"}`)}}},
		{Role: "tool", ToolCallID: "tc2", Content: largeReadOutput},
	}
	for i := range fillerRequestBatches {
		id := fmt.Sprintf("tc-filler-%d", i)
		msgs = append(msgs,
			message.Message{Role: "assistant", ToolCalls: []message.ToolCall{{ID: id, Name: tools.NameWebFetch, Args: json.RawMessage(`{"url":"https://example.com/b.go"}`)}}},
			message.Message{Role: "tool", ToolCallID: id, Content: "short read output"},
		)
	}

	prepared := a.prepareMessagesForLLM(msgs)
	if !strings.Contains(prepared[2].Content, "[Older "+tools.NameShell+" success summarized for this request to save context; bytes=") ||
		!strings.Contains(prepared[2].Content, "lines=") ||
		!strings.Contains(prepared[2].Content, "- test output line") {
		t.Fatalf("expected early shell output in same user turn to be pruned, got %q", prepared[2].Content)
	}
	if !strings.Contains(prepared[4].Content, "Older "+tools.NameWebFetch+" output truncated for this request to save context; url=\"https://example.com/a.go\"") {
		t.Fatalf("expected early read output in same user turn to be pruned, got %q", prepared[4].Content)
	}
	lastIndex := len(prepared) - 1
	if prepared[lastIndex].Content != "short read output" {
		t.Fatalf("latest tool output should remain intact, got %q", prepared[lastIndex].Content)
	}
}

func TestPrepareMessagesForLLM_PrunesOldSuccessfulBashOutput(t *testing.T) {
	a := &MainAgent{}
	largeOutput := strings.Repeat("test output line\n", 500)
	msgs := []message.Message{
		{Role: "user", Content: "u1"},
		{Role: "assistant", ToolCalls: []message.ToolCall{
			{ID: "tc1", Name: "shell", Args: json.RawMessage(`{"command":"npm test"}`)},
		}},
		{Role: "tool", ToolCallID: "tc1", Content: strings.Repeat("test output line\n", 500)},
		{Role: "user", Content: "u2"},
		{Role: "user", Content: "u3"},
		{Role: "user", Content: "u4"},
	}

	prepared := a.prepareMessagesForLLM(msgs)
	if !strings.Contains(prepared[2].Content, "[Older "+tools.NameShell+" success summarized for this request to save context; bytes=") ||
		!strings.Contains(prepared[2].Content, "lines=") ||
		!strings.Contains(prepared[2].Content, "- test output line") {
		t.Fatalf("expected old successful Shell output to be pruned, got %q", prepared[2].Content)
	}
	stats := a.GetContextReductionStats()
	wantSaved := len(largeOutput) - len(prepared[2].Content)
	if stats.Messages != 1 || stats.Bytes != wantSaved {
		t.Fatalf("reduction stats = %+v, want messages=1 bytes=%d", stats, wantSaved)
	}
}
