package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/agent/requesttrim"
	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tooloutput"
	"github.com/keakon/chord/internal/tools"
)

func TestPrepareMessagesForLLM_ShellSuccessSummaryPrefersSalientLines(t *testing.T) {
	a := &MainAgent{}
	largeOutput := strings.Repeat("updated dependency cache\n", requesttrim.DefaultPolicy().ShellSuccessBytes) + "\nok github.com/example/project 1.23s\ncoverage: 82.1%\n" + strings.Repeat("tail noise line\n", 20)
	msgs := []message.Message{
		{Role: "user", Content: "u1"},
		{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "tc1", Name: tools.NameShell, Args: json.RawMessage(`{"command":"go test ./..."}`)}}},
		{Role: "tool", ToolCallID: "tc1", Content: largeOutput},
		{Role: "user", Content: "u2"},
		{Role: "user", Content: "u3"},
	}

	prepared := a.prepareMessagesForLLM(msgs)
	got := prepared[2].Content
	if !isShellSuccessSummary(got) {
		t.Fatalf("expected shell success summary, got %q", got)
	}
	if !strings.Contains(got, "go test success summarized") ||
		!strings.Contains(got, "packages_ok=1") ||
		!strings.Contains(got, "coverage_reports=1") ||
		!strings.Contains(got, "ok github.com/example/project 1.23s") ||
		!strings.Contains(got, "coverage: 82.1%") {
		t.Fatalf("expected salient success lines, got %q", got)
	}
	if strings.Contains(got, "updated dependency cache") {
		t.Fatalf("expected strong result lines to take priority over early weak progress lines, got %q", got)
	}
	if strings.Contains(got, "tail noise line") {
		t.Fatalf("expected command-aware summary to omit unrelated tail noise, got %q", got)
	}
}

func TestPrepareMessagesForLLM_ShellSuccessPreservesArtifactReference(t *testing.T) {
	a := &MainAgent{}
	path := "/session/tool-outputs/call_" + strings.Repeat("a", requesttrim.SummaryLineSnippetChars) + ".log"
	ref := tooloutput.ArtifactReferencePrefix + path + ". " + tooloutput.ArtifactReadGuidance
	largeOutput := strings.Repeat("completed work item\n", requesttrim.DefaultPolicy().ShellSuccessBytes) + ref
	msgs := []message.Message{
		{Role: message.RoleUser, Content: "request"},
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "tc1", Name: tools.NameShell, Args: json.RawMessage(`{"command":"sample-command"}`)}}},
		{Role: message.RoleTool, ToolCallID: "tc1", Content: largeOutput},
		{Role: message.RoleUser, Content: "continue"},
		{Role: message.RoleUser, Content: "continue again"},
	}

	got := a.prepareMessagesForLLM(msgs)[2].Content
	if !isShellSuccessSummary(got) {
		t.Fatalf("expected shell success summary, got %q", got)
	}
	if !strings.Contains(got, ref) {
		t.Fatalf("shell success summary lost artifact reference %q: %q", ref, got)
	}
}

func TestPrepareMessagesForLLM_GoTestSummaryCountsPackagesAndCachedResults(t *testing.T) {
	a := &MainAgent{}
	largeOutput := strings.Repeat("compile progress line\n", requesttrim.DefaultPolicy().ShellSuccessBytes) +
		"?   \tgithub.com/example/project/cmd\t[no test files]\n" +
		"ok  \tgithub.com/example/project/internal/a\t(cached)\n" +
		"ok  \tgithub.com/example/project/internal/b\t1.23s\tcoverage: 84.2% of statements\n"
	msgs := []message.Message{
		{Role: "user", Content: "u1"},
		{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "tc1", Name: tools.NameShell, Args: json.RawMessage(`{"command":"go test ./..."}`)}}},
		{Role: "tool", ToolCallID: "tc1", Content: largeOutput},
		{Role: "user", Content: "u2"},
		{Role: "user", Content: "u3"},
	}

	prepared := a.prepareMessagesForLLM(msgs)
	got := prepared[2].Content
	for _, want := range []string{
		"go test success summarized",
		"packages_ok=2",
		"cached=1",
		"no_test_files=1",
		"coverage_reports=1",
		"github.com/example/project/internal/a",
		"github.com/example/project/internal/b",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected %q in command-aware go test summary, got %q", want, got)
		}
	}
	if strings.Contains(got, "compile progress line") {
		t.Fatalf("expected go test summary to omit progress noise, got %q", got)
	}
}

func TestPrepareMessagesForLLM_GoTestJSONKeepsExcerptFallback(t *testing.T) {
	a := &MainAgent{}
	largeOutput := strings.Repeat(`{"Action":"pass","Package":"github.com/example/project"}`+"\n", requesttrim.DefaultPolicy().ShellSuccessBytes)
	msgs := []message.Message{
		{Role: "user", Content: "u1"},
		{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "tc1", Name: tools.NameShell, Args: json.RawMessage(`{"command":"go test -json ./..."}`)}}},
		{Role: "tool", ToolCallID: "tc1", Content: largeOutput},
		{Role: "user", Content: "u2"},
		{Role: "user", Content: "u3"},
	}

	got := a.prepareMessagesForLLM(msgs)[2].Content
	if strings.Contains(got, "go test success summarized") {
		t.Fatalf("structured go test output must not use line-oriented command summary, got %q", got)
	}
	// An NDJSON stream folds into no JSON skeleton, so the JSON branch falls
	// back to the excerpt-bearing stale summary. A bare "omitted" marker would
	// leave the request citing nothing for the run it summarized; the
	// fallback keeps representative lines instead.
	if !strings.Contains(got, "Older shell output summarized") || !strings.Contains(got, `{"Action":"pass"`) {
		t.Fatalf("expected excerpt-bearing fallback for go test -json output, got %q", got)
	}
}

func TestPrepareMessagesForLLM_CompoundGoTestUsesGenericShellSummary(t *testing.T) {
	a := &MainAgent{}
	largeOutput := strings.Repeat("ok github.com/example/project 1.23s\n", requesttrim.DefaultPolicy().ShellSuccessBytes)
	msgs := []message.Message{
		{Role: "user", Content: "u1"},
		{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "tc1", Name: tools.NameShell, Args: json.RawMessage(`{"command":"go test ./... && echo done"}`)}}},
		{Role: "tool", ToolCallID: "tc1", Content: largeOutput},
		{Role: "user", Content: "u2"},
		{Role: "user", Content: "u3"},
	}

	got := a.prepareMessagesForLLM(msgs)[2].Content
	if !strings.Contains(got, "[Older "+tools.NameShell+" success summarized") {
		t.Fatalf("expected generic shell summary for compound command, got %q", got)
	}
	if strings.Contains(got, "go test success summarized") {
		t.Fatalf("compound command must not use direct go test summary, got %q", got)
	}
}

func TestPrepareMessagesForLLM_CompactsOlderDiagnosticsBlocks(t *testing.T) {
	a := &MainAgent{}
	content := "Replaced 1 occurrence\n\nDiagnostics:\nUsed Ruff quick diagnostics because this Python file exceeds the configured threshold.\n[E] 10:1 [F821] Undefined name `x`\n[E] 11:1 another diagnostic"
	msgs := []message.Message{
		{Role: "user", Content: "u1"},
		{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "tc1", Name: tools.NameApplyPatch, Args: json.RawMessage(`{"patch":"*** Begin Patch\n*** Update File: a.py\n@@\n-x\n+y\n*** End Patch"}`)}}},
		{Role: "tool", ToolCallID: "tc1", Content: content},
		{Role: "user", Content: "u2"},
		{Role: "user", Content: "u3"},
		{Role: "user", Content: "u4"},
	}

	prepared := a.prepareMessagesForLLM(msgs)
	if !strings.Contains(prepared[2].Content, "Diagnostics summary:") {
		t.Fatalf("expected diagnostics summary, got %q", prepared[2].Content)
	}
	if !strings.Contains(prepared[2].Content, "[E] 10:1 [F821] Undefined name `x`") || !strings.Contains(prepared[2].Content, "another diagnostic") {
		t.Fatalf("expected every diagnostic location preserved, got %q", prepared[2].Content)
	}
	if strings.Contains(prepared[2].Content, "Used Ruff quick diagnostics") {
		t.Fatalf("expected explanatory diagnostics prose dropped, got %q", prepared[2].Content)
	}
}

func TestPrepareMessagesForLLM_DiagnosticsCompactionPreservesToolDiff(t *testing.T) {
	a := &MainAgent{}
	content := "Replaced 1 occurrence\n\nDiagnostics:\n[E] 10:1 [F821] Undefined name `x`\n[E] 11:1 another diagnostic"
	msgs := []message.Message{
		{Role: "user", Content: "u1"},
		{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "tc1", Name: tools.NameApplyPatch, Args: json.RawMessage(`{"patch":"*** Begin Patch\n*** Update File: a.py\n@@\n-x\n+y\n*** End Patch"}`)}}},
		{Role: "tool", ToolCallID: "tc1", Content: content, ToolDiff: "--- a/a.py\n+++ b/a.py\n@@ -1 +1 @@\n-x\n+y\n"},
		{Role: "user", Content: "u2"},
		{Role: "user", Content: "u3"},
		{Role: "user", Content: "u4"},
	}

	prepared := a.prepareMessagesForLLM(msgs)
	if !strings.Contains(prepared[2].Content, "Diagnostics summary:") {
		t.Fatalf("expected diagnostics compaction, got %q", prepared[2].Content)
	}
	if prepared[2].ToolDiff == "" {
		t.Fatal("diagnostics compaction should preserve ToolDiff")
	}
}

func TestPrepareMessagesForLLM_CompactsOlderDiagnosticsBlocksPrefersActionableLine(t *testing.T) {
	a := &MainAgent{}
	content := "Replaced 1 occurrence\n\nDiagnostics:\nDiagnostics status: backend=LSP, new=1, resolved=0, current=1 errors, 0 warnings (best effort).\n[E] 10:1 [F821] Undefined name `x`"
	msgs := []message.Message{
		{Role: "user", Content: "u1"},
		{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "tc1", Name: tools.NameApplyPatch, Args: json.RawMessage(`{"patch":"*** Begin Patch\n*** Update File: a.py\n@@\n-x\n+y\n*** End Patch"}`)}}},
		{Role: "tool", ToolCallID: "tc1", Content: content},
		{Role: "user", Content: "u2"},
		{Role: "user", Content: "u3"},
		{Role: "user", Content: "u4"},
	}

	prepared := a.prepareMessagesForLLM(msgs)
	if !strings.Contains(prepared[2].Content, "[E] 10:1 [F821] Undefined name `x`") {
		t.Fatalf("expected actionable diagnostic line kept, got %q", prepared[2].Content)
	}
	if strings.Contains(prepared[2].Content, "Diagnostics status:") {
		t.Fatalf("expected status line dropped, got %q", prepared[2].Content)
	}
}

// A newer diagnostics-bearing result supersedes the older block: the older one
// collapses to a single representative line with an explicit note pointing at
// the fresher output (mirroring the read superseded semantics), while the
// newest block keeps its full location list.
func TestPrepareMessagesForLLM_NewerDiagnosticsSupersedeOlderBlock(t *testing.T) {
	a := &MainAgent{}
	older := "Replaced 1 occurrence\n\nDiagnostics:\n[E] 10:1 [F821] Undefined name `x`\n[E] 11:1 another diagnostic"
	newer := "Replaced 1 occurrence\n\nDiagnostics:\n[E] 20:1 [F822] undefined name `y`\n[E] 21:1 warning z"
	msgs := []message.Message{
		{Role: "user", Content: "u1"},
		{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "tc1", Name: tools.NameApplyPatch, Args: json.RawMessage(`{"patch":"*** Begin Patch\n*** Update File: a.py\n@@\n-x\n+y\n*** End Patch"}`)}}},
		{Role: "tool", ToolCallID: "tc1", Content: older},
		{Role: "user", Content: "u2"},
		{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "tc2", Name: tools.NameApplyPatch, Args: json.RawMessage(`{"patch":"*** Begin Patch\n*** Update File: a.py\n@@\n-y\n+z\n*** End Patch"}`)}}},
		{Role: "tool", ToolCallID: "tc2", Content: newer},
		{Role: "user", Content: "u3"},
		{Role: "user", Content: "u4"},
	}

	prepared := a.prepareMessagesForLLM(msgs)
	if !strings.Contains(prepared[2].Content, "a newer diagnostics output appears later") {
		t.Fatalf("older diagnostics block should note the newer output, got %q", prepared[2].Content)
	}
	if strings.Contains(prepared[2].Content, "another diagnostic") {
		t.Fatalf("superseded diagnostics block should collapse, got %q", prepared[2].Content)
	}
	if !strings.Contains(prepared[5].Content, "[E] 20:1 [F822] undefined name `y`") || !strings.Contains(prepared[5].Content, "warning z") {
		t.Fatalf("newest diagnostics block must keep its full location list, got %q", prepared[5].Content)
	}
}

func TestPrepareMessagesForLLM_SearchReducerBeatsGenericStaleFallback(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.projectConfig = &config.Config{
		Context: config.ContextConfig{Reduction: config.ContextReductionConfig{
			MinToolResultsPrune:  1,
			ReadLikeAgeTurns:     1,
			ReadLikeOutputBytes:  80,
			StaleAgeTurns:        1,
			StaleOutputBytes:     40,
			ShellSuccessAgeTurns: 9,
			ShellSuccessBytes:    1 << 20,
			MinIncrementalTokens: 1,
		}},
	}
	content := strings.Join([]string{
		"internal/agent/compaction_policy.go:12:func prepareMessagesForLLM(...)",
		"internal/agent/compaction_policy.go:88:func prepareMessagesForLLMWithOptions(...)",
		"internal/agent/compaction_policy.go:150:func reduceReadLikeOutputSummary(...)",
		"internal/agent/compaction_test.go:50:func TestPrepareMessagesForLLM(...)",
		"internal/agent/main_llm.go:125:candidateTools := llmToolDefinitionsFromVisibleTools(...)",
	}, "\n") + "\n" + strings.Repeat("internal/agent/compaction_policy.go:999:func extraMatch()\n", 40)
	msgs := []message.Message{
		{Role: "user", Content: "u1"},
		{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "tc1", Name: tools.NameGrep, Args: json.RawMessage(`{"paths":["internal/agent"],"includes":["*.go"],"pattern":"prepareMessagesForLLM"}`)}}},
		{Role: "tool", ToolCallID: "tc1", Content: content},
		{Role: "user", Content: "u2"},
		{Role: "assistant", Content: "ack"},
		{Role: "user", Content: "u3"},
	}

	prepared := a.prepareMessagesForLLM(msgs)
	if !strings.Contains(prepared[2].Content, "Older "+tools.NameGrep+" results summarized") {
		t.Fatalf("expected search summary, got %q", prepared[2].Content)
	}
	if strings.Contains(prepared[2].Content, "refer to exported history") || strings.Contains(strings.ToLower(prepared[2].Content), "re-run") {
		t.Fatalf("expected search summary without history/re-run hint, got %q", prepared[2].Content)
	}
	if !strings.Contains(prepared[2].Content, `pattern="prepareMessagesForLLM"`) {
		t.Fatalf("expected grep pattern in summary, got %q", prepared[2].Content)
	}
	if !strings.Contains(prepared[2].Content, "internal/agent/compaction_policy.go:") || !strings.Contains(prepared[2].Content, "internal/agent/compaction_test.go:") {
		t.Fatalf("expected search summary grouped across files, got %q", prepared[2].Content)
	}
	stats := a.GetContextReductionStats()
	if stats.Messages == 0 || stats.Bytes == 0 {
		t.Fatalf("expected search summary to save bytes, stats=%+v", stats)
	}
}

func TestPrepareMessagesForLLM_SearchSummaryPreservesArtifactReference(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.projectConfig = &config.Config{
		Context: config.ContextConfig{Reduction: config.ContextReductionConfig{
			MinToolResultsPrune:  1,
			ReadLikeAgeTurns:     1,
			ReadLikeOutputBytes:  80,
			StaleAgeTurns:        1,
			StaleOutputBytes:     40,
			ShellSuccessAgeTurns: 9,
			ShellSuccessBytes:    1 << 20,
			MinIncrementalTokens: 1,
		}},
	}
	path := "/session/tool-outputs/call_" + strings.Repeat("b", requesttrim.SummaryLineSnippetChars) + ".log"
	ref := tooloutput.ArtifactReferencePrefix + path + ". " + tooloutput.ArtifactReadGuidance
	content := strings.Repeat("internal/agent/context_reduction.go:700:func reduceRequestToolOutput(...)\n", 40) + ref
	msgs := []message.Message{
		{Role: message.RoleUser, Content: "request"},
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "tc1", Name: tools.NameGrep, Args: json.RawMessage(`{"paths":["internal/agent"],"pattern":"reduceRequestToolOutput"}`)}}},
		{Role: message.RoleTool, ToolCallID: "tc1", Content: content},
		{Role: message.RoleUser, Content: "continue"},
		{Role: message.RoleAssistant, Content: "ack"},
		{Role: message.RoleUser, Content: "continue again"},
	}

	got := a.prepareMessagesForLLM(msgs)[2].Content
	if !strings.Contains(got, "Older "+tools.NameGrep+" results summarized") {
		t.Fatalf("expected search summary, got %q", got)
	}
	if !strings.Contains(got, ref) {
		t.Fatalf("search summary lost artifact reference %q: %q", ref, got)
	}
}

func TestPrepareMessagesForLLM_ShellSearchOutputUsesSearchSummary(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.projectConfig = &config.Config{
		Context: config.ContextConfig{Reduction: config.ContextReductionConfig{
			MinToolResultsPrune:  1,
			ReadLikeAgeTurns:     9,
			ReadLikeOutputBytes:  1 << 20,
			StaleAgeTurns:        9,
			StaleOutputBytes:     1 << 20,
			ShellSuccessAgeTurns: 1,
			ShellSuccessBytes:    80,
			MinIncrementalTokens: 1,
		}},
	}
	content := strings.Join([]string{
		"a.go:10:func alpha() {}",
		"b.go:20:func beta() {}",
		"c.go:30:func gamma() {}",
	}, "\n") + "\n" + strings.Repeat("b.go:99:func repeated() {}\n", 40)
	msgs := []message.Message{
		{Role: "user", Content: "u1"},
		{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "tc1", Name: tools.NameShell, Args: json.RawMessage(`{"command":"rg 'func '"}`)}}},
		{Role: "tool", ToolCallID: "tc1", Content: content},
		{Role: "user", Content: "u2"},
		{Role: "assistant", Content: "ack"},
		{Role: "user", Content: "u3"},
	}

	prepared := a.prepareMessagesForLLM(msgs)
	if !strings.Contains(prepared[2].Content, "Older shell results summarized") {
		t.Fatalf("expected shell grep-like output to use search summary, got %q", prepared[2].Content)
	}
	if !strings.Contains(prepared[2].Content, "a.go:") || !strings.Contains(prepared[2].Content, "b.go:") {
		t.Fatalf("expected grouped search summary, got %q", prepared[2].Content)
	}
	stats := a.GetContextReductionStats()
	if stats.ByToolAndRule[tools.NameShell+"/search_result"].Messages != 1 {
		t.Fatalf("expected shell/search_result stats, got %+v", stats.ByToolAndRule)
	}
}

func TestPrepareMessagesForLLM_ShellSearchOutputParsesWindowsAbsolutePaths(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.projectConfig = &config.Config{
		Context: config.ContextConfig{Reduction: config.ContextReductionConfig{
			MinToolResultsPrune:  1,
			ReadLikeAgeTurns:     9,
			ReadLikeOutputBytes:  1 << 20,
			StaleAgeTurns:        9,
			StaleOutputBytes:     1 << 20,
			ShellSuccessAgeTurns: 1,
			ShellSuccessBytes:    80,
			MinIncrementalTokens: 1,
		}},
	}
	content := strings.Join([]string{
		`C:\repo\a.go:10:func alpha() {}`,
		`C:\repo\b.go:20:func beta() {}`,
		`D:/work/c.go:30:func gamma() {}`,
	}, "\n") + "\n" + strings.Repeat(`C:\repo\b.go:99:func repeated() {}`+"\n", 40)
	msgs := []message.Message{
		{Role: "user", Content: "u1"},
		{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "tc1", Name: tools.NameShell, Args: json.RawMessage(`{"command":"rg 'func ' C:\\repo"}`)}}},
		{Role: "tool", ToolCallID: "tc1", Content: content},
		{Role: "user", Content: "u2"},
		{Role: "assistant", Content: "ack"},
		{Role: "user", Content: "u3"},
	}

	prepared := a.prepareMessagesForLLM(msgs)
	got := prepared[2].Content
	if !strings.Contains(got, "Older shell results summarized") {
		t.Fatalf("expected Windows shell search output to use search summary, got %q", got)
	}
	if !strings.Contains(got, `C:\repo\a.go:`) || !strings.Contains(got, `D:/work/c.go:`) {
		t.Fatalf("expected Windows paths grouped in search summary, got %q", got)
	}
	stats := a.GetContextReductionStats()
	if stats.ByToolAndRule[tools.NameShell+"/search_result"].Messages != 1 {
		t.Fatalf("expected shell/search_result stats, got %+v", stats.ByToolAndRule)
	}
}

func TestPrepareMessagesForLLM_JSONReducerBeatsGenericStaleFallback(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.projectConfig = &config.Config{
		Context: config.ContextConfig{Reduction: config.ContextReductionConfig{
			MinToolResultsPrune:  1,
			ReadLikeAgeTurns:     1,
			ReadLikeOutputBytes:  60,
			StaleAgeTurns:        1,
			StaleOutputBytes:     40,
			ShellSuccessAgeTurns: 1,
			ShellSuccessBytes:    60,
			MinIncrementalTokens: 1,
		}},
	}
	content := `[{"path":"a.go","line":10,"message":"bad"},{"path":"b.go","line":12,"message":"worse"},{"path":"c.go","line":99,"message":"ok"},` + strings.Repeat(`{"path":"dup.go","line":1,"message":"extra"},`, 30) + `{"path":"z.go","line":100,"message":"tail"}]`
	msgs := []message.Message{
		{Role: "user", Content: "u1"},
		{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "tc1", Name: tools.NameShell, Args: json.RawMessage(`{"command":"python script.py --json"}`)}}},
		{Role: "tool", ToolCallID: "tc1", Content: content},
		{Role: "user", Content: "u2"},
		{Role: "assistant", Content: "ack"},
		{Role: "user", Content: "u3"},
	}

	prepared := a.prepareMessagesForLLM(msgs)
	if !strings.Contains(prepared[2].Content, "JSON array summarized") {
		t.Fatalf("expected json summary, got %q", prepared[2].Content)
	}
	if strings.Contains(prepared[2].Content, "refer to exported history") || strings.Contains(strings.ToLower(prepared[2].Content), "re-run") {
		t.Fatalf("expected json summary without history/re-run hint, got %q", prepared[2].Content)
	}
	stats := a.GetContextReductionStats()
	if stats.Messages == 0 || stats.Bytes == 0 {
		t.Fatalf("expected json summary to save bytes, stats=%+v", stats)
	}
}

func TestPrepareMessagesForLLM_LongLogReducerBeatsShellSuccessMarker(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.projectConfig = &config.Config{
		Context: config.ContextConfig{Reduction: config.ContextReductionConfig{
			MinToolResultsPrune:  1,
			ReadLikeAgeTurns:     9,
			ReadLikeOutputBytes:  1 << 20,
			StaleAgeTurns:        9,
			StaleOutputBytes:     1 << 20,
			ShellSuccessAgeTurns: 1,
			ShellSuccessBytes:    80,
			MinIncrementalTokens: 1,
		}},
	}
	content := strings.Join([]string{
		"go test ./...",
		"--- FAIL: TestSomething (0.00s)",
		"main_test.go:12: expected 1, got 2",
		"build failed",
		"warning: using fallback config",
		"FAIL\tgithub.com/keakon/chord/internal/agent\t0.123s",
	}, "\n") + "\n" + strings.Repeat("warning: repeated failure detail\n", 60)
	msgs := []message.Message{
		{Role: "user", Content: "u1"},
		{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "tc1", Name: tools.NameShell, Args: json.RawMessage(`{"command":"go test ./..."}`)}}},
		{Role: "tool", ToolCallID: "tc1", Content: content},
		{Role: "user", Content: "u2"},
		{Role: "assistant", Content: "ack"},
		{Role: "user", Content: "u3"},
		{Role: "user", Content: "u4"},
		{Role: "user", Content: "u5"},
	}

	prepared := a.prepareMessagesForLLM(msgs)
	if !strings.Contains(prepared[2].Content, "log summarized for this request to save context") {
		t.Fatalf("expected long log summary, got %q", prepared[2].Content)
	}
	if strings.Contains(prepared[2].Content, "refer to exported history") || strings.Contains(strings.ToLower(prepared[2].Content), "re-run") {
		t.Fatalf("expected long-log summary without history/re-run hint, got %q", prepared[2].Content)
	}
	stats := a.GetContextReductionStats()
	if stats.Messages == 0 || stats.Bytes == 0 {
		t.Fatalf("expected long-log summary to save bytes, stats=%+v", stats)
	}
}

func TestPrepareMessagesForLLM_OlderToolErrorKeepsImportantDetails(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.projectConfig = &config.Config{
		Context: config.ContextConfig{Reduction: config.ContextReductionConfig{
			ErrorAgeTurns:           1,
			HighRiskProtectAgeTurns: 1,
			MinIncrementalTokens:    1,
		}},
	}
	content := strings.Join([]string{
		"running test",
		"--- FAIL: TestImportant (0.00s)",
		"main_test.go:12: expected: 1",
		"main_test.go:13: actual: 2",
		"FAIL",
	}, "\n") + "\n" + strings.Repeat("irrelevant failure padding\n", 80)
	msgs := []message.Message{
		{Role: "user", Content: "u1"},
		{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "tc1", Name: tools.NameShell, Args: json.RawMessage(`{"command":"go test ./..."}`)}}},
		{Role: "tool", ToolCallID: "tc1", Content: content, ToolStatus: "error"},
		{Role: "user", Content: "u2"},
		{Role: "assistant", Content: "ack"},
		{Role: "user", Content: "u3"},
	}

	prepared := a.prepareMessagesForLLM(msgs)
	got := prepared[2].Content
	if !strings.Contains(got, "error summarized") || !strings.Contains(got, "expected: 1") || !strings.Contains(got, "actual: 2") {
		t.Fatalf("expected older error summary to preserve assertion details, got %q", got)
	}
	if got == "[Older tool error omitted]" {
		t.Fatalf("older error should not be fully omitted")
	}
}

func TestPrepareMessagesForLLM_GenericStaleKeepsHeadTailExcerpt(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.projectConfig = &config.Config{
		Context: config.ContextConfig{Reduction: config.ContextReductionConfig{
			MinToolResultsPrune:  1,
			ReadLikeAgeTurns:     9,
			ReadLikeOutputBytes:  1 << 20,
			StaleAgeTurns:        1,
			StaleOutputBytes:     40,
			ShellSuccessAgeTurns: 9,
			ShellSuccessBytes:    1 << 20,
			MinIncrementalTokens: 1,
		}},
	}
	content := "first important line\n" + strings.Repeat("middle filler\n", 80) + "last important line\n"
	msgs := []message.Message{
		{Role: "user", Content: "u1"},
		{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "tc1", Name: "custom_tool", Args: json.RawMessage(`{"query":"x"}`)}}},
		{Role: "tool", ToolCallID: "tc1", Content: content},
		{Role: "user", Content: "u2"},
		{Role: "assistant", Content: "ack"},
		{Role: "user", Content: "u3"},
	}

	prepared := a.prepareMessagesForLLM(msgs)
	got := prepared[2].Content
	if !strings.Contains(got, "output summarized") || !strings.Contains(got, "first important line") || !strings.Contains(got, "last important line") {
		t.Fatalf("expected generic stale summary with head/tail excerpt, got %q", got)
	}
}

func TestPrepareMessagesForLLM_ShellNumberedSourceKeepsExcerpt(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.projectConfig = &config.Config{
		Context: config.ContextConfig{Reduction: config.ContextReductionConfig{
			MinToolResultsPrune:  1,
			ReadLikeAgeTurns:     9,
			ReadLikeOutputBytes:  1 << 20,
			StaleAgeTurns:        9,
			StaleOutputBytes:     1 << 20,
			ShellSuccessAgeTurns: 1,
			ShellSuccessBytes:    80,
			MinIncrementalTokens: 1,
		}},
	}
	content := strings.Join([]string{
		"1 package main",
		"2",
		"3 func main() {",
		"4 \tfmt.Println(computeValue())",
		"5 }",
	}, "\n") + "\n" + strings.Repeat("6 // filler line to force reduction\n", 60)
	msgs := []message.Message{
		{Role: "user", Content: "u1"},
		{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "tc1", Name: tools.NameShell, Args: json.RawMessage(`{"command":"nl -ba main.go"}`)}}},
		{Role: "tool", ToolCallID: "tc1", Content: content},
		{Role: "user", Content: "u2"},
		{Role: "assistant", Content: "ack"},
		{Role: "user", Content: "u3"},
	}

	prepared := a.prepareMessagesForLLM(msgs)
	got := prepared[2].Content
	if !strings.Contains(got, "Older shell output summarized as numbered source") || !strings.Contains(got, "1 package main") || !strings.Contains(got, "3 func main() {") {
		t.Fatalf("expected shell numbered source summary to keep source excerpt, got %q", got)
	}
	if !strings.Contains(got, "range=1-6") {
		t.Fatalf("expected numbered source summary to preserve source range, got %q", got)
	}
	if got == "[Older "+tools.NameShell+" output omitted from this request to save context.]" {
		t.Fatalf("numbered source should not fall back to shell success omission")
	}
	stats := a.GetContextReductionStats()
	if stats.ByToolAndRule[tools.NameShell+"/numbered_source"].Messages != 1 {
		t.Fatalf("expected shell/numbered_source stats, got %+v", stats.ByToolAndRule)
	}
}

func TestPrepareMessagesForLLM_EnhancedReadSummaryIncludesRangeDetails(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.projectConfig = &config.Config{
		Context: config.ContextConfig{Reduction: config.ContextReductionConfig{
			MinToolResultsPrune:  1,
			ReadLikeAgeTurns:     1,
			ReadLikeOutputBytes:  80,
			StaleAgeTurns:        9,
			StaleOutputBytes:     1 << 20,
			ShellSuccessAgeTurns: 9,
			ShellSuccessBytes:    1 << 20,
			MinIncrementalTokens: 1,
		}},
	}
	bodyLines := []string{"func important() {", "\treturn computeImportantValue()"}
	for range 60 {
		bodyLines = append(bodyLines, "\tpadding line to force reduction")
	}
	bodyLines = append(bodyLines, "}")
	content := "READ_RESULT lines=41-103 total=200\n" + strings.Join(bodyLines, "\n") + "\n"
	msgs := []message.Message{
		{Role: "user", Content: "u1"},
		{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "tc1", Name: tools.NameRead, Args: json.RawMessage(`{"path":"internal/agent/compaction_policy.go","offset":41,"limit":63}`)}}},
		{Role: "tool", ToolCallID: "tc1", Content: content},
		{Role: "user", Content: "u2"},
		{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "tc2", Name: tools.NameEdit, Args: json.RawMessage(`{"path":"internal/agent/compaction_policy.go","old_string":"x","new_string":"y"}`)}}},
		{Role: "tool", ToolCallID: "tc2", ToolStatus: "success", Content: "edited"},
		{Role: "user", Content: "u3"},
	}

	prepared := a.prepareMessagesForLLM(msgs)
	got := prepared[2].Content
	// The invalidating edit turns the read stale; the reduction keeps only
	// leading lines and reuses the shared READ_RESULT header with
	// truncated=stale; the lines range starts at the original start and total
	// stays the file total.
	if !strings.HasPrefix(got, "READ_RESULT lines=41-") || !strings.Contains(got, "total=200") || !strings.Contains(got, "truncated=stale") {
		t.Fatalf("expected stale READ_RESULT header, got %q", got)
	}
	if !strings.Contains(got, "func important() {") {
		t.Fatalf("expected leading lines preserved, got %q", got)
	}
	if strings.Contains(got, "}") && strings.HasSuffix(strings.TrimSpace(got), "}") {
		t.Fatalf("expected trailing lines dropped by head truncation, got %q", got)
	}
	stats := a.GetContextReductionStats()
	if stats.Messages == 0 || stats.Bytes == 0 {
		t.Fatalf("expected read summary to save bytes, stats=%+v", stats)
	}
}
