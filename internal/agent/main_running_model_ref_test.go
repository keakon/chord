package agent

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/ctxmgr"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/ratelimit"
)

// newSidebarTestProviderConfig builds a single-model provider config for the
// sidebar identity tests, where the context window is the observable that must
// stay paired with the displayed model ref.
func newSidebarTestProviderConfig(name, model string, contextLimit, inputLimit int) *llm.ProviderConfig {
	return llm.NewProviderConfig(name, config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			model: {Limit: config.ModelLimit{Context: contextLimit, Input: inputLimit, Output: 4096}},
		},
	}, []string{name + "-key"})
}

// TestMainLLMAbortedFallbackAttemptKeepsCursorHeadModelRef pins the sidebar
// contract behind the reported bug: an attempt that has not emitted visible
// output is not a switch. When the user aborts while the fallback request is
// still waiting for its response, the sidebar must keep showing the model the
// request started on — with that model's window — and must agree with the
// sticky cursor the next request will use.
func TestMainLLMAbortedFallbackAttemptKeepsCursorHeadModelRef(t *testing.T) {
	a := newReadyTestMainAgent(t)

	primaryImpl := &blockingStreamProvider{calls: []scriptedStreamCall{{
		err: &llm.APIError{StatusCode: 500, Message: "primary unavailable"},
	}}}
	fallbackImpl := &blockingStreamProvider{
		streamedCh: make(chan struct{}),
		releaseCh:  make(chan struct{}),
		calls:      []scriptedStreamCall{{holdAfterStreams: true}},
	}
	client := llm.NewClient(newSidebarTestProviderConfig("primary-prov", "primary-model", 128000, 100000), primaryImpl, "primary-model", 4096, "sys")
	client.SetFallbackModels([]llm.FallbackModel{{
		ProviderConfig: newSidebarTestProviderConfig("fallback-prov", "fallback-model", 64000, 64000),
		ProviderImpl:   fallbackImpl,
		ModelID:        "fallback-model",
		MaxTokens:      4096,
		ContextLimit:   64000,
		InputLimit:     64000,
	}})
	a.swapLLMClientWithRef(client, "primary-model", 128000, "primary-prov/primary-model")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := a.callLLMForRequest(ctx, []message.Message{{Role: "user", Content: "hi"}}, 0)
		done <- err
	}()

	select {
	case <-fallbackImpl.streamedCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the fallback attempt to start")
	}

	if got := a.RunningModelRef(); got != "primary-prov/primary-model" {
		t.Fatalf("RunningModelRef during unconfirmed fallback attempt = %q, want primary-prov/primary-model", got)
	}
	if got := a.ctxMgr.GetMaxTokens(); got != 128000 {
		t.Fatalf("context window during unconfirmed fallback attempt = %d, want 128000", got)
	}
	for _, evt := range drainAgentEvents(a.Events()) {
		if changed, ok := evt.(RunningModelChangedEvent); ok {
			t.Fatalf("unconfirmed fallback attempt moved the sidebar model: %+v", changed)
		}
	}

	cancel()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "cancel") {
			t.Fatalf("callLLM err = %v, want a cancellation error", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the cancelled request to unwind")
	}

	if got, want := a.RunningModelRef(), client.NextRequestModelRef(); got != want {
		t.Fatalf("RunningModelRef after abort = %q, want the sticky cursor head %q", got, want)
	}
	if got := a.ctxMgr.GetMaxTokens(); got != 128000 {
		t.Fatalf("context window after abort = %d, want the cursor model's 128000", got)
	}
	for _, evt := range drainAgentEvents(a.Events()) {
		if changed, ok := evt.(RunningModelChangedEvent); ok {
			t.Fatalf("aborting an unconfirmed attempt changed the sidebar model: %+v", changed)
		}
	}
}

// TestMainLLMAbortedConfirmedFallbackReturnsToCursorHead covers the other half:
// once the fallback emitted visible output the switch is real, but a request that
// still ends in cancellation never confirmed it for the next request. The sidebar
// must return to the cursor head — both the identity and the window — so MODEL
// and the key/limit section keep resolving to the same model.
func TestMainLLMAbortedConfirmedFallbackReturnsToCursorHead(t *testing.T) {
	a := newReadyTestMainAgent(t)

	primaryImpl := &blockingStreamProvider{calls: []scriptedStreamCall{{
		err: &llm.APIError{StatusCode: 500, Message: "primary unavailable"},
	}}}
	fallbackImpl := &blockingStreamProvider{
		streamedCh: make(chan struct{}),
		releaseCh:  make(chan struct{}),
		calls: []scriptedStreamCall{{
			streams:          []message.StreamDelta{{Type: message.StreamDeltaThinking, Text: "reasoning"}},
			holdAfterStreams: true,
		}},
	}
	client := llm.NewClient(newSidebarTestProviderConfig("primary-prov", "primary-model", 128000, 100000), primaryImpl, "primary-model", 4096, "sys")
	client.SetFallbackModels([]llm.FallbackModel{{
		ProviderConfig: newSidebarTestProviderConfig("fallback-prov", "fallback-model", 64000, 64000),
		ProviderImpl:   fallbackImpl,
		ModelID:        "fallback-model",
		MaxTokens:      4096,
		ContextLimit:   64000,
		InputLimit:     64000,
	}})
	a.swapLLMClientWithRef(client, "primary-model", 128000, "primary-prov/primary-model")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := a.callLLMForRequest(ctx, []message.Message{{Role: "user", Content: "hi"}}, 0)
		done <- err
	}()

	select {
	case <-fallbackImpl.streamedCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the fallback attempt to emit visible output")
	}

	if got := a.RunningModelRef(); got != "fallback-prov/fallback-model" {
		t.Fatalf("RunningModelRef after visible fallback output = %q, want fallback-prov/fallback-model", got)
	}
	if got := a.ctxMgr.GetMaxTokens(); got != 64000 {
		t.Fatalf("context window after visible fallback output = %d, want the fallback's 64000", got)
	}

	cancel()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "cancel") {
			t.Fatalf("callLLM err = %v, want a cancellation error", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the cancelled request to unwind")
	}

	if got, want := a.RunningModelRef(), "primary-prov/primary-model"; got != want {
		t.Fatalf("RunningModelRef after aborting a confirmed fallback = %q, want %q", got, want)
	}
	if got := client.NextRequestModelRef(); got != "primary-prov/primary-model" {
		t.Fatalf("cursor head after abort = %q, want primary-prov/primary-model", got)
	}
	if got := a.ctxMgr.GetMaxTokens(); got != 128000 {
		t.Fatalf("context window after aborting a confirmed fallback = %d, want 128000", got)
	}
	sawReturn := false
	for _, evt := range drainAgentEvents(a.Events()) {
		if changed, ok := evt.(RunningModelChangedEvent); ok && changed.RunningModelRef == "primary-prov/primary-model" {
			sawReturn = true
		}
	}
	if !sawReturn {
		t.Fatal("missing RunningModelChangedEvent when the sidebar returned to the cursor head")
	}
}

// TestMainLLMFailedModelPoolKeepsSidebarOnCursorHead pins the exhausted-round
// case: the last attempted target is not what the next request will use, so the
// sidebar must show the sticky cursor head instead of the model that just failed
// last.
func TestMainLLMFailedModelPoolKeepsSidebarOnCursorHead(t *testing.T) {
	a := newReadyTestMainAgent(t)

	impls := []*blockingStreamProvider{
		{calls: []scriptedStreamCall{{err: &llm.APIError{StatusCode: 500, Message: "primary unavailable"}}}},
		{calls: []scriptedStreamCall{{err: &llm.APIError{StatusCode: 500, Message: "second unavailable"}}}},
		{calls: []scriptedStreamCall{{err: &llm.APIError{StatusCode: 500, Message: "third unavailable"}}}},
	}
	client := llm.NewClient(newSidebarTestProviderConfig("primary-prov", "primary-model", 128000, 100000), impls[0], "primary-model", 4096, "sys")
	client.SetFallbackModels([]llm.FallbackModel{
		{
			ProviderConfig: newSidebarTestProviderConfig("second-prov", "second-model", 64000, 64000),
			ProviderImpl:   impls[1],
			ModelID:        "second-model",
			MaxTokens:      4096,
			ContextLimit:   64000,
			InputLimit:     64000,
		},
		{
			ProviderConfig: newSidebarTestProviderConfig("third-prov", "third-model", 32000, 32000),
			ProviderImpl:   impls[2],
			ModelID:        "third-model",
			MaxTokens:      4096,
			ContextLimit:   32000,
			InputLimit:     32000,
		},
	})
	client.SetStreamRetryRounds(1)
	a.swapLLMClientWithRef(client, "primary-model", 128000, "primary-prov/primary-model")

	if _, err := a.callLLMForRequest(context.Background(), []message.Message{{Role: "user", Content: "hi"}}, 0); err == nil {
		t.Fatal("callLLM err = nil, want the exhausted pool error")
	}

	// The cursor advanced past the failed primary, so the next request starts on
	// the second model; the sidebar must follow it rather than the last failure.
	if got := client.NextRequestModelRef(); got != "second-prov/second-model" {
		t.Fatalf("cursor head = %q, want second-prov/second-model", got)
	}
	if got := a.RunningModelRef(); got != "second-prov/second-model" {
		t.Fatalf("RunningModelRef after an exhausted pool = %q, want second-prov/second-model", got)
	}
	if got := a.ctxMgr.GetMaxTokens(); got != 64000 {
		t.Fatalf("context window after an exhausted pool = %d, want the cursor model's 64000", got)
	}
}

// TestMainLLMFallbackDownshiftKeepsCommittedFallbackRef covers the fallback
// identity contract: a fallback that narrows the request budget commits its
// reference and budgets at the boundary before the request runs on it, and once
// that request succeeds the sidebar must keep reporting the fallback. The
// compaction line is evaluated against that window, so realigning the sidebar to
// the sticky cursor would show one model's name with another model's window.
func TestMainLLMFallbackDownshiftKeepsCommittedFallbackRef(t *testing.T) {
	a := newReadyTestMainAgent(t)
	a.globalConfig = &config.Config{Context: config.ContextConfig{Compaction: config.CompactionConfig{Threshold: 0.8}}}
	a.ctxMgr = ctxmgr.NewManagerWithInputBudget(128000, 100000, 0, 0.8)
	a.ctxMgr.Append(message.Message{Role: message.RoleUser, Content: "continue the task"})
	a.ctxMgr.UpdateFromUsage(message.TokenUsage{InputTokens: 90000})
	a.newTurn()
	a.started.Store(true)
	t.Cleanup(func() {
		if a.IsCompactionRunning() {
			a.handleCompactionCancel()
		}
		a.compactionWg.Wait()
	})

	primaryImpl := &blockingStreamProvider{calls: []scriptedStreamCall{{
		err: &llm.APIError{StatusCode: 500, Message: "primary unavailable"},
	}}}
	firstImpl := &blockingStreamProvider{calls: []scriptedStreamCall{{
		err: &llm.APIError{StatusCode: 500, Message: "first fallback unavailable"},
	}}}
	secondImpl := &blockingStreamProvider{calls: []scriptedStreamCall{{
		resp: &message.Response{Content: "fallback reply", StopReason: "stop"},
	}}}
	client := llm.NewClient(newSidebarTestProviderConfig("primary-prov", "primary-model", 128000, 100000), primaryImpl, "primary-model", 4096, "sys")
	client.SetFallbackModels([]llm.FallbackModel{
		{
			ProviderConfig: newSidebarTestProviderConfig("first-prov", "first-model", 128000, 100000),
			ProviderImpl:   firstImpl,
			ModelID:        "first-model",
			MaxTokens:      4096,
			ContextLimit:   128000,
			InputLimit:     100000,
		},
		{
			ProviderConfig: newSidebarTestProviderConfig("second-prov", "second-model", 64000, 64000),
			ProviderImpl:   secondImpl,
			ModelID:        "second-model",
			MaxTokens:      4096,
			ContextLimit:   64000,
			InputLimit:     64000,
		},
	})
	a.swapLLMClientWithRef(client, "primary-model", 128000, "primary-prov/primary-model")

	// Act as the event loop: every fallback pauses at the boundary and waits for
	// the loop's decision, so answer them in order until the request unwinds.
	done := make(chan error, 1)
	go func() {
		_, err := a.callLLMForRequest(a.turn.Ctx, a.ctxMgr.Snapshot(), 0)
		done <- err
	}()

	var boundaryRefs []string
	var runErr error
	deadline := time.After(5 * time.Second)
fallbackLoop:
	for {
		select {
		case evt := <-a.eventCh:
			if payload, ok := evt.Payload.(*llmFallbackBoundaryPayload); ok && payload != nil {
				boundaryRefs = append(boundaryRefs, payload.fallbackModelRef)
				a.handleLLMFallbackBoundary(Event{Type: EventLLMFallbackBoundary, TurnID: a.turn.ID, Payload: payload})
				continue
			}
			a.dispatch(evt)
		case err := <-done:
			runErr = err
			break fallbackLoop
		case <-deadline:
			t.Fatal("timed out waiting for the fallback downshift request")
		}
	}

	if want := []string{"first-prov/first-model", "second-prov/second-model"}; !slices.Equal(boundaryRefs, want) {
		t.Fatalf("fallback boundaries = %q, want %q", boundaryRefs, want)
	}
	if runErr != nil {
		t.Fatalf("callLLM err = %v, want the round to complete on the narrower fallback", runErr)
	}

	// A successful fallback pins the sticky cursor to the model that served the
	// round, so the next request starts from it instead of retrying the dead
	// primary; the sidebar identity must agree with that pinned cursor.
	if got := client.NextRequestModelRef(); got != "second-prov/second-model" {
		t.Fatalf("cursor head = %q, want the fallback that served the round", got)
	}
	if got := a.RunningModelRef(); got != "second-prov/second-model" {
		t.Fatalf("RunningModelRef after the fallback round = %q, want the committed second-prov/second-model", got)
	}
	if got := a.ctxMgr.GetMaxTokens(); got != 64000 {
		t.Fatalf("context window after the fallback round = %d, want the committed fallback window 64000", got)
	}
}

// TestMainInterruptedPartialKeepsConfirmedProducerProvenance pins the provenance
// contract for a preserved partial reply: the sidebar identity returns to the
// sticky cursor when the cancelled request unwinds, but the message must stay
// attributed to the fallback model that actually wrote it. Otherwise a replay
// strips the native reasoning items the producer's wire family vouched for and
// the reply silently downgrades.
func TestMainInterruptedPartialKeepsConfirmedProducerProvenance(t *testing.T) {
	a := newReadyTestMainAgent(t)

	primaryImpl := &blockingStreamProvider{calls: []scriptedStreamCall{{
		err: &llm.APIError{StatusCode: 500, Message: "primary unavailable"},
	}}}
	fallbackImpl := &blockingStreamProvider{
		streamedCh: make(chan struct{}),
		releaseCh:  make(chan struct{}),
		calls: []scriptedStreamCall{{
			streams:          []message.StreamDelta{{Type: message.StreamDeltaText, Text: "partial reply"}},
			holdAfterStreams: true,
		}},
	}
	client := llm.NewClient(newSidebarTestProviderConfig("primary-prov", "primary-model", 128000, 100000), primaryImpl, "primary-model", 4096, "sys")
	client.SetFallbackModels([]llm.FallbackModel{{
		ProviderConfig: newSidebarTestProviderConfig("fallback-prov", "fallback-model", 64000, 64000),
		ProviderImpl:   fallbackImpl,
		ModelID:        "fallback-model",
		MaxTokens:      4096,
		ContextLimit:   64000,
		InputLimit:     64000,
	}})
	a.swapLLMClientWithRef(client, "primary-model", 128000, "primary-prov/primary-model")
	a.newTurn()
	turn := a.turn

	done := make(chan error, 1)
	go func() {
		_, err := a.callLLMForRequest(turn.Ctx, []message.Message{{Role: "user", Content: "hi"}}, 0)
		done <- err
	}()

	select {
	case <-fallbackImpl.streamedCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the fallback attempt to emit visible output")
	}

	turn.Cancel()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "cancel") {
			t.Fatalf("callLLM err = %v, want a cancellation error", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the cancelled request to unwind")
	}

	if got, want := a.RunningModelRef(), "primary-prov/primary-model"; got != want {
		t.Fatalf("RunningModelRef after abort = %q, want the cursor head %q", got, want)
	}
	if !a.savePartialAssistantMsgForTurn(turn) {
		t.Fatal("expected the streamed partial reply to be preserved")
	}
	msgs := a.ctxMgr.Snapshot()
	last := msgs[len(msgs)-1]
	if last.Provenance == nil {
		t.Fatal("preserved partial reply has no provenance")
	}
	if got := last.Provenance.ModelRef; got != "fallback-prov/fallback-model" {
		t.Fatalf("partial reply provenance = %q, want the producing fallback fallback-prov/fallback-model", got)
	}
}

func inlineCodexSnapshot(provider string, usedPct float64) *ratelimit.KeyRateLimitSnapshot {
	return &ratelimit.KeyRateLimitSnapshot{
		Provider:   provider,
		CapturedAt: time.Now(),
		Primary:    &ratelimit.RateLimitWindow{UsedPct: usedPct},
		Source:     ratelimit.SnapshotSourceInlineKey,
	}
}

// TestKeySwitchedDeltaClearsRotatingProviderSnapshots pins the attribution of a
// key rotation inside an unconfirmed fallback: the delta names the attempt
// target, so that provider's snapshots are invalidated — not the sidebar model's,
// which is the model the user is actually looking at.
func TestKeySwitchedDeltaClearsRotatingProviderSnapshots(t *testing.T) {
	a := newReadyTestMainAgent(t)
	a.globalConfig = &config.Config{Providers: map[string]config.ProviderConfig{
		"sidebar-prov":  {Type: config.ProviderTypeResponses, Preset: config.ProviderPresetCodex},
		"rotating-prov": {Type: config.ProviderTypeResponses, Preset: config.ProviderPresetCodex},
	}}
	a.SetProviderModelRef("sidebar-prov/sidebar-model")

	sidebarCfg := newSidebarTestProviderConfig("sidebar-prov", "sidebar-model", 128000, 100000)
	rotatingCfg := newSidebarTestProviderConfig("rotating-prov", "rotating-model", 64000, 64000)
	if _, _, err := sidebarCfg.SelectKeyWithContext(t.Context()); err != nil {
		t.Fatalf("SelectKeyWithContext: %v", err)
	}
	if _, _, err := rotatingCfg.SelectKeyWithContext(t.Context()); err != nil {
		t.Fatalf("SelectKeyWithContext: %v", err)
	}
	sidebarCfg.UpdateKeySnapshot("sidebar-prov-key", inlineCodexSnapshot("sidebar-prov", 12))
	rotatingCfg.UpdateKeySnapshot("rotating-prov-key", inlineCodexSnapshot("rotating-prov", 34))

	client := llm.NewClient(sidebarCfg, stubProvider{}, "sidebar-model", 4096, "sys")
	client.SetFallbackModels([]llm.FallbackModel{{
		ProviderConfig: rotatingCfg,
		ProviderImpl:   stubProvider{},
		ModelID:        "rotating-model",
		MaxTokens:      4096,
		ContextLimit:   64000,
		InputLimit:     64000,
	}})
	a.swapLLMClientWithRef(client, "sidebar-model", 128000, "sidebar-prov/sidebar-model")

	a.updateRateLimitSnapshot(inlineCodexSnapshot("sidebar-prov", 12))
	a.updateRateLimitSnapshot(inlineCodexSnapshot("rotating-prov", 34))

	reducer := a.newMainLLMStreamReducer(client, "sidebar-prov/sidebar-model", "", nil, false, nil, 0)
	reducer.Handle(message.StreamDelta{Type: message.StreamDeltaKeySwitched, ModelRef: "rotating-prov/rotating-model"})

	if got := sidebarCfg.CurrentInlineRateLimitSnapshot(); got == nil {
		t.Fatal("the sidebar provider's inline snapshot was cleared by another provider's key rotation")
	}
	if got := rotatingCfg.CurrentInlineRateLimitSnapshot(); got != nil {
		t.Fatalf("rotating provider inline snapshot = %+v, want it cleared", got)
	}
	a.rateLimitMu.Lock()
	_, sidebarKept := a.rateLimitSnaps["sidebar-prov"]
	_, rotatingKept := a.rateLimitSnaps["rotating-prov"]
	a.rateLimitMu.Unlock()
	if !sidebarKept {
		t.Fatal("the sidebar provider's cached snapshot was dropped by another provider's key rotation")
	}
	if rotatingKept {
		t.Fatal("the rotating provider's cached snapshot survived its key switch")
	}
}

// TestMainTurnCancelledReleasesCommittedFallbackRef pins the cancel contract for
// a round that was running on a narrower fallback: the boundary commits that
// identity and its budgets, and when the user aborts the round before it
// confirms them, both must return to the cursor the next request starts from.
func TestMainTurnCancelledReleasesCommittedFallbackRef(t *testing.T) {
	a := newReadyTestMainAgent(t)
	a.globalConfig = &config.Config{Context: config.ContextConfig{Compaction: config.CompactionConfig{Threshold: 0.8}}}
	a.ctxMgr = ctxmgr.NewManagerWithInputBudget(128000, 100000, 0, 0.8)
	a.ctxMgr.Append(message.Message{Role: message.RoleUser, Content: "continue the task"})
	a.ctxMgr.UpdateFromUsage(message.TokenUsage{InputTokens: 90000})
	a.newTurn()
	turn := a.turn
	a.started.Store(true)

	primaryImpl := &blockingStreamProvider{calls: []scriptedStreamCall{{
		err: &llm.APIError{StatusCode: 500, Message: "primary unavailable"},
	}}}
	fallbackImpl := &blockingStreamProvider{
		streamedCh: make(chan struct{}),
		releaseCh:  make(chan struct{}),
		calls: []scriptedStreamCall{{
			streams:          []message.StreamDelta{{Type: message.StreamDeltaText, Text: "partial reply"}},
			holdAfterStreams: true,
		}},
	}
	client := llm.NewClient(newSidebarTestProviderConfig("primary-prov", "primary-model", 128000, 100000), primaryImpl, "primary-model", 4096, "sys")
	client.SetFallbackModels([]llm.FallbackModel{{
		ProviderConfig: newSidebarTestProviderConfig("second-prov", "second-model", 64000, 64000),
		ProviderImpl:   fallbackImpl,
		ModelID:        "second-model",
		MaxTokens:      4096,
		ContextLimit:   64000,
		InputLimit:     64000,
	}})
	a.swapLLMClientWithRef(client, "primary-model", 128000, "primary-prov/primary-model")

	done := make(chan error, 1)
	go func() {
		_, err := a.callLLMForRequest(turn.Ctx, a.ctxMgr.Snapshot(), 0)
		done <- err
	}()

	// Act as the event loop: the narrowed fallback pauses at the boundary, which
	// commits its identity and budgets before the request is dispatched.
	deadline := time.After(5 * time.Second)
	for committed := false; !committed; {
		select {
		case evt := <-a.eventCh:
			payload, ok := evt.Payload.(*llmFallbackBoundaryPayload)
			if !ok || payload == nil {
				a.dispatch(evt)
				continue
			}
			a.handleLLMFallbackBoundary(Event{Type: EventLLMFallbackBoundary, TurnID: turn.ID, Payload: payload})
			committed = true
		case <-deadline:
			t.Fatal("timed out waiting for the fallback boundary")
		}
	}

	select {
	case <-fallbackImpl.streamedCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the narrower fallback attempt to dispatch")
	}
	if got := a.RunningModelRef(); got != "second-prov/second-model" {
		t.Fatalf("RunningModelRef on the committed fallback = %q, want the committed second-prov/second-model", got)
	}
	if got := a.ctxMgr.GetMaxTokens(); got != 64000 {
		t.Fatalf("context window on the committed fallback = %d, want 64000", got)
	}

	turn.Cancel()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "cancel") {
			t.Fatalf("callLLM err = %v, want a cancellation error", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the cancelled request to unwind")
	}

	if got, want := a.RunningModelRef(), client.NextRequestModelRef(); got != want {
		t.Fatalf("RunningModelRef after aborting the round = %q, want the cursor head %q", got, want)
	}
	if got := a.ctxMgr.GetMaxTokens(); got != 128000 {
		t.Fatalf("context window after aborting the round = %d, want the cursor model's 128000", got)
	}
	sawReturn := false
	for _, evt := range drainAgentEvents(a.Events()) {
		if changed, ok := evt.(RunningModelChangedEvent); ok && changed.RunningModelRef == "primary-prov/primary-model" {
			sawReturn = true
		}
	}
	if !sawReturn {
		t.Fatal("missing RunningModelChangedEvent when the aborted round released the committed fallback")
	}
}

// TestMainTurnCancelledReleasesOversizeSuspendedFallbackRef pins the cancel
// contract for a round held behind a pending compaction: the oversize
// suspension legitimately keeps the narrowed fallback's identity and budgets
// because the continuation is admitted against that same window, but cancelling
// the turn ends the round for good, so the sidebar must drop the suspended
// target and return to the cursor the next request starts from.
func TestMainTurnCancelledReleasesOversizeSuspendedFallbackRef(t *testing.T) {
	a := newReadyTestMainAgent(t)
	a.globalConfig = &config.Config{Context: config.ContextConfig{Compaction: config.CompactionConfig{Threshold: 0.8}}}
	a.ctxMgr = ctxmgr.NewManagerWithInputBudget(128000, 100000, 0, 0.8)
	a.ctxMgr.Append(message.Message{Role: message.RoleUser, Content: "continue the task"})
	a.ctxMgr.UpdateFromUsage(message.TokenUsage{InputTokens: 90000})
	a.newTurn()
	turn := a.turn
	a.started.Store(true)

	client := llm.NewClient(newSidebarTestProviderConfig("primary-prov", "primary-model", 128000, 100000), &blockingStreamProvider{}, "primary-model", 4096, "sys")
	client.SetFallbackModels([]llm.FallbackModel{{
		ProviderConfig: newSidebarTestProviderConfig("second-prov", "second-model", 64000, 64000),
		ProviderImpl:   &blockingStreamProvider{},
		ModelID:        "second-model",
		MaxTokens:      4096,
		ContextLimit:   64000,
		InputLimit:     64000,
	}})
	a.swapLLMClientWithRef(client, "primary-model", 128000, "primary-prov/primary-model")

	// The oversize suspension commits the narrower fallback at the boundary and
	// then holds the round behind the running compaction. callLLMForRequest keeps
	// that committed target because the continuation is admitted against the same
	// budgets, so this is the state the cancel path must clean up.
	a.applyRunningModelRefIfCurrent(client, "second-prov/second-model", 64000, 64000)
	a.compactionState.oversizeSuspended = true
	if got := a.RunningModelRef(); got != "second-prov/second-model" {
		t.Fatalf("precondition: RunningModelRef = %q, want the suspended fallback", got)
	}
	if got := a.ctxMgr.GetMaxTokens(); got != 64000 {
		t.Fatalf("precondition: context window = %d, want the suspended fallback's 64000", got)
	}

	if !a.CancelCurrentTurn() {
		t.Fatal("CancelCurrentTurn() = false, want true")
	}
	a.handleTurnCancelled(Event{
		Type:    EventTurnCancelled,
		TurnID:  turn.ID,
		Payload: &TurnCancelledPayload{TurnID: turn.ID},
	})

	if got, want := a.RunningModelRef(), client.NextRequestModelRef(); got != want {
		t.Fatalf("RunningModelRef after cancelling the oversize suspension = %q, want the cursor head %q", got, want)
	}
	if got := client.NextRequestModelRef(); got != "primary-prov/primary-model" {
		t.Fatalf("cursor head after cancellation = %q, want primary-prov/primary-model", got)
	}
	if got := a.ctxMgr.GetMaxTokens(); got != 128000 {
		t.Fatalf("context window after cancelling the oversize suspension = %d, want the cursor model's 128000", got)
	}
}

// TestSubAgentRunningModelRefFollowsConfirmedSwitchOnly pins the worker's
// identity rule: an attempt target announced before any visible output must not
// move the displayed model, the first visible output confirms it, and the end of
// a request whose switch was never confirmed leaves the sidebar on the cursor
// head the next request starts from.
func TestSubAgentRunningModelRefFollowsConfirmedSwitchOnly(t *testing.T) {
	a := newReadyTestMainAgent(t)

	client := llm.NewClient(newSidebarTestProviderConfig("primary-prov", "primary-model", 128000, 100000), stubProvider{}, "primary-model", 4096, "sys")
	client.SetFallbackModels([]llm.FallbackModel{{
		ProviderConfig: newSidebarTestProviderConfig("fallback-prov", "fallback-model", 64000, 64000),
		ProviderImpl:   stubProvider{},
		ModelID:        "fallback-model",
		MaxTokens:      4096,
		ContextLimit:   64000,
		InputLimit:     64000,
	}})
	client.NoteRunningModelRef("primary-prov/primary-model")

	turn := &Turn{}
	sub := &SubAgent{instanceID: "sub-1", parent: a, llmClient: client, turn: turn}
	reducer := sub.newSubLLMStreamReducer(turn, func(string) {}, false, nil, 1)
	if reducer.beforeStatus != nil {
		t.Fatal("the worker identity must not move on an announced attempt")
	}

	reducer.Handle(message.StreamDelta{
		Type:   message.StreamDeltaStatus,
		Status: &message.StatusDelta{Type: message.StatusDeltaRetrying, ModelRef: "fallback-prov/fallback-model"},
	})
	if got := client.RunningModelRef(); got != "primary-prov/primary-model" {
		t.Fatalf("worker identity after an unconfirmed attempt = %q, want primary-prov/primary-model", got)
	}

	reducer.Handle(message.StreamDelta{
		Type:   message.StreamDeltaKeyConfirmed,
		Status: &message.StatusDelta{ModelRef: "fallback-prov/fallback-model"},
	})
	if got := client.RunningModelRef(); got != "fallback-prov/fallback-model" {
		t.Fatalf("worker identity after visible output = %q, want fallback-prov/fallback-model", got)
	}
	if got := turn.producingModelRef(); got != "fallback-prov/fallback-model" {
		t.Fatalf("turn producing ref after visible output = %q, want fallback-prov/fallback-model", got)
	}

	sub.syncRunningModelRefToCursorHead()
	if got := client.RunningModelRef(); got != "primary-prov/primary-model" {
		t.Fatalf("worker identity after the request ended = %q, want the cursor head primary-prov/primary-model", got)
	}
}

// TestApplyRunningModelRefIfCurrentSkipsSupersededClient pins the guard behind
// the captured-client apply entries: a concurrent model switch installs a new
// client, so a superseded request's late confirmation or success must not
// overwrite the switched identity or pair it with the old client's budgets.
func TestApplyRunningModelRefIfCurrentSkipsSupersededClient(t *testing.T) {
	a := newReadyTestMainAgent(t)

	installed := llm.NewClient(newSidebarTestProviderConfig("prov-a", "model-a", 128000, 100000), &blockingStreamProvider{}, "model-a", 4096, "sys")
	superseded := llm.NewClient(newSidebarTestProviderConfig("prov-b", "model-b", 64000, 60000), &blockingStreamProvider{}, "model-b", 4096, "sys")
	a.swapLLMClientWithRef(installed, "model-a", 128000, "prov-a/model-a")
	if got := a.RunningModelRef(); got != "prov-a/model-a" {
		t.Fatalf("precondition: RunningModelRef = %q, want prov-a/model-a", got)
	}

	a.applyRunningModelRefIfCurrent(superseded, "prov-b/model-b", 64000, 60000)
	if got := a.RunningModelRef(); got != "prov-a/model-a" {
		t.Fatalf("RunningModelRef after superseded apply = %q, want prov-a/model-a", got)
	}
	if got := a.ctxMgr.GetMaxTokens(); got != 128000 {
		t.Fatalf("context window after superseded apply = %d, want 128000", got)
	}

	a.applyRunningModelRefIfCurrent(installed, "prov-a/model-a2", 64000, 60000)
	if got := a.RunningModelRef(); got != "prov-a/model-a2" {
		t.Fatalf("RunningModelRef after current apply = %q, want prov-a/model-a2", got)
	}
	if got := a.ctxMgr.GetMaxTokens(); got != 64000 {
		t.Fatalf("context window after current apply = %d, want 64000", got)
	}
	for _, evt := range drainAgentEvents(a.Events()) {
		if changed, ok := evt.(RunningModelChangedEvent); ok && changed.RunningModelRef == "prov-b/model-b" {
			t.Fatalf("superseded client moved the sidebar model: %+v", changed)
		}
	}
}
