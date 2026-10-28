package agent

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/ctxmgr"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func TestSplitMessagesForCompaction_BuildsSyntheticEvidenceArtifact(t *testing.T) {
	diff := strings.Repeat("+ changed line\n", 30)
	msgs := []message.Message{
		{Role: "user", Content: "Please fix the failing tests."},
		{Role: "assistant", Content: "I'll investigate."},
		{Role: "tool", ToolCallID: "tc1", Content: "go test ./...\n\nError: exit code 1"},
		{Role: "user", Content: "Do not change the API; only fix CLI behavior."},
		{Role: "tool", ToolCallID: "tc2", Content: "patched", ToolDiff: diff},
	}

	head, evidence := splitMessagesForCompactionForTest(msgs, 4096)
	if len(head) == 0 {
		t.Fatal("expected non-empty archived head")
	}
	if got := head[len(head)-1].Content; got != msgs[2].Content {
		t.Fatalf("latest archived message = %q, want %q", got, msgs[2].Content)
	}
	if len(evidence) != 1 {
		t.Fatalf("expected single synthetic evidence artifact, got %d", len(evidence))
	}
	if evidence[0].Role != "user" {
		t.Fatalf("evidence artifact role = %q, want user", evidence[0].Role)
	}
	if !strings.Contains(evidence[0].Content, "[Context Evidence]") {
		t.Fatalf("expected synthetic evidence artifact header, got %q", evidence[0].Content)
	}
	if !strings.Contains(evidence[0].Content, "User correction / constraint") {
		t.Fatalf("expected user correction evidence, got %q", evidence[0].Content)
	}
	if !strings.Contains(evidence[0].Content, "Latest failing tool result") {
		t.Fatalf("expected tool error evidence, got %q", evidence[0].Content)
	}
	if !strings.Contains(evidence[0].Content, "Recent code diff") {
		t.Fatalf("expected diff evidence, got %q", evidence[0].Content)
	}
}

func TestSplitMessagesForCompaction_PreservesRecentRawTailOutsideArchive(t *testing.T) {
	const userTurns = 8
	var msgs []message.Message
	for i := 1; i <= userTurns; i++ {
		msgs = append(msgs,
			message.Message{Role: "user", Content: fmt.Sprintf("u%d", i)},
			message.Message{Role: "assistant", Content: fmt.Sprintf("a%d", i)},
		)
	}

	head, _ := splitMessagesForCompactionForTest(msgs, 8192)
	if len(head) == 0 {
		t.Fatal("expected archived head")
	}
	// Every message fits the tail budget, so the split keeps the newest
	// compactRecentTailTurns authored turns raw and archives the rest.
	want := fmt.Sprintf("a%d", userTurns-compactRecentTailTurns)
	if got := head[len(head)-1].Content; got != want {
		t.Fatalf("latest archived message = %q, want %q", got, want)
	}
}

func TestBuildStructuredFallbackSummaryUsesGoalAnchorWhenRecentTailMissing(t *testing.T) {
	summary := buildStructuredFallbackSummary(
		"history-1.md",
		&compactionInput{RecentTailAnchor: "- (none)", GoalAnchor: "- 分析当前压缩逻辑是否正确"},
		fmt.Errorf("provider returned invalid summary"),
		nil,
		nil,
		nil,
	)
	if !strings.Contains(summary, "Latest recoverable user request from durable anchors") {
		t.Fatalf("fallback summary did not use goal anchor:\n%s", summary)
	}
	if !strings.Contains(summary, "- 分析当前压缩逻辑是否正确") {
		t.Fatalf("fallback summary missing goal anchor:\n%s", summary)
	}
	if !strings.Contains(summary, "Choose the immediate next action only from this latest preserved request") {
		t.Fatalf("fallback next step did not constrain continuation to latest request:\n%s", summary)
	}
	if !strings.Contains(summary, "Ignore completed/background and stale/superseded todos") {
		t.Fatalf("fallback next step did not guard against stale todo restart:\n%s", summary)
	}
}

func TestResolveCompactionProfileAuto(t *testing.T) {
	a := &MainAgent{}
	if got := a.resolveCompactionProfile(nil, nil, nil, nil); got != compactionProfileArchival {
		t.Fatalf("resolveCompactionProfile() = %q, want archival", got)
	}
	if got := a.resolveCompactionProfile([]tools.TodoItem{{ID: "1", Content: "finish", Status: "pending"}}, nil, nil, nil); got != compactionProfileContinuation {
		t.Fatalf("resolveCompactionProfile() with pending todo = %q, want continuation", got)
	}
	if got := a.resolveCompactionProfile(nil, nil, nil, []evidenceItem{{Kind: evidenceToolError}}); got != compactionProfileContinuation {
		t.Fatalf("resolveCompactionProfile() with blocker evidence = %q, want continuation", got)
	}
}

func TestProduceCompactionDraftArchivalProfileOmitsRecentTail(t *testing.T) {
	t.Parallel()
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.globalConfig.Context.Compaction.Profile = config.CompactionProfileArchival
	a.SetProviderModelRef("sample/compact-model")

	providerCfg := llm.NewProviderConfig("sample", config.ProviderConfig{
		Type: "stub",
		Models: map[string]config.ModelConfig{
			"compact-model": {
				Limit: config.ModelLimit{Context: 16384, Output: 2048},
			},
		},
	}, []string{"test-key"})
	provider := &countingCompactionProvider{
		response: &message.Response{Content: validCompactionSummaryForTest("history-1.md")},
	}
	client := llm.NewClient(providerCfg, provider, "compact-model", 2048, "")
	a.SetModelSwitchFactory(func(providerModel string, _ []string, _ string) (*llm.Client, string, int, error) {
		return client, "compact-model", 16384, nil
	})

	snapshot := []message.Message{
		{Role: "user", Content: "u1"},
		{Role: "assistant", Content: "a1"},
		{Role: "user", Content: "u2"},
		{Role: "assistant", Content: "a2"},
		{Role: "user", Content: "u3"},
		{Role: "assistant", Content: "a3"},
	}
	// Test bypasses ctxmgr so use a non-zero headSplit directly. The async path
	// requires headSplit > 0 because tail is preserved by ReplacePrefixAtomic.
	headSplit := len(snapshot)
	draft, err := a.produceCompactionDraftAsync(t.Context(), a.testCompactionInput(snapshot, false, 1, compactionTarget{sessionEpoch: a.sessionEpoch}, headSplit, compactionProfileArchival, "", nil, a.captureCompactionArchiveMeta()))
	if err != nil {
		t.Fatalf("produceCompactionDraftAsync error: %v", err)
	}
	if draft.Skip {
		t.Fatal("expected non-skip compaction draft")
	}
	if draft.Profile != config.CompactionProfileArchival {
		t.Fatalf("draft.Profile = %q, want archival", draft.Profile)
	}
	if got := len(draft.NewMessages); got != 1 {
		t.Fatalf("len(draft.NewMessages) = %d, want 1 summary-only message", got)
	}
	if len(draft.SourceRefs) != headSplit || draft.SourceFingerprint == "" {
		t.Fatalf("draft provenance refs=%d fingerprint=%q", len(draft.SourceRefs), draft.SourceFingerprint)
	}
}

func TestCompactionHeadSplitPreservesContinuationTail(t *testing.T) {
	snapshot := []message.Message{
		{Role: message.RoleUser, Content: "u1"},
		{Role: message.RoleAssistant, Content: "a1"},
		{Role: message.RoleUser, Content: "u2"},
		{Role: message.RoleAssistant, Content: "a2"},
		{Role: message.RoleUser, Content: "u3"},
		{Role: message.RoleAssistant, Content: "a3"},
		{Role: message.RoleUser, Content: "u4"},
		{Role: message.RoleAssistant, Content: "a4"},
	}
	headSplit := compactionHeadSplitForProfile(nil, compactionProfileContinuation, snapshot, 16384)
	if headSplit != 4 {
		t.Fatalf("continuation headSplit = %d, want 4 so the latest two user turns remain raw", headSplit)
	}
	if got := snapshot[headSplit:]; !reflect.DeepEqual(got, snapshot[4:]) {
		t.Fatalf("preserved tail = %+v, want %+v", got, snapshot[4:])
	}
	if archival := compactionHeadSplitForProfile(nil, compactionProfileArchival, snapshot, 16384); archival != len(snapshot) {
		t.Fatalf("archival headSplit = %d, want %d", archival, len(snapshot))
	}
}

func TestCompactionHeadSplitKeepsMinimumSummaryHead(t *testing.T) {
	snapshot := []message.Message{
		{Role: message.RoleUser, Content: "u1"},
		{Role: message.RoleAssistant, Content: "a1"},
		{Role: message.RoleUser, Content: "u2"},
		{Role: message.RoleAssistant, Content: "a2"},
		{Role: message.RoleUser, Content: "u3"},
		{Role: message.RoleAssistant, Content: "a3"},
	}
	if got := compactionHeadSplitForProfile(nil, compactionProfileContinuation, snapshot, 16384); got != 4 {
		t.Fatalf("headSplit = %d, want a four-message summary head and one raw user turn", got)
	}
}

func TestCompactionHeadSplitKeepsToolCallPairTogether(t *testing.T) {
	snapshot := []message.Message{
		{Role: message.RoleUser, Content: "u1"},
		{Role: message.RoleAssistant, Content: "a1"},
		{Role: message.RoleUser, Content: "u2"},
		{Role: message.RoleAssistant, Content: "a2"},
		{Role: message.RoleUser, Content: "u3"},
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "read-1", Name: tools.NameRead}}},
		{Role: message.RoleTool, ToolCallID: "read-1", Content: "result"},
		{Role: message.RoleAssistant, Content: "done"},
	}
	headSplit := compactionHeadSplitForProfile(nil, compactionProfileContinuation, snapshot, 16384)
	if headSplit != 4 {
		t.Fatalf("headSplit = %d, want 4 before the preserved user/tool turn", headSplit)
	}
	if snapshot[headSplit].Role != message.RoleUser || snapshot[headSplit+1].Role != message.RoleAssistant || len(snapshot[headSplit+1].ToolCalls) != 1 {
		t.Fatalf("preserved tail does not begin at a complete user/tool turn: %+v", snapshot[headSplit:])
	}
}

func TestCompactionHeadSplitFallsBackToFullHeadWhenSafeTailIsTooLarge(t *testing.T) {
	snapshot := []message.Message{
		{Role: message.RoleUser, Content: "u1"},
		{Role: message.RoleAssistant, Content: "a1"},
		{Role: message.RoleUser, Content: "u2"},
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "read-1", Name: tools.NameRead}}},
		{Role: message.RoleTool, ToolCallID: "read-1", Content: "result"},
		{Role: message.RoleAssistant, Content: "done"},
	}
	if got := compactionHeadSplitForProfile(nil, compactionProfileContinuation, snapshot, 16384); got != len(snapshot) {
		t.Fatalf("headSplit = %d, want full head %d when preserving the tool turn leaves too little to summarize", got, len(snapshot))
	}
}

func TestSplitMessagesForCompaction_FallsBackToLegacyExtractionHelper(t *testing.T) {
	msgs := []message.Message{
		{Role: "user", Content: "Earlier discussion."},
		{Role: "assistant", Content: "working"},
		{Role: "user", Content: "[SubAgent agent-2 requests intervention]\n\nReason: merge conflict in auth flow\n\nDecide how to help this agent."},
		{Role: "assistant", Content: "ok"},
	}

	items := selectEvidenceItems(msgs, 4096)
	if len(items) == 0 {
		t.Fatal("expected fallback evidence items")
	}
	if items[0].Kind != evidenceEscalate {
		t.Fatalf("first item kind = %q, want %q", items[0].Kind, evidenceEscalate)
	}
}

func TestSplitMessagesForCompaction_UsesRuntimeEvidenceCandidates(t *testing.T) {
	t.Parallel()
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.addEvidenceCandidate(buildEvidenceItem(
		evidenceUserCorrection,
		"User correction / constraint",
		"preserve correction",
		"runtime candidate",
		"Do not change the API; only fix CLI behavior.",
	))
	msgs := []message.Message{
		{Role: "user", Content: "old request"},
		{Role: "assistant", Content: "working"},
		{Role: "user", Content: "plain follow-up"},
		{Role: "assistant", Content: "done"},
	}
	head, evidence := splitMessagesForCompactionForTestWithAgent(a, msgs, 4096)
	if len(head) == 0 {
		t.Fatal("expected non-empty archived head")
	}
	if len(evidence) != 1 {
		t.Fatalf("len(evidence) = %d, want 1", len(evidence))
	}
	if !strings.Contains(evidence[0].Content, "User correction / constraint") {
		t.Fatalf("runtime evidence not used: %q", evidence[0].Content)
	}
}

func TestSplitMessagesForCompaction_PreservesLatestActionableUserRequestEvidence(t *testing.T) {
	msgs := []message.Message{
		{Role: "user", Content: "Implement feature X."},
		{Role: "assistant", Content: "Done."},
		{Role: "user", Content: "Thanks."},
		{Role: "assistant", Content: "You're welcome."},
	}

	head, evidence := splitMessagesForCompactionForTest(msgs, 4096)
	if len(head) == 0 {
		t.Fatal("expected non-empty archived head")
	}
	if len(evidence) != 1 {
		t.Fatalf("len(evidence) = %d, want 1", len(evidence))
	}
	if !strings.Contains(evidence[0].Content, "Latest user request") || !strings.Contains(evidence[0].Content, "Implement feature X") || strings.Contains(evidence[0].Content, "Thanks") {
		t.Fatalf("latest actionable request evidence mismatch: %q", evidence[0].Content)
	}
}

// agenticTailHistory builds userTurns turns, each with callsPerTurn tool calls
// whose results are payload bytes long.
func agenticTailHistory(userTurns, callsPerTurn, payload int) []message.Message {
	body := strings.Repeat("x", payload)
	var msgs []message.Message
	for u := range userTurns {
		msgs = append(msgs, message.Message{Role: message.RoleUser, Content: fmt.Sprintf("request %d", u)})
		for c := range callsPerTurn {
			id := fmt.Sprintf("call-%d-%d", u, c)
			msgs = append(msgs,
				message.Message{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: id, Name: "read"}}},
				message.Message{Role: message.RoleTool, ToolCallID: id, Content: body},
			)
		}
		msgs = append(msgs, message.Message{Role: message.RoleAssistant, Content: "turn wrap-up"})
	}
	return msgs
}

func TestSelectRecentTailDegradesInsteadOfDroppingTail(t *testing.T) {
	const contextLimit = 200000
	budget := recentTailTokenBudget(contextLimit)

	// A latest user turn whose own tool loop dwarfs the tail budget: whole-turn
	// selection cannot fit, so the suffix fallback must still preserve context.
	messages := agenticTailHistory(12, 5, 12000)
	tail := selectRecentTailMessages(nil, messages, compactRecentTailTurns, budget)
	if len(tail) == 0 {
		t.Fatal("large agentic turn produced no recent tail; continuation compaction would degrade to summary-only")
	}
	if got := ctxmgr.EstimateMessagesTokens(tail); got > budget {
		t.Fatalf("tail tokens = %d, want <= %d", got, budget)
	}
	if tail[0].Role == message.RoleTool {
		t.Fatalf("tail starts with an orphan tool result: %+v", tail[0])
	}
}

// TestSelectRecentTailIgnoresSyntheticUserRoleMessages pins that the recent-tail
// user-turn count follows authored turns, not RoleUser: a trailing
// context_notice or background_result must not consume one of the two slots and
// push a real user turn out of the verbatim tail.
func TestSelectRecentTailIgnoresSyntheticUserRoleMessages(t *testing.T) {
	messages := []message.Message{
		{Role: message.RoleAssistant, Content: "warmup"},
		{Role: message.RoleUser, Content: "real-1"},
		{Role: message.RoleAssistant, Content: "reply-1"},
		{Role: message.RoleUser, Content: "real-2"},
		{Role: message.RoleAssistant, Content: "reply-2"},
		{Role: message.RoleUser, Content: "context pressure", Kind: message.KindContextNotice},
	}
	tail := selectRecentTailMessages(nil, messages, 2, recentTailTokenBudget(200000))
	if len(tail) == 0 {
		t.Fatal("expected a recent tail")
	}
	if tail[0].Content != "real-1" {
		t.Fatalf("tail starts at %q, want the first of the two authored turns", tail[0].Content)
	}
}

func TestCompactionHeadSplitPreservesTailForLargeAgenticTurn(t *testing.T) {
	const contextLimit = 200000
	messages := agenticTailHistory(12, 5, 12000)
	split := compactionHeadSplitForProfile(nil, compactionProfileContinuation, messages, contextLimit)
	if split <= 0 || split >= len(messages) {
		t.Fatalf("head split = %d with %d messages; want a split that keeps a raw tail", split, len(messages))
	}
	if messages[split].Role == message.RoleTool {
		t.Fatalf("head split %d leaves an orphan tool result at the tail head", split)
	}
	if got := ctxmgr.EstimateMessagesTokens(messages[split:]); got > recentTailTokenBudget(contextLimit) {
		t.Fatalf("preserved tail tokens = %d, want <= %d", got, recentTailTokenBudget(contextLimit))
	}
}

func TestCompactionHeadSplitStillArchivesEverythingForArchival(t *testing.T) {
	messages := agenticTailHistory(12, 5, 12000)
	if got := compactionHeadSplitForProfile(nil, compactionProfileArchival, messages, 200000); got != len(messages) {
		t.Fatalf("archival head split = %d, want %d", got, len(messages))
	}
}

func TestLongestSafeTailWithinBudgetRejectsUnsafeStarts(t *testing.T) {
	messages := []message.Message{
		{Role: message.RoleUser, Content: "start"},
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "a"}, {ID: "b"}}},
		{Role: message.RoleTool, ToolCallID: "a", Content: "one"},
		{Role: message.RoleTool, ToolCallID: "b", Content: "two"},
		{Role: message.RoleAssistant, Content: "done"},
	}
	tail := longestSafeTailWithinBudget(nil, messages, 100000)
	if len(tail) == 0 {
		t.Fatal("expected a tail")
	}
	if tail[0].Role == message.RoleTool {
		t.Fatalf("tail starts with an orphan tool result: %+v", tail[0])
	}
}

func TestFormatRecentTailAnchorBoundsRenderedMessages(t *testing.T) {
	var messages []message.Message
	for i := range compactRecentTailAnchorMessages + 8 {
		messages = append(messages, message.Message{Role: message.RoleAssistant, Content: fmt.Sprintf("line %d", i)})
	}
	anchor := formatRecentTailAnchor(messages)
	if got := strings.Count(anchor, "\n") + 1; got > compactRecentTailAnchorMessages+1 {
		t.Fatalf("anchor rendered %d lines, want at most %d", got, compactRecentTailAnchorMessages+1)
	}
	if !strings.Contains(anchor, "older preserved message(s) omitted here") {
		t.Fatalf("anchor missing omission notice: %q", anchor)
	}
	if !strings.Contains(anchor, fmt.Sprintf("line %d", len(messages)-1)) {
		t.Fatalf("anchor dropped the newest message: %q", anchor)
	}
}
