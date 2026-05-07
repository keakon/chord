package sessionimport

import (
	"testing"
)

func TestConvertClaudeTranscript_StructuredThinkingAndTools(t *testing.T) {
	data := []byte(`{"uuid":"u1","message":{"role":"user","content":[{"type":"text","text":"hi"}]}}
{"uuid":"a1","parentUuid":"u1","message":{"role":"assistant","content":[{"type":"thinking","thinking":"reason","signature":"sig"},{"type":"text","text":"hello"},{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"pwd"}}]}}
{"uuid":"u2","parentUuid":"a1","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"/tmp"}]}}
`)
	var report ImportReport
	msgs, err := convertClaudeTranscript(data, ToolModeStructured, ReasoningStrict, &report)
	if err != nil {
		t.Fatalf("convertClaudeTranscript: %v", err)
	}
	if len(msgs) != 3 {
		t.Fatalf("msgs len=%d, want 3", len(msgs))
	}
	if msgs[1].Role != "assistant" || len(msgs[1].ThinkingBlocks) != 1 || len(msgs[1].ToolCalls) != 1 {
		t.Fatalf("assistant msg=%+v", msgs[1])
	}
	if msgs[2].Role != "tool" || msgs[2].ToolCallID != "toolu_1" || msgs[2].Content != "/tmp" {
		t.Fatalf("tool msg=%+v", msgs[2])
	}
}

func TestConvertClaudeTranscript_AutoDowngradesUnsignedToolTurn(t *testing.T) {
	data := []byte(`{"uuid":"u1","message":{"role":"user","content":[{"type":"text","text":"hi"}]}}
{"uuid":"a1","parentUuid":"u1","message":{"role":"assistant","content":[{"type":"thinking","thinking":"reason"},{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"pwd"}}]}}
`)
	var report ImportReport
	msgs, err := convertClaudeTranscript(data, ToolModeAuto, ReasoningStrict, &report)
	if err != nil {
		t.Fatalf("convertClaudeTranscript: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("msgs len=%d, want 2", len(msgs))
	}
	if len(msgs[1].ToolCalls) != 0 {
		t.Fatalf("expected tool calls downgraded, got %+v", msgs[1].ToolCalls)
	}
	if msgs[1].Content == "" {
		t.Fatalf("expected visible tool marker content")
	}
	if report.ReasoningBlocksSkipped == 0 {
		t.Fatalf("ReasoningBlocksSkipped=%d, want >0", report.ReasoningBlocksSkipped)
	}
}

func TestConvertClaudeTranscript_SelectsLatestNonSidechainLeaf(t *testing.T) {
	data := []byte(`{"uuid":"u1","message":{"role":"user","content":[{"type":"text","text":"hi"}]}}
{"uuid":"a1","parentUuid":"u1","message":{"role":"assistant","content":[{"type":"text","text":"first"}]}}
{"uuid":"a2","parentUuid":"u1","message":{"role":"assistant","content":[{"type":"text","text":"second"}]}}
`)
	var report ImportReport
	msgs, err := convertClaudeTranscript(data, ToolModeText, ReasoningStrict, &report)
	if err != nil {
		t.Fatalf("convertClaudeTranscript: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("msgs len=%d, want 2", len(msgs))
	}
	if msgs[1].Content != "second" {
		t.Fatalf("assistant content=%q, want second", msgs[1].Content)
	}
	if len(report.Warnings) == 0 {
		t.Fatalf("expected warning for multiple leaves")
	}
}
