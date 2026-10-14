package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/ctxmgr"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func TestProviderSupportsRequiredToolChoiceUsesKnownFamiliesOnly(t *testing.T) {
	tests := []struct {
		name     string
		typeName string
		want     bool
	}{
		{name: "chat completions", typeName: config.ProviderTypeChatCompletions, want: true},
		{name: "responses", typeName: config.ProviderTypeResponses, want: true},
		{name: "messages", typeName: config.ProviderTypeMessages, want: true},
		{name: "generate content", typeName: config.ProviderTypeGenerateContent, want: true},
		{name: "unknown", typeName: "custom-provider", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := llm.NewProviderConfig("test", config.ProviderConfig{Type: tt.typeName}, nil)
			if got := providerSupportsRequiredToolChoice(provider); got != tt.want {
				t.Fatalf("providerSupportsRequiredToolChoice(%q) = %v, want %v", tt.typeName, got, tt.want)
			}
		})
	}
}

func TestDoneRejectedContinuationNoteIncludesRejectReason(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.loopState.enableWithTarget("finish current task")

	note := a.buildLoopContinuationNote(&LoopAssessment{
		Action:  LoopAssessmentActionContinue,
		Message: "Done rejected: 分析所有会话并找出可沉淀命令",
		Reasons: []string{"done_rejected", "context_continue"},
	})
	if note == nil {
		t.Fatal("expected continuation note")
	}
	if !strings.Contains(note.Text, "Latest Done rejection reason:\n分析所有会话并找出可沉淀命令") {
		t.Fatalf("continuation note = %q, want explicit rejection reason", note.Text)
	}
}

func TestLoopAnchorOmitsTodoRequirementWhenNoOpenTodos(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.sendLoopAnchorFromCommand("finish current task")
	msgs := a.ctxMgr.Snapshot()
	var found *message.Message
	for i := range msgs {
		if msgs[i].Kind == "loop_notice" {
			found = &msgs[i]
			break
		}
	}
	if found == nil {
		t.Fatal("expected persisted loop notice message")
	}
	if strings.Contains(found.Content, "No open TODO items remain") {
		t.Fatalf("loop notice should NOT contain 'No open TODO items remain' when no TODOs exist, got: %q", found.Content)
	}
	if strings.Contains(found.Content, tools.NameTodoWrite) {
		t.Fatalf("loop notice should NOT contain %q when no TODOs exist, got: %q", tools.NameTodoWrite, found.Content)
	}
}

func TestLoopAnchorIncludesTodoRequirementWhenOpenTodosWithTodoWrite(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.tools.Register(tools.NewTodoWriteTool(nil))
	a.todoItems = []tools.TodoItem{{ID: "1", Content: "ship feature", Status: "pending"}}
	a.sendLoopAnchorFromCommand("finish current task")
	msgs := a.ctxMgr.Snapshot()
	var found *message.Message
	for i := range msgs {
		if msgs[i].Kind == "loop_notice" {
			found = &msgs[i]
			break
		}
	}
	if found == nil {
		t.Fatal("expected persisted loop notice message")
	}
	if !strings.Contains(found.Content, "No open TODO items remain") {
		t.Fatalf("loop notice should contain 'No open TODO items remain' when TODOs exist and TodoWrite is available, got: %q", found.Content)
	}
	if !strings.Contains(found.Content, tools.NameTodoWrite) {
		t.Fatalf("loop notice should name %q when TODOs exist and it is available, got: %q", tools.NameTodoWrite, found.Content)
	}
	if !strings.Contains(found.Content, "Open TODO items:") {
		t.Fatalf("loop notice should list open TODOs section when TODOs exist, got: %q", found.Content)
	}
}

func TestLoopAnchorIncludesTodoRequirementWhenOpenTodosWithoutTodoWrite(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	// No TodoWrite registered — hasTodoWriteAccess() returns false.
	a.todoItems = []tools.TodoItem{{ID: "1", Content: "ship feature", Status: "pending"}}
	a.sendLoopAnchorFromCommand("finish current task")
	msgs := a.ctxMgr.Snapshot()
	var found *message.Message
	for i := range msgs {
		if msgs[i].Kind == "loop_notice" {
			found = &msgs[i]
			break
		}
	}
	if found == nil {
		t.Fatal("expected persisted loop notice message")
	}
	if strings.Contains(found.Content, "Mark every remaining open TODO item completed or cancelled with `"+tools.NameTodoWrite+"`") {
		t.Fatalf("loop notice should NOT instruct model to use %q when the tool is unavailable, got: %q", tools.NameTodoWrite, found.Content)
	}
	if !strings.Contains(found.Content, "`"+tools.NameTodoWrite+"` is not available") {
		t.Fatalf("loop notice should explain %q is unavailable, got: %q", tools.NameTodoWrite, found.Content)
	}
	if !strings.Contains(found.Content, "finish the remaining work") {
		t.Fatalf("loop notice should instruct to finish remaining work, got: %q", found.Content)
	}
	if strings.Contains(found.Content, "No open TODO items remain") {
		t.Fatalf("loop notice should NOT say 'No open TODO items remain' when TodoWrite is unavailable (cannot sync), got: %q", found.Content)
	}
	if !strings.Contains(found.Content, "Open TODO items:") {
		t.Fatalf("loop notice should list open TODOs section even when TodoWrite is unavailable, got: %q", found.Content)
	}
}

func TestLoopContinuationOmitsTodoRequirementWhenNoOpenTodos(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.loopState.enableWithTarget("finish current task")
	note := a.buildLoopContinuationNote(&LoopAssessment{Action: LoopAssessmentActionContinue, Reasons: []string{"target_active"}})
	if note == nil {
		t.Fatal("expected continuation note")
	}
	if strings.Contains(note.Text, "No open TODO items remain") {
		t.Fatalf("LOOP CONTINUE should NOT contain 'No open TODO items remain' when no TODOs exist, got: %q", note.Text)
	}
	if strings.Contains(note.Text, tools.NameTodoWrite) {
		t.Fatalf("LOOP CONTINUE should NOT contain %q when no TODOs exist, got: %q", tools.NameTodoWrite, note.Text)
	}
}

func TestLoopContinuationIncludesTodoRequirementWhenOpenTodosWithTodoWrite(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.tools.Register(tools.NewTodoWriteTool(nil))
	a.loopState.enableWithTarget("finish current task")
	a.todoItems = []tools.TodoItem{{ID: "1", Content: "ship feature", Status: "in_progress"}}
	note := a.buildLoopContinuationNote(&LoopAssessment{Action: LoopAssessmentActionContinue, Reasons: []string{"open_todos", "target_active"}})
	if note == nil {
		t.Fatal("expected continuation note")
	}
	if !strings.Contains(note.Text, "No open TODO items remain") {
		t.Fatalf("LOOP CONTINUE should contain 'No open TODO items remain' when TODOs exist and TodoWrite is available, got: %q", note.Text)
	}
	if !strings.Contains(note.Text, tools.NameTodoWrite) {
		t.Fatalf("LOOP CONTINUE should name %q when TODOs exist and it is available, got: %q", tools.NameTodoWrite, note.Text)
	}
}

func TestLoopContinuationIncludesTodoRequirementWhenOpenTodosWithoutTodoWrite(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	// No TodoWrite registered — hasTodoWriteAccess() returns false.
	a.loopState.enableWithTarget("finish current task")
	a.todoItems = []tools.TodoItem{{ID: "1", Content: "ship feature", Status: "in_progress"}}
	note := a.buildLoopContinuationNote(&LoopAssessment{Action: LoopAssessmentActionContinue, Reasons: []string{"open_todos", "target_active"}})
	if note == nil {
		t.Fatal("expected continuation note")
	}
	if strings.Contains(note.Text, "Mark every remaining open TODO item completed or cancelled with `"+tools.NameTodoWrite+"`") {
		t.Fatalf("LOOP CONTINUE should NOT tell model to use %q when the tool is unavailable, got: %q", tools.NameTodoWrite, note.Text)
	}
	if !strings.Contains(note.Text, "Open TODO items:") {
		t.Fatalf("LOOP CONTINUE should list open TODOs even when TodoWrite is unavailable, got: %q", note.Text)
	}
}

func TestLoopAnchorOmitsSubAgentRequirementWhenNoActiveSubAgents(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.sendLoopAnchorFromCommand("finish current task")
	msgs := a.ctxMgr.Snapshot()
	var found *message.Message
	for i := range msgs {
		if msgs[i].Kind == "loop_notice" {
			found = &msgs[i]
			break
		}
	}
	if found == nil {
		t.Fatal("expected persisted loop notice message")
	}
	if strings.Contains(found.Content, "No active subagents remain") {
		t.Fatalf("loop notice should NOT contain 'No active subagents remain' when no active subagents exist, got: %q", found.Content)
	}
	if strings.Contains(found.Content, "Active subagents:") {
		t.Fatalf("loop notice should NOT contain 'Active subagents:' section when no active subagents exist, got: %q", found.Content)
	}
}

func TestLoopAnchorIncludesSubAgentRequirementWhenActiveSubAgents(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	sub := &SubAgent{
		instanceID: "agent-1",
		parent:     a,
		parentCtx:  ctx,
		cancel:     cancel,
		inputCh:    make(chan pendingUserMessage, 1),
		recovery:   a.recoveryManager(),
		ctxMgr:     ctxmgr.NewManager(100, 0),
	}
	a.subs.mu.Lock()
	a.subs.subAgents["agent-1"] = sub
	a.subs.mu.Unlock()
	a.sendLoopAnchorFromCommand("finish current task")
	msgs := a.ctxMgr.Snapshot()
	var found *message.Message
	for i := range msgs {
		if msgs[i].Kind == "loop_notice" {
			found = &msgs[i]
			break
		}
	}
	if found == nil {
		t.Fatal("expected persisted loop notice message")
	}
	if !strings.Contains(found.Content, "No active subagents remain") {
		t.Fatalf("loop notice should contain 'No active subagents remain' when active subagents exist, got: %q", found.Content)
	}
	if !strings.Contains(found.Content, "Active subagents:") {
		t.Fatalf("loop notice should contain 'Active subagents:' section when active subagents exist, got: %q", found.Content)
	}
}

func TestLoopContinuationOmitsSubAgentRequirementWhenNoActiveSubAgents(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.loopState.enableWithTarget("finish current task")
	note := a.buildLoopContinuationNote(&LoopAssessment{Action: LoopAssessmentActionContinue, Reasons: []string{"target_active"}})
	if note == nil {
		t.Fatal("expected continuation note")
	}
	if strings.Contains(note.Text, "No active subagents remain") {
		t.Fatalf("LOOP CONTINUE should NOT contain 'No active subagents remain' when no active subagents exist, got: %q", note.Text)
	}
}

func TestLoopContinuationIncludesSubAgentRequirementWhenActiveSubAgents(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.loopState.enableWithTarget("finish current task")
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	sub := &SubAgent{
		instanceID: "agent-1",
		parent:     a,
		parentCtx:  ctx,
		cancel:     cancel,
		inputCh:    make(chan pendingUserMessage, 1),
		recovery:   a.recoveryManager(),
		ctxMgr:     ctxmgr.NewManager(100, 0),
	}
	a.subs.mu.Lock()
	a.subs.subAgents["agent-1"] = sub
	a.subs.mu.Unlock()
	note := a.buildLoopContinuationNote(&LoopAssessment{Action: LoopAssessmentActionContinue, Reasons: []string{"subagents_active", "target_active"}})
	if note == nil {
		t.Fatal("expected continuation note")
	}
	if !strings.Contains(note.Text, "No active subagents remain") {
		t.Fatalf("LOOP CONTINUE should contain 'No active subagents remain' when active subagents exist, got: %q", note.Text)
	}
}

func TestLoopAnchorIncludesIterationBudget(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.sendLoopAnchorFromCommand("finish current task")
	msgs := a.ctxMgr.Snapshot()
	var found *message.Message
	for i := range msgs {
		if msgs[i].Kind == "loop_notice" {
			found = &msgs[i]
			break
		}
	}
	if found == nil {
		t.Fatal("expected persisted loop notice message")
	}
	if !strings.Contains(found.Content, "Automatic Done interceptions:") {
		t.Fatalf("loop notice should contain iteration budget, got: %q", found.Content)
	}
}

func TestLoopContinuationIncludesIterationBudget(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.loopState.enableWithTarget("finish current task")
	a.loopState.MaxIterations = 100
	a.loopState.Iteration = 7
	note := a.buildLoopContinuationNote(&LoopAssessment{Action: LoopAssessmentActionContinue, Reasons: []string{"target_active"}})
	if note == nil {
		t.Fatal("expected continuation note")
	}
	if !strings.Contains(note.Text, "Automatic Done interceptions 7 of 100 (93 remaining)") {
		t.Fatalf("LOOP CONTINUE should contain iteration budget with remaining count, got: %q", note.Text)
	}
}

// TestLoopContinuationBudgetCountsTheRejectionThatProducedIt pins the off-by-one
// the budget line used to carry: the auto-reject path records the intercept
// before it builds the notice, so a counter of 7 must report 7 spent.
func TestLoopContinuationBudgetCountsTheRejectionThatProducedIt(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.loopState.enableWithTarget("finish current task")
	a.loopState.MaxIterations = 10
	before := a.loopState.Iteration
	a.loopState.recordAutoExitIntercept()
	note := a.buildLoopContinuationNote(&LoopAssessment{Action: LoopAssessmentActionContinue, Reasons: []string{"target_active"}})
	if note == nil {
		t.Fatal("expected continuation note")
	}
	want := fmt.Sprintf("Automatic Done interceptions %d of 10 (%d remaining)", before+1, 10-(before+1))
	if !strings.Contains(note.Text, want) {
		t.Fatalf("LOOP CONTINUE budget = %q, want %q", note.Text, want)
	}
}

func TestLoopContinuationConvergenceWarningNearBudgetLimit(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.loopState.enableWithTarget("finish current task")
	a.loopState.MaxIterations = 100
	a.loopState.Iteration = 99
	note := a.buildLoopContinuationNote(&LoopAssessment{Action: LoopAssessmentActionContinue, Reasons: []string{"target_active"}})
	if note == nil {
		t.Fatal("expected continuation note")
	}
	if got := strings.Count(note.Text, "near the automatic Done interception limit"); got != 1 {
		t.Fatalf("LOOP CONTINUE must warn about the limit exactly once, got %d in: %q", got, note.Text)
	}
	// The budget line reports the counter only; a second near-exhaustion
	// sentence next to the instruction line is the duplication this guards.
	if strings.Contains(note.Text, "budget is nearly exhausted") {
		t.Fatalf("the near-exhaustion warning must have a single home, got: %q", note.Text)
	}
}

func TestLoopContinuationGapLinesConcrete(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.loopState.enableWithTarget("fix the bug")
	note := a.buildLoopContinuationNote(&LoopAssessment{Action: LoopAssessmentActionContinue, Reasons: []string{"open_todos", "subagents_active"}})
	if note == nil {
		t.Fatal("expected continuation note")
	}
	if !strings.Contains(note.Text, "Why this loop continues:") {
		t.Fatalf("LOOP CONTINUE should have 'Why this loop continues' section, got: %q", note.Text)
	}
	if !strings.Contains(note.Text, "open TODO items remain") {
		t.Fatalf("LOOP CONTINUE gap lines should mention open TODO items, got: %q", note.Text)
	}
	if !strings.Contains(note.Text, "active subagents are still running") {
		t.Fatalf("LOOP CONTINUE gap lines should mention active subagents, got: %q", note.Text)
	}
}

func TestLoopContinuationNoVagueFallbackWhenConcreteReasonsExist(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.loopState.enableWithTarget("fix the bug")
	note := a.buildLoopContinuationNote(&LoopAssessment{Action: LoopAssessmentActionContinue, Reasons: []string{"open_todos"}})
	if note == nil {
		t.Fatal("expected continuation note")
	}
	// When concrete reasons exist, the generic "loop is not yet complete" fallback should NOT appear.
	if strings.Contains(note.Text, "loop is not yet complete") {
		t.Fatalf("LOOP CONTINUE should not use vague fallback when concrete reasons exist, got: %q", note.Text)
	}
}

func TestLoopContinuationSubAgentStuckInstruction(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.loopState.enableWithTarget("finish current task")
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	sub := &SubAgent{
		instanceID: "agent-1",
		parent:     a,
		parentCtx:  ctx,
		cancel:     cancel,
		inputCh:    make(chan pendingUserMessage, 1),
		recovery:   a.recoveryManager(),
		ctxMgr:     ctxmgr.NewManager(100, 0),
	}
	a.subs.mu.Lock()
	a.subs.subAgents["agent-1"] = sub
	a.subs.mu.Unlock()
	note := a.buildLoopContinuationNote(&LoopAssessment{Action: LoopAssessmentActionContinue, Reasons: []string{"subagents_active", "target_active"}})
	if note == nil {
		t.Fatal("expected continuation note")
	}
	if !strings.Contains(note.Text, "stuck or blocked") {
		t.Fatalf("LOOP CONTINUE should instruct about stuck subagents when active subagents exist, got: %q", note.Text)
	}
}

func TestCurrentLoopContinuationReasonsUsesHasActiveSubAgents(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.loopState.enableWithTarget("finish current task")
	// Add a completed subagent — it should NOT trigger subagents_active.
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	sub := &SubAgent{
		instanceID: "agent-1",
		parent:     a,
		parentCtx:  ctx,
		cancel:     cancel,
		inputCh:    make(chan pendingUserMessage, 1),
		recovery:   a.recoveryManager(),
		ctxMgr:     ctxmgr.NewManager(100, 0),
	}
	sub.setState(SubAgentStateCompleted, "")
	a.subs.mu.Lock()
	a.subs.subAgents["agent-1"] = sub
	a.subs.mu.Unlock()
	reasons := a.currentLoopContinuationReasons()
	for _, r := range reasons {
		if r == "subagents_active" {
			t.Fatalf("should not report subagents_active for completed subagent, reasons: %v", reasons)
		}
	}
}
