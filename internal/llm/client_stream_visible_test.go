package llm

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
)

func TestResponseHasUsableOutputRequiresReplayableThinking(t *testing.T) {
	if responseHasUsableOutput(&message.Response{ThinkingBlocks: []message.ThinkingBlock{{Thinking: "analysis"}}}) {
		t.Fatal("thinking block without signature should not be usable output")
	}
	if !responseHasUsableOutput(&message.Response{ThinkingBlocks: []message.ThinkingBlock{{Thinking: "analysis", Signature: "sig"}}}) {
		t.Fatal("signed thinking block should be usable output")
	}
	if !responseHasUsableOutput(&message.Response{Content: "visible"}) {
		t.Fatal("visible text should be usable output")
	}
	if !responseHasUsableOutput(&message.Response{ToolCalls: []message.ToolCall{{ID: "call_1", Name: "read"}}}) {
		t.Fatal("tool calls should be usable output")
	}
}

func TestResponseHasUsableOutputIgnoresZeroWidthPlaceholder(t *testing.T) {
	// Some gateways return a zero-width space as the only "content" of an
	// interrupted/empty response (e.g. after a 60s chunk timeout). Treating it
	// as usable output bypasses retry and idles the agent without an error.
	if responseHasUsableOutput(&message.Response{Content: "\u200b"}) {
		t.Fatal("zero-width placeholder content should not be usable output")
	}
	if responseHasUsableOutput(&message.Response{Content: "\u200b \u200b\t\u200e\n"}) {
		t.Fatal("whitespace/zero-width-only content should not be usable output")
	}
	if !responseHasUsableOutput(&message.Response{Content: "\u200bvisible"}) {
		t.Fatal("content with visible characters should be usable output")
	}
}

func TestResponseHasUsableOutputAcceptsTruncatedReasoning(t *testing.T) {
	if !responseHasUsableOutput(&message.Response{
		ReasoningContent: "unfinished reasoning",
		StopReason:       "max_tokens",
	}) {
		t.Fatal("visible reasoning from a truncated response must reach agent recovery")
	}
	if responseHasUsableOutput(&message.Response{
		ReasoningContent: "finished reasoning only",
		StopReason:       "stop",
	}) {
		t.Fatal("reasoning-only normal stop should not bypass empty-response handling")
	}
}

func TestCompleteStreamReturnsTruncatedReasoningToCaller(t *testing.T) {
	cfg := testProviderConfig("sample", "test-model")
	provider := &scriptedProvider{calls: []scriptedCall{{resp: &message.Response{
		ReasoningContent: "unfinished reasoning",
		StopReason:       "max_tokens",
	}}}}
	c := NewClient(cfg, provider, "test-model", 1024, "")

	resp, err := c.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "question"}}, nil, nil)
	if err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}
	if resp == nil || resp.ReasoningContent != "unfinished reasoning" || resp.StopReason != "max_tokens" {
		t.Fatalf("unexpected response: %#v", resp)
	}
}

func TestCompleteStreamZeroWidthInterruptedRetriesNextKey(t *testing.T) {
	primaryCfg := testProviderConfigWithKeys("primary-prov", "primary-model", []string{"k1", "k2"})
	disableRetryDelayForTest(primaryCfg)
	primaryImpl := &recordingProvider{}
	primaryImpl.calls = []scriptedCall{
		// First key returns an empty interrupted response whose only "content"
		// is a zero-width space (seen from real gateways after a chunk timeout).
		{resp: &message.Response{Content: "\u200b", StopReason: "interrupted"}},
		// Second key succeeds with visible content.
		{resp: &message.Response{Content: "ok from k2"}},
	}
	c := NewClient(primaryCfg, primaryImpl, "primary-model", 4096, "sys")

	var deltas []message.StreamDelta
	resp, err := c.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "hi"}}, nil, func(delta message.StreamDelta) {
		deltas = append(deltas, delta)
	})
	if err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}
	if resp == nil || resp.Content != "ok from k2" {
		t.Fatalf("unexpected response: %#v", resp)
	}
	if len(primaryImpl.apiKeys) != 2 || primaryImpl.apiKeys[0] != "k1" || primaryImpl.apiKeys[1] != "k2" {
		t.Fatalf("attempted keys = %#v, want [k1 k2]", primaryImpl.apiKeys)
	}
	hadRetry := false
	for _, d := range deltas {
		if d.Type == message.StreamDeltaRetryError {
			hadRetry = true
		}
	}
	if !hadRetry {
		t.Fatal("expected StreamDeltaRetryError for the empty interrupted response")
	}
}

func TestVisibleStreamTrackerMarksToolUseAsStreaming(t *testing.T) {
	var got []message.StreamDelta
	tracker := &visibleStreamTracker{
		inner: func(delta message.StreamDelta) {
			got = append(got, delta)
		},
		onVisibleStart: func() {
			got = append(got, message.StreamDelta{
				Type:   "status",
				Status: &message.StatusDelta{Type: "streaming"},
			})
			got = append(got, message.StreamDelta{Type: message.StreamDeltaKeyConfirmed})
		},
	}

	tracker.Callback(message.StreamDelta{
		Type:   "status",
		Status: &message.StatusDelta{Type: "waiting_token"},
	})
	tracker.Callback(message.StreamDelta{
		Type: "tool_use_start",
		ToolCall: &message.ToolCallDelta{
			ID:    "call-1",
			Name:  "Write",
			Input: `{"path":"demo.txt","content":"hello"}`,
		},
	})

	if len(got) != 4 {
		t.Fatalf("len(got) = %d, want 4", len(got))
	}
	if got[0].Type != "status" || got[0].Status == nil || got[0].Status.Type != "waiting_token" {
		t.Fatalf("got[0] = %#v, want waiting_token status", got[0])
	}
	if got[1].Type != "status" || got[1].Status == nil || got[1].Status.Type != "streaming" {
		t.Fatalf("got[1] = %#v, want streaming status", got[1])
	}
	if got[2].Type != message.StreamDeltaKeyConfirmed {
		t.Fatalf("got[2].Type = %q, want %q", got[2].Type, message.StreamDeltaKeyConfirmed)
	}
	if got[3].Type != "tool_use_start" || got[3].ToolCall == nil || got[3].ToolCall.ID != "call-1" {
		t.Fatalf("got[3] = %#v, want tool_use_start for call-1", got[3])
	}
}

func TestVisibleStreamTrackerRearmsAfterRollback(t *testing.T) {
	var got []message.StreamDelta
	tracker := &visibleStreamTracker{
		inner: func(delta message.StreamDelta) {
			got = append(got, delta)
		},
		onVisibleStart: func() {
			got = append(got, message.StreamDelta{
				Type:   "status",
				Status: &message.StatusDelta{Type: "streaming"},
			})
			got = append(got, message.StreamDelta{Type: message.StreamDeltaKeyConfirmed})
		},
	}

	tracker.Callback(message.StreamDelta{
		Type: "tool_use_start",
		ToolCall: &message.ToolCallDelta{
			ID:    "call-1",
			Name:  "Write",
			Input: `{"path":"demo.txt"}`,
		},
	})
	tracker.Callback(message.StreamDelta{
		Type:     "rollback",
		Rollback: &message.RollbackDelta{Reason: "retry same key"},
	})
	tracker.Callback(message.StreamDelta{
		Type: "text",
		Text: "retry succeeded",
	})

	if len(got) != 7 {
		t.Fatalf("len(got) = %d, want 7", len(got))
	}
	if got[0].Type != "status" || got[0].Status == nil || got[0].Status.Type != "streaming" {
		t.Fatalf("got[0] = %#v, want first streaming status", got[0])
	}
	if got[1].Type != message.StreamDeltaKeyConfirmed {
		t.Fatalf("got[1].Type = %q, want %q", got[1].Type, message.StreamDeltaKeyConfirmed)
	}
	if got[2].Type != "tool_use_start" {
		t.Fatalf("got[2].Type = %q, want tool_use_start", got[2].Type)
	}
	if got[3].Type != "rollback" {
		t.Fatalf("got[3].Type = %q, want rollback", got[3].Type)
	}
	if got[4].Type != "status" || got[4].Status == nil || got[4].Status.Type != "streaming" {
		t.Fatalf("got[4] = %#v, want second streaming status", got[4])
	}
	if got[5].Type != message.StreamDeltaKeyConfirmed {
		t.Fatalf("got[5].Type = %q, want second %q", got[5].Type, message.StreamDeltaKeyConfirmed)
	}
	if got[6].Type != "text" || got[6].Text != "retry succeeded" {
		t.Fatalf("got[6] = %#v, want retry text", got[6])
	}
}

func TestClient_InterruptedPartialEscalatesForContinuation(t *testing.T) {
	primaryCfg := testProviderConfigWithKeys("sample", "gpt-5.4", []string{"key-a"})
	impl := &scriptedProvider{calls: []scriptedCall{
		{streams: []message.StreamDelta{{Type: "text", Text: "partial "}}, resp: &message.Response{Content: "partial ", StopReason: "interrupted"}},
		{resp: &message.Response{Content: "text continued", StopReason: "stop"}},
	}}
	c := &Client{}
	resp, err := callCompleteStreamWithRetryForTest(
		c,
		context.Background(),
		primaryCfg,
		impl,
		"gpt-5.4",
		4096,
		RequestTuning{},
		"",
		[]message.Message{{Role: "user", Content: "hi"}},
		nil,
		nil,
		true,
		nil,
		0, // default: retry until success
		&CallStatus{},
	)
	// A stream interruption that already produced assistant text is no longer
	// silently retried by the client: the partial reply is preserved on screen
	// and the failure escalates so the caller can save it and resume with a
	// continuation prompt. Nothing may be discarded by an in-client retry.
	if _, ok := errors.AsType[*InterruptedResponseError](err); !ok {
		t.Fatalf("completeStreamWithRetry err = %v, want *InterruptedResponseError escalated for continuation", err)
	}
	if resp != nil {
		t.Fatalf("unexpected response: %#v", resp)
	}
	if got := impl.CallCount(); got != 1 {
		t.Fatalf("provider calls = %d, want 1 (the interrupted attempt only; no silent in-client retry)", got)
	}
}

// TestClient_VisibleInterruptedStreamCoolsKeyForEscalation guards the retry
// governance behind an uncapped preserved-interruption resume: instead of a
// round cap, each escalated interruption cools the key that streamed the
// partial, so a restart through the normal key/fallback rotation skips the key
// (or waits out the cooldown when every key is cooling) rather than immediately
// reusing the same key that just truncated. A single blip costs only the base
// wait.
func TestClient_VisibleInterruptedStreamCoolsKeyForEscalation(t *testing.T) {
	t.Run("stream_error", func(t *testing.T) {
		primaryCfg := testProviderConfigWithKeys("sample", "gpt-5.4", []string{"key-a"})
		impl := &scriptedProvider{calls: []scriptedCall{
			{streams: []message.StreamDelta{{Type: "text", Text: "partial "}}, err: io.ErrUnexpectedEOF},
		}}
		c := NewClient(primaryCfg, impl, "gpt-5.4", 4096, "sys")
		_, err := callCompleteStreamWithRetryForTest(
			c,
			context.Background(),
			primaryCfg,
			impl,
			"gpt-5.4",
			4096,
			RequestTuning{},
			"",
			[]message.Message{{Role: "user", Content: "hi"}},
			nil,
			nil,
			true,
			nil,
			0,
			&CallStatus{},
		)
		if !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatalf("err = %v, want io.ErrUnexpectedEOF escalated for continuation", err)
		}
		ks := *primaryCfg.keyStates[0]
		if !ks.CooldownEnd.After(time.Now()) {
			t.Fatalf("key cooldown_end = %v, want a future cooldown after the escalation", ks.CooldownEnd)
		}
		if ks.TransportFailureCount != 1 {
			t.Fatalf("key transport_failure_count = %d, want 1", ks.TransportFailureCount)
		}
		if !ks.Recovering {
			t.Fatal("cooled key should be marked recovering")
		}
		// A restart through the normal rotation must not immediately reuse the
		// cooled key: with a single key the next selection waits for cooldown.
		if _, _, err := primaryCfg.SelectKeyWithContext(context.Background()); err == nil {
			t.Fatal("SelectKeyWithContext succeeded, want AllKeysCoolingError while the key is cooled")
		}
	})
	t.Run("interrupted_response", func(t *testing.T) {
		primaryCfg := testProviderConfigWithKeys("sample", "gpt-5.4", []string{"key-a"})
		impl := &scriptedProvider{calls: []scriptedCall{
			{resp: &message.Response{Content: "partial ", StopReason: "interrupted"}},
		}}
		c := NewClient(primaryCfg, impl, "gpt-5.4", 4096, "sys")
		_, err := callCompleteStreamWithRetryForTest(
			c,
			context.Background(),
			primaryCfg,
			impl,
			"gpt-5.4",
			4096,
			RequestTuning{},
			"",
			[]message.Message{{Role: "user", Content: "hi"}},
			nil,
			nil,
			true,
			nil,
			0,
			&CallStatus{},
		)
		if _, ok := errors.AsType[*InterruptedResponseError](err); !ok {
			t.Fatalf("err = %v, want *InterruptedResponseError escalated", err)
		}
		ks := *primaryCfg.keyStates[0]
		if !ks.CooldownEnd.After(time.Now()) {
			t.Fatalf("key cooldown_end = %v, want a future cooldown after the escalation", ks.CooldownEnd)
		}
		if !ks.Recovering {
			t.Fatal("cooled key should be marked recovering")
		}
		// An interrupted response is a failed attempt, so it must not be
		// marked as a key success: doing so would reset CooldownCount and pin
		// this path at the base wait forever, however often the gateway
		// truncates.
		if ks.TransportFailureCount != 1 {
			t.Fatalf("key transport_failure_count = %d, want 1", ks.TransportFailureCount)
		}
		if _, _, err := primaryCfg.SelectKeyWithContext(context.Background()); err == nil {
			t.Fatal("SelectKeyWithContext succeeded, want AllKeysCoolingError while the key is cooled")
		}
	})
}

// TestClient_RepeatedInterruptionsGrowTheTransportCooldown pins the throttle
// that replaces a round cap: consecutive truncations must back off, otherwise a
// gateway that truncates after a few tokens keeps the agent restarting at a
// fixed cadence while the history grows by two messages per round. Both shapes
// of interruption are covered because they settle through different branches
// and only one of them used to keep the failure count.
func TestClient_RepeatedInterruptionsGrowTheTransportCooldown(t *testing.T) {
	origBase, origMax := preservedInterruptionCooldownBase, preservedInterruptionCooldownMax
	preservedInterruptionCooldownBase, preservedInterruptionCooldownMax = 50*time.Millisecond, 400*time.Millisecond
	t.Cleanup(func() {
		preservedInterruptionCooldownBase, preservedInterruptionCooldownMax = origBase, origMax
	})

	run := func(t *testing.T, call scriptedCall) time.Duration {
		t.Helper()
		cfg := testProviderConfigWithKeys("sample", "gpt-5.4", []string{"key-a"})
		var waits []time.Duration
		for round := 1; round <= 2; round++ {
			impl := &scriptedProvider{calls: []scriptedCall{call}}
			c := NewClient(cfg, impl, "gpt-5.4", 4096, "sys")
			_, err := callCompleteStreamWithRetryForTest(
				c, context.Background(), cfg, impl, "gpt-5.4", 4096,
				RequestTuning{}, "", []message.Message{{Role: "user", Content: "hi"}},
				nil, nil, true, nil, 0, &CallStatus{},
			)
			if err == nil {
				t.Fatalf("round %d: err = nil, want an escalated interruption", round)
			}
			ks := *cfg.keyStates[0]
			if ks.TransportFailureCount != round {
				t.Fatalf("round %d: transport_failure_count = %d, want %d (first visible output must not clear the truncation backoff)", round, ks.TransportFailureCount, round)
			}
			waits = append(waits, time.Until(ks.CooldownEnd))
		}
		if waits[1] <= waits[0] {
			t.Fatalf("cooldown did not grow across consecutive interruptions: %v then %v", waits[0], waits[1])
		}
		return waits[1]
	}
	t.Run("stream_error", func(t *testing.T) {
		run(t, scriptedCall{streams: []message.StreamDelta{{Type: "text", Text: "partial "}}, err: io.ErrUnexpectedEOF})
	})
	t.Run("interrupted_response", func(t *testing.T) {
		grown := run(t, scriptedCall{resp: &message.Response{Content: "partial ", StopReason: "interrupted"}})
		// The throttle saturates far below maxProviderRetryDelay: the same
		// ProviderConfig serves compaction, sub-agents and thinking translation,
		// which must not be starved while one reply keeps truncating.
		if grown > preservedInterruptionCooldownMax+time.Second {
			t.Fatalf("cooldown = %v, want it capped near %v", grown, preservedInterruptionCooldownMax)
		}
	})
}

// TestClient_InterruptedPartialResponseRespectsExplicitRetryCap guards the
// retry counter: an interrupted-with-text response is discarded as a failed
// attempt and must count against an explicit cap. Counting it as a usable
// reply would reset the counter every round and turn a bounded cap into an
// unthrottled request loop.
func TestClient_InterruptedPartialResponseRespectsExplicitRetryCap(t *testing.T) {
	primaryCfg := testProviderConfigWithKeys("primary-prov", "gpt-test", []string{"k1"})
	impl := &scriptedProvider{calls: []scriptedCall{
		{resp: &message.Response{Content: "partial ", StopReason: "interrupted"}},
		{resp: &message.Response{Content: "unreachable", StopReason: "interrupted"}},
	}}
	c := &Client{}
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
		nil,
		false,
		nil,
		1, // explicit cap: the interrupted attempt must exhaust it
		&CallStatus{},
	)
	if resp != nil {
		t.Fatalf("unexpected response: %#v", resp)
	}
	if _, ok := errors.AsType[*InterruptedResponseError](err); !ok {
		t.Fatalf("err = %v, want *InterruptedResponseError after the cap", err)
	}
	if got := impl.CallCount(); got != 1 {
		t.Fatalf("provider calls = %d, want 1 (cap exhausted by the interrupted attempt)", got)
	}
}

// TestClient_VisibleInterruptionEscalatesWithoutRollback guards the preserved
// partial of a visible stream interruption: the failure escalates to the
// caller (which saves the partial and resumes it) and no rollback is emitted,
// so the card keeps showing the partial text instead of blanking it.
func TestClient_VisibleInterruptionEscalatesWithoutRollback(t *testing.T) {
	primaryCfg := testProviderConfigWithKeys("primary-prov", "gpt-test", []string{"k1"})
	impl := &scriptedProvider{calls: []scriptedCall{
		{streams: []message.StreamDelta{{Type: "text", Text: "partial"}}, err: io.ErrUnexpectedEOF},
		{streams: []message.StreamDelta{{Type: "text", Text: "regenerated"}}, resp: &message.Response{Content: "regenerated", StopReason: "stop"}},
	}}
	c := NewClient(primaryCfg, impl, "gpt-test", 4096, "sys")

	var rollbacks int
	var textSeen bool
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
			switch delta.Type {
			case message.StreamDeltaText:
				textSeen = true
			case message.StreamDeltaRollback:
				rollbacks++
			}
		},
		false,
		nil,
		0, // default: retry until success
		&CallStatus{},
	)
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("completeStreamWithRetry err = %v, want io.ErrUnexpectedEOF escalated for continuation", err)
	}
	if resp != nil {
		t.Fatalf("response = %#v, want nil (no silent in-client retry)", resp)
	}
	if rollbacks != 0 {
		t.Fatalf("rollback deltas = %d, want 0 (preserved partial stays on screen)", rollbacks)
	}
	if !textSeen {
		t.Fatal("expected the streamed partial text delta before the failure")
	}
	if got := impl.CallCount(); got != 1 {
		t.Fatalf("provider calls = %d, want 1 (the interrupted attempt only; no silent in-client retry)", got)
	}
}

// TestClient_ThinkingOnlyInterruptionKeepsSilentRetry guards the other half of
// the escalation rule. Reasoning deltas are visible but never reach the
// caller's partial-text accumulator, so an interruption after thinking alone
// leaves nothing to resume: escalating it would end the turn instead of
// recovering it, which is the opposite of what the preservation is for.
func TestClient_ThinkingOnlyInterruptionKeepsSilentRetry(t *testing.T) {
	primaryCfg := testProviderConfigWithKeys("primary-prov", "gpt-test", []string{"k1"})
	disableRetryDelayForTest(primaryCfg)
	impl := &scriptedProvider{calls: []scriptedCall{
		{streams: []message.StreamDelta{{Type: message.StreamDeltaThinking, Text: "let me work through this"}}, err: io.ErrUnexpectedEOF},
		{streams: []message.StreamDelta{{Type: message.StreamDeltaText, Text: "the answer"}}, resp: &message.Response{Content: "the answer", StopReason: "stop"}},
	}}
	c := NewClient(primaryCfg, impl, "gpt-test", 4096, "sys")

	var rollbacks int
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
			if delta.Type == message.StreamDeltaRollback {
				rollbacks++
			}
		},
		false,
		nil,
		0, // default: retry until success
		&CallStatus{},
	)
	if err != nil {
		t.Fatalf("completeStreamWithRetry err = %v, want the silent retry to recover the turn", err)
	}
	if resp == nil || resp.Content != "the answer" {
		t.Fatalf("response = %#v, want the retried attempt's reply", resp)
	}
	if got := impl.CallCount(); got != 2 {
		t.Fatalf("provider calls = %d, want 2 (interrupted attempt + silent retry)", got)
	}
	if rollbacks != 1 {
		t.Fatalf("rollback deltas = %d, want 1 (the thinking-only output is rolled back once the retry produces text)", rollbacks)
	}
}

func TestCompleteStreamApplies429PacingAfterVisibleOutput(t *testing.T) {
	cfg := NewProviderConfig("sample", config.ProviderConfig{
		Type:         config.ProviderTypeChatCompletions,
		RetryBackoff: config.RetryBackoffFixed,
		RetryDelayMS: new(config.MaxProviderRetryDelayMS),
		Models: map[string]config.ModelConfig{
			"test-model": {Limit: config.ModelLimit{Context: 128000, Output: 4096}},
		},
	}, []string{"k1"})
	impl := &recordingProvider{calls: []scriptedCall{
		{
			streams: []message.StreamDelta{{Type: message.StreamDeltaText, Text: "partial"}},
			err:     &APIError{StatusCode: 429, Message: "rate limited"},
		},
	}}
	c := NewClient(cfg, impl, "test-model", 4096, "sys")
	c.SetStreamRetryRounds(1)

	_, err := c.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "hi"}}, nil, func(message.StreamDelta) {})
	if err == nil {
		t.Fatal("CompleteStream() error = nil, want capped 429 failure")
	}
	cfg.mu.Lock()
	end := cfg.keyStates[0].CooldownEnd
	cfg.mu.Unlock()
	remain := time.Until(end)
	if remain < 55*time.Second || remain > 61*time.Second {
		t.Fatalf("cooldown remaining = %v, want configured ~1m after visible hint-less 429", remain)
	}
}

func TestCompleteStreamThirdParty429PacingAfterVisibleOutputCapsRetryAfter(t *testing.T) {
	cfg := NewProviderConfig("sample", config.ProviderConfig{
		Type:         config.ProviderTypeChatCompletions,
		RetryBackoff: config.RetryBackoffFixed,
		RetryDelayMS: new(config.MaxProviderRetryDelayMS),
		Models: map[string]config.ModelConfig{
			"test-model": {Limit: config.ModelLimit{Context: 128000, Output: 4096}},
		},
	}, []string{"k1"})
	impl := &recordingProvider{calls: []scriptedCall{
		{
			streams: []message.StreamDelta{{Type: message.StreamDeltaText, Text: "partial"}},
			err:     &APIError{StatusCode: 429, Message: "rate limited", RetryAfter: 5 * time.Minute},
		},
	}}
	c := NewClient(cfg, impl, "test-model", 4096, "sys")
	c.SetStreamRetryRounds(1)

	_, err := c.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "hi"}}, nil, func(message.StreamDelta) {})
	if err == nil {
		t.Fatal("CompleteStream() error = nil, want capped 429 failure")
	}
	cfg.mu.Lock()
	end := cfg.keyStates[0].CooldownEnd
	cfg.mu.Unlock()
	remain := time.Until(end)
	if remain < 55*time.Second || remain > 61*time.Second {
		t.Fatalf("cooldown remaining = %v, want ~1m (third-party Retry-After capped at the default retry_after_max_s)", remain)
	}
}

// TestClientCompleteStreamVisibleInterruptionEscalates guards that a visible
// stream interruption escalates to the caller (which saves the partial text
// and resumes it with a continuation prompt) instead of silently retrying the
// same key and regenerating the whole reply.
func TestClientCompleteStreamVisibleInterruptionEscalates(t *testing.T) {
	primaryCfg := testProviderConfigWithKeys("primary-prov", "gpt-test", []string{"k1", "k2"})
	impl := &recordingProvider{}
	impl.calls = []scriptedCall{
		{streams: []message.StreamDelta{{Type: "text", Text: "partial"}}, err: io.ErrUnexpectedEOF},
		{resp: &message.Response{Content: "ok from same key"}},
	}
	c := NewClient(primaryCfg, impl, "gpt-test", 4096, "sys")

	resp, err := c.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "hi"}}, nil, nil)
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("CompleteStream err = %v, want io.ErrUnexpectedEOF escalated for continuation", err)
	}
	if resp != nil {
		t.Fatalf("unexpected response: %#v", resp)
	}
	if len(impl.apiKeys) != 1 {
		t.Fatalf("provider calls = %d, want 1 (the interrupted attempt only; no silent in-client retry)", len(impl.apiKeys))
	}
	if impl.apiKeys[0] != "k1" {
		t.Fatalf("first call key = %q, want k1", impl.apiKeys[0])
	}
}

func TestCompleteStreamWithRetryDoesNotResetRetryCountAfterVisibleOutputOnly(t *testing.T) {
	primaryCfg := testProviderConfigWithKeys("primary-prov", "gpt-test", []string{"k1"})
	impl := &recordingProvider{}
	impl.calls = []scriptedCall{
		{streams: []message.StreamDelta{{Type: "text", Text: "partial"}}, err: io.ErrUnexpectedEOF},
		{resp: &message.Response{Content: "should not be reached"}},
	}
	c := NewClient(primaryCfg, impl, "gpt-test", 4096, "sys")

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
		nil,
		false,
		nil,
		1,
		&CallStatus{},
	)
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("completeStreamWithRetry err = %v, want io.ErrUnexpectedEOF", err)
	}
	if resp != nil {
		t.Fatalf("unexpected response: %#v", resp)
	}
	if got := impl.CallCount(); got != 1 {
		t.Fatalf("provider calls = %d, want 1 when visible output does not reset retry budget", got)
	}
}

func TestCompleteStreamWithRetryEscalatesVisibleInterruptedStream(t *testing.T) {
	primaryCfg := testProviderConfigWithKeys("primary-prov", "gpt-test", []string{"k1"})
	impl := &recordingProvider{}
	impl.calls = []scriptedCall{
		{streams: []message.StreamDelta{{Type: "text", Text: "partial"}}, err: io.ErrUnexpectedEOF},
		{resp: &message.Response{Content: " continued", StopReason: "stop"}},
	}
	c := NewClient(primaryCfg, impl, "gpt-test", 4096, "sys")

	var rollbacks int
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
			if delta.Type == message.StreamDeltaRollback {
				rollbacks++
			}
		},
		false,
		nil,
		0,
		&CallStatus{},
	)
	// Visible interruption escalates to the caller (which saves the partial
	// text and resumes it) instead of silently regenerating in-client; no
	// rollback wipes the preserved partial from the screen.
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("completeStreamWithRetry err = %v, want io.ErrUnexpectedEOF escalated for continuation", err)
	}
	if resp != nil {
		t.Fatalf("resp = %#v, want nil (no silent in-client retry)", resp)
	}
	if rollbacks != 0 {
		t.Fatalf("rollback deltas = %d, want 0 (interrupted partial stays on screen)", rollbacks)
	}
	if got := impl.CallCount(); got != 1 {
		t.Fatalf("provider calls = %d, want 1 (the interrupted attempt only)", got)
	}
}

func TestCompleteStreamWithRetryRollsBackVisibleTextOnRequestRejection(t *testing.T) {
	primaryCfg := testProviderConfigWithKeys("primary-prov", "gpt-test", []string{"k1", "k2"})
	impl := &recordingProvider{}
	impl.calls = []scriptedCall{
		{streams: []message.StreamDelta{{Type: "text", Text: "partial"}}, err: &APIError{StatusCode: 401, Message: "invalid key"}},
		{resp: &message.Response{Content: "recovered", StopReason: "stop"}},
	}
	c := NewClient(primaryCfg, impl, "gpt-test", 4096, "sys")

	var rollbacks int
	if _, err := callCompleteStreamWithRetryForTest(
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
			if delta.Type == message.StreamDeltaRollback {
				rollbacks++
			}
		},
		false,
		nil,
		0,
		&CallStatus{},
	); err != nil {
		t.Fatalf("completeStreamWithRetry err = %v, want success on the second key", err)
	}
	if rollbacks != 1 {
		t.Fatalf("rollback deltas = %d, want 1 (rejected request output is discarded)", rollbacks)
	}
}

func TestClientRetryEmitsRollbackAfterVisibleToolStream(t *testing.T) {
	primaryCfg := testProviderConfigWithKeys("primary-prov", "primary-model", []string{"k1", "k2"})
	primaryImpl := &scriptedProvider{
		calls: []scriptedCall{
			{
				streams: []message.StreamDelta{{
					Type:     "tool_use_start",
					ToolCall: &message.ToolCallDelta{ID: "call-1", Name: "Read"},
				}},
				err: testNetTimeoutErr{timeout: true},
			},
			{
				resp: &message.Response{Content: "same key ok"},
			},
		},
	}

	c := NewClient(primaryCfg, primaryImpl, "primary-model", 4096, "sys")

	var deltas []message.StreamDelta
	resp, err := c.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "hi"}}, nil, func(delta message.StreamDelta) {
		deltas = append(deltas, delta)
	})
	if err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}
	if resp == nil || resp.Content != "same key ok" {
		t.Fatalf("unexpected response: %#v", resp)
	}

	var sawToolStart, sawRollback bool
	for _, d := range deltas {
		if d.Type == "tool_use_start" {
			sawToolStart = true
		}
		if d.Type == "rollback" {
			sawRollback = true
		}
	}
	if !sawToolStart {
		t.Fatal("expected tool_use_start before retry")
	}
	if !sawRollback {
		t.Fatal("expected rollback after visible failed attempt")
	}
}

func TestClientVisibleToolStreamRetriesSameKey(t *testing.T) {
	primaryCfg := testProviderConfigWithKeys("primary-prov", "primary-model", []string{"k1", "k2"})
	impl := &recordingProvider{}
	impl.calls = []scriptedCall{
		{
			streams: []message.StreamDelta{{
				Type:     "tool_use_start",
				ToolCall: &message.ToolCallDelta{ID: "call-1", Name: "Read"},
			}},
			err: testNetTimeoutErr{timeout: true},
		},
		{
			resp: &message.Response{Content: "ok from same key"},
		},
	}
	c := NewClient(primaryCfg, impl, "primary-model", 4096, "sys")

	var deltas []message.StreamDelta
	resp, err := c.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "hi"}}, nil, func(delta message.StreamDelta) {
		deltas = append(deltas, delta)
	})
	if err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}
	if resp == nil || resp.Content != "ok from same key" {
		t.Fatalf("unexpected response: %#v", resp)
	}
	if len(impl.apiKeys) != 2 {
		t.Fatalf("expected 2 provider calls, got %d", len(impl.apiKeys))
	}
	if impl.apiKeys[0] != "k1" || impl.apiKeys[1] != "k1" {
		t.Fatalf("expected same key retry after visible tool interruption, got %#v", impl.apiKeys)
	}

	var sawToolStart, sawRollback, sawSameKeyRetry bool
	for _, d := range deltas {
		if d.Type == "tool_use_start" {
			sawToolStart = true
		}
		if d.Type == "rollback" {
			sawRollback = true
		}
		if d.Type == "status" && d.Status != nil && d.Status.Type == "retrying" && d.Status.Detail == "same key" {
			sawSameKeyRetry = true
		}
	}
	if !sawToolStart {
		t.Fatal("expected tool_use_start before retry")
	}
	if !sawRollback {
		t.Fatal("expected rollback after visible failed attempt")
	}
	if !sawSameKeyRetry {
		t.Fatal("expected same-key retry status after visible tool interruption")
	}
	healthy, total := primaryCfg.HealthyKeyCount()
	if total != 2 || healthy != 2 {
		t.Fatalf("HealthyKeyCount = %d/%d, want 2/2 after same-key recovery", healthy, total)
	}
}

func TestClientVisibleRollbackCancelStopsSameKeyRetry(t *testing.T) {
	cfg := testProviderConfigWithKeys("primary-prov", "primary-model", []string{"k1"})
	impl := &recordingProvider{}
	impl.calls = []scriptedCall{
		{
			streams: []message.StreamDelta{{
				Type:     "tool_use_start",
				ToolCall: &message.ToolCallDelta{ID: "call-1", Name: "Read"},
			}},
			err: testNetTimeoutErr{timeout: true},
		},
		{
			resp: &message.Response{Content: "should not retry after cancel"},
		},
	}
	c := NewClient(cfg, impl, "primary-model", 4096, "sys")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var deltas []message.StreamDelta
	_, err := c.CompleteStream(ctx, []message.Message{{Role: "user", Content: "hi"}}, nil, func(delta message.StreamDelta) {
		deltas = append(deltas, delta)
		if delta.Type == "rollback" {
			cancel()
		}
	})
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("CompleteStream err = %v, want context.Canceled", err)
	}
	if impl.CallCount() != 1 {
		t.Fatalf("provider CallCount=%d, want 1 (no same-key retry after cancel)", impl.CallCount())
	}
	for _, d := range deltas {
		if d.Type == "status" && d.Status != nil && d.Status.Type == "retrying" && d.Status.Detail == "same key" {
			t.Fatal("unexpected same-key retry status after cancellation")
		}
	}
}
