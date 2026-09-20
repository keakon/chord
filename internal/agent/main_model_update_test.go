package agent

import (
	"sync"
	"testing"
	"time"

	"github.com/keakon/chord/internal/ctxmgr"
	"github.com/keakon/chord/internal/llm"
)

func TestConcurrentRunningModelUpdatesKeepBudgetsAndEventsAligned(t *testing.T) {
	client := &llm.Client{}
	instance := &MainAgent{llmClient: client, ctxMgr: ctxmgr.NewManager(128000, 0.8), outputCh: make(chan AgentEvent, 4)}
	for iteration := range 20000 {
		start := make(chan struct{})
		var workers sync.WaitGroup
		workers.Go(func() {
			<-start
			instance.applyRunningModelRefIfCurrent(client, "sample/model-a", 64000, 60000)
		})
		workers.Go(func() {
			<-start
			instance.applyRunningModelRefIfCurrent(client, "sample/model-b", 128000, 120000)
		})
		close(start)
		workers.Wait()
		ref := instance.RunningModelRef()
		wantContext, wantInput := 64000, 60000
		if ref == "sample/model-b" {
			wantContext, wantInput = 128000, 120000
		}
		if gotContext, gotInput := instance.ctxMgr.GetMaxTokens(), instance.ctxMgr.GetInputBudget(); gotContext != wantContext || gotInput != wantInput {
			t.Fatalf("iteration=%d model=%s: context/input=%d/%d, want %d/%d", iteration, ref, gotContext, gotInput, wantContext, wantInput)
		}
		events := drainAgentEvents(instance.Events())
		if len(events) == 0 {
			t.Fatalf("iteration=%d: missing model change events", iteration)
		}
		if changed := events[len(events)-1].(RunningModelChangedEvent); changed.RunningModelRef != ref {
			t.Fatalf("iteration=%d: last event model=%s, current model=%s", iteration, changed.RunningModelRef, ref)
		}
	}
}

func TestModelInstallationSerializesWithCapturedClientUpdate(t *testing.T) {
	instance := newReadyTestMainAgent(t)
	for iteration := range 200 {
		previous := llm.NewClient(newSidebarTestProviderConfig("sample", "model-a", 64000, 60000), &blockingStreamProvider{}, "model-a", 4096, "sys")
		installed := llm.NewClient(newSidebarTestProviderConfig("sample", "model-b", 128000, 120000), &blockingStreamProvider{}, "model-b", 4096, "sys")
		prepared := &preparedMainModel{client: installed, modelName: "model-b", selectedRef: "sample/model-b", runningRef: "sample/model-b", contextLimit: 128000}
		instance.swapLLMClientWithRef(previous, "model-a", 64000, "sample/model-a")
		var workers sync.WaitGroup
		workers.Go(func() {
			instance.applyRunningModelRefIfCurrent(previous, "sample/model-c", 32000, 30000)
		})
		workers.Go(func() {
			instance.installPreparedMainModel(prepared)
		})
		workers.Wait()
		installed.Close()
		if ref := instance.RunningModelRef(); ref != prepared.runningRef {
			t.Fatalf("iteration=%d: model=%s, want %s", iteration, ref, prepared.runningRef)
		}
		if budget := instance.ctxMgr.GetMaxTokens(); budget != prepared.contextLimit {
			t.Fatalf("iteration=%d: budget=%d, want %d", iteration, budget, prepared.contextLimit)
		}
		events := drainAgentEvents(instance.Events())
		if len(events) == 0 {
			t.Fatalf("iteration=%d: missing installation event", iteration)
		}
		if changed, ok := events[len(events)-1].(RunningModelChangedEvent); !ok || changed.RunningModelRef != prepared.runningRef {
			t.Fatalf("iteration=%d: last event=%+v, want installed model", iteration, events[len(events)-1])
		}
	}
}

func TestRunningModelReadersRemainAvailableDuringOutputBackpressure(t *testing.T) {
	instance := newReadyTestMainAgent(t)
	instance.outputCh = make(chan AgentEvent, 1)
	instance.outputCh <- StreamTextEvent{Text: "sample"}
	done := make(chan struct{})
	go func() {
		instance.applyRunningModelRefIfCurrent(nil, "sample/model-a", 64000, 60000)
		close(done)
	}()
	read := make(chan string, 1)
	stopRead := make(chan struct{})
	defer close(stopRead)
	go func() {
		for {
			select {
			case <-stopRead:
				return
			default:
			}
			if ref := instance.RunningModelRef(); ref == "sample/model-a" {
				read <- ref
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()
	select {
	case <-read:
	case <-time.After(time.Second):
		<-instance.outputCh
		t.Fatal("model reader blocked by output backpressure")
	}
	if budget := instance.ctxMgr.GetMaxTokens(); budget != 64000 {
		t.Fatalf("budget=%d, want 64000", budget)
	}
	<-instance.outputCh
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("model update did not finish after output space became available")
	}
}
