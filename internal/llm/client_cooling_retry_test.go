package llm

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
)

func TestCompleteStreamBeforeRetryRoundRunsBeforeNextRound(t *testing.T) {
	cfg := testProviderConfigWithKeys("sample", "model-1", []string{"k1"})
	impl := &recordingProvider{calls: []scriptedCall{
		{resp: &message.Response{Content: "ok after cooldown"}},
	}}
	c := NewClient(cfg, impl, "model-1", 4096, "sys")
	cfg.MarkCooldown("k1", 20*time.Millisecond)

	initial := []message.Message{{Role: "user", Content: "first"}}
	queued := message.Message{Role: "user", Content: "queued during cooldown"}
	calls := 0
	resp, err := c.CompleteStreamWithOptions(context.Background(), initial, nil, nil, CompleteStreamOptions{
		BeforeRetryRound: func(_ context.Context, messages []message.Message) ([]message.Message, error) {
			calls++
			if len(messages) != 1 || messages[0].Content != "first" {
				t.Errorf("retry boundary messages = %#v, want the original request", messages)
			}
			return append(messages, queued), nil
		},
	})
	if err != nil {
		t.Fatalf("CompleteStreamWithOptions returned error: %v", err)
	}
	if resp == nil || resp.Content != "ok after cooldown" {
		t.Fatalf("unexpected response: %#v", resp)
	}
	if calls != 1 {
		t.Fatalf("retry boundary calls = %d, want 1", calls)
	}
	if len(impl.messages) != 1 {
		t.Fatalf("provider messages recorded = %d, want 1", len(impl.messages))
	}
	got := impl.messages[0]
	if len(got) != 2 || got[0].Content != "first" || got[1].Content != queued.Content {
		t.Fatalf("request did not carry the retry-round input: %#v", got)
	}
}

func TestCompleteStreamBeforeRetryRoundSkipsKeyRotation(t *testing.T) {
	cfg := testProviderConfigWithKeys("sample", "model-1", []string{"k1", "k2"})
	impl := &recordingProvider{calls: []scriptedCall{
		{err: &APIError{StatusCode: 503, Message: "retry another key"}},
		{resp: &message.Response{Content: "ok on the next key"}},
	}}
	c := NewClient(cfg, impl, "model-1", 4096, "sys")

	calls := 0
	resp, err := c.CompleteStreamWithOptions(context.Background(), []message.Message{{Role: "user", Content: "first"}}, nil, nil, CompleteStreamOptions{
		BeforeRetryRound: func(_ context.Context, messages []message.Message) ([]message.Message, error) {
			calls++
			return messages, nil
		},
	})
	if err != nil {
		t.Fatalf("CompleteStreamWithOptions returned error: %v", err)
	}
	if resp == nil || resp.Content != "ok on the next key" {
		t.Fatalf("unexpected response: %#v", resp)
	}
	if calls != 0 {
		t.Fatalf("retry boundary calls = %d, want 0 for key rotation inside one round", calls)
	}
}

func TestClientCompleteStreamCompatible400RetriesAfterAllKeysCooling(t *testing.T) {
	cfg := testCompatibleResponsesProviderConfigWithKeys("gateway", "gpt-test", []string{"k1", "k2"})
	disableRetryDelayForTest(cfg)
	impl := &recordingProvider{}
	impl.calls = []scriptedCall{
		{err: &APIError{StatusCode: 400, Message: "Concurrency limit exceeded for user, please retry later", RetryAfter: time.Millisecond}},
		{err: &APIError{StatusCode: 400, Message: "upstream temporarily busy", RetryAfter: time.Millisecond}},
		{resp: &message.Response{Content: "ok after cooldown"}},
	}
	c := NewClient(cfg, impl, "gpt-test", 4096, "sys")

	resp, err := c.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err != nil {
		t.Fatalf("CompleteStream returned error: %v", err)
	}
	if resp == nil || resp.Content != "ok after cooldown" {
		t.Fatalf("unexpected response: %#v", resp)
	}
	if got := impl.apiKeys; len(got) != 3 || got[0] != "k1" || got[1] != "k2" || got[2] != "k1" {
		t.Fatalf("api keys = %#v, want [k1 k2 k1]", got)
	}
}

func TestClientCompleteStreamConfiguredRetryRoundsHardCapsConcurrent429(t *testing.T) {
	cfg := testProviderConfigWithKeys("primary-prov", "primary-model", []string{"k1"})
	impl := &constantErrProvider{err: &APIError{StatusCode: 429, Message: `{"error":"Too many concurrent requests for this model"}`}}
	c := NewClient(cfg, impl, "primary-model", 4096, "sys")
	c.SetStreamRetryRounds(1)

	ctx, cancel := context.WithTimeout(context.Background(), 2500*time.Millisecond)
	defer cancel()
	resp, err := c.CompleteStream(ctx, []message.Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err == nil {
		t.Fatal("expected configured retry cap to stop concurrent-request 429 retries")
	}
	if resp != nil {
		t.Fatalf("resp = %#v, want nil on capped retry failure", resp)
	}
	if got := impl.calls; got != 1 {
		t.Fatalf("provider calls = %d, want 1 when stream_retry_rounds=1", got)
	}
}

func TestClientCompleteStreamConfiguredRetryRoundsHardCapsAllKeysCooling(t *testing.T) {
	cfg := testProviderConfigWithKeys("primary-prov", "primary-model", []string{"k1"})
	cfg.MarkCooldown("k1", 50*time.Millisecond)
	impl := &recordingProvider{}
	impl.calls = []scriptedCall{
		{resp: &message.Response{Content: "should not be reached"}},
	}
	c := NewClient(cfg, impl, "primary-model", 4096, "sys")
	c.SetStreamRetryRounds(1)

	ctx, cancel := context.WithTimeout(context.Background(), 2500*time.Millisecond)
	defer cancel()
	resp, err := c.CompleteStream(ctx, []message.Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err == nil {
		t.Fatal("expected configured retry cap to stop all-keys-cooling retries")
	}
	if _, ok := errors.AsType[*AllKeysCoolingError](err); !ok {
		t.Fatalf("CompleteStream err = %v, want AllKeysCoolingError", err)
	}
	if resp != nil {
		t.Fatalf("resp = %#v, want nil on capped cooling failure", resp)
	}
	if got := impl.CallCount(); got != 0 {
		t.Fatalf("provider calls = %d, want 0 while key is cooling and stream_retry_rounds=1", got)
	}
}

func TestCompleteStreamWithRetryKeepsRetryingWhileAllKeysCooling(t *testing.T) {
	primaryCfg := NewProviderConfig("primary-prov", config.ProviderConfig{
		Type:         config.ProviderTypeChatCompletions,
		RetryBackoff: config.RetryBackoffNone,
		Models: map[string]config.ModelConfig{
			"test-model": {Limit: config.ModelLimit{Context: 128000, Output: 4096}},
		},
	}, []string{"k1"})
	impl := &recordingProvider{}
	impl.calls = []scriptedCall{
		{resp: &message.Response{Content: "ok after cooldown"}},
	}
	c := NewClient(primaryCfg, impl, "test-model", 4096, "sys")
	primaryCfg.MarkCooldown("k1", 30*time.Millisecond)

	resp, err := callCompleteStreamWithRetryForTest(
		c,
		context.Background(),
		primaryCfg,
		impl,
		"test-model",
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
	if err != nil {
		t.Fatalf("completeStreamWithRetry returned error: %v", err)
	}
	if resp == nil || resp.Content != "ok after cooldown" {
		t.Fatalf("unexpected response: %#v", resp)
	}
	if got := impl.CallCount(); got != 1 {
		t.Fatalf("provider calls = %d, want 1 after cooldown wait", got)
	}
}

func TestCompleteStreamRetryRoundUsesCursorProviderBackoff(t *testing.T) {
	primaryCfg := NewProviderConfig("primary-prov", config.ProviderConfig{
		Type:         config.ProviderTypeChatCompletions,
		RetryBackoff: config.RetryBackoffFixed,
		RetryDelayMS: new(config.MaxProviderRetryDelayMS),
		Models: map[string]config.ModelConfig{
			"primary-model": {Limit: config.ModelLimit{Context: 128000, Output: 4096}},
		},
	}, []string{"k1"})
	fallbackCfg := NewProviderConfig("fallback-prov", config.ProviderConfig{
		Type:         config.ProviderTypeChatCompletions,
		RetryBackoff: config.RetryBackoffNone,
		Models: map[string]config.ModelConfig{
			"fallback-model": {Limit: config.ModelLimit{Context: 128000, Output: 4096}},
		},
	}, []string{"k2"})
	primaryImpl := &recordingProvider{calls: []scriptedCall{
		{err: &APIError{StatusCode: 503, Message: "temporary primary failure"}},
		{resp: &message.Response{Content: "should wait before this call"}},
	}}
	fallbackImpl := &recordingProvider{calls: []scriptedCall{
		{err: &APIError{StatusCode: 503, Message: "temporary fallback failure"}},
	}}
	c := NewClient(primaryCfg, primaryImpl, "primary-model", 4096, "sys")
	c.SetModelPool([]FallbackModel{
		{ProviderConfig: primaryCfg, ProviderImpl: primaryImpl, ModelID: "primary-model", MaxTokens: 4096, ContextLimit: 128000},
		{ProviderConfig: fallbackCfg, ProviderImpl: fallbackImpl, ModelID: "fallback-model", MaxTokens: 4096, ContextLimit: 128000},
	}, 0)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := c.CompleteStream(ctx, []message.Message{{Role: "user", Content: "hi"}}, nil, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("CompleteStream() error = %v, want context deadline during cursor-provider backoff", err)
	}
	if got := primaryImpl.CallCount(); got != 1 {
		t.Fatalf("primary calls = %d, want 1 before round backoff completes", got)
	}
	if got := fallbackImpl.CallCount(); got != 1 {
		t.Fatalf("fallback calls = %d, want 1 within the first round", got)
	}
}

func TestCompleteStreamRetryBackoffFollowsPinnedCursor(t *testing.T) {
	primaryCfg := NewProviderConfig("primary-prov", config.ProviderConfig{
		Type:         config.ProviderTypeChatCompletions,
		RetryBackoff: config.RetryBackoffFixed,
		RetryDelayMS: new(config.MaxProviderRetryDelayMS),
		Models: map[string]config.ModelConfig{
			"primary-model": {Limit: config.ModelLimit{Context: 128000, Output: 4096}},
		},
	}, []string{"k1"})
	fallbackCfg := NewProviderConfig("fallback-prov", config.ProviderConfig{
		Type:         config.ProviderTypeChatCompletions,
		RetryBackoff: config.RetryBackoffNone,
		Models: map[string]config.ModelConfig{
			"fallback-model": {Limit: config.ModelLimit{Context: 128000, Output: 4096}},
		},
	}, []string{"k2"})
	primaryImpl := &recordingProvider{calls: []scriptedCall{
		{err: &APIError{StatusCode: 503, Message: "use fallback"}},
		{err: &APIError{StatusCode: 503, Message: "primary still unavailable"}},
	}}
	fallbackImpl := &recordingProvider{calls: []scriptedCall{
		{resp: &message.Response{Content: "pin fallback"}},
		{err: &APIError{StatusCode: 503, Message: "retry fallback"}},
		{resp: &message.Response{Content: "fallback recovered"}},
	}}
	c := NewClient(primaryCfg, primaryImpl, "primary-model", 4096, "sys")
	c.SetModelPool([]FallbackModel{
		{ProviderConfig: primaryCfg, ProviderImpl: primaryImpl, ModelID: "primary-model", MaxTokens: 4096, ContextLimit: 128000},
		{ProviderConfig: fallbackCfg, ProviderImpl: fallbackImpl, ModelID: "fallback-model", MaxTokens: 4096, ContextLimit: 128000},
	}, 0)

	resp, err := c.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "first"}}, nil, nil)
	if err != nil || resp == nil || resp.Content != "pin fallback" {
		t.Fatalf("first CompleteStream() = %#v, %v", resp, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	resp, err = c.CompleteStream(ctx, []message.Message{{Role: "user", Content: "second"}}, nil, nil)
	if err != nil {
		t.Fatalf("second CompleteStream() error = %v", err)
	}
	if resp == nil || resp.Content != "fallback recovered" {
		t.Fatalf("second response = %#v, want fallback recovery", resp)
	}
}

func TestCompleteStreamWithRetryDoesNotCountPureCoolingRoundAsSoftRetry(t *testing.T) {
	primaryCfg := testProviderConfigWithKeys("primary-prov", "gpt-test", []string{"k1"})
	primaryCfg.MarkCooldown("k1", 20*time.Millisecond)
	impl := &recordingProvider{}
	impl.calls = []scriptedCall{
		{resp: &message.Response{Content: "ok after cooldown"}},
	}
	c := NewClient(primaryCfg, impl, "gpt-test", 4096, "sys")

	var retryingStatuses []string
	resp, err := callCompleteStreamWithRetryForTest(
		c,
		context.Background(),
		primaryCfg,
		impl,
		"gpt-test",
		4096,
		RequestTuning{},
		"",
		[]message.Message{{Role: "user", Content: "hi"}},
		nil,
		func(delta message.StreamDelta) {
			if delta.Type == message.StreamDeltaStatus && delta.Status != nil && delta.Status.Type == "retrying" {
				retryingStatuses = append(retryingStatuses, delta.Status.Detail)
			}
		},
		false,
		nil,
		1,
		&CallStatus{},
	)
	if err != nil {
		t.Fatalf("completeStreamWithRetry returned error: %v", err)
	}
	if resp == nil || resp.Content != "ok after cooldown" {
		t.Fatalf("unexpected response: %#v", resp)
	}
	if got := impl.CallCount(); got != 1 {
		t.Fatalf("provider calls = %d, want 1 after cooldown wait", got)
	}
	if len(retryingStatuses) != 0 {
		t.Fatalf("pure cooling round emitted retrying statuses: %#v", retryingStatuses)
	}
}

func TestCompleteStreamWithRetryPrefersShortestRoundWaitAcrossModels(t *testing.T) {
	primaryCfg := testProviderConfigWithKeys("primary-prov", "gpt-primary", []string{"k1"})
	fallbackCfg := testProviderConfigWithKeys("fallback-prov", "gpt-fallback", []string{"k2"})
	primaryCfg.MarkCooldown("k1", time.Minute)
	fallbackCfg.MarkCooldown("k2", 50*time.Millisecond)
	implPrimary := &recordingProvider{}
	implFallback := &recordingProvider{}
	implFallback.calls = []scriptedCall{{resp: &message.Response{Content: "ok after short wait"}}}
	c := NewClient(primaryCfg, implPrimary, "gpt-primary", 4096, "sys")
	c.SetFallbackModels([]FallbackModel{{
		ProviderConfig: fallbackCfg,
		ProviderImpl:   implFallback,
		ModelID:        "gpt-fallback",
		MaxTokens:      4096,
		ContextLimit:   128000,
	}})

	start := time.Now()
	resp, err := callCompleteStreamWithRetryForTest(
		c,
		context.Background(),
		primaryCfg,
		implPrimary,
		"gpt-primary",
		4096,
		RequestTuning{},
		"",
		[]message.Message{{Role: "user", Content: "hi"}},
		nil,
		nil,
		true,
		c.fallbackModels,
		2,
		&CallStatus{},
	)
	if err != nil {
		t.Fatalf("completeStreamWithRetry returned error: %v", err)
	}
	if resp == nil || resp.Content != "ok after short wait" {
		t.Fatalf("unexpected response: %#v", resp)
	}
	elapsed := time.Since(start)
	if elapsed > 5*time.Second {
		t.Fatalf("elapsed = %v, want to avoid waiting for the 1m primary cooldown", elapsed)
	}
}

func TestMergeRoundWaitPrefersShortestCoolingWindow(t *testing.T) {
	if got := roundRetryDelay(0, 30*time.Millisecond); got != 30*time.Millisecond {
		t.Fatalf("roundRetryDelay(0, 30ms) = %v, want mandatory cooling wait", got)
	}
	got := mergeCoolingWindow(roundCoolingWait{}, time.Minute, maxCoolingWait)
	got = mergeCoolingWindow(got, time.Second, maxCoolingWait)
	if got.sleep != time.Second || got.recovery != time.Second {
		t.Fatalf("mergeCoolingWindow(1m, 1s) = %+v, want 1s for both", got)
	}
	got = mergeCoolingWindow(roundCoolingWait{}, 0, maxCoolingWait)
	if got.sleep != time.Second || got.recovery != time.Second {
		t.Fatalf("mergeCoolingWindow(0) = %+v, want the 1s floor for both", got)
	}
	// A capped pool sleeps for the re-probe interval but still reports the real
	// recovery instant, so the countdown never promises an earlier request.
	got = mergeCoolingWindow(roundCoolingWait{}, 2*time.Minute, maxCoolingWait)
	if got.sleep != maxCoolingWait {
		t.Fatalf("mergeCoolingWindow(2m).sleep = %v, want the %v re-probe cap", got.sleep, maxCoolingWait)
	}
	if got.recovery != 2*time.Minute {
		t.Fatalf("mergeCoolingWindow(2m).recovery = %v, want the real 2m window", got.recovery)
	}
	// An uncapped pool (single target) sleeps until the real recovery instant.
	got = mergeCoolingWindow(roundCoolingWait{}, 3*time.Hour, 0)
	if got.sleep != 3*time.Hour || got.recovery != 3*time.Hour {
		t.Fatalf("mergeCoolingWindow(3h, uncapped) = %+v, want 3h for both", got)
	}
	// The shortest sleep and the shortest recovery can come from different
	// targets, and each must keep its own minimum.
	got = mergeCoolingWindow(roundCoolingWait{}, 30*time.Minute, maxCoolingWait)
	got = mergeCoolingWindow(got, 5*time.Minute, maxCoolingWait)
	if got.sleep != maxCoolingWait {
		t.Fatalf("merged sleep = %v, want the %v re-probe cap", got.sleep, maxCoolingWait)
	}
	if got.recovery != 5*time.Minute {
		t.Fatalf("merged recovery = %v, want the shortest real window", got.recovery)
	}

	if got := mergePendingCoolingWait(roundCoolingWait{}, roundCoolingWait{}); got.waiting() {
		t.Fatalf("mergePendingCoolingWait(zero, zero) = %+v, want no synthetic wait", got)
	}
	current := roundCoolingWait{sleep: 2 * time.Second, recovery: 2 * time.Second}
	if got := mergePendingCoolingWait(current, roundCoolingWait{}); got != current {
		t.Fatalf("mergePendingCoolingWait(2s, zero) = %+v, want %+v preserved", got, current)
	}
	got = mergePendingCoolingWait(
		roundCoolingWait{sleep: maxCoolingWait, recovery: 30 * time.Minute},
		roundCoolingWait{sleep: 5 * time.Second, recovery: 5 * time.Second},
	)
	if got.sleep != 5*time.Second || got.recovery != 5*time.Second {
		t.Fatalf("mergePendingCoolingWait picked %+v, want the shorter target's window", got)
	}
}

func TestCoolingWaitCapDependsOnPoolShape(t *testing.T) {
	if got := coolingWaitCap(false, nil); got != 0 {
		t.Fatalf("coolingWaitCap(false, nil) = %v, want 0 (uncapped single target)", got)
	}
	if got := coolingWaitCap(true, nil); got != 0 {
		t.Fatalf("coolingWaitCap(true, nil) = %v, want 0 (uncapped single target)", got)
	}
	if got := coolingWaitCap(true, []FallbackModel{{ModelID: "model-1"}}); got != maxCoolingWait {
		t.Fatalf("coolingWaitCap(true, 1 fallback) = %v, want %v", got, maxCoolingWait)
	}
}

func TestCompleteStreamCoolingStatusUsesMergedRoundWait(t *testing.T) {
	primaryCfg := testProviderConfigWithKeys("primary-prov", "gpt-primary", []string{"k1"})
	fallbackCfg := testProviderConfigWithKeys("fallback-prov", "gpt-fallback", []string{"k2"})
	primaryCfg.MarkCooldown("k1", 700*time.Millisecond)
	fallbackCfg.MarkCooldown("k2", time.Minute)
	implPrimary := &recordingProvider{}
	implPrimary.calls = []scriptedCall{{resp: &message.Response{Content: "ok after short cooling"}}}
	implFallback := &recordingProvider{}
	c := NewClient(primaryCfg, implPrimary, "gpt-primary", 4096, "sys")
	c.SetFallbackModels([]FallbackModel{{
		ProviderConfig: fallbackCfg,
		ProviderImpl:   implFallback,
		ModelID:        "gpt-fallback",
		MaxTokens:      4096,
		ContextLimit:   128000,
	}})

	start := time.Now()
	var coolingDetails []string
	var coolingDeadlines []time.Time
	resp, err := c.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "hi"}}, nil, func(delta message.StreamDelta) {
		if delta.Type == "status" && delta.Status != nil && delta.Status.Type == "cooling" {
			coolingDetails = append(coolingDetails, delta.Status.Detail)
			coolingDeadlines = append(coolingDeadlines, delta.Status.Deadline)
		}
	})
	if err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}
	if resp == nil || resp.Content != "ok after short cooling" {
		t.Fatalf("unexpected response: %#v", resp)
	}
	if len(coolingDetails) == 0 {
		t.Fatal("expected at least one cooling status")
	}
	if got := coolingDetails[len(coolingDetails)-1]; got != "1s" {
		t.Fatalf("last cooling detail = %q, want 1s", got)
	}
	// The status bar counts down from this deadline, so it must be a real
	// future instant derived from the merged wait, not a zero value.
	lastDeadline := coolingDeadlines[len(coolingDeadlines)-1]
	if !lastDeadline.After(start) || lastDeadline.Sub(start) > time.Minute {
		t.Fatalf("last cooling deadline = %v (%v after start), want within the merged wait", lastDeadline, lastDeadline.Sub(start))
	}
}

func TestCompleteStreamSingleTargetCoolingWaitsForRealRecovery(t *testing.T) {
	const cooldown = 30 * time.Minute
	cfg := testProviderConfigWithKeys("only-prov", "only-model", []string{"k1"})
	// MarkCooldown clamps to maxProviderRetryDelay, so a window this long only
	// comes from a confirmed quota reset instant: it carries the provider's own
	// reset time and is not bounded by retry_after_max_s, which caps only a
	// Retry-After hint.
	start := time.Now()
	cfg.MarkQuotaExhaustedUntil("k1", start.Add(cooldown))
	impl := &recordingProvider{}
	c := NewClient(cfg, impl, "only-model", 4096, "sys")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var coolingDeadlines []time.Time
	var coolingDetails []string
	// The first cooling status is emitted before the round sleeps, so cancelling
	// from the callback observes the intended wait without waiting it out.
	_, err := c.CompleteStream(ctx, []message.Message{{Role: "user", Content: "hi"}}, nil, func(delta message.StreamDelta) {
		if delta.Type == message.StreamDeltaStatus && delta.Status != nil && delta.Status.Type == message.StatusDeltaCooling {
			coolingDeadlines = append(coolingDeadlines, delta.Status.Deadline)
			coolingDetails = append(coolingDetails, delta.Status.Detail)
			cancel()
		}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("CompleteStream err = %v, want context.Canceled after observing the cooling status", err)
	}
	if got := impl.CallCount(); got != 0 {
		t.Fatalf("provider calls = %d, want 0 while the only key is cooling", got)
	}
	if len(coolingDeadlines) == 0 {
		t.Fatal("expected a cooling status for the single-target pool")
	}
	deadline := coolingDeadlines[0]
	elapsed := deadline.Sub(start)
	if elapsed <= maxCoolingWait {
		t.Fatalf("cooling deadline = %v after start, want the real %v recovery instant instead of the %v re-probe cap", elapsed, cooldown, maxCoolingWait)
	}
	// The deadline is stamped a moment after the remaining window is measured,
	// so it may land just past the reset instant.
	if elapsed > cooldown+time.Second {
		t.Fatalf("cooling deadline = %v after start, want at most the %v cooldown", elapsed, cooldown)
	}
	if got := coolingDetails[0]; got != cooldown.String() {
		t.Fatalf("cooling detail = %q, want %v", got, cooldown)
	}
}

func TestCompleteStreamCoolingWaitReportsCauseFromBackgroundCaller(t *testing.T) {
	cfg := testProviderConfigWithKeys("only-prov", "only-model", []string{"k1"})
	err401 := error(&APIError{StatusCode: 401, Message: "unauthorized"})
	// A background caller (memory extraction, compaction or thinking translation)
	// has no stream callback; the failure must still be recorded on the shared
	// key state so the next request can explain the cooling wait it runs into.
	if res := markKeyCooldown(context.Background(), cfg, "k1", "only-model", err401); !res.cooldownApplied {
		t.Fatal("markKeyCooldown(401) cooldownApplied = false, want the key to enter cooldown")
	}
	impl := &recordingProvider{}
	c := NewClient(cfg, impl, "only-model", 4096, "sys")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var retryDeltas []message.StreamDelta
	var coolingStatuses int
	_, err := c.CompleteStream(ctx, []message.Message{{Role: "user", Content: "hi"}}, nil, func(delta message.StreamDelta) {
		switch {
		case delta.Type == message.StreamDeltaRetryError:
			retryDeltas = append(retryDeltas, delta)
		case delta.Type == message.StreamDeltaStatus && delta.Status != nil && delta.Status.Type == message.StatusDeltaCooling:
			// Cancel from the first cooling status: the cause is reported right
			// after it, before the round actually sleeps.
			coolingStatuses++
			cancel()
		}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("CompleteStream err = %v, want context.Canceled after observing the cooling status", err)
	}
	if got := impl.CallCount(); got != 0 {
		t.Fatalf("provider calls = %d, want 0 while the only key is cooling", got)
	}
	if coolingStatuses != 1 {
		t.Fatalf("cooling statuses = %d, want 1", coolingStatuses)
	}
	if len(retryDeltas) != 1 {
		t.Fatalf("retry errors = %d, want exactly 1 for the recorded cause", len(retryDeltas))
	}
	if retryDeltas[0].Err != err401 {
		t.Fatalf("retry error = %v, want the recorded 401", retryDeltas[0].Err)
	}
	if retryDeltas[0].Provider != "only-prov" || retryDeltas[0].Model != "only-model" {
		t.Fatalf("retry error route = (%q, %q), want (only-prov, only-model)", retryDeltas[0].Provider, retryDeltas[0].Model)
	}
}

func TestCompleteStreamCoolingWaitDoesNotRepeatCauseReportedByAttempt(t *testing.T) {
	cfg := testProviderConfigWithKeys("only-prov", "only-model", []string{"k1"})
	cfg.retryDelayBase = -1 // test hook: a round with no backoff re-probes immediately
	impl := &constantErrProvider{err: &APIError{StatusCode: 401, Message: "unauthorized"}}
	c := NewClient(cfg, impl, "only-model", 4096, "sys")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var retryErrs []error
	coolingSeen := false
	_, err := c.CompleteStream(ctx, []message.Message{{Role: "user", Content: "hi"}}, nil, func(delta message.StreamDelta) {
		switch {
		case delta.Type == message.StreamDeltaRetryError:
			retryErrs = append(retryErrs, delta.Err)
		case delta.Type == message.StreamDeltaStatus && delta.Status != nil && delta.Status.Type == message.StatusDeltaCooling:
			coolingSeen = true
			cancel()
		}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("CompleteStream err = %v, want context.Canceled after observing the cooling status", err)
	}
	if got := impl.calls; got != 1 {
		t.Fatalf("provider calls = %d, want 1: the 401 attempt must not be retried while its key cools", got)
	}
	if !coolingSeen {
		t.Fatal("expected a cooling status once the only key was put into cooldown")
	}
	// The attempt's 401 already reached the panel as a retry error; the cooling
	// wait must not record the same cause a second time.
	if len(retryErrs) != 1 {
		t.Fatalf("retry errors = %d (%v), want exactly 1", len(retryErrs), retryErrs)
	}
}

func TestCompleteStreamDrainsBackgroundKeyFailureOnce(t *testing.T) {
	cfg := testProviderConfigWithKeys("drain-prov", "drain-model", []string{"k1", "k2"})
	err401 := error(&APIError{StatusCode: 401, Message: "unauthorized"})
	// A background caller has no stream callback, so its 401 cools k1 without
	// ever reaching the UI. k2 keeps the next request from waiting, which makes
	// the drained report the only trace of that failure.
	if res := markKeyCooldown(context.Background(), cfg, "k1", "drain-model", err401); !res.cooldownApplied {
		t.Fatal("markKeyCooldown(401) cooldownApplied = false, want the key to enter cooldown")
	}
	impl := &recordingProvider{}
	impl.calls = []scriptedCall{{resp: &message.Response{Content: "ok"}}}
	c := NewClient(cfg, impl, "drain-model", 4096, "sys")

	var retryDeltas []message.StreamDelta
	resp, err := c.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "hi"}}, nil, func(delta message.StreamDelta) {
		if delta.Type == message.StreamDeltaRetryError {
			retryDeltas = append(retryDeltas, delta)
		}
	})
	if err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}
	if resp == nil || resp.Content != "ok" {
		t.Fatalf("unexpected response: %#v", resp)
	}
	if len(retryDeltas) != 1 {
		t.Fatalf("retry errors = %d (%v), want exactly 1 drained background failure", len(retryDeltas), retryDeltas)
	}
	if retryDeltas[0].Err != err401 {
		t.Fatalf("retry error = %v, want the recorded 401", retryDeltas[0].Err)
	}
	if retryDeltas[0].Provider != "drain-prov" || retryDeltas[0].Model != "drain-model" {
		t.Fatalf("retry error route = (%q, %q), want (drain-prov, drain-model)", retryDeltas[0].Provider, retryDeltas[0].Model)
	}
	if got := impl.CallCount(); got != 1 {
		t.Fatalf("provider calls = %d, want 1: k2 must serve the request while k1 cools", got)
	}

	// The next request must not replay the same failure.
	impl.calls = append(impl.calls, scriptedCall{resp: &message.Response{Content: "ok again"}})
	var repeated []message.StreamDelta
	if _, err := c.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "hi again"}}, nil, func(delta message.StreamDelta) {
		if delta.Type == message.StreamDeltaRetryError {
			repeated = append(repeated, delta)
		}
	}); err != nil {
		t.Fatalf("second CompleteStream: %v", err)
	}
	if len(repeated) != 0 {
		t.Fatalf("second request retry errors = %d (%v), want none", len(repeated), repeated)
	}
}

func TestCompleteStreamTerminalFailureIsNotReplayedFromPendingQueue(t *testing.T) {
	structural400 := &APIError{StatusCode: 400, Message: "invalid assistant message"}
	drainRetryErrors := func(cfg *ProviderConfig) int {
		n := 0
		cfg.drainPendingKeyReports(func(delta message.StreamDelta) {
			if delta.Type == message.StreamDeltaRetryError {
				n++
			}
		})
		return n
	}
	run := func(cb StreamCallback) *ProviderConfig {
		cfg := testProviderConfigWithKeys("gateway", "model-1", []string{"k1"})
		c := NewClient(cfg, &constantErrProvider{err: structural400}, "model-1", 4096, "sys")
		if _, err := c.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "hi"}}, nil, cb); err == nil {
			t.Fatal("CompleteStream err = nil, want the terminal 400")
		}
		return cfg
	}

	// A background request has no callback, so its failure stays queued for
	// the next request that can show it.
	if got := drainRetryErrors(run(nil)); got != 1 {
		t.Fatalf("background request: drained retry errors = %d, want 1", got)
	}
	// A request with a callback returns the failure as its own error; replaying
	// it later would record the same failure twice.
	if got := drainRetryErrors(run(func(message.StreamDelta) {})); got != 0 {
		t.Fatalf("foreground request: drained retry errors = %d, want 0", got)
	}
}

func TestCompleteStreamRetryRoundEmitsDeadlineForScheduledWait(t *testing.T) {
	cfg := testProviderConfig("provider", "model-1")
	impl := &constantErrProvider{err: &APIError{StatusCode: 503, Message: "no available accounts"}}
	c := NewClient(cfg, impl, "model-1", 4096, "sys")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	start := time.Now()
	var details []string
	var deadlines []time.Time
	// The retrying status is emitted before the round sleeps, so cancelling
	// from the callback observes the deadline without waiting it out.
	_, err := c.CompleteStream(ctx, []message.Message{{Role: "user", Content: "hi"}}, nil, func(delta message.StreamDelta) {
		if delta.Type != message.StreamDeltaStatus || delta.Status == nil || delta.Status.Type != message.StatusDeltaRetrying {
			return
		}
		details = append(details, delta.Status.Detail)
		deadlines = append(deadlines, delta.Status.Deadline)
		cancel()
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("CompleteStream err = %v, want context.Canceled after observing the retry round", err)
	}
	if got := impl.calls; got != 1 {
		t.Fatalf("provider calls = %d, want 1 before the round backed off", got)
	}
	if len(deadlines) != 1 {
		t.Fatalf("retrying statuses = %d, want 1", len(deadlines))
	}
	if got := details[0]; got != "round 2" {
		t.Fatalf("retrying detail = %q, want round 2", got)
	}
	// The default provider backoff is 1s with exponential growth, so the
	// deadline is a real instant one second out, not a far-future placeholder.
	if d := deadlines[0].Sub(start); d < 500*time.Millisecond || d > 2*time.Second {
		t.Fatalf("retrying deadline = %v after start, want the 1s round delay", d)
	}
}

func TestCompleteStreamImmediateRetryRoundCarriesNoDeadline(t *testing.T) {
	cfg := testProviderConfig("provider", "model-1")
	cfg.retryDelayBase = -1 // test hook: a round with no backoff re-probes immediately
	impl := &constantErrProvider{err: &APIError{StatusCode: 503, Message: "no available accounts"}}
	c := NewClient(cfg, impl, "model-1", 4096, "sys")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var statuses []message.StatusDelta
	_, err := c.CompleteStream(ctx, []message.Message{{Role: "user", Content: "hi"}}, nil, func(delta message.StreamDelta) {
		if delta.Type != message.StreamDeltaStatus || delta.Status == nil || delta.Status.Type != message.StatusDeltaRetrying {
			return
		}
		statuses = append(statuses, *delta.Status)
		cancel()
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("CompleteStream err = %v, want context.Canceled", err)
	}
	if len(statuses) != 1 {
		t.Fatalf("retrying statuses = %d, want 1", len(statuses))
	}
	if got := statuses[0].Detail; got != "round 2" {
		t.Fatalf("retrying detail = %q, want round 2", got)
	}
	if !statuses[0].Deadline.IsZero() {
		t.Fatalf("immediate retry round deadline = %v, want zero: it re-probes with nothing to count down", statuses[0].Deadline)
	}
}
