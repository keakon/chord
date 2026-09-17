package agent

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
)

// The tests in this file drive the whole preserved-interruption loop through
// the real client: MainAgent → llm.Client retry/key rotation → a scripted
// provider that truncates its stream. The agent-level tests elsewhere inject
// the escalated error directly and the llm-level tests exercise cooldown
// bookkeeping in isolation; neither shows that the pieces line up — that an
// escalated interruption really cools the key it used, that the restart really
// picks a different one, and that the continuation the transcript records is
// really what the next request carries.

// recordedRequest is one provider call as the provider saw it.
type recordedRequest struct {
	apiKey   string
	model    string
	messages []message.Message
}

// keyRecordingProvider serves a script of responses and records the credential
// and model each call ran with.
type keyRecordingProvider struct {
	mu       sync.Mutex
	calls    []scriptedStreamCall
	requests []recordedRequest
}

func (p *keyRecordingProvider) CompleteStream(
	ctx context.Context,
	apiKey string,
	model string,
	_ string,
	messages []message.Message,
	_ []message.ToolDefinition,
	_ int,
	_ llm.RequestTuning,
	cb llm.StreamCallback,
) (*message.Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p.mu.Lock()
	p.requests = append(p.requests, recordedRequest{apiKey: apiKey, model: model, messages: cloneMessages(messages)})
	var next scriptedStreamCall
	if len(p.calls) > 0 {
		next = p.calls[0]
		p.calls = p.calls[1:]
	} else {
		next = scriptedStreamCall{resp: &message.Response{Content: "unscripted", StopReason: "stop"}}
	}
	p.mu.Unlock()

	if cb != nil {
		for _, delta := range next.streams {
			cb(delta)
		}
	}
	if next.err != nil {
		return nil, next.err
	}
	if next.resp != nil {
		return next.resp, nil
	}
	return &message.Response{}, nil
}

func (p *keyRecordingProvider) Complete(
	ctx context.Context,
	apiKey string,
	model string,
	systemPrompt string,
	messages []message.Message,
	tools []message.ToolDefinition,
	maxTokens int,
	tuning llm.RequestTuning,
) (*message.Response, error) {
	return p.CompleteStream(ctx, apiKey, model, systemPrompt, messages, tools, maxTokens, tuning, nil)
}

func (p *keyRecordingProvider) recorded() []recordedRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]recordedRequest(nil), p.requests...)
}

// textDelta is the stream event a provider emits for visible body text; it is
// what makes an interruption preservable rather than an empty failure.
func textDelta(text string) message.StreamDelta {
	return message.StreamDelta{Type: "text", Text: text}
}

// truncatedReply is a reply the gateway cut off after streaming body text: the
// content arrives, the stop reason says it never finished.
func truncatedReply(text string) scriptedStreamCall {
	return scriptedStreamCall{
		streams: []message.StreamDelta{textDelta(text)},
		resp:    &message.Response{Content: text, StopReason: "interrupted"},
	}
}

func completedReply(text string) scriptedStreamCall {
	return scriptedStreamCall{
		streams: []message.StreamDelta{textDelta(text)},
		resp:    &message.Response{Content: text, StopReason: "stop"},
	}
}

// newContinuationTestAgent wires a MainAgent to a scripted provider with the
// given credentials. wireType selects the wire family, which decides whether
// the pool can resume from a trailing assistant turn.
func newContinuationTestAgent(t *testing.T, wireType string, keys []string, calls ...scriptedStreamCall) (*MainAgent, *keyRecordingProvider, *llm.ProviderConfig) {
	t.Helper()
	a := newReadyTestMainAgent(t)
	provider := &keyRecordingProvider{calls: calls}
	providerCfg := llm.NewProviderConfig("scripted", config.ProviderConfig{
		Type: wireType,
		Models: map[string]config.ModelConfig{
			"primary-model": {Limit: config.ModelLimit{Context: 8192, Output: 1024}},
		},
	}, keys)
	a.llmClient = llm.NewClient(providerCfg, provider, "primary-model", 1024, "")
	a.newTurn()
	return a, provider, providerCfg
}

// waitForMainLLMOutcome waits for the event the agent's own restart posts when
// its request finishes, so the test observes the resumed request through the
// same path the event loop would.
func waitForMainLLMOutcome(t *testing.T, a *MainAgent, timeout time.Duration) Event {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case evt := <-a.eventCh:
			switch evt.Type {
			case EventLLMResponse, EventAgentError:
				return evt
			default:
				continue
			}
		case <-deadline:
			t.Fatal("timed out waiting for the resumed main LLM request to finish")
			return Event{}
		}
	}
}

func llmResponseFromEvent(t *testing.T, evt Event) *LLMResponsePayload {
	t.Helper()
	if evt.Type != EventLLMResponse {
		t.Fatalf("resumed request ended with %v (payload %v), want a successful response", evt.Type, evt.Payload)
	}
	payload, ok := evt.Payload.(*LLMResponsePayload)
	if !ok {
		t.Fatalf("EventLLMResponse payload = %T, want *LLMResponsePayload", evt.Payload)
	}
	return payload
}

// TestPreservedInterruptionResumeRotatesKeysAndCompletes covers the full loop
// end to end on a two-credential pool: the truncated stream cools the key it
// ran on, the partial reply and its continuation land in durable history, and
// the agent's own restart runs on the other key and completes the reply.
func TestPreservedInterruptionResumeRotatesKeysAndCompletes(t *testing.T) {
	a, provider, providerCfg := newContinuationTestAgent(t, config.ProviderTypeMessages,
		[]string{"key-a", "key-b"},
		truncatedReply("the first half of the answer"),
		completedReply("and the second half"),
	)

	_, err := a.callLLMForRequest(t.Context(), []message.Message{{Role: message.RoleUser, Content: "explain the failure"}}, 0)
	if err == nil {
		t.Fatal("callLLM err = nil, want the escalated stream interruption")
	}
	if !llm.IsPreservableStreamInterruption(err) {
		t.Fatalf("callLLM err = %v, want a preservable stream interruption", err)
	}
	// The escalation cooled the key that truncated, which is what makes the
	// rotation below a skip of an unhealthy credential rather than plain
	// round-robin.
	if available, total := providerCfg.AvailableKeyCount(); available != total-1 {
		t.Fatalf("available keys = %d of %d, want exactly one cooling after the interruption", available, total)
	}

	// The agent restarts the request itself; the escalated error carries the
	// cooldown the client applied to the key that truncated.
	a.handleAgentError(Event{Type: EventAgentError, TurnID: a.turn.ID, Payload: err})
	a.flushPersist()

	// callLLM does not append the prompt to ctxMgr (that is the user-message
	// handler's job), so the history the resume built is exactly the preserved
	// partial followed by its continuation.
	msgs := a.GetMessages()
	if len(msgs) != 2 {
		t.Fatalf("history = %d messages, want 2 (preserved partial + continuation): %#v", len(msgs), msgs)
	}
	if msgs[0].Role != message.RoleAssistant || msgs[0].StopReason != "interrupted" || !strings.Contains(msgs[0].Content, "the first half") {
		t.Fatalf("preserved partial = %#v, want the interrupted assistant reply", msgs[0])
	}
	if msgs[1].Kind != message.KindStreamContinue || msgs[1].Content != streamContinueMessageText {
		t.Fatalf("continuation = %#v, want the durable KindStreamContinue message", msgs[1])
	}

	resp := llmResponseFromEvent(t, waitForMainLLMOutcome(t, a, 10*time.Second))
	if !strings.Contains(resp.Content, "and the second half") {
		t.Fatalf("resumed reply = %q, want the continued text", resp.Content)
	}

	requests := provider.recorded()
	if len(requests) != 2 {
		t.Fatalf("provider saw %d requests, want 2 (interrupted + resumed)", len(requests))
	}
	if requests[0].apiKey == requests[1].apiKey {
		t.Fatalf("both requests ran on %q, want the resume to rotate off the cooled key", requests[0].apiKey)
	}
	// A truncated reply is a transport failure, not a model that refuses to
	// answer, so the resume stays on the same model instead of falling back.
	if requests[0].model != requests[1].model {
		t.Fatalf("resume switched model from %q to %q, want it to stay on one model", requests[0].model, requests[1].model)
	}

	// What the transcript records is what the resumed request carried.
	sent := requests[1].messages
	if !containsMessage(sent, message.RoleAssistant, "the first half of the answer") {
		t.Fatalf("resumed request does not carry the preserved partial: %#v", sent)
	}
	if !containsMessage(sent, message.RoleUser, streamContinueMessageText) {
		t.Fatalf("resumed request does not carry the continuation message: %#v", sent)
	}
}

// TestPreservedInterruptionResumeWaitsForTheOnlyKeyToCool covers the
// single-credential pool: with nothing to rotate to, the restart has to wait
// out the cooldown rather than fail fast on AllKeysCoolingError, which is the
// only backoff available when one key keeps truncating.
func TestPreservedInterruptionResumeWaitsForTheOnlyKeyToCool(t *testing.T) {
	a, provider, providerCfg := newContinuationTestAgent(t, config.ProviderTypeMessages,
		[]string{"only-key"},
		truncatedReply("the first half of the answer"),
		completedReply("and the second half"),
	)

	_, err := a.callLLMForRequest(t.Context(), []message.Message{{Role: message.RoleUser, Content: "explain the failure"}}, 0)
	if !llm.IsPreservableStreamInterruption(err) {
		t.Fatalf("callLLM err = %v, want a preservable stream interruption", err)
	}
	if got := len(provider.recorded()); got != 1 {
		t.Fatalf("provider saw %d requests, want 1", got)
	}
	if available, total := providerCfg.AvailableKeyCount(); available != 0 || total != 1 {
		t.Fatalf("available keys = %d of %d, want the only key cooling", available, total)
	}

	// The only key is cooling now. A restart bounded by a short deadline must
	// spend it waiting, not return the cooling error immediately.
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, retryErr := a.callLLMForRequest(ctx, a.GetMessages(), 0)
	waited := time.Since(start)

	if retryErr == nil {
		t.Fatal("retry err = nil, want the deadline to expire while waiting for the cooldown")
	}
	if isAllKeysCoolingErr(retryErr) {
		t.Fatalf("retry failed immediately with %v, want it to wait out the cooldown instead", retryErr)
	}
	if waited < 200*time.Millisecond {
		t.Fatalf("retry returned after %v, want it to spend its whole deadline waiting for the key", waited)
	}
	if got := len(provider.recorded()); got != 1 {
		t.Fatalf("provider saw %d requests, want 1 (the retry must not reach a cooling key)", got)
	}
}

// TestStreamContinuationMatchesWhatAPrefillCapablePoolReceives covers the pool
// that can resume from a trailing assistant turn: nothing is appended, and the
// resumed request really does end on the preserved partial. The mismatch this
// guards against is a transcript that shows a continuation instruction the
// request never carried, or the reverse.
func TestStreamContinuationMatchesWhatAPrefillCapablePoolReceives(t *testing.T) {
	a, provider, _ := newContinuationTestAgent(t, config.ProviderTypeChatCompletions,
		[]string{"key-a", "key-b"},
		truncatedReply("the first half of the answer"),
		completedReply("and the second half"),
	)
	if !a.llmClient.AllPoolTargetsSupportAssistantPrefillContinuation() {
		t.Skip("chat-completions test pool is not prefill-capable in this build")
	}

	_, err := a.callLLMForRequest(t.Context(), []message.Message{{Role: message.RoleUser, Content: "explain the failure"}}, 0)
	if !llm.IsPreservableStreamInterruption(err) {
		t.Fatalf("callLLM err = %v, want a preservable stream interruption", err)
	}
	a.handleAgentError(Event{Type: EventAgentError, TurnID: a.turn.ID, Payload: err})
	a.flushPersist()

	msgs := a.GetMessages()
	if len(msgs) != 1 {
		t.Fatalf("history = %d messages, want 1 (the preserved partial, no continuation): %#v", len(msgs), msgs)
	}
	if msgs[0].Role != message.RoleAssistant || msgs[0].StopReason != "interrupted" {
		t.Fatalf("only message = %#v, want the preserved partial", msgs[0])
	}

	llmResponseFromEvent(t, waitForMainLLMOutcome(t, a, 10*time.Second))

	requests := provider.recorded()
	if len(requests) != 2 {
		t.Fatalf("provider saw %d requests, want 2", len(requests))
	}
	sent := requests[1].messages
	if len(sent) == 0 {
		t.Fatal("resumed request carried no messages")
	}
	last := sent[len(sent)-1]
	if last.Role != message.RoleAssistant || !strings.Contains(last.Content, "the first half of the answer") {
		t.Fatalf("resumed request ends on %#v, want the preserved partial as the prefill", last)
	}
	if containsMessage(sent, message.RoleUser, streamContinueMessageText) {
		t.Fatalf("prefill-capable pool received the continuation instruction anyway: %#v", sent)
	}
}

// TestConsecutiveResumesKeepEveryPartialAndOneContinuationEach covers a gateway
// that truncates every reply: each round must contribute its own partial and
// exactly one continuation — no dropped text, no duplicated instruction — and
// the final request must carry all of it.
func TestConsecutiveResumesKeepEveryPartialAndOneContinuationEach(t *testing.T) {
	a, provider, providerCfg := newContinuationTestAgent(t, config.ProviderTypeMessages,
		[]string{"key-a", "key-b"},
		truncatedReply("part one"),
		truncatedReply("part two"),
		truncatedReply("part three"),
		completedReply("the ending"),
	)

	_, err := a.callLLMForRequest(t.Context(), []message.Message{{Role: message.RoleUser, Content: "explain the failure"}}, 0)
	if !llm.IsPreservableStreamInterruption(err) {
		t.Fatalf("callLLM err = %v, want a preservable stream interruption", err)
	}

	const rounds = 3
	for round := 1; round <= rounds; round++ {
		a.handleAgentError(Event{Type: EventAgentError, TurnID: a.turn.ID, Payload: err})
		a.flushPersist()

		evt := waitForMainLLMOutcome(t, a, 30*time.Second)
		if round < rounds {
			// Each intermediate round truncates again; feed that error back in
			// exactly as the event loop would.
			payload, ok := evt.Payload.(error)
			if !ok || !llm.IsPreservableStreamInterruption(payload) {
				t.Fatalf("round %d ended with %v / %#v, want another preservable interruption", round, evt.Type, evt.Payload)
			}
			err = payload
			continue
		}
		resp := llmResponseFromEvent(t, evt)
		if !strings.Contains(resp.Content, "the ending") {
			t.Fatalf("final reply = %q, want the completed text", resp.Content)
		}
	}
	a.flushPersist()

	msgs := a.GetMessages()
	var partials, continuations []message.Message
	for _, msg := range msgs {
		switch {
		case msg.Role == message.RoleAssistant && msg.StopReason == "interrupted":
			partials = append(partials, msg)
		case msg.Kind == message.KindStreamContinue:
			continuations = append(continuations, msg)
		}
	}
	if len(partials) != rounds {
		t.Fatalf("history holds %d preserved partials, want %d (one per truncated reply)", len(partials), rounds)
	}
	for i, want := range []string{"part one", "part two", "part three"} {
		if !strings.Contains(partials[i].Content, want) {
			t.Fatalf("preserved partial %d = %q, want it to carry %q", i, partials[i].Content, want)
		}
	}
	if len(continuations) != len(partials) {
		t.Fatalf("history holds %d continuation messages for %d partials, want one each", len(continuations), len(partials))
	}

	requests := provider.recorded()
	if len(requests) != rounds+1 {
		t.Fatalf("provider saw %d requests, want %d", len(requests), rounds+1)
	}
	final := requests[len(requests)-1].messages
	for _, want := range []string{"part one", "part two", "part three"} {
		if !containsMessage(final, message.RoleAssistant, want) {
			t.Fatalf("final request lost the partial %q: %#v", want, final)
		}
	}
	// Every restart keeps the same model: a truncated reply does not advance
	// the sticky model cursor.
	for i, req := range requests {
		if req.model != requests[0].model {
			t.Fatalf("request %d ran on model %q, want all rounds on %q", i, req.model, requests[0].model)
		}
	}
	// Each truncation cools the credential it ran on, so consecutive rounds
	// never reuse the same one back-to-back.
	for i := 1; i < len(requests); i++ {
		if requests[i].apiKey == requests[i-1].apiKey {
			t.Fatalf("requests %d and %d both ran on %q, want each restart off the key that just truncated", i-1, i, requests[i].apiKey)
		}
	}
	// The reply that finally completed cleared its own key's throttle; the one
	// that truncated before it is still cooling.
	if available, total := providerCfg.AvailableKeyCount(); available != total-1 {
		t.Fatalf("available keys = %d of %d, want the last truncated key still cooling", available, total)
	}
}

func isAllKeysCoolingErr(err error) bool {
	_, ok := errors.AsType[*llm.AllKeysCoolingError](err)
	return ok
}

func containsMessage(msgs []message.Message, role message.Role, substr string) bool {
	for _, msg := range msgs {
		if msg.Role == role && strings.Contains(msg.Content, substr) {
			return true
		}
	}
	return false
}
