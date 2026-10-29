package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
)

func TestCooldownRetryRoundIncludesQueuedUserInput(t *testing.T) {
	t.Parallel()
	a := newReadyTestMainAgent(t)
	cfg := llm.NewProviderConfig("primary", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"model-a": {Limit: config.ModelLimit{Context: 128000, Output: 4096}},
		},
	}, []string{"primary-key"})
	impl := &blockingStreamProvider{
		streamedCh: make(chan struct{}),
		releaseCh:  make(chan struct{}),
		calls: []scriptedStreamCall{{
			resp:             &message.Response{Content: "retry response"},
			holdAfterStreams: true,
		}},
	}
	client := llm.NewClient(cfg, impl, "model-a", 4096, "sys")
	a.swapLLMClientWithRef(client, "model-a", 128000, "primary/model-a")

	// Keep every key cooling long enough for the second user message to land in
	// the durable next-request queue before the first actual provider attempt.
	cfg.MarkCooldown("primary-key", 300*time.Millisecond)

	runCtx, cancelRun := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- a.Run(runCtx) }()

	a.SendUserMessage("initial request")
	waitForAgentState(t, "user turn started", func() bool { return a.currentTurnID() != 0 })
	a.SendUserMessage("queued follow-up")
	waitForAgentState(t, "queued input committed", func() bool {
		snapshot := a.InputQueueSnapshot()
		return snapshot != nil && snapshot.State != nil && len(snapshot.State.Inputs) == 1
	})

	select {
	case <-impl.streamedCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the retry-round request")
	}
	requests, _ := impl.snapshot()
	if len(requests) != 1 {
		t.Fatalf("provider requests = %d, want 1", len(requests))
	}
	if !requestHasUserText(requests[0], "queued follow-up") {
		t.Fatalf("retry-round request did not include queued input: %#v", requests[0])
	}
	if !requestHasUserText(a.ctxMgr.Snapshot(), "queued follow-up") {
		t.Fatal("queued input was not committed to conversation context")
	}

	close(impl.releaseCh)
	cancelRun()
	select {
	case <-runDone:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out stopping main agent")
	}
}

func retryBoundaryMessages(t *testing.T, a *MainAgent, messages []message.Message, tailOverlayCount int) llmRetryBoundaryResult {
	t.Helper()
	payload := &llmRetryBoundaryPayload{
		turnID:           a.turn.ID,
		messages:         messages,
		tailOverlayCount: tailOverlayCount,
		reply:            make(chan llmRetryBoundaryResult, 1),
	}
	a.handleLLMRetryBoundary(Event{Type: EventLLMRetryBoundary, TurnID: payload.turnID, Payload: payload})
	finishDeliveryWrites(t, a)
	select {
	case result := <-payload.reply:
		if result.err != nil {
			t.Fatalf("retry boundary err = %v", result.err)
		}
		return result
	case <-time.After(3 * time.Second):
		t.Fatal("retry boundary did not resume")
		return llmRetryBoundaryResult{}
	}
}

// A JOB RESULT that lands while keys are cooling must ride the next retry round
// together with queued user input, in the same order the fallback boundary
// uses: user input first, then the mailbox row, before the transient tail.
func TestRetryRoundBoundaryMergesUserInputBeforeMailboxResult(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.newTurn("")
	a.ctxMgr.Append(message.Message{Role: message.RoleUser, Content: "hi"})

	a.handleJobFinished(Event{Type: EventJobFinished, SourceID: a.instanceID, Payload: backgroundResultPayload(a.instanceID, t.Name()+"-1", "Run production build")})
	a.handleUserMessage(Event{Payload: "queued user follow-up"})

	messages := []message.Message{
		{Role: message.RoleUser, Content: "hi"},
		{Role: message.RoleUser, Content: "transient tail", Kind: message.KindTurnOverlay},
	}
	got := retryBoundaryMessages(t, a, messages, 1).messages
	userIdx, jobIdx := -1, -1
	for i, msg := range got {
		if msg.Role == message.RoleUser && msg.Content == "queued user follow-up" {
			userIdx = i
		}
		if msg.Kind == message.KindBackgroundResult && strings.Contains(msg.Content, "Run production build") {
			jobIdx = i
		}
	}
	if userIdx < 0 || jobIdx < 0 || userIdx > jobIdx {
		t.Fatalf("retry messages = %#v, want user input before the JOB RESULT", got)
	}
	if last := got[len(got)-1]; last.Kind != message.KindTurnOverlay {
		t.Fatalf("last message = %#v, want the transient tail to stay last", last)
	}
	if !requestHasBackgroundResult(a.ctxMgr.Snapshot(), "Run production build") {
		t.Fatal("JOB RESULT was not committed to conversation context")
	}

	// The next round with no new arrivals must not duplicate either message.
	again := retryBoundaryMessages(t, a, got, 1)
	if again.rebuild {
		t.Fatal("retry round without new input must reuse the prepared request")
	}
	users, jobs := 0, 0
	for _, msg := range again.messages {
		if msg.Role == message.RoleUser && msg.Content == "queued user follow-up" {
			users++
		}
		if msg.Kind == message.KindBackgroundResult && strings.Contains(msg.Content, "Run production build") {
			jobs++
		}
	}
	if users != 1 || jobs != 1 {
		t.Fatalf("copies user=%d job=%d, want exactly 1 each", users, jobs)
	}
}

// A boundary event that outlives its turn must not claim queued input: the
// input stays queued for the next request instead of joining a dead turn.
func TestRetryRoundBoundaryRejectsStaleTurnWithoutClaiming(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	a.newTurn(a.newDeliveryWork(recovery.DeliveryWorkUser))
	a.recordCommittedUserMessage(message.Message{Role: message.RoleUser, Content: "initial"})
	inputID := submitDeliveryForTest(t, a, "correction", recovery.InputDeliveryNextRequest)
	staleTurnID := a.turn.ID
	a.newTurn(a.newDeliveryWork(recovery.DeliveryWorkUser))

	payload := &llmRetryBoundaryPayload{
		turnID:   staleTurnID,
		messages: a.ctxMgr.Snapshot(),
		reply:    make(chan llmRetryBoundaryResult, 1),
	}
	a.handleLLMRetryBoundary(Event{Type: EventLLMRetryBoundary, TurnID: staleTurnID, Payload: payload})
	select {
	case result := <-payload.reply:
		if !errors.Is(result.err, context.Canceled) {
			t.Fatalf("stale retry boundary err = %v, want context.Canceled", result.err)
		}
	default:
		t.Fatal("stale retry boundary did not reply")
	}
	finishDeliveryWrites(t, a)
	for _, input := range a.delivery.committed.Inputs {
		if input.InputID == inputID && input.Stage != recovery.PendingInputStageQueued {
			t.Fatalf("stale retry boundary moved input to %q, want queued", input.Stage)
		}
	}
	if requestHasUserText(a.ctxMgr.Snapshot(), "correction") {
		t.Fatal("stale retry boundary committed the input to the transcript")
	}
}

// The retry worker gives up when its round is cancelled while it waits for the
// event loop, so a cancelled turn never blocks on a boundary reply.
func TestRetryRoundBoundaryWaitHonorsCancellation(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	a.newTurn("")
	a.started.Store(true)
	t.Cleanup(func() { a.started.Store(false) })

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan error, 1)
	go func() {
		_, err := a.updateMainLLMRequestBeforeRetryRound(ctx, a.turn.ID, nil, 0)
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("retry boundary wait ignored cancellation")
	}
}

func TestRetryRoundInputRequiresDurableExecutionPermit(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	work := a.newDeliveryWork(recovery.DeliveryWorkUser)
	a.newTurn(work)
	a.recordCommittedUserMessage(message.Message{Role: message.RoleUser, Content: "initial"})
	inputID := submitDeliveryForTest(t, a, "correction", recovery.InputDeliveryNextRequest)
	followupID := submitDeliveryForTest(t, a, "later", recovery.InputDeliveryAfterCompletion)
	payload := &llmRetryBoundaryPayload{
		turnID:   a.turn.ID,
		messages: a.ctxMgr.Snapshot(),
		reply:    make(chan llmRetryBoundaryResult, 1),
	}
	a.handleLLMRetryBoundary(Event{Type: EventLLMRetryBoundary, TurnID: a.turn.ID, Payload: payload})
	select {
	case <-payload.reply:
		t.Fatal("retry round resumed before the durable claim and start")
	default:
	}
	finishDeliveryWrites(t, a)
	select {
	case result := <-payload.reply:
		if result.err != nil || !result.rebuild {
			t.Fatalf("retry boundary result = %+v", result)
		}
		if !requestHasUserText(result.messages, "correction") {
			t.Fatal("retry round did not carry the queued input")
		}
		if requestHasUserText(result.messages, "later") {
			t.Fatal("retry round consumed an after-completion input")
		}
		count := 0
		for _, msg := range result.messages {
			if msg.InputID == inputID {
				count++
			}
		}
		if count != 1 {
			t.Fatalf("admitted input copies = %d, want 1", count)
		}
	default:
		t.Fatal("retry round did not resume after the durable permit")
	}

	disk, err := a.recoveryManager().LoadInputDelivery()
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range disk.Inputs {
		if input.InputID == inputID && input.Stage != recovery.PendingInputStageStarted {
			t.Fatal("retry round resumed without a durable start")
		}
		if input.InputID == followupID && input.Stage != recovery.PendingInputStageQueued {
			t.Fatal("retry round consumed an after-completion input")
		}
	}
	persisted, err := a.recoveryManager().LoadMessages(identity.MainAgentID)
	if err != nil {
		t.Fatal(err)
	}
	if !requestHasUserText(persisted, "correction") {
		t.Fatal("retry round resumed before the input reached the transcript")
	}
}
