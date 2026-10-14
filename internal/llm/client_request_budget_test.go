package llm

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
)

func TestClientContextLengthExceededDoesNotRetryKeysAndFallsBackWithinRound(t *testing.T) {
	primaryCfg := testProviderConfigWithKeys("primary-prov", "primary-model", []string{"k1", "k2"})
	fallbackCfg := testProviderConfig("fallback-prov", "fallback-model")

	primaryImpl := &recordingProvider{}
	primaryImpl.calls = []scriptedCall{{err: &APIError{StatusCode: 400, Code: "context_length_exceeded", Message: "input is too long"}}}
	fallbackImpl := &scriptedProvider{calls: []scriptedCall{{resp: &message.Response{Content: "ok from fallback"}}}}

	c := NewClient(primaryCfg, primaryImpl, "primary-model", 4096, "sys")
	c.SetFallbackModels([]FallbackModel{{
		ProviderConfig: fallbackCfg,
		ProviderImpl:   fallbackImpl,
		ModelID:        "fallback-model",
		MaxTokens:      4096,
		ContextLimit:   128000,
		InputLimit:     128000,
	}})

	resp, err := c.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}
	if resp == nil || resp.Content != "ok from fallback" {
		t.Fatalf("unexpected response: %#v", resp)
	}
	if got := primaryImpl.CallCount(); got != 1 {
		t.Fatalf("primary calls = %d, want 1 (do not retry next key on oversize)", got)
	}
	if len(primaryImpl.apiKeys) != 1 || primaryImpl.apiKeys[0] != "k1" {
		t.Fatalf("primary keys = %#v, want only first key tried", primaryImpl.apiKeys)
	}
}

func TestClientContextLengthExceededAfterVisibleOutputStillEmitsRetryError(t *testing.T) {
	primaryCfg := testProviderConfigWithKeys("primary-prov", "primary-model", []string{"k1", "k2"})
	fallbackCfg := testProviderConfig("fallback-prov", "fallback-model")

	primaryImpl := &recordingProvider{}
	primaryImpl.calls = []scriptedCall{{
		streams: []message.StreamDelta{{Type: message.StreamDeltaText, Text: "partial"}},
		err:     &APIError{StatusCode: 400, Code: "context_length_exceeded", Message: "input is too long"},
	}}
	fallbackImpl := &scriptedProvider{calls: []scriptedCall{{resp: &message.Response{Content: "ok from fallback"}}}}

	c := NewClient(primaryCfg, primaryImpl, "primary-model", 4096, "sys")
	c.SetFallbackModels([]FallbackModel{{
		ProviderConfig: fallbackCfg,
		ProviderImpl:   fallbackImpl,
		ModelID:        "fallback-model",
		MaxTokens:      4096,
		ContextLimit:   128000,
		InputLimit:     128000,
	}})

	var deltas []message.StreamDelta
	resp, err := c.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "hi"}}, nil, func(delta message.StreamDelta) {
		deltas = append(deltas, delta)
	})
	if err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}
	if resp == nil || resp.Content != "ok from fallback" {
		t.Fatalf("unexpected response: %#v", resp)
	}

	var sawText, sawRollback, sawRetryError bool
	for _, d := range deltas {
		switch d.Type {
		case message.StreamDeltaText:
			sawText = true
		case message.StreamDeltaRollback:
			sawRollback = true
		case message.StreamDeltaRetryError:
			sawRetryError = true
		}
	}
	if !sawText {
		t.Fatal("expected visible text before the context-length failure")
	}
	if !sawRollback {
		t.Fatal("expected rollback after visible failed attempt")
	}
	if !sawRetryError {
		t.Fatal("expected retry error record for context-length failure after visible output")
	}
	if got := primaryImpl.CallCount(); got != 1 {
		t.Fatalf("primary calls = %d, want 1 (do not retry next key on oversize)", got)
	}
	if len(primaryImpl.apiKeys) != 1 || primaryImpl.apiKeys[0] != "k1" {
		t.Fatalf("primary keys = %#v, want only first key tried", primaryImpl.apiKeys)
	}
}

func TestClientSkipsRequestWhenInputFillsContextWindow(t *testing.T) {
	cfg := NewProviderConfig("provider", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"small-model": {Limit: config.ModelLimit{Context: 1000, Output: 256}},
		},
	}, []string{"test-key"})
	impl := &recordingProvider{}
	c := NewClient(cfg, impl, "small-model", 256, "sys")
	// A provider-confirmed prompt size from an earlier response makes the local
	// admission gate active for this session.
	c.setLastInputTokens(900)

	// ~3000 bytes ≈ 1000 tokens: the input side alone fills the 1000-token
	// context window once the provider buffer is added.
	messages := []message.Message{{Role: "user", Content: strings.Repeat("x", 3000)}}
	_, err := c.CompleteStream(context.Background(), messages, nil, nil)
	if err == nil {
		t.Fatal("CompleteStream err = nil, want oversize error")
	}
	if !IsAllAttemptedCandidatesContextLengthExceeded(err) {
		t.Fatalf("err = %v, want all-attempted-candidates context length exceeded", err)
	}
	if got := impl.CallCount(); got != 0 {
		t.Fatalf("provider calls = %d, want 0: a request the runtime knows cannot fit must not reach the provider", got)
	}
}

func TestClientContextLengthExceededStopsAfterPoolExhaustedWithoutNextRound(t *testing.T) {
	primaryCfg := testProviderConfigWithKeys("primary-prov", "primary-model", []string{"k1", "k2"})
	fallbackCfg := testProviderConfig("fallback-prov", "fallback-model")

	primaryImpl := &recordingProvider{}
	primaryImpl.calls = []scriptedCall{{err: &APIError{StatusCode: 400, Code: "context_length_exceeded", Message: "input is too long"}}}
	fallbackImpl := &scriptedProvider{calls: []scriptedCall{{err: &APIError{StatusCode: 400, Code: "context_length_exceeded", Message: "prompt is too long"}}}}

	c := NewClient(primaryCfg, primaryImpl, "primary-model", 4096, "sys")
	c.SetFallbackModels([]FallbackModel{{
		ProviderConfig: fallbackCfg,
		ProviderImpl:   fallbackImpl,
		ModelID:        "fallback-model",
		MaxTokens:      4096,
		ContextLimit:   128000,
		InputLimit:     128000,
	}})

	ctx, cancel := context.WithTimeout(context.Background(), 2500*time.Millisecond)
	defer cancel()
	_, err := c.CompleteStream(ctx, []message.Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err == nil {
		t.Fatal("CompleteStream err = nil, want oversize error")
	}
	if !IsContextLengthExceeded(err) {
		t.Fatalf("err = %v, want context length exceeded", err)
	}
	if !IsAllAttemptedCandidatesContextLengthExceeded(err) {
		t.Fatalf("err = %v, want all-attempted-candidates context length exceeded", err)
	}
	if got := primaryImpl.CallCount(); got != 1 {
		t.Fatalf("primary calls = %d, want 1", got)
	}
	if got := fallbackImpl.CallCount(); got != 1 {
		t.Fatalf("fallback calls = %d, want 1", got)
	}
}

func TestClientMixedFallbackErrorsDoNotReportAllAttemptedCandidatesContextLengthExceeded(t *testing.T) {
	primaryCfg := testProviderConfig("primary-prov", "primary-model")
	fallbackCfg := testProviderConfig("fallback-prov", "fallback-model")

	primaryImpl := &scriptedProvider{calls: []scriptedCall{{err: &APIError{StatusCode: 402, Message: "quota exhausted"}}}}
	fallbackImpl := &scriptedProvider{calls: []scriptedCall{{err: &APIError{StatusCode: 400, Code: "context_length_exceeded", Message: "prompt is too long"}}}}

	c := NewClient(primaryCfg, primaryImpl, "primary-model", 4096, "sys")
	c.SetFallbackModels([]FallbackModel{{
		ProviderConfig: fallbackCfg,
		ProviderImpl:   fallbackImpl,
		ModelID:        "fallback-model",
		MaxTokens:      4096,
		ContextLimit:   128000,
		InputLimit:     128000,
	}})

	_, err := c.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err == nil {
		t.Fatal("CompleteStream err = nil, want final fallback context-length error")
	}
	if !IsContextLengthExceeded(err) {
		t.Fatalf("err = %v, want context length exceeded from final fallback", err)
	}
	if IsAllAttemptedCandidatesContextLengthExceeded(err) {
		t.Fatalf("err = %v, did not expect all-attempted-candidates context length exceeded after mixed errors", err)
	}
	if got := primaryImpl.CallCount(); got != 1 {
		t.Fatalf("primary calls = %d, want 1", got)
	}
	if got := fallbackImpl.CallCount(); got != 1 {
		t.Fatalf("fallback calls = %d, want 1", got)
	}
}

func TestClientContextLengthExceededStillTriesSameNamedFallbackOnDifferentProvider(t *testing.T) {
	primaryCfg := testProviderConfigWithKeys("primary-prov", "openai/gpt-5.5", []string{"k1"})
	dupCfg := testProviderConfig("dup-prov", "gpt-5.5")
	okCfg := testProviderConfig("ok-prov", "claude-opus")

	primaryImpl := &recordingProvider{}
	primaryImpl.calls = []scriptedCall{{err: &APIError{StatusCode: 400, Code: "context_length_exceeded", Message: "input is too long"}}}
	dupImpl := &recordingProvider{}
	dupImpl.calls = []scriptedCall{{resp: &message.Response{Content: "ok from same-name fallback on different provider"}}}
	okImpl := &recordingProvider{}
	okImpl.calls = []scriptedCall{{resp: &message.Response{Content: "should not be used"}}}

	c := NewClient(primaryCfg, primaryImpl, "openai/gpt-5.5", 4096, "sys")
	c.SetFallbackModels([]FallbackModel{{
		ProviderConfig: dupCfg,
		ProviderImpl:   dupImpl,
		ModelID:        "gpt-5.5",
		MaxTokens:      4096,
		ContextLimit:   400000,
		InputLimit:     272000,
	}, {
		ProviderConfig: okCfg,
		ProviderImpl:   okImpl,
		ModelID:        "claude-opus",
		MaxTokens:      4096,
		ContextLimit:   200000,
		InputLimit:     200000,
	}})

	resp, err := c.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}
	if resp == nil || resp.Content != "ok from same-name fallback on different provider" {
		t.Fatalf("unexpected response: %#v", resp)
	}
	if got := dupImpl.CallCount(); got != 1 {
		t.Fatalf("same-name fallback provider calls = %d, want 1", got)
	}
	if got := okImpl.CallCount(); got != 0 {
		t.Fatalf("later distinct fallback calls = %d, want 0", got)
	}
}

func TestClampEffectiveMaxTokensUsesTotalContextForOutputClampWhenInputBudgetConfigured(t *testing.T) {
	model := config.ModelConfig{Limit: config.ModelLimit{Context: 400000, Input: 272000, Output: 128000}}
	messages := []message.Message{{Role: "user", Content: strings.Repeat("x", 720000)}} // ~240k tokens
	got, _, _ := clampEffectiveMaxTokens(model, 128000, 128000, RequestTuning{}, "", messages, nil, 0)
	if got != 128000 {
		t.Fatalf("clampEffectiveMaxTokens() = %d, want 128000", got)
	}
}

func TestClampEffectiveMaxTokensReasoningStillRespectsGlobalOutputCap(t *testing.T) {
	model := config.ModelConfig{Limit: config.ModelLimit{Context: 400000, Input: 272000, Output: 128000}}
	got, _, _ := clampEffectiveMaxTokens(model, 128000, 32000, RequestTuning{OpenAI: OpenAITuning{ReasoningEffort: "high"}}, "", []message.Message{{Role: "user", Content: "hi"}}, nil, 0)
	if got != 32000 {
		t.Fatalf("clampEffectiveMaxTokens() = %d, want 32000", got)
	}
}

func TestClampEffectiveMaxTokensUsesSmallerDefaultOrModelOutputLimit(t *testing.T) {
	cases := []struct {
		name        string
		modelOutput int
		want        int
	}{
		{name: "default bounds larger model", modelOutput: 128000, want: DefaultOutputTokenMax},
		{name: "model bounds default", modelOutput: 16000, want: 16000},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			model := config.ModelConfig{Limit: config.ModelLimit{Context: 400000, Output: tc.modelOutput}}
			got, _, _ := clampEffectiveMaxTokens(model, tc.modelOutput, 0, RequestTuning{}, "", []message.Message{{Role: "user", Content: "hi"}}, nil, 0)
			if got != tc.want {
				t.Fatalf("clampEffectiveMaxTokens() = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestInputLimitForModelRefReservesRequestedOutput(t *testing.T) {
	cfg := NewProviderConfig("prov", config.ProviderConfig{
		Type:   config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{"model": {Limit: config.ModelLimit{Context: 400000, Output: 128000}}},
	}, []string{"k"})
	c := NewClient(cfg, &scriptedProvider{}, "model", 128000, "")
	if got := c.InputLimitForModelRef("prov/model"); got != 336000 {
		t.Fatalf("default input = %d, want 336000", got)
	}
	c.SetOutputTokenMax(8192)
	if got := c.InputLimitForModelRef("prov/model"); got != 391808 {
		t.Fatalf("configured input = %d, want 391808", got)
	}
}

func TestInputLimitForModelRefFallsBackToOutputCapSetting(t *testing.T) {
	cfg := NewProviderConfig("prov", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"model": {Limit: config.ModelLimit{Context: 400000}},
		},
	}, []string{"k"})
	c := NewClient(cfg, &scriptedProvider{}, "model", 128000, "")

	// No limit.output declared: the reservation is the effective default
	// output cap and tracks the configured max_output_tokens.
	if got := c.InputLimitForModelRef("prov/model"); got != 336000 {
		t.Fatalf("default InputLimitForModelRef() = %d, want 336000", got)
	}
	c.SetOutputTokenMax(8192)
	if got := c.InputLimitForModelRef("prov/model"); got != 391808 {
		t.Fatalf("configured InputLimitForModelRef() = %d, want 391808", got)
	}
}

func TestInputLimitForModelRefUsesExplicitInputLimit(t *testing.T) {
	cfg := NewProviderConfig("prov", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"model": {Limit: config.ModelLimit{Context: 400000, Input: 272000, Output: 128000}},
		},
	}, []string{"k"})
	c := NewClient(cfg, &scriptedProvider{}, "model", 128000, "")
	c.SetOutputTokenMax(8192)

	if got := c.InputLimitForModelRef("prov/model"); got != 272000 {
		t.Fatalf("InputLimitForModelRef() = %d, want 272000", got)
	}
}

func TestClientFallbackRunningInputLimitRecomputesDerivedBudgetAfterOutputCapChange(t *testing.T) {
	primaryCfg := testProviderConfigWithKeys("primary-prov", "primary-model", []string{"k1"})
	fallbackCfg := NewProviderConfig("fallback-prov", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			// No limit.output declared: the derived input budget follows the
			// configured max_output_tokens, so a runtime output-cap change
			// recomputes it.
			"fallback-model": {Limit: config.ModelLimit{Context: 400000}},
		},
	}, []string{"k2"})

	primaryImpl := &recordingProvider{}
	primaryImpl.calls = []scriptedCall{{err: &APIError{StatusCode: 400, Code: "context_length_exceeded", Message: "input is too long"}}}
	fallbackImpl := &scriptedProvider{calls: []scriptedCall{{resp: &message.Response{Content: "ok from fallback"}}}}

	c := NewClient(primaryCfg, primaryImpl, "primary-model", 4096, "sys")
	c.SetModelPool([]FallbackModel{{
		ProviderConfig: primaryCfg,
		ProviderImpl:   primaryImpl,
		ModelID:        "primary-model",
		MaxTokens:      4096,
		ContextLimit:   128000,
		InputLimit:     128000,
	}, {
		ProviderConfig:   fallbackCfg,
		ProviderImpl:     fallbackImpl,
		ModelID:          "fallback-model",
		MaxTokens:        128000,
		ContextLimit:     400000,
		InputLimit:       336000,
		DeriveInputLimit: true,
	}}, 0)
	c.SetOutputTokenMax(8192)

	resp, err := c.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}
	if resp == nil || resp.Content != "ok from fallback" {
		t.Fatalf("unexpected response: %#v", resp)
	}
	st := c.LastCallStatus()
	if got := st.RunningModelRef; got != "fallback-prov/fallback-model" {
		t.Fatalf("RunningModelRef = %q, want fallback-prov/fallback-model", got)
	}
	if got := st.RunningInputLimit; got != 391808 {
		t.Fatalf("RunningInputLimit = %d, want 391808 after output cap update", got)
	}
	if st.RunningAttemptAt.IsZero() {
		t.Fatal("RunningAttemptAt is zero after successful fallback request")
	}
}

func TestClassifyFallbackReasonUsesContextLengthExceededCode(t *testing.T) {
	err := &APIError{StatusCode: 400, Code: "context_length_exceeded", Message: "input is too long"}
	if got := classifyFallbackReason(err); got != "context_length_exceeded" {
		t.Fatalf("classifyFallbackReason() = %q, want context_length_exceeded", got)
	}
}

func TestClampEffectiveMaxTokensUsesCurrentRequestEstimate(t *testing.T) {
	model := config.ModelConfig{
		Limit: config.ModelLimit{
			Context: 200000,
			Output:  64000,
		},
	}
	messages := []message.Message{{
		Role:    "user",
		Content: strings.Repeat("x", 540000), // ~180k tokens
	}}

	got, _, _ := clampEffectiveMaxTokens(
		model,
		64000,
		64000,
		RequestTuning{},
		"",
		messages,
		nil,
		150000,
	)
	if got != 18000 {
		t.Fatalf("clampEffectiveMaxTokens() = %d, want 18000", got)
	}
}

func TestClampEffectiveMaxTokensStartsShrinkingAtDefaultOutputBoundary(t *testing.T) {
	model := config.ModelConfig{
		Limit: config.ModelLimit{
			Context: 200000,
			Output:  32000,
		},
	}
	messages := []message.Message{{
		Role:    "user",
		Content: strings.Repeat("x", 504000), // ~168k tokens
	}}

	got, _, _ := clampEffectiveMaxTokens(
		model,
		32000,
		32000,
		RequestTuning{},
		"",
		messages,
		nil,
		0,
	)
	if got != 30000 {
		t.Fatalf("clampEffectiveMaxTokens() = %d, want 30000", got)
	}
}

func TestClampEffectiveMaxTokensReportsInputThatFillsContextWindow(t *testing.T) {
	// ~3000 bytes ≈ 1000 tokens plus the 256-token provider buffer: the input
	// side alone fills the window, so no output budget is left.
	model := config.ModelConfig{Limit: config.ModelLimit{Context: 1000, Output: 256}}
	messages := []message.Message{{Role: "user", Content: strings.Repeat("x", 3000)}}

	got, _, inputFits := clampEffectiveMaxTokens(model, 256, 256, RequestTuning{}, "", messages, nil, 900)
	if inputFits {
		t.Fatal("inputFits = true, want false for input that fills the context window")
	}
	if got != 1 {
		t.Fatalf("clampEffectiveMaxTokens() = %d, want 1 (the output floor)", got)
	}

	// Without a provider-confirmed prompt size the conservative byte estimate
	// must not refuse the request: a fitting request would be compacted for
	// nothing. The provider call is the only way to find out.
	if _, _, fits := clampEffectiveMaxTokens(model, 256, 256, RequestTuning{}, "", messages, nil, 0); !fits {
		t.Fatal("inputFits = false without a provider-confirmed baseline")
	}

	// A request that leaves room for the normal output budget still fits.
	roomy := config.ModelConfig{Limit: config.ModelLimit{Context: 128000, Output: 4096}}
	if _, _, fits := clampEffectiveMaxTokens(roomy, 4096, 4096, RequestTuning{}, "", []message.Message{{Role: "user", Content: "hi"}}, nil, 0); !fits {
		t.Fatal("inputFits = false, want true when the input leaves room for the output budget")
	}
}

func TestEstimateRequestInputTokensIncludesSystemPromptAndTools(t *testing.T) {
	systemPrompt := strings.Repeat("s", 3000) // ~1000
	messages := []message.Message{{
		Role:    "user",
		Content: strings.Repeat("m", 6000), // ~2000
	}}
	tools := []message.ToolDefinition{{
		Name:        "Read",
		Description: strings.Repeat("d", 1500),
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": strings.Repeat("p", 1500),
				},
			},
		},
	}}

	got := estimateRequestInputTokens(systemPrompt, messages, tools)
	if got <= estimateInputTokens(messages) {
		t.Fatalf("estimateRequestInputTokens() = %d, want to exceed bare message estimate", got)
	}
}

func TestClientCompleteStreamClampsOutputToRemainingContextWithoutLastUsage(t *testing.T) {
	cfg := NewProviderConfig("primary-prov", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"primary-model": {
				Limit: config.ModelLimit{
					Context: 200000,
					Output:  64000,
				},
			},
		},
	}, []string{"k1"})
	impl := &recordingProvider{}
	impl.calls = []scriptedCall{{resp: &message.Response{Content: "ok"}}}

	c := NewClient(cfg, impl, "primary-model", 64000, "")
	c.SetOutputTokenMax(64000)

	_, err := c.CompleteStream(context.Background(), []message.Message{{
		Role:    "user",
		Content: strings.Repeat("x", 540000), // ~180k tokens
	}}, nil, nil)
	if err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}
	if len(impl.maxTokens) != 1 {
		t.Fatalf("provider maxTokens calls = %d, want 1", len(impl.maxTokens))
	}
	if impl.maxTokens[0] != 18000 {
		t.Fatalf("provider maxTokens[0] = %d, want 18000", impl.maxTokens[0])
	}
}

func TestClientCompleteStreamReusesClampedOutputBudgetAcrossKeyRetries(t *testing.T) {
	cfg := NewProviderConfig("primary-prov", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"primary-model": {
				Limit: config.ModelLimit{
					Context: 202752,
					Output:  65536,
				},
			},
		},
	}, []string{"k1", "k2"})
	impl := &recordingProvider{}
	impl.calls = []scriptedCall{
		{err: &APIError{StatusCode: 429, Message: "Too many concurrent requests for this model"}},
		{resp: &message.Response{Content: "ok"}},
	}

	c := NewClient(cfg, impl, "primary-model", 65536, "")

	_, err := c.CompleteStream(context.Background(), []message.Message{{
		Role:    "user",
		Content: "hi",
	}}, nil, nil)
	if err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}
	if len(impl.maxTokens) != 2 {
		t.Fatalf("provider maxTokens calls = %d, want 2", len(impl.maxTokens))
	}
	for i, got := range impl.maxTokens {
		if got != DefaultOutputTokenMax {
			t.Fatalf("provider maxTokens[%d] = %d, want %d", i, got, DefaultOutputTokenMax)
		}
	}
}

func TestClientCompleteReusesContextClampedOutputBudgetAcrossKeyRetries(t *testing.T) {
	cfg := NewProviderConfig("primary-prov", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"primary-model": {
				Limit: config.ModelLimit{
					Context: 200000,
					Output:  64000,
				},
			},
		},
	}, []string{"k1", "k2"})
	impl := &recordingProvider{}
	impl.calls = []scriptedCall{
		{err: &APIError{StatusCode: 429, Message: "rate limited"}},
		{resp: &message.Response{Content: "ok"}},
	}

	c := NewClient(cfg, impl, "primary-model", 64000, "")
	c.SetOutputTokenMax(64000)

	_, err := c.CompleteStream(context.Background(), []message.Message{{
		Role:    "user",
		Content: strings.Repeat("x", 540000), // ~180k tokens
	}}, nil, nil)
	if err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}
	if len(impl.maxTokens) != 2 {
		t.Fatalf("provider maxTokens calls = %d, want 2", len(impl.maxTokens))
	}
	for i, got := range impl.maxTokens {
		if got != 18000 {
			t.Fatalf("provider maxTokens[%d] = %d, want 18000", i, got)
		}
	}
}
