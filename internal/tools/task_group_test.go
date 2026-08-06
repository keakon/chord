package tools

import (
	"context"
	"encoding/json"
	"testing"
)

type taskGroupCreatorStub struct {
	request TaskGroupCreateRequest
}

func (s *taskGroupCreatorStub) CreateTaskGroup(_ context.Context, request TaskGroupCreateRequest) (TaskGroupCreateResult, error) {
	s.request = request
	return TaskGroupCreateResult{GroupID: "group-1", Members: []TaskGroupMember{{TaskID: "task-a", Attempt: 1}}, JoinPolicy: "all_settled"}, nil
}

func TestTaskGroupCreateToolNormalizesRequest(t *testing.T) {
	creator := &taskGroupCreatorStub{}
	tool := NewTaskGroupCreateTool(creator)
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"task_ids":[" task-b ","task-a","task-a"],"label":" phase ","semantic_key":" key "}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if out == "" {
		t.Fatal("expected JSON result")
	}
	if got := creator.request.TaskIDs; len(got) != 2 || got[0] != "task-b" || got[1] != "task-a" {
		t.Fatalf("task IDs = %#v", got)
	}
	if creator.request.Label != "phase" || creator.request.SemanticKey != "key" {
		t.Fatalf("request = %#v", creator.request)
	}
}

func TestTaskCollectToolAcceptsGroupID(t *testing.T) {
	collector := &taskCollectorStub{}
	tool := NewTaskCollectTool(collector)
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"group_id":" group-1 "}`)); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if collector.request.GroupID != "group-1" || len(collector.request.TaskIDs) != 0 {
		t.Fatalf("request = %#v", collector.request)
	}
}

func TestTaskCollectToolRejectsTasksAndGroupTogether(t *testing.T) {
	tool := NewTaskCollectTool(&taskCollectorStub{})
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"task_ids":["task-a"],"group_id":"group-1"}`)); err == nil {
		t.Fatal("expected mutually exclusive selector error")
	}
}
