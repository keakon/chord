package llm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"
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

// unresolvedHostErr mirrors what the HTTP transport returns when the provider
// host cannot be resolved, wrapped the way the request path wraps it.
func unresolvedHostErr() error {
	return fmt.Errorf("send request: %w", &url.Error{
		Op:  "Post",
		URL: "https://example.invalid/v1/responses",
		Err: &net.OpError{Op: "dial", Net: "tcp", Err: &net.DNSError{Err: "no such host", Name: "example.invalid"}},
	})
}

// shrinkConnectFailureLadder shrinks the connect-failure ladder for a test and
// restores the production shape on cleanup.
func shrinkConnectFailureLadder(t *testing.T, base, ceiling time.Duration, jitter bool) {
	t.Helper()
	prevBase, prevCeiling, prevJitter := connectFailureRetryBase, connectFailureRetryCeiling, connectFailureRetryJitter
	connectFailureRetryBase, connectFailureRetryCeiling, connectFailureRetryJitter = base, ceiling, jitter
	t.Cleanup(func() {
		connectFailureRetryBase, connectFailureRetryCeiling, connectFailureRetryJitter = prevBase, prevCeiling, prevJitter
	})
}

func TestConnectFailureRetryDelayLadder(t *testing.T) {
	shrinkConnectFailureLadder(t, time.Second, 20*time.Second, false)
	// One rung per consecutive connect-failed round: 1s doubling to the 20s cap.
	for _, tc := range []struct {
		failures int
		want     time.Duration
	}{
		{1, time.Second},
		{2, 2 * time.Second},
		{3, 4 * time.Second},
		{4, 8 * time.Second},
		{5, 16 * time.Second},
		{6, 20 * time.Second},
		{7, 20 * time.Second},
		{50, 20 * time.Second},
	} {
		if got := connectFailureRetryDelay(tc.failures); got != tc.want {
			t.Fatalf("connectFailureRetryDelay(%d) = %v, want %v", tc.failures, got, tc.want)
		}
	}
	// With jitter the wait keeps at least half the rung and randomizes the rest.
	shrinkConnectFailureLadder(t, time.Second, 20*time.Second, true)
	for range 50 {
		got := connectFailureRetryDelay(1)
		if got < 500*time.Millisecond || got >= time.Second {
			t.Fatalf("jittered first rung = %v, want in [500ms, 1s)", got)
		}
	}
}

func TestClientCompleteStreamConnectFailureLadderUnderNoneBackoff(t *testing.T) {
	cfg := testProviderConfigWithKeys("primary-prov", "primary-model", []string{"k1"})
	// retry_backoff: none — the ladder must pace the round regardless.
	disableRetryDelayForTest(cfg)
	impl := &recordingProvider{}
	impl.calls = []scriptedCall{
		{err: unresolvedHostErr()},
		{err: unresolvedHostErr()},
		{resp: &message.Response{Content: "recovered"}},
	}
	c := NewClient(cfg, impl, "primary-model", 4096, "sys")

	shrinkConnectFailureLadder(t, 10*time.Millisecond, 40*time.Millisecond, true)

	type retryStatus struct {
		at       time.Time
		deadline time.Time
	}
	var statuses []retryStatus
	cb := func(delta message.StreamDelta) {
		if delta.Status != nil && delta.Status.Type == message.StatusDeltaRetrying {
			statuses = append(statuses, retryStatus{at: time.Now(), deadline: delta.Status.Deadline})
		}
	}
	start := time.Now()
	resp, err := callCompleteStreamWithRetryForTest(
		c, context.Background(), cfg, impl, "primary-model", 4096, RequestTuning{}, "",
		[]message.Message{{Role: "user", Content: "hi"}}, nil, cb, false, nil, 0, &CallStatus{},
	)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("completeStreamWithRetry err = %v", err)
	}
	if resp == nil || resp.Content != "recovered" {
		t.Fatalf("unexpected response: %#v", resp)
	}
	if got := impl.CallCount(); got != 3 {
		t.Fatalf("provider calls = %d, want 3", got)
	}
	// Two floored waits: the first rung is at least half the 10ms base, the
	// second at least half of 20ms.
	if elapsed < 15*time.Millisecond {
		t.Fatalf("two connect-failed rounds completed in %v, want >= the ladder waits", elapsed)
	}
	if len(statuses) != 2 {
		t.Fatalf("retry statuses = %d, want 2", len(statuses))
	}
	for i, st := range statuses {
		if st.deadline.IsZero() {
			t.Fatalf("retry status %d carried no deadline", i)
		}
	}
}

func TestClientCompleteStreamConnectFailureLadderResetsAfterServerRound(t *testing.T) {
	cfg := testProviderConfigWithKeys("primary-prov", "primary-model", []string{"k1"})
	disableRetryDelayForTest(cfg)
	impl := &recordingProvider{}
	impl.calls = []scriptedCall{
		{err: unresolvedHostErr()},
		{err: &APIError{StatusCode: 500, Message: "upstream overloaded"}},
		{err: unresolvedHostErr()},
		{resp: &message.Response{Content: "recovered"}},
	}
	c := NewClient(cfg, impl, "primary-model", 4096, "sys")

	// Jitter off: each rung is exact, so the second connect failure must wait
	// the first rung (10ms) again, not the second (20ms).
	shrinkConnectFailureLadder(t, 10*time.Millisecond, 40*time.Millisecond, false)

	type retryStatus struct {
		at       time.Time
		deadline time.Time
	}
	var statuses []retryStatus
	cb := func(delta message.StreamDelta) {
		if delta.Status != nil && delta.Status.Type == message.StatusDeltaRetrying {
			statuses = append(statuses, retryStatus{at: time.Now(), deadline: delta.Status.Deadline})
		}
	}
	resp, err := callCompleteStreamWithRetryForTest(
		c, context.Background(), cfg, impl, "primary-model", 4096, RequestTuning{}, "",
		[]message.Message{{Role: "user", Content: "hi"}}, nil, cb, false, nil, 0, &CallStatus{},
	)
	if err != nil {
		t.Fatalf("completeStreamWithRetry err = %v", err)
	}
	if resp == nil || resp.Content != "recovered" {
		t.Fatalf("unexpected response: %#v", resp)
	}
	if got := impl.CallCount(); got != 4 {
		t.Fatalf("provider calls = %d, want 4", got)
	}
	if len(statuses) != 3 {
		t.Fatalf("retry statuses = %d, want 3", len(statuses))
	}
	// The server responded in round two, so the ladder restarts: the wait
	// promised after the third round is the first rung again.
	for _, i := range []int{0, 2} {
		wait := statuses[i].deadline.Sub(statuses[i].at)
		if wait < 9*time.Millisecond || wait > 13*time.Millisecond {
			t.Fatalf("retry status %d promised %v, want the first rung (10ms)", i, wait)
		}
	}
}

func TestClientCompleteStreamAPIErrorRetryKeepsZeroDelay(t *testing.T) {
	cfg := testProviderConfigWithKeys("primary-prov", "primary-model", []string{"k1"})
	disableRetryDelayForTest(cfg)
	impl := &recordingProvider{}
	impl.calls = []scriptedCall{
		{err: &APIError{StatusCode: 500, Message: "upstream overloaded"}},
		{resp: &message.Response{Content: "recovered"}},
	}
	c := NewClient(cfg, impl, "primary-model", 4096, "sys")

	var statuses []message.StatusDelta
	cb := func(delta message.StreamDelta) {
		if delta.Status != nil {
			statuses = append(statuses, *delta.Status)
		}
	}
	resp, err := callCompleteStreamWithRetryForTest(
		c, context.Background(), cfg, impl, "primary-model", 4096, RequestTuning{}, "",
		[]message.Message{{Role: "user", Content: "hi"}}, nil, cb, false, nil, 0, &CallStatus{},
	)
	if err != nil {
		t.Fatalf("completeStreamWithRetry err = %v", err)
	}
	if resp == nil || resp.Content != "recovered" {
		t.Fatalf("unexpected response: %#v", resp)
	}
	// An in-band API error reached the server: with backoff disabled the retry
	// round re-probes immediately and promises no countdown.
	for _, st := range statuses {
		if st.Type == message.StatusDeltaRetrying && !st.Deadline.IsZero() {
			t.Fatalf("non-connect retry status carried a deadline: %+v", st)
		}
	}
}

// TestClientCompleteStreamConnectLadderIgnoresNeverSentSibling guards the
// round-level ladder verdict: a sibling target that never went on the wire
// (no usable keys) is the round's last target, so its error used to become
// the round's lastErr and bypass the connect ladder — zero-wait retries on
// the DNS-failing sibling, exactly the storm the ladder exists to stop.
func TestClientCompleteStreamConnectLadderIgnoresNeverSentSibling(t *testing.T) {
	primaryCfg := testProviderConfigWithKeys("primary-prov", "primary-model", []string{"k1"})
	disableRetryDelayForTest(primaryCfg)
	primaryImpl := &recordingProvider{}
	primaryImpl.calls = []scriptedCall{
		{err: unresolvedHostErr()},
		{err: unresolvedHostErr()},
		{resp: &message.Response{Content: "recovered"}},
	}
	siblingCfg := testProviderConfigWithKeys("sibling-prov", "sibling-model", []string{"k-dead"})
	siblingCfg.mu.Lock()
	siblingCfg.keyStates[0].Invalid = true
	siblingCfg.mu.Unlock()
	siblingImpl := &recordingProvider{}

	client := NewClient(primaryCfg, primaryImpl, "primary-model", 4096, "sys")
	client.SetModelPool([]FallbackModel{
		{ProviderConfig: primaryCfg, ProviderImpl: primaryImpl, ModelID: "primary-model", MaxTokens: 4096, ContextLimit: 128000},
		{ProviderConfig: siblingCfg, ProviderImpl: siblingImpl, ModelID: "sibling-model", MaxTokens: 4096, ContextLimit: 128000},
	}, 0)

	shrinkConnectFailureLadder(t, 10*time.Millisecond, 40*time.Millisecond, true)

	var statuses []message.StatusDelta
	resp, err := client.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "hi"}}, nil, func(delta message.StreamDelta) {
		// Round-delay statuses carry the promised wait; the fallback target's
		// own "fallback:" status is a target switch, not a pacing promise.
		if delta.Status != nil && delta.Status.Type == message.StatusDeltaRetrying && strings.HasPrefix(delta.Status.Detail, "round ") {
			statuses = append(statuses, *delta.Status)
		}
	})
	if err != nil {
		t.Fatalf("CompleteStream err = %v", err)
	}
	if resp == nil || resp.Content != "recovered" {
		t.Fatalf("unexpected response: %#v", resp)
	}
	if got := primaryImpl.CallCount(); got != 3 {
		t.Fatalf("primary calls = %d, want 3", got)
	}
	if got := siblingImpl.CallCount(); got != 0 {
		t.Fatalf("sibling calls = %d, want 0 (no usable keys)", got)
	}
	if len(statuses) != 2 {
		t.Fatalf("round retry statuses = %d, want 2", len(statuses))
	}
	for i, st := range statuses {
		if st.Deadline.IsZero() {
			t.Fatalf("round retry status %d carried no deadline: %+v", i, st)
		}
	}
}

// TestClientCompleteStreamConnectLadderResetsWhenSiblingReachesServer mirrors
// the reset direction: the round's last target failed connect-phase (DNS), but
// an earlier sibling reached the server and got a 500. A lastErr-based verdict
// counts that round as connect-failed and lets the ladder climb; the
// round-level verdict restarts it, so the next connect wait is the first rung.
func TestClientCompleteStreamConnectLadderResetsWhenSiblingReachesServer(t *testing.T) {
	primaryCfg := testProviderConfigWithKeys("primary-prov", "primary-model", []string{"k1"})
	disableRetryDelayForTest(primaryCfg)
	primaryImpl := &recordingProvider{}
	primaryImpl.calls = []scriptedCall{
		{err: &APIError{StatusCode: 500, Message: "upstream overloaded"}},
		{err: unresolvedHostErr()},
		{resp: &message.Response{Content: "recovered"}},
	}
	siblingCfg := testProviderConfigWithKeys("sibling-prov", "sibling-model", []string{"k2"})
	disableRetryDelayForTest(siblingCfg)
	siblingImpl := &recordingProvider{}
	siblingImpl.calls = []scriptedCall{
		{err: unresolvedHostErr()},
		{err: unresolvedHostErr()},
	}

	client := NewClient(primaryCfg, primaryImpl, "primary-model", 4096, "sys")
	client.SetModelPool([]FallbackModel{
		{ProviderConfig: primaryCfg, ProviderImpl: primaryImpl, ModelID: "primary-model", MaxTokens: 4096, ContextLimit: 128000},
		{ProviderConfig: siblingCfg, ProviderImpl: siblingImpl, ModelID: "sibling-model", MaxTokens: 4096, ContextLimit: 128000},
	}, 0)

	// Jitter off: each rung is exact, so the wait after the server round must
	// be the first rung (10ms), not the second (20ms).
	shrinkConnectFailureLadder(t, 10*time.Millisecond, 40*time.Millisecond, false)

	type retryStatus struct {
		at       time.Time
		deadline time.Time
	}
	var statuses []retryStatus
	resp, err := client.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "hi"}}, nil, func(delta message.StreamDelta) {
		// Round-delay statuses carry the promised wait; the fallback target's
		// own "fallback:" status is a target switch, not a pacing promise.
		if delta.Status != nil && delta.Status.Type == message.StatusDeltaRetrying && strings.HasPrefix(delta.Status.Detail, "round ") {
			statuses = append(statuses, retryStatus{at: time.Now(), deadline: delta.Status.Deadline})
		}
	})
	if err != nil {
		t.Fatalf("CompleteStream err = %v", err)
	}
	if resp == nil || resp.Content != "recovered" {
		t.Fatalf("unexpected response: %#v", resp)
	}
	if got := primaryImpl.CallCount(); got != 3 {
		t.Fatalf("primary calls = %d, want 3", got)
	}
	if got := siblingImpl.CallCount(); got != 2 {
		t.Fatalf("sibling calls = %d, want 2", got)
	}
	if len(statuses) != 2 {
		t.Fatalf("round retry statuses = %d, want 2", len(statuses))
	}
	// Round one ended with a 500 from the primary: the server was reached, so
	// the immediate re-probe promises no countdown.
	if !statuses[0].deadline.IsZero() {
		t.Fatalf("server-round retry status promised %v, want no deadline", statuses[0].deadline)
	}
	// Round two failed connect-phase, but the ladder had been restarted, so
	// the promised wait is the first rung again.
	wait := statuses[1].deadline.Sub(statuses[1].at)
	if wait < 9*time.Millisecond || wait > 13*time.Millisecond {
		t.Fatalf("retry after the server round promised %v, want the first rung (10ms)", wait)
	}
}
