package main

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/config"
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
