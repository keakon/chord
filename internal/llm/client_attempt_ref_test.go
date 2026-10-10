package llm

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
)

// gatedProvider blocks its streaming call until the test releases it, so the
// test can read the client while a request is genuinely in flight.
type gatedProvider struct {
	started   chan struct{}
	release   chan struct{}
	startOnce sync.Once
	resp      *message.Response
}

func newGatedProvider(resp *message.Response) *gatedProvider {
	return &gatedProvider{
		started: make(chan struct{}),
		release: make(chan struct{}),
		resp:    resp,
	}
}

func (p *gatedProvider) CompleteStream(
	ctx context.Context,
	_ string,
	_ string,
	_ string,
	_ []message.Message,
	_ []message.ToolDefinition,
	_ int,
	_ RequestTuning,
	_ StreamCallback,
) (*message.Response, error) {
	p.startOnce.Do(func() { close(p.started) })
	select {
	case <-p.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if p.resp != nil {
		return p.resp, nil
	}
	return &message.Response{}, nil
}

// TestAttemptModelRefNamesInflightFallbackTarget pins the display-only attempt
// identity on the client: while the retry loop has dispatched the request to a
// fallback that has produced no output yet, DisplayModelRef already names that
// fallback, RunningModelRef still names the last confirmed model, and the
// attempt ref is cleared once the request returns so an idle surface falls back
// to the next cursor target.
func TestAttemptModelRefNamesInflightFallbackTarget(t *testing.T) {
	primaryImpl := &scriptedProvider{
		calls: []scriptedCall{{err: &APIError{StatusCode: 500, Message: "primary unavailable"}}},
	}
	fallbackImpl := newGatedProvider(&message.Response{Content: "fallback reply", StopReason: "stop"})

	c := NewClient(testProviderConfig("primary-prov", "primary-model"), primaryImpl, "primary-model", 4096, "sys")
	c.SetFallbackModels([]FallbackModel{{
		ProviderConfig: testProviderConfig("fallback-prov", "fallback-model"),
		ProviderImpl:   fallbackImpl,
		ModelID:        "fallback-model",
		MaxTokens:      4096,
		ContextLimit:   64000,
		InputLimit:     64000,
	}})

	if got, active := c.DisplayModelRef(); active {
		t.Fatalf("DisplayModelRef before any request = %q, want no active request", got)
	}

	done := make(chan error, 1)
	go func() {
		_, err := c.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "hi"}}, nil, nil)
		done <- err
	}()

	select {
	case <-fallbackImpl.started:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the fallback attempt to dispatch")
	}

	if got, active := c.DisplayModelRef(); !active || got != "fallback-prov/fallback-model" {
		t.Fatalf("DisplayModelRef during the in-flight fallback = %q, want fallback-prov/fallback-model", got)
	}
	if got := c.RunningModelRef(); got != "primary-prov/primary-model" {
		t.Fatalf("RunningModelRef during the in-flight fallback = %q, want the confirmed primary-prov/primary-model", got)
	}

	close(fallbackImpl.release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("CompleteStream err = %v, want the fallback reply", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the request to return")
	}

	if got, active := c.DisplayModelRef(); active {
		t.Fatalf("DisplayModelRef after the request returned = %q, want no active request", got)
	}
	if got := c.RunningModelRef(); got != "fallback-prov/fallback-model" {
		t.Fatalf("RunningModelRef after the successful fallback = %q, want fallback-prov/fallback-model", got)
	}
}

// TestAttemptModelRefClearedAfterExhaustedRound pins the other end of the
// lifecycle: a round that fails on every pool entry must not leave its last
// failed target pinned as an in-flight attempt, otherwise an idle display would
// keep naming a model nothing is using.
func TestAttemptModelRefClearedAfterExhaustedRound(t *testing.T) {
	primaryImpl := &scriptedProvider{
		calls: []scriptedCall{{err: &APIError{StatusCode: 500, Message: "primary unavailable"}}},
	}
	fallbackImpl := &scriptedProvider{
		calls: []scriptedCall{{err: &APIError{StatusCode: 500, Message: "fallback unavailable"}}},
	}

	c := NewClient(testProviderConfig("primary-prov", "primary-model"), primaryImpl, "primary-model", 4096, "sys")
	c.SetFallbackModels([]FallbackModel{{
		ProviderConfig: testProviderConfig("fallback-prov", "fallback-model"),
		ProviderImpl:   fallbackImpl,
		ModelID:        "fallback-model",
		MaxTokens:      4096,
		ContextLimit:   64000,
		InputLimit:     64000,
	}})
	c.SetStreamRetryRounds(1)

	if _, err := c.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "hi"}}, nil, nil); err == nil {
		t.Fatal("CompleteStream err = nil, want the exhausted pool error")
	}
	if got, active := c.DisplayModelRef(); active {
		t.Fatalf("DisplayModelRef after an exhausted round = %q, want no active request", got)
	}
}

// A retry wait prepares the start of the next round, not the last failed target.
func TestAttemptModelRefFollowsUpcomingRoundDuringWait(t *testing.T) {
	primary := testProviderConfig("provider-a", "model-a")
	primary.retryDelay = 5 * time.Second
	primary.retryBackoff = config.RetryBackoffFixed
	c := NewClient(primary, &scriptedProvider{calls: []scriptedCall{{err: &APIError{StatusCode: 500, Message: "unavailable"}}}}, "model-a", 4096, "sys")
	c.SetFallbackModels([]FallbackModel{{
		ProviderConfig: testProviderConfig("provider-b", "model-b"),
		ProviderImpl:   &scriptedProvider{calls: []scriptedCall{{err: &APIError{StatusCode: 500, Message: "unavailable"}}}},
		ModelID:        "model-b", MaxTokens: 4096,
	}})
	c.SetStreamRetryRounds(2)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	waitRef := make(chan string, 1)
	done := make(chan error, 1)
	go func() {
		_, err := c.CompleteStream(ctx, nil, nil, func(delta message.StreamDelta) {
			if delta.Status != nil && strings.HasPrefix(delta.Status.Detail, "round 2") {
				ref, _ := c.DisplayModelRef()
				waitRef <- ref
				cancel()
			}
		})
		done <- err
	}()
	select {
	case ref := <-waitRef:
		if ref != "provider-a/model-a" {
			t.Fatalf("round wait target = %q, want provider-a/model-a", ref)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the next round")
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("request error = %v, want cancellation", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for cancellation")
	}
	if _, active := c.DisplayModelRef(); active {
		t.Fatal("returned request retained its attempt")
	}
	if ref, active := c.DisplayModelRef(); active || ref != c.NextRequestModelRef() {
		t.Fatalf("returned display = %q, active=%v, want next request", ref, active)
	}
}

func TestDisplayModelRefUsesAdvancedCursorWithoutVariantBackfill(t *testing.T) {
	provider := NewProviderConfig("sample", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{"model": {
			Limit:    config.ModelLimit{Context: 128000, Output: 4096},
			Variants: map[string]config.ModelVariant{"high": {Thinking: &config.ThinkingConfig{Effort: "high"}}},
		}},
	}, []string{"test-key"})
	impl := &scriptedProvider{calls: []scriptedCall{
		{err: &APIError{StatusCode: 500, Message: "unavailable"}},
		{err: &APIError{StatusCode: 500, Message: "unavailable"}},
	}}
	c := NewClient(provider, impl, "model", 4096, "sys")
	c.SetModelPool([]FallbackModel{
		{ProviderConfig: provider, ProviderImpl: impl, ModelID: "model", Variant: "high", MaxTokens: 4096},
		{ProviderConfig: provider, ProviderImpl: impl, ModelID: "model", MaxTokens: 4096},
	}, 0)
	c.SetStreamRetryRounds(1)
	if _, err := c.CompleteStream(context.Background(), nil, nil, nil); err == nil {
		t.Fatal("expected an exhausted round")
	}
	if ref, active := c.DisplayModelRef(); active || ref != "sample/model" {
		t.Fatalf("next display = %q, active=%v, want sample/model", ref, active)
	}
	if ref := c.NextRequestModelRef(); ref != "sample/model" {
		t.Fatalf("next request = %q, want sample/model", ref)
	}
	if got := c.ProviderForModelRef("other/model"); got != nil {
		t.Fatal("unknown target reused the primary provider")
	}
}

func TestDisplayModelRefIsActiveDuringPreflightAndClearsOnRejection(t *testing.T) {
	c := NewClient(testProviderConfig("sample", "model"), &scriptedProvider{}, "model", 4096, "sys")
	_, err := c.CompleteStreamWithOptions(t.Context(), nil, nil, nil, CompleteStreamOptions{
		NativeTools: &NativeToolPolicy{Preflight: func(context.Context) error {
			if ref, active := c.DisplayModelRef(); !active || ref != "sample/model" {
				t.Fatalf("preflight display = %q, active=%v", ref, active)
			}
			return context.Canceled
		}},
	})
	if err == nil {
		t.Fatal("preflight rejection was lost")
	}
	if ref, active := c.DisplayModelRef(); active || ref != "sample/model" {
		t.Fatalf("rejected preflight display = %q, active=%v", ref, active)
	}
}
