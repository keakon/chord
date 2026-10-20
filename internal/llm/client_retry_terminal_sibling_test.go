package llm

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/keakon/chord/internal/message"
)

// A provider-scoped parameter rejection (a nonstandard field one relay refuses
// but another accepts) must not end the request while a sibling target is still
// retriable, so the pool can recover instead of surfacing a terminal error.
func TestTerminalPoolFailureKeepsRetrying(t *testing.T) {
	t.Parallel()
	relay := testCompatibleResponsesProviderConfigWithKeys("relay-a", "gpt-test", []string{"k1"})
	official := testOfficialOpenAIProviderConfigWithKeys("official", "gpt-test", []string{"k1"})

	param400 := &APIError{StatusCode: 400, Type: "invalid_parameter", Message: "bad response status code 400"}
	structural400 := &APIError{StatusCode: 400, Message: "invalid tool schema"}
	structural400WithCode := &APIError{StatusCode: 400, Code: "INVALID_ARGUMENT", Type: "invalid_request_error", Message: "invalid tool schema"}
	contract400 := &APIError{StatusCode: 400, Code: invalidResponsesRequestCode, Message: "unsupported request shape"}
	contextLength400 := &APIError{StatusCode: 400, Code: "context_length_exceeded", Message: "prompt is too long"}
	notFound := &APIError{StatusCode: 404, Message: "no such model"}

	cases := []struct {
		name              string
		provider          *ProviderConfig
		err               error
		retriableSiblings int
		want              bool
	}{
		{"param400 relay with retriable sibling", relay, param400, 1, true},
		{"param400 relay with no retriable sibling", relay, param400, 0, false},
		{"param400 trusted provider", official, param400, 1, false},
		{"structural400 is provider-independent", relay, structural400, 1, false},
		{"structural400 keeps its meaning with an explicit code", relay, structural400WithCode, 1, false},
		{"relay client-contract refusal is request-scoped", relay, contract400, 1, false},
		{"context length is request-scoped", relay, contextLength400, 1, false},
		{"deterministic 4xx always stops", relay, notFound, 1, false},
		{"non-API error is not withheld", relay, context.DeadlineExceeded, 1, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := terminalPoolFailureKeepsRetrying(tc.provider, tc.err, tc.retriableSiblings); got != tc.want {
				t.Fatalf("terminalPoolFailureKeepsRetrying = %v, want %v", got, tc.want)
			}
		})
	}
}

// A cooling target never reaches the wire, but its window can end during a
// later round; it must still count as a retriable sibling for the withholding
// decision, otherwise the terminal error ends the request before the pool can
// probe that target again.
func TestStreamRoundAttemptSummaryCountsCoolingSiblingWithoutRequestAttempt(t *testing.T) {
	t.Parallel()
	cfg := testCompatibleResponsesProviderConfigWithKeys("relay-a", "gpt-test", []string{"k1"})
	var summary streamRoundAttemptSummary
	summary.record(streamTargetAttemptResult{
		lastErr:         &AllKeysCoolingError{},
		lastErrProvider: cfg,
	})
	if summary.retriableTargets != 1 {
		t.Fatalf("retriableTargets = %d, want 1 for a cooling sibling", summary.retriableTargets)
	}
	if summary.attemptedTargets != 0 {
		t.Fatalf("attemptedTargets = %d, want 0 for a target that never reached the wire", summary.attemptedTargets)
	}
}

// The pool must keep retrying rounds when a relay rejects a request the start
// target is still retriable on, and recover on the next round.
func TestCompleteStreamTerminalRelay400KeepsRetryingWhileSiblingRetriable(t *testing.T) {
	startCfg := testCompatibleResponsesProviderConfigWithKeys("relay-a", "gpt-test", []string{"k1"})
	disableRetryDelayForTest(startCfg)
	fallbackCfg := testCompatibleResponsesProviderConfigWithKeys("relay-b", "gpt-test", []string{"k2"})
	disableRetryDelayForTest(fallbackCfg)
	startImpl := &recordingProvider{calls: []scriptedCall{
		{err: &APIError{StatusCode: 503, Message: "temporary overload"}},
		{resp: &message.Response{Content: "ok after sibling probe"}},
	}}
	fallbackImpl := &recordingProvider{calls: []scriptedCall{
		{err: &APIError{StatusCode: 400, Type: "invalid_parameter", Message: "bad response status code 400"}},
	}}
	c := NewClient(startCfg, startImpl, "gpt-test", 4096, "sys")
	c.SetFallbackModels([]FallbackModel{{
		ProviderConfig: fallbackCfg,
		ProviderImpl:   fallbackImpl,
		ModelID:        "gpt-test",
		MaxTokens:      4096,
		ContextLimit:   128000,
	}})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := c.CompleteStream(ctx, []message.Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err != nil {
		t.Fatalf("CompleteStream returned error: %v", err)
	}
	if resp == nil || resp.Content != "ok after sibling probe" {
		t.Fatalf("unexpected response: %#v", resp)
	}
	if got := startImpl.CallCount(); got != 2 {
		t.Fatalf("start provider calls = %d, want 2 (retriable round 1 + success round 2)", got)
	}
	if got := fallbackImpl.CallCount(); got != 1 {
		t.Fatalf("fallback provider calls = %d, want 1 (probed once in round 1)", got)
	}
}

// When every attempted target fails with a terminal request-shape error the
// request still stops, so a genuinely malformed request cannot loop.
func TestCompleteStreamTerminalRelay400StopsWhenAllTargetsTerminal(t *testing.T) {
	startCfg := testCompatibleResponsesProviderConfigWithKeys("relay-a", "gpt-test", []string{"k1"})
	disableRetryDelayForTest(startCfg)
	fallbackCfg := testCompatibleResponsesProviderConfigWithKeys("relay-b", "gpt-test", []string{"k2"})
	disableRetryDelayForTest(fallbackCfg)
	terminal400 := &APIError{StatusCode: 400, Type: "invalid_parameter", Message: "bad response status code 400"}
	startImpl := &constantErrProvider{err: terminal400}
	fallbackImpl := &constantErrProvider{err: terminal400}
	c := NewClient(startCfg, startImpl, "gpt-test", 4096, "sys")
	c.SetFallbackModels([]FallbackModel{{
		ProviderConfig: fallbackCfg,
		ProviderImpl:   fallbackImpl,
		ModelID:        "gpt-test",
		MaxTokens:      4096,
		ContextLimit:   128000,
	}})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := c.CompleteStream(ctx, []message.Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err == nil {
		t.Fatalf("CompleteStream err = nil, want the terminal 400; resp=%#v", resp)
	}
	if _, ok := errors.AsType[*APIError](err); !ok {
		t.Fatalf("CompleteStream err = %v, want *APIError", err)
	}
	if startImpl.calls != 1 || fallbackImpl.calls != 1 {
		t.Fatalf("provider calls = (%d, %d), want (1, 1): all-terminal targets must stop after one round", startImpl.calls, fallbackImpl.calls)
	}
}

// Target-scoped refusals must not be returned as full-round request defects.
func TestRequestDefectClassification(t *testing.T) {
	t.Parallel()
	relay := testCompatibleResponsesProviderConfigWithKeys("relay-a", "gpt-test", []string{"k1"})
	official := testOfficialOpenAIProviderConfigWithKeys("official", "gpt-test", []string{"k1"})

	cases := []struct {
		name     string
		provider *ProviderConfig
		err      error
		want     bool
	}{
		{"structural 400 on a relay", relay, &APIError{StatusCode: 400, Message: "invalid tool schema"}, true},
		{"structured 400 keeps its structural meaning", relay, &APIError{StatusCode: 400, Code: "INVALID_ARGUMENT", Type: "invalid_request_error", Message: "invalid tool schema"}, true},
		{"relay client-contract refusal", relay, &APIError{StatusCode: 400, Code: invalidResponsesRequestCode, Message: "unsupported request shape"}, true},
		{"structural 400 on a trusted provider", official, &APIError{StatusCode: 400, Message: "invalid tool schema"}, true},
		{"relay-scoped param 400 stays withheld", relay, &APIError{StatusCode: 400, Type: "invalid_parameter", Message: "bad response status code 400"}, false},
		{"param 400 from a trusted provider", official, &APIError{StatusCode: 400, Type: "invalid_parameter", Message: "bad response status code 400"}, true},
		{"endpoint-scoped 404", relay, &APIError{StatusCode: 404, Message: "no such model"}, false},
		{"replay evidence echo is target-scoped", relay, &ReplayEvidenceEchoError{}, false},
		{"context length is handled by the round pathway", relay, &APIError{StatusCode: 400, Code: "context_length_exceeded", Message: "prompt is too long"}, false},
		{"transient 503", relay, &APIError{StatusCode: 503, Message: "temporary overload"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := requestDefect(tc.provider, tc.err); got != tc.want {
				t.Fatalf("requestDefect = %v, want %v", got, tc.want)
			}
		})
	}
}

// After a failed full round, prefer the request defect to a sibling's transient error.
func TestCompleteStreamRequestDefectStopsDespiteLaterRetriableSibling(t *testing.T) {
	startCfg := testCompatibleResponsesProviderConfigWithKeys("relay-a", "gpt-test", []string{"k1"})
	disableRetryDelayForTest(startCfg)
	fallbackCfg := testCompatibleResponsesProviderConfigWithKeys("relay-b", "gpt-test", []string{"k2"})
	disableRetryDelayForTest(fallbackCfg)
	startImpl := &constantErrProvider{err: &APIError{StatusCode: 400, Message: "invalid tool schema"}}
	fallbackImpl := &constantErrProvider{err: &APIError{StatusCode: 503, Message: "temporary overload"}}
	c := NewClient(startCfg, startImpl, "gpt-test", 4096, "sys")
	c.SetFallbackModels([]FallbackModel{{
		ProviderConfig: fallbackCfg,
		ProviderImpl:   fallbackImpl,
		ModelID:        "gpt-test",
		MaxTokens:      4096,
		ContextLimit:   128000,
	}})

	// A regression here loops rounds instead of returning, so bound the request.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := c.CompleteStream(ctx, []message.Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err == nil {
		t.Fatalf("CompleteStream err = nil, want the structural 400; resp=%#v", resp)
	}
	apiErr, ok := errors.AsType[*APIError](err)
	if !ok || apiErr.StatusCode != 400 || apiErr.Message != "invalid tool schema" {
		t.Fatalf("CompleteStream err = %v, want the structural 400 rather than the sibling's 503", err)
	}
	if startImpl.calls != 1 || fallbackImpl.calls != 1 {
		t.Fatalf("provider calls = (%d, %d), want (1, 1): a request defect must not wait for rounds", startImpl.calls, fallbackImpl.calls)
	}
}

// Withholding a relay-scoped rejection is bounded: once the sibling recovery
// budget lapses the relay's error surfaces instead of the request retrying
// forever. A sibling that never recovers (all keys invalid, a relay that stays
// overloaded) is exactly why the bound exists.
func TestCompleteStreamWithheldTerminal400StopsWhenSiblingBudgetLapses(t *testing.T) {
	previous := withheldTerminalFailureBudget
	withheldTerminalFailureBudget = 0
	t.Cleanup(func() { withheldTerminalFailureBudget = previous })

	for _, terminalFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "terminal last", true: "terminal first"}[terminalFirst], func(t *testing.T) {
			startCfg := testCompatibleResponsesProviderConfigWithKeys("sample-a", "test-model", []string{"k1"})
			fallbackCfg := testCompatibleResponsesProviderConfigWithKeys("sample-b", "test-model", []string{"k2"})
			disableRetryDelayForTest(startCfg)
			disableRetryDelayForTest(fallbackCfg)
			terminal := &APIError{StatusCode: 400, Type: "invalid_parameter", Message: "unsupported parameter"}
			transient := &APIError{StatusCode: 503, Message: "temporary overload"}
			startErr, fallbackErr := error(transient), error(terminal)
			if terminalFirst {
				startErr, fallbackErr = terminal, transient
			}
			startImpl := &constantErrProvider{err: startErr}
			fallbackImpl := &constantErrProvider{err: fallbackErr}
			c := NewClient(startCfg, startImpl, "test-model", 4096, "sys")
			defer c.Close()
			c.SetFallbackModels([]FallbackModel{{ProviderConfig: fallbackCfg, ProviderImpl: fallbackImpl, ModelID: "test-model", MaxTokens: 4096}})
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_, err := c.CompleteStream(ctx, []message.Message{{Role: message.RoleUser, Content: "hello"}}, nil, nil)
			if !errors.Is(err, terminal) || startImpl.calls != 1 || fallbackImpl.calls != 1 {
				t.Fatalf("err=%v calls=(%d,%d), want relay rejection after one full round", err, startImpl.calls, fallbackImpl.calls)
			}
		})
	}
}

// An unsuccessful round returns the structural defect even when it is last.
func TestCompleteStreamStructural400StopsDespiteRetriableSibling(t *testing.T) {
	startCfg := testCompatibleResponsesProviderConfigWithKeys("relay-a", "gpt-test", []string{"k1"})
	disableRetryDelayForTest(startCfg)
	fallbackCfg := testCompatibleResponsesProviderConfigWithKeys("relay-b", "gpt-test", []string{"k2"})
	disableRetryDelayForTest(fallbackCfg)
	startImpl := &constantErrProvider{err: &APIError{StatusCode: 503, Message: "temporary overload"}}
	fallbackImpl := &constantErrProvider{err: &APIError{StatusCode: 400, Message: "invalid tool schema"}}
	c := NewClient(startCfg, startImpl, "gpt-test", 4096, "sys")
	c.SetFallbackModels([]FallbackModel{{
		ProviderConfig: fallbackCfg,
		ProviderImpl:   fallbackImpl,
		ModelID:        "gpt-test",
		MaxTokens:      4096,
		ContextLimit:   128000,
	}})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := c.CompleteStream(ctx, []message.Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err == nil {
		t.Fatalf("CompleteStream err = nil, want the structural 400; resp=%#v", resp)
	}
	if startImpl.calls != 1 || fallbackImpl.calls != 1 {
		t.Fatalf("provider calls = (%d, %d), want (1, 1): the structural defect must end the request", startImpl.calls, fallbackImpl.calls)
	}
}

func TestCompleteStreamRequestDefectAllowsSiblingSuccess(t *testing.T) {
	startCfg := testCompatibleResponsesProviderConfigWithKeys("sample-a", "test-model", []string{"k1"})
	fallbackCfg := testCompatibleResponsesProviderConfigWithKeys("sample-b", "test-model", []string{"k2"})
	startImpl := &constantErrProvider{err: &APIError{StatusCode: 400, Message: "invalid tool schema"}}
	fallbackImpl := &recordingProvider{calls: []scriptedCall{{resp: &message.Response{Content: "hello"}}}}
	c := NewClient(startCfg, startImpl, "test-model", 4096, "sys")
	defer c.Close()
	c.SetFallbackModels([]FallbackModel{{ProviderConfig: fallbackCfg, ProviderImpl: fallbackImpl, ModelID: "test-model", MaxTokens: 4096}})
	resp, err := c.CompleteStream(context.Background(), []message.Message{{Role: message.RoleUser, Content: "hello"}}, nil, nil)
	if err != nil || resp == nil || resp.Content != "hello" || startImpl.calls != 1 || fallbackImpl.CallCount() != 1 {
		t.Fatalf("resp=%+v err=%v; want full-round sibling success", resp, err)
	}
}

func TestCompleteStreamRelayBudgetSurvivesTransientOnlyRound(t *testing.T) {
	previous := withheldTerminalFailureBudget
	withheldTerminalFailureBudget = time.Hour
	t.Cleanup(func() { withheldTerminalFailureBudget = previous })
	startCfg := testCompatibleResponsesProviderConfigWithKeys("sample-a", "test-model", []string{"k1"})
	fallbackCfg := testCompatibleResponsesProviderConfigWithKeys("sample-b", "test-model", []string{"k2"})
	disableRetryDelayForTest(startCfg)
	disableRetryDelayForTest(fallbackCfg)
	terminal := &APIError{StatusCode: 400, Type: "invalid_parameter", Message: "unsupported parameter"}
	transient := &APIError{StatusCode: 503, Message: "temporary overload"}
	startImpl := &constantErrProvider{err: transient}
	fallbackImpl := &recordingProvider{calls: []scriptedCall{{err: terminal}, {err: transient}}}
	c := NewClient(startCfg, startImpl, "test-model", 4096, "sys")
	defer c.Close()
	c.SetFallbackModels([]FallbackModel{{ProviderConfig: fallbackCfg, ProviderImpl: fallbackImpl, ModelID: "test-model", MaxTokens: 4096}})
	retries := 0
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := c.CompleteStream(ctx, []message.Message{{Role: message.RoleUser, Content: "hello"}}, nil, func(d message.StreamDelta) {
		if d.Status != nil && d.Status.Type == message.StatusDeltaRetrying && (d.Status.Detail == "round 2" || d.Status.Detail == "round 3") {
			retries++
			if retries == 2 {
				withheldTerminalFailureBudget = 0
			}
		}
	})
	if !errors.Is(err, terminal) || startImpl.calls != 2 || fallbackImpl.CallCount() != 2 {
		t.Fatalf("err=%v calls=(%d,%d), want saved rejection after transient-only round", err, startImpl.calls, fallbackImpl.CallCount())
	}
}

func TestCompleteStreamRelayBudgetBoundsCoolingWait(t *testing.T) {
	previous := withheldTerminalFailureBudget
	withheldTerminalFailureBudget = 10 * time.Millisecond
	t.Cleanup(func() { withheldTerminalFailureBudget = previous })
	startCfg := testCompatibleResponsesProviderConfigWithKeys("sample-a", "test-model", []string{"k1"})
	fallbackCfg := testCompatibleResponsesProviderConfigWithKeys("sample-b", "test-model", []string{"k2"})
	fallbackCfg.MarkCooldown("k2", time.Minute)
	terminal := &APIError{StatusCode: 400, Type: "invalid_parameter", Message: "unsupported parameter"}
	startImpl := &constantErrProvider{err: terminal}
	fallbackImpl := &recordingProvider{}
	c := NewClient(startCfg, startImpl, "test-model", 4096, "sys")
	defer c.Close()
	c.SetFallbackModels([]FallbackModel{{ProviderConfig: fallbackCfg, ProviderImpl: fallbackImpl, ModelID: "test-model", MaxTokens: 4096}})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := c.CompleteStream(ctx, []message.Message{{Role: message.RoleUser, Content: "hello"}}, nil, nil)
	if !errors.Is(err, terminal) || startImpl.calls != 1 || fallbackImpl.CallCount() != 0 {
		t.Fatalf("err=%v calls=(%d,%d), want rejection before another cooling probe", err, startImpl.calls, fallbackImpl.CallCount())
	}
}

func TestCompleteStreamRelayBudgetCancellationWins(t *testing.T) {
	previous := withheldTerminalFailureBudget
	withheldTerminalFailureBudget = time.Hour
	t.Cleanup(func() { withheldTerminalFailureBudget = previous })
	startCfg := testCompatibleResponsesProviderConfigWithKeys("sample-a", "test-model", []string{"k1"})
	fallbackCfg := testCompatibleResponsesProviderConfigWithKeys("sample-b", "test-model", []string{"k2"})
	disableRetryDelayForTest(startCfg)
	disableRetryDelayForTest(fallbackCfg)
	startImpl := &constantErrProvider{err: &APIError{StatusCode: 400, Type: "invalid_parameter", Message: "unsupported parameter"}}
	fallbackImpl := &constantErrProvider{err: &APIError{StatusCode: 503, Message: "temporary overload"}}
	c := NewClient(startCfg, startImpl, "test-model", 4096, "sys")
	defer c.Close()
	c.SetFallbackModels([]FallbackModel{{ProviderConfig: fallbackCfg, ProviderImpl: fallbackImpl, ModelID: "test-model", MaxTokens: 4096}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, err := c.CompleteStream(ctx, []message.Message{{Role: message.RoleUser, Content: "hello"}}, nil, func(d message.StreamDelta) {
		if d.Status != nil && d.Status.Type == message.StatusDeltaRetrying && d.Status.Detail == "round 2" {
			withheldTerminalFailureBudget = 0
			cancel()
		}
	})
	if !errors.Is(err, context.Canceled) || startImpl.calls != 1 || fallbackImpl.calls != 1 {
		t.Fatalf("err=%v calls=(%d,%d), want cancellation before another round", err, startImpl.calls, fallbackImpl.calls)
	}
}

func TestCompleteStreamTransientOnlyIgnoresRelayBudget(t *testing.T) {
	previous := withheldTerminalFailureBudget
	withheldTerminalFailureBudget = 0
	t.Cleanup(func() { withheldTerminalFailureBudget = previous })
	cfg := testCompatibleResponsesProviderConfigWithKeys("sample", "test-model", []string{"k1"})
	disableRetryDelayForTest(cfg)
	impl := &recordingProvider{calls: []scriptedCall{
		{err: &APIError{StatusCode: 503, Message: "temporary overload"}},
		{resp: &message.Response{Content: "hello"}},
	}}
	c := NewClient(cfg, impl, "test-model", 4096, "sys")
	defer c.Close()
	resp, err := c.CompleteStream(context.Background(), []message.Message{{Role: message.RoleUser, Content: "hello"}}, nil, nil)
	if err != nil || resp == nil || resp.Content != "hello" || impl.CallCount() != 2 {
		t.Fatalf("resp=%+v err=%v calls=%d, want transient recovery", resp, err, impl.CallCount())
	}
}
