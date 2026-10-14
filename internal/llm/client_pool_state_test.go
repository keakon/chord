package llm

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/ratelimit"
)

func TestClientNoteRunningModelRefUpdatesInFlightRunningRef(t *testing.T) {
	cfg := testProviderConfig("provider", "primary")
	c := NewClient(cfg, &scriptedProvider{}, "primary", 128000, "")

	c.NoteRunningModelRef("fallback-provider/fallback-model")

	if got := c.RunningModelRef(); got != "fallback-provider/fallback-model" {
		t.Fatalf("RunningModelRef() = %q, want fallback-provider/fallback-model", got)
	}
	st := c.LastCallStatus()
	if st.SelectedModelRef != "provider/primary" {
		t.Fatalf("SelectedModelRef = %q, want provider/primary", st.SelectedModelRef)
	}
	if st.RunningModelRef != "fallback-provider/fallback-model" {
		t.Fatalf("RunningModelRef status = %q, want fallback-provider/fallback-model", st.RunningModelRef)
	}
}

func TestClientNoteRunningModelRefRefreshesLimitsWhenRunningRefChanges(t *testing.T) {
	primaryCfg := testProviderConfig("primary", "model-a")
	fallbackACfg := NewProviderConfig("fallback-a", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"model-b": {Limit: config.ModelLimit{Context: 200000, Input: 150000, Output: 32000}},
		},
	}, []string{"key-a"})
	fallbackBCfg := NewProviderConfig("fallback-b", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"model-c": {Limit: config.ModelLimit{Context: 400000, Input: 272000, Output: 128000}},
		},
	}, []string{"key-b"})
	c := NewClient(primaryCfg, &scriptedProvider{}, "model-a", 4096, "")
	c.SetModelPool([]FallbackModel{
		{ProviderConfig: primaryCfg, ProviderImpl: &scriptedProvider{}, ModelID: "model-a", MaxTokens: 4096, ContextLimit: 128000, InputLimit: 128000},
		{ProviderConfig: fallbackACfg, ProviderImpl: &scriptedProvider{}, ModelID: "model-b", MaxTokens: 4096, ContextLimit: 200000, InputLimit: 150000},
		{ProviderConfig: fallbackBCfg, ProviderImpl: &scriptedProvider{}, ModelID: "model-c", MaxTokens: 4096, ContextLimit: 400000, InputLimit: 272000},
	}, 0)

	c.NoteRunningModelRef("fallback-a/model-b")
	st := c.LastCallStatus()
	if st.RunningContextLimit != 200000 || st.RunningInputLimit != 150000 {
		t.Fatalf("first running limits = %d/%d, want 200000/150000", st.RunningContextLimit, st.RunningInputLimit)
	}

	c.NoteRunningModelRef("fallback-b/model-c")
	st = c.LastCallStatus()
	if st.RunningContextLimit != 400000 || st.RunningInputLimit != 272000 {
		t.Fatalf("second running limits = %d/%d, want 400000/272000", st.RunningContextLimit, st.RunningInputLimit)
	}
}

func TestClient_RetriableErrorTriesOtherKeysBeforeFallbackModel(t *testing.T) {
	primaryCfg := testProviderConfigWithKeys("codex", "gpt-5.5", []string{"key-a", "key-b"})
	fallbackCfg := testProviderConfig("sample", "glm-5.1")

	primaryImpl := &recordingProvider{}
	primaryImpl.calls = []scriptedCall{
		{err: &APIError{StatusCode: 503, Message: "upstream unavailable"}},
		{resp: &message.Response{Content: "ok from second key"}},
	}
	fallbackImpl := &recordingProvider{}
	fallbackImpl.calls = []scriptedCall{{resp: &message.Response{Content: "fallback should not run"}}}

	client := NewClient(primaryCfg, primaryImpl, "gpt-5.5", 512, "")
	client.SetModelPool([]FallbackModel{{
		ProviderConfig: primaryCfg,
		ProviderImpl:   primaryImpl,
		ModelID:        "gpt-5.5",
		MaxTokens:      512,
		ContextLimit:   128000,
	}, {
		ProviderConfig: fallbackCfg,
		ProviderImpl:   fallbackImpl,
		ModelID:        "glm-5.1",
		MaxTokens:      512,
		ContextLimit:   128000,
	}}, 0)

	resp, err := client.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "hello"}}, nil, func(message.StreamDelta) {})
	if err != nil {
		t.Fatalf("CompleteStream returned error: %v", err)
	}
	if resp == nil || resp.Content != "ok from second key" {
		t.Fatalf("unexpected response: %#v", resp)
	}

	if got := len(primaryImpl.apiKeys); got != 2 {
		t.Fatalf("initial pool entry call count = %d, want 2", got)
	}
	if primaryImpl.apiKeys[0] != "key-a" || primaryImpl.apiKeys[1] != "key-b" {
		t.Fatalf("initial pool-entry apiKeys = %#v, want [key-a key-b]", primaryImpl.apiKeys)
	}
	if got := len(fallbackImpl.apiKeys); got != 0 {
		t.Fatalf("later pool entry should not be called before second key succeeds, got %d calls", got)
	}
}

// TestClientBeforeFallbackReceivesResolvedFallbackTarget verifies that the
// BeforeFallback hook receives the resolved fallback entry — provider/model
// plus the derived context and input budgets the client would dispatch with —
// so the agent boundary can detect a move to a smaller window before the
// fallback request is sent.
func TestClientBeforeFallbackReceivesResolvedFallbackTarget(t *testing.T) {
	primaryCfg := testProviderConfigWithKeys("strict", "strict-model", []string{"key-a"})
	fallbackCfg := testProviderConfig("fallback-prov", "fallback-model")

	primaryImpl := &recordingProvider{}
	primaryImpl.calls = []scriptedCall{
		{err: &APIError{StatusCode: 503, Message: "upstream unavailable"}},
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
		InputLimit:     96000,
	}, {
		ProviderConfig: fallbackCfg,
		ProviderImpl:   fallbackImpl,
		ModelID:        "fallback-model",
		MaxTokens:      512,
		ContextLimit:   128000,
		InputLimit:     32000,
	}}, 0)

	var sawFallback []FallbackModel
	resp, err := client.CompleteStreamWithOptions(
		context.Background(),
		[]message.Message{{Role: "user", Content: "hello"}},
		nil,
		nil,
		CompleteStreamOptions{
			BeforeFallback: func(_ context.Context, _ []message.Message, target FallbackModel) ([]message.Message, error) {
				sawFallback = append(sawFallback, target)
				return nil, nil
			},
		},
	)
	if err != nil {
		t.Fatalf("CompleteStreamWithOptions returned error: %v", err)
	}
	if resp == nil || resp.Content != "ok from fallback" {
		t.Fatalf("unexpected response: %#v", resp)
	}
	if len(sawFallback) != 1 {
		t.Fatalf("BeforeFallback calls = %d, want 1", len(sawFallback))
	}
	target := sawFallback[0]
	if target.ProviderConfig == nil || target.ProviderConfig.Name() != "fallback-prov" || target.ModelID != "fallback-model" {
		t.Fatalf("BeforeFallback target = %#v, want fallback-prov/fallback-model", target)
	}
	if target.ContextLimit != 128000 {
		t.Fatalf("BeforeFallback ContextLimit = %d, want 128000", target.ContextLimit)
	}
	if target.InputLimit != 32000 {
		t.Fatalf("BeforeFallback InputLimit = %d, want 32000", target.InputLimit)
	}
	if got := len(fallbackImpl.apiKeys); got != 1 {
		t.Fatalf("fallback call count = %d, want 1 (callback must not suppress the fallback)", got)
	}
}

func TestClient_402QuotaErrorTriesOtherKeysBeforeFallbackModel(t *testing.T) {
	primaryCfg := testProviderConfigWithKeys("sample", "gpt-5.4", []string{"key-a", "key-b", "key-c"})
	fallbackCfg := testProviderConfig("fallback-prov", "fallback-model")

	primaryImpl := &recordingProvider{}
	primaryImpl.calls = []scriptedCall{
		{err: &APIError{StatusCode: 402, Message: "quota exhausted; retry later"}},
		{resp: &message.Response{Content: "ok from second key"}},
	}
	fallbackImpl := &recordingProvider{}
	fallbackImpl.calls = []scriptedCall{{resp: &message.Response{Content: "fallback should not run"}}}

	c := &Client{}
	resp, err := callCompleteStreamWithRetryForTest(
		c,
		context.Background(),
		primaryCfg,
		primaryImpl,
		"gpt-5.4",
		4096,
		RequestTuning{},
		"",
		[]message.Message{{Role: "user", Content: "hi"}},
		nil,
		nil,
		true,
		[]FallbackModel{{ProviderConfig: fallbackCfg, ProviderImpl: fallbackImpl, ModelID: "glm-5.1", MaxTokens: 4096, ContextLimit: 128000}},
		1,
		&CallStatus{},
	)
	if err != nil {
		t.Fatalf("completeStreamWithRetry returned error: %v", err)
	}
	if resp == nil || resp.Content != "ok from second key" {
		t.Fatalf("response = %#v, want second key success", resp)
	}
	if got := len(primaryImpl.apiKeys); got != 2 {
		t.Fatalf("primary call count = %d, want 2", got)
	}
	if primaryImpl.apiKeys[0] != "key-a" || primaryImpl.apiKeys[1] != "key-b" {
		t.Fatalf("primary apiKeys = %#v, want [key-a key-b]", primaryImpl.apiKeys)
	}
	if got := len(fallbackImpl.apiKeys); got != 0 {
		t.Fatalf("fallback should not run before second key succeeds, got %d calls", got)
	}
}

func TestClient_ModelPoolNoUsableKeysDoesNotStopRetryRounds(t *testing.T) {
	primaryCfg := testProviderConfigWithKeys("linux", "gpt-5.5", []string{"key-a"})
	fallbackCfg := testProviderConfig("codex2", "gpt-5.5")
	disabledCfg := testProviderConfigWithKeys("codex", "gpt-5.5", []string{"disabled-key"})

	primaryImpl := &recordingProvider{}
	primaryImpl.calls = []scriptedCall{
		{err: &APIError{StatusCode: 503, Message: "temporary primary failure"}},
		{resp: &message.Response{Content: "ok after retry round"}},
	}
	fallbackImpl := &recordingProvider{}
	fallbackImpl.calls = []scriptedCall{{err: &APIError{StatusCode: 503, Message: "temporary upstream failure"}}}
	disabledImpl := &recordingProvider{}

	disabledCfg.MarkInvalidated("disabled-key")
	primaryCfg.retryDelayBase = -1
	var statuses []message.StatusDelta

	resp, err := callCompleteStreamWithRetryForTest(
		&Client{},
		context.Background(),
		primaryCfg,
		primaryImpl,
		"gpt-5.5",
		4096,
		RequestTuning{},
		"",
		[]message.Message{{Role: "user", Content: "hi"}},
		nil,
		func(delta message.StreamDelta) {
			if delta.Type == message.StreamDeltaStatus && delta.Status != nil {
				statuses = append(statuses, *delta.Status)
			}
		},
		true,
		[]FallbackModel{{
			ProviderConfig: fallbackCfg,
			ProviderImpl:   fallbackImpl,
			ModelID:        "gpt-5.5",
			MaxTokens:      4096,
			ContextLimit:   128000,
		}, {
			ProviderConfig: disabledCfg,
			ProviderImpl:   disabledImpl,
			ModelID:        "gpt-5.5",
			MaxTokens:      4096,
			ContextLimit:   128000,
		}},
		2,
		&CallStatus{},
	)
	if err != nil {
		t.Fatalf("completeStreamWithRetry returned error: %v", err)
	}
	if resp == nil || resp.Content != "ok after retry round" {
		t.Fatalf("response = %#v, want second-round primary success", resp)
	}
	if got := len(primaryImpl.apiKeys); got != 2 {
		t.Fatalf("primary calls = %d, want 2", got)
	}
	if got := len(fallbackImpl.apiKeys); got != 1 {
		t.Fatalf("fallback calls = %d, want 1", got)
	}
	if got := len(disabledImpl.apiKeys); got != 0 {
		t.Fatalf("disabled provider should not be called, got %d calls", got)
	}
	foundRetryStartRef := false
	for _, st := range statuses {
		if st.Type == "retrying" && st.Detail == "round 2" && st.ModelRef == "linux/gpt-5.5" {
			foundRetryStartRef = true
			break
		}
	}
	if !foundRetryStartRef {
		t.Fatalf("missing round retry status with start model ref; statuses=%#v", statuses)
	}
}

func TestClient_ModelPoolAllNoUsableKeysStopsWithoutEmptyRetryLoop(t *testing.T) {
	primaryCfg := testProviderConfigWithKeys("primary", "gpt-5.5", []string{"disabled-primary"})
	fallbackCfg := testProviderConfigWithKeys("fallback", "gpt-5.5", []string{"disabled-fallback"})
	primaryCfg.MarkInvalidated("disabled-primary")
	fallbackCfg.MarkInvalidated("disabled-fallback")
	primaryCfg.retryDelayBase = -1

	primaryImpl := &recordingProvider{}
	fallbackImpl := &recordingProvider{}

	resp, err := callCompleteStreamWithRetryForTest(
		&Client{},
		context.Background(),
		primaryCfg,
		primaryImpl,
		"gpt-5.5",
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
			ModelID:        "gpt-5.5",
			MaxTokens:      4096,
			ContextLimit:   128000,
		}},
		-3,
		&CallStatus{},
	)
	if err == nil {
		t.Fatalf("completeStreamWithRetry returned nil error with response %#v, want NoUsableKeysError", resp)
	}
	if _, ok := errors.AsType[*NoUsableKeysError](err); !ok {
		t.Fatalf("error = %T %[1]v, want NoUsableKeysError", err)
	}
	if got := len(primaryImpl.apiKeys); got != 0 {
		t.Fatalf("primary provider should not be called, got %d calls", got)
	}
	if got := len(fallbackImpl.apiKeys); got != 0 {
		t.Fatalf("fallback provider should not be called, got %d calls", got)
	}
}

func TestClientFallbackChainUsedOnce(t *testing.T) {
	primaryCfg := testProviderConfig("primary-prov", "primary-model")
	fallbackCfg := testProviderConfig("fallback-prov", "fallback-model")

	primaryImpl := &scriptedProvider{
		calls: []scriptedCall{
			{err: &APIError{StatusCode: 413, Message: "context too long"}},
		},
	}
	fallbackImpl := &scriptedProvider{
		calls: []scriptedCall{
			{resp: &message.Response{Content: "ok from fallback"}},
		},
	}

	c := NewClient(primaryCfg, primaryImpl, "primary-model", 4096, "sys")
	c.SetFallbackModels([]FallbackModel{
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
		t.Fatalf("expected success with fallback, got error: %v", err)
	}
	if resp == nil || resp.Content != "ok from fallback" {
		t.Fatalf("unexpected response: %#v", resp)
	}

	if got := primaryImpl.CallCount(); got != 1 {
		t.Fatalf("expected initial pool entry called once, got %d", got)
	}
	if got := fallbackImpl.CallCount(); got != 1 {
		t.Fatalf("expected fallback called once, got %d", got)
	}

	st := c.LastCallStatus()
	if !st.FallbackTriggered {
		t.Fatal("expected FallbackTriggered=true")
	}
	if st.RunningModelRef != "fallback-prov/fallback-model" {
		t.Fatalf("unexpected running model ref: %q", st.RunningModelRef)
	}
	if st.RunningContextLimit != 128000 {
		t.Fatalf("unexpected running context limit: %d", st.RunningContextLimit)
	}
	if st.RunningInputLimit != 123904 {
		t.Fatalf("unexpected running input limit: %d", st.RunningInputLimit)
	}
}

func TestClientKeyStatsFollowRunningFallbackProvider(t *testing.T) {
	primaryCfg := testProviderConfigWithKeys("primary-prov", "primary-model", []string{"p1", "p2", "p3", "p4", "p5", "p6", "p7", "p8", "p9", "p10"})
	fallbackCfg := testProviderConfigWithKeys("fallback-prov", "fallback-model", []string{"fb1"})

	primaryImpl := &scriptedProvider{
		calls: []scriptedCall{{err: &APIError{StatusCode: 413, Message: "context too long"}}},
	}
	fallbackImpl := &scriptedProvider{
		calls: []scriptedCall{{resp: &message.Response{Content: "ok from fallback"}}},
	}

	c := NewClient(primaryCfg, primaryImpl, "primary-model", 4096, "sys")
	c.SetFallbackModels([]FallbackModel{{
		ProviderConfig: fallbackCfg,
		ProviderImpl:   fallbackImpl,
		ModelID:        "fallback-model",
		MaxTokens:      4096,
		ContextLimit:   128000,
	}})

	if available, total := c.KeyStats(); available != 10 || total != 10 {
		t.Fatalf("initial KeyStats = %d/%d, want 10/10", available, total)
	}

	if _, err := c.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "hi"}}, nil, nil); err != nil {
		t.Fatalf("CompleteStream() error = %v", err)
	}

	if available, total := c.KeyStats(); available != 1 || total != 1 {
		t.Fatalf("fallback KeyStats = %d/%d, want 1/1", available, total)
	}
}

func TestKeyStatsForRef_matchesPrimaryWhenLastCallUsedFallback(t *testing.T) {
	primaryCfg := testProviderConfigWithKeys("primary-prov", "primary-model", []string{"p1", "p2"})
	fallbackCfg := testProviderConfigWithKeys("fallback-prov", "fallback-model", []string{"fb1"})

	primaryImpl := &scriptedProvider{
		calls: []scriptedCall{{err: &APIError{StatusCode: 413, Message: "context too long"}}},
	}
	fallbackImpl := &scriptedProvider{
		calls: []scriptedCall{{resp: &message.Response{Content: "ok"}}},
	}

	c := NewClient(primaryCfg, primaryImpl, "primary-model", 4096, "sys")
	c.SetFallbackModels([]FallbackModel{{
		ProviderConfig: fallbackCfg,
		ProviderImpl:   fallbackImpl,
		ModelID:        "fallback-model",
		MaxTokens:      4096,
		ContextLimit:   128000,
	}})

	if _, err := c.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "hi"}}, nil, nil); err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}
	gotAvail, gotTot := c.KeyStats()
	if gotAvail != 1 || gotTot != 1 {
		t.Fatalf("KeyStats (lastCall=fallback) = %d/%d, want 1/1", gotAvail, gotTot)
	}
	// Sidebar MODEL may still show selected primary while MainAgent uses this ref:
	pAvail, pTot := c.KeyStatsForRef("primary-prov/primary-model")
	if pAvail != 2 || pTot != 2 {
		t.Fatalf("KeyStatsForRef(primary) = %d/%d, want 2/2", pAvail, pTot)
	}
}

func TestKeyStatsForRefIgnoresInlineVariantSuffix(t *testing.T) {
	primaryCfg := testProviderConfigWithKeys("codex", "gpt-5.5-mini", []string{"k1", "k2", "k3"})
	fallbackCfg := testProviderConfigWithKeys("fallback-prov", "fallback-model", []string{"fb1"})

	primaryImpl := &scriptedProvider{}
	fallbackImpl := &scriptedProvider{}

	c := NewClient(primaryCfg, primaryImpl, "gpt-5.5-mini", 4096, "sys")
	c.SetFallbackModels([]FallbackModel{{
		ProviderConfig: fallbackCfg,
		ProviderImpl:   fallbackImpl,
		ModelID:        "fallback-model",
		MaxTokens:      4096,
		ContextLimit:   128000,
	}})

	avail, total := c.KeyStatsForRef("codex/gpt-5.5-mini@high")
	if avail != 3 || total != 3 {
		t.Fatalf("KeyStatsForRef(codex/gpt-5.5-mini@high) = %d/%d, want 3/3", avail, total)
	}
}

func TestCurrentRateLimitSnapshotForRefIgnoresInlineVariantSuffix(t *testing.T) {
	primaryCfg := testProviderConfigWithKeys("codex", "gpt-5.5-mini", []string{"k1", "k2"})
	fallbackCfg := testProviderConfigWithKeys("fallback-prov", "fallback-model", []string{"fb1"})

	primaryImpl := &scriptedProvider{}
	fallbackImpl := &scriptedProvider{}

	c := NewClient(primaryCfg, primaryImpl, "gpt-5.5-mini", 4096, "sys")
	c.SetFallbackModels([]FallbackModel{{
		ProviderConfig: fallbackCfg,
		ProviderImpl:   fallbackImpl,
		ModelID:        "fallback-model",
		MaxTokens:      4096,
		ContextLimit:   128000,
	}})

	snap := &ratelimit.KeyRateLimitSnapshot{
		Provider:   "codex",
		CapturedAt: time.Now(),
		Primary:    &ratelimit.RateLimitWindow{UsedPct: 42},
	}
	primaryCfg.UpdateKeySnapshot("k1", snap)
	if _, _, err := primaryCfg.SelectKeyWithContext(context.Background()); err != nil {
		t.Fatalf("SelectKeyWithContext() error = %v", err)
	}

	got := c.CurrentRateLimitSnapshotForRef("codex/gpt-5.5-mini@high")
	if got != snap {
		t.Fatalf("CurrentRateLimitSnapshotForRef(codex/gpt-5.5-mini@high) = %#v, want %#v", got, snap)
	}
}

func TestSetModelPoolWrapAround(t *testing.T) {
	cfg0 := testProviderConfig("prov", "model0")
	cfg1 := testProviderConfig("prov", "model1")
	cfg2 := testProviderConfig("prov", "model2")
	cfg3 := testProviderConfig("prov", "model3")

	// model0 and model2 fail; model3 succeeds.
	impl0 := &scriptedProvider{calls: []scriptedCall{{err: &APIError{StatusCode: 500, Message: "fail"}}}}
	impl1 := &scriptedProvider{calls: []scriptedCall{{err: &APIError{StatusCode: 500, Message: "fail"}}}}
	impl2 := &scriptedProvider{calls: []scriptedCall{{resp: &message.Response{Content: "from model2"}}}}
	impl3 := &scriptedProvider{calls: []scriptedCall{{resp: &message.Response{Content: "from model3"}}}}

	pool := []FallbackModel{
		{ProviderConfig: cfg0, ProviderImpl: impl0, ModelID: "model0", MaxTokens: 4096, ContextLimit: 128000},
		{ProviderConfig: cfg1, ProviderImpl: impl1, ModelID: "model1", MaxTokens: 4096, ContextLimit: 128000},
		{ProviderConfig: cfg2, ProviderImpl: impl2, ModelID: "model2", MaxTokens: 4096, ContextLimit: 128000},
		{ProviderConfig: cfg3, ProviderImpl: impl3, ModelID: "model3", MaxTokens: 4096, ContextLimit: 128000},
	}

	// Select model1 (index 1): retry order should be model1 -> model2 -> model3 -> model0.
	// model1 fails, model2 succeeds.
	c := NewClient(cfg1, impl1, "model1", 4096, "sys")
	c.SetModelPool(pool, 1)

	resp, err := c.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err != nil {
		t.Fatalf("expected success, got error: %v", err)
	}
	if resp == nil || resp.Content != "from model2" {
		t.Fatalf("expected 'from model2', got: %#v", resp)
	}
	st := c.LastCallStatus()
	if st.RunningModelRef != "prov/model2" {
		t.Fatalf("expected running=prov/model2, got %q", st.RunningModelRef)
	}
	if st.RunningContextLimit != 128000 {
		t.Fatalf("expected running context limit=128000, got %d", st.RunningContextLimit)
	}
	if !st.FallbackTriggered {
		t.Fatal("expected model-pool advance to set FallbackTriggered=true")
	}
}

func TestModelPoolStickyCursorKeepsSuccessfulModel(t *testing.T) {
	cfg0 := testProviderConfig("prov", "model0")
	cfg1 := testProviderConfig("prov", "model1")
	cfg2 := testProviderConfig("prov", "model2")

	impl0 := &scriptedProvider{calls: []scriptedCall{
		{err: &APIError{StatusCode: 500, Message: "m0-fail-1"}},
		{err: &APIError{StatusCode: 500, Message: "m0-fail-2"}},
	}}
	impl1 := &scriptedProvider{calls: []scriptedCall{
		{resp: &message.Response{Content: "from model1 call1"}},
		{resp: &message.Response{Content: "from model1 call2"}},
	}}
	impl2 := &scriptedProvider{calls: []scriptedCall{
		{resp: &message.Response{Content: "from model2"}},
	}}

	pool := []FallbackModel{
		{ProviderConfig: cfg0, ProviderImpl: impl0, ModelID: "model0", MaxTokens: 4096, ContextLimit: 128000},
		{ProviderConfig: cfg1, ProviderImpl: impl1, ModelID: "model1", MaxTokens: 4096, ContextLimit: 128000},
		{ProviderConfig: cfg2, ProviderImpl: impl2, ModelID: "model2", MaxTokens: 4096, ContextLimit: 128000},
	}

	c := NewClient(cfg0, impl0, "model0", 4096, "sys")
	c.SetModelPool(pool, 0)

	resp1, err := c.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "hi-1"}}, nil, nil)
	if err != nil {
		t.Fatalf("first CompleteStream() error = %v", err)
	}
	if resp1 == nil || resp1.Content != "from model1 call1" {
		t.Fatalf("first response = %#v, want from model1 call1", resp1)
	}
	st1 := c.LastCallStatus()
	if st1.SelectedModelRef != "prov/model0" {
		t.Fatalf("first cursor-start SelectedModelRef = %q, want prov/model0", st1.SelectedModelRef)
	}
	if st1.RunningModelRef != "prov/model1" {
		t.Fatalf("first RunningModelRef = %q, want prov/model1", st1.RunningModelRef)
	}
	if next := c.NextRequestModelRef(); next != "prov/model1" {
		t.Fatalf("NextRequestModelRef after fallback success = %q, want prov/model1", next)
	}

	resp2, err := c.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "hi-2"}}, nil, nil)
	if err != nil {
		t.Fatalf("second CompleteStream() error = %v", err)
	}
	if resp2 == nil || resp2.Content != "from model1 call2" {
		t.Fatalf("second response = %#v, want from model1 call2", resp2)
	}
	st2 := c.LastCallStatus()
	if st2.SelectedModelRef != "prov/model1" {
		t.Fatalf("second cursor-start SelectedModelRef = %q, want prov/model1", st2.SelectedModelRef)
	}
	if st2.RunningModelRef != "prov/model1" {
		t.Fatalf("second RunningModelRef = %q, want prov/model1", st2.RunningModelRef)
	}
	if impl0.CallCount() != 1 {
		t.Fatalf("model0 calls = %d, want 1 (should not reset to model0 on second request)", impl0.CallCount())
	}
}

func TestSupportsAnthropicPromptCacheUsesCursorFallbackTarget(t *testing.T) {
	primaryCfg := NewProviderConfig("openai", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"gpt": {Limit: config.ModelLimit{Context: 128000, Output: 4096}},
		},
	}, []string{"primary-key"})
	fallbackCfg := NewProviderConfig("anthropic", config.ProviderConfig{
		Type: config.ProviderTypeMessages,
		Models: map[string]config.ModelConfig{
			"claude": {Limit: config.ModelLimit{Context: 200000, Output: 8192}},
		},
	}, []string{"fallback-key"})

	c := NewClient(primaryCfg, &scriptedProvider{}, "gpt", 4096, "sys")
	c.SetFallbackModels([]FallbackModel{{
		ProviderConfig: fallbackCfg,
		ProviderImpl:   &scriptedProvider{},
		ModelID:        "claude",
		MaxTokens:      8192,
		ContextLimit:   200000,
	}})
	c.mu.Lock()
	c.poolCursor = 1
	c.mu.Unlock()

	if next := c.NextRequestModelRef(); next != "anthropic/claude" {
		t.Fatalf("NextRequestModelRef = %q, want anthropic/claude", next)
	}
	if !c.SupportsAnthropicPromptCache(c.NextRequestModelRef()) {
		t.Fatal("expected Anthropic fallback cursor target to support prompt-cache boundary hints")
	}
	if !c.SupportsAnthropicPromptCache("") {
		t.Fatal("expected empty ref to use the Anthropic cursor target")
	}
	if c.SupportsAnthropicPromptCache("openai/gpt") {
		t.Fatal("primary OpenAI target should not support Anthropic prompt-cache boundary hints")
	}
}

// TestSupportsThinkingReplay verifies the visible-reasoning replay capability
// is resolved per cursor-head target: DeepSeek-style thinking models report
// true, plain chat models false, and the empty ref follows the current cursor.
func TestSupportsThinkingReplay(t *testing.T) {
	deepseekCfg := NewProviderConfig("deepseek", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"deepseek-v4-pro": {Compat: &config.ModelCompatConfig{
				ReasoningContinuity: &config.ReasoningContinuityCompatConfig{Mode: "openai_visible"},
			}},
		},
	}, []string{"k"})
	plainCfg := NewProviderConfig("openai", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"gpt-4o": {},
		},
	}, []string{"k"})

	c := NewClient(deepseekCfg, &scriptedProvider{}, "deepseek-v4-pro", 1024, "sys")
	if !c.SupportsThinkingReplay("deepseek/deepseek-v4-pro") {
		t.Fatal("DeepSeek-style target should support visible reasoning replay")
	}
	if !c.SupportsThinkingReplay("") {
		t.Fatal("empty ref should follow the DeepSeek cursor target")
	}
	if c.SupportsThinkingReplay("openai/gpt-4o") {
		t.Fatal("plain chat target should not support visible reasoning replay")
	}

	plain := NewClient(plainCfg, &scriptedProvider{}, "gpt-4o", 1024, "sys")
	if plain.SupportsThinkingReplay("openai/gpt-4o") {
		t.Fatal("plain chat target should not support visible reasoning replay")
	}
	if plain.SupportsThinkingReplay("") {
		t.Fatal("empty ref should follow the plain chat cursor target")
	}
}

func TestModelPoolStickyCursorPreservesPinnedFallbackVariant(t *testing.T) {
	primaryCfg := testProviderConfig("primary-prov", "primary-model")
	fallbackCfg := NewProviderConfig("fallback-prov", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"gpt-5.5": {
				Limit: config.ModelLimit{Context: 128000, Output: 4096},
				Variants: map[string]config.ModelVariant{
					"xhigh": {Reasoning: &config.ReasoningConfig{Effort: "high"}},
				},
			},
		},
	}, []string{"fb-key"})

	primaryImpl := &scriptedProvider{calls: []scriptedCall{{err: &APIError{StatusCode: 500, Message: "upstream unavailable"}}}}
	fallbackImpl := &scriptedProvider{calls: []scriptedCall{
		{resp: &message.Response{Content: "fallback first"}},
		{resp: &message.Response{Content: "fallback second"}},
	}}

	c := NewClient(primaryCfg, primaryImpl, "primary-model", 4096, "sys")
	c.SetModelPool([]FallbackModel{
		{ProviderConfig: primaryCfg, ProviderImpl: primaryImpl, ModelID: "primary-model", MaxTokens: 4096, ContextLimit: 128000},
		{ProviderConfig: fallbackCfg, ProviderImpl: fallbackImpl, ModelID: "gpt-5.5", MaxTokens: 4096, ContextLimit: 128000, Variant: "xhigh"},
	}, 0)

	resp1, err := c.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "hi-1"}}, nil, nil)
	if err != nil {
		t.Fatalf("first CompleteStream() error = %v", err)
	}
	if resp1 == nil || resp1.Content != "fallback first" {
		t.Fatalf("first response = %#v, want fallback first", resp1)
	}
	st1 := c.LastCallStatus()
	if st1.RunningModelRef != "fallback-prov/gpt-5.5@xhigh" {
		t.Fatalf("first RunningModelRef = %q, want fallback-prov/gpt-5.5@xhigh", st1.RunningModelRef)
	}

	resp2, err := c.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "hi-2"}}, nil, nil)
	if err != nil {
		t.Fatalf("second CompleteStream() error = %v", err)
	}
	if resp2 == nil || resp2.Content != "fallback second" {
		t.Fatalf("second response = %#v, want fallback second", resp2)
	}
	st2 := c.LastCallStatus()
	if st2.SelectedModelRef != "fallback-prov/gpt-5.5@xhigh" {
		t.Fatalf("second SelectedModelRef = %q, want fallback-prov/gpt-5.5@xhigh", st2.SelectedModelRef)
	}
	if st2.RunningModelRef != "fallback-prov/gpt-5.5@xhigh" {
		t.Fatalf("second RunningModelRef = %q, want fallback-prov/gpt-5.5@xhigh", st2.RunningModelRef)
	}
}

func TestModelPoolStickyCursorDoesNotLeakPrimaryVariantToVariantlessPinnedFallback(t *testing.T) {
	primaryCfg := NewProviderConfig("openai", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"gpt-5.5": {
				Limit: config.ModelLimit{Context: 400000, Output: 128000},
				Variants: map[string]config.ModelVariant{
					"high": {Thinking: &config.ThinkingConfig{Effort: "high"}},
				},
			},
		},
	}, []string{"k"})
	fallbackCfg := NewProviderConfig("fallback-prov", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"fallback-model": {Limit: config.ModelLimit{Context: 128000, Output: 4096}},
		},
	}, []string{"fb-key"})

	primaryImpl := &scriptedProvider{calls: []scriptedCall{{err: &APIError{StatusCode: 500, Message: "fail primary"}}}}
	fallbackImpl := &scriptedProvider{calls: []scriptedCall{
		{resp: &message.Response{Content: "fallback first"}},
		{resp: &message.Response{Content: "fallback second"}},
	}}

	c := NewClient(primaryCfg, primaryImpl, "gpt-5.5", 4096, "sys")
	c.SetVariant("high")
	c.SetModelPool([]FallbackModel{
		{ProviderConfig: primaryCfg, ProviderImpl: primaryImpl, ModelID: "gpt-5.5", MaxTokens: 4096, ContextLimit: 400000, Variant: "high"},
		{ProviderConfig: fallbackCfg, ProviderImpl: fallbackImpl, ModelID: "fallback-model", MaxTokens: 4096, ContextLimit: 128000},
	}, 0)

	resp1, err := c.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "hi-1"}}, nil, nil)
	if err != nil {
		t.Fatalf("first CompleteStream() error = %v", err)
	}
	if resp1 == nil || resp1.Content != "fallback first" {
		t.Fatalf("first response = %#v, want fallback first", resp1)
	}
	st1 := c.LastCallStatus()
	if st1.RunningModelRef != "fallback-prov/fallback-model" {
		t.Fatalf("first RunningModelRef = %q, want fallback-prov/fallback-model", st1.RunningModelRef)
	}

	resp2, err := c.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "hi-2"}}, nil, nil)
	if err != nil {
		t.Fatalf("second CompleteStream() error = %v", err)
	}
	if resp2 == nil || resp2.Content != "fallback second" {
		t.Fatalf("second response = %#v, want fallback second", resp2)
	}
	st2 := c.LastCallStatus()
	if st2.SelectedModelRef != "fallback-prov/fallback-model" {
		t.Fatalf("second SelectedModelRef = %q, want fallback-prov/fallback-model", st2.SelectedModelRef)
	}
	if st2.RunningModelRef != "fallback-prov/fallback-model" {
		t.Fatalf("second RunningModelRef = %q, want fallback-prov/fallback-model", st2.RunningModelRef)
	}
}

func TestClientCompleteRetriesCompatibleOverloaded400BeforeFallbackModel(t *testing.T) {
	primaryCfg := testCompatibleResponsesProviderConfigWithKeys("gateway", "gpt-test", []string{"k1", "k2"})
	fallbackCfg := testProviderConfig("fallback", "fallback-model")
	disableRetryDelayForTest(primaryCfg)

	primaryImpl := &recordingProvider{}
	primaryImpl.calls = []scriptedCall{
		{err: &APIError{StatusCode: 400, Message: "Our servers are currently overloaded. Please try again later."}},
		{resp: &message.Response{Content: "ok after second key"}},
	}
	fallbackImpl := &recordingProvider{}
	fallbackImpl.calls = []scriptedCall{{resp: &message.Response{Content: "fallback should not run"}}}

	client := NewClient(primaryCfg, primaryImpl, "gpt-test", 4096, "sys")
	client.SetFallbackModels([]FallbackModel{{
		ProviderConfig: fallbackCfg,
		ProviderImpl:   fallbackImpl,
		ModelID:        "fallback-model",
		MaxTokens:      4096,
		ContextLimit:   128000,
	}})

	resp, err := client.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}
	if resp == nil || resp.Content != "ok after second key" {
		t.Fatalf("response = %#v, want ok after second key", resp)
	}
	if primaryImpl.CallCount() != 2 {
		t.Fatalf("primary provider calls = %d, want 2", primaryImpl.CallCount())
	}
	if fallbackImpl.CallCount() != 0 {
		t.Fatalf("fallback provider calls = %d, want 0", fallbackImpl.CallCount())
	}
	if got := len(primaryImpl.apiKeys); got != 2 {
		t.Fatalf("primary api key call count = %d, want 2", got)
	}
	if primaryImpl.apiKeys[0] != "k1" || primaryImpl.apiKeys[1] != "k2" {
		t.Fatalf("primary apiKeys = %#v, want [k1 k2]", primaryImpl.apiKeys)
	}
}

func TestClientSetModelPoolIgnoresUndefinedVariant(t *testing.T) {
	provider := &recordingTuningProvider{}
	cfg := NewProviderConfig("openai", config.ProviderConfig{
		Type: config.ProviderTypeResponses,
		Models: map[string]config.ModelConfig{
			"gpt-5.5": {
				Limit:     config.ModelLimit{Context: 400000, Output: 128000},
				Reasoning: &config.ReasoningConfig{Effort: "medium", Summary: "auto"},
				Variants: map[string]config.ModelVariant{
					"high": {Reasoning: &config.ReasoningConfig{Effort: "high"}},
				},
			},
		},
	}, []string{"k"})
	c := NewClient(cfg, provider, "gpt-5.5", 4096, "sys")
	c.SetModelPool([]FallbackModel{{
		ProviderConfig: cfg,
		ProviderImpl:   provider,
		ModelID:        "gpt-5.5",
		MaxTokens:      4096,
		ContextLimit:   400000,
		Variant:        "missing",
	}}, 0)

	if got := c.ActiveVariant(); got != "" {
		t.Fatalf("ActiveVariant = %q, want empty for undefined variant", got)
	}
	if got := c.NextRequestModelRef(); got != "openai/gpt-5.5" {
		t.Fatalf("NextRequestModelRef = %q, want openai/gpt-5.5", got)
	}
	if _, err := c.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "hi"}}, nil, nil); err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}
	if len(provider.tuning) != 1 {
		t.Fatalf("len(provider.tuning) = %d, want 1", len(provider.tuning))
	}
	got := provider.tuning[0].OpenAI
	if got.ReasoningEffort != "medium" || got.ReasoningSummary != "auto" {
		t.Fatalf("OpenAI tuning = %+v, want model defaults without undefined variant", got)
	}
}

func TestClientRunningModelRefIncludesVariantOnPrimarySuccess(t *testing.T) {
	cfg := NewProviderConfig("openai", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"gpt-5.5": {
				Limit: config.ModelLimit{Context: 400000, Output: 128000},
				Variants: map[string]config.ModelVariant{
					"high": {Thinking: &config.ThinkingConfig{Effort: "high"}},
				},
			},
		},
	}, []string{"k"})

	impl := &scriptedProvider{
		calls: []scriptedCall{
			{resp: &message.Response{Content: "ok"}},
		},
	}

	c := NewClient(cfg, impl, "gpt-5.5", 4096, "sys")
	c.SetVariant("high")

	resp, err := c.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}
	if resp == nil || resp.Content != "ok" {
		t.Fatalf("unexpected response: %#v", resp)
	}

	st := c.LastCallStatus()
	if st.RunningModelRef != "openai/gpt-5.5@high" {
		t.Fatalf("RunningModelRef = %q, want openai/gpt-5.5@high", st.RunningModelRef)
	}
}

func TestClientRunningModelRefOmitsVariantOnFallbackSuccess(t *testing.T) {
	primaryCfg := NewProviderConfig("openai", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"gpt-5.5": {
				Limit: config.ModelLimit{Context: 400000, Output: 128000},
				Variants: map[string]config.ModelVariant{
					"high": {Thinking: &config.ThinkingConfig{Effort: "high"}},
				},
			},
		},
	}, []string{"k"})

	fallbackCfg := NewProviderConfig("fallback-prov", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"fallback-model": {
				Limit: config.ModelLimit{Context: 128000, Output: 4096},
			},
		},
	}, []string{"fb-key"})

	primaryImpl := &scriptedProvider{
		calls: []scriptedCall{
			{err: &APIError{StatusCode: 500, Message: "fail"}},
		},
	}
	fallbackImpl := &scriptedProvider{
		calls: []scriptedCall{
			{resp: &message.Response{Content: "fallback ok"}},
		},
	}

	c := NewClient(primaryCfg, primaryImpl, "gpt-5.5", 4096, "sys")
	c.SetVariant("high")
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
	if resp == nil || resp.Content != "fallback ok" {
		t.Fatalf("unexpected response: %#v", resp)
	}

	st := c.LastCallStatus()
	if st.RunningModelRef != "fallback-prov/fallback-model" {
		t.Fatalf("RunningModelRef = %q, want fallback-prov/fallback-model (no variant leak)", st.RunningModelRef)
	}
}

func TestCompleteStreamFallbackRunningModelRefIncludesFallbackVariant(t *testing.T) {
	primaryCfg := NewProviderConfig("primary-prov", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"primary-model": {Limit: config.ModelLimit{Context: 128000, Output: 4096}},
		},
	}, []string{"k1"})
	primaryCfg.MarkCooldown("k1", time.Minute)

	fallbackCfg := NewProviderConfig("fallback-prov", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"fallback-model": {
				Limit: config.ModelLimit{Context: 128000, Output: 4096},
				Variants: map[string]config.ModelVariant{
					"high": {},
				},
			},
		},
	}, []string{"k2"})

	implPrimary := &recordingProvider{}
	implFallback := &recordingProvider{}
	implFallback.calls = []scriptedCall{{resp: &message.Response{Content: "ok"}}}

	c := NewClient(primaryCfg, implPrimary, "primary-model", 4096, "sys")
	c.SetFallbackModels([]FallbackModel{{
		ProviderConfig: fallbackCfg,
		ProviderImpl:   implFallback,
		ModelID:        "fallback-model",
		MaxTokens:      4096,
		ContextLimit:   128000,
		Variant:        "high",
	}})

	st := &CallStatus{}
	resp, err := callCompleteStreamWithRetryForTest(
		c,
		context.Background(),
		primaryCfg,
		implPrimary,
		"primary-model",
		4096,
		RequestTuning{},
		"",
		[]message.Message{{Role: "user", Content: "hi"}},
		nil,
		nil,
		true,
		c.fallbackModels,
		1,
		st,
	)
	if err != nil {
		t.Fatalf("completeStreamWithRetry returned error: %v", err)
	}
	if resp == nil || resp.Content != "ok" {
		t.Fatalf("unexpected response: %#v", resp)
	}
	if st.RunningModelRef != "fallback-prov/fallback-model@high" {
		t.Fatalf("RunningModelRef = %q, want fallback-prov/fallback-model@high", st.RunningModelRef)
	}
}
