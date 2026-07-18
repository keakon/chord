package agent

import (
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func TestSubAgentInputOverflowPreservesOrder(t *testing.T) {
	ctx := t.Context()

	sub := &SubAgent{
		instanceID: "agent-1",
		parentCtx:  ctx,
		inputCh:    make(chan pendingUserMessage, 1),
	}

	sub.enqueueUserMessage(pendingUserMessage{Content: "one"})
	sub.enqueueUserMessage(pendingUserMessage{Content: "two"})
	sub.enqueueUserMessage(pendingUserMessage{Content: "three"})

	if got := (<-sub.inputCh).Content; got != "one" {
		t.Fatalf("first dequeued content = %q, want one", got)
	}
	sub.refillInputChannelFromOverflow()
	if got := (<-sub.inputCh).Content; got != "two" {
		t.Fatalf("second dequeued content = %q, want two", got)
	}
	sub.refillInputChannelFromOverflow()
	if got := (<-sub.inputCh).Content; got != "three" {
		t.Fatalf("third dequeued content = %q, want three", got)
	}
}

func TestSubAgentInputQueueRejectsConfiguredBudget(t *testing.T) {
	sub := &SubAgent{
		parentCtx:         t.Context(),
		inputCh:           make(chan pendingUserMessage, 1),
		queueMessageLimit: 2,
		queueByteLimit:    1024,
	}
	if !sub.enqueueUserMessage(pendingUserMessage{Content: "one"}) || !sub.enqueueUserMessage(pendingUserMessage{Content: "two"}) {
		t.Fatal("expected first two messages to fit queue budget")
	}
	if sub.enqueueUserMessage(pendingUserMessage{Content: "three"}) {
		t.Fatal("third message exceeded configured message budget")
	}
	first, ok := sub.tryReceiveUserInput()
	if !ok || first.Content != "one" {
		t.Fatalf("first dequeue = %#v, %v", first, ok)
	}
	sub.refillInputChannelFromOverflow()
	if !sub.enqueueUserMessage(pendingUserMessage{Content: "three"}) {
		t.Fatal("queue budget was not released after dequeue")
	}
}

func TestSubAgentContextAppendQueueRejectsByteBudget(t *testing.T) {
	sub := &SubAgent{
		parentCtx:         t.Context(),
		ctxAppendCh:       make(chan message.Message, 1),
		queueMessageLimit: 4,
		queueByteLimit:    160,
	}
	first := message.Message{Role: message.RoleUser, Content: strings.Repeat("a", 40)}
	if !sub.TryEnqueueContextAppend(first) {
		t.Fatal("first context append rejected")
	}
	if sub.TryEnqueueContextAppend(message.Message{Role: message.RoleUser, Content: strings.Repeat("b", 120)}) {
		t.Fatal("oversized context append exceeded configured byte budget")
	}
	if got, ok := sub.tryReceiveContextAppend(); !ok || got.Content != first.Content {
		t.Fatalf("context dequeue = %#v, %v", got, ok)
	}
	if !sub.TryEnqueueContextAppend(message.Message{Role: message.RoleUser, Content: "small"}) {
		t.Fatal("context byte budget was not released after dequeue")
	}
}

func TestSubAgentBusyTurnDefersQueuedUserInput(t *testing.T) {
	sub := &SubAgent{
		inputCh: make(chan pendingUserMessage, 1),
		turn:    &Turn{ID: 1},
	}
	sub.setState(SubAgentStateRunning, "working")
	sub.inputCh <- pendingUserMessage{DraftID: "draft-1", Content: "follow up", FromUser: true}
	sub.llmRequestInFlight.Store(true)

	if sub.canStartUserTurn() {
		t.Fatal("canStartUserTurn() = true during an in-flight request")
	}
	if got := len(sub.inputCh); got != 1 {
		t.Fatalf("queued input count = %d, want 1", got)
	}
}

func TestSubAgentContinuationIncludesQueuedUserInputAfterToolResult(t *testing.T) {
	parent, sub := newMixedBatchTestSubAgent(t)
	provider := &captureMessagesProvider{}
	sub.llmMu.Lock()
	sub.llmClient = testLLMClientWithProvider(provider)
	sub.llmMu.Unlock()
	sub.ctxMgr.Append(message.Message{
		Role: "assistant",
		ToolCalls: []message.ToolCall{{
			ID:   "call-1",
			Name: "Dummy",
		}},
	})
	sub.turn.PendingToolCalls.Store(1)
	sub.enqueueUserMessage(pendingUserMessage{DraftID: "draft-1", Content: "use the new constraint", FromUser: true})

	sub.handleToolResult(&toolResult{
		CallID:   "call-1",
		Name:     "Dummy",
		Result:   "tool output",
		TurnID:   sub.turn.ID,
		ArgsJSON: `{}`,
	})

	msgs := sub.ctxMgr.Snapshot()
	if len(msgs) < 3 {
		t.Fatalf("messages = %#v, want assistant, tool result, and queued user input", msgs)
	}
	toolMsg := msgs[len(msgs)-2]
	if toolMsg.Role != "tool" || toolMsg.Content != "tool output" {
		t.Fatalf("penultimate message = %#v, want tool output", toolMsg)
	}
	userMsg := msgs[len(msgs)-1]
	if userMsg.Role != "user" || userMsg.Content != "use the new constraint" {
		t.Fatalf("last message = %#v, want queued user input", userMsg)
	}
	if got := len(sub.inputCh) + len(sub.inputOverflow); got != 0 {
		t.Fatalf("queued input count = %d, want 0", got)
	}
	waitForCapturedSubAgentMessages(t, sub, provider, "use the new constraint")

	for {
		select {
		case evt := <-parent.Events():
			consumed, ok := evt.(PendingDraftConsumedEvent)
			if ok && consumed.DraftID == "draft-1" && consumed.AgentID == sub.instanceID {
				return
			}
		default:
			t.Fatal("queued draft was appended without a matching PendingDraftConsumedEvent")
		}
	}
}

func TestSubAgentCompleteWaitsForQueuedUserInput(t *testing.T) {
	parent, sub := newMixedBatchTestSubAgent(t)
	provider := &captureMessagesProvider{}
	sub.llmMu.Lock()
	sub.llmClient = testLLMClientWithProvider(provider)
	sub.llmMu.Unlock()
	sub.enqueueUserMessage(pendingUserMessage{DraftID: "draft-1", Content: "check one more thing", FromUser: true})

	sub.handleLLMResponse(&llmResult{
		turnID: sub.turn.ID,
		resp: &message.Response{ToolCalls: []message.ToolCall{{
			ID:   "complete-1",
			Name: tools.NameComplete,
			Args: []byte(`{"summary":"done"}`),
		}}},
	})

	msgs := sub.ctxMgr.Snapshot()
	if len(msgs) < 3 {
		t.Fatalf("messages = %#v, want assistant Complete, deferred tool result, and queued user input", msgs)
	}
	completeResult := msgs[len(msgs)-2]
	if completeResult.Role != "tool" || completeResult.ToolCallID != "complete-1" || !strings.Contains(completeResult.Content, "received new user input") {
		t.Fatalf("completion result = %#v, want deferred Complete result", completeResult)
	}
	userMsg := msgs[len(msgs)-1]
	if userMsg.Role != "user" || userMsg.Content != "check one more thing" {
		t.Fatalf("last message = %#v, want queued user input", userMsg)
	}
	waitForCapturedSubAgentMessages(t, sub, provider, "check one more thing")

	for {
		select {
		case evt := <-parent.eventCh:
			if evt.Type == EventAgentDone {
				t.Fatal("SubAgent completed before processing queued user input")
			}
		default:
			return
		}
	}
}

func testLLMClientWithProvider(provider llm.Provider) *llm.Client {
	providerCfg := llm.NewProviderConfig("test", config.ProviderConfig{
		Type: config.ProviderTypeMessages,
		Models: map[string]config.ModelConfig{
			"test-model": {Limit: config.ModelLimit{Context: 8192, Output: 1024}},
		},
	}, []string{"test-key"})
	return llm.NewClient(providerCfg, provider, "test-model", 1024, "")
}

func waitForCapturedSubAgentMessages(t *testing.T, sub *SubAgent, provider *captureMessagesProvider, wantLastUser string) {
	t.Helper()
	select {
	case result := <-sub.llmCh:
		if result.err != nil {
			t.Fatalf("continuation request failed: %v", result.err)
		}
	case <-time.After(time.Second):
		t.Fatalf("provider did not receive continuation containing %q", wantLastUser)
	}
	if len(provider.messages) == 0 {
		t.Fatalf("provider captured no messages for continuation containing %q", wantLastUser)
	}
	last := provider.messages[len(provider.messages)-1]
	if last.Role != "user" || last.Content != wantLastUser {
		t.Fatalf("last provider message = %#v, want queued user input %q", last, wantLastUser)
	}
}

func TestSubAgentQueuedLLMResultKeepsUserInputBlockedUntilConsumed(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	sub := &SubAgent{
		instanceID: "agent-1",
		parent:     a,
		inputCh:    make(chan pendingUserMessage, 1),
		turn:       &Turn{ID: 1},
	}
	sub.setState(SubAgentStateRunning, "working")
	sub.inputCh <- pendingUserMessage{Content: "follow up", FromUser: true}
	sub.llmRequestInFlight.Store(true)

	// The provider goroutine may already have queued its result, but runLoop
	// must keep the request gate closed until it consumes that result.
	if sub.canStartUserTurn() {
		t.Fatal("canStartUserTurn() = true before queued LLM result was consumed")
	}

	sub.finishLLMRequest()
	if !sub.canStartUserTurn() {
		t.Fatal("canStartUserTurn() = false after queued LLM result was consumed")
	}
}

func TestSubAgentTerminalStateDefersQueuedUserInputUntilReactivated(t *testing.T) {
	for _, state := range []SubAgentState{SubAgentStateCompleted, SubAgentStateFailed, SubAgentStateCancelled} {
		t.Run(string(state), func(t *testing.T) {
			sub := &SubAgent{
				inputCh: make(chan pendingUserMessage, 1),
				turn:    &Turn{ID: 1},
			}
			sub.setState(state, "stopped")
			sub.inputCh <- pendingUserMessage{DraftID: "draft-1", Content: "follow up", FromUser: true}

			if sub.canStartUserTurn() {
				t.Fatalf("canStartUserTurn() = true while state=%q", state)
			}
			if got := len(sub.inputCh); got != 1 {
				t.Fatalf("queued input count = %d, want 1", got)
			}

			sub.setState(SubAgentStateRunning, "resumed")
			if !sub.canStartUserTurn() {
				t.Fatal("canStartUserTurn() = false after explicit reactivation")
			}
		})
	}
}

func TestSubAgentContinueDoesNotReplaceRunningTurn(t *testing.T) {
	sub := &SubAgent{
		turn:       &Turn{ID: 7},
		continueCh: make(chan continueMsg, 1),
	}
	sub.continueCh <- continueMsg{}

	if !sub.tryHandleContinueSignal() {
		t.Fatal("continue signal was not handled")
	}
	if sub.turn == nil || sub.turn.ID != 7 {
		t.Fatalf("running turn = %#v, want original turn 7", sub.turn)
	}
}

func TestSubAgentRestartContinueWaitsForInFlightRequest(t *testing.T) {
	sub := &SubAgent{
		turn:       &Turn{ID: 7},
		continueCh: make(chan continueMsg, 1),
	}
	sub.llmRequestInFlight.Store(true)
	sub.continueCh <- continueMsg{restartStoppedTurn: true, drainContextAppends: true}

	if !sub.tryHandleContinueSignal() {
		t.Fatal("continue signal was not handled")
	}
	if sub.pendingContinue == nil || !sub.pendingContinue.restartStoppedTurn || !sub.pendingContinue.drainContextAppends {
		t.Fatalf("pendingContinue = %#v, want deferred restart", sub.pendingContinue)
	}
	if sub.tryHandlePendingContinue() {
		t.Fatal("pending restart ran before the in-flight request exited")
	}

	sub.llmRequestInFlight.Store(false)
	if sub.pendingContinue == nil {
		t.Fatal("pending restart was lost when the request exited")
	}
}

func TestSubAgentContextAppendOverflowPreservesOrder(t *testing.T) {
	ctx := t.Context()

	sub := &SubAgent{
		instanceID:  "agent-1",
		parentCtx:   ctx,
		ctxAppendCh: make(chan message.Message, 1),
	}

	if !sub.TryEnqueueContextAppend(message.Message{Content: "one"}) {
		t.Fatal("expected first context append to enqueue")
	}
	if !sub.TryEnqueueContextAppend(message.Message{Content: "two"}) {
		t.Fatal("expected second context append to buffer")
	}
	if !sub.TryEnqueueContextAppend(message.Message{Content: "three"}) {
		t.Fatal("expected third context append to buffer")
	}

	if got := (<-sub.ctxAppendCh).Content; got != "one" {
		t.Fatalf("first dequeued context append = %q, want one", got)
	}
	sub.refillContextAppendChannelFromOverflow()
	if got := (<-sub.ctxAppendCh).Content; got != "two" {
		t.Fatalf("second dequeued context append = %q, want two", got)
	}
	sub.refillContextAppendChannelFromOverflow()
	if got := (<-sub.ctxAppendCh).Content; got != "three" {
		t.Fatalf("third dequeued context append = %q, want three", got)
	}
}

func TestSubAgentHandleContinueDrainsQueuedContextAppendsFirst(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	sub := newControllableTestSubAgent(t, a, "task-1")

	if !sub.TryEnqueueContextAppend(message.Message{Content: "background note"}) {
		t.Fatal("expected context append to enqueue")
	}

	sub.handleContinue()

	msgs := sub.ctxMgr.Snapshot()
	if len(msgs) == 0 {
		t.Fatal("expected queued context append to be committed before continue")
	}
	last := msgs[len(msgs)-1]
	if last.Role != "user" || last.Content != "background note" {
		t.Fatalf("last message = %#v, want user background note", last)
	}
}
