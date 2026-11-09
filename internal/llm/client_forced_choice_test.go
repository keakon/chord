package llm

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/keakon/chord/internal/executiongate"
	"github.com/keakon/chord/internal/jsonx"
	"github.com/keakon/chord/internal/message"
)

// invalidArgumentErr is the generic gateway-shaped 400 a compatible backend
// returns for a refused forced tool choice. Its message carries the narrow
// "invalid argument" marker the terminal request-shape classification keys on.
func invalidArgumentErr() *APIError {
	return &APIError{
		StatusCode: 400,
		Type:       "upstream_error",
		Message:    "Request contains an invalid argument.",
	}
}

// choiceGateProvider records the OpenAI tool choice each call observed and
// fails with a terminal request-shape 400 whenever a forced choice was sent,
// standing in for a gateway that rejects toolConfig mode ANY.
type choiceGateProvider struct {
	mu          sync.Mutex
	choices     []string
	refuseAll   bool
	probeErr    error
	probeResp   *message.Response
	beforeProbe func()
}

func (p *choiceGateProvider) CompleteStream(
	ctx context.Context,
	_ string,
	_ string,
	_ string,
	_ []message.Message,
	_ []message.ToolDefinition,
	_ int,
	tuning RequestTuning,
	cb StreamCallback,
) (*message.Response, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.choices = append(p.choices, tuning.OpenAI.ToolChoice)
	body, _ := jsonx.Marshal(map[string]any{
		"tools":       []any{map[string]any{"type": "function"}},
		"tool_choice": tuning.OpenAI.ToolChoice,
	})
	recordToolChoiceRequest(ctx, body)
	if p.refuseAll || tuning.OpenAI.ToolChoice != "" {
		return nil, invalidArgumentErr()
	}
	if p.beforeProbe != nil {
		p.beforeProbe()
	}
	if _, interrupted := errors.AsType[*InterruptedResponseError](p.probeErr); interrupted && cb != nil {
		cb(message.StreamDelta{Type: message.StreamDeltaText, Text: "Partial answer"})
	}
	if p.probeErr != nil || p.probeResp != nil {
		return p.probeResp, p.probeErr
	}
	// The retry layer discards an empty response as unusable and re-dispatches,
	// so the accepted call must return output that survives that check.
	return &message.Response{Content: "ok", StopReason: "stop"}, nil
}

func (p *choiceGateProvider) recordedChoices() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.choices...)
}

func TestAdaptiveForcedToolChoiceDegradesAfterRefusal(t *testing.T) {
	gate := &choiceGateProvider{}
	provider := testProviderConfig("primary-prov", "primary-model")
	c := NewClient(provider, gate, "primary-model", 4096, "sys")
	defer c.Close()
	c.MergeNextRequestTuningOverride(RequestTuning{OpenAI: OpenAITuning{ToolChoice: "required"}})

	resp, err := c.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}
	if resp == nil {
		t.Fatal("CompleteStream returned no response")
	}
	choices := gate.recordedChoices()
	if len(choices) != 2 || choices[0] != "required" || choices[1] != "" {
		t.Fatalf("provider saw tool choices %v, want [required \"\"] (one forced call plus one probe)", choices)
	}
	if !provider.forcedChoiceRefused("primary-model") {
		t.Fatal("refusal memo not recorded after the probe succeeded")
	}
	if !forcedToolChoiceDowngraded(provider, "primary-model", "required") {
		t.Fatal("downgrade predicate does not report the memoized refusal")
	}
}

func TestAdaptiveForcedToolChoiceProbeFailureSurfacesOriginalError(t *testing.T) {
	gate := &choiceGateProvider{refuseAll: true}
	provider := testProviderConfig("primary-prov", "primary-model")
	c := NewClient(provider, gate, "primary-model", 4096, "sys")
	defer c.Close()
	c.MergeNextRequestTuningOverride(RequestTuning{OpenAI: OpenAITuning{ToolChoice: "required"}})

	_, err := c.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "hi"}}, nil, nil)
	apiErr, ok := errors.AsType[*APIError](err)
	if !ok || apiErr == nil || apiErr.StatusCode != 400 {
		t.Fatalf("CompleteStream error = %v, want the original request-shape 400", err)
	}
	choices := gate.recordedChoices()
	if len(choices) != 2 || choices[0] != "required" || choices[1] != "" {
		t.Fatalf("provider saw tool choices %v, want [required \"\"] (exactly one probe)", choices)
	}
	if provider.forcedChoiceRefused("primary-model") {
		t.Fatal("refusal memo recorded despite the probe failing; a later gateway recovery could never regain the choice")
	}
}

func TestAdaptiveForcedToolChoiceFailedProbeDoesNotMemoize(t *testing.T) {
	for _, tc := range []struct {
		name string
		resp *message.Response
		err  error
	}{
		{name: "unavailable", err: &APIError{StatusCode: 503, Message: "unavailable"}},
		{name: "rate_limit", err: &APIError{StatusCode: 429, Message: "rate limit"}},
		{name: "empty", resp: &message.Response{StopReason: "stop"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gate := &choiceGateProvider{probeErr: tc.err, probeResp: tc.resp}
			provider := testProviderConfig("sample", "test-model")
			c := NewClient(provider, gate, "test-model", 4096, "")
			defer c.Close()
			c.MergeNextRequestTuningOverride(RequestTuning{OpenAI: OpenAITuning{ToolChoice: "required"}})
			_, err := c.CompleteStream(t.Context(), []message.Message{{Role: message.RoleUser, Content: "Sample"}}, nil, nil)
			apiErr, ok := errors.AsType[*APIError](err)
			if !ok || apiErr.StatusCode != 400 || provider.forcedChoiceRefused("test-model") {
				t.Fatalf("err=%v memo=%v", err, provider.forcedChoiceRefused("test-model"))
			}
			if calls := len(gate.recordedChoices()); calls != 2 {
				t.Fatalf("calls=%d, want one request and one probe", calls)
			}
		})
	}
}

func TestAdaptiveForcedToolChoicePropagatesTerminalProbeError(t *testing.T) {
	for _, terminal := range []error{
		&NativeToolError{Cause: invalidArgumentErr()},
		&HostedOutcomeUnknownError{Cause: invalidArgumentErr()},
		&executiongate.ErrSessionPaused{},
		context.Canceled,
		&RequestRedirectError{},
		&InterruptedResponseError{StopReason: "interrupted"},
	} {
		t.Run(terminal.Error(), func(t *testing.T) {
			gate := &choiceGateProvider{probeErr: terminal}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if errors.Is(terminal, context.Canceled) {
				gate.beforeProbe = cancel
			}
			fallback := &choiceGateProvider{}
			provider := testProviderConfig("sample", "test-model")
			c := NewClient(provider, gate, "test-model", 4096, "")
			defer c.Close()
			c.SetFallbackModels([]FallbackModel{{ProviderConfig: testProviderConfig("backup", "backup-model"), ProviderImpl: fallback, ModelID: "backup-model", MaxTokens: 4096}})
			c.MergeNextRequestTuningOverride(RequestTuning{OpenAI: OpenAITuning{ToolChoice: "required"}})
			_, err := c.CompleteStream(ctx, []message.Message{{Role: message.RoleUser, Content: "Sample"}}, nil, func(message.StreamDelta) {})
			if !errors.Is(err, terminal) || len(fallback.recordedChoices()) != 0 || provider.forcedChoiceRefused("test-model") {
				t.Fatalf("err=%v fallback_calls=%v memo=%v", err, fallback.recordedChoices(), provider.forcedChoiceRefused("test-model"))
			}
		})
	}
}
