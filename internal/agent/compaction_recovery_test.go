package agent

import (
	"strings"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func TestApplyCompactionRecoveryStateReplacesSummaryRequest(t *testing.T) {
	summary := "## Current User Request\n- the model's stale guess\n\n## Next Step\n- run the tests"
	got := applyCompactionRecoveryState(summary, compactionRecoveryState{
		anchor: fallbackAnchor{Kind: "user_request", Label: "Latest user request", Text: "finish the parser"},
	})
	if strings.Contains(got, "stale guess") {
		t.Fatalf("the summary body must be replaced by the runtime anchor:\n%s", got)
	}
	if n := strings.Count(got, checkpointCurrentUserRequestHeading); n != 1 {
		t.Fatalf("heading count = %d, want 1:\n%s", n, got)
	}
	if !strings.Contains(got, "- Latest user request: finish the parser") {
		t.Fatalf("runtime anchor missing:\n%s", got)
	}
	if !strings.Contains(got, "## Next Step\n- run the tests") {
		t.Fatalf("later sections must survive:\n%s", got)
	}
}

func TestApplyCompactionRecoveryStateInsertsMissingRequestSection(t *testing.T) {
	got := applyCompactionRecoveryState("## Next Step\n- run the tests", compactionRecoveryState{
		anchor: fallbackAnchor{Kind: "user_request", Label: "Latest user request", Text: "finish the parser"},
	})
	if !strings.HasPrefix(got, checkpointCurrentUserRequestHeading+"\n") {
		t.Fatalf("a summary without the section must get it first:\n%s", got)
	}
	if !strings.Contains(got, "## Next Step\n- run the tests") {
		t.Fatalf("the existing sections must survive:\n%s", got)
	}
}

func TestApplyCompactionRecoveryStateKeepsSummaryWithoutAnchor(t *testing.T) {
	summary := "## Current User Request\n- Unknown: no reliable latest user request was preserved\n\n## Next Step\n- inspect the archives"
	if got := applyCompactionRecoveryState(summary, compactionRecoveryState{}); got != summary {
		t.Fatalf("an empty runtime state must leave the summary untouched:\ngot:\n%s\nwant:\n%s", got, summary)
	}
}

func TestBuildCompactionRecoveryStateResolvesAnchorAndUnsettledCalls(t *testing.T) {
	transcript := []message.Message{
		{Role: message.RoleUser, Content: "do X"},
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "call-1", Name: tools.NameApplyPatch}}},
		{Role: message.RoleTool, ToolCallID: "call-1", Content: "session restored before the result was persisted", ToolRecoveryState: message.ToolRecoveryStateOutcomeUnknown},
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "call-2", Name: "Bash"}}},
		{Role: message.RoleTool, ToolCallID: "call-2", Content: "ok", ToolStatus: "success"},
	}
	state := buildCompactionRecoveryState(transcript, transcript)
	if state.anchor.Kind != "user_request" || state.anchor.Text != "do X" {
		t.Fatalf("anchor = %#v, want the latest user request", state.anchor)
	}
	if len(state.unsettled) != 1 {
		t.Fatalf("unsettled = %#v, want only the outcome_unknown result", state.unsettled)
	}
	if state.unsettled[0] != (unsettledToolCall{CallID: "call-1", Tool: tools.NameApplyPatch}) {
		t.Fatalf("unsettled[0] = %#v, want call-1 of the patch tool", state.unsettled[0])
	}
}

func TestBuildCompactionRecoveryStateDetectsDoneRejection(t *testing.T) {
	transcript := []message.Message{
		{Role: message.RoleUser, Content: "finish the refactor"},
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "done-1", Name: tools.NameDone}}},
		{Role: message.RoleTool, ToolCallID: "done-1", Content: "Done rejected: the parser tests still fail"},
	}
	state := buildCompactionRecoveryState(transcript, transcript)
	if state.anchor.Kind != "done_rejected" {
		t.Fatalf("anchor = %#v, want the Done rejection", state.anchor)
	}
	got := applyCompactionRecoveryState("## Current User Request\n- (model text)\n\n## Next Step\n- x", state)
	if !strings.Contains(got, "- Latest Done rejected reason: the parser tests still fail") {
		t.Fatalf("the rejection must reach the checkpoint:\n%s", got)
	}
}

func TestEnsureCompactionUnsettledToolStateReplacesAndClearsSection(t *testing.T) {
	calls := []unsettledToolCall{{CallID: "call-1", Tool: "apply_patch"}}
	withSection := applyCompactionRecoveryState("## Next Step\n- x", compactionRecoveryState{unsettled: calls})
	if !strings.Contains(withSection, runtimeRecoveryStateHeading) {
		t.Fatalf("the section must be rendered:\n%s", withSection)
	}
	if !strings.Contains(withSection, "apply_patch (call-1)") {
		t.Fatalf("the unsettled call must be named:\n%s", withSection)
	}
	// Re-applying replaces the block instead of stacking a second copy.
	again := applyCompactionRecoveryState(withSection, compactionRecoveryState{unsettled: calls})
	if n := strings.Count(again, runtimeRecoveryStateHeading); n != 1 {
		t.Fatalf("heading count = %d, want 1:\n%s", n, again)
	}
	// A capture with nothing unsettled removes the block entirely.
	resolved := applyCompactionRecoveryState(again, compactionRecoveryState{})
	if strings.Contains(resolved, runtimeRecoveryStateHeading) || strings.Contains(resolved, "call-1") {
		t.Fatalf("a resolved capture must drop the block:\n%s", resolved)
	}
	if !strings.Contains(resolved, "## Next Step\n- x") {
		t.Fatalf("dropping the block must keep the other sections:\n%s", resolved)
	}
}

func TestCarryStripsTheRuntimeRecoveryStateSection(t *testing.T) {
	block := renderRuntimeRecoveryStateSection([]unsettledToolCall{{CallID: "call-1", Tool: "apply_patch"}})
	content := message.CompactionSummaryHeader + "\n\n## Current User Request\n- continue\n\n" + block +
		"\n\n## Next Step\n- continue" + message.CompactionCompressedTag
	got := latestPriorCheckpointStrippedBody([]message.Message{{Role: message.RoleUser, Content: content, IsCompactionSummary: true}})
	if strings.Contains(got, runtimeRecoveryStateHeading) || strings.Contains(got, "call-1") {
		t.Fatalf("the runtime-owned block must not be carried into the next checkpoint:\n%s", got)
	}
	if !strings.Contains(got, "## Current User Request") || !strings.Contains(got, "## Next Step") {
		t.Fatalf("stripping removed unrelated sections:\n%s", got)
	}
}

func TestStructuredFallbackSummaryCarriesRuntimeRecoveryState(t *testing.T) {
	// The fallback paths compose the same runtime-owned state as the model
	// summary path, so a summarization failure cannot drop the authority.
	fallback := buildStructuredFallbackSummary("history-1.md", &compactionInput{}, nil, []string{"key.go"}, nil, nil)
	got := applyCompactionRecoveryState(fallback, compactionRecoveryState{
		anchor:    fallbackAnchor{Kind: "done_rejected", Label: "Latest Done rejected reason", Text: "redo it"},
		unsettled: []unsettledToolCall{{CallID: "call-9", Tool: "Bash"}},
	})
	if !strings.Contains(got, "- Latest Done rejected reason: redo it") {
		t.Fatalf("the fallback body must be replaced by the runtime anchor:\n%s", got)
	}
	if n := strings.Count(got, checkpointCurrentUserRequestHeading); n != 1 {
		t.Fatalf("heading count = %d, want 1:\n%s", n, got)
	}
	if !strings.Contains(got, runtimeRecoveryStateHeading) || !strings.Contains(got, "Bash (call-9)") {
		t.Fatalf("the fallback must carry the unsettled tool state:\n%s", got)
	}
}

// The model-driven checkpoint composes the same runtime-owned recovery state
// through the shared step, without duplicating the section it already renders.
func TestModelDrivenCheckpointCarriesRuntimeRecoveryState(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	snapshot := []message.Message{
		{Role: message.RoleUser, Content: "next request"},
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "call-unsettled", Name: "Bash"}}},
		{Role: message.RoleTool, ToolCallID: "call-unsettled", Content: "session restored before the result was persisted", ToolRecoveryState: message.ToolRecoveryStateOutcomeUnknown},
	}
	req := &modelDrivenCheckpointRequest{Args: tools.CompactContextArgs{ActiveObjective: "keep going", NextStep: "continue"}}
	summary := a.buildModelDrivenCheckpointSummary(modelDrivenBarrierSnapshot{snapshot: snapshot}, snapshot, len(snapshot), req)
	if !strings.Contains(summary, "- Latest user request: next request") {
		t.Fatalf("checkpoint missing the runtime-resolved request:\n%s", summary)
	}
	if n := strings.Count(summary, checkpointCurrentUserRequestHeading); n != 1 {
		t.Fatalf("heading count = %d, want 1:\n%s", n, summary)
	}
	if !strings.Contains(summary, runtimeRecoveryStateHeading) || !strings.Contains(summary, "Bash (call-unsettled)") {
		t.Fatalf("checkpoint missing the unsettled tool state:\n%s", summary)
	}
}

// The usage-driven draft composes the same runtime-owned state, so a
// summarizer that writes a stale request section (or ignores the unsettled
// tool results) cannot drop either from the checkpoint.
func TestProduceCompactionDraftCarriesRuntimeRecoveryState(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.globalConfig.Context.Compaction.Profile = config.CompactionProfileArchival
	a.SetProviderModelRef("sample/compact-model")
	providerCfg := llm.NewProviderConfig("sample", config.ProviderConfig{
		Type: "stub",
		Models: map[string]config.ModelConfig{
			"compact-model": {Limit: config.ModelLimit{Context: 16384, Output: 2048}},
		},
	}, []string{"test-key"})
	provider := &countingCompactionProvider{response: &message.Response{Content: validCompactionSummaryForTest("history-1.md")}}
	client := llm.NewClient(providerCfg, provider, "compact-model", 2048, "")
	a.SetModelSwitchFactory(func(string, []string, string) (*llm.Client, string, int, error) {
		return client, "compact-model", 16384, nil
	})

	snapshot := []message.Message{
		{Role: message.RoleUser, Content: "u1"},
		{Role: message.RoleAssistant, Content: "a1"},
		{Role: message.RoleUser, Content: "u2"},
		{Role: message.RoleAssistant, Content: "a2"},
		{Role: message.RoleUser, Content: "u3"},
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "call-unsettled", Name: "Bash"}}},
		{Role: message.RoleTool, ToolCallID: "call-unsettled", Content: "session restored before the result was persisted", ToolRecoveryState: message.ToolRecoveryStateOutcomeUnknown},
	}
	draft, err := a.produceCompactionDraftAsync(t.Context(), snapshot, false, 1, compactionTarget{sessionEpoch: a.sessionEpoch}, len(snapshot), compactionProfileArchival, "", nil, a.captureCompactionArchiveMeta())
	if err != nil {
		t.Fatalf("produceCompactionDraftAsync: %v", err)
	}
	if draft.Skip || len(draft.NewMessages) == 0 {
		t.Fatalf("draft = %+v, want one summary message", draft)
	}
	body := draft.NewMessages[0].Content
	if !strings.Contains(body, "- Latest user request: u3") {
		t.Fatalf("checkpoint missing the runtime-resolved request:\n%s", body)
	}
	if strings.Contains(body, "- continue current task") {
		t.Fatalf("the summarizer's own request body must be replaced:\n%s", body)
	}
	if !strings.Contains(body, runtimeRecoveryStateHeading) || !strings.Contains(body, "Bash (call-unsettled)") {
		t.Fatalf("checkpoint missing the unsettled tool state:\n%s", body)
	}
}
