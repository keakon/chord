package agent

import (
	"context"
	"os"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/keakon/chord/internal/ctxmgr"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func TestLoopModeToggleDoesNotChangeSystemPrompt(t *testing.T) {
	a := &MainAgent{
		ctxMgr:     ctxmgr.NewManager(8192, 0),
		outputCh:   make(chan AgentEvent, 8),
		stoppingCh: make(chan struct{}),
	}
	a.installedSysPrompt = "stable system prompt"
	a.ctxMgr.SetSystemPrompt(message.Message{Role: "system", Content: "stable system prompt"})

	a.EnableLoopMode("finish current task")
	if got := a.installedSysPrompt; got != "stable system prompt" {
		t.Fatalf("EnableLoopMode changed installedSysPrompt = %q", got)
	}
	if got := a.ctxMgr.SystemPrompt().Content; got != "stable system prompt" {
		t.Fatalf("EnableLoopMode changed ctxMgr system prompt = %q", got)
	}

	a.DisableLoopMode()
	if got := a.installedSysPrompt; got != "stable system prompt" {
		t.Fatalf("DisableLoopMode changed installedSysPrompt = %q", got)
	}
	if got := a.ctxMgr.SystemPrompt().Content; got != "stable system prompt" {
		t.Fatalf("DisableLoopMode changed ctxMgr system prompt = %q", got)
	}
}

func TestBuildSystemPromptDoesNotDependOnLoopMode(t *testing.T) {
	a := &MainAgent{}
	before := a.buildSystemPrompt()

	a.loopState.enableWithTarget("finish current task")
	a.loopState.DeferContinuationPromptUntilDone = true
	duringLoop := a.buildSystemPrompt()
	if duringLoop != before {
		t.Fatalf("buildSystemPrompt() changed after enabling loop mode")
	}

	a.loopState.disable()
	a.loopState.DeferContinuationPromptUntilDone = false
	a.todoItems = []tools.TodoItem{{ID: "1", Content: "ship feature", Status: "pending"}}
	withLoopArtifacts := a.buildSystemPrompt()
	if withLoopArtifacts != before {
		t.Fatalf("buildSystemPrompt() changed after stale loop artifacts")
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func parsePermissionNode(t *testing.T, src string) yaml.Node {
	t.Helper()
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(src), &doc); err != nil {
		t.Fatalf("parse permission: %v", err)
	}
	if doc.Kind == yaml.DocumentNode && len(doc.Content) > 0 {
		return *doc.Content[0]
	}
	return doc
}

type taskCreatorStub struct {
	agents []tools.AgentInfo
}

func (s taskCreatorStub) CreateSubAgent(_ context.Context, req tools.SubAgentRequest) (tools.TaskHandle, error) {
	return tools.TaskHandle{
		Status:          "started",
		TaskID:          "adhoc-1",
		AgentID:         "stub-subagent",
		Message:         "running in background",
		PlanTaskRef:     req.PlanTaskRef,
		SemanticTaskKey: req.SemanticTaskKey,
	}, nil
}

func (s taskCreatorStub) AvailableSubAgents() []tools.AgentInfo {
	return s.agents
}
