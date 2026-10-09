package modelcompat

import (
	"encoding/json"
	"testing"

	"github.com/keakon/chord/internal/message"
)

func TestNormalizePreservesNativeReplayTrajectory(t *testing.T) {
	for _, structured := range []bool{false, true} {
		for _, level := range []int{ReplayCompatNative, ReplayCompatSynthesized, ReplayCompatStrict} {
			history := []message.Message{
				{Role: message.RoleUser, Content: "Read sample records"},
				{Role: message.RoleAssistant, Content: "Reading", ReasoningContent: "Plan", ThinkingBlocks: []message.ThinkingBlock{{Thinking: "Plan", Signature: "sample-signature"}}, ToolCalls: []message.ToolCall{{ID: "call-1", Name: "sample_lookup", Args: json.RawMessage(`{}`)}}, NativeTools: &message.NativeToolHistory{Target: "sample/test-model", Authorization: message.NativeToolAuthorization{Tool: "web_search", Contract: "sample", Constraints: json.RawMessage(`{"max_uses":1}`)}, RequestIDs: []string{"request-1"}, Calls: []message.HostedCall{{ID: "search-1", Result: json.RawMessage(`[]`)}}, Items: []json.RawMessage{json.RawMessage(`{"type":"tool_use","id":"call-1","name":"sample_lookup","input":{}}`)}}},
				{Role: message.RoleTool, ToolCallID: "call-1", Content: "Sample result"},
				{Role: message.RoleUser, Content: "Continue"},
			}
			before, _ := json.Marshal(history)
			target := TargetModel{ProviderID: "sample", ModelID: "test-model", WireFamily: WireFamilyAnthropic, ReasoningContinuityMode: ReasoningContinuityAnthropicBlocks, ReasoningReplay: ReasoningReplayNone, SupportsStructuredTools: true, ToolResultEncoding: ToolResultEncodingAnthropicUserBlock}
			projected, report := NormalizeForTarget(history, target, NormalizeOptions{StructuredTools: structured, ReplayCompat: level})
			after, _ := json.Marshal(projected)
			if string(before) != string(after) {
				t.Fatalf("native replay changed at level=%v structured=%v: %s", level, structured, after)
			}
			if report.DowngradedToolCalls != 0 || report.DroppedThinkingBlocks != 0 {
				t.Fatalf("native history reported generic degradation: %+v", report)
			}
			projected[1].NativeTools.Items[0][0] = ' '
			current, _ := json.Marshal(history)
			if string(before) != string(current) {
				t.Fatal("native request projection aliases canonical history")
			}
		}
	}
}
