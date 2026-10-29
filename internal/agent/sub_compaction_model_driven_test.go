package agent

import (
	"context"
	"encoding/json"
	"maps"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/tools"
)

func workerCompactCallJSON(t *testing.T, objective, step string, extra map[string]any) string {
	t.Helper()
	args := map[string]any{
		"active_objective": objective,
		"next_step":        step,
		"completed":        []string{"moved the parser into its own file"},
		"decisions":        []string{"keep one shared parser"},
		"open_issues":      []string{"flaky retry test"},
		"state_files":      []string{"notes/20261030-refactor.md"},
	}
	maps.Copy(args, extra)
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	return string(raw)
}

// armWorkerCompactCall appends the declaring assistant message (a sole
// compact_context call) to the worker transcript and returns the call ID.
func armWorkerCompactCall(t *testing.T, sub *SubAgent, raw string) string {
	t.Helper()
	callID := "call-cc-worker"
	sub.ctxMgr.Append(message.Message{
		Role:      message.RoleAssistant,
		ToolCalls: []message.ToolCall{{ID: callID, Name: tools.NameCompactContext, Args: json.RawMessage(raw)}},
	})
	return callID
}

func workerCompactionHistory() []message.Message {
	messages := []message.Message{{Role: message.RoleUser, Content: "refactor the parser"}}
	for range 12 {
		messages = append(messages,
			message.Message{Role: message.RoleAssistant, Content: strings.Repeat("analysis ", 300)},
			message.Message{Role: message.RoleUser, Content: "keep going"},
		)
	}
	return messages
}

func TestSubAgentWorkerCompactContextRegistrationConditions(t *testing.T) {
	t.Parallel()
	parent, sub := newMixedBatchTestSubAgent(t)
	t.Cleanup(sub.cancel)
	base := tools.NewRegistry()
	base.Register(tools.NewCompactContextTool(tools.CompactContextValidator{}))

	sub.registerWorkerCompactContextTool(parent, base, sub.currentRuleset())
	if sub.compactContextRegistered {
		t.Fatal("worker tool must not register while the parent feature is disabled")
	}

	parent.modelDrivenCompactionEnabled.Store(true)
	denied := permission.Ruleset{{Permission: tools.NameCompactContext, Pattern: "*", Action: permission.ActionDeny}}
	sub.registerWorkerCompactContextTool(parent, base, denied)
	if sub.compactContextRegistered {
		t.Fatal("worker tool must not register under a deny rule")
	}

	sub.registerWorkerCompactContextTool(parent, base, sub.currentRuleset())
	if !sub.compactContextRegistered {
		t.Fatal("worker tool must register when enabled, present in the base registry and allowed")
	}
	tool, ok := sub.tools.Get(tools.NameCompactContext)
	if !ok {
		t.Fatal("compact_context missing from the worker registry")
	}
	if !strings.Contains(tool.Description(), "delegated task") {
		t.Fatalf("worker registry carries the main description: %q", tool.Description())
	}

	// state_files spellings anchor at this worker's own checkout: an absolute
	// path inside the effective base dir normalizes, one outside is rejected.
	// The validation is purely lexical, so no file needs to exist.
	workDir := sub.effectiveToolBaseDir()
	insideRaw, err := json.Marshal(map[string]any{
		"active_objective": "a",
		"next_step":        "b",
		"state_files":      []string{filepath.ToSlash(filepath.Join(workDir, "notes", "task.md"))},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	accepted, err := tool.Execute(context.Background(), insideRaw)
	if err != nil {
		t.Fatalf("state_files inside the worker checkout must be accepted: %v", err)
	}
	if !strings.Contains(accepted, "accepted") {
		t.Fatalf("unexpected acceptance text: %q", accepted)
	}
	outsideRaw, err := json.Marshal(map[string]any{
		"active_objective": "a",
		"next_step":        "b",
		"state_files":      []string{filepath.ToSlash(filepath.Join(workDir, "..", "outside.md"))},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if _, err := tool.Execute(context.Background(), outsideRaw); err == nil {
		t.Fatal("state_files escaping the worker checkout must be rejected")
	}
}

func TestSubAgentModelDrivenCheckpointAppliesAtBatchClose(t *testing.T) {
	t.Parallel()
	parent, sub := newMixedBatchTestSubAgent(t)
	t.Cleanup(sub.cancel)
	parent.modelDrivenCompactionEnabled.Store(true)
	sub.compactContextRegistered = true
	sub.taskDesc = "preserve the delegated task"
	sub.ownerAgentID = "main"
	// A cancelled turn context stops the LLM continuation that would normally
	// consume the transient overlay, so the test can inspect it directly. The
	// apply happens before that exit, exactly as it would on a live turn.
	stopCtx, stop := context.WithCancel(sub.parentCtx)
	stop()
	sub.turn.Ctx = stopCtx

	messages := workerCompactionHistory()
	sub.ctxMgr.RestoreMessages(messages)
	raw := workerCompactCallJSON(t, "finish the delegated refactor", "run the package tests", nil)
	callID := armWorkerCompactCall(t, sub, raw)

	sub.handleToolResult(&toolResult{
		CallID:   callID,
		Name:     tools.NameCompactContext,
		ArgsJSON: raw,
		Result:   requestAcceptedToolResult,
		TurnID:   sub.turn.ID,
	})

	if sub.turn.PendingModelDrivenCompaction != nil {
		t.Fatal("pending checkpoint request must be consumed at the batch close")
	}
	snapshot := sub.ctxMgr.Snapshot()
	if len(snapshot) >= len(messages) {
		t.Fatalf("history did not compact: before=%d after=%d", len(messages), len(snapshot))
	}
	checkpoint := snapshot[1]
	if checkpoint.CompactionSummaryMode != message.CompactionSummaryModeModelDriven {
		t.Fatalf("checkpoint summary mode = %q, want %q", checkpoint.CompactionSummaryMode, message.CompactionSummaryModeModelDriven)
	}
	for _, want := range []string{
		"[system] SubAgent context checkpoint:",
		"- Active objective: finish the delegated refactor",
		"- Next step: run the package tests",
		"- Decisions:\n  - keep one shared parser",
		"- Open problems:\n  - flaky retry test",
		"- State files: notes/20261030-refactor.md",
		subAgentCheckpointSkillsPrefix,
		"- Task: preserve the delegated task",
		"- Owner: main/",
		"Full pre-checkpoint history: ",
	} {
		if !strings.Contains(checkpoint.Content, want) {
			t.Fatalf("model-driven checkpoint missing %q:\n%s", want, checkpoint.Content)
		}
	}
	// The continuation guidance rides on the next request, not the transcript.
	if transient := sub.takeCompactionTransient(); !strings.Contains(transient, "checkpoint was applied") || !strings.Contains(transient, subCheckpointVerificationGuidance) {
		t.Fatalf("applied transient = %q, want the verification guidance", transient)
	}
}

func TestSubAgentModelDrivenCheckpointAppliesWithLargeArguments(t *testing.T) {
	t.Parallel()
	parent, sub := newMixedBatchTestSubAgent(t)
	t.Cleanup(sub.cancel)
	parent.modelDrivenCompactionEnabled.Store(true)
	sub.compactContextRegistered = true
	stopCtx, stop := context.WithCancel(sub.parentCtx)
	stop()
	sub.turn.Ctx = stopCtx

	var messages []message.Message
	for range 4 {
		messages = append(messages, workerCompactionHistory()...)
	}
	sub.ctxMgr.RestoreMessages(messages)
	// The newest tail is the compact_context call and its result, about as
	// large as the arguments; it must still fit the retained tail.
	findings := make([]string, 16)
	for i := range findings {
		findings[i] = strings.Repeat("verified finding with its evidence and conditions ", 6)
	}
	raw := workerCompactCallJSON(t, "finish the delegated refactor", "run the package tests", map[string]any{
		"completed": findings,
		"decisions": findings,
	})
	if len(raw) < 8000 {
		t.Fatalf("arguments too small to cover the large-checkpoint path: %d bytes", len(raw))
	}
	callID := armWorkerCompactCall(t, sub, raw)

	sub.handleToolResult(&toolResult{
		CallID:   callID,
		Name:     tools.NameCompactContext,
		ArgsJSON: raw,
		Result:   requestAcceptedToolResult,
		TurnID:   sub.turn.ID,
	})

	snapshot := sub.ctxMgr.Snapshot()
	if len(snapshot) >= len(messages) {
		t.Fatalf("large checkpoint did not apply: before=%d after=%d transient=%q", len(messages), len(snapshot), sub.takeCompactionTransient())
	}
	if snapshot[1].CompactionSummaryMode != message.CompactionSummaryModeModelDriven {
		t.Fatalf("checkpoint summary mode = %q, want %q", snapshot[1].CompactionSummaryMode, message.CompactionSummaryModeModelDriven)
	}
}

func TestSubAgentModelDrivenCheckpointLowGainSkipsWithTransient(t *testing.T) {
	t.Parallel()
	parent, sub := newMixedBatchTestSubAgent(t)
	t.Cleanup(sub.cancel)
	parent.modelDrivenCompactionEnabled.Store(true)
	sub.compactContextRegistered = true
	// See the apply test: the cancelled turn context keeps the transient from
	// being consumed by the LLM continuation.
	stopCtx, stop := context.WithCancel(sub.parentCtx)
	stop()
	sub.turn.Ctx = stopCtx

	messages := []message.Message{
		{Role: message.RoleUser, Content: "small task"},
		{Role: message.RoleAssistant, Content: strings.Repeat("short ", 40)},
	}
	sub.ctxMgr.RestoreMessages(messages)
	raw := workerCompactCallJSON(t, "finish the small task", "report back", nil)
	callID := armWorkerCompactCall(t, sub, raw)

	sub.handleToolResult(&toolResult{
		CallID:   callID,
		Name:     tools.NameCompactContext,
		ArgsJSON: raw,
		Result:   requestAcceptedToolResult,
		TurnID:   sub.turn.ID,
	})

	if sub.turn.PendingModelDrivenCompaction != nil {
		t.Fatal("pending request must be consumed even when skipped")
	}
	if got := len(sub.ctxMgr.Snapshot()); got != len(messages)+2 {
		t.Fatalf("history changed on skip: want the untouched %d plus assistant and result, got %d", len(messages), got)
	}
	transient := sub.takeCompactionTransient()
	if !strings.Contains(transient, "Context checkpoint not applied") || !strings.Contains(transient, "low-gain") {
		t.Fatalf("skip transient = %q, want the not-applied reason", transient)
	}
	deadline := time.After(2 * time.Second)
	for {
		select {
		case evt := <-parent.Events():
			if toast, ok := evt.(ToastEvent); ok && strings.Contains(toast.Message, "Context checkpoint not applied") {
				if toast.AgentID != sub.instanceID {
					t.Fatalf("toast agent = %q, want %q", toast.AgentID, sub.instanceID)
				}
				return
			}
		case <-deadline:
			t.Fatal("timed out waiting for the skip toast")
		}
	}
}

func TestSubAgentModelDrivenCheckpointRejectsSiblingCallBeforeEvent(t *testing.T) {
	t.Parallel()
	parent, sub := newMixedBatchTestSubAgent(t)
	t.Cleanup(sub.cancel)
	parent.modelDrivenCompactionEnabled.Store(true)
	sub.compactContextRegistered = true

	messages := workerCompactionHistory()
	sub.ctxMgr.RestoreMessages(messages)
	raw := workerCompactCallJSON(t, "finish the delegated refactor", "run the package tests", nil)
	callID := "call-cc-sibling"
	sub.ctxMgr.Append(message.Message{
		Role: message.RoleAssistant,
		ToolCalls: []message.ToolCall{
			{ID: callID, Name: tools.NameCompactContext, Args: json.RawMessage(raw)},
			{ID: "call-read-1", Name: tools.NameRead},
		},
	})

	sub.handleToolResult(&toolResult{
		CallID:   callID,
		Name:     tools.NameCompactContext,
		ArgsJSON: raw,
		Result:   requestAcceptedToolResult,
		TurnID:   sub.turn.ID,
	})

	if sub.turn.PendingModelDrivenCompaction != nil {
		t.Fatal("a sibling call must not arm a checkpoint request")
	}
	toolMsg := lastToolMessage(t, sub)
	if toolMsg.ToolStatus != message.ToolStatusError {
		t.Fatalf("tool status = %q, want error for a non-sole call", toolMsg.ToolStatus)
	}
	if !strings.Contains(toolMsg.Content, "Context checkpoint rejected") {
		t.Fatalf("tool result = %q, want the rejection text", toolMsg.Content)
	}
	if got := len(sub.ctxMgr.Snapshot()); got != len(messages)+2 {
		t.Fatalf("history = %d messages, want the untouched %d plus assistant and result", got, len(messages))
	}
}

func TestSubAgentModelDrivenCheckpointContentKeepsSkillsContract(t *testing.T) {
	t.Parallel()
	_, sub := newMixedBatchTestSubAgent(t)
	t.Cleanup(sub.cancel)
	raw := workerCompactCallJSON(t, "obj", "step", nil)
	parsed, err := tools.CompactContextValidator{}.ParseCompactContextArgs(json.RawMessage(raw))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	messages := []message.Message{
		{Role: message.RoleUser, Content: "refactor the parser"},
		{Role: message.RoleAssistant, Content: strings.Repeat("analysis ", 40)},
	}
	checkpoint := newSubModelDrivenCheckpointContent(sub, messages, parsed, subModelDrivenApplyReason)
	content := checkpoint.build(7, "archives/worker-1.md")
	for _, want := range []string{
		"[system] SubAgent context checkpoint: 7 earlier messages were removed for " + subModelDrivenApplyReason,
		"- Skills loaded earlier: none",
		"- Task: do work",
		"Full pre-checkpoint history: archives/worker-1.md.",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("model-driven checkpoint missing %q:\n%s", want, content)
		}
	}
	// The skills recognizer accepts the model-driven body as the newest
	// checkpoint: a second compaction must read this row instead of falling
	// back to an older one. parseSubAgentCheckpointSkills drives the same
	// prefix match collectSubAgentCheckpointSkillNamesWithOverflow uses.
	if _, omitted, _ := parseSubAgentCheckpointSkills(content); omitted != 0 {
		t.Fatalf("fresh checkpoint carried an unexpected omission count")
	}
}
