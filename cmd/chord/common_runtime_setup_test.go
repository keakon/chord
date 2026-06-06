package main

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/mcp"
	"github.com/keakon/chord/internal/tools"
)

func TestCreateRuntimeRequiresMainAgent(t *testing.T) {
	rt, err := createRuntime(&AppContext{Registry: tools.NewRegistry()})
	if err == nil || rt != nil {
		t.Fatalf("createRuntime() = (%v, %v), want nil runtime and error", rt, err)
	}
}

func TestCreateRuntimeRequiresRegistry(t *testing.T) {
	ac := newTestAppContext(t)
	ac.Registry = nil
	rt, err := createRuntime(ac)
	if err == nil || rt != nil {
		t.Fatalf("createRuntime() = (%v, %v), want nil runtime and error", rt, err)
	}
}

func TestCreateRuntimeWiresConfirmAndQuestionTools(t *testing.T) {
	ac := newTestAppContext(t)
	ac.Ctx, ac.Cancel = context.WithCancel(context.Background())
	defer ac.Cancel()
	ac.Registry = tools.NewRegistry()
	ac.Cfg = &config.Config{}

	rt, err := createRuntime(ac)
	if err != nil {
		t.Fatalf("createRuntime: %v", err)
	}
	defer rt.Close()

	if rt.Agent != ac.MainAgent {
		t.Fatal("runtime agent does not reference app context main agent")
	}
	if _, ok := ac.Registry.Get(tools.NameQuestion); !ok {
		t.Fatal("question tool was not registered into runtime registry")
	}

	confirmDone := make(chan error, 1)
	go func() {
		_, err := ac.MainAgent.AwaitConfirm(context.Background(), "Delete", `{}`, time.Second, nil, nil)
		confirmDone <- err
	}()
	confirmReq := waitForConfirmRequestEvent(t, ac.MainAgent.Events())
	ac.MainAgent.ResolveConfirm("allow", `{}`, "", "", confirmReq.RequestID)
	if err := <-confirmDone; err != nil {
		t.Fatalf("AwaitConfirm via runtime wiring: %v", err)
	}

	questionDone := make(chan error, 1)
	go func() {
		_, err := ac.Registry.Execute(context.Background(), tools.NameQuestion, []byte(`{"questions":[{"header":"h","question":"q","options":[{"label":"yes","description":"y"}]}]}`))
		questionDone <- err
	}()
	questionReq := waitForQuestionRequestEvent(t, ac.MainAgent.Events())
	ac.MainAgent.ResolveQuestion([]string{"yes"}, false, questionReq.RequestID)
	if err := <-questionDone; err != nil {
		t.Fatalf("Question tool via runtime wiring: %v", err)
	}
}

func TestRuntimeCloseIsNilSafe(t *testing.T) {
	(&Runtime{}).Close()
}

func TestEnsureRuntimeLSPNoopsWithoutConfig(t *testing.T) {
	ac := newTestAppContext(t)
	ac.Registry = tools.NewRegistry()
	ac.Cfg = &config.Config{}
	ensureRuntimeLSP(ac)
	if ac.LSPManager != nil {
		t.Fatal("LSP manager should stay nil without LSP config")
	}
	if _, ok := ac.Registry.Get(tools.NameLsp); ok {
		t.Fatal("LSP tool should not be registered without LSP config")
	}
}

func TestEnsureRuntimeLSPRegistersLSPAwareTools(t *testing.T) {
	ac := newTestAppContext(t)
	ac.Registry = tools.NewRegistry()
	ac.Cfg = &config.Config{
		LSP: config.LSPConfig{
			"sample-lsp": {
				Command:   "sample-lsp",
				FileTypes: []string{"go"},
			},
		},
	}
	ensureRuntimeLSP(ac)
	if ac.LSPManager == nil {
		t.Fatal("LSP manager was not initialized")
	}
	for _, name := range []string{tools.NameRead, tools.NameWrite, tools.NameEdit, tools.NameDelete, tools.NameLsp} {
		if _, ok := ac.Registry.Get(name); !ok {
			t.Fatalf("tool %s was not registered", name)
		}
	}
}

func TestEnsureRuntimeLSPKeepsExistingManager(t *testing.T) {
	ac := newTestAppContext(t)
	ac.Registry = tools.NewRegistry()
	ac.Cfg = &config.Config{
		LSP: config.LSPConfig{
			"sample-lsp": {Command: "sample-lsp"},
		},
	}
	ensureRuntimeLSP(ac)
	existing := ac.LSPManager
	ensureRuntimeLSP(ac)
	if ac.LSPManager != existing {
		t.Fatal("ensureRuntimeLSP replaced an existing manager")
	}
}

func TestStartRuntimeMCPNoopsWhenRuntimeIsIncomplete(t *testing.T) {
	tests := []struct {
		name string
		ac   *AppContext
	}{
		{name: "nil app context"},
		{name: "missing agent", ac: &AppContext{Registry: tools.NewRegistry()}},
		{name: "missing registry", ac: &AppContext{MainAgent: newTestAppContext(t).MainAgent}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			startRuntimeMCP(tt.ac)
		})
	}
}

func TestStartRuntimeMCPManualServerMarksDiscoveryReady(t *testing.T) {
	ac := newTestAppContext(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ac.Ctx = ctx
	ac.Cancel = cancel
	ac.Registry = tools.NewRegistry()
	ac.Cfg = &config.Config{}
	mgr, err := mcp.NewManager(context.Background(), nil)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	ac.MCPMgr = mgr
	ac.MCPConfigs = []mcp.ServerConfig{{Name: "manual", Manual: true}}

	startRuntimeMCP(ac)

	deadline := time.After(2 * time.Second)
	updates := 0
	for updates < 2 {
		select {
		case evt := <-ac.MainAgent.Events():
			if _, ok := evt.(agent.EnvStatusUpdateEvent); ok {
				updates++
			}
		case <-deadline:
			t.Fatalf("timed out waiting for MCP env status updates, got %d", updates)
		}
	}
}

func waitForConfirmRequestEvent(t *testing.T, ch <-chan agent.AgentEvent) agent.ConfirmRequestEvent {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case evt := <-ch:
			if req, ok := evt.(agent.ConfirmRequestEvent); ok {
				return req
			}
		case <-deadline:
			t.Fatal("timed out waiting for ConfirmRequestEvent")
		}
	}
}

func waitForQuestionRequestEvent(t *testing.T, ch <-chan agent.AgentEvent) agent.QuestionRequestEvent {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case evt := <-ch:
			if req, ok := evt.(agent.QuestionRequestEvent); ok {
				return req
			}
		case <-deadline:
			t.Fatal("timed out waiting for QuestionRequestEvent")
		}
	}
}

func TestCreateRuntimeQuestionToolRoundTripReturnsAnswers(t *testing.T) {
	ac := newTestAppContext(t)
	ac.Ctx, ac.Cancel = context.WithCancel(context.Background())
	defer ac.Cancel()
	ac.Registry = tools.NewRegistry()
	ac.Cfg = &config.Config{}

	if _, err := createRuntime(ac); err != nil {
		t.Fatalf("createRuntime: %v", err)
	}

	questionDone := make(chan string, 1)
	go func() {
		out, err := ac.Registry.Execute(context.Background(), tools.NameQuestion, []byte(`{"questions":[{"header":"h","question":"q","options":[{"label":"yes","description":"y"}]}]}`))
		if err != nil {
			questionDone <- err.Error()
			return
		}
		questionDone <- out
	}()
	questionReq := waitForQuestionRequestEvent(t, ac.MainAgent.Events())
	ac.MainAgent.ResolveQuestion([]string{"yes"}, false, questionReq.RequestID)
	out := <-questionDone
	var answers []tools.QuestionAnswer
	if err := json.Unmarshal([]byte(out), &answers); err != nil {
		t.Fatalf("unmarshal answers: %v", err)
	}
	if len(answers) != 1 || len(answers[0].Selected) != 1 || answers[0].Selected[0] != "yes" {
		t.Fatalf("answers = %#v, want yes", answers)
	}
}
