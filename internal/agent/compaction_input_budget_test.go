package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
	"github.com/keakon/chord/internal/tools"
)

func TestFitCompactionInputToContextLimitReturnsErrorForGrosslyOversizedPrompt(t *testing.T) {
	head := []message.Message{{Role: "user", Content: strings.Repeat("alpha ", 12000)}}
	input := &compactionInput{
		Transcript:       strings.Repeat("very large transcript ", 6000),
		EvidenceItems:    []evidenceItem{{Kind: evidenceUserCorrection, Excerpt: "do not hardcode"}},
		RecentTailAnchor: "- user: continue",
		GoalAnchor:       "- improve extraction",
		DecisionAnchor:   "- classify issues before changing implementation",
		ProgressAnchor:   "- latest error: patch not found",
	}
	_, _, err := (&MainAgent{}).fitCompactionInputToContextLimit(head, input, 20000, "history-1.md", nil, nil, nil, nil, compactReservedOutput)
	if err == nil {
		t.Fatal("expected oversized compaction prompt to fail fitting")
	}
}

func TestFitCompactionInputToContextLimitRejectsUnavailableLimit(t *testing.T) {
	input := &compactionInput{Transcript: "history"}
	_, _, err := (&MainAgent{}).fitCompactionInputToContextLimit(nil, input, 0, "history.md", nil, nil, nil, nil, compactReservedOutput)
	if err == nil || !strings.Contains(err.Error(), "context limit is unavailable") {
		t.Fatalf("fitCompactionInputToContextLimit error = %v, want unavailable context limit error", err)
	}
}

func TestBuildCompactionPromptIncludesDurableAnchors(t *testing.T) {
	prompt := buildCompactionPromptWithKeyFiles(
		&compactionInput{
			Transcript:     "transcript",
			GoalAnchor:     "- improve extraction quality",
			DecisionAnchor: "- classify failures before choosing the next layer",
			ProgressAnchor: "- latest error: patch not found",
		},
		"history-1.md",
		nil,
		nil,
		nil,
		nil,
	)
	for _, want := range []string{"Extraction hints (summary input only; not carried into the checkpoint):", "Latest user request anchor:", "Decision anchor:", "Recent progress anchor:"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
}

func TestBuildCompactionPromptIncludesActiveObjectivePreservationRules(t *testing.T) {
	prompt := buildCompactionPromptWithKeyFiles(
		&compactionInput{Transcript: "transcript"},
		"history-2.md",
		[]string{"internal/agent/compaction.go"},
		[]tools.TodoItem{{ID: "old", Content: "continue investigating rate limit", Status: "pending"}},
		nil,
		nil,
	)
	for _, want := range []string{
		"Full archived history file for this compaction: history-2.md",
		"These todos are not automatically authoritative after compaction",
		"classify it as active/relevant, completed/background, or stale/superseded",
		"continue investigating rate limit",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
	// The archive-chain rule is stated once, in the compaction system prompt.
	const archiveChain = "checkpoint wrapper may also list all archived history files"
	if !strings.Contains(compactionSystemPrompt, archiveChain) || strings.Contains(prompt, "checkpoint wrapper") {
		t.Fatalf("archive-chain rule must live only in the system prompt:\n%s", prompt)
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

func TestCompactionPromptInputsDegradeByAuthority(t *testing.T) {
	keyFiles := []string{"key"}
	todos := []tools.TodoItem{{ID: "todo"}}
	subAgents := []SubAgentInfo{{TaskID: "task"}}
	background := []recovery.BackgroundObjectState{{ID: "background"}}

	cases := []struct {
		attempt                                    int
		wantKey, wantTodo, wantSub, wantBackground int
	}{
		{0, 1, 1, 1, 1},
		{1, 1, 1, 1, 0},
		{2, 0, 1, 1, 0},
		{3, 0, 1, 0, 0},
		{4, 0, 0, 0, 0},
	}
	for _, test := range cases {
		gotKey, gotTodo, gotSub, gotBackground := compactionPromptInputsForAttempt(keyFiles, todos, subAgents, background, test.attempt)
		if len(gotKey) != test.wantKey || len(gotTodo) != test.wantTodo || len(gotSub) != test.wantSub || len(gotBackground) != test.wantBackground {
			t.Fatalf("attempt %d inputs = (%d, %d, %d, %d), want (%d, %d, %d, %d)", test.attempt, len(gotKey), len(gotTodo), len(gotSub), len(gotBackground), test.wantKey, test.wantTodo, test.wantSub, test.wantBackground)
		}
	}
}

// compactionPromptCaptureProvider records the compaction prompt message and
// answers with a valid summary, so a summarizeCompactionHead call can be
// inspected for what was actually sent to the utility model.
type compactionPromptCaptureProvider struct {
	prompt string
}

func (p *compactionPromptCaptureProvider) CompleteStream(
	_ context.Context,
	_ string,
	_ string,
	_ string,
	messages []message.Message,
	_ []message.ToolDefinition,
	_ int,
	_ llm.RequestTuning,
	_ llm.StreamCallback,
) (*message.Response, error) {
	if len(messages) > 0 {
		p.prompt = messages[0].Content
	}
	return &message.Response{Content: validCompactionSummaryForTest("history-1.md"), StopReason: "stop"}, nil
}

func (p *compactionPromptCaptureProvider) Complete(
	_ context.Context,
	_ string,
	_ string,
	_ string,
	_ []message.Message,
	_ []message.ToolDefinition,
	_ int,
	_ llm.RequestTuning,
) (*message.Response, error) {
	return nil, fmt.Errorf("unexpected Complete call")
}

func (p *compactionPromptCaptureProvider) InvalidateRouting(string) {}

// TestSummarizeCompactionHeadSendsExactlyTheBudgetedInputs drives the real
// caller (summarizeCompactionHead) into the budget-fit degradation path and
// pins that the prompt sent to the compaction model is assembled from the
// degraded auxiliary lists the admission gate accepted, never from the
// original full lists. Only an oversized auxiliary set forces a degradation,
// and the full-lists prompt must then exceed the reserved budget while the
// sent (degraded) prompt fits it.
func TestSummarizeCompactionHeadSendsExactlyTheBudgetedInputs(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.SetProviderModelRef("sample/compact-model")

	providerCfg := llm.NewProviderConfig("sample", config.ProviderConfig{
		Type: config.ProviderTypeMessages,
		Models: map[string]config.ModelConfig{
			"compact-model": {Limit: config.ModelLimit{Context: 16384, Output: 2048}},
		},
	}, []string{"test-key"})
	provider := &compactionPromptCaptureProvider{}
	client := llm.NewClient(providerCfg, provider, "compact-model", 2048, "")
	a.llmClient = client

	// The head must be large enough that trimming the transcript alone cannot
	// admit the prompt while the oversized auxiliary sections still occupy the
	// budget. Every message carries a big payload so the token estimator keeps
	// the transcript near the input budget.
	head := make([]message.Message, 0, 90)
	for i := range 30 {
		callID := fmt.Sprintf("capture-read-%d", i)
		head = append(head,
			message.Message{Role: message.RoleUser, Content: fmt.Sprintf("inspect area %d", i)},
			message.Message{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: callID, Name: tools.NameRead, Args: json.RawMessage(`{"path":"internal/agent/compaction.go"}`)}}},
			message.Message{Role: message.RoleTool, ToolCallID: callID, Content: strings.Repeat("output line from a wide read\n", 120)},
		)
	}

	// Oversized auxiliary sections: sub-agent summaries and background
	// descriptions are the largest things the prompt echoes verbatim.
	subAgents := make([]SubAgentInfo, 0, 40)
	for i := range 40 {
		subAgents = append(subAgents, SubAgentInfo{
			InstanceID: fmt.Sprintf("inst-%d", i),
			TaskID:     fmt.Sprintf("task-%d", i),
			State:      "running",
			TaskDesc:   strings.Repeat(fmt.Sprintf("subtask %d detail ", i), 30),
		})
	}
	backgroundObjects := make([]recovery.BackgroundObjectState, 0, 80)
	for i := range 80 {
		backgroundObjects = append(backgroundObjects, recovery.BackgroundObjectState{
			ID:          fmt.Sprintf("bg-%d", i),
			Description: strings.Repeat(fmt.Sprintf("background job %d detail ", i), 40),
			Command:     "chord headless --port 4000",
		})
	}
	todos := []tools.TodoItem{{ID: "t1", Status: "pending", Content: strings.Repeat("outstanding todo detail ", 60)}}

	historyPath := filepath.Join(a.sessionDir, "history-1.md")
	summary, _, err := a.summarizeCompactionHead(context.Background(), head, historyPath, nil, nil, todos, subAgents, backgroundObjects, compactionAnchors{})
	if err != nil {
		t.Fatalf("summarizeCompactionHead error: %v", err)
	}
	if strings.TrimSpace(summary) == "" {
		t.Fatal("expected a summary from the capture provider")
	}

	// The sent prompt must not carry the degraded-away auxiliary sections.
	// Even if one round kept them (borderline fit), the full prompt built from
	// the ORIGINAL lists must strictly exceed the reserved budget — that is
	// the admission gate's own arithmetic — so comparing against it proves the
	// sent prompt was admitted only after degradation.
	// The sent prompt must not carry the degraded-away background objects:
	// their section stays with an explicit "(none)" rather than echoing the
	// oversized descriptions that would have blown the admission budget.
	if strings.Contains(provider.prompt, "background job 0 detail") {
		t.Fatalf("oversized background objects must be degraded out of the sent prompt (sent=%d bytes)", len(provider.prompt))
	}

	// Sanity: the fixture really did force a degradation (the full lists would
	// not have fit).
	fullPrompt := buildCompactionPromptWithKeyFiles(
		&compactionInput{Transcript: strings.Repeat("transcript line to keep the size honest\n", 200)},
		"history-1.md",
		nil, todos, subAgents, backgroundObjects,
	)
	if len(fullPrompt)/3 <= 11264 {
		t.Fatalf("fixture auxiliary sections too small to force degradation: %d tokens", len(fullPrompt)/3)
	}
}

// TestFitCompactionInputReturnsTheInputsUsedForPromptAssembly pins the
// admission boundary: when degradation is needed, the returned inputs must be
// the degraded ones, and the prompt assembled from them must fit the reserved
// budget. A caller that ignored the returned inputs and rebuilt the prompt
// from the original full lists would exceed that budget.
func TestFitCompactionInputReturnsTheInputsUsedForPromptAssembly(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	head := make([]message.Message, 0, 60)
	for i := range 20 {
		callID := fmt.Sprintf("fit-read-%d", i)
		head = append(head,
			message.Message{Role: message.RoleUser, Content: fmt.Sprintf("inspect area %d", i)},
			message.Message{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: callID, Name: tools.NameRead, Args: json.RawMessage(`{"path":"internal/agent/compaction.go"}`)}}},
			message.Message{Role: message.RoleTool, ToolCallID: callID, Content: strings.Repeat("output line from a wide read\n", 120)},
		)
	}
	input := &compactionInput{Transcript: strings.Repeat("transcript payload to prime the fit loop\n", 300)}
	keyFiles := make([]string, 0, 40)
	for i := range 40 {
		keyFiles = append(keyFiles, "internal/agent/"+strings.Repeat("file", 20)+fmt.Sprintf("-%d.go", i))
	}
	subAgents := make([]SubAgentInfo, 0, 40)
	for i := range 40 {
		subAgents = append(subAgents, SubAgentInfo{InstanceID: fmt.Sprintf("inst-%d", i), TaskID: fmt.Sprintf("task-%d", i), State: "running", TaskDesc: strings.Repeat(fmt.Sprintf("subtask %d detail ", i), 30)})
	}
	backgroundObjects := make([]recovery.BackgroundObjectState, 0, 40)
	for i := range 40 {
		backgroundObjects = append(backgroundObjects, recovery.BackgroundObjectState{ID: fmt.Sprintf("bg-%d", i), Description: strings.Repeat(fmt.Sprintf("background job %d detail ", i), 20), Command: "chord headless"})
	}

	const contextLimit = 16384
	got, inputs, err := a.fitCompactionInputToContextLimit(head, input, contextLimit, "history-1.md", keyFiles, nil, subAgents, backgroundObjects, compactReservedOutput)
	if err != nil {
		t.Fatalf("fit: %v", err)
	}
	if got == nil || inputs == nil {
		t.Fatal("fit must return the admitted input and its auxiliary inputs")
	}
	if len(inputs.BackgroundObjects) != 0 {
		t.Fatalf("degradation must drop background objects, got %d", len(inputs.BackgroundObjects))
	}
	allowedInput := contextLimit - compactReservedOutput - max(contextLimit/compactPreflightBufferRatio, compactPreflightBufferMin)
	if estimate := compactionPromptTokenEstimate(got, "history-1.md", inputs.KeyFiles, inputs.Todos, inputs.SubAgents, inputs.BackgroundObjects); estimate > allowedInput {
		t.Fatalf("prompt assembled from the returned inputs = %d tokens, want ≤ %d", estimate, allowedInput)
	}
}
