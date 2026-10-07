package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func TestQuestionAsyncFlowContinuesThenParksWithoutPolling(t *testing.T) {
	testQuestionAsyncAnswer(t, false)
}

func TestQuestionAsyncAnswerDuringRequestResumesBeforeCompletion(t *testing.T) {
	testQuestionAsyncAnswer(t, true)
}

func testQuestionAsyncAnswer(t *testing.T, inFlight bool) {
	t.Helper()
	a := newTestMainAgent(t, t.TempDir())
	args, _ := json.Marshal(tools.QuestionArgs{Questions: questionItems("Choice"), Wait: new(false)})
	provider := &blockingStreamProvider{calls: []scriptedStreamCall{
		{resp: &message.Response{ToolCalls: []message.ToolCall{{ID: "question-1", Name: tools.NameQuestion, Args: args}}, StopReason: "tool_calls"}},
		{resp: &message.Response{Content: "Independent work finished", StopReason: "stop"}, holdAfterStreams: inFlight},
		{resp: &message.Response{Content: "Decision applied", StopReason: "stop"}},
	}}
	if inFlight {
		provider.streamedCh = make(chan struct{})
		provider.releaseCh = make(chan struct{})
	}
	cfg := llm.NewProviderConfig("sample", config.ProviderConfig{Type: config.ProviderTypeChatCompletions, Models: map[string]config.ModelConfig{"test-model": {Limit: config.ModelLimit{Context: 128000, Output: 4096}}}}, []string{"test-key"})
	a.swapLLMClientWithRef(llm.NewClient(cfg, provider, "test-model", 4096, ""), "test-model", 128000, "sample/test-model")
	a.tools.Register(tools.NewQuestionTool(a.ExecuteQuestion))
	a.markAgentsMDReady()
	a.MarkSkillsReady()
	a.markMCPReady()
	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- a.Run(ctx) }()
	defer func() {
		cancel()
		if err := a.Shutdown(5 * time.Second); err != nil {
			t.Errorf("shutdown: %v", err)
		}
		<-runDone
	}()
	a.SendUserMessage("Do independent work and request one decision")
	deadline := time.After(10 * time.Second)
	id := ""
	parked := false
	independent := false
	started := false
	streamed := provider.streamedCh
	for id == "" || (inFlight && !started) || (!inFlight && (!parked || !independent)) {
		select {
		case <-streamed:
			started = true
			streamed = nil
		case event := <-a.Events():
			switch e := event.(type) {
			case QuestionStateEvent:
				if e.Question.Outcome == "" {
					id = e.Question.ID
				}
			case AssistantMessageEvent:
				if e.Text == "Independent work finished" {
					independent = true
				}
			case AgentActivityEvent:
				if e.Type == ActivityWaitingInput {
					parked = true
				}
			}
		case <-deadline:
			t.Fatal("independent work did not finish before waiting")
		}
	}
	requests, _ := provider.snapshot()
	if len(requests) != 2 {
		t.Fatalf("requests before answer=%d, want 2", len(requests))
	}
	receipt, err := a.ApplyQuestionOperation(ctx, QuestionOperation{Operation: QuestionOpAnswer, OperationID: "answer-1", QuestionID: id, Answers: []string{"no"}})
	if err != nil || !receipt.Accepted {
		t.Fatalf("answer: %+v %v", receipt, err)
	}
	if inFlight {
		close(provider.releaseCh)
	}
	finished := false
	for !finished {
		select {
		case event := <-a.Events():
			if e, ok := event.(AssistantMessageEvent); ok && e.Text == "Decision applied" {
				finished = true
			}
		case <-deadline:
			t.Fatal("answer did not resume task")
		}
	}
	requests, _ = provider.snapshot()
	if len(requests) != 3 {
		t.Fatalf("requests after answer=%d, want 3", len(requests))
	}
	seen := false
	for _, m := range requests[2] {
		if len(m.Question) > 0 {
			var fact QuestionFact
			if json.Unmarshal(m.Question, &fact) == nil && fact.Result != nil && fact.Result.QuestionID == id {
				seen = true
			}
		}
	}
	if !seen {
		t.Fatal("resumed request missed durable answer")
	}
}
