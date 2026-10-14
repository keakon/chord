package llm

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
)

func TestClient_400ErrorTriesFallbackModel(t *testing.T) {
	primaryCfg := testProviderConfigWithKeys("strict", "strict-model", []string{"key-a", "key-b"})
	fallbackCfg := testProviderConfig("compatible", "compatible-model")

	primaryImpl := &recordingProvider{}
	primaryImpl.calls = []scriptedCall{
		{err: &APIError{StatusCode: 400, Message: "The `reasoning_content` in the thinking mode must be passed back to the API."}},
	}
	fallbackImpl := &recordingProvider{}
	fallbackImpl.calls = []scriptedCall{{resp: &message.Response{Content: "ok from fallback"}}}

	client := NewClient(primaryCfg, primaryImpl, "strict-model", 512, "")
	client.SetModelPool([]FallbackModel{{
		ProviderConfig: primaryCfg,
		ProviderImpl:   primaryImpl,
		ModelID:        "strict-model",
		MaxTokens:      512,
		ContextLimit:   128000,
	}, {
		ProviderConfig: fallbackCfg,
		ProviderImpl:   fallbackImpl,
		ModelID:        "compatible-model",
		MaxTokens:      512,
		ContextLimit:   128000,
	}}, 0)

	var retryErrors []message.StreamDelta
	resp, err := client.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "hello"}}, nil, func(delta message.StreamDelta) {
		if delta.Type == message.StreamDeltaRetryError {
			retryErrors = append(retryErrors, delta)
		}
	})
	if err != nil {
		t.Fatalf("CompleteStream returned error: %v", err)
	}
	if resp == nil || resp.Content != "ok from fallback" {
		t.Fatalf("unexpected response: %#v", resp)
	}

	if got := len(primaryImpl.apiKeys); got != 1 {
		t.Fatalf("primary call count = %d, want 1", got)
	}
	if got := len(fallbackImpl.apiKeys); got != 1 {
		t.Fatalf("fallback call count = %d, want 1", got)
	}
	st := client.LastCallStatus()
	if !st.FallbackTriggered {
		t.Fatal("expected FallbackTriggered=true")
	}
	if st.RunningModelRef != "compatible/compatible-model" {
		t.Fatalf("RunningModelRef = %q, want compatible/compatible-model", st.RunningModelRef)
	}
	if len(retryErrors) != 1 {
		t.Fatalf("retry error delta count = %d, want 1", len(retryErrors))
	}
	if retryErrors[0].Err == nil || retryErrors[0].Provider != "strict" || retryErrors[0].Model != "strict-model" {
		t.Fatalf("retry error delta = %#v, want primary 400 metadata", retryErrors[0])
	}
}

func TestClientCompleteStreamCompatible400CoolsKeyAndRotates(t *testing.T) {
	cfg := testCompatibleResponsesProviderConfigWithKeys("gateway", "gpt-test", []string{"k1", "k2"})
	disableRetryDelayForTest(cfg)
	impl := &recordingProvider{}
	impl.calls = []scriptedCall{
		{err: &APIError{StatusCode: 400, Message: `all 10 attempts failed: HTTP 403: {"error":{"code":"insufficient_user_quota"}}`}},
		{resp: &message.Response{Content: "ok from k2"}},
	}
	c := NewClient(cfg, impl, "gpt-test", 4096, "sys")

	resp, err := c.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err != nil {
		t.Fatalf("CompleteStream returned error: %v", err)
	}
	if resp == nil || resp.Content != "ok from k2" {
		t.Fatalf("unexpected response: %#v", resp)
	}
	if got := impl.apiKeys; len(got) != 2 || got[0] != "k1" || got[1] != "k2" {
		t.Fatalf("api keys = %#v, want [k1 k2]", got)
	}
}

func TestClientCompleteStreamOfficialPrimaryCompatibleFallback400UsesFallbackProviderSemantics(t *testing.T) {
	primaryCfg := testOfficialOpenAIProviderConfigWithKeys("openai", "primary-model", []string{"openai-key"})
	fallbackCfg := testCompatibleResponsesProviderConfigWithKeys("gateway", "fallback-model", []string{"gateway-key"})
	disableRetryDelayForTest(primaryCfg)
	primaryImpl := &recordingProvider{}
	primaryImpl.calls = []scriptedCall{
		{err: &APIError{StatusCode: 429, Message: "rate limited"}},
	}
	fallbackImpl := &recordingProvider{}
	fallbackImpl.calls = []scriptedCall{
		{err: &APIError{StatusCode: 400, Message: "upstream temporarily busy", RetryAfter: time.Millisecond}},
		{resp: &message.Response{Content: "ok after fallback cooldown"}},
	}
	c := NewClient(primaryCfg, primaryImpl, "primary-model", 4096, "sys")

	resp, err := callCompleteStreamWithRetryForTest(
		c,
		context.Background(),
		primaryCfg,
		primaryImpl,
		"primary-model",
		4096,
		RequestTuning{},
		"",
		[]message.Message{{Role: "user", Content: "hi"}},
		nil,
		nil,
		true,
		[]FallbackModel{{
			ProviderConfig: fallbackCfg,
			ProviderImpl:   fallbackImpl,
			ModelID:        "fallback-model",
			MaxTokens:      4096,
		}},
		2,
		&CallStatus{},
	)
	if err != nil {
		t.Fatalf("completeStreamWithRetry returned error: %v", err)
	}
	if resp == nil || resp.Content != "ok after fallback cooldown" {
		t.Fatalf("unexpected response: %#v", resp)
	}
	if got := primaryImpl.CallCount(); got != 1 {
		t.Fatalf("primary calls = %d, want 1", got)
	}
	if got := fallbackImpl.CallCount(); got != 2 {
		t.Fatalf("fallback calls = %d, want 2 after compatible 400 retries into next round", got)
	}
}

func TestClientCompleteStreamCompatiblePrimaryOfficialFallback400UsesFallbackProviderSemantics(t *testing.T) {
	primaryCfg := testCompatibleResponsesProviderConfigWithKeys("gateway", "primary-model", []string{"gateway-key"})
	fallbackCfg := testOfficialOpenAIProviderConfigWithKeys("openai", "fallback-model", []string{"openai-key"})
	disableRetryDelayForTest(primaryCfg)
	primaryImpl := &recordingProvider{}
	primaryImpl.calls = []scriptedCall{
		{err: &APIError{StatusCode: 429, Message: "rate limited"}},
		{resp: &message.Response{Content: "should not retry after official 400"}},
	}
	fallbackImpl := &recordingProvider{}
	fallbackImpl.calls = []scriptedCall{
		{err: &APIError{StatusCode: 400, Message: "invalid_request_error: missing required parameter: input"}},
	}
	c := NewClient(primaryCfg, primaryImpl, "primary-model", 4096, "sys")

	resp, err := callCompleteStreamWithRetryForTest(
		c,
		context.Background(),
		primaryCfg,
		primaryImpl,
		"primary-model",
		4096,
		RequestTuning{},
		"",
		[]message.Message{{Role: "user", Content: "hi"}},
		nil,
		nil,
		true,
		[]FallbackModel{{
			ProviderConfig: fallbackCfg,
			ProviderImpl:   fallbackImpl,
			ModelID:        "fallback-model",
			MaxTokens:      4096,
		}},
		2,
		&CallStatus{},
	)
	if err == nil {
		t.Fatal("expected official fallback 400 error")
	}
	if resp != nil {
		t.Fatalf("unexpected response: %#v", resp)
	}
	if got := primaryImpl.CallCount(); got != 1 {
		t.Fatalf("primary calls = %d, want 1", got)
	}
	if got := fallbackImpl.CallCount(); got != 1 {
		t.Fatalf("fallback calls = %d, want 1", got)
	}
}

func TestClientCompleteStreamOfficial400DoesNotRetry(t *testing.T) {
	cfg := testOfficialOpenAIProviderConfigWithKeys("openai", "gpt-test", []string{"k1", "k2"})
	impl := &recordingProvider{}
	impl.calls = []scriptedCall{
		{err: &APIError{StatusCode: 400, Message: "invalid_request_error: missing required parameter: input"}},
		{resp: &message.Response{Content: "should not retry"}},
	}
	c := NewClient(cfg, impl, "gpt-test", 4096, "sys")

	var retryErrors []message.StreamDelta
	resp, err := c.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "hi"}}, nil, func(delta message.StreamDelta) {
		if delta.Type == message.StreamDeltaRetryError {
			retryErrors = append(retryErrors, delta)
		}
	})
	if err == nil {
		t.Fatal("expected official 400 error")
	}
	if resp != nil {
		t.Fatalf("unexpected response: %#v", resp)
	}
	if got := impl.apiKeys; len(got) != 1 || got[0] != "k1" {
		t.Fatalf("api keys = %#v, want only [k1]", got)
	}
	if len(retryErrors) != 0 {
		t.Fatalf("retry error delta count = %d, want 0 for terminal official 400: %#v", len(retryErrors), retryErrors)
	}
}

func TestClientCompleteStreamInvisibleTimeoutMarksKeyRecoveringBeforeNextRound(t *testing.T) {
	cfg := testProviderConfigWithKeys("primary-prov", "gpt-test", []string{"k1", "k2"})
	disableRetryDelayForTest(cfg)
	impl := &recordingProvider{}
	impl.calls = []scriptedCall{
		{err: testNetTimeoutErr{timeout: true}},
		{resp: &message.Response{Content: "ok from k2"}},
	}
	c := NewClient(cfg, impl, "gpt-test", 4096, "sys")

	resp, err := c.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err != nil {
		t.Fatalf("CompleteStream returned error: %v", err)
	}
	if resp == nil || resp.Content != "ok from k2" {
		t.Fatalf("unexpected response: %#v", resp)
	}
	if got := impl.apiKeys; len(got) != 2 || got[0] != "k1" || got[1] != "k2" {
		t.Fatalf("api keys = %#v, want [k1 k2]", got)
	}
}

func TestCompleteStreamWithRetryHonorsExplicitMaxAttempts(t *testing.T) {
	cfg := testProviderConfigWithKeys("primary-prov", "primary-model", []string{"k1"})
	impl := &recordingProvider{}
	impl.calls = []scriptedCall{
		{streams: []message.StreamDelta{{Type: "text", Text: "partial-1"}}, err: io.ErrUnexpectedEOF},
		{resp: &message.Response{Content: "should not be reached"}},
	}
	c := NewClient(cfg, impl, "primary-model", 4096, "sys")

	_, err := callCompleteStreamWithRetryForTest(
		c,
		context.Background(),
		cfg,
		impl,
		"primary-model",
		4096,
		RequestTuning{},
		"",
		[]message.Message{{Role: "user", Content: "hi"}},
		nil,
		nil,
		false,
		nil,
		1,
		&CallStatus{},
	)
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("completeStreamWithRetry err = %v, want io.ErrUnexpectedEOF", err)
	}
	if got := impl.CallCount(); got != 1 {
		t.Fatalf("provider calls = %d, want 1 when explicit maxAttempts=1", got)
	}
}

func TestClientCompleteStreamDefaultRetriesOrdinaryErrorsUntilRecovery(t *testing.T) {
	cfg := testProviderConfigWithKeys("primary-prov", "primary-model", []string{"k1"})
	disableRetryDelayForTest(cfg)
	impl := &recordingProvider{}
	impl.calls = []scriptedCall{
		{err: &APIError{StatusCode: 500, Message: "upstream overloaded"}},
		{resp: &message.Response{Content: "recovered"}},
	}
	c := NewClient(cfg, impl, "primary-model", 4096, "sys")

	ctx, cancel := context.WithTimeout(context.Background(), 2500*time.Millisecond)
	defer cancel()
	resp, err := c.CompleteStream(ctx, []message.Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err != nil {
		t.Fatalf("CompleteStream returned error: %v", err)
	}
	if resp == nil || resp.Content != "recovered" {
		t.Fatalf("unexpected response: %#v", resp)
	}
	if got := impl.CallCount(); got != 2 {
		t.Fatalf("provider calls = %d, want 2 after retrying into the next round", got)
	}
}

func TestClientCompleteStreamConnectionEstablishmentTimeoutRetriesProviderNextRound(t *testing.T) {
	cfg := testProviderConfigWithKeys("primary-prov", "primary-model", []string{"k1"})
	disableRetryDelayForTest(cfg)
	impl := &recordingProvider{}
	impl.calls = []scriptedCall{
		{err: tlsHandshakeTimeoutErr{}},
		{resp: &message.Response{Content: "recovered after reconnect"}},
	}
	c := NewClient(cfg, impl, "primary-model", 4096, "sys")

	ctx, cancel := context.WithTimeout(context.Background(), 2500*time.Millisecond)
	defer cancel()
	resp, err := c.CompleteStream(ctx, []message.Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err != nil {
		t.Fatalf("CompleteStream returned error: %v", err)
	}
	if resp == nil || resp.Content != "recovered after reconnect" {
		t.Fatalf("unexpected response: %#v", resp)
	}
	if got := impl.CallCount(); got != 2 {
		t.Fatalf("provider calls = %d, want 2 after retrying the provider in the next round", got)
	}
}

func TestClientCompleteConnectionEstablishmentTimeoutRetriesProviderNextRound(t *testing.T) {
	cfg := testProviderConfigWithKeys("primary-prov", "primary-model", []string{"k1"})
	disableRetryDelayForTest(cfg)
	impl := &recordingProvider{}
	impl.calls = []scriptedCall{
		{err: tlsHandshakeTimeoutErr{}},
		{resp: &message.Response{Content: "recovered after reconnect"}},
	}
	c := NewClient(cfg, impl, "primary-model", 4096, "sys")

	ctx, cancel := context.WithTimeout(context.Background(), 2500*time.Millisecond)
	defer cancel()
	resp, err := c.CompleteStream(ctx, []message.Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err != nil {
		t.Fatalf("Complete returned error: %v", err)
	}
	if resp == nil || resp.Content != "recovered after reconnect" {
		t.Fatalf("unexpected response: %#v", resp)
	}
	if got := impl.CallCount(); got != 2 {
		t.Fatalf("provider calls = %d, want 2 after retrying the provider in the next round", got)
	}
}

func TestClientReadTimeoutSkipsRemainingKeysAndFallsBack(t *testing.T) {
	primaryCfg := testProviderConfigWithKeys("primary-prov", "primary-model", []string{"k1", "k2"})
	fallbackCfg := testProviderConfig("fallback-prov", "fallback-model")

	primaryImpl := &recordingProvider{}
	primaryImpl.calls = []scriptedCall{
		{err: testNetTimeoutErr{timeout: true}},
	}
	fallbackImpl := &scriptedProvider{
		calls: []scriptedCall{
			{resp: &message.Response{Content: "from fallback"}},
		},
	}

	c := NewClient(primaryCfg, primaryImpl, "primary-model", 4096, "sys")
	c.SetFallbackModels([]FallbackModel{{
		ProviderConfig: fallbackCfg,
		ProviderImpl:   fallbackImpl,
		ModelID:        "fallback-model",
		MaxTokens:      4096,
		ContextLimit:   128000,
	}})

	resp, err := c.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}
	if resp == nil || resp.Content != "from fallback" {
		t.Fatalf("unexpected response: %#v", resp)
	}
	if primaryImpl.CallCount() != 1 {
		t.Fatalf("initial-entry CallCount=%d, want 1 (invisible timeout skips second key)", primaryImpl.CallCount())
	}
	if len(primaryImpl.apiKeys) != 1 || primaryImpl.apiKeys[0] != "k1" {
		t.Fatalf("initial-entry keys = %#v, want [k1]", primaryImpl.apiKeys)
	}
	if fallbackImpl.CallCount() != 1 {
		t.Fatalf("fallback CallCount=%d, want 1", fallbackImpl.CallCount())
	}
	status := c.LastCallStatus()
	if !status.FallbackTriggered {
		t.Fatal("expected FallbackTriggered=true")
	}
	if status.FallbackReason != "timeout" {
		t.Fatalf("FallbackReason = %q, want timeout", status.FallbackReason)
	}
}

func TestClientReadTimeoutSkipsSiblingTargetsOnSameProvider(t *testing.T) {
	primaryCfg := NewProviderConfig("primary-prov", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"primary-model": {
				Limit: config.ModelLimit{Context: 128000, Output: 4096},
			},
			"sibling-model": {
				Limit: config.ModelLimit{Context: 128000, Output: 4096},
			},
		},
	}, []string{"k1", "k2"})
	fallbackCfg := testProviderConfig("fallback-prov", "fallback-model")

	primaryImpl := &recordingProvider{}
	primaryImpl.calls = []scriptedCall{{err: testNetTimeoutErr{timeout: true}}}
	siblingImpl := &recordingProvider{}
	siblingImpl.calls = []scriptedCall{{resp: &message.Response{Content: "sibling should not run"}}}
	fallbackImpl := &scriptedProvider{
		calls: []scriptedCall{{resp: &message.Response{Content: "from fallback"}}},
	}

	c := NewClient(primaryCfg, primaryImpl, "primary-model", 4096, "sys")
	c.SetFallbackModels([]FallbackModel{
		{
			ProviderConfig: primaryCfg,
			ProviderImpl:   siblingImpl,
			ModelID:        "sibling-model",
			MaxTokens:      4096,
			ContextLimit:   128000,
		},
		{
			ProviderConfig: fallbackCfg,
			ProviderImpl:   fallbackImpl,
			ModelID:        "fallback-model",
			MaxTokens:      4096,
			ContextLimit:   128000,
		},
	})

	resp, err := c.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}
	if resp == nil || resp.Content != "from fallback" {
		t.Fatalf("unexpected response: %#v", resp)
	}
	if primaryImpl.CallCount() != 1 {
		t.Fatalf("primary CallCount=%d, want 1", primaryImpl.CallCount())
	}
	if len(primaryImpl.apiKeys) != 1 || primaryImpl.apiKeys[0] != "k1" {
		t.Fatalf("primary keys = %#v, want [k1]", primaryImpl.apiKeys)
	}
	if siblingImpl.CallCount() != 0 {
		t.Fatalf("sibling same-provider CallCount=%d, want 0", siblingImpl.CallCount())
	}
	if fallbackImpl.CallCount() != 1 {
		t.Fatalf("fallback CallCount=%d, want 1", fallbackImpl.CallCount())
	}
	status := c.LastCallStatus()
	if !status.FallbackTriggered {
		t.Fatal("expected FallbackTriggered=true")
	}
	if status.FallbackReason != "timeout" {
		t.Fatalf("FallbackReason = %q, want timeout", status.FallbackReason)
	}
}

func TestClientReadTimeoutWithoutFallbackDoesNotRotateKeyWithinRound(t *testing.T) {
	primaryCfg := testProviderConfigWithKeys("primary-prov", "primary-model", []string{"k1", "k2"})
	primaryImpl := &recordingProvider{}
	primaryImpl.calls = []scriptedCall{
		{err: testNetTimeoutErr{timeout: true}},
		{resp: &message.Response{Content: "second round ok"}},
	}
	c := NewClient(primaryCfg, primaryImpl, "primary-model", 4096, "sys")
	c.SetStreamRetryRounds(1)

	resp, err := c.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if resp != nil {
		t.Fatalf("resp = %#v, want nil", resp)
	}
	if !isTimeoutLikeError(err) {
		t.Fatalf("err = %v, want timeout-like error", err)
	}
	if primaryImpl.CallCount() != 1 {
		t.Fatalf("initial-entry CallCount=%d, want 1 in a single round", primaryImpl.CallCount())
	}
	if len(primaryImpl.apiKeys) != 1 || primaryImpl.apiKeys[0] != "k1" {
		t.Fatalf("initial-entry keys = %#v, want [k1]", primaryImpl.apiKeys)
	}
}

func TestClientEmptyStopResponseFallsBack(t *testing.T) {
	primaryCfg := testProviderConfig("primary-prov", "primary-model")
	fallbackCfg := testProviderConfig("fallback-prov", "fallback-model")

	primaryImpl := &scriptedProvider{
		calls: []scriptedCall{
			{resp: &message.Response{StopReason: "stop"}},
		},
	}
	fallbackImpl := &scriptedProvider{
		calls: []scriptedCall{
			{resp: &message.Response{Content: "from fallback"}},
		},
	}

	c := NewClient(primaryCfg, primaryImpl, "primary-model", 4096, "sys")
	c.SetFallbackModels([]FallbackModel{{
		ProviderConfig: fallbackCfg,
		ProviderImpl:   fallbackImpl,
		ModelID:        "fallback-model",
		MaxTokens:      4096,
		ContextLimit:   128000,
	}})

	resp, err := c.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}
	if resp == nil || resp.Content != "from fallback" {
		t.Fatalf("unexpected response: %#v", resp)
	}
	if primaryImpl.CallCount() != 1 {
		t.Fatalf("initial-entry CallCount=%d, want 1", primaryImpl.CallCount())
	}
	if fallbackImpl.CallCount() != 1 {
		t.Fatalf("fallback CallCount=%d, want 1", fallbackImpl.CallCount())
	}
}

func TestClientCompleteStopsAfterAnthropicThinkingReplay400WithoutExtraRounds(t *testing.T) {
	primaryCfg := testProviderConfig("primary-prov", "primary-model")
	impl := &constantErrProvider{err: &APIError{StatusCode: 400, Message: "The `content[].thinking` in the thinking mode must be passed back to the API."}}

	c := NewClient(primaryCfg, impl, "primary-model", 4096, "sys")
	_, err := c.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if apiErr, ok := errors.AsType[*APIError](err); !ok || apiErr.StatusCode != 400 {
		t.Fatalf("err = %v, want APIError 400", err)
	}
	if impl.calls != 1 {
		t.Fatalf("provider calls = %d, want 1", impl.calls)
	}
}

func TestClientCompleteStopsAfterPermanentRequestShape400WithoutExtraRounds(t *testing.T) {
	primaryCfg := testProviderConfig("primary-prov", "primary-model")
	impl := &constantErrProvider{err: &APIError{StatusCode: 400, Message: "Invalid assistant message: content or tool_calls must be set"}}

	c := NewClient(primaryCfg, impl, "primary-model", 4096, "sys")
	_, err := c.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if apiErr, ok := errors.AsType[*APIError](err); !ok || apiErr.StatusCode != 400 {
		t.Fatalf("err = %v, want APIError 400", err)
	}
	if impl.calls != 1 {
		t.Fatalf("provider calls = %d, want 1", impl.calls)
	}
}

func TestClientCompleteStopsAfterModelIncompatible400WithoutFallback(t *testing.T) {
	primaryCfg := testProviderConfig("primary-prov", "primary-model")
	impl := &constantErrProvider{err: &APIError{StatusCode: 400, Message: `{"detail":"Stream must be set to true"}`}}

	c := NewClient(primaryCfg, impl, "primary-model", 4096, "sys")
	_, err := c.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if apiErr, ok := errors.AsType[*APIError](err); !ok || apiErr.StatusCode != 400 {
		t.Fatalf("err = %v, want APIError 400", err)
	}
	if impl.calls != 1 {
		t.Fatalf("provider calls = %d, want 1", impl.calls)
	}
}

func TestClientCompleteRetriesCompatibleOverloaded400(t *testing.T) {
	primaryCfg := testCompatibleResponsesProviderConfigWithKeys("gateway", "gpt-test", []string{"k1", "k2"})
	disableRetryDelayForTest(primaryCfg)
	impl := &scriptedProvider{calls: []scriptedCall{
		{err: &APIError{StatusCode: 400, Message: "Our servers are currently overloaded. Please try again later."}},
		{resp: &message.Response{Content: "ok after retry"}},
	}}

	c := NewClient(primaryCfg, impl, "gpt-test", 4096, "sys")
	resp, err := c.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}
	if resp == nil || resp.Content != "ok after retry" {
		t.Fatalf("response = %#v, want ok after retry", resp)
	}
	if impl.CallCount() != 2 {
		t.Fatalf("provider calls = %d, want 2", impl.CallCount())
	}
}

func TestClientCompleteStreamStopsAfterModelIncompatible400WhenFallbackPoolExhausted(t *testing.T) {
	primaryCfg := testProviderConfig("primary-prov", "primary-model")
	fallbackCfg := testProviderConfig("fallback-prov", "fallback-model")
	primaryImpl := &constantErrProvider{err: &APIError{StatusCode: 400, Message: `{"detail":"Stream must be set to true"}`}}
	fallbackImpl := &constantErrProvider{err: &APIError{StatusCode: 400, Message: `{"detail":"Stream must be set to true"}`}}

	c := NewClient(primaryCfg, primaryImpl, "primary-model", 4096, "sys")
	c.SetFallbackModels([]FallbackModel{{
		ProviderConfig: fallbackCfg,
		ProviderImpl:   fallbackImpl,
		ModelID:        "fallback-model",
		MaxTokens:      4096,
		ContextLimit:   128000,
	}})

	_, err := c.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if apiErr, ok := errors.AsType[*APIError](err); !ok || apiErr.StatusCode != 400 {
		t.Fatalf("err = %v, want APIError 400", err)
	}
	if primaryImpl.calls != 1 {
		t.Fatalf("initial-entry calls = %d, want 1", primaryImpl.calls)
	}
	if fallbackImpl.calls != 1 {
		t.Fatalf("fallback calls = %d, want 1", fallbackImpl.calls)
	}
	status := c.LastCallStatus()
	if !status.FallbackTriggered {
		t.Fatal("expected FallbackTriggered=true")
	}
	if !status.FallbackExhausted {
		t.Fatal("expected FallbackExhausted=true")
	}
}

func TestClientDialTimeoutSkipsRemainingKeys(t *testing.T) {
	primaryCfg := testProviderConfigWithKeys("primary-prov", "primary-model", []string{"k1", "k2"})
	fallbackCfg := testProviderConfig("fallback-prov", "fallback-model")

	dialTimeout := &net.OpError{Op: "dial", Err: testNetTimeoutErr{timeout: true}}
	primaryImpl := &scriptedProvider{
		calls: []scriptedCall{
			{err: dialTimeout},
		},
	}
	fallbackImpl := &scriptedProvider{
		calls: []scriptedCall{
			{resp: &message.Response{Content: "from fallback"}},
		},
	}

	c := NewClient(primaryCfg, primaryImpl, "primary-model", 4096, "sys")
	c.SetFallbackModels([]FallbackModel{{
		ProviderConfig: fallbackCfg,
		ProviderImpl:   fallbackImpl,
		ModelID:        "fallback-model",
		MaxTokens:      4096,
		ContextLimit:   128000,
	}})

	resp, err := c.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}
	if resp == nil || resp.Content != "from fallback" {
		t.Fatalf("unexpected response: %#v", resp)
	}
	if primaryImpl.CallCount() != 1 {
		t.Fatalf("initial-entry CallCount=%d, want 1 (dial failure skips second key)", primaryImpl.CallCount())
	}
}

func TestClientTLSHandshakeTimeoutSkipsRemainingKeys(t *testing.T) {
	primaryCfg := testProviderConfigWithKeys("primary-prov", "primary-model", []string{"k1", "k2"})
	fallbackCfg := testProviderConfig("fallback-prov", "fallback-model")

	primaryImpl := &scriptedProvider{
		calls: []scriptedCall{
			{err: tlsHandshakeTimeoutErr{}},
		},
	}
	fallbackImpl := &scriptedProvider{
		calls: []scriptedCall{
			{resp: &message.Response{Content: "from fallback"}},
		},
	}

	c := NewClient(primaryCfg, primaryImpl, "primary-model", 4096, "sys")
	c.SetFallbackModels([]FallbackModel{{
		ProviderConfig: fallbackCfg,
		ProviderImpl:   fallbackImpl,
		ModelID:        "fallback-model",
		MaxTokens:      4096,
		ContextLimit:   128000,
	}})

	resp, err := c.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}
	if resp == nil || resp.Content != "from fallback" {
		t.Fatalf("unexpected response: %#v", resp)
	}
	if primaryImpl.CallCount() != 1 {
		t.Fatalf("initial-entry CallCount=%d, want 1 (TLS handshake timeout skips second key)", primaryImpl.CallCount())
	}
}

func TestCompleteStreamDoesNotRetryOtherKeysWhenContextCancelled(t *testing.T) {
	cfg := testProviderConfigWithKeys("p", "m", []string{"k1", "k2"})
	impl := &scriptedProvider{
		calls: []scriptedCall{
			{resp: &message.Response{Content: "should not be reached"}},
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	c := NewClient(cfg, impl, "m", 4096, "sys")
	_, err := c.CompleteStream(ctx, []message.Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("CompleteStream err = %v, want context.Canceled", err)
	}
	if impl.CallCount() != 0 {
		t.Fatalf("provider CallCount=%d, want 0 (cancel should short-circuit before any request)", impl.CallCount())
	}
}
