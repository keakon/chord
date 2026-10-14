package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/keakon/chord/internal/agent/requesttrim"
	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func TestContextReductionPolicyProjectMappingOverridesGlobalFalse(t *testing.T) {
	var globalReduction config.ContextReductionConfig
	if err := yaml.Unmarshal([]byte("false"), &globalReduction); err != nil {
		t.Fatalf("unmarshal global reduction: %v", err)
	}
	var projectReduction config.ContextReductionConfig
	if err := yaml.Unmarshal([]byte("confirm_age_turns: 9\n"), &projectReduction); err != nil {
		t.Fatalf("unmarshal project reduction: %v", err)
	}
	a := &MainAgent{
		globalConfig:  &config.Config{Context: config.ContextConfig{Reduction: globalReduction}},
		projectConfig: &config.Config{Context: config.ContextConfig{Reduction: projectReduction}},
	}

	policy := a.contextReductionPolicy()
	if policy.Disabled {
		t.Fatal("project reduction mapping should re-enable globally disabled reduction")
	}
	if policy.ConfirmAgeTurns != 9 {
		t.Fatalf("ConfirmAgeTurns = %d, want 9", policy.ConfirmAgeTurns)
	}
}

// TestContextReductionPolicyProjectTrueOverGlobalFalseKeepsCompiledDefaults
// pins the zero-tuning fallback the boolean shorthand relies on: a global
// `reduction: false` marshals as a scalar, so a project `reduction: true`
// re-parses without the base mapping and leaves every tuning field at zero.
// The policy layer must then fall back to the compiled defaults instead of
// running with zero thresholds.
func TestContextReductionPolicyProjectTrueOverGlobalFalseKeepsCompiledDefaults(t *testing.T) {
	var globalReduction config.ContextReductionConfig
	if err := yaml.Unmarshal([]byte("false"), &globalReduction); err != nil {
		t.Fatalf("unmarshal global reduction: %v", err)
	}
	// Decoded into a zero-valued receiver, mirroring the defaults-free re-parse
	// of the merged project config: mode enabled, all tuning fields zero.
	var projectReduction config.ContextReductionConfig
	if err := yaml.Unmarshal([]byte("true"), &projectReduction); err != nil {
		t.Fatalf("unmarshal project reduction: %v", err)
	}
	a := &MainAgent{
		globalConfig:  &config.Config{Context: config.ContextConfig{Reduction: globalReduction}},
		projectConfig: &config.Config{Context: config.ContextConfig{Reduction: projectReduction}},
	}

	policy := a.contextReductionPolicy()
	want := requesttrim.DefaultPolicy()
	if policy != want {
		t.Fatalf("policy = %+v, want compiled defaults %+v", policy, want)
	}
}

func TestPrepareMessagesForLLM_ProtectsRecentHighRiskOutputWithinSingleUserTurn(t *testing.T) {
	a := &MainAgent{}
	largeFailureOutput := "panic: boom\nTraceback (most recent call last):\n  File \"test.py\", line 12, in test_case\nexpected: 1\nactual: 2\n" + strings.Repeat("failure context line\n", 500)
	msgs := []message.Message{
		{Role: "user", Content: "u1"},
		{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "tc1", Name: tools.NameShell, Args: json.RawMessage(`{"command":"pytest"}`)}}},
		{Role: "tool", ToolCallID: "tc1", Content: largeFailureOutput},
	}
	for i := range requesttrim.DefaultPolicy().ShellSuccessAgeTurns {
		id := fmt.Sprintf("tc-filler-%d", i)
		msgs = append(msgs,
			message.Message{Role: "assistant", ToolCalls: []message.ToolCall{{ID: id, Name: tools.NameRead, Args: json.RawMessage(`{"path":"b.go"}`)}}},
			message.Message{Role: "tool", ToolCallID: id, Content: "short read output"},
		)
	}

	prepared := a.prepareMessagesForLLM(msgs)
	if prepared[2].Content != largeFailureOutput {
		t.Fatalf("recent high-risk output should remain intact, got %q", prepared[2].Content)
	}
	if stats := a.GetContextReductionStats(); stats.Messages != 0 || stats.Bytes != 0 {
		t.Fatalf("expected no reduction for protected high-risk output, stats=%+v", stats)
	}
}

func TestPrepareMessagesForLLM_ConfiguresHighRiskProtectAgeTurns(t *testing.T) {
	a := &MainAgent{globalConfig: &config.Config{Context: config.ContextConfig{Reduction: config.ContextReductionConfig{HighRiskProtectAgeTurns: 1}}}}
	largeFailureOutput := "panic: boom\nTraceback (most recent call last):\n  File \"test.py\", line 12, in test_case\nexpected: 1\nactual: 2\n" + strings.Repeat("failure context line\n", 500)
	currentFailureOutput := "panic: still active\nTraceback (most recent call last):\n" + strings.Repeat("current failure context line\n", 500)
	msgs := []message.Message{
		{Role: "user", Content: "u1"},
		{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "tc1", Name: tools.NameShell, Args: json.RawMessage(`{"command":"pytest"}`)}}},
		{Role: "tool", ToolCallID: "tc1", Content: largeFailureOutput},
		{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "tc-mid", Name: tools.NameRead, Args: json.RawMessage(`{"path":"b.go"}`)}}},
		{Role: "tool", ToolCallID: "tc-mid", Content: "short read output"},
		{Role: "user", Content: "u2"},
		{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "tc2", Name: tools.NameShell, Args: json.RawMessage(`{"command":"go test ./..."}`)}}},
		{Role: "tool", ToolCallID: "tc2", Content: currentFailureOutput},
	}
	prepared := a.prepareMessagesForLLM(msgs)
	if prepared[2].Content == largeFailureOutput {
		t.Fatalf("previous-turn high-risk output should be reducible with high_risk_protect_age_turns=1")
	}
	if !strings.Contains(prepared[2].Content, "Older shell log summarized") {
		t.Fatalf("previous-turn high-risk output should use conservative log summary, got %q", prepared[2].Content)
	}
	if prepared[7].Content != currentFailureOutput {
		t.Fatalf("current-turn high-risk output should remain intact, got %q", prepared[7].Content)
	}
}

func TestPrepareMessagesForLLM_PrunesRecentLowRiskOutputWithinSingleUserTurn(t *testing.T) {
	a := &MainAgent{}
	largeOutput := strings.Repeat("install progress line\n", requesttrim.DefaultPolicy().ShellSuccessBytes)
	msgs := []message.Message{
		{Role: "user", Content: "u1"},
		{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "tc1", Name: tools.NameShell, Args: json.RawMessage(`{"command":"npm install"}`)}}},
		{Role: "tool", ToolCallID: "tc1", Content: largeOutput},
	}
	for i := range requesttrim.DefaultPolicy().ShellSuccessAgeTurns {
		id := fmt.Sprintf("tc-filler-%d", i)
		msgs = append(msgs,
			message.Message{Role: "assistant", ToolCalls: []message.ToolCall{{ID: id, Name: tools.NameRead, Args: json.RawMessage(`{"path":"b.go"}`)}}},
			message.Message{Role: "tool", ToolCallID: id, Content: "short read output"},
		)
	}

	prepared := a.prepareMessagesForLLM(msgs)
	if !strings.Contains(prepared[2].Content, "[Older "+tools.NameShell+" success summarized for this request to save context; bytes=") ||
		!strings.Contains(prepared[2].Content, "lines=") ||
		!strings.Contains(prepared[2].Content, "- install progress line") {
		t.Fatalf("expected recent low-risk shell output to use existing effective-age pruning, got %q", prepared[2].Content)
	}
}

func TestPrepareMessagesForLLM_ProtectsRecentDiffOutputWithinSingleUserTurn(t *testing.T) {
	a := &MainAgent{}
	// A unified diff with no failure markers: protected only via diff detection.
	diffOutput := "diff --git a/calc.go b/calc.go\n--- a/calc.go\n+++ b/calc.go\n@@ -1,4 +1,4 @@\n-func add(a, b int) int { return a - b }\n+func add(a, b int) int { return a + b }\n" + strings.Repeat(" // stable context line for the diff body\n", 500)
	msgs := []message.Message{
		{Role: "user", Content: "u1"},
		{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "tc1", Name: tools.NameShell, Args: json.RawMessage(`{"command":"git diff"}`)}}},
		{Role: "tool", ToolCallID: "tc1", Content: diffOutput},
	}
	for i := range requesttrim.DefaultPolicy().ShellSuccessAgeTurns {
		id := fmt.Sprintf("tc-filler-%d", i)
		msgs = append(msgs,
			message.Message{Role: "assistant", ToolCalls: []message.ToolCall{{ID: id, Name: tools.NameRead, Args: json.RawMessage(`{"path":"b.go"}`)}}},
			message.Message{Role: "tool", ToolCallID: id, Content: "short read output"},
		)
	}

	prepared := a.prepareMessagesForLLM(msgs)
	if prepared[2].Content != diffOutput {
		t.Fatalf("recent diff output should remain intact, got %q", prepared[2].Content)
	}
	if stats := a.GetContextReductionStats(); stats.Messages != 0 || stats.Bytes != 0 {
		t.Fatalf("expected no reduction for protected diff output, stats=%+v", stats)
	}
}

func TestPrepareMessagesForLLM_ProtectsRecentShellFailureOutputWithinSingleUserTurn(t *testing.T) {
	a := &MainAgent{}
	// High-risk only via the shell + "failed" heuristic: no diff, no marker words.
	failureOutput := "running build pipeline\nstep 3 of 7 failed\n" + strings.Repeat("build log line without markers\n", 500)
	msgs := []message.Message{
		{Role: "user", Content: "u1"},
		{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "tc1", Name: tools.NameShell, Args: json.RawMessage(`{"command":"make build"}`)}}},
		{Role: "tool", ToolCallID: "tc1", Content: failureOutput},
	}
	for i := range requesttrim.DefaultPolicy().ShellSuccessAgeTurns {
		id := fmt.Sprintf("tc-filler-%d", i)
		msgs = append(msgs,
			message.Message{Role: "assistant", ToolCalls: []message.ToolCall{{ID: id, Name: tools.NameRead, Args: json.RawMessage(`{"path":"b.go"}`)}}},
			message.Message{Role: "tool", ToolCallID: id, Content: "short read output"},
		)
	}

	prepared := a.prepareMessagesForLLM(msgs)
	if prepared[2].Content != failureOutput {
		t.Fatalf("recent shell failure output should remain intact, got %q", prepared[2].Content)
	}
	if stats := a.GetContextReductionStats(); stats.Messages != 0 || stats.Bytes != 0 {
		t.Fatalf("expected no reduction for protected shell failure output, stats=%+v", stats)
	}
}

func TestPrepareMessagesForLLM_FreshSpecializedOutputIsNotReduced(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.projectConfig = &config.Config{
		Context: config.ContextConfig{Reduction: config.ContextReductionConfig{
			MinToolResultsPrune:  1,
			ReadLikeAgeTurns:     1,
			ReadLikeOutputBytes:  40,
			StaleAgeTurns:        1,
			StaleOutputBytes:     40,
			ShellSuccessAgeTurns: 1,
			ShellSuccessBytes:    40,
			MinIncrementalTokens: 1,
		}},
	}
	content := strings.Join([]string{
		"internal/agent/compaction_policy.go:12:func prepareMessagesForLLM(...)",
		"internal/agent/compaction_policy.go:88:func prepareMessagesForLLMWithOptions(...)",
		"internal/agent/compaction_test.go:50:func TestPrepareMessagesForLLM(...)",
	}, "\n")
	msgs := []message.Message{
		{Role: "user", Content: "u1"},
		{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "tc1", Name: tools.NameGrep, Args: json.RawMessage(`{"paths":["internal/agent"],"includes":["*.go"],"pattern":"prepareMessagesForLLM"}`)}}},
		{Role: "tool", ToolCallID: "tc1", Content: content},
	}

	prepared := a.prepareMessagesForLLM(msgs)
	if prepared[2].Content != content {
		t.Fatalf("fresh specialized output should remain unreduced, got %q", prepared[2].Content)
	}
	stats := a.GetContextReductionStats()
	if stats.Messages != 0 {
		t.Fatalf("fresh specialized output should not count as reduced, stats=%+v", stats)
	}
}

func TestPrepareMessagesForLLM_UsesReductionThresholdConfig(t *testing.T) {
	a := &MainAgent{projectConfig: &config.Config{Context: config.ContextConfig{Reduction: config.ContextReductionConfig{ShellSuccessAgeTurns: 5}}}}
	largeOutput := strings.Repeat("test output line\n", 500)
	msgs := []message.Message{
		{Role: "user", Content: "u1"},
		{Role: "assistant", ToolCalls: []message.ToolCall{
			{ID: "tc1", Name: "shell", Args: json.RawMessage(`{"command":"npm test"}`)},
		}},
		{Role: "tool", ToolCallID: "tc1", Content: largeOutput},
		{Role: "user", Content: "u2"},
		{Role: "user", Content: "u3"},
		{Role: "user", Content: "u4"},
	}

	prepared := a.prepareMessagesForLLM(msgs)
	if prepared[2].Content != largeOutput {
		t.Fatalf("configured shell age should delay pruning, got %q", prepared[2].Content)
	}
}
