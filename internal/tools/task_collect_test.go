package tools

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

type taskCollectorStub struct {
	request TaskCollectRequest
}

func (s *taskCollectorStub) CollectTasks(_ context.Context, request TaskCollectRequest) (TaskCollectResult, error) {
	s.request = request
	return TaskCollectResult{AllSettled: true, AllDurable: true}, nil
}

func TestTaskCollectToolNormalizesRequest(t *testing.T) {
	collector := &taskCollectorStub{}
	tool := NewTaskCollectTool(collector)
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"task_ids":[" task-b ","task-a","task-a"],"wait":true,"timeout_ms":25}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if out == "" {
		t.Fatal("expected JSON result")
	}
	if got, want := collector.request.TaskIDs, []string{"task-b", "task-a"}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("task IDs = %#v, want %#v", got, want)
	}
	if !collector.request.Wait || collector.request.Timeout != 25*time.Millisecond {
		t.Fatalf("request = %#v", collector.request)
	}
}

func TestTaskCollectToolUsesBoundedDefaultTimeout(t *testing.T) {
	collector := &taskCollectorStub{}
	tool := NewTaskCollectTool(collector)
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"task_ids":["task-a"],"wait":true}`)); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if collector.request.Timeout != defaultCollectTimeout {
		t.Fatalf("timeout = %v, want %v", collector.request.Timeout, defaultCollectTimeout)
	}
}

func TestTaskCollectToolRejectsEmptyMember(t *testing.T) {
	tool := NewTaskCollectTool(&taskCollectorStub{})
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"task_ids":[" "]}`)); err == nil {
		t.Fatal("expected empty task ID error")
	}
}

func TestTaskCollectToolRejectsTimeoutBeforeDurationConversion(t *testing.T) {
	tool := NewTaskCollectTool(&taskCollectorStub{})
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"task_ids":["task-a"],"wait":true,"timeout_ms":9223372036854775807}`)); err == nil {
		t.Fatal("expected oversized timeout error")
	}
}
