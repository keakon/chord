package llm

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
)

// TestProviderConfig_KeylessTransportCooldownStillThrottles covers providers
// configured without an API key (local gateways, public endpoints). Their
// selected credential is the empty string, so a key-scoped cooldown is a no-op
// and the caller would restart a preserved interruption back-to-back with no
// wait at all.
func TestProviderConfig_KeylessTransportCooldownStillThrottles(t *testing.T) {
	cfg := testProviderConfigWithKeys("local", "llama", nil)
	if len(cfg.keyStates) != 0 {
		t.Fatalf("keyStates = %d, want a keyless provider for this test", len(cfg.keyStates))
	}
	if _, _, err := cfg.SelectKeyWithContext(context.Background()); err != nil {
		t.Fatalf("baseline SelectKeyWithContext err = %v, want nil", err)
	}
	cfg.MarkTransportCooldown("", preservedInterruptionCooldownBase, preservedInterruptionCooldownMax)
	_, _, err := cfg.SelectKeyWithContext(context.Background())
	cooling, ok := errors.AsType[*AllKeysCoolingError](err)
	if !ok {
		t.Fatalf("SelectKeyWithContext err = %v, want AllKeysCoolingError while the provider is cooling", err)
	}
	if cooling.RetryAfter <= 0 {
		t.Fatalf("RetryAfter = %v, want a positive wait", cooling.RetryAfter)
	}
	first := cooling.RetryAfter
	cfg.MarkTransportCooldown("", preservedInterruptionCooldownBase, preservedInterruptionCooldownMax)
	_, _, err = cfg.SelectKeyWithContext(context.Background())
	cooling, ok = errors.AsType[*AllKeysCoolingError](err)
	if !ok {
		t.Fatalf("second SelectKeyWithContext err = %v, want AllKeysCoolingError", err)
	}
	if cooling.RetryAfter <= first {
		t.Fatalf("keyless cooldown did not grow: %v then %v", first, cooling.RetryAfter)
	}
	// A completed reply clears the backoff. With no second credential to fall
	// back to, holding the door shut after the endpoint proved healthy would
	// only stall the next request.
	cfg.ClearTransportCooldown("")
	if _, _, err := cfg.SelectKeyWithContext(context.Background()); err != nil {
		t.Fatalf("after success SelectKeyWithContext err = %v, want nil", err)
	}
}

func TestMarkKeyCooldownAPIStatusPolicies(t *testing.T) {
	ctx := context.Background()
	err429 := &APIError{StatusCode: 429, Message: "rate limited"}

	t.Run("403_global_quota_uses_retry_after", func(t *testing.T) {
		p := NewProviderConfig("p", config.ProviderConfig{Type: config.ProviderTypeResponses}, []string{"k1"})
		res := markKeyCooldown(ctx, p, "k1", "test-model", &APIError{
			StatusCode: 403, Code: "global_fixed_window_quota_exhausted",
			Message: "provider quota exhausted", RetryAfter: 2 * time.Minute,
		})
		if !res.cooldownApplied {
			t.Fatal("expected cooldownApplied=true for global quota 403")
		}
		p.mu.Lock()
		end := p.keyStates[0].ExhaustedUntil
		p.mu.Unlock()
		remain := time.Until(end)
		if remain < 55*time.Second || remain > 65*time.Second {
			t.Fatalf("expected ~1m capped cooldown from Retry-After, got remaining %v", remain)
		}
	})

	t.Run("unconfigured_429_without_retry_after_uses_default_exponential", func(t *testing.T) {
		p := NewProviderConfig("p", config.ProviderConfig{Type: config.ProviderTypeChatCompletions}, []string{"k1"})
		for attempt, want := range []time.Duration{time.Second, 2 * time.Second} {
			res := markKeyCooldown(ctx, p, "k1", "test-model", err429)
			if !res.cooldownApplied {
				t.Fatal("expected cooldownApplied=true for 429")
			}
			p.mu.Lock()
			end := p.keyStates[0].CooldownEnd
			count := p.keyStates[0].CooldownCount
			p.mu.Unlock()
			remain := time.Until(end)
			if remain < want-500*time.Millisecond || remain > want+500*time.Millisecond {
				t.Fatalf("attempt %d cooldown remaining = %v, want ~%v", attempt+1, remain, want)
			}
			if count != attempt+1 {
				t.Fatalf("attempt %d CooldownCount = %d, want %d", attempt+1, count, attempt+1)
			}
		}
	})

	t.Run("unconfigured_429_preserves_full_retry_after", func(t *testing.T) {
		p := NewProviderConfig("p", config.ProviderConfig{
			Type:           config.ProviderTypeChatCompletions,
			RetryAfterMaxS: new(config.OfficialRetryAfterMaxS),
		}, []string{"k1"})
		for attempt := range 2 {
			res := markKeyCooldown(ctx, p, "k1", "test-model", &APIError{StatusCode: 429, Message: "rl", RetryAfter: 2 * time.Minute})
			if !res.cooldownApplied {
				t.Fatal("expected cooldownApplied=true for Retry-After 429")
			}
			p.mu.Lock()
			end := p.keyStates[0].CooldownEnd
			count := p.keyStates[0].CooldownCount
			p.mu.Unlock()
			remain := time.Until(end)
			if remain < 115*time.Second || remain > 125*time.Second {
				t.Fatalf("attempt %d cooldown remaining = %v, want full ~2m trusted Retry-After", attempt+1, remain)
			}
			if count != attempt+1 {
				t.Fatalf("attempt %d CooldownCount = %d, want %d", attempt+1, count, attempt+1)
			}
		}
	})

	t.Run("third_party_429_caps_retry_after_at_one_minute", func(t *testing.T) {
		p := testCompatibleResponsesProviderConfigWithKeys("gateway", "gpt-test", []string{"k1"})
		for attempt := range 2 {
			res := markKeyCooldown(ctx, p, "k1", "test-model", &APIError{StatusCode: 429, Message: "rl", RetryAfter: 2 * time.Minute})
			if !res.cooldownApplied {
				t.Fatal("expected cooldownApplied=true for third-party 429")
			}
			p.mu.Lock()
			end := p.keyStates[0].CooldownEnd
			count := p.keyStates[0].CooldownCount
			p.mu.Unlock()
			remain := time.Until(end)
			if remain < 55*time.Second || remain > 65*time.Second {
				t.Fatalf("attempt %d cooldown remaining = %v, want capped ~1m third-party Retry-After", attempt+1, remain)
			}
			if count != attempt+1 {
				t.Fatalf("attempt %d CooldownCount = %d, want %d", attempt+1, count, attempt+1)
			}
		}
	})

	t.Run("explicit_delay_only_429_paces_when_no_retry_after", func(t *testing.T) {
		p := NewProviderConfig("p", config.ProviderConfig{
			Type:         config.ProviderTypeChatCompletions,
			RetryDelayMS: new(2500),
		}, []string{"k1"})
		res := markKeyCooldown(ctx, p, "k1", "test-model", err429)
		if !res.cooldownApplied {
			t.Fatal("expected explicit retry_delay_ms cooldown for 429")
		}
		p.mu.Lock()
		end := p.keyStates[0].CooldownEnd
		p.mu.Unlock()
		remain := time.Until(end)
		if remain < 2*time.Second || remain > 3*time.Second {
			t.Fatalf("cooldown remaining = %v, want ~2.5s", remain)
		}
	})

	t.Run("explicit_zero_delay_429_uses_default_when_no_retry_after", func(t *testing.T) {
		p := NewProviderConfig("p", config.ProviderConfig{
			Type:         config.ProviderTypeChatCompletions,
			RetryDelayMS: new(0),
		}, []string{"k1"})
		res := markKeyCooldown(ctx, p, "k1", "test-model", err429)
		if !res.cooldownApplied {
			t.Fatal("expected explicit zero retry_delay_ms cooldown for 429")
		}
		p.mu.Lock()
		end := p.keyStates[0].CooldownEnd
		p.mu.Unlock()
		remain := time.Until(end)
		if remain < 500*time.Millisecond || remain > 1500*time.Millisecond {
			t.Fatalf("cooldown remaining = %v, want explicit default ~1s", remain)
		}
	})

	t.Run("explicit_exponential_429_paces_when_no_retry_after", func(t *testing.T) {
		p := NewProviderConfig("p", config.ProviderConfig{
			Type:         config.ProviderTypeChatCompletions,
			RetryBackoff: config.RetryBackoffExponential,
			RetryDelayMS: new(2000),
		}, []string{"k1"})
		for attempt, want := range []time.Duration{2 * time.Second, 4 * time.Second} {
			res := markKeyCooldown(ctx, p, "k1", "test-model", err429)
			if !res.cooldownApplied {
				t.Fatal("expected configured exponential cooldown for 429")
			}
			p.mu.Lock()
			end := p.keyStates[0].CooldownEnd
			p.mu.Unlock()
			remain := time.Until(end)
			if remain < want-500*time.Millisecond || remain > want+500*time.Millisecond {
				t.Fatalf("attempt %d cooldown remaining = %v, want ~%v", attempt+1, remain, want)
			}
		}
	})

	t.Run("explicit_fixed_429_paces_when_no_retry_after_without_doubling", func(t *testing.T) {
		p := NewProviderConfig("p", config.ProviderConfig{
			Type:         config.ProviderTypeChatCompletions,
			RetryBackoff: config.RetryBackoffFixed,
			RetryDelayMS: new(3000),
		}, []string{"k1"})
		for attempt := range 2 {
			res := markKeyCooldown(ctx, p, "k1", "test-model", err429)
			if !res.cooldownApplied {
				t.Fatal("expected configured fixed cooldown for 429")
			}
			p.mu.Lock()
			end := p.keyStates[0].CooldownEnd
			count := p.keyStates[0].CooldownCount
			p.mu.Unlock()
			remain := time.Until(end)
			if remain < 2500*time.Millisecond || remain > 3500*time.Millisecond {
				t.Fatalf("attempt %d cooldown remaining = %v, want ~3s", attempt+1, remain)
			}
			if count != attempt+1 {
				t.Fatalf("attempt %d CooldownCount = %d, want %d", attempt+1, count, attempt+1)
			}
		}
	})

	t.Run("explicit_none_429_still_honors_retry_after", func(t *testing.T) {
		p := NewProviderConfig("p", config.ProviderConfig{
			Type:         config.ProviderTypeChatCompletions,
			RetryBackoff: config.RetryBackoffNone,
		}, []string{"k1", "k2"})
		res := markKeyCooldown(ctx, p, "k1", "test-model", &APIError{StatusCode: 429, Message: "rl", RetryAfter: 30 * time.Second})
		if !res.cooldownApplied {
			t.Fatal("server-directed Retry-After must apply even under retry_backoff=none")
		}
		p.mu.Lock()
		end := p.keyStates[0].CooldownEnd
		p.mu.Unlock()
		if remain := time.Until(end); remain < 25*time.Second || remain > 31*time.Second {
			t.Fatalf("cooldown remaining = %v, want ~30s verbatim Retry-After", remain)
		}
		key, _, err := p.SelectKeyWithContext(ctx)
		if err != nil {
			t.Fatalf("SelectKeyWithContext() error = %v", err)
		}
		if key != "k2" {
			t.Fatalf("selected key = %q, want healthy alternative k2", key)
		}
	})

	t.Run("explicit_none_429_without_retry_after_marks_recovering", func(t *testing.T) {
		p := NewProviderConfig("p", config.ProviderConfig{
			Type:         config.ProviderTypeChatCompletions,
			RetryBackoff: config.RetryBackoffNone,
		}, []string{"k1", "k2"})
		res := markKeyCooldown(ctx, p, "k1", "test-model", err429)
		if res.cooldownApplied {
			t.Fatal("expected retry_backoff=none to skip timed 429 cooldown")
		}
		p.mu.Lock()
		ks := *p.keyStates[0]
		p.mu.Unlock()
		if !ks.CooldownEnd.IsZero() || ks.CooldownCount != 0 || !ks.Recovering {
			t.Fatalf("key state = cooldown_end=%v count=%d recovering=%v, want no timed cooldown and recovering", ks.CooldownEnd, ks.CooldownCount, ks.Recovering)
		}
		key, _, err := p.SelectKeyWithContext(ctx)
		if err != nil {
			t.Fatalf("SelectKeyWithContext() error = %v", err)
		}
		if key != "k2" {
			t.Fatalf("selected key = %q, want healthy alternative k2", key)
		}
	})

	t.Run("explicit_none_429_does_not_clear_existing_cooldown", func(t *testing.T) {
		p := NewProviderConfig("p", config.ProviderConfig{
			Type:         config.ProviderTypeChatCompletions,
			RetryBackoff: config.RetryBackoffNone,
		}, []string{"k1"})
		p.MarkCooldown("k1", time.Minute)
		p.mu.Lock()
		before := p.keyStates[0].CooldownEnd
		beforeCount := p.keyStates[0].CooldownCount
		p.mu.Unlock()

		res := markKeyCooldown(ctx, p, "k1", "test-model", err429)
		if res.cooldownApplied {
			t.Fatal("expected retry_backoff=none not to add a timed 429 cooldown")
		}
		p.mu.Lock()
		after := p.keyStates[0].CooldownEnd
		afterCount := p.keyStates[0].CooldownCount
		p.mu.Unlock()
		if !after.Equal(before) || afterCount != beforeCount {
			t.Fatalf("existing cooldown changed from %v/%d to %v/%d", before, beforeCount, after, afterCount)
		}
	})

	t.Run("explicit_fixed_429_does_not_shorten_existing_cooldown", func(t *testing.T) {
		p := NewProviderConfig("p", config.ProviderConfig{
			Type:         config.ProviderTypeChatCompletions,
			RetryBackoff: config.RetryBackoffFixed,
			RetryDelayMS: new(2000),
		}, []string{"k1"})
		p.MarkCooldown("k1", time.Minute)
		p.mu.Lock()
		before := p.keyStates[0].CooldownEnd
		p.mu.Unlock()

		res := markKeyCooldown(ctx, p, "k1", "test-model", &APIError{StatusCode: 429, Message: "rl", RetryAfter: time.Millisecond})
		if !res.cooldownApplied {
			t.Fatal("expected fixed 429 pacing to be applied")
		}
		p.mu.Lock()
		after := p.keyStates[0].CooldownEnd
		p.mu.Unlock()
		if after.Before(before) {
			t.Fatalf("existing cooldown shortened from %v to %v", before, after)
		}
	})

	t.Run("402_uses_same_cooldown_path", func(t *testing.T) {
		p := NewProviderConfig("p", config.ProviderConfig{Type: config.ProviderTypeChatCompletions}, []string{"k1"})
		res := markKeyCooldown(ctx, p, "k1", "test-model", &APIError{StatusCode: 402, Message: "quota exhausted; retry later"})
		if !res.cooldownApplied {
			t.Fatal("expected cooldownApplied=true for 402 quota exhaustion")
		}
		p.mu.Lock()
		end := p.keyStates[0].CooldownEnd
		p.mu.Unlock()
		remain := time.Until(end)
		if remain < 500*time.Millisecond || remain > 1500*time.Millisecond {
			t.Fatalf("expected ~1s cooldown for 402 without Retry-After, got remaining %v", remain)
		}
	})

	t.Run("compatible_400_without_retry_after_uses_short_probe_cooldown", func(t *testing.T) {
		p := testCompatibleResponsesProviderConfigWithKeys("gateway", "gpt-test", []string{"k1"})
		res := markKeyCooldown(ctx, p, "k1", "test-model", &APIError{StatusCode: 400, Message: "Concurrency limit exceeded for user, please retry later"})
		if !res.cooldownApplied {
			t.Fatal("expected cooldownApplied=true for retriable compatible 400")
		}
		p.mu.Lock()
		end := p.keyStates[0].CooldownEnd
		p.mu.Unlock()
		remain := time.Until(end)
		if remain < 500*time.Millisecond || remain > 1500*time.Millisecond {
			t.Fatalf("expected ~1s cooldown for compatible 400 without Retry-After, got remaining %v", remain)
		}
	})

	t.Run("402_deactivated_workspace_code_deactivates_oauth_key", func(t *testing.T) {
		p := NewProviderConfig("p", config.ProviderConfig{Type: config.ProviderTypeResponses, Preset: config.ProviderPresetCodex}, []string{"oauth-key"})
		p.mu.Lock()
		p.keyStates[0].OAuthInfo = &OAuthKeyInfo{Expires: time.Now().Add(time.Hour).UnixMilli(), AccountID: "acc-1", Email: "user@example.com"}
		p.mu.Unlock()

		res := markKeyCooldown(ctx, p, "oauth-key", "test-model", &APIError{StatusCode: 402, Code: "deactivated_workspace", Message: `{"detail":{"code":"deactivated_workspace"}}`})
		if !res.cooldownApplied {
			t.Fatal("expected cooldownApplied=true")
		}
		if res.deactivatedAccountID != "acc-1" || res.deactivatedEmail != "user@example.com" {
			t.Fatalf("deactivated identity = %q/%q, want acc-1/user@example.com", res.deactivatedAccountID, res.deactivatedEmail)
		}
		_, total := p.AvailableKeyCount()
		if total != 0 {
			t.Fatalf("total = %d, want 0: deactivated OAuth key should be excluded", total)
		}
	})

	t.Run("402_deactivated_workspace_message_deactivates_oauth_key", func(t *testing.T) {
		p := NewProviderConfig("p", config.ProviderConfig{Type: config.ProviderTypeResponses, Preset: config.ProviderPresetCodex}, []string{"oauth-key"})
		p.mu.Lock()
		p.keyStates[0].OAuthInfo = &OAuthKeyInfo{Expires: time.Now().Add(time.Hour).UnixMilli(), AccountID: "acc-1"}
		p.mu.Unlock()

		res := markKeyCooldown(ctx, p, "oauth-key", "test-model", &APIError{StatusCode: 402, Message: `{"detail":{"code":"deactivated_workspace"}}`})
		if !res.cooldownApplied {
			t.Fatal("expected cooldownApplied=true")
		}
		if res.deactivatedAccountID != "acc-1" {
			t.Fatalf("deactivatedAccountID = %q, want acc-1", res.deactivatedAccountID)
		}
		_, total := p.AvailableKeyCount()
		if total != 0 {
			t.Fatalf("total = %d, want 0: deactivated OAuth key should be excluded", total)
		}
	})

	t.Run("explicit_pacing_429_still_honors_retry_after", func(t *testing.T) {
		p := NewProviderConfig("p", config.ProviderConfig{
			Type:         config.ProviderTypeChatCompletions,
			RetryBackoff: config.RetryBackoffFixed,
			RetryDelayMS: new(5000),
		}, []string{"k1"})
		res := markKeyCooldown(ctx, p, "k1", "test-model", &APIError{StatusCode: 429, Message: "rl", RetryAfter: 30 * time.Second})
		if !res.cooldownApplied {
			t.Fatal("expected Retry-After cooldown despite explicit pacing")
		}
		p.mu.Lock()
		end := p.keyStates[0].CooldownEnd
		p.mu.Unlock()
		if remain := time.Until(end); remain < 25*time.Second || remain > 31*time.Second {
			t.Fatalf("cooldown remaining = %v, want ~30s: server-directed Retry-After outranks retry_backoff", remain)
		}
	})

	t.Run("402_honors_long_retry_after_verbatim_without_60s_cap", func(t *testing.T) {
		p := NewProviderConfig("p", config.ProviderConfig{
			Type:           config.ProviderTypeChatCompletions,
			RetryAfterMaxS: new(config.OfficialRetryAfterMaxS),
		}, []string{"k1"})
		for attempt := range 2 {
			res := markKeyCooldown(ctx, p, "k1", "test-model", &APIError{StatusCode: 402, Message: "quota", RetryAfter: 2 * time.Hour})
			if !res.cooldownApplied {
				t.Fatal("expected cooldownApplied=true for 402 with Retry-After")
			}
			p.mu.Lock()
			end := p.keyStates[0].CooldownEnd
			p.mu.Unlock()
			remain := time.Until(end)
			if remain < 2*time.Hour-time.Minute || remain > 2*time.Hour+time.Minute {
				t.Fatalf("attempt %d cooldown remaining = %v, want verbatim ~2h (no exponential 60s cap)", attempt+1, remain)
			}
		}
	})

	t.Run("retry_after_max_s_explicit_cap_bounds_the_hint", func(t *testing.T) {
		p := NewProviderConfig("p", config.ProviderConfig{
			Type:           config.ProviderTypeChatCompletions,
			RetryAfterMaxS: new(120),
		}, []string{"k1"})
		res := markKeyCooldown(ctx, p, "k1", "test-model", &APIError{StatusCode: 429, Message: "rl", RetryAfter: 5 * time.Minute})
		if !res.cooldownApplied {
			t.Fatal("expected cooldownApplied=true for capped 429")
		}
		p.mu.Lock()
		end := p.keyStates[0].CooldownEnd
		p.mu.Unlock()
		if remain := time.Until(end); remain < 115*time.Second || remain > 125*time.Second {
			t.Fatalf("cooldown remaining = %v, want ~2m explicit retry_after_max_s cap", remain)
		}
	})

}

func TestClientCompleteEmptyResponseMarksKeyRecovering(t *testing.T) {
	primaryCfg := testProviderConfigWithKeys("primary-prov", "primary-model", []string{"k1", "k2"})
	fallbackCfg := testProviderConfig("fallback-prov", "fallback-model")
	primaryImpl := &recordingProvider{}
	primaryImpl.calls = []scriptedCall{
		{resp: &message.Response{StopReason: "stop"}},
	}
	fallbackImpl := &recordingProvider{}
	fallbackImpl.calls = []scriptedCall{
		{resp: &message.Response{Content: "from fallback"}},
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
	key, switched, err := primaryCfg.SelectKeyWithContext(context.Background())
	if err != nil {
		t.Fatalf("SelectKeyWithContext: %v", err)
	}
	if key != "k2" {
		t.Fatalf("selected key after empty response = %q, want k2", key)
	}
	if !switched {
		t.Fatal("expected recovering empty-response key to yield a slot switch to k2")
	}
}
