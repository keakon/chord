package agent

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func TestPrepareMessagesForLLM_StableReusePreservesToolResultBoundaries(t *testing.T) {
	for _, boundary := range []struct {
		name      string
		prefixLen int
	}{
		{"before_tool_calls", 4},
		{"after_tool_calls", 5},
		{"between_tool_results", 6},
		{"after_tool_results", 7},
	} {
		t.Run(boundary.name, func(t *testing.T) {
			for _, shapeSource := range []bool{true, false} {
				name := "shape_hashes"
				if shapeSource {
					name = "shape_source"
				}
				t.Run(name, func(t *testing.T) {
					a := &MainAgent{parentCtx: context.Background(), projectConfig: &config.Config{Context: config.ContextConfig{Reduction: config.ContextReductionConfig{
						ReadLikeAgeTurns:     1,
						ReadLikeOutputBytes:  1,
						StaleAgeTurns:        99,
						StaleOutputBytes:     1,
						MinToolResultsPrune:  1,
						MinIncrementalTokens: 4096,
						ShellSuccessAgeTurns: 99,
						ShellSuccessBytes:    1,
					}}}}
					a.newTurn()
					a.providerModelRef = "sample/test-model"
					a.lastLLMRequestModelRef = a.providerModelRef
					a.llmModelRunLength = 1
					messages := []message.Message{
						{Role: message.RoleUser, Content: "Read the references"},
						{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "earlier", Name: tools.NameWebFetch, Args: json.RawMessage(`{"url":"https://example.invalid/earlier"}`)}}},
						{Role: message.RoleTool, ToolCallID: "earlier", Content: strings.Repeat("sample reference output ", 100)},
						{Role: message.RoleUser, Content: "Continue with the remaining references"},
						{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{
							{ID: "first", Name: tools.NameWebFetch, Args: json.RawMessage(`{"url":"https://example.invalid/first"}`)},
							{ID: "second", Name: tools.NameWebFetch, Args: json.RawMessage(`{"url":"https://example.invalid/second"}`)},
						}},
						{Role: message.RoleTool, ToolCallID: "first", Content: "First reference"},
						{Role: message.RoleTool, ToolCallID: "second", Content: "Second reference"},
						{Role: message.RoleAssistant, Content: "References collected"},
					}
					original := cloneMessageSliceForRequestShape(messages)
					prefix := messages[:boundary.prefixLen]
					first := a.prepareMessagesForLLM(prefix)
					if len(first) != len(prefix) || first[2].Content == prefix[2].Content || !hasReductionSavings(a.GetContextReductionStats()) {
						t.Fatal("fixture did not establish a reduced prefix")
					}
					// Shape compatibility uses the original history. The stored reduced
					// surface must therefore preserve positions and tool identities.
					for i := range prefix {
						if first[i].Role != prefix[i].Role || first[i].ToolCallID != prefix[i].ToolCallID || !reflect.DeepEqual(first[i].ToolCalls, prefix[i].ToolCalls) {
							t.Fatalf("reduction changed the tool identity at index %d", i)
						}
					}
					if !shapeSource {
						a.lastPreparedLLMShapeSource = nil
					}
					prepared := a.prepareMessagesForLLM(messages)
					if stats := a.GetContextReductionStats(); !stats.ReusedStable {
						t.Fatalf("expected successful stable reuse, got %+v", stats)
					}
					if len(prepared) != len(messages) || !reflect.DeepEqual(prepared[:len(prefix)], first) || !reflect.DeepEqual(prepared[len(prefix):], messages[len(prefix):]) {
						t.Fatal("reuse changed the stable prefix or fresh tail")
					}
					if repaired, dropped := message.RepairOrphanToolResults(prepared); dropped != 0 || !reflect.DeepEqual(repaired, prepared) {
						t.Fatalf("reuse required tool-result repair: dropped=%d", dropped)
					}
					if !reflect.DeepEqual(messages, original) {
						t.Fatal("request preparation modified the original history")
					}
				})
			}
		})
	}
}
