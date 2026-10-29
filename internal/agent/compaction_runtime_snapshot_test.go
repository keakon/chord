package agent

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/agent/checkpointfmt"
	"github.com/keakon/chord/internal/recovery"
	"github.com/keakon/chord/internal/tools"
)

func TestFormatSubAgentsAsBulletsOmitsCompletedTasksAndTruncates(t *testing.T) {
	longDesc := strings.Repeat("desc ", 120)
	longSummary := strings.Repeat("summary ", 120)
	got := formatSubAgentsAsBullets([]SubAgentInfo{
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

func TestSubAgentPromptOrderingKeepsWaitingTasksFirstAndDeterministic(t *testing.T) {
	subs := []SubAgentInfo{
		{InstanceID: "w1", TaskID: "t-w1", State: string(SubAgentStateRunning)},
		{InstanceID: "w2", TaskID: "t-w2", State: string(SubAgentStateWaitingMain)},
		{InstanceID: "w3", TaskID: "t-w3", State: string(SubAgentStateIdle)},
		{InstanceID: "w4", TaskID: "t-w4", State: string(SubAgentStateWaitingDescendant)},
	}
	first, _, _ := subAgentsForCompactionPrompt(subs)
	reversed := make([]SubAgentInfo, len(subs))
	for i, s := range subs {
		reversed[len(subs)-1-i] = s
	}
	second, _, _ := subAgentsForCompactionPrompt(reversed)
	want := []string{"t-w2", "t-w4", "t-w1", "t-w3"}
	var firstIDs, secondIDs []string
	for _, s := range first {
		firstIDs = append(firstIDs, s.TaskID)
	}
	for _, s := range second {
		secondIDs = append(secondIDs, s.TaskID)
	}
	if !slices.Equal(firstIDs, want) || !slices.Equal(secondIDs, want) {
		t.Fatalf("prompt order is not priority-aware or not input-order-independent: first=%v second=%v want=%v", firstIDs, secondIDs, want)
	}
}

func TestSubAgentPromptLimitCountsActiveAndInactiveOmissionsSeparately(t *testing.T) {
	subs := make([]SubAgentInfo, 0, 10)
	for i := range 8 {
		subs = append(subs, SubAgentInfo{InstanceID: fmt.Sprintf("w%d", i), TaskID: fmt.Sprintf("t-%d", i), State: string(SubAgentStateRunning), TaskDesc: fmt.Sprintf("task %d", i)})
	}
	subs = append(subs, SubAgentInfo{InstanceID: "done-1", TaskID: "t-done", State: string(SubAgentStateCompleted)})
	visible, omittedActive, omittedInactive := subAgentsForCompactionPrompt(subs)
	if len(visible) != compactPromptSubAgentLimit {
		t.Fatalf("visible = %d, want %d", len(visible), compactPromptSubAgentLimit)
	}
	if omittedActive != 2 {
		t.Fatalf("omittedActive = %d, want 2 (running workers beyond the limit)", omittedActive)
	}
	if omittedInactive != 1 {
		t.Fatalf("omittedInactive = %d, want 1 (the completed task)", omittedInactive)
	}
	got := formatSubAgentsAsBullets(subs)
	if !strings.Contains(got, "2 active task(s) beyond the prompt limit omitted") {
		t.Fatalf("active omission footer missing:\n%s", got)
	}
	if !strings.Contains(got, "1 historical or completed task(s) omitted") {
		t.Fatalf("inactive omission footer missing:\n%s", got)
	}
}

func TestFormatSubAgentInfoLineIncludesNestedParentOnly(t *testing.T) {
	nested := formatSubAgentInfoLine(SubAgentInfo{
		InstanceID:   "worker-1",
		TaskID:       "adhoc-1",
		OwnerAgentID: "parent-1",
		OwnerTaskID:  "parent-task",
		State:        string(SubAgentStateRunning),
		AgentDefName: "coder",
		TaskDesc:     "build the widget",
	})
	if !strings.Contains(nested, " | parent=parent-1/parent-task") {
		t.Fatalf("nested worker row missing its parent: %q", nested)
	}
	mainOwned := formatSubAgentInfoLine(SubAgentInfo{
		InstanceID:   "worker-2",
		TaskID:       "adhoc-2",
		State:        string(SubAgentStateRunning),
		AgentDefName: "coder",
		TaskDesc:     "another task",
	})
	if strings.Contains(mainOwned, "parent=") {
		t.Fatalf("main-agent-owned worker row should not name a parent: %q", mainOwned)
	}
}

func TestEnsureCompactionSubAgentSnapshotRestoresRuntimeWorkers(t *testing.T) {
	modelSummary := "## Current User Request\n- continue current task\n\n## Active Objective\n- continue current task\n\n## Background Goals\n- none\n\n## User Constraints\n- none\n\n## Progress\n- progress recorded\n\n## Key Decisions\n- decisions captured\n\n## Files and Evidence\n- Archived history: history-1.md\n- src/current_task.go\n\n## Todo State\n- Active/relevant to latest request: (none)\n- Completed/background: (none)\n- Stale/superseded: (none)\n\n## SubAgent State\n- none active\n\n## Open Problems\n- none\n\n## Next Step\n- Inspect src/current_task.go and continue the current task."
	subAgents := []SubAgentInfo{{
		InstanceID:   "worker-1",
		TaskID:       "adhoc-1",
		State:        string(SubAgentStateRunning),
		AgentDefName: "coder",
		TaskDesc:     "build the widget",
	}}
	got := ensureCompactionSubAgentSnapshot(modelSummary, subAgents)
	if !strings.Contains(got, "- worker-1 | task=adhoc-1") {
		t.Fatalf("running worker dropped from checkpoint:\n%s", got)
	}
	if idx := strings.Index(got, "## SubAgent State"); idx < 0 || idx > strings.Index(got, "## Open Problems") {
		t.Fatalf("SubAgent State section displaced out of required order:\n%s", got)
	}
	if err := checkpointfmt.ValidateSummary(got); err != nil {
		t.Fatalf("ensured summary no longer validates: %v\n%s", err, got)
	}
	// An already-authoritative section must pass through unchanged (fallback
	// and truncate-only summaries render it deterministically themselves).
	if once := ensureCompactionSubAgentSnapshot(got, subAgents); once != got {
		t.Fatalf("ensure is not idempotent over its own output")
	}
}

func TestEnsureCompactionSubAgentSnapshotNormalizesPhantomRestatements(t *testing.T) {
	modelSummary := "## Current User Request\n- continue current task\n\n## Active Objective\n- continue current task\n\n## Background Goals\n- none\n\n## User Constraints\n- none\n\n## Progress\n- progress recorded\n\n## Key Decisions\n- decisions captured\n\n## Files and Evidence\n- Archived history: history-1.md\n\n## Todo State\n- none\n\n## SubAgent State\n- agent-2 | task=adhoc-2 | state=running | agent=coder | desc=older restatement\n\n## Open Problems\n- none\n\n## Next Step\n- Inspect src/current_task.go and continue the current task."
	got := ensureCompactionSubAgentSnapshot(modelSummary, nil)
	if !strings.Contains(got, "## SubAgent State\n- (none active)") {
		t.Fatalf("phantom worker restatement survived the authoritative replace:\n%s", got)
	}
	if strings.Contains(got, "agent-2") {
		t.Fatalf("settled worker still described as running in the checkpoint:\n%s", got)
	}
	if err := checkpointfmt.ValidateSummary(got); err != nil {
		t.Fatalf("ensured summary no longer validates: %v\n%s", err, got)
	}
}

func TestTaskInfosForCompactionCarriesOwnerIdentity(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	parent := &SubAgent{
		instanceID:   "parent-1",
		taskID:       "parent-task",
		agentDefName: "planner",
		cancel:       func() {},
	}
	worker := &SubAgent{
		instanceID:   "worker-1",
		taskID:       "adhoc-worker",
		agentDefName: "coder",
		taskDesc:     "build the thing",
		ownerAgentID: "parent-1",
		ownerTaskID:  "parent-task",
		depth:        1,
		cancel:       func() {},
	}
	a.subs.mu.Lock()
	a.subs.subAgents["parent-1"] = parent
	a.subs.subAgents["worker-1"] = worker
	a.subs.taskRecords["done-task"] = &DurableTaskRecord{
		TaskID:           "done-task",
		LatestInstanceID: "worker-old",
		AgentDefName:     "coder",
		TaskDesc:         "finished work",
		OwnerAgentID:     "parent-1",
		OwnerTaskID:      "parent-task",
		Depth:            1,
		State:            string(SubAgentStateCompleted),
		LastSummary:      "done",
	}
	a.subs.mu.Unlock()

	infos := a.taskInfosForCompaction()
	byTask := make(map[string]SubAgentInfo, len(infos))
	for _, info := range infos {
		byTask[info.TaskID] = info
	}
	for _, taskID := range []string{"adhoc-worker", "done-task"} {
		info, ok := byTask[taskID]
		if !ok {
			t.Fatalf("task %s missing from compaction infos: %v", taskID, byTask)
		}
		if info.OwnerAgentID != "parent-1" || info.OwnerTaskID != "parent-task" || info.Depth != 1 {
			t.Fatalf("task %s owner = (%q, %q, depth %d), want (parent-1, parent-task, 1)", taskID, info.OwnerAgentID, info.OwnerTaskID, info.Depth)
		}
	}
}

func TestBuildCompactionPromptIncludesBackgroundObjects(t *testing.T) {
	prompt := buildCompactionPromptWithKeyFiles(
		&compactionInput{Transcript: "transcript"},
		"history-1.md",
		nil,
		nil,
		nil,
		[]recovery.BackgroundObjectState{{
			ID:            "job-1",
			AgentID:       "builder-2",
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
	if !strings.Contains(prompt, "max_runtime=300s") {
		t.Fatalf("prompt missing background object max runtime:\n%s", prompt)
	}
}

func TestEnsureCompactionTodoSnapshotPreservesEveryRuntimeTodo(t *testing.T) {
	todos := []tools.TodoItem{
		{ID: "1", Content: "inspect the parser", Status: "in_progress", ActiveForm: "inspecting the parser"},
		{ID: "2", Content: "run tests\nthen report the result", Status: "pending"},
		{ID: "3", Content: "old cleanup", Status: "completed"},
	}
	summary := ensureCompactionTodoSnapshot(validCompactionSummaryForTest("history-1.md"), todos)
	for _, want := range []string{
		"### Runtime TODO snapshot",
		"classify against the latest user request and reconcile with ## Progress above",
		"[in_progress] 1: inspect the parser | active: inspecting the parser",
		"[pending] 2: run tests",
		"> then report the result",
		"[completed] 3: old cleanup",
	} {
		if !strings.Contains(summary, want) {
			t.Fatalf("snapshot missing %q:\n%s", want, summary)
		}
	}
	if err := checkpointfmt.ValidateSummary(summary); err != nil {
		t.Fatalf("summary with runtime todo snapshot rejected: %v", err)
	}
}

// compactionSummaryWithTodoStateForTest replaces the canonical empty Todo State
// body so snapshot decisions can be exercised without other sections changing.
func compactionSummaryWithTodoStateForTest(body string) string {
	return strings.Replace(
		validCompactionSummaryForTest("history-1.md"),
		"- Active/relevant to latest request: (none)\n- Completed/background: (none)\n- Stale/superseded: (none)",
		body,
		1,
	)
}

func TestEnsureCompactionTodoSnapshotSkipsCoveredClassification(t *testing.T) {
	todos := []tools.TodoItem{
		{ID: "1", Content: "verify the manual compaction path", Status: tools.TodoStatusCompleted},
		{ID: "2", Content: "persist the compaction trigger", Status: tools.TodoStatusCompleted},
	}
	summary := compactionSummaryWithTodoStateForTest(
		"- Active/relevant to latest request:\n  - (none)\n" +
			"- Completed/background:\n  - [completed] 1: verify the manual compaction path\n  - [completed] 2: persist the compaction trigger\n" +
			"- Stale/superseded:\n  - (none)")
	if got := ensureCompactionTodoSnapshot(summary, todos); got != summary {
		t.Fatalf("runtime snapshot repeated an exact classification:\n%s", got)
	}
}

func TestEnsureCompactionTodoSnapshotAppendsWhenClassificationShortensText(t *testing.T) {
	todos := []tools.TodoItem{{ID: "1", Content: "record the expected failure case", Status: tools.TodoStatusCompleted}}
	summary := compactionSummaryWithTodoStateForTest(
		"- Active/relevant to latest request:\n  - (none)\n" +
			"- Completed/background:\n  - [completed] 1: record the failure\n" +
			"- Stale/superseded:\n  - (none)")
	got := ensureCompactionTodoSnapshot(summary, todos)
	if !strings.Contains(got, "### Runtime TODO snapshot") || !strings.Contains(got, "  - [completed] 1: record the expected failure case") {
		t.Fatalf("runtime snapshot missing after the summary shortened the todo:\n%s", got)
	}
}

func TestEnsureCompactionTodoSnapshotAppendsWhenClassificationMissesAnItem(t *testing.T) {
	todos := []tools.TodoItem{
		{ID: "1", Content: "verify the manual compaction path", Status: tools.TodoStatusCompleted},
		{ID: "2", Content: "run affected tests", Status: tools.TodoStatusInProgress},
	}
	summary := compactionSummaryWithTodoStateForTest(
		"- Active/relevant to latest request:\n  - [completed] 1: verify the manual compaction path\n" +
			"- Completed/background:\n  - (none)\n" +
			"- Stale/superseded:\n  - (none)")
	got := ensureCompactionTodoSnapshot(summary, todos)
	if !strings.Contains(got, "### Runtime TODO snapshot") || !strings.Contains(got, "  - [in_progress] 2: run affected tests") {
		t.Fatalf("runtime snapshot missing the unclassified todo:\n%s", got)
	}
}

func TestEnsureCompactionTodoSnapshotAppendsOnStatusMismatch(t *testing.T) {
	todos := []tools.TodoItem{{ID: "1", Content: "run affected tests", Status: tools.TodoStatusInProgress}}
	summary := compactionSummaryWithTodoStateForTest(
		"- Active/relevant to latest request:\n  - [completed] 1: run affected tests\n" +
			"- Completed/background:\n  - (none)\n" +
			"- Stale/superseded:\n  - (none)")
	got := ensureCompactionTodoSnapshot(summary, todos)
	if !strings.Contains(got, "### Runtime TODO snapshot") || !strings.Contains(got, "  - [in_progress] 1: run affected tests") {
		t.Fatalf("runtime snapshot missing after a status mismatch:\n%s", got)
	}
}

func TestEnsureCompactionTodoSnapshotCountsDuplicateIDs(t *testing.T) {
	todos := []tools.TodoItem{
		{ID: "1", Content: "first pass", Status: tools.TodoStatusCompleted},
		{ID: "1", Content: "second pass", Status: tools.TodoStatusCompleted},
	}
	summary := compactionSummaryWithTodoStateForTest(
		"- Active/relevant to latest request:\n  - (none)\n" +
			"- Completed/background:\n  - [completed] 1: first pass\n" +
			"- Stale/superseded:\n  - (none)")
	got := ensureCompactionTodoSnapshot(summary, todos)
	if !strings.Contains(got, "### Runtime TODO snapshot") || !strings.Contains(got, "  - [completed] 1: second pass") {
		t.Fatalf("one classified line must not cover two runtime todos sharing an ID:\n%s", got)
	}
}
