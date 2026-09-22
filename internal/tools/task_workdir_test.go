package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestDelegateToolPassesWorkdirThrough(t *testing.T) {
	creator := &countingTaskCreator{}
	_, err := NewDelegateTool(creator).Execute(context.Background(), json.RawMessage(`{"description":"implement feature","agent_type":"builder","expected_write_scope":{"path_prefix":["internal/agent"]},"workdir":"  feat-review  "}`))
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if creator.lastRequest.WorkDir != "feat-review" {
		t.Fatalf("WorkDir = %q, want the trimmed workdir argument", creator.lastRequest.WorkDir)
	}
}

func TestDelegateToolOmitsWorkdirWhenAbsent(t *testing.T) {
	creator := &countingTaskCreator{}
	if _, err := NewDelegateTool(creator).Execute(context.Background(), json.RawMessage(`{"description":"implement feature","agent_type":"builder","expected_write_scope":{"path_prefix":["internal/agent"]}}`)); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if creator.lastRequest.WorkDir != "" {
		t.Fatalf("WorkDir = %q, want empty when the argument is omitted", creator.lastRequest.WorkDir)
	}
}

func TestDelegateToolSchemaDescribesOptionalWorkdir(t *testing.T) {
	params := NewDelegateTool(taskTestCreator{}).Parameters()
	text := fmt.Sprint(params)
	for _, want := range []string{
		"workdir",
		"existing chord worktree",
		"never creates one",
		"inherits your working directory",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("Parameters() missing %q in %s", want, text)
		}
	}
	required, ok := params["required"].([]string)
	if !ok {
		t.Fatalf("required = %#v, want []string", params["required"])
	}
	for _, field := range required {
		if field == "workdir" {
			t.Fatal("workdir must stay optional: the worker inherits the delegating agent's directory when omitted")
		}
	}
}
