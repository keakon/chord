package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/ctxmgr"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
	"github.com/keakon/chord/internal/tools"
)

type countingCompactionProvider struct {
	calls        int
	compactCalls int
	response     *message.Response
	err          error
}

type countingSummaryOnlyProvider struct {
	calls    int
	response *message.Response
	err      error
}

func (p *countingCompactionProvider) CompleteStream(
	_ context.Context,
	_ string,
	_ string,
	_ string,
	_ []message.Message,
	_ []message.ToolDefinition,
	_ int,
	_ llm.RequestTuning,
	_ llm.StreamCallback,
) (*message.Response, error) {
	p.calls++
	if p.err != nil {
		return nil, p.err
	}
	if p.response != nil {
		return p.response, nil
	}
	return &message.Response{}, nil
}

func (p *countingCompactionProvider) Complete(
	_ context.Context,
	_ string,
	_ string,
	_ string,
	_ []message.Message,
	_ []message.ToolDefinition,
	_ int,
	_ llm.RequestTuning,
) (*message.Response, error) {
	p.calls++
	if p.err != nil {
		return nil, p.err
	}
	if p.response != nil {
		return p.response, nil
	}
	return &message.Response{}, nil
}

func (p *countingCompactionProvider) Compact(
	_ context.Context,
	_ string,
	_ string,
	_ string,
	_ []message.Message,
	_ []message.ToolDefinition,
	_ int,
	_ llm.RequestTuning,
) (*message.Response, error) {
	p.compactCalls++
	if p.err != nil {
		return nil, p.err
	}
	if p.response != nil {
		return p.response, nil
	}
	return &message.Response{}, nil
}

func (p *countingSummaryOnlyProvider) CompleteStream(
	_ context.Context,
	_ string,
	_ string,
	_ string,
	_ []message.Message,
	_ []message.ToolDefinition,
	_ int,
	_ llm.RequestTuning,
	_ llm.StreamCallback,
) (*message.Response, error) {
	p.calls++
	if p.err != nil {
		return nil, p.err
	}
	if p.response != nil {
		return p.response, nil
	}
	return &message.Response{}, nil
}

func (p *countingSummaryOnlyProvider) Complete(
	_ context.Context,
	_ string,
	_ string,
	_ string,
	_ []message.Message,
	_ []message.ToolDefinition,
	_ int,
	_ llm.RequestTuning,
) (*message.Response, error) {
	p.calls++
	if p.err != nil {
		return nil, p.err
	}
	if p.response != nil {
		return p.response, nil
	}
	return &message.Response{}, nil
}

func validCompactionSummaryForTest(history string) string {
	return fmt.Sprintf(
		"## Goal\n- continue current task\n\n## User Constraints\n- none\n\n## Progress\n- progress recorded\n\n## Key Decisions\n- decisions captured\n\n## Files and Evidence\n- Archived history: %s\n- internal/agent/compaction.go\n\n## Todo State\n- none\n\n## SubAgent State\n- none active\n\n## Open Problems\n- none\n\n## Next Step\n- continue",
		history,
	)
}

func TestPrepareMessagesForLLM_PrunesRepeatedAndErrorOutputs(t *testing.T) {
	a := &MainAgent{}

	msgs := []message.Message{
		{Role: "user", Content: "u1"},
		{Role: "assistant", ToolCalls: []message.ToolCall{
			{ID: "tc1", Name: "Read", Args: json.RawMessage(`{"path":"a.go"}`)},
		}},
		{Role: "tool", ToolCallID: "tc1", Content: "very old read output"},
		{Role: "user", Content: "u2"},
		{Role: "assistant", ToolCalls: []message.ToolCall{
			{ID: "tc2", Name: "Read", Args: json.RawMessage(`{"path":"a.go"}`)},
		}},
		{Role: "tool", ToolCallID: "tc2", Content: "new read output"},
		{Role: "user", Content: "u3"},
		{Role: "assistant", ToolCalls: []message.ToolCall{
			{ID: "tc3", Name: "Bash", Args: json.RawMessage(`{"cmd":"bad"}`)},
		}},
		{Role: "tool", ToolCallID: "tc3", Content: "Error: command failed"},
		{Role: "user", Content: "u4"},
		{Role: "user", Content: "u5"},
		{Role: "user", Content: "u6"},
		{Role: "user", Content: "u7"},
		{Role: "user", Content: "u8"},
	}

	prepared := a.prepareMessagesForLLM(msgs)
	if !strings.Contains(prepared[2].Content, "Repeated Read output omitted") {
		t.Fatalf("expected repeated tool output to be pruned, got %q", prepared[2].Content)
	}
	if prepared[8].Content != "[Older tool error omitted]" {
		t.Fatalf("expected old error output to be pruned, got %q", prepared[8].Content)
	}
}

func TestPrepareMessagesForLLM_PrunesOldReadLikeOutput(t *testing.T) {
	a := &MainAgent{}
	msgs := []message.Message{
		{Role: "user", Content: "u1"},
		{Role: "assistant", ToolCalls: []message.ToolCall{
			{ID: "tc1", Name: "Read", Args: json.RawMessage(`{"path":"a.go"}`)},
		}},
		{Role: "tool", ToolCallID: "tc1", Content: strings.Repeat("large read output ", 400)},
		{Role: "user", Content: "u2"},
		{Role: "user", Content: "u3"},
	}

	prepared := a.prepareMessagesForLLM(msgs)
	if !strings.Contains(prepared[2].Content, "Older Read output truncated to save context; file=a.go") {
		t.Fatalf("expected old read output to keep path hint, got %q", prepared[2].Content)
	}
	if !strings.Contains(prepared[2].Content, "large read output") {
		t.Fatalf("expected old read output to keep a small excerpt, got %q", prepared[2].Content)
	}
}

func TestPrepareMessagesForLLM_PrunesOldSuccessfulBashOutput(t *testing.T) {
	a := &MainAgent{}
	msgs := []message.Message{
		{Role: "user", Content: "u1"},
		{Role: "assistant", ToolCalls: []message.ToolCall{
			{ID: "tc1", Name: "Bash", Args: json.RawMessage(`{"command":"npm test"}`)},
		}},
		{Role: "tool", ToolCallID: "tc1", Content: strings.Repeat("test output line\n", 500)},
		{Role: "user", Content: "u2"},
		{Role: "user", Content: "u3"},
		{Role: "user", Content: "u4"},
	}

	prepared := a.prepareMessagesForLLM(msgs)
	if prepared[2].Content != "[Older Bash output omitted to save context; re-run the command if needed.]" {
		t.Fatalf("expected old successful Bash output to be pruned, got %q", prepared[2].Content)
	}
}

func TestSplitMessagesForCompaction_BuildsSyntheticEvidenceArtifact(t *testing.T) {
	diff := strings.Repeat("+ changed line\n", 30)
	msgs := []message.Message{
		{Role: "user", Content: "Please fix the failing tests."},
		{Role: "assistant", Content: "I'll investigate."},
		{Role: "tool", ToolCallID: "tc1", Content: "go test ./...\n\nError: exit code 1"},
		{Role: "user", Content: "Do not change the API; only fix CLI behavior."},
		{Role: "tool", ToolCallID: "tc2", Content: "patched", ToolDiff: diff},
	}

	head, evidence := splitMessagesForCompaction(msgs, 4096)
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
	msgs := []message.Message{
		{Role: "user", Content: "u1"},
		{Role: "assistant", Content: "a1"},
		{Role: "user", Content: "u2"},
		{Role: "assistant", Content: "a2"},
		{Role: "user", Content: "u3"},
		{Role: "assistant", Content: "a3"},
		{Role: "user", Content: "u4"},
		{Role: "assistant", Content: "a4"},
	}

	head, _ := splitMessagesForCompaction(msgs, 8192)
	if len(head) == 0 {
		t.Fatal("expected archived head")
	}
	if got := head[len(head)-1].Content; got != "a2" {
		t.Fatalf("latest archived message = %q, want a2", got)
	}
}

func TestValidateCompactionSummaryRejectsWeakOneLiner(t *testing.T) {
	if err := validateCompactionSummary("Keep improving extraction quality."); err == nil {
		t.Fatal("expected weak one-line summary to be rejected")
	}
}

func TestBuildStructuredFallbackSummaryIncludesSections(t *testing.T) {
	summary := buildStructuredFallbackSummary(
		"history-1.md",
		&compactionInput{RecentTailAnchor: "- user: continue", EvidenceItems: []evidenceItem{{Kind: evidenceUserCorrection, Excerpt: "do not hardcode"}}},
		fmt.Errorf("summary too short"),
		nil,
		nil,
		nil,
		nil,
	)
	for _, heading := range compactionRequiredHeadings {
		if !strings.Contains(summary, heading) {
			t.Fatalf("fallback summary missing heading %q:\n%s", heading, summary)
		}
	}
	if !strings.Contains(summary, "history-1.md") {
		t.Fatalf("fallback summary missing archive path:\n%s", summary)
	}
}

func TestHandleCompactionReadyRechecksGateAfterQueuedInput(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.ctxMgr.SetMaxTokens(1024)
	a.gitStatusInjected.Store(true)
	a.newTurn()
	if a.turn == nil {
		t.Fatal("expected active turn")
	}
	turnID := a.turn.ID
	a.startCompactionState(1, compactionTarget{turnID: turnID, turnEpoch: a.turn.Epoch, sessionEpoch: a.sessionEpoch}, compactionTrigger{UsageDriven: true}, continuationPlan{kind: compactionResumeMainLLM, turnID: turnID, turnEpoch: a.turn.Epoch, agentErrSourceID: "main"})
	a.pendingUserMessages = []pendingUserMessage{{Content: strings.Repeat("queued user message ", 220)}}

	draft := &compactionDraft{
		NewMessages:    []message.Message{{Role: "user", Content: "[Context Summary]\nsummary"}},
		Index:          1,
		AbsHistoryPath: "/tmp/history-1.md",
		RelHistoryPath: "history-1.md",
		SummaryMode:    "truncate_only",
		ModelRef:       "fallback",
		Manual:         false,
		ArchivedCount:  2,
	}
	a.handleCompactionReady(Event{Type: EventCompactionReady, TurnID: turnID, Payload: draft})

	if !a.IsCompactionRunning() {
		t.Fatal("compaction state should be running after gate re-check")
	}
	pending := a.currentCompactionPendingCall()
	if pending == nil || pending.turnID != turnID {
		t.Fatalf("pending continuation = %+v, want turn %d", pending, turnID)
	}
	if got := len(a.pendingUserMessages); got != 1 {
		t.Fatalf("len(pendingUserMessages) = %d, want 1 queued user message until next resume", got)
	}
	msgs := a.ctxMgr.Snapshot()
	if got := len(msgs); got != 0 {
		t.Fatalf("len(snapshot) = %d, want 0 because stale draft should be ignored", got)
	}
}

func TestHandleCompactCommandSchedulesAsyncCompaction(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.ctxMgr.Append(message.Message{Role: "user", Content: "one"})
	a.ctxMgr.Append(message.Message{Role: "assistant", Content: "two"})
	a.ctxMgr.Append(message.Message{Role: "user", Content: "three"})
	a.ctxMgr.Append(message.Message{Role: "assistant", Content: "four"})

	a.handleCompactCommand()
	if !a.IsCompactionRunning() {
		t.Fatal("compaction state should be running after /compact scheduling")
	}
	pending := a.currentCompactionPendingCall()
	if pending == nil {
		t.Fatal("pending continuation should be set for async manual compaction")
	}
	if pending.turnID != 0 {
		t.Fatalf("pending.turnID = %d, want 0 for manual compaction", pending.turnID)
	}
	if pending.continuation != compactionResumeIdle {
		t.Fatalf("pending continuation = %q, want %q", pending.continuation, compactionResumeIdle)
	}
}

func TestScheduleCompactionSkipsWhenAlreadyRunning(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.startCompactionState(7, compactionTarget{sessionEpoch: a.sessionEpoch}, compactionTrigger{UsageDriven: true}, continuationPlan{kind: compactionResumeIdle})

	if ok := a.scheduleCompaction(false); ok {
		t.Fatal("scheduleCompaction should return false while compaction is already running")
	}
	pending := a.currentCompactionPendingCall()
	if pending == nil {
		t.Fatal("expected existing compaction pending state to remain")
	}
	if pending.planID != 7 {
		t.Fatalf("pending.planID = %d, want 7", pending.planID)
	}
}

func TestHistoryMutationAllowedOutsideCompaction(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	if err := a.historyMutationAllowed(0); err != nil {
		t.Fatalf("historyMutationAllowed outside compaction returned error: %v", err)
	}
}

func TestHistoryMutationAllowedRejectsFrozenHead(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.startCompactionState(1, compactionTarget{sessionEpoch: a.sessionEpoch}, compactionTrigger{Manual: true}, continuationPlan{kind: compactionResumeIdle})
	a.compactionState.headSplit = 3
	if err := a.historyMutationAllowed(2); err == nil {
		t.Fatal("historyMutationAllowed on frozen head = nil, want error")
	}
}

func TestHistoryMutationAllowedAllowsTail(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.startCompactionState(1, compactionTarget{sessionEpoch: a.sessionEpoch}, compactionTrigger{Manual: true}, continuationPlan{kind: compactionResumeIdle})
	a.compactionState.headSplit = 3
	if err := a.historyMutationAllowed(3); err != nil {
		t.Fatalf("historyMutationAllowed on tail returned error: %v", err)
	}
}

func TestHandleCompactionReadyAsyncIdleAppliesImmediately(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.ctxMgr.Append(message.Message{Role: "user", Content: "one"})
	a.ctxMgr.Append(message.Message{Role: "assistant", Content: "two"})
	a.startCompactionState(1, compactionTarget{sessionEpoch: a.sessionEpoch}, compactionTrigger{Manual: true}, continuationPlan{kind: compactionResumeIdle})
	a.compactionState.headSplit = 2

	draft := &compactionDraft{
		NewMessages:    []message.Message{{Role: "user", Content: "[Context Summary]", IsCompactionSummary: true}},
		HeadSplit:      2,
		Index:          1,
		AbsHistoryPath: filepath.Join(a.sessionDir, "history-1.md"),
		RelHistoryPath: "history-1.md",
		SummaryMode:    "truncate_only",
		PlanID:         1,
		Target:         compactionTarget{sessionEpoch: a.sessionEpoch},
		Manual:         true,
	}

	a.handleCompactionReady(Event{Type: EventCompactionReady, Payload: draft})

	if a.compactionState.readyDraft != nil {
		t.Fatal("readyDraft should be nil after idle async compaction applies immediately")
	}
	if a.compactionState.running {
		t.Fatal("compactionState.running should be false after idle async compaction apply")
	}
	msgs := a.ctxMgr.Snapshot()
	if len(msgs) != 1 || !msgs[0].IsCompactionSummary {
		t.Fatalf("messages after apply = %#v, want single compaction summary", msgs)
	}
}

func TestApplyReadyDraftClearsRunningState(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.startCompactionState(1, compactionTarget{sessionEpoch: a.sessionEpoch}, compactionTrigger{Manual: true}, continuationPlan{kind: compactionResumeIdle})
	a.compactionState.headSplit = 2
	a.compactionState.readyDraft = &compactionDraft{
		NewMessages:    []message.Message{{Role: "user", Content: "[Context Summary]", IsCompactionSummary: true}},
		HeadSplit:      2,
		Index:          1,
		AbsHistoryPath: filepath.Join(a.sessionDir, "history-1.md"),
		RelHistoryPath: "history-1.md",
		SummaryMode:    "truncate_only",
		PlanID:         1,
		Target:         compactionTarget{sessionEpoch: a.sessionEpoch},
		Manual:         true,
	}

	if !a.applyReadyDraft() {
		t.Fatal("applyReadyDraft() = false, want true")
	}
	if a.compactionState.running {
		t.Fatal("compactionState.running should be false after applyReadyDraft")
	}
	if a.IsCompactionRunning() {
		t.Fatal("IsCompactionRunning() should be false after applyReadyDraft")
	}
}

func TestHandleCompactionReadyEmitsIdleActivityOnCompletion(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.startCompactionState(1, compactionTarget{sessionEpoch: a.sessionEpoch}, compactionTrigger{}, continuationPlan{kind: compactionResumeIdle})
	draft := &compactionDraft{Skip: true, PlanID: 1, Target: compactionTarget{sessionEpoch: a.sessionEpoch}}

	a.handleCompactionReady(Event{Type: EventCompactionReady, Payload: draft})

	events := drainAgentEvents(a.Events())
	foundIdle := false
	for _, evt := range events {
		act, ok := evt.(AgentActivityEvent)
		if ok && act.AgentID == "main" && act.Type == ActivityIdle {
			foundIdle = true
			break
		}
	}
	if !foundIdle {
		t.Fatalf("expected ActivityIdle after compaction ready, got %#v", events)
	}
}

func TestHandleCompactionFailedEmitsIdleActivityOnCompletion(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.startCompactionState(1, compactionTarget{sessionEpoch: a.sessionEpoch}, compactionTrigger{}, continuationPlan{kind: compactionResumeIdle})

	a.handleCompactionFailed(Event{Type: EventCompactionFailed, Payload: &compactionFailure{planID: 1, target: compactionTarget{sessionEpoch: a.sessionEpoch}, err: fmt.Errorf("temporary compaction failure")}})

	events := drainAgentEvents(a.Events())
	foundIdle := false
	for _, evt := range events {
		act, ok := evt.(AgentActivityEvent)
		if ok && act.AgentID == "main" && act.Type == ActivityIdle {
			foundIdle = true
			break
		}
	}
	if !foundIdle {
		t.Fatalf("expected ActivityIdle after compaction failed, got %#v", events)
	}
}

func TestAutoCompactionDrainsPendingUserMessages(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.ctxMgr.SetMaxTokens(1024)
	a.gitStatusInjected.Store(true)

	// Add pending user message before auto compaction
	a.pendingUserMessages = []pendingUserMessage{{Content: "continue the task"}}

	// Start auto compaction (compactionResumeIdle means it's not during an LLM call)
	a.startCompactionState(1, compactionTarget{sessionEpoch: a.sessionEpoch}, compactionTrigger{UsageDriven: true}, continuationPlan{kind: compactionResumeIdle})

	// Create a valid compaction draft
	draft := &compactionDraft{
		NewMessages:    []message.Message{{Role: "user", Content: "[Context Summary]\n## Goal\n- continue\n\n[Context compressed]", IsCompactionSummary: true}},
		Index:          1,
		AbsHistoryPath: filepath.Join(a.sessionDir, "history-1.md"),
		RelHistoryPath: "history-1.md",
		SummaryMode:    "truncate_only",
		PlanID:         1,
		Target:         compactionTarget{sessionEpoch: a.sessionEpoch},
	}

	a.handleCompactionReady(Event{Type: EventCompactionReady, Payload: draft})

	// Verify pending user messages were drained (a new turn should have started)
	if len(a.pendingUserMessages) != 0 {
		t.Fatalf("expected pendingUserMessages to be drained, got %d", len(a.pendingUserMessages))
	}
	if a.turn == nil {
		t.Fatal("expected a new turn to be created from pending user message")
	}
}

func TestFitCompactionInputToContextLimitReturnsErrorForGrosslyOversizedPrompt(t *testing.T) {
	head := []message.Message{{Role: "user", Content: strings.Repeat("alpha ", 12000)}}
	input := &compactionInput{
		Transcript:       strings.Repeat("very large transcript ", 6000),
		EvidenceItems:    []evidenceItem{{Kind: evidenceUserCorrection, Excerpt: "do not hardcode"}},
		RecentTailAnchor: "- user: continue",
		GoalAnchor:       "- improve extraction",
		ConstraintAnchor: "- do not hardcode",
		DecisionAnchor:   "- classify issues before changing implementation",
		ProgressAnchor:   "- latest error: oldString not found",
	}
	_, err := fitCompactionInputToContextLimit(head, input, 20000, "history-1.md", nil, nil, nil, nil, compactReservedOutput)
	if err == nil {
		t.Fatal("expected oversized compaction prompt to fail fitting")
	}
}

func TestBuildCompactionInputUsesProvidedEvidenceAndTail(t *testing.T) {
	head := []message.Message{
		{Role: "user", Content: "Improve extraction quality and prioritize candidate filtering."},
		{Role: "assistant", Content: "Classify the failure source before changing prompts or rules."},
		{Role: "tool", Content: "Error: oldString not found"},
	}
	evidence := []evidenceItem{{Kind: evidenceUserCorrection, Title: "constraint", Excerpt: "do not hardcode"}}
	tail := []message.Message{{Role: "user", Content: "Continue and prioritize candidate containment handling."}}

	input, err := buildCompactionInput(head, 8192, evidence, tail)
	if err != nil {
		t.Fatalf("buildCompactionInput error: %v", err)
	}
	if got := len(input.EvidenceItems); got != 1 {
		t.Fatalf("len(EvidenceItems) = %d, want 1", got)
	}
	if input.EvidenceItems[0].Excerpt != "do not hardcode" {
		t.Fatalf("evidence excerpt = %q", input.EvidenceItems[0].Excerpt)
	}
	if len(input.RecentTail) != 1 || input.RecentTail[0].Content != tail[0].Content {
		t.Fatalf("recent tail mismatch: %+v", input.RecentTail)
	}
	if !strings.Contains(input.ConstraintAnchor, "do not hardcode") {
		t.Fatalf("constraint anchor missing provided evidence: %q", input.ConstraintAnchor)
	}
}

func TestBuildCompactionPromptIncludesDurableAnchors(t *testing.T) {
	prompt := buildCompactionPrompt(
		&compactionInput{
			Transcript:       "transcript",
			GoalAnchor:       "- improve extraction quality",
			ConstraintAnchor: "- do not hardcode",
			DecisionAnchor:   "- classify failures before choosing the next layer",
			ProgressAnchor:   "- latest error: oldString not found",
		},
		"history-1.md",
		nil,
		nil,
		nil,
	)
	for _, want := range []string{"Durable anchors extracted before summarization:", "Goal anchor:", "Constraint anchor:", "Decision anchor:", "Recent progress anchor:"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
}

func TestBuildCompactionPromptWithKeyFilesIncludesCandidates(t *testing.T) {
	prompt := buildCompactionPromptWithKeyFiles(
		&compactionInput{Transcript: "transcript"},
		"history-1.md",
		[]string{"internal/agent/compaction.go", "docs/architecture/context-management.md"},
		nil,
		nil,
		nil,
	)
	if !strings.Contains(prompt, "Key file candidates:") {
		t.Fatalf("prompt missing key file candidates section:\n%s", prompt)
	}
	if !strings.Contains(prompt, "internal/agent/compaction.go") {
		t.Fatalf("prompt missing compaction key file candidate:\n%s", prompt)
	}
}

func TestSummarizeCompactionHeadDoesNotRetryWeakSummary(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
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
		response: &message.Response{Content: "too short"},
	}
	client := llm.NewClient(providerCfg, provider, "compact-model", 2048, "")
	a.SetModelSwitchFactory(func(providerModel string) (*llm.Client, string, int, error) {
		if providerModel != "sample/compact-model" {
			t.Fatalf("providerModel = %q, want sample/compact-model", providerModel)
		}
		return client, "compact-model", 16384, nil
	})

	head := []message.Message{
		{Role: "user", Content: "Please continue working on the current task."},
		{Role: "assistant", Content: "I will inspect the current implementation and summarize next steps."},
	}
	_, _, err := a.summarizeCompactionHead(head, "history-1.md")
	if err == nil {
		t.Fatal("expected weak summary validation error")
	}
	if provider.calls != 1 {
		t.Fatalf("provider Complete calls = %d, want 1", provider.calls)
	}
}

func TestSummarizeCompactionHeadUsesCompactEndpointForCodexPreset(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.SetProviderModelRef("sample/compact-model")

	providerCfg := llm.NewProviderConfig("sample", config.ProviderConfig{
		Type:   "stub",
		Preset: config.ProviderPresetCodex,
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
	a.SetModelSwitchFactory(func(providerModel string) (*llm.Client, string, int, error) {
		if providerModel != "sample/compact-model" {
			t.Fatalf("providerModel = %q, want sample/compact-model", providerModel)
		}
		return client, "compact-model", 16384, nil
	})

	head := []message.Message{
		{Role: "user", Content: "Please continue working on the current task."},
		{Role: "assistant", Content: "I will inspect the current implementation and summarize next steps."},
	}
	got, _, err := a.summarizeCompactionHead(head, "history-1.md")
	if err != nil {
		t.Fatalf("summarizeCompactionHead error: %v", err)
	}
	if provider.compactCalls != 1 {
		t.Fatalf("provider Compact calls = %d, want 1", provider.compactCalls)
	}
	if provider.calls != 0 {
		t.Fatalf("provider Complete calls = %d, want 0", provider.calls)
	}
	if !strings.Contains(got, "history-1.md") {
		t.Fatalf("summary should contain archive reference, got:\n%s", got)
	}
}

func TestSummarizeCompactionHeadUsesGenericBackendWhenCompactionPresetIsGeneric(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.globalConfig.Context.Compaction.Preset = config.CompactionPresetGeneric
	a.SetProviderModelRef("sample/compact-model")

	providerCfg := llm.NewProviderConfig("sample", config.ProviderConfig{
		Type:   "stub",
		Preset: config.ProviderPresetCodex,
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
	a.SetModelSwitchFactory(func(providerModel string) (*llm.Client, string, int, error) {
		return client, "compact-model", 16384, nil
	})

	_, _, err := a.summarizeCompactionHead([]message.Message{
		{Role: "user", Content: "Please continue working on the current task."},
		{Role: "assistant", Content: "I will inspect the current implementation and summarize next steps."},
	}, "history-1.md")
	if err != nil {
		t.Fatalf("summarizeCompactionHead error: %v", err)
	}
	if provider.calls != 1 {
		t.Fatalf("provider Complete calls = %d, want 1", provider.calls)
	}
	if provider.compactCalls != 0 {
		t.Fatalf("provider Compact calls = %d, want 0", provider.compactCalls)
	}
}

func TestSummarizeCompactionHeadCodexPresetFallsBackToGenericWhenEndpointUnavailable(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.globalConfig.Context.Compaction.Preset = config.CompactionPresetCodex
	a.SetProviderModelRef("sample/compact-model")

	providerCfg := llm.NewProviderConfig("sample", config.ProviderConfig{
		Type:   "stub",
		Preset: config.ProviderPresetCodex,
		Models: map[string]config.ModelConfig{
			"compact-model": {
				Limit: config.ModelLimit{Context: 16384, Output: 2048},
			},
		},
	}, []string{"test-key"})
	provider := &countingSummaryOnlyProvider{
		response: &message.Response{Content: validCompactionSummaryForTest("history-1.md")},
	}
	client := llm.NewClient(providerCfg, provider, "compact-model", 2048, "")
	a.SetModelSwitchFactory(func(providerModel string) (*llm.Client, string, int, error) {
		return client, "compact-model", 16384, nil
	})

	_, _, err := a.summarizeCompactionHead([]message.Message{
		{Role: "user", Content: "Please continue working on the current task."},
		{Role: "assistant", Content: "I will inspect the current implementation and summarize next steps."},
	}, "history-1.md")
	if err != nil {
		t.Fatalf("summarizeCompactionHead error: %v", err)
	}
	if provider.calls != 1 {
		t.Fatalf("provider Complete calls = %d, want 1", provider.calls)
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
	a.SetModelSwitchFactory(func(providerModel string) (*llm.Client, string, int, error) {
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
	draft, err := a.produceCompactionDraft(snapshot, false, 1, compactionTarget{sessionEpoch: a.sessionEpoch})
	if err != nil {
		t.Fatalf("produceCompactionDraft error: %v", err)
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
}

func TestFormatSubAgentsForPromptOmitsCompletedTasksAndTruncates(t *testing.T) {
	longDesc := strings.Repeat("desc ", 120)
	longSummary := strings.Repeat("summary ", 120)
	got := formatSubAgentsForPrompt([]SubAgentInfo{
		{
			InstanceID:   "coder-1",
			TaskID:       "adhoc-1",
			State:        string(SubAgentStateRunning),
			AgentDefName: "coder",
			SelectedRef:  "provider/model",
			TaskDesc:     longDesc,
			LastSummary:  longSummary,
		},
		{
			InstanceID:   "coder-2",
			TaskID:       "adhoc-2",
			State:        string(SubAgentStateCompleted),
			AgentDefName: "coder",
			SelectedRef:  "provider/model",
			TaskDesc:     "completed task",
			LastSummary:  "done",
		},
	})
	if strings.Contains(got, "coder-2") {
		t.Fatalf("completed task should be omitted from prompt:\n%s", got)
	}
	if strings.Contains(got, longDesc) {
		t.Fatalf("long task description should be truncated:\n%s", got)
	}
	if strings.Contains(got, longSummary) {
		t.Fatalf("long last summary should be truncated:\n%s", got)
	}
	if !strings.Contains(got, "omitted") {
		t.Fatalf("expected omitted-task note in prompt:\n%s", got)
	}
}

func TestFormatSubAgentsAsBulletsReportsNoActiveTasksWhenOnlyHistoricalRemain(t *testing.T) {
	got := formatSubAgentsAsBullets([]SubAgentInfo{{
		InstanceID:   "coder-2",
		TaskID:       "adhoc-2",
		State:        string(SubAgentStateCompleted),
		AgentDefName: "coder",
		SelectedRef:  "provider/model",
		TaskDesc:     "completed task",
		LastSummary:  "done",
	}})
	if !strings.Contains(got, "none active") || !strings.Contains(got, "omitted") {
		t.Fatalf("unexpected historical-only summary: %q", got)
	}
}

func TestEnsureCompactionSummaryKeyFilesAppendsMissingPaths(t *testing.T) {
	summary := "## Goal\n- continue\n\n## User Constraints\n- none\n\n## Progress\n- progress\n\n## Key Decisions\n- decisions\n\n## Files and Evidence\n- Archived history: history-1.md\n\n## Todo State\n- none\n\n## SubAgent State\n- none\n\n## Open Problems\n- none\n\n## Next Step\n- continue"
	got := ensureCompactionSummaryKeyFiles(summary, []string{"internal/agent/compaction.go", "docs/architecture/context-management.md"})
	if !strings.Contains(got, "- internal/agent/compaction.go") {
		t.Fatalf("summary missing injected key file:\n%s", got)
	}
	if !strings.Contains(got, "- docs/architecture/context-management.md") {
		t.Fatalf("summary missing injected docs file:\n%s", got)
	}
}

func TestEnsureCompactionSummaryKeyFilesIgnoresProseMentionWhenCheckingExistingBullets(t *testing.T) {
	summary := "## Goal\n- continue\n\n## User Constraints\n- none\n\n## Progress\n- progress\n\n## Key Decisions\n- decisions\n\n## Files and Evidence\n- Archived history: history-1.md\n- See internal/agent/compaction.go for the latest draft context.\n\n## Todo State\n- none\n\n## SubAgent State\n- none\n\n## Open Problems\n- none\n\n## Next Step\n- continue"
	got := ensureCompactionSummaryKeyFiles(summary, []string{"internal/agent/compaction.go"})
	if !strings.Contains(got, "\n- internal/agent/compaction.go\n") {
		t.Fatalf("summary should still inject standalone bullet for key file:\n%s", got)
	}
}

func TestExtractCompactionKeyFilesIgnoresInvalidLines(t *testing.T) {
	projectRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projectRoot, "internal", "agent"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(projectRoot, "docs", "architecture"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectRoot, "internal", "agent", "compaction.go"), []byte("package agent\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectRoot, "docs", "architecture", "context-management.md"), []byte("# doc\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	content := buildCompactionCheckpointMessage(
		"## Goal\n- continue\n\n## User Constraints\n- none\n\n## Progress\n- progress\n\n## Key Decisions\n- decisions\n\n## Files and Evidence\n- Archived history: history-1.md\n- internal/agent/compaction.go\n- `docs/architecture/context-management.md`\n- the key file is internal/tui/app.go because ...\n\n## Todo State\n- none\n\n## SubAgent State\n- none\n\n## Open Problems\n- none\n\n## Next Step\n- continue",
		[]string{".chord/sessions/test/history-1.md"},
		"model_summary",
		nil,
	)
	got := extractCompactionKeyFiles(content, projectRoot)
	if len(got) != 2 {
		t.Fatalf("len(got) = %d, want 2 (%v)", len(got), got)
	}
	if got[0] != "internal/agent/compaction.go" || got[1] != "docs/architecture/context-management.md" {
		t.Fatalf("got = %#v", got)
	}
}

func TestBuildTruncateOnlySummaryIncludesFilesAndEvidenceSection(t *testing.T) {
	summary := buildTruncateOnlySummary(
		"history-1.md",
		fmt.Errorf("utility model unavailable"),
		[]string{"internal/agent/compaction.go"},
		nil,
		nil,
		nil,
	)
	for _, heading := range compactionRequiredHeadings {
		if !strings.Contains(summary, heading) {
			t.Fatalf("truncate-only summary missing heading %q:\n%s", heading, summary)
		}
	}
	if !strings.Contains(summary, "- internal/agent/compaction.go") {
		t.Fatalf("truncate-only summary missing key file bullet:\n%s", summary)
	}
}

func TestExtractCompactionKeyFilesFromTruncateOnlySummary(t *testing.T) {
	projectRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projectRoot, "internal", "agent"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectRoot, "internal", "agent", "compaction.go"), []byte("package agent\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	summary := buildTruncateOnlySummary(
		"history-1.md",
		fmt.Errorf("utility model unavailable"),
		[]string{"internal/agent/compaction.go"},
		nil,
		nil,
		nil,
	)
	content := buildCompactionCheckpointMessage(summary, []string{".chord/sessions/test/history-1.md"}, "truncate_only", nil)
	got := extractCompactionKeyFiles(content, projectRoot)
	if len(got) != 1 || got[0] != "internal/agent/compaction.go" {
		t.Fatalf("got = %#v, want internal/agent/compaction.go", got)
	}
}

func TestInjectCompactionFileContextOnlyOncePerCheckpoint(t *testing.T) {
	projectRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projectRoot, "internal", "agent"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	path := filepath.Join(projectRoot, "internal", "agent", "compaction.go")
	if err := os.WriteFile(path, []byte("package agent\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	a := newTestMainAgent(t, projectRoot)
	summary := buildCompactionCheckpointMessage(
		"## Goal\n- continue\n\n## User Constraints\n- none\n\n## Progress\n- progress\n\n## Key Decisions\n- decisions\n\n## Files and Evidence\n- Archived history: history-1.md\n- internal/agent/compaction.go\n\n## Todo State\n- none\n\n## SubAgent State\n- none\n\n## Open Problems\n- none\n\n## Next Step\n- continue",
		[]string{".chord/sessions/test/history-1.md"},
		"model_summary",
		nil,
	)
	msgs := []message.Message{
		{Role: "user", IsCompactionSummary: true, Content: summary},
		{Role: "user", Content: "continue"},
	}
	first := a.injectCompactionFileContext(msgs)
	if len(first) != 3 {
		t.Fatalf("len(first) = %d, want 3", len(first))
	}
	if len(first[1].Parts) == 0 || !strings.Contains(first[1].Parts[0].Text, "Automatically loaded key files") {
		t.Fatalf("injected message parts = %#v", first[1].Parts)
	}
	second := a.injectCompactionFileContext(msgs)
	if len(second) != 2 {
		t.Fatalf("len(second) = %d, want 2 after one-shot consumption", len(second))
	}
}

func TestInjectCompactionFileContextHonorsByteBudgets(t *testing.T) {
	projectRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projectRoot, "pkg"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	var keyFiles []string
	for i := 0; i < 5; i++ {
		rel := filepath.Join("pkg", fmt.Sprintf("f%d.txt", i))
		abs := filepath.Join(projectRoot, rel)
		if err := os.WriteFile(abs, []byte(strings.Repeat("abcdefghij\n", 2048)), 0o644); err != nil {
			t.Fatalf("WriteFile(%d): %v", i, err)
		}
		keyFiles = append(keyFiles, filepath.ToSlash(rel))
	}

	var filesSection strings.Builder
	filesSection.WriteString("- Archived history: history-1.md\n")
	for _, rel := range keyFiles {
		filesSection.WriteString("- ")
		filesSection.WriteString(rel)
		filesSection.WriteByte('\n')
	}
	summary := buildCompactionCheckpointMessage(
		"## Goal\n- continue\n\n## User Constraints\n- none\n\n## Progress\n- progress\n\n## Key Decisions\n- decisions\n\n## Files and Evidence\n"+strings.TrimRight(filesSection.String(), "\n")+"\n\n## Todo State\n- none\n\n## SubAgent State\n- none\n\n## Open Problems\n- none\n\n## Next Step\n- continue",
		[]string{".chord/sessions/test/history-1.md"},
		"model_summary",
		nil,
	)
	a := newTestMainAgent(t, projectRoot)
	msgs := []message.Message{
		{Role: "user", IsCompactionSummary: true, Content: summary},
		{Role: "user", Content: "continue"},
	}
	got := a.injectCompactionFileContext(msgs)
	if len(got) != 3 {
		t.Fatalf("len(got) = %d, want 3", len(got))
	}
	parts := got[1].Parts
	if len(parts) != 6 {
		t.Fatalf("len(parts) = %d, want 6 (system + 4 files + omission note)", len(parts))
	}
	if !strings.Contains(parts[len(parts)-1].Text, "additional files omitted") {
		t.Fatalf("expected omission note, got %q", parts[len(parts)-1].Text)
	}
	for i := 1; i < len(parts)-1; i++ {
		if !strings.Contains(parts[i].Text, "showing first 12 KB only") {
			t.Fatalf("expected per-file truncation note in part %d, got %q", i, parts[i].Text)
		}
	}
}

func TestBuildCompactionPromptIncludesBackgroundObjects(t *testing.T) {
	prompt := buildCompactionPrompt(
		&compactionInput{Transcript: "transcript"},
		"history-1.md",
		nil,
		nil,
		[]recovery.BackgroundObjectState{{
			ID:            "job-1",
			AgentID:       "builder-2",
			Kind:          "job",
			Description:   "Run production build",
			Command:       "npm test --watch",
			StartedAt:     time.Unix(1700000000, 0),
			MaxRuntimeSec: 300,
			Status:        "running",
		}},
	)
	if !strings.Contains(prompt, "Current background objects:") {
		t.Fatalf("prompt missing background objects section:\n%s", prompt)
	}
	if !strings.Contains(prompt, "job-1") || !strings.Contains(prompt, "Run production build") {
		t.Fatalf("prompt missing background object details:\n%s", prompt)
	}
	if !strings.Contains(prompt, "agent=builder-2") {
		t.Fatalf("prompt missing background object agent routing info:\n%s", prompt)
	}
	if !strings.Contains(prompt, "kind=job") {
		t.Fatalf("prompt missing background object kind:\n%s", prompt)
	}
	if !strings.Contains(prompt, "max_runtime=300s") {
		t.Fatalf("prompt missing background object max runtime:\n%s", prompt)
	}
}

func TestHandleCompactionReadyIgnoresStaleSessionDraft(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.startCompactionState(2, compactionTarget{sessionEpoch: 2, turnEpoch: 1}, compactionTrigger{}, continuationPlan{kind: compactionResumeIdle})
	a.sessionEpoch = 2
	a.turnEpoch = 1

	draft := &compactionDraft{
		PlanID:         1,
		Target:         compactionTarget{sessionEpoch: 1},
		NewMessages:    []message.Message{{Role: "user", Content: "[Context Summary]\nsummary"}},
		Index:          1,
		AbsHistoryPath: "/tmp/history-1.md",
		RelHistoryPath: "history-1.md",
		SummaryMode:    "truncate_only",
		ModelRef:       "fallback",
	}
	a.handleCompactionReady(Event{Type: EventCompactionReady, Payload: draft})

	if !a.IsCompactionRunning() {
		t.Fatal("stale draft should not clear compaction state")
	}
	if got := len(a.ctxMgr.Snapshot()); got != 0 {
		t.Fatalf("len(snapshot) = %d, want 0 for stale ready event", got)
	}
}

func TestExportCompactionHistoryMetaPendingThenApplied(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	msgs := []message.Message{{Role: "user", Content: "hello"}}

	absPath, relPath, err := a.exportCompactionHistory(msgs, 1)
	if err != nil {
		t.Fatalf("exportCompactionHistory: %v", err)
	}
	metaPath := compactionHistoryMetaPath(absPath)
	data, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatalf("read meta: %v", err)
	}
	var meta compactionHistoryMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		t.Fatalf("unmarshal meta: %v", err)
	}
	if meta.Status != compactionHistoryPending {
		t.Fatalf("pending meta status = %q, want %q", meta.Status, compactionHistoryPending)
	}
	exportedAt := meta.ExportedAt
	if exportedAt.IsZero() {
		t.Fatal("expected ExportedAt to be set in pending meta")
	}

	draft := &compactionDraft{
		PlanID:             1,
		Target:             compactionTarget{sessionEpoch: a.sessionEpoch, turnEpoch: a.turnEpoch},
		NewMessages:        []message.Message{{Role: "user", Content: "[Context Summary]\nsummary"}},
		Index:              1,
		AbsHistoryPath:     absPath,
		AbsHistoryMetaPath: metaPath,
		RelHistoryPath:     relPath,
		SummaryMode:        "structured_fallback",
		ModelRef:           "fallback",
	}
	if err := a.applyCompactionDraft(draft); err != nil {
		t.Fatalf("applyCompactionDraft: %v", err)
	}

	data, err = os.ReadFile(metaPath)
	if err != nil {
		t.Fatalf("read updated meta: %v", err)
	}
	if err := json.Unmarshal(data, &meta); err != nil {
		t.Fatalf("unmarshal updated meta: %v", err)
	}
	if meta.Status != compactionHistoryApplied {
		t.Fatalf("applied meta status = %q, want %q", meta.Status, compactionHistoryApplied)
	}
	if !meta.ExportedAt.Equal(exportedAt) {
		t.Fatalf("ExportedAt = %v, want preserved %v", meta.ExportedAt, exportedAt)
	}
	if meta.AppliedAt.IsZero() {
		t.Fatal("expected AppliedAt to be set")
	}
}

func TestSpawnFinishedEventHandledImmediatelyDuringCompaction(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.startCompactionState(1, compactionTarget{sessionEpoch: a.sessionEpoch}, compactionTrigger{}, continuationPlan{kind: compactionResumeIdle})
	payload := &tools.SpawnFinishedPayload{BackgroundID: "job-1", AgentID: a.instanceID, Kind: "job", Status: "finished (exit 0)", Message: "background finished"}
	a.dispatch(Event{Type: EventSpawnFinished, SourceID: "main", Payload: payload})

	// Phase B: events are no longer queued behind compaction.
	// With no active turn, spawn-finished starts a new turn immediately.
	if a.turn == nil {
		t.Fatal("expected spawn-finished to start a turn immediately during compaction")
	}
	if got := len(a.pendingUserMessages); got != 0 {
		t.Fatalf("len(pendingUserMessages) = %d, want 0", got)
	}
}

func TestCompactionFailureDoesNotRetrySameGate(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.gitStatusInjected.Store(true)
	a.ctxMgr.SetMaxTokens(1000)
	a.newTurn()
	if a.turn == nil {
		t.Fatal("expected active turn")
	}
	turnID := a.turn.ID
	turnEpoch := a.turn.Epoch
	a.pendingUserMessages = []pendingUserMessage{{Content: "queued after failure"}}
	a.startCompactionState(1, compactionTarget{turnID: turnID, turnEpoch: turnEpoch, sessionEpoch: a.sessionEpoch}, compactionTrigger{UsageDriven: true}, continuationPlan{kind: compactionResumeMainLLM, turnID: turnID, turnEpoch: turnEpoch, agentErrSourceID: "main"})

	a.handleCompactionFailed(Event{Type: EventCompactionFailed, TurnID: turnID, Payload: &compactionFailure{planID: 1, target: compactionTarget{turnID: turnID, turnEpoch: turnEpoch, sessionEpoch: a.sessionEpoch}, err: fmt.Errorf("temporary compaction failure")}})

	if a.IsCompactionRunning() {
		t.Fatal("expected same gate not to be retried immediately after compaction failure")
	}
	if a.turn == nil || a.turn.ID != turnID || a.turn.Epoch != turnEpoch {
		t.Fatalf("turn = %+v, want active original turn %d/%d", a.turn, turnID, turnEpoch)
	}
	if got := len(a.pendingUserMessages); got != 0 {
		t.Fatalf("len(pendingUserMessages) = %d, want 0 after queued input merge", got)
	}
	msgs := a.ctxMgr.Snapshot()
	if len(msgs) == 0 || msgs[len(msgs)-1].Content != "queued after failure" {
		t.Fatalf("expected deferred user message to be merged into context after failure resume, got %+v", msgs)
	}
}

func TestUsageDrivenFailureCanRetryAcrossTurnsBeforeBreakerTrips(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.ctxMgr = ctxmgr.NewManager(10000, true, 0.9)
	a.autoCompactRequested.Store(true)

	a.startCompactionState(1, compactionTarget{sessionEpoch: a.sessionEpoch}, compactionTrigger{UsageDriven: true}, continuationPlan{kind: compactionResumeIdle})
	a.handleCompactionFailed(Event{Type: EventCompactionFailed, Payload: &compactionFailure{planID: 1, target: compactionTarget{sessionEpoch: a.sessionEpoch}, err: fmt.Errorf("temporary compaction failure")}})

	if a.isUsageDrivenAutoCompactSuppressed() {
		t.Fatal("expected breaker to stay open before reaching failure threshold")
	}
	if !a.autoCompactRequested.Load() {
		t.Fatal("expected usage-driven auto compact request to remain armed before breaker trips")
	}
	if !a.shouldDurableCompactBeforeMainLLM() {
		t.Fatal("expected another turn to retry usage-driven compaction before breaker trips")
	}
}

func TestUsageDrivenFailureStopsRetriyingAfterBreakerTrips(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.ctxMgr = ctxmgr.NewManager(10000, true, 0.9)
	a.autoCompactRequested.Store(true)

	for planID := uint64(1); planID <= usageDrivenCompactionFailureThreshold; planID++ {
		a.startCompactionState(planID, compactionTarget{sessionEpoch: a.sessionEpoch}, compactionTrigger{UsageDriven: true}, continuationPlan{kind: compactionResumeIdle})
		a.handleCompactionFailed(Event{Type: EventCompactionFailed, Payload: &compactionFailure{planID: planID, target: compactionTarget{sessionEpoch: a.sessionEpoch}, err: fmt.Errorf("temporary compaction failure")}})
	}

	if !a.isUsageDrivenAutoCompactSuppressed() {
		t.Fatal("expected breaker to suppress usage-driven auto compaction after threshold")
	}
	if a.shouldDurableCompactBeforeMainLLM() {
		t.Fatal("expected usage-driven compaction to stop retrying after breaker trips")
	}
}

func TestUsageDrivenCompactionSkipsAfterPreRequestShrink(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.ctxMgr = ctxmgr.NewManager(8000, true, 0.8)
	a.autoCompactRequested.Store(true)
	a.ctxMgr.SetSystemPrompt(message.Message{Role: "system", Content: ""})
	snapshot := []message.Message{
		{Role: "user", Content: "u1"},
		{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "tc1", Name: "Read", Args: json.RawMessage(`{"path":"a.go"}`)}}},
		{Role: "tool", ToolCallID: "tc1", Content: strings.Repeat("very old read output ", 800)},
		{Role: "user", Content: "u2"},
		{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "tc2", Name: "Read", Args: json.RawMessage(`{"path":"a.go"}`)}}},
		{Role: "tool", ToolCallID: "tc2", Content: "new read output"},
		{Role: "user", Content: "u3"},
		{Role: "user", Content: "u4"},
		{Role: "user", Content: "u5"},
		{Role: "user", Content: "u6"},
		{Role: "user", Content: "u7"},
		{Role: "user", Content: "continue"},
	}

	if !a.trySkipUsageDrivenCompactionAfterShrink(snapshot) {
		t.Fatal("expected shrink stage to skip usage-driven durable compaction")
	}
	if a.autoCompactRequested.Load() {
		t.Fatal("expected usage-driven auto compact request to be cleared after shrink skip")
	}
}

func TestUsageDrivenCompactionStillNeededWhenShrinkEstimateRemainsHigh(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.ctxMgr = ctxmgr.NewManager(1000, true, 0.8)
	a.autoCompactRequested.Store(true)
	a.ctxMgr.SetSystemPrompt(message.Message{Role: "system", Content: strings.Repeat("system ", 120)})
	snapshot := []message.Message{
		{Role: "user", Content: strings.Repeat("keep a lot of context ", 200)},
		{Role: "assistant", Content: strings.Repeat("recent detailed answer ", 180)},
		{Role: "user", Content: strings.Repeat("follow-up ", 160)},
	}

	if a.trySkipUsageDrivenCompactionAfterShrink(snapshot) {
		t.Fatal("expected durable compaction to remain armed when shrink estimate stays high")
	}
	if !a.autoCompactRequested.Load() {
		t.Fatal("expected auto compact request to remain armed")
	}
}

func TestLargeTranscriptDoesNotAutoCompactWithoutUsageSignal(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.ctxMgr = ctxmgr.NewManager(1000, true, 0.5)
	a.ctxMgr.RestoreMessages([]message.Message{
		{Role: "user", Content: strings.Repeat("large prompt ", 400)},
		{Role: "assistant", Content: strings.Repeat("large answer ", 400)},
		{Role: "user", Content: strings.Repeat("follow-up ", 400)},
	})

	if a.shouldDurableCompactBeforeMainLLM() {
		t.Fatal("expected automatic compaction to require a usage-driven signal even for a large transcript")
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
	head, evidence := a.splitMessagesForCompaction(msgs, 4096)
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

func TestRecordEvidenceFromMessageCapturesToolErrorAndDiff(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.recordEvidenceFromMessage(message.Message{Role: "tool", Content: "go test failed\n\nError: exit code 1", ToolDiff: "+ changed\n- old"})
	if got := len(a.evidenceCandidates); got != 2 {
		t.Fatalf("len(evidenceCandidates) = %d, want 2", got)
	}
}

func TestSplitMessagesForCompaction_AllowsSummaryOnlyWhenNoEvidence(t *testing.T) {
	msgs := []message.Message{
		{Role: "user", Content: "Implement feature X."},
		{Role: "assistant", Content: "Done."},
		{Role: "user", Content: "Thanks."},
		{Role: "assistant", Content: "You're welcome."},
	}

	head, evidence := splitMessagesForCompaction(msgs, 4096)
	if len(head) == 0 {
		t.Fatal("expected non-empty archived head")
	}
	if len(evidence) != 0 {
		t.Fatalf("expected no evidence artifact, got %+v", evidence)
	}
}

func TestCompactionInputBudgetUsesOneSixthOfContext(t *testing.T) {
	// Verify that compaction input budget uses 1/6 of context window
	// to reduce transcript size and speed up model calls.
	// The budget is calculated as: contextLimit - overhead - reservedOutput - preflightBuffer,
	// then max'd with contextLimit/6.
	for _, tc := range []struct {
		contextLimit int
		minExpected  int
	}{
		{200000, 33333}, // 200K context -> at least 33K budget (1/6)
		{128000, 21333}, // 128K context -> at least 21K budget
		{32768, 5461},   // 32K context -> at least 5K budget
	} {
		budget := compactionInputBudget(tc.contextLimit)
		if budget < tc.minExpected {
			t.Errorf("contextLimit=%d: budget=%d, want at least %d", tc.contextLimit, budget, tc.minExpected)
		}
		// Budget should be reasonable - not more than contextLimit minus overheads
		if budget > tc.contextLimit-8192 {
			t.Errorf("contextLimit=%d: budget=%d is too large (should leave room for overhead)", tc.contextLimit, budget)
		}
	}
}

func TestRewriteSessionAfterCompactionPreservesOriginalFirstUserMessage(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)

	// Set up initial first user message via usage ledger
	if a.usageLedger == nil {
		t.Fatal("newTestMainAgent should initialize usageLedger")
	}
	if err := a.usageLedger.SetFirstUserMessage("Original user request for feature X"); err != nil {
		t.Fatalf("SetFirstUserMessage: %v", err)
	}

	// Simulate compaction creating a summary message
	summaryContent := "[Context Summary]\n## Goal\n- continue\n\n[Context compressed]"
	compactionMsg := message.Message{
		Role:                "user",
		Content:             summaryContent,
		IsCompactionSummary: true,
	}

	// Rewrite session with compaction summary as first message
	_, err := a.rewriteSessionAfterCompaction(1, []message.Message{compactionMsg})
	if err != nil {
		t.Fatalf("rewriteSessionAfterCompaction: %v", err)
	}

	// Verify OriginalFirstUserMessage is preserved
	summary, err := a.usageLedger.Summary()
	if err != nil {
		t.Fatalf("Summary: %v", err)
	}
	if summary.OriginalFirstUserMessage != "Original user request for feature X" {
		t.Errorf("OriginalFirstUserMessage = %q, want %q", summary.OriginalFirstUserMessage, "Original user request for feature X")
	}
	// FirstUserMessage should be updated to reflect compaction summary
	if summary.FirstUserMessage == "" {
		t.Error("FirstUserMessage should not be empty after compaction")
	}
}
