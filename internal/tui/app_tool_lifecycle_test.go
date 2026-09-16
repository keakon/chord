package tui

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func TestToolCallExecutionEventMarksToolQueuedWithoutAnimating(t *testing.T) {
	m := NewModelWithSize(nil, 80, 12)

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallStartEvent{
		ID:       "call-q1",
		Name:     "shell",
		AgentID:  "",
		ArgsJSON: `{"command":"command -v benchstat || true"}`,
	}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallExecutionEvent{
		ID:       "call-q1",
		Name:     "shell",
		ArgsJSON: `{"command":"command -v benchstat || true"}`,
		State:    agent.ToolCallExecutionStateQueued,
		AgentID:  "",
	}})

	block, ok := m.viewport.FindBlockByToolID("call-q1")
	if !ok {
		t.Fatal("expected queued tool block")
	}
	if block.ToolExecutionState != agent.ToolCallExecutionStateQueued {
		t.Fatalf("ToolExecutionState = %q, want %q", block.ToolExecutionState, agent.ToolCallExecutionStateQueued)
	}
	joined := stripANSI(strings.Join(block.Render(96, "●"), "\n"))
	if !strings.Contains(joined, "Queued") {
		t.Fatalf("expected queued header badge; got:\n%s", joined)
	}
	for _, seg := range activeToolSpinnerSegments {
		if strings.Contains(joined, seg) {
			t.Fatalf("queued tool should not render spinner segment %q; got:\n%s", seg, joined)
		}
	}
	if !block.StartedAt.IsZero() {
		t.Fatalf("queued tool StartedAt = %v, want zero", block.StartedAt)
	}
}

// The last call in a batch gets no successor, so nothing infers its completion.
// On the normal path finalize dispatches an execution event soon after, but when
// streaming ends without a finalized response the card would keep rendering a
// live char counter forever.
func TestStreamEndWithoutFinalizeSettlesReceivingToolCards(t *testing.T) {
	m := NewModelWithSize(nil, 100, 12)

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallStartEvent{
		ID:       "call-unsignalled",
		Name:     "delegate",
		AgentID:  "",
		ArgsJSON: `{"task":"orphan`,
	}})

	block, ok := m.viewport.FindBlockByToolID("call-unsignalled")
	if !ok {
		t.Fatal("expected delegate tool block")
	}
	if !block.toolArgumentsAreReceiving() {
		t.Fatalf("ToolExecutionState = %q, want receiving before the fallback runs", block.ToolExecutionState)
	}

	m.finalizeAssistantBlock()

	if block.ToolExecutionState != agent.ToolCallExecutionStateQueued {
		t.Fatalf("ToolExecutionState = %q, want %q after streaming ended", block.ToolExecutionState, agent.ToolCallExecutionStateQueued)
	}
	if block.ToolProgress != nil {
		t.Fatalf("ToolProgress = %+v, want nil — a stale char counter must not survive", block.ToolProgress)
	}
}

func TestFinalWriteUpdateCreatesFullPreviewWithoutStartEvent(t *testing.T) {
	m := NewModelWithSize(nil, 100, 12)
	args := `{"path":"src/demo.go","content":"package main\n"}`

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallUpdateEvent{
		ID:                "call-write-missing-start-1",
		Name:              tools.NameWrite,
		AgentID:           "",
		ArgsJSON:          args,
		ArgsStreamingDone: true,
	}})

	block, ok := m.viewport.FindBlockByToolID("call-write-missing-start-1")
	if !ok {
		t.Fatal("expected Write tool block")
	}
	block.Collapsed = false
	plain := stripANSI(strings.Join(block.Render(100, "●"), "\n"))
	if !strings.Contains(plain, "package main") {
		t.Fatalf("expected completed Write content preview after start-event recovery, got:\n%s", plain)
	}
}

func TestTodoWriteQueuedCardDoesNotAnimateWithGlobalSpinner(t *testing.T) {
	m := NewModelWithSize(nil, 80, 12)

	args := `{"todos":[{"id":"1","content":"a","status":"pending"}]}`
	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallStartEvent{
		ID:       "call-todo-static-queued-1",
		Name:     "todo_write",
		AgentID:  "",
		ArgsJSON: args,
	}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallUpdateEvent{
		ID:                "call-todo-static-queued-1",
		Name:              "todo_write",
		AgentID:           "",
		ArgsJSON:          args,
		ArgsStreamingDone: true,
	}})

	block, ok := m.viewport.FindBlockByToolID("call-todo-static-queued-1")
	if !ok {
		t.Fatal("expected todo tool block")
	}
	if !block.toolExecutionIsQueued() {
		t.Fatalf("expected queued tool state, got %q", block.ToolExecutionState)
	}

	joinedA := stripANSI(strings.Join(block.Render(96, "▖"), "\n"))
	joinedB := stripANSI(strings.Join(block.Render(96, "▘"), "\n"))
	if joinedA != joinedB {
		t.Fatalf("expected speculative queued todo card to avoid global spinner animation\nframe A:\n%s\n\nframe B:\n%s", joinedA, joinedB)
	}
	if strings.Contains(joinedA, "Queued") || strings.Contains(joinedB, "Queued") {
		t.Fatalf("did not expect execution-queue badge on speculative queued todo card\nframe A:\n%s\n\nframe B:\n%s", joinedA, joinedB)
	}
}

func TestTodoWriteCompletedCardDoesNotAnimateWithGlobalSpinner(t *testing.T) {
	m := NewModelWithSize(nil, 80, 12)

	args := `{"todos":[{"id":"1","content":"a","status":"completed"}]}`
	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallStartEvent{
		ID:       "call-todo-static-done-1",
		Name:     "todo_write",
		AgentID:  "",
		ArgsJSON: args,
	}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolResultEvent{
		CallID:   "call-todo-static-done-1",
		Name:     "todo_write",
		ArgsJSON: args,
		Result:   "- [x] 1. a\n",
		Status:   agent.ToolResultStatusSuccess,
		AgentID:  "",
	}})

	block, ok := m.viewport.FindBlockByToolID("call-todo-static-done-1")
	if !ok {
		t.Fatal("expected todo tool block")
	}
	if !block.ResultDone {
		t.Fatal("expected todo tool block to be marked done")
	}

	joinedA := stripANSI(strings.Join(block.Render(96, "▖"), "\n"))
	joinedB := stripANSI(strings.Join(block.Render(96, "▘"), "\n"))
	if joinedA != joinedB {
		t.Fatalf("completed todo card should not animate with global spinner\nframe A:\n%s\n\nframe B:\n%s", joinedA, joinedB)
	}
}

func TestToolCallUpdateEventArgsStreamingDoneDoesNotDowngradeFinishedToolBlock(t *testing.T) {
	m := NewModelWithSize(nil, 80, 12)
	args := `{"path":"internal/tui/app_agent_events.go","offset":0,"limit":1}`

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallStartEvent{
		ID:       "call-read-done-late-update-1",
		Name:     "read",
		AgentID:  "",
		ArgsJSON: args,
	}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolResultEvent{
		CallID:   "call-read-done-late-update-1",
		Name:     "read",
		AgentID:  "",
		ArgsJSON: args,
		Result:   "ok",
		Status:   agent.ToolResultStatusSuccess,
	}})

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallUpdateEvent{
		ID:                "call-read-done-late-update-1",
		Name:              "read",
		AgentID:           "",
		ArgsJSON:          args,
		ArgsStreamingDone: true,
	}})

	block, ok := m.viewport.FindBlockByToolID("call-read-done-late-update-1")
	if !ok {
		t.Fatal("expected read tool block")
	}
	if !block.ResultDone {
		t.Fatal("expected tool block to remain done")
	}
	if block.ToolExecutionState == agent.ToolCallExecutionStateQueued {
		t.Fatalf("did not expect finished tool to be downgraded to queued")
	}
	joined := stripANSI(strings.Join(block.Render(96, "▖"), "\n"))
	if strings.Contains(joined, "⏸") {
		t.Fatalf("did not expect queued glyph in finished tool render; got:\n%s", joined)
	}
	if !strings.Contains(joined, "✓") {
		t.Fatalf("expected finished tool render to contain ✓; got:\n%s", joined)
	}
}

func TestToolCallRendersStructuredElapsedFooterAfterCompletion(t *testing.T) {
	m := NewModelWithSize(nil, 80, 12)

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallStartEvent{
		ID:       "call-elapsed-running",
		Name:     "shell",
		AgentID:  "",
		ArgsJSON: `{"command":"sleep 10"}`,
	}})
	block, ok := m.viewport.FindBlockByToolID("call-elapsed-running")
	if !ok {
		t.Fatal("expected tool block")
	}
	if !block.StartedAt.IsZero() {
		t.Fatalf("speculative tool StartedAt = %v, want zero", block.StartedAt)
	}

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallExecutionEvent{
		ID:       "call-elapsed-running",
		Name:     "shell",
		ArgsJSON: `{"command":"sleep 10"}`,
		State:    agent.ToolCallExecutionStateRunning,
		AgentID:  "",
	}})
	if block.StartedAt.IsZero() {
		t.Fatal("running tool should record StartedAt")
	}
	block.StartedAt = time.Now().Add(-6 * time.Second)

	joined := stripANSI(strings.Join(block.Render(96, "●"), "\n"))
	if strings.Contains(joined, "⏱") {
		t.Fatalf("did not expect elapsed footer while tool still running; got:\n%s", joined)
	}

	block.StartedAt = time.Now().Add(-4 * time.Second)
	joined = stripANSI(strings.Join(block.Render(96, "●"), "\n"))
	if strings.Contains(joined, "⏱") {
		t.Fatalf("did not expect elapsed footer before completion; got:\n%s", joined)
	}

	block.StartedAt = time.Now().Add(-7 * time.Second)
	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolResultEvent{
		CallID:   "call-elapsed-running",
		Name:     "shell",
		ArgsJSON: `{"command":"sleep 10"}`,
		Result:   "done",
		Status:   agent.ToolResultStatusSuccess,
		AgentID:  "",
		Duration: 7 * time.Second,
	}})
	if block.SettledAt.IsZero() {
		t.Fatal("finished tool should record SettledAt")
	}
	joined = stripANSI(strings.Join(block.Render(96, "●"), "\n"))
	if !strings.Contains(joined, "⏱ 7s") {
		t.Fatalf("expected structured elapsed footer after completion; got:\n%s", joined)
	}
}

func TestJobOutputCardShowsElapsedOnlyAfterCompletion(t *testing.T) {
	m := NewModelWithSize(nil, 96, 12)

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallStartEvent{
		ID:       "call-job-output-running",
		Name:     "job_output",
		AgentID:  "",
		ArgsJSON: `{"job_id":"job-8"}`,
	}})
	block, ok := m.viewport.FindBlockByToolID("call-job-output-running")
	if !ok {
		t.Fatal("expected job_output block")
	}
	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallExecutionEvent{
		ID:       "call-job-output-running",
		Name:     "job_output",
		ArgsJSON: `{"job_id":"job-8"}`,
		State:    agent.ToolCallExecutionStateRunning,
		AgentID:  "",
	}})
	if block.StartedAt.IsZero() {
		t.Fatal("running job_output should record StartedAt")
	}
	block.StartedAt = time.Now().Add(-6 * time.Second)

	joined := stripANSI(strings.Join(block.Render(96, "●"), "\n"))
	if !strings.Contains(joined, "job-8") {
		t.Fatalf("running job_output card should keep the read handle; got:\n%s", joined)
	}
	if strings.Contains(joined, "⏱") {
		t.Fatalf("job_output card must not show a live timer while it runs; got:\n%s", joined)
	}

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolResultEvent{
		CallID:   "call-job-output-running",
		Name:     "job_output",
		ArgsJSON: `{"job_id":"job-8"}`,
		Result:   "line one\nline two\n[status: running]",
		Status:   agent.ToolResultStatusSuccess,
		AgentID:  "",
		Duration: 7 * time.Second,
	}})
	joined = stripANSI(strings.Join(block.Render(96, "●"), "\n"))
	if !strings.Contains(joined, "⏱ 7s") {
		t.Fatalf("finished job_output card should show the total elapsed; got:\n%s", joined)
	}
}

func TestFinishedJobOutputCardHidesSubSecondTotal(t *testing.T) {
	m := NewModelWithSize(nil, 96, 12)

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallStartEvent{
		ID:       "call-job-output-fast",
		Name:     "job_output",
		AgentID:  "",
		ArgsJSON: `{"job_id":"job-9"}`,
	}})
	block, ok := m.viewport.FindBlockByToolID("call-job-output-fast")
	if !ok {
		t.Fatal("expected job_output block")
	}
	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolResultEvent{
		CallID:   "call-job-output-fast",
		Name:     "job_output",
		ArgsJSON: `{"job_id":"job-9"}`,
		Result:   "line one\n[status: running]",
		Status:   agent.ToolResultStatusSuccess,
		AgentID:  "",
		Duration: 400 * time.Millisecond,
	}})
	joined := stripANSI(strings.Join(block.Render(96, "●"), "\n"))
	if !strings.Contains(joined, "job-9") {
		t.Fatalf("finished job_output card should keep the read handle; got:\n%s", joined)
	}
	if strings.Contains(joined, "⏱") {
		t.Fatalf("a sub-second total does not deserve a header slot; got:\n%s", joined)
	}
}

func TestToolResultSuccessIsNotOverwrittenByLateCancellation(t *testing.T) {
	m := NewModelWithSize(nil, 80, 12)

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallStartEvent{
		ID:       "call-late-cancel",
		Name:     "read",
		AgentID:  "",
		ArgsJSON: `{"path":"README.md"}`,
	}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolResultEvent{
		CallID:   "call-late-cancel",
		Name:     "read",
		AgentID:  "",
		ArgsJSON: `{"path":"README.md"}`,
		Result:   "success result",
		Status:   agent.ToolResultStatusSuccess,
	}})
	block, ok := m.viewport.FindBlockByToolID("call-late-cancel")
	if !ok {
		t.Fatal("expected tool block after success")
	}
	if block.ResultStatus != agent.ToolResultStatusSuccess {
		t.Fatalf("success ResultStatus = %q, want %q", block.ResultStatus, agent.ToolResultStatusSuccess)
	}
	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolResultEvent{
		CallID:   "call-late-cancel",
		Name:     "read",
		AgentID:  "",
		ArgsJSON: `{"path":"README.md"}`,
		Result:   "Error:\ncontext canceled",
		Status:   agent.ToolResultStatusError,
	}})

	block, ok = m.viewport.FindBlockByToolID("call-late-cancel")
	if !ok {
		t.Fatal("expected tool block")
	}
	if block.ResultStatus != agent.ToolResultStatusSuccess {
		t.Fatalf("ResultStatus = %q, want %q", block.ResultStatus, agent.ToolResultStatusSuccess)
	}
	if block.ResultContent != "success result" {
		t.Fatalf("ResultContent = %q, want original success result", block.ResultContent)
	}
}

func TestToolCallUpdateEventCreatesMissingToolBlock(t *testing.T) {
	m := NewModelWithSize(nil, 80, 12)

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallUpdateEvent{
		ID:       "call-missing-update-1",
		Name:     "shell",
		AgentID:  "",
		ArgsJSON: `{"command":"echo hello"}`,
	}})

	block, ok := m.viewport.FindBlockByToolID("call-missing-update-1")
	if !ok {
		t.Fatal("expected ToolCallUpdateEvent to create a missing tool block")
	}
	if block.ToolExecutionState != agent.ToolCallExecutionStateRunning {
		t.Fatalf("ToolExecutionState = %q, want %q", block.ToolExecutionState, agent.ToolCallExecutionStateRunning)
	}
	if block.ToolProgress == nil {
		t.Fatal("expected streaming arg progress on created tool block")
	}
}

func TestToolCallUpdateEventArgsStreamingDoneCreatesQueuedNonAnimatingToolBlock(t *testing.T) {
	m := NewModelWithSize(nil, 80, 12)
	args := `{"todos":[{"id":"1","content":"a","status":"pending"}]}`

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallUpdateEvent{
		ID:                "call-missing-update-done-1",
		Name:              "todo_write",
		AgentID:           "",
		ArgsJSON:          args,
		ArgsStreamingDone: true,
	}})

	block, ok := m.viewport.FindBlockByToolID("call-missing-update-done-1")
	if !ok {
		t.Fatal("expected ToolCallUpdateEvent to create a missing tool block")
	}
	if block.ToolExecutionState != agent.ToolCallExecutionStateQueued {
		t.Fatalf("ToolExecutionState = %q, want %q", block.ToolExecutionState, agent.ToolCallExecutionStateQueued)
	}
	if block.ToolProgress != nil {
		t.Fatalf("expected no transient arg progress on queued block, got %+v", *block.ToolProgress)
	}
	joinedA := stripANSI(strings.Join(block.Render(96, "▖"), "\n"))
	joinedB := stripANSI(strings.Join(block.Render(96, "▘"), "\n"))
	if joinedA != joinedB {
		t.Fatalf("expected queued block created from final arg update to avoid global spinner animation\nframe A:\n%s\n\nframe B:\n%s", joinedA, joinedB)
	}
	if strings.Contains(joinedA, "Queued") || strings.Contains(joinedB, "Queued") {
		t.Fatalf("did not expect execution-queue badge on queued block created from final arg update\nframe A:\n%s\n\nframe B:\n%s", joinedA, joinedB)
	}
}

func TestToolCallExecutionEventCreatesMissingToolBlock(t *testing.T) {
	m := NewModelWithSize(nil, 80, 12)

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallExecutionEvent{
		ID:       "call-missing-exec-1",
		Name:     "read",
		AgentID:  "",
		ArgsJSON: `{"path":"go.mod"}`,
		State:    agent.ToolCallExecutionStateQueued,
	}})

	block, ok := m.viewport.FindBlockByToolID("call-missing-exec-1")
	if !ok {
		t.Fatal("expected ToolCallExecutionEvent to create a missing tool block")
	}
	if block.ToolExecutionState != agent.ToolCallExecutionStateQueued {
		t.Fatalf("ToolExecutionState = %q, want %q", block.ToolExecutionState, agent.ToolCallExecutionStateQueued)
	}
	if block.ToolProgress != nil {
		t.Fatalf("expected no transient arg progress on execution-state fallback block, got %+v", *block.ToolProgress)
	}
}

func TestToolProgressEventUpdatesRunningToolCard(t *testing.T) {
	m := NewModelWithSize(nil, 80, 12)

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallStartEvent{
		ID:       "call-progress-1",
		Name:     "delete",
		AgentID:  "",
		ArgsJSON: `{"paths":["a.txt","b.txt","c.txt"],"reason":"cleanup"}`,
	}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolProgressEvent{
		CallID:  "call-progress-1",
		Name:    "delete",
		AgentID: "",
		Progress: agent.ToolProgressSnapshot{
			Label:   "paths",
			Current: 3,
			Total:   8,
		},
	}})

	block, ok := m.viewport.FindBlockByToolID("call-progress-1")
	if !ok {
		t.Fatal("expected running tool block")
	}
	if block.ToolProgress == nil {
		t.Fatal("expected tool progress on running block")
	}
	joined := stripANSI(strings.Join(block.Render(96, "●"), "\n"))
	if !strings.Contains(joined, "3 / 8 paths") {
		t.Fatalf("expected tool progress in rendered card; got:\n%s", joined)
	}
}

func TestToolProgressEventPreservesProgressInNarrowWidthByTruncatingHeader(t *testing.T) {
	m := NewModelWithSize(nil, 80, 12)

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallStartEvent{
		ID:       "call-progress-tight-1",
		Name:     "delete",
		AgentID:  "",
		ArgsJSON: `{"paths":["a-very-long-file-name.txt","b-very-long-file-name.txt","c-very-long-file-name.txt","d-very-long-file-name.txt"],"reason":"cleanup stale generated artifacts from previous long-running benchmark pass"}`,
	}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolProgressEvent{
		CallID:  "call-progress-tight-1",
		Name:    "delete",
		AgentID: "",
		Progress: agent.ToolProgressSnapshot{
			Label:   "paths",
			Current: 3,
			Total:   8,
		},
	}})

	block, ok := m.viewport.FindBlockByToolID("call-progress-tight-1")
	if !ok {
		t.Fatal("expected running tool block")
	}
	joined := stripANSI(strings.Join(block.Render(44, "●"), "\n"))
	if !strings.Contains(joined, "3 / 8 paths") {
		t.Fatalf("expected narrow render to preserve progress; got:\n%s", joined)
	}
}

func TestToolProgressEventDoesNotUpdateQueuedToolCard(t *testing.T) {
	m := NewModelWithSize(nil, 80, 12)

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallStartEvent{
		ID:       "call-progress-q1",
		Name:     "delete",
		AgentID:  "",
		ArgsJSON: `{"paths":["a.txt","b.txt"],"reason":"cleanup"}`,
	}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallExecutionEvent{
		ID:       "call-progress-q1",
		Name:     "delete",
		ArgsJSON: `{"paths":["a.txt","b.txt"],"reason":"cleanup"}`,
		State:    agent.ToolCallExecutionStateQueued,
		AgentID:  "",
	}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolProgressEvent{
		CallID:  "call-progress-q1",
		Name:    "delete",
		AgentID: "",
		Progress: agent.ToolProgressSnapshot{
			Label:   "paths",
			Current: 1,
			Total:   2,
		},
	}})

	block, ok := m.viewport.FindBlockByToolID("call-progress-q1")
	if !ok {
		t.Fatal("expected queued tool block")
	}
	if block.ToolProgress != nil {
		t.Fatal("did not expect queued tool to keep progress")
	}
	joined := stripANSI(strings.Join(block.Render(96, "●"), "\n"))
	if strings.Contains(joined, "1 / 2 paths") {
		t.Fatalf("queued tool should not render progress; got:\n%s", joined)
	}
}

func TestToolResultEventClearsToolProgress(t *testing.T) {
	m := NewModelWithSize(nil, 80, 12)

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallStartEvent{
		ID:       "call-progress-clear-1",
		Name:     "delete",
		AgentID:  "",
		ArgsJSON: `{"paths":["a.txt","b.txt","c.txt"],"reason":"cleanup"}`,
	}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolProgressEvent{
		CallID:  "call-progress-clear-1",
		Name:    "delete",
		AgentID: "",
		Progress: agent.ToolProgressSnapshot{
			Label:   "paths",
			Current: 2,
			Total:   3,
		},
	}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolResultEvent{
		CallID:   "call-progress-clear-1",
		Name:     "delete",
		ArgsJSON: `{"paths":["a.txt","b.txt","c.txt"],"reason":"cleanup"}`,
		Result:   "Deleted (3):\n- a.txt\n- b.txt\n- c.txt",
		Status:   agent.ToolResultStatusSuccess,
		AgentID:  "",
	}})

	block, ok := m.viewport.FindBlockByToolID("call-progress-clear-1")
	if !ok {
		t.Fatal("expected tool block")
	}
	if block.ToolProgress != nil {
		t.Fatalf("expected ToolProgress to be cleared, got %+v", *block.ToolProgress)
	}
	joined := stripANSI(strings.Join(block.Render(96, "●"), "\n"))
	if strings.Contains(joined, "2 / 3 paths") {
		t.Fatalf("completed tool should not render stale progress; got:\n%s", joined)
	}
}

func TestQuestionToolResultAdoptsPendingQuestionBlockByName(t *testing.T) {
	m := NewModelWithSize(nil, 80, 12)

	questionArgs := `{"questions":[{"header":"Provider compatibility","question":"continue?","options":[{"label":"yes"}]}]}`
	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallStartEvent{
		ID:       "call-question-stream-1",
		Name:     "question",
		AgentID:  "",
		ArgsJSON: questionArgs,
	}})

	block, ok := m.viewport.FindBlockByToolID("call-question-stream-1")
	if !ok {
		t.Fatal("expected pending Question block")
	}
	block.ToolID = ""
	block.InvalidateCache()
	m.updateViewportBlock(block)

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolResultEvent{
		CallID:   "call-question-final-1",
		Name:     "question",
		AgentID:  "",
		ArgsJSON: questionArgs,
		Result:   `[{"header":"Provider compatibility","selected":["yes"]}]`,
		Status:   agent.ToolResultStatusSuccess,
	}})

	blocks := m.viewport.visibleBlocks()
	questionBlocks := 0
	for _, b := range blocks {
		if b != nil && b.Type == BlockToolCall && b.ToolName == "question" {
			questionBlocks++
			block = b
		}
	}
	if questionBlocks != 1 {
		t.Fatalf("Question tool blocks = %d, want 1", questionBlocks)
	}
	if block.ToolID != "call-question-final-1" {
		t.Fatalf("Question ToolID = %q, want call-question-final-1", block.ToolID)
	}
	if !block.ResultDone {
		t.Fatal("expected Question block ResultDone after tool result")
	}
	if m.viewport.HasPendingToolWork() {
		t.Fatal("expected no pending tool work after Question result")
	}
}

func TestDuplicateToolResultEventIsIgnoredAfterCompletion(t *testing.T) {
	m := NewModelWithSize(nil, 80, 12)

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallStartEvent{
		ID:       "call-read-dedupe-1",
		Name:     "read",
		AgentID:  "",
		ArgsJSON: `{"path":"internal/tui/app_agent_events.go","limit":1}`,
	}})
	resultEvt := agent.ToolResultEvent{
		CallID:   "call-read-dedupe-1",
		Name:     "read",
		AgentID:  "",
		ArgsJSON: `{"path":"internal/tui/app_agent_events.go","limit":1}`,
		Result:   "ok",
		Status:   agent.ToolResultStatusSuccess,
	}
	_ = m.handleAgentEvent(agentEventMsg{event: resultEvt})
	_ = m.handleAgentEvent(agentEventMsg{event: resultEvt})

	blocks := m.viewport.visibleBlocks()
	toolBlocks := 0
	for _, b := range blocks {
		if b != nil && b.ToolID == "call-read-dedupe-1" {
			toolBlocks++
		}
	}
	if toolBlocks != 1 {
		t.Fatalf("tool blocks for call-read-dedupe-1 = %d, want 1", toolBlocks)
	}
	block, ok := m.viewport.FindBlockByToolID("call-read-dedupe-1")
	if !ok {
		t.Fatal("expected read tool block")
	}
	if !block.ResultDone {
		t.Fatal("expected read tool block to stay completed")
	}
}

func TestSingleHiddenLineGenericCompactToolCannotBeCollapsedByToggleAtWidth(t *testing.T) {
	// Grep/glob are intentionally not part of this heuristic: their count-based
	// summaries must stay collapsible (see TestGrepGlobForceExpandedHeuristic-
	// DoesNotBlockCollapse).
	tests := []struct {
		name     string
		toolName string
		content  string
		result   string
	}{
		{
			name:     "delete",
			toolName: "delete",
			content:  `{"paths":["examples/compression-config.yaml"],"reason":"remove obsolete example"}`,
			result:   "delete completed.\n\nDeleted (1):\n- examples/compression-config.yaml",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			block := &Block{
				ID:                     1,
				Type:                   BlockToolCall,
				ToolName:               tt.toolName,
				Content:                tt.content,
				ResultContent:          tt.result,
				ResultDone:             true,
				ToolCallDetailExpanded: true,
			}

			block.ToggleAtWidth(120)

			if !block.ToolCallDetailExpanded {
				t.Fatal("single hidden line compact tool should remain expanded after toggle")
			}
		})
	}
}

func TestToolResultEventDeleteTracksDeletedFileWithoutFakeLineCount(t *testing.T) {
	m := NewModelWithSize(nil, 80, 12)
	m.sidebar.Update(nil, "main", "builder")

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallStartEvent{
		ID:       "call-delete-file-1",
		Name:     "delete",
		ArgsJSON: `{"paths":["obsolete.go"],"reason":"cleanup"}`,
	}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolResultEvent{
		CallID:   "call-delete-file-1",
		Name:     "delete",
		ArgsJSON: `{"paths":["obsolete.go"],"reason":"cleanup"}`,
		Result:   "delete completed.\n\nDeleted (1):\n- obsolete.go",
		Status:   agent.ToolResultStatusSuccess,
	}})

	edits := m.sidebar.CurrentAgentFiles()
	if len(edits) != 1 {
		t.Fatalf("changed files = %d, want 1: %+v", len(edits), edits)
	}
	if edits[0].Path != "obsolete.go" || !edits[0].Deleted {
		t.Fatalf("changed file = %+v, want deleted obsolete.go", edits[0])
	}
	if edits[0].Added != 0 || edits[0].Removed != 0 {
		t.Fatalf("deleted file stats = +%d -%d, want +0 -0", edits[0].Added, edits[0].Removed)
	}
}

func TestToolResultEventEditTracksEditedFile(t *testing.T) {
	m := NewModelWithSize(nil, 80, 12)
	m.sidebar.Update(nil, "main", "builder")
	patch := "@@\n-old\n+new\n"
	args, _ := json.Marshal(map[string]string{"path": "src/demo.go", "patch": patch})

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallStartEvent{
		ID:       "call-apply-patch-1",
		Name:     tools.NameEdit,
		ArgsJSON: string(args),
	}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolResultEvent{
		CallID:      "call-apply-patch-1",
		Name:        tools.NameEdit,
		ArgsJSON:    string(args),
		Result:      "Applied patch to src/demo.go (+1 -1)",
		Status:      agent.ToolResultStatusSuccess,
		Diff:        "--- src/demo.go\n+++ src/demo.go\n@@ -1 +1 @@\n-old\n+new\n",
		DiffAdded:   1,
		DiffRemoved: 1,
	}})

	edits := m.sidebar.CurrentAgentFiles()
	if len(edits) != 1 {
		t.Fatalf("changed files = %d, want 1: %+v", len(edits), edits)
	}
	if !strings.HasSuffix(edits[0].Path, "src/demo.go") || edits[0].Deleted {
		t.Fatalf("changed file = %+v, want edited src/demo.go", edits[0])
	}
	if edits[0].Added != 1 || edits[0].Removed != 1 {
		t.Fatalf("edited file stats = +%d -%d, want +1 -1", edits[0].Added, edits[0].Removed)
	}
}

func TestToolResultEventShowsEditedBeforeApprovalSummary(t *testing.T) {
	m := NewModelWithSize(nil, 80, 12)

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallStartEvent{
		ID:       "call-edited-1",
		Name:     "delete",
		AgentID:  "",
		ArgsJSON: `{"paths":["a.txt"],"reason":"cleanup"}`,
	}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolResultEvent{
		CallID:   "call-edited-1",
		Name:     "delete",
		ArgsJSON: `{"paths":["a.txt"],"reason":"cleanup"}`,
		Result:   "Deleted (1):\n- a.txt",
		Status:   agent.ToolResultStatusSuccess,
		AgentID:  "",
		Audit: &message.ToolArgsAudit{
			OriginalArgsJSON:  `{"paths":["old.txt"],"reason":"cleanup"}`,
			EffectiveArgsJSON: `{"paths":["a.txt"],"reason":"cleanup"}`,
			UserModified:      true,
		},
	}})

	block, ok := m.viewport.FindBlockByToolID("call-edited-1")
	if !ok {
		t.Fatal("expected tool block")
	}
	if block.Audit == nil || !block.Audit.UserModified {
		t.Fatalf("block.Audit = %#v, want user-modified audit", block.Audit)
	}
	joined := stripANSI(strings.Join(block.Render(96, "●"), "\n"))
	if !strings.Contains(joined, "edited before approval") {
		t.Fatalf("expected edited-before-approval marker, got:\n%s", joined)
	}
}

func TestStreamRollbackPreservesCancelledSpeculativeToolCard(t *testing.T) {
	m := NewModelWithSize(nil, 80, 12)

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallStartEvent{
		ID:      "call-1",
		Name:    "read",
		AgentID: "",
	}})
	if _, ok := m.viewport.FindBlockByToolID("call-1"); !ok {
		t.Fatal("expected speculative tool block after ToolCallStartEvent")
	}
	if got := len(m.viewport.visibleBlocks()); got != 1 {
		t.Fatalf("len(visibleBlocks()) = %d, want 1", got)
	}

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolResultEvent{
		CallID:   "call-1",
		Name:     "read",
		Status:   agent.ToolResultStatusCancelled,
		Result:   "Cancelled",
		AgentID:  "",
		ArgsJSON: `{"path":"internal/llm/provider.go"}`,
	}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.StreamRollbackEvent{AgentID: ""}})

	block, ok := m.viewport.FindBlockByToolID("call-1")
	if !ok {
		t.Fatal("expected cancelled tool block to remain after rollback")
	}
	if !block.ResultDone {
		t.Fatal("expected tool block ResultDone after cancelled result")
	}
	if block.ResultStatus != agent.ToolResultStatusCancelled {
		t.Fatalf("ResultStatus = %q, want %q", block.ResultStatus, agent.ToolResultStatusCancelled)
	}
	if m.currentThinkingBlock != nil || m.currentAssistantBlock != nil {
		t.Fatal("expected rollback to clear only in-flight thinking/assistant blocks")
	}
}

func TestStreamRollbackPreservesCancelledSpeculativeWriteToolCard(t *testing.T) {
	m := NewModelWithSize(nil, 80, 12)

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallStartEvent{
		ID:       "write-call-1",
		Name:     "write",
		AgentID:  "",
		ArgsJSON: `{"path":".chord/plans/plan-002.md","content":"partial"}`,
	}})
	block, ok := m.viewport.FindBlockByToolID("write-call-1")
	if !ok {
		t.Fatal("expected speculative Write tool block after ToolCallStartEvent")
	}
	if block.ResultDone {
		t.Fatal("expected speculative Write block to start pending")
	}

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolResultEvent{
		CallID:   "write-call-1",
		Name:     "write",
		Status:   agent.ToolResultStatusCancelled,
		Result:   "Cancelled",
		AgentID:  "",
		ArgsJSON: `{"path":".chord/plans/plan-002.md","content":"partial"}`,
	}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.StreamRollbackEvent{AgentID: ""}})

	block, ok = m.viewport.FindBlockByToolID("write-call-1")
	if !ok {
		t.Fatal("expected cancelled Write tool block to remain after rollback")
	}
	if !block.ResultDone {
		t.Fatal("expected Write block ResultDone after cancelled result")
	}
	if block.ResultStatus != agent.ToolResultStatusCancelled {
		t.Fatalf("ResultStatus = %q, want %q", block.ResultStatus, agent.ToolResultStatusCancelled)
	}
	if block.ResultContent != "Cancelled" {
		t.Fatalf("ResultContent = %q, want Cancelled", block.ResultContent)
	}
	if block.ToolName != tools.NameWrite {
		t.Fatalf("ToolName = %q, want Write", block.ToolName)
	}
	if m.currentThinkingBlock != nil || m.currentAssistantBlock != nil {
		t.Fatal("expected rollback to clear only in-flight thinking/assistant blocks")
	}
}

func TestToolCallDiscardEventRemovesSpeculativeToolCard(t *testing.T) {
	m := NewModelWithSize(nil, 80, 12)

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallStartEvent{
		ID:       "call-discard-1",
		Name:     "read",
		AgentID:  "",
		ArgsJSON: `{"path":"README.md"}`,
	}})
	if _, ok := m.viewport.FindBlockByToolID("call-discard-1"); !ok {
		t.Fatal("expected speculative tool block after ToolCallStartEvent")
	}

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallDiscardEvent{
		ID:      "call-discard-1",
		Name:    "read",
		AgentID: "",
		Reason:  "not_in_context",
	}})
	if _, ok := m.viewport.FindBlockByToolID("call-discard-1"); ok {
		t.Fatal("expected speculative tool block to be removed after ToolCallDiscardEvent")
	}
	if got := len(m.viewport.visibleBlocks()); got != 0 {
		t.Fatalf("len(visibleBlocks()) = %d, want 0", got)
	}
}

func TestTaskToolResultErrorClearsPendingPlaceholder(t *testing.T) {
	m := NewModelWithSize(nil, 80, 12)

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallStartEvent{
		ID:      "task-call-1",
		Name:    "delegate",
		AgentID: "",
	}})
	if got := m.sidebar.PendingTasks(); got != 1 {
		t.Fatalf("PendingTasks after Delegate start = %d, want 1", got)
	}

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolResultEvent{
		CallID:   "task-call-1",
		Name:     "delegate",
		Status:   agent.ToolResultStatusError,
		Result:   "max concurrent agents reached",
		AgentID:  "",
		ArgsJSON: `{"description":"child work","agent_type":"worker"}`,
	}})
	if got := m.sidebar.PendingTasks(); got != 0 {
		t.Fatalf("PendingTasks after Delegate error = %d, want 0", got)
	}
}

func TestTaskToolResultWithoutAgentIDClearsPendingPlaceholder(t *testing.T) {
	m := NewModelWithSize(nil, 80, 12)

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallStartEvent{
		ID:      "task-call-2",
		Name:    "delegate",
		AgentID: "",
	}})
	if got := m.sidebar.PendingTasks(); got != 1 {
		t.Fatalf("PendingTasks after Delegate start = %d, want 1", got)
	}

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolResultEvent{
		CallID:   "task-call-2",
		Name:     "delegate",
		Status:   agent.ToolResultStatusSuccess,
		Result:   `{"status":"child_limit_reached","message":"direct active child limit reached (max_children=10)"}`,
		AgentID:  "",
		ArgsJSON: `{"description":"child work","agent_type":"worker"}`,
	}})
	if got := m.sidebar.PendingTasks(); got != 0 {
		t.Fatalf("PendingTasks after Delegate child_limit_reached = %d, want 0", got)
	}
}

func TestStreamRollbackRemovesCurrentAssistantAndThinkingBlocks(t *testing.T) {
	m := NewModelWithSize(nil, 80, 12)

	_ = m.handleAgentEvent(agentEventMsg{event: agent.StreamThinkingDeltaEvent{Text: "think", AgentID: ""}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.StreamTextEvent{Text: "answer", AgentID: ""}})
	if m.currentThinkingBlock == nil || m.currentAssistantBlock == nil {
		t.Fatal("expected in-flight thinking and assistant blocks before rollback")
	}
	if got := len(m.viewport.visibleBlocks()); got != 2 {
		t.Fatalf("len(visibleBlocks()) = %d, want 2 before rollback", got)
	}

	_ = m.handleAgentEvent(agentEventMsg{event: agent.StreamRollbackEvent{AgentID: ""}})
	if m.currentThinkingBlock != nil {
		t.Fatal("expected currentThinkingBlock cleared after rollback")
	}
	if m.currentAssistantBlock != nil {
		t.Fatal("expected currentAssistantBlock cleared after rollback")
	}
	if got := len(m.viewport.visibleBlocks()); got != 0 {
		t.Fatalf("len(visibleBlocks()) = %d, want 0 after rollback", got)
	}
}

func TestStreamRollbackRemovesSettledStreamingThinkingBlock(t *testing.T) {
	m := NewModelWithSize(nil, 80, 12)

	_ = m.handleAgentEvent(agentEventMsg{event: agent.StreamThinkingDeltaEvent{Text: "failed thought", AgentID: ""}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.StreamThinkingEvent{AgentID: ""}})
	if m.currentThinkingBlock != nil {
		t.Fatal("expected currentThinkingBlock detached after thinking end")
	}
	blocks := m.viewport.visibleBlocks()
	if len(blocks) != 1 {
		t.Fatalf("len(visibleBlocks()) = %d, want 1 before rollback", len(blocks))
	}
	if blocks[0].Type != BlockThinking || blocks[0].Streaming {
		t.Fatalf("block = %#v, want settled thinking block", blocks[0])
	}

	_ = m.handleAgentEvent(agentEventMsg{event: agent.StreamRollbackEvent{AgentID: ""}})
	if got := len(m.viewport.visibleBlocks()); got != 0 {
		t.Fatalf("len(visibleBlocks()) = %d, want 0 after rollback", got)
	}

	_ = m.handleAgentEvent(agentEventMsg{event: agent.StreamThinkingDeltaEvent{Text: "retry thought", AgentID: ""}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.StreamThinkingEvent{AgentID: ""}})
	blocks = m.viewport.visibleBlocks()
	if len(blocks) != 1 {
		t.Fatalf("len(visibleBlocks()) = %d, want 1 after retry", len(blocks))
	}
	if blocks[0].ThinkingBlockIndex != 0 {
		t.Fatalf("ThinkingBlockIndex = %d, want retry to restart at 0", blocks[0].ThinkingBlockIndex)
	}
}

func TestStreamTextEventDoesNotTriggerInlineImageRefresh(t *testing.T) {
	ApplyTheme(DefaultTheme())
	caps := TerminalImageCapabilities{Backend: ImageBackendITerm2, SupportsInline: true, SupportsFullscreen: true}
	setCurrentTerminalImageCapabilities(caps)
	t.Cleanup(func() {
		setCurrentTerminalImageCapabilities(TerminalImageCapabilities{Backend: ImageBackendNone})
	})

	m := NewModelWithSize(nil, 80, 12)
	m.imageCaps = caps
	m.mode = ModeNormal

	m.viewport.AppendBlock(&Block{
		ID:         1,
		Type:       BlockUser,
		ImageCount: 1,
		ImageParts: []BlockImagePart{{
			FileName: "sample.png",
			MimeType: "image/png",
			Data:     makeTestPNG(t),
		}},
	})

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.StreamTextEvent{Text: "hello"}})
	if cmd == nil {
		t.Fatal("stream text should schedule a coalesced UI flush")
	}
	if m.currentAssistantBlock == nil || m.currentAssistantBlock.Content != "hello" {
		t.Fatalf("assistant block content = %#v, want hello", m.currentAssistantBlock)
	}
}
