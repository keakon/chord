package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/ratelimit"
)

// testNetTimeoutErr implements net.Error for timeout classification tests.
type testNetTimeoutErr struct {
	timeout   bool
	temporary bool
}

func (e testNetTimeoutErr) Error() string   { return "net timeout" }
func (e testNetTimeoutErr) Timeout() bool   { return e.timeout }
func (e testNetTimeoutErr) Temporary() bool { return e.temporary }

// tlsHandshakeTimeoutErr mimics crypto/tls-style handshake deadline errors.
type tlsHandshakeTimeoutErr struct{}

func (tlsHandshakeTimeoutErr) Error() string   { return "tls: handshake timeout" }
func (tlsHandshakeTimeoutErr) Timeout() bool   { return true }
func (tlsHandshakeTimeoutErr) Temporary() bool { return false }

type scriptedCall struct {
	resp         *message.Response
	err          error
	streams      []message.StreamDelta
	expectTuning *RequestTuning
}

type scriptedProvider struct {
	mu    sync.Mutex
	calls []scriptedCall
	count int
}

func (p *scriptedProvider) CompleteStream(
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
	if ctx.Err() != nil {
		p.mu.Lock()
		defer p.mu.Unlock()
		p.count++
		return nil, ctx.Err()
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.count++
	if len(p.calls) == 0 {
		return nil, &APIError{StatusCode: 400, Message: "invalid_request_error: unexpected provider call"}
	}
	next := p.calls[0]
	p.calls = p.calls[1:]
	if next.expectTuning != nil {
		if !reflect.DeepEqual(tuning, *next.expectTuning) {
			return nil, &APIError{StatusCode: 400, Message: fmt.Sprintf("invalid_request_error: unexpected request tuning: got %#v want %#v", tuning, *next.expectTuning)}
		}
	}
	if cb != nil {
		for _, delta := range next.streams {
			cb(delta)
		}
	}
	return next.resp, next.err
}

func (p *scriptedProvider) CallCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.count
}

type recordingProvider struct {
	scriptedProvider
	apiKeys   []string
	maxTokens []int
}

type constantErrProvider struct {
	err   error
	calls int
}

func (p *constantErrProvider) CompleteStream(
	_ context.Context,
	_ string,
	_ string,
	_ string,
	_ []message.Message,
	_ []message.ToolDefinition,
	_ int,
	_ RequestTuning,
	_ StreamCallback,
) (*message.Response, error) {
	p.calls++
	return nil, p.err
}

type recordingTuningProvider struct {
	calls  int
	tuning []RequestTuning
}

type serviceTierToggleRetryProvider struct {
	client     *Client
	expectFast RequestTuning
	calls      int
}

func (p *serviceTierToggleRetryProvider) CompleteStream(
	_ context.Context,
	_ string,
	_ string,
	_ string,
	_ []message.Message,
	_ []message.ToolDefinition,
	_ int,
	tuning RequestTuning,
	_ StreamCallback,
) (*message.Response, error) {
	p.calls++
	switch p.calls {
	case 1:
		want := RequestTuning{Anthropic: AnthropicTuning{PromptCacheMode: "explicit"}, SupportedServiceTiers: map[config.ServiceTier]bool{config.ServiceTierFast: true}}
		if !reflect.DeepEqual(tuning, want) {
			return nil, fmt.Errorf("first retry-round tuning = %#v, want %#v", tuning, want)
		}
		p.client.SetServiceTier(config.ServiceTierFast)
		return nil, &APIError{StatusCode: 500, Message: "retry with fast service tier"}
	case 2:
		if !reflect.DeepEqual(tuning, p.expectFast) {
			return nil, fmt.Errorf("second retry-round tuning = %#v, want %#v", tuning, p.expectFast)
		}
		return &message.Response{Content: "ok"}, nil
	default:
		return nil, fmt.Errorf("unexpected provider call %d", p.calls)
	}
}

type serviceTierSwitchingProvider struct {
	client *Client
}

func (p *serviceTierSwitchingProvider) CompleteStream(
	_ context.Context,
	_ string,
	_ string,
	_ string,
	_ []message.Message,
	_ []message.ToolDefinition,
	_ int,
	tuning RequestTuning,
	_ StreamCallback,
) (*message.Response, error) {
	if tuning.OpenAI.ServiceTier != "fast" {
		return nil, fmt.Errorf("OpenAI service tier = %q, want fast", tuning.OpenAI.ServiceTier)
	}
	p.client.SetServiceTier(config.ServiceTierStandard)
	return &message.Response{Content: "ok"}, nil
}

func (p *recordingTuningProvider) CompleteStream(
	_ context.Context,
	_ string,
	_ string,
	_ string,
	_ []message.Message,
	_ []message.ToolDefinition,
	_ int,
	tuning RequestTuning,
	_ StreamCallback,
) (*message.Response, error) {
	p.calls++
	p.tuning = append(p.tuning, tuning)
	return &message.Response{Content: "ok"}, nil
}

func callCompleteStreamWithRetryForTest(
	c *Client,
	ctx context.Context,
	startProvider *ProviderConfig,
	startImpl Provider,
	startModelID string,
	startMaxTokens int,
	startTuning RequestTuning,
	startVariant string,
	messages []message.Message,
	tools []message.ToolDefinition,
	cb StreamCallback,
	fallbackEnabled bool,
	fallbackModels []FallbackModel,
	maxAttempts int,
	status *CallStatus,
) (*message.Response, error) {
	generation, changedCh := c.routingSnapshot()
	return c.completeStreamWithRetry(
		ctx,
		startProvider,
		startImpl,
		startModelID,
		startMaxTokens,
		startTuning,
		startVariant,
		messages,
		tools,
		cb,
		fallbackEnabled,
		fallbackModels,
		maxAttempts,
		status,
		generation,
		changedCh,
	)
}

func TestPrimarySupportsViewImageToolUsesFirstPoolModel(t *testing.T) {
	responsesCfg := NewProviderConfig("responses", config.ProviderConfig{
		Type: config.ProviderTypeResponses,
		Models: map[string]config.ModelConfig{
			"vision": {Modalities: &config.ModelModalities{Input: []string{"text", "image"}}},
		},
	}, []string{"key"})
	chatCfg := NewProviderConfig("chat", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"vision": {Modalities: &config.ModelModalities{Input: []string{"text", "image"}}},
		},
	}, []string{"key"})

	client := NewClient(chatCfg, &scriptedProvider{}, "vision", 1024, "")
	client.SetModelPool([]FallbackModel{
		{ProviderConfig: responsesCfg, ProviderImpl: &scriptedProvider{}, ModelID: "vision", MaxTokens: 1024},
		{ProviderConfig: chatCfg, ProviderImpl: &scriptedProvider{}, ModelID: "vision", MaxTokens: 1024},
	}, 1)
	if !client.PrimarySupportsViewImageTool() {
		t.Fatal("PrimarySupportsViewImageTool() = false, want true from first model-pool entry")
	}

	client.SetModelPool([]FallbackModel{
		{ProviderConfig: chatCfg, ProviderImpl: &scriptedProvider{}, ModelID: "vision", MaxTokens: 1024},
		{ProviderConfig: responsesCfg, ProviderImpl: &scriptedProvider{}, ModelID: "vision", MaxTokens: 1024},
	}, 1)
	if client.PrimarySupportsViewImageTool() {
		t.Fatal("PrimarySupportsViewImageTool() = true, want false when first model-pool entry is OpenAI Chat")
	}
}

func TestCompleteStreamSkipsChatFallbackForImageToolResultHistory(t *testing.T) {
	responsesCfg := NewProviderConfig("responses", config.ProviderConfig{
		Type: config.ProviderTypeResponses,
		Models: map[string]config.ModelConfig{
			"vision": {Modalities: &config.ModelModalities{Input: []string{"text", "image"}}},
		},
	}, []string{"key"})
	chatCfg := NewProviderConfig("chat", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"vision": {Modalities: &config.ModelModalities{Input: []string{"text", "image"}}},
		},
	}, []string{"key"})
	startProvider := &scriptedProvider{calls: []scriptedCall{{err: &APIError{StatusCode: 502, Message: "bad gateway"}}}}
	chatProvider := &scriptedProvider{calls: []scriptedCall{{resp: &message.Response{Content: "should not run"}}}}
	client := NewClient(responsesCfg, startProvider, "vision", 1024, "")
	messages := []message.Message{{
		Role:       "tool",
		ToolCallID: "call_1",
		Content:    "Loaded image",
		Parts: []message.ContentPart{
			{Type: "text", Text: "Loaded image"},
			{Type: "image", MimeType: "image/png", Data: []byte("png")},
		},
	}}

	_, err := callCompleteStreamWithRetryForTest(
		client,
		context.Background(),
		responsesCfg,
		startProvider,
		"vision",
		1024,
		RequestTuning{},
		"",
		messages,
		nil,
		nil,
		true,
		[]FallbackModel{{ProviderConfig: chatCfg, ProviderImpl: chatProvider, ModelID: "vision", MaxTokens: 1024}},
		1,
		&CallStatus{},
	)
	if err == nil || !strings.Contains(err.Error(), "lacks required input support or uses the Chat API") {
		t.Fatalf("CompleteStream error = %v, want image tool-result replay error", err)
	}
	if got := chatProvider.CallCount(); got != 0 {
		t.Fatalf("chat fallback calls = %d, want 0", got)
	}

	textCfg := NewProviderConfig("text", config.ProviderConfig{
		Type: config.ProviderTypeResponses,
		Models: map[string]config.ModelConfig{
			"text": {Modalities: &config.ModelModalities{Input: []string{"text"}}},
		},
	}, []string{"key"})
	textProvider := &scriptedProvider{calls: []scriptedCall{{resp: &message.Response{Content: "should not run"}}}}
	_, err = callCompleteStreamWithRetryForTest(
		client,
		context.Background(),
		responsesCfg,
		startProvider,
		"vision",
		1024,
		RequestTuning{},
		"",
		messages,
		nil,
		nil,
		true,
		[]FallbackModel{{ProviderConfig: textCfg, ProviderImpl: textProvider, ModelID: "text", MaxTokens: 1024}},
		1,
		&CallStatus{},
	)
	if err == nil || !strings.Contains(err.Error(), "lacks required input support or uses the Chat API") {
		t.Fatalf("CompleteStream text fallback error = %v, want image input replay error", err)
	}
	if got := textProvider.CallCount(); got != 0 {
		t.Fatalf("text fallback calls = %d, want 0", got)
	}
}

func TestCompleteStreamSkipsImageOnlyFallbackForPDFToolResultHistory(t *testing.T) {
	responsesCfg := NewProviderConfig("responses", config.ProviderConfig{
		Type: config.ProviderTypeResponses,
		Models: map[string]config.ModelConfig{
			"document": {Modalities: &config.ModelModalities{Input: []string{"text", "pdf"}}},
		},
	}, []string{"key"})
	imageOnlyCfg := NewProviderConfig("image-only", config.ProviderConfig{
		Type: config.ProviderTypeResponses,
		Models: map[string]config.ModelConfig{
			"vision": {Modalities: &config.ModelModalities{Input: []string{"text", "image"}}},
		},
	}, []string{"key"})
	startProvider := &scriptedProvider{calls: []scriptedCall{{err: &APIError{StatusCode: 502, Message: "bad gateway"}}}}
	imageOnlyProvider := &scriptedProvider{calls: []scriptedCall{{resp: &message.Response{Content: "should not run"}}}}
	client := NewClient(responsesCfg, startProvider, "document", 1024, "")
	messages := []message.Message{{
		Role:       "tool",
		ToolCallID: "call_1",
		Content:    "Loaded PDF",
		Parts: []message.ContentPart{
			{Type: "text", Text: "Loaded PDF"},
			{Type: "pdf", MimeType: "application/pdf", Data: []byte("%PDF-1.7")},
		},
	}}

	_, err := callCompleteStreamWithRetryForTest(
		client,
		context.Background(),
		responsesCfg,
		startProvider,
		"document",
		1024,
		RequestTuning{},
		"",
		messages,
		nil,
		nil,
		true,
		[]FallbackModel{{ProviderConfig: imageOnlyCfg, ProviderImpl: imageOnlyProvider, ModelID: "vision", MaxTokens: 1024}},
		1,
		&CallStatus{},
	)
	if err == nil || !strings.Contains(err.Error(), "tool-returned pdf data") || !strings.Contains(err.Error(), "lacks required input support or uses the Chat API") {
		t.Fatalf("CompleteStream error = %v, want pdf replay capability error", err)
	}
	if got := imageOnlyProvider.CallCount(); got != 0 {
		t.Fatalf("image-only fallback calls = %d, want 0", got)
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

func (p *recordingProvider) CompleteStream(
	ctx context.Context,
	apiKey string,
	model string,
	systemPrompt string,
	messages []message.Message,
	tools []message.ToolDefinition,
	maxTokens int,
	tuning RequestTuning,
	cb StreamCallback,
) (*message.Response, error) {
	p.mu.Lock()
	p.apiKeys = append(p.apiKeys, apiKey)
	p.maxTokens = append(p.maxTokens, maxTokens)
	p.mu.Unlock()
	return p.scriptedProvider.CompleteStream(ctx, apiKey, model, systemPrompt, messages, tools, maxTokens, tuning, cb)
}

func testProviderConfig(name, model string) *ProviderConfig {
	return testProviderConfigWithKeys(name, model, []string{"test-key"})
}

func testProviderConfigWithKeys(name, model string, keys []string) *ProviderConfig {
	return NewProviderConfig(name, config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			model: {
				Limit: config.ModelLimit{
					Context: 128000,
					Output:  4096,
				},
			},
		},
	}, keys)
}

func testOfficialOpenAIProviderConfigWithKeys(name, model string, keys []string) *ProviderConfig {
	return NewProviderConfig(name, config.ProviderConfig{
		Type:        config.ProviderTypeResponses,
		APIURL:      "https://api.openai.com/v1/responses",
		OfficialAPI: new(true),
		Models: map[string]config.ModelConfig{
			model: {
				Limit: config.ModelLimit{
					Context: 128000,
					Output:  4096,
				},
			},
		},
	}, keys)
}

func testCompatibleResponsesProviderConfigWithKeys(name, model string, keys []string) *ProviderConfig {
	return NewProviderConfig(name, config.ProviderConfig{
		Type:        config.ProviderTypeResponses,
		APIURL:      "https://gateway.example.com/v1/responses",
		OfficialAPI: new(false),
		Models: map[string]config.ModelConfig{
			model: {
				Limit: config.ModelLimit{
					Context: 128000,
					Output:  4096,
				},
			},
		},
	}, keys)
}

func testFastProviderConfig(name, model string) *ProviderConfig {
	return NewProviderConfig(name, config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			model: {
				Limit:                 config.ModelLimit{Context: 128000, Output: 4096},
				SupportedServiceTiers: []config.ServiceTier{config.ServiceTierFast},
			},
		},
	}, []string{"test-key"})
}

func disableRetryDelayForTest(provider *ProviderConfig) {
	provider.retryDelayBase = -1
}

func TestClient_RetriableErrorTriesOtherKeysBeforeFallbackModel(t *testing.T) {
	primaryCfg := testProviderConfigWithKeys("codex", "gpt-5.5", []string{"key-a", "key-b"})
	fallbackCfg := testProviderConfig("qt", "glm-5.1")

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

func TestClient_402QuotaErrorTriesOtherKeysBeforeFallbackModel(t *testing.T) {
	primaryCfg := testProviderConfigWithKeys("freemodel", "gpt-5.4", []string{"key-a", "key-b", "key-c"})
	fallbackCfg := testProviderConfig("qt", "glm-5.1")

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

func TestMarkKeyCooldown402And429UseRetryAfterOrDefault(t *testing.T) {
	ctx := context.Background()
	snapReset := time.Now().Add(3 * time.Minute)
	snap := &ratelimit.KeyRateLimitSnapshot{
		Primary: &ratelimit.RateLimitWindow{UsedPct: 50, ResetsAt: snapReset},
	}
	err429 := &APIError{StatusCode: 429, Message: "rate limited"}

	t.Run("default_is_1s_when_no_retry_after", func(t *testing.T) {
		p := NewProviderConfig("p", config.ProviderConfig{Type: config.ProviderTypeChatCompletions}, []string{"k1"})
		p.UpdateKeySnapshot("k1", snap)
		res := markKeyCooldown(ctx, p, "k1", err429)
		if !res.cooldownApplied {
			t.Fatal("expected cooldownApplied=true for 429")
		}
		p.mu.Lock()
		var end time.Time
		for _, ks := range p.keyStates {
			if ks.Key == "k1" {
				end = ks.CooldownEnd
			}
		}
		p.mu.Unlock()
		remain := time.Until(end)
		if remain < 500*time.Millisecond || remain > 1500*time.Millisecond {
			t.Fatalf("expected ~1s cooldown, got remaining %v", remain)
		}
	})

	t.Run("retry_after_is_capped_at_1m", func(t *testing.T) {
		p := NewProviderConfig("p", config.ProviderConfig{Type: config.ProviderTypeChatCompletions}, []string{"k1"})
		res := markKeyCooldown(ctx, p, "k1", &APIError{StatusCode: 429, Message: "rl", RetryAfter: 2 * time.Minute})
		if !res.cooldownApplied {
			t.Fatal("expected cooldownApplied=true for Retry-After 429")
		}
		p.mu.Lock()
		var end time.Time
		for _, ks := range p.keyStates {
			if ks.Key == "k1" {
				end = ks.CooldownEnd
			}
		}
		p.mu.Unlock()
		remain := time.Until(end)
		if remain < 55*time.Second || remain > 61*time.Second {
			t.Fatalf("expected ~1m cooldown cap, got remaining %v", remain)
		}
	})

	t.Run("402_uses_same_cooldown_path", func(t *testing.T) {
		p := NewProviderConfig("p", config.ProviderConfig{Type: config.ProviderTypeChatCompletions}, []string{"k1"})
		res := markKeyCooldown(ctx, p, "k1", &APIError{StatusCode: 402, Message: "quota exhausted; retry later"})
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
		res := markKeyCooldown(ctx, p, "k1", &APIError{StatusCode: 400, Message: "Concurrency limit exceeded for user, please retry later"})
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

		res := markKeyCooldown(ctx, p, "oauth-key", &APIError{StatusCode: 402, Code: "deactivated_workspace", Message: `{"detail":{"code":"deactivated_workspace"}}`})
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

		res := markKeyCooldown(ctx, p, "oauth-key", &APIError{StatusCode: 402, Message: `{"detail":{"code":"deactivated_workspace"}}`})
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
}

func TestMarkKeyCooldown429CodexOAuthQuotaExhaustedUsesResetWindow(t *testing.T) {
	ctx := context.Background()
	resetPrimary := time.Now().Add(2 * time.Hour)
	resetSecondary := time.Now().Add(24 * time.Hour)
	p := NewProviderConfig("p", config.ProviderConfig{Type: config.ProviderTypeResponses, Preset: config.ProviderPresetCodex}, []string{"oauth-key"})
	p.mu.Lock()
	p.keyStates[0].OAuthInfo = &OAuthKeyInfo{Expires: time.Now().Add(time.Hour).UnixMilli()}
	p.mu.Unlock()
	p.UpdateKeySnapshot("oauth-key", &ratelimit.KeyRateLimitSnapshot{
		Primary:   &ratelimit.RateLimitWindow{UsedPct: 100, ResetsAt: resetPrimary},
		Secondary: &ratelimit.RateLimitWindow{UsedPct: 100, ResetsAt: resetSecondary},
	})
	res := markKeyCooldown(ctx, p, "oauth-key", &APIError{StatusCode: 429, Message: "quota exhausted"})
	if !res.cooldownApplied {
		t.Fatal("expected cooldownApplied=true for quota exhaustion")
	}
	p.mu.Lock()
	ks := p.keyStates[0]
	exhaustedUntil := ks.ExhaustedUntil
	cooldownEnd := ks.CooldownEnd
	p.mu.Unlock()
	if exhaustedUntil.Before(resetSecondary.Add(-2*time.Second)) || exhaustedUntil.After(resetSecondary.Add(2*time.Second)) {
		t.Fatalf("ExhaustedUntil = %v, want ~%v", exhaustedUntil, resetSecondary)
	}
	if !cooldownEnd.IsZero() {
		t.Fatalf("CooldownEnd = %v, want zero when quota exhaustion uses hard reset window", cooldownEnd)
	}
}

func TestMarkKeyCooldown401OAuthRefreshReturnsRefreshedKey(t *testing.T) {
	ctx := context.Background()
	oldAccess := testProviderOAuthJWT(`{"chatgpt_account_id":"acc-1","chatgpt_user_id":"user-1"}`)
	newAccess := testProviderOAuthJWT(`{"chatgpt_account_id":"acc-1","chatgpt_user_id":"user-1","exp":4102444800}`)
	refreshServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
			t.Fatalf("Content-Type = %q, want application/json", got)
		}
		var body struct {
			GrantType    string `json:"grant_type"`
			RefreshToken string `json:"refresh_token"`
			ClientID     string `json:"client_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("Decode body: %v", err)
		}
		if body.GrantType != "refresh_token" {
			t.Fatalf("grant_type = %q, want refresh_token", body.GrantType)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"`+newAccess+`","refresh_token":"new-refresh-token","expires_in":3600}`)
	}))
	defer refreshServer.Close()

	creds := []config.ProviderCredential{{
		OAuth: &config.OAuthCredential{
			Access:  oldAccess,
			Refresh: "old-refresh-token",
			Expires: time.Now().Add(time.Hour).UnixMilli(),
		},
	}}
	auth := config.AuthConfig{"openai": creds}
	var authMu sync.Mutex
	p := NewProviderConfig("openai", config.ProviderConfig{Type: config.ProviderTypeResponses, Preset: config.ProviderPresetCodex}, config.ExtractAPIKeys(creds))
	p.SetOAuthRefresher(refreshServer.URL, "client-id", "", "", &auth, &authMu, map[string]OAuthKeySetup{creds[0].OAuth.Access: {CredentialIndex: 0, AccountUserID: "user-1__acc-1", AccountID: "acc-1", Expires: creds[0].OAuth.Expires}}, "")

	result := markKeyCooldown(ctx, p, oldAccess, &APIError{StatusCode: 401, Message: "unauthorized"})
	if !result.oauthRefreshed {
		t.Fatal("expected oauthRefreshed=true")
	}
	if result.cooldownApplied {
		t.Fatal("expected cooldownApplied=false after successful refresh")
	}
	if result.refreshedKey != newAccess {
		t.Fatalf("refreshedKey = %q, want refreshed access", result.refreshedKey)
	}

	key, _, err := p.SelectKeyWithContext(ctx)
	if err != nil {
		t.Fatalf("SelectKeyWithContext: %v", err)
	}
	if key != newAccess {
		t.Fatalf("selected key = %q, want refreshed key", key)
	}
}

func TestMarkKeyCooldown403OAuthRefreshReturnsRefreshedKey(t *testing.T) {
	ctx := context.Background()
	oldAccess := testProviderOAuthJWT(`{"chatgpt_account_id":"acc-1","chatgpt_user_id":"user-1"}`)
	newAccess := testProviderOAuthJWT(`{"chatgpt_account_id":"acc-1","chatgpt_user_id":"user-1","exp":4102444800}`)
	refreshServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"`+newAccess+`","refresh_token":"new-refresh-token","expires_in":3600}`)
	}))
	defer refreshServer.Close()

	creds := []config.ProviderCredential{{
		OAuth: &config.OAuthCredential{
			Access:  oldAccess,
			Refresh: "old-refresh-token",
			Expires: time.Now().Add(time.Hour).UnixMilli(),
		},
	}}
	auth := config.AuthConfig{"openai": creds}
	var authMu sync.Mutex
	p := NewProviderConfig("openai", config.ProviderConfig{Type: config.ProviderTypeResponses, Preset: config.ProviderPresetCodex}, config.ExtractAPIKeys(creds))
	p.SetOAuthRefresher(refreshServer.URL, "client-id", "", "", &auth, &authMu, map[string]OAuthKeySetup{creds[0].OAuth.Access: {CredentialIndex: 0, AccountUserID: "user-1__acc-1", AccountID: "acc-1", Expires: creds[0].OAuth.Expires}}, "")

	result := markKeyCooldown(ctx, p, oldAccess, &APIError{StatusCode: 403, Message: "forbidden"})
	if !result.oauthRefreshed {
		t.Fatal("expected oauthRefreshed=true")
	}
	if result.cooldownApplied {
		t.Fatal("expected cooldownApplied=false after successful refresh")
	}
	if result.refreshedKey != newAccess {
		t.Fatalf("refreshedKey = %q, want refreshed access", result.refreshedKey)
	}
}

func TestMarkKeyCooldown401OAuthNoRefresherPersistsDeactivatedKey(t *testing.T) {
	ctx := context.Background()
	auth := config.AuthConfig{"openai": {{OAuth: &config.OAuthCredential{
		Access:        "oauth-key",
		Refresh:       "refresh-token",
		Expires:       time.Now().Add(time.Hour).UnixMilli(),
		AccountUserID: "user-1__acc-1", AccountID: "acc-1",
	}}}}
	var authMu sync.Mutex
	p := NewProviderConfig("openai", config.ProviderConfig{Type: config.ProviderTypeResponses, Preset: config.ProviderPresetCodex}, []string{"oauth-key"})
	p.SetOAuthRefresher(config.OpenAIOAuthTokenURL, config.OpenAIOAuthClientID, "", "", &auth, &authMu, map[string]OAuthKeySetup{
		"oauth-key": {CredentialIndex: 0, AccountUserID: "user-1__acc-1", AccountID: "acc-1", Expires: auth["openai"][0].OAuth.Expires},
	}, "")

	result := markKeyCooldown(ctx, p, "oauth-key", &APIError{StatusCode: 401, Code: "account_deactivated", Message: "account deactivated"})
	if !result.cooldownApplied {
		t.Fatal("expected cooldownApplied=true")
	}
	if auth["openai"][0].OAuth.Status != config.OAuthStatusDeactivated {
		t.Fatal("expected auth config OAuth credential to be marked deactivated")
	}

	_, _, err := p.SelectKeyWithContext(ctx)
	if err == nil {
		t.Fatal("expected NoUsableKeysError after disabling only OAuth key")
	}
	var noUsable *NoUsableKeysError
	if !errors.As(err, &noUsable) {
		t.Fatalf("expected NoUsableKeysError, got %T: %v", err, err)
	}
}

func TestMarkKeyCooldown401OAuthNoRefresherDeactivatesKey(t *testing.T) {
	ctx := context.Background()
	p := newTestProviderConfig([]string{"oauth-key"})
	p.mu.Lock()
	p.keyStates[0].OAuthInfo = &OAuthKeyInfo{Expires: time.Now().Add(time.Hour).UnixMilli()}
	p.mu.Unlock()
	// no OAuthRefresher set → TryRefreshOAuthKey returns false → MarkDeactivated
	result := markKeyCooldown(ctx, p, "oauth-key", &APIError{StatusCode: 401, Code: "account_deactivated", Message: "account deactivated"})
	if !result.cooldownApplied {
		t.Fatal("expected cooldownApplied=true")
	}
	_, total := p.AvailableKeyCount()
	if total != 0 {
		t.Fatalf("total = %d, want 0: deactivated OAuth key should be excluded", total)
	}
}

func TestMarkKeyCooldownOAuthMessageOnlyDeactivatesKey(t *testing.T) {
	ctx := context.Background()
	for _, status := range []int{401, 403} {
		t.Run(fmt.Sprintf("HTTP_%d", status), func(t *testing.T) {
			p := newTestProviderConfig([]string{"oauth-key"})
			p.mu.Lock()
			p.keyStates[0].OAuthInfo = &OAuthKeyInfo{
				AccountID: "acc-1",
				Email:     "user@example.com",
				Expires:   time.Now().Add(time.Hour).UnixMilli(),
			}
			p.mu.Unlock()

			result := markKeyCooldown(ctx, p, "oauth-key", &APIError{StatusCode: status, Message: "Your account has been disabled."})
			if !result.cooldownApplied {
				t.Fatal("expected cooldownApplied=true")
			}
			if result.deactivatedAccountID != "acc-1" || result.deactivatedEmail != "user@example.com" {
				t.Fatalf("deactivated identity = %q/%q, want acc-1/user@example.com", result.deactivatedAccountID, result.deactivatedEmail)
			}
			_, total := p.AvailableKeyCount()
			if total != 0 {
				t.Fatalf("total = %d, want 0: deactivated OAuth key should be excluded", total)
			}
		})
	}
}

func TestMarkKeyCooldown401OAuthInvalidatedSkipsRefreshAndDeactivatesKey(t *testing.T) {
	ctx := context.Background()
	refreshHit := false
	refreshServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		refreshHit = true
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"access_token":"new-access-token","refresh_token":"new-refresh-token","expires_in":3600}`)
	}))
	defer refreshServer.Close()

	expires := time.Now().Add(time.Hour).UnixMilli()
	auth := config.AuthConfig{"openai": {{OAuth: &config.OAuthCredential{
		Access:        "oauth-key",
		Refresh:       "refresh-token",
		Expires:       expires,
		AccountUserID: "user-1__acc-1", AccountID: "acc-1",
	}}}}
	var authMu sync.Mutex
	p := NewProviderConfig("openai", config.ProviderConfig{Type: config.ProviderTypeResponses, Preset: config.ProviderPresetCodex}, []string{"oauth-key"})
	p.SetOAuthRefresher(refreshServer.URL, "client-id", "", "", &auth, &authMu, map[string]OAuthKeySetup{
		"oauth-key": {CredentialIndex: 0, AccountUserID: "user-1__acc-1", AccountID: "acc-1", Expires: expires},
	}, "")

	result := markKeyCooldown(ctx, p, "oauth-key", &APIError{StatusCode: 401, Code: "account_invalidated", Message: "account invalidated"})
	if !result.cooldownApplied {
		t.Fatal("expected cooldownApplied=true")
	}
	if refreshHit {
		t.Fatal("refresh endpoint was called for invalidated account")
	}
	if auth["openai"][0].OAuth.Status != config.OAuthStatusInvalidated {
		t.Fatalf("OAuth status = %q, want invalidated", auth["openai"][0].OAuth.Status)
	}
	_, total := p.AvailableKeyCount()
	if total != 0 {
		t.Fatalf("total = %d, want 0: invalidated OAuth key should be excluded", total)
	}
}

func TestMarkKeyCooldownOAuthMessageOnlyInvalidatedSkipsRefresh(t *testing.T) {
	ctx := context.Background()
	for _, status := range []int{401, 403} {
		t.Run(fmt.Sprintf("HTTP_%d", status), func(t *testing.T) {
			refreshHit := false
			refreshServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				refreshHit = true
				w.WriteHeader(http.StatusOK)
				_, _ = io.WriteString(w, `{"access_token":"new-access-token","refresh_token":"new-refresh-token","expires_in":3600}`)
			}))
			defer refreshServer.Close()

			expires := time.Now().Add(time.Hour).UnixMilli()
			auth := config.AuthConfig{"openai": {{OAuth: &config.OAuthCredential{
				Access:        "oauth-key",
				Refresh:       "refresh-token",
				Expires:       expires,
				AccountUserID: "user-1__acc-1",
				AccountID:     "acc-1",
				Email:         "user@example.com",
			}}}}
			var authMu sync.Mutex
			p := NewProviderConfig("openai", config.ProviderConfig{Type: config.ProviderTypeResponses, Preset: config.ProviderPresetCodex}, []string{"oauth-key"})
			p.SetOAuthRefresher(refreshServer.URL, "client-id", "", "", &auth, &authMu, map[string]OAuthKeySetup{
				"oauth-key": {CredentialIndex: 0, AccountUserID: "user-1__acc-1", AccountID: "acc-1", Email: "user@example.com", Expires: expires},
			}, "")

			result := markKeyCooldown(ctx, p, "oauth-key", &APIError{StatusCode: status, Message: "Your authentication token has been revoked."})
			if !result.cooldownApplied {
				t.Fatal("expected cooldownApplied=true")
			}
			if refreshHit {
				t.Fatal("refresh endpoint was called for message-only invalidated account")
			}
			if result.invalidatedAccountID != "acc-1" || result.invalidatedEmail != "user@example.com" {
				t.Fatalf("invalidated identity = %q/%q, want acc-1/user@example.com", result.invalidatedAccountID, result.invalidatedEmail)
			}
			if auth["openai"][0].OAuth.Status != config.OAuthStatusInvalidated {
				t.Fatalf("OAuth status = %q, want invalidated", auth["openai"][0].OAuth.Status)
			}
			_, total := p.AvailableKeyCount()
			if total != 0 {
				t.Fatalf("total = %d, want 0: invalidated OAuth key should be excluded", total)
			}
		})
	}
}

func TestMarkKeyCooldown401OAuthEmptyRefreshTokenMarksExpiredWithoutHTTP(t *testing.T) {
	ctx := context.Background()
	refreshHit := false
	refreshServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		refreshHit = true
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"access_token":"new-access-token","refresh_token":"new-refresh-token","expires_in":3600}`)
	}))
	defer refreshServer.Close()

	expires := time.Now().Add(time.Hour).UnixMilli()
	auth := config.AuthConfig{"openai": {{OAuth: &config.OAuthCredential{
		Access:        "oauth-key",
		Expires:       expires,
		AccountUserID: "user-1__acc-1", AccountID: "acc-1",
	}}}}
	var authMu sync.Mutex
	p := NewProviderConfig("openai", config.ProviderConfig{Type: config.ProviderTypeResponses, Preset: config.ProviderPresetCodex}, []string{"oauth-key"})
	p.SetOAuthRefresher(refreshServer.URL, "client-id", "", "", &auth, &authMu, map[string]OAuthKeySetup{
		"oauth-key": {CredentialIndex: 0, AccountUserID: "user-1__acc-1", AccountID: "acc-1", Expires: expires},
	}, "")

	result := markKeyCooldown(ctx, p, "oauth-key", &APIError{StatusCode: 401, Message: "unauthorized"})
	if !result.cooldownApplied {
		t.Fatal("expected cooldownApplied=true")
	}
	if refreshHit {
		t.Fatal("refresh endpoint was called despite empty refresh token")
	}
	if auth["openai"][0].OAuth.Status != config.OAuthStatusExpired {
		t.Fatalf("OAuth status = %q, want expired: missing refresh token cannot recover after access token is rejected", auth["openai"][0].OAuth.Status)
	}
	available, total := p.AvailableKeyCount()
	if available != 0 || total != 0 {
		t.Fatalf("AvailableKeyCount = %d/%d, want 0/0: unrecoverable OAuth key should be permanently excluded", available, total)
	}
}

func TestMarkKeyCooldown401OAuthTokenInvalidatedCodeSkipsRefreshAndInvalidatesKey(t *testing.T) {
	ctx := context.Background()
	refreshHit := false
	refreshServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		refreshHit = true
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"access_token":"new-access-token","refresh_token":"new-refresh-token","expires_in":3600}`)
	}))
	defer refreshServer.Close()

	expires := time.Now().Add(time.Hour).UnixMilli()
	auth := config.AuthConfig{"openai": {{OAuth: &config.OAuthCredential{
		Access:        "oauth-key",
		Refresh:       "refresh-token",
		Expires:       expires,
		AccountUserID: "user-1__acc-1", AccountID: "acc-1",
	}}}}
	var authMu sync.Mutex
	p := NewProviderConfig("openai", config.ProviderConfig{Type: config.ProviderTypeResponses, Preset: config.ProviderPresetCodex}, []string{"oauth-key"})
	p.SetOAuthRefresher(refreshServer.URL, "client-id", "", "", &auth, &authMu, map[string]OAuthKeySetup{
		"oauth-key": {CredentialIndex: 0, AccountUserID: "user-1__acc-1", AccountID: "acc-1", Expires: expires},
	}, "")

	result := markKeyCooldown(ctx, p, "oauth-key", &APIError{StatusCode: 401, Code: "token_invalidated", Message: "Your authentication token has been invalidated. Please try signing in again."})
	if !result.cooldownApplied {
		t.Fatal("expected cooldownApplied=true")
	}
	if refreshHit {
		t.Fatal("refresh endpoint was called for token_invalidated")
	}
	if auth["openai"][0].OAuth.Status != config.OAuthStatusInvalidated {
		t.Fatalf("OAuth status = %q, want invalidated", auth["openai"][0].OAuth.Status)
	}
}

func TestMarkKeyCooldown401OAuthMalformedAuthTokenDetailInvalidatesKey(t *testing.T) {
	ctx := context.Background()
	refreshHit := false
	refreshServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		refreshHit = true
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"access_token":"new-access-token","refresh_token":"new-refresh-token","expires_in":3600}`)
	}))
	defer refreshServer.Close()

	expires := time.Now().Add(time.Hour).UnixMilli()
	auth := config.AuthConfig{"openai": {{OAuth: &config.OAuthCredential{
		Access:        "oauth-key",
		Refresh:       "refresh-token",
		Expires:       expires,
		AccountUserID: "user-1__acc-1", AccountID: "acc-1",
	}}}}
	var authMu sync.Mutex
	p := NewProviderConfig("openai", config.ProviderConfig{Type: config.ProviderTypeResponses, Preset: config.ProviderPresetCodex}, []string{"oauth-key"})
	p.SetOAuthRefresher(refreshServer.URL, "client-id", "", "", &auth, &authMu, map[string]OAuthKeySetup{
		"oauth-key": {CredentialIndex: 0, AccountUserID: "user-1__acc-1", AccountID: "acc-1", Expires: expires},
	}, "")

	apiErr := parseOpenAIHTTPErrorFromBytes(http.StatusUnauthorized, nil, []byte(`{"detail":"Could not parse your authentication token. Please try signing in again."}`))
	result := markKeyCooldown(ctx, p, "oauth-key", apiErr)
	if !result.cooldownApplied {
		t.Fatal("expected cooldownApplied=true")
	}
	if refreshHit {
		t.Fatal("refresh endpoint was called for malformed authentication token")
	}
	if auth["openai"][0].OAuth.Status != config.OAuthStatusInvalidated {
		t.Fatalf("OAuth status = %q, want invalidated", auth["openai"][0].OAuth.Status)
	}
}

func TestMarkKeyCooldown401OAuthRefreshUnknownUnauthorizedMarksExpired(t *testing.T) {
	ctx := context.Background()
	refreshServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"detail":"unauthorized"}`)
	}))
	defer refreshServer.Close()

	expires := time.Now().Add(time.Hour).UnixMilli()
	auth := config.AuthConfig{"openai": {{OAuth: &config.OAuthCredential{
		Access:        "oauth-key",
		Refresh:       "refresh-token",
		Expires:       expires,
		AccountUserID: "user-1__acc-1", AccountID: "acc-1",
	}}}}
	var authMu sync.Mutex
	p := NewProviderConfig("openai", config.ProviderConfig{Type: config.ProviderTypeResponses, Preset: config.ProviderPresetCodex}, []string{"oauth-key"})
	p.SetOAuthRefresher(refreshServer.URL, "client-id", "", "", &auth, &authMu, map[string]OAuthKeySetup{
		"oauth-key": {CredentialIndex: 0, AccountUserID: "user-1__acc-1", AccountID: "acc-1", Expires: expires},
	}, "")

	result := markKeyCooldown(ctx, p, "oauth-key", &APIError{StatusCode: 401, Message: "unauthorized"})
	if !result.cooldownApplied {
		t.Fatal("expected cooldownApplied=true")
	}
	if auth["openai"][0].OAuth.Status != config.OAuthStatusExpired {
		t.Fatalf("OAuth status = %q, want expired for refresh 401", auth["openai"][0].OAuth.Status)
	}
}

func TestCompleteStreamTerminal401MarksInvalidatedOAuthBeforeReturning(t *testing.T) {
	ctx := context.Background()
	expires := time.Now().Add(time.Hour).UnixMilli()
	auth := config.AuthConfig{"openai": {{OAuth: &config.OAuthCredential{
		Access:        "oauth-key",
		Refresh:       "refresh-token",
		Expires:       expires,
		AccountUserID: "user-1__acc-1", AccountID: "acc-1",
		Email: "user@example.com",
	}}}}
	var authMu sync.Mutex
	p := NewProviderConfig("openai", config.ProviderConfig{Type: config.ProviderTypeResponses, Preset: config.ProviderPresetCodex}, []string{"oauth-key"})
	p.SetOAuthRefresher(config.OpenAIOAuthTokenURL, config.OpenAIOAuthClientID, "", "", &auth, &authMu, map[string]OAuthKeySetup{
		"oauth-key": {CredentialIndex: 0, AccountUserID: "user-1__acc-1", AccountID: "acc-1", Email: "user@example.com", Expires: expires},
	}, "")
	impl := &constantErrProvider{err: &APIError{StatusCode: 401, Code: "account_invalidated", Message: "account invalidated"}}
	client := NewClient(p, impl, "model", 128, "")
	client.SetTerminalAPIStatusCodes(401)

	var deltas []message.StreamDelta
	_, err := client.CompleteStream(ctx, []message.Message{{Role: "user", Content: "hello"}}, nil, func(delta message.StreamDelta) {
		deltas = append(deltas, delta)
	})
	if err == nil {
		t.Fatal("expected terminal API error")
	}
	if auth["openai"][0].OAuth.Status != config.OAuthStatusInvalidated {
		t.Fatalf("expected auth config OAuth credential to be marked invalidated, got %q", auth["openai"][0].OAuth.Status)
	}
	foundInvalidatedDelta := false
	for _, delta := range deltas {
		if delta.Type == "key_invalidated" && delta.AccountID == "acc-1" && delta.Email == "user@example.com" {
			foundInvalidatedDelta = true
			break
		}
	}
	if !foundInvalidatedDelta {
		t.Fatalf("expected key_invalidated delta with account metadata, got %#v", deltas)
	}
}

func TestMarkKeyCooldown403OAuthInvalidatedSkipsRefreshAndInvalidatesKey(t *testing.T) {
	ctx := context.Background()
	refreshHit := false
	refreshServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		refreshHit = true
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"access_token":"new-access-token","refresh_token":"new-refresh-token","expires_in":3600}`)
	}))
	defer refreshServer.Close()

	expires := time.Now().Add(time.Hour).UnixMilli()
	auth := config.AuthConfig{"openai": {{OAuth: &config.OAuthCredential{
		Access:        "oauth-key",
		Refresh:       "refresh-token",
		Expires:       expires,
		AccountUserID: "user-1__acc-1", AccountID: "acc-1",
	}}}}
	var authMu sync.Mutex
	p := NewProviderConfig("openai", config.ProviderConfig{Type: config.ProviderTypeResponses, Preset: config.ProviderPresetCodex}, []string{"oauth-key"})
	p.SetOAuthRefresher(refreshServer.URL, "client-id", "", "", &auth, &authMu, map[string]OAuthKeySetup{
		"oauth-key": {CredentialIndex: 0, AccountUserID: "user-1__acc-1", AccountID: "acc-1", Expires: expires},
	}, "")

	result := markKeyCooldown(ctx, p, "oauth-key", &APIError{StatusCode: 403, Code: "account_invalidated", Message: "account invalidated"})
	if !result.cooldownApplied {
		t.Fatal("expected cooldownApplied=true")
	}
	if refreshHit {
		t.Fatal("refresh endpoint was called for invalidated account")
	}
	if auth["openai"][0].OAuth.Status != config.OAuthStatusInvalidated {
		t.Fatalf("OAuth status = %q, want invalidated", auth["openai"][0].OAuth.Status)
	}
	_, total := p.AvailableKeyCount()
	if total != 0 {
		t.Fatalf("total = %d, want 0: invalidated OAuth key should be excluded", total)
	}
}

func TestMarkKeyCooldown403OAuthNoRefresherPersistsDeactivatedKey(t *testing.T) {
	ctx := context.Background()
	auth := config.AuthConfig{"openai": {{OAuth: &config.OAuthCredential{
		Access:        "oauth-key",
		Refresh:       "refresh-token",
		Expires:       time.Now().Add(time.Hour).UnixMilli(),
		AccountUserID: "user-1__acc-1", AccountID: "acc-1",
	}}}}
	var authMu sync.Mutex
	p := NewProviderConfig("openai", config.ProviderConfig{Type: config.ProviderTypeResponses, Preset: config.ProviderPresetCodex}, []string{"oauth-key"})
	p.SetOAuthRefresher(config.OpenAIOAuthTokenURL, config.OpenAIOAuthClientID, "", "", &auth, &authMu, map[string]OAuthKeySetup{
		"oauth-key": {CredentialIndex: 0, AccountUserID: "user-1__acc-1", AccountID: "acc-1", Expires: auth["openai"][0].OAuth.Expires},
	}, "")

	result := markKeyCooldown(ctx, p, "oauth-key", &APIError{StatusCode: 403, Code: "account_deactivated", Message: "account deactivated"})
	if !result.cooldownApplied {
		t.Fatal("expected cooldownApplied=true")
	}
	if auth["openai"][0].OAuth.Status != config.OAuthStatusDeactivated {
		t.Fatal("expected auth config OAuth credential to be marked deactivated")
	}

	_, _, err := p.SelectKeyWithContext(ctx)
	if err == nil {
		t.Fatal("expected NoUsableKeysError after disabling only OAuth key")
	}
	var noUsable *NoUsableKeysError
	if !errors.As(err, &noUsable) {
		t.Fatalf("expected NoUsableKeysError, got %T: %v", err, err)
	}
}

func TestMarkKeyCooldown403OAuthNoRefresherDeactivatesKey(t *testing.T) {
	ctx := context.Background()
	p := newTestProviderConfig([]string{"oauth-key"})
	p.mu.Lock()
	p.keyStates[0].OAuthInfo = &OAuthKeyInfo{Expires: time.Now().Add(time.Hour).UnixMilli()}
	p.mu.Unlock()
	result := markKeyCooldown(ctx, p, "oauth-key", &APIError{StatusCode: 403, Code: "account_deactivated", Message: "account deactivated"})
	if !result.cooldownApplied {
		t.Fatal("expected cooldownApplied=true")
	}
	_, total := p.AvailableKeyCount()
	if total != 0 {
		t.Fatalf("total = %d, want 0: deactivated OAuth key should be excluded", total)
	}
}

func TestCompleteStreamTerminal401MarksExpiredOAuthBeforeReturning(t *testing.T) {
	ctx := context.Background()
	refreshServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"message":"Your refresh token has already been used to generate a new access token.","code":"refresh_token_reused"}}`)
	}))
	defer refreshServer.Close()

	expires := time.Now().Add(time.Hour).UnixMilli()
	auth := config.AuthConfig{"openai": {{OAuth: &config.OAuthCredential{
		Access:        "oauth-key",
		Refresh:       "refresh-token",
		Expires:       expires,
		AccountUserID: "user-1__acc-1", AccountID: "acc-1",
		Email: "user@example.com",
	}}}}
	var authMu sync.Mutex
	p := NewProviderConfig("openai", config.ProviderConfig{Type: config.ProviderTypeResponses, Preset: config.ProviderPresetCodex}, []string{"oauth-key"})
	p.SetOAuthRefresher(refreshServer.URL, "client-id", "", "", &auth, &authMu, map[string]OAuthKeySetup{
		"oauth-key": {CredentialIndex: 0, AccountUserID: "user-1__acc-1", AccountID: "acc-1", Email: "user@example.com", Expires: expires},
	}, "")
	impl := &constantErrProvider{err: &APIError{StatusCode: 401, Message: "unauthorized"}}
	client := NewClient(p, impl, "model", 128, "")
	client.SetTerminalAPIStatusCodes(401)

	var deltas []message.StreamDelta
	_, err := client.CompleteStream(ctx, []message.Message{{Role: "user", Content: "hello"}}, nil, func(delta message.StreamDelta) {
		deltas = append(deltas, delta)
	})
	if err == nil {
		t.Fatal("expected terminal API error")
	}
	if auth["openai"][0].OAuth.Status != config.OAuthStatusExpired {
		t.Fatalf("expected auth config OAuth credential to be marked expired, got %q", auth["openai"][0].OAuth.Status)
	}
	if impl.calls != 1 {
		t.Fatalf("provider calls = %d, want 1", impl.calls)
	}
	foundExpiredDelta := false
	for _, delta := range deltas {
		if delta.Type == "key_expired" && delta.AccountID == "acc-1" && delta.Email == "user@example.com" {
			foundExpiredDelta = true
			break
		}
	}
	if !foundExpiredDelta {
		t.Fatalf("expected key_expired delta with account metadata, got %#v", deltas)
	}
}

func TestMarkKeyCooldown403OAuthNonDeactivationMessageUsesCooldown(t *testing.T) {
	ctx := context.Background()
	p := newTestProviderConfig([]string{"oauth-key"})
	p.mu.Lock()
	p.keyStates[0].OAuthInfo = &OAuthKeyInfo{Expires: time.Now().Add(time.Hour).UnixMilli()}
	p.mu.Unlock()
	// generic 403 (e.g. from proxy) — message does not match deactivation keywords
	result := markKeyCooldown(ctx, p, "oauth-key", &APIError{StatusCode: 403, Message: "forbidden"})
	if !result.cooldownApplied {
		t.Fatal("expected cooldownApplied=true")
	}
	if result.deactivatedAccountID != "" || result.deactivatedEmail != "" {
		t.Fatal("expected no deactivation for proxy 403")
	}
	_, total := p.AvailableKeyCount()
	if total != 1 {
		t.Fatalf("total = %d, want 1: proxy 403 should not deactivate key", total)
	}
}

func TestMarkKeyCooldown401NonOAuthUsesCooldown(t *testing.T) {
	ctx := context.Background()
	p := newTestProviderConfig([]string{"plain-key"})
	result := markKeyCooldown(ctx, p, "plain-key", &APIError{StatusCode: 401, Message: "unauthorized"})
	if !result.cooldownApplied {
		t.Fatal("expected cooldownApplied=true for plain key 401")
	}
	// plain key should use cooldown, not deactivation — total stays 1
	_, total := p.AvailableKeyCount()
	if total != 1 {
		t.Fatalf("total = %d, want 1: non-OAuth key should not be deactivated", total)
	}
}

func TestClientComplete401OAuthRefreshRotatesToNextKey(t *testing.T) {
	oldAccess := testProviderOAuthJWT(`{"chatgpt_account_id":"acc-1","chatgpt_user_id":"user-1"}`)
	newAccess := testProviderOAuthJWT(`{"chatgpt_account_id":"acc-1","exp":4102444800}`)
	refreshServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"`+newAccess+`","refresh_token":"new-refresh-token","expires_in":3600}`)
	}))
	defer refreshServer.Close()

	creds := []config.ProviderCredential{
		{OAuth: &config.OAuthCredential{Access: oldAccess, Refresh: "refresh-1", Expires: time.Now().Add(time.Hour).UnixMilli(), AccountUserID: "user-1__acc-1", AccountID: "acc-1"}},
		{APIKey: "key-2"},
	}
	auth := config.AuthConfig{"openai": creds}
	var authMu sync.Mutex
	primaryCfg := NewProviderConfig("openai", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"gpt-test": {Limit: config.ModelLimit{Context: 128000, Output: 4096}},
		},
	}, config.ExtractAPIKeys(creds))
	primaryCfg.SetOAuthRefresher(refreshServer.URL, "client-id", "", "", &auth, &authMu, map[string]OAuthKeySetup{creds[0].OAuth.Access: {CredentialIndex: 0, AccountUserID: "user-1__acc-1", AccountID: "acc-1", Expires: creds[0].OAuth.Expires}}, "")

	impl := &recordingProvider{}
	impl.calls = []scriptedCall{
		{err: &APIError{StatusCode: 401, Message: "account deactivated"}},
		{resp: &message.Response{Content: "ok from key-2"}},
	}
	c := NewClient(primaryCfg, impl, "gpt-test", 4096, "sys")

	resp, err := c.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err != nil {
		t.Fatalf("Complete returned error: %v", err)
	}
	if resp == nil || resp.Content != "ok from key-2" {
		t.Fatalf("unexpected response: %#v", resp)
	}
	if len(impl.apiKeys) != 2 {
		t.Fatalf("expected 2 provider calls, got %d", len(impl.apiKeys))
	}
	if impl.apiKeys[0] != oldAccess {
		t.Fatalf("first call key = %q, want old access", impl.apiKeys[0])
	}
	if impl.apiKeys[1] != "key-2" {
		t.Fatalf("second call key = %q, want key-2", impl.apiKeys[1])
	}
}

func TestClientCompleteStreamVisibleInterruptionRetriesSameKey(t *testing.T) {
	primaryCfg := testProviderConfigWithKeys("primary-prov", "gpt-test", []string{"k1", "k2"})
	impl := &recordingProvider{}
	impl.calls = []scriptedCall{
		{streams: []message.StreamDelta{{Type: "text", Text: "partial"}}, err: io.ErrUnexpectedEOF},
		{resp: &message.Response{Content: "ok from same key"}},
	}
	c := NewClient(primaryCfg, impl, "gpt-test", 4096, "sys")

	resp, err := c.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err != nil {
		t.Fatalf("CompleteStream returned error: %v", err)
	}
	if resp == nil || resp.Content != "ok from same key" {
		t.Fatalf("unexpected response: %#v", resp)
	}
	if len(impl.apiKeys) != 2 {
		t.Fatalf("expected 2 provider calls, got %d", len(impl.apiKeys))
	}
	if impl.apiKeys[0] != "k1" || impl.apiKeys[1] != "k1" {
		t.Fatalf("expected same key retry after visible interruption, got %#v", impl.apiKeys)
	}
	healthy, total := primaryCfg.HealthyKeyCount()
	if total != 2 || healthy != 2 {
		t.Fatalf("HealthyKeyCount = %d/%d, want 2/2 after same-key recovery", healthy, total)
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
	var coolingErr *AllKeysCoolingError
	if !errors.As(err, &coolingErr) {
		t.Fatalf("CompleteStream err = %v, want AllKeysCoolingError", err)
	}
	if resp != nil {
		t.Fatalf("resp = %#v, want nil on capped cooling failure", resp)
	}
	if got := impl.CallCount(); got != 0 {
		t.Fatalf("provider calls = %d, want 0 while key is cooling and stream_retry_rounds=1", got)
	}
}

func TestClientCompleteStreamConnectionEstablishmentTimeoutRetriesProviderNextRound(t *testing.T) {
	cfg := testProviderConfigWithKeys("primary-prov", "primary-model", []string{"k1"})
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
	got := clampEffectiveMaxTokens(model, 128000, 128000, RequestTuning{}, "", messages, nil, 0)
	if got != 128000 {
		t.Fatalf("clampEffectiveMaxTokens() = %d, want 128000", got)
	}
}

func TestClampEffectiveMaxTokensReasoningStillRespectsGlobalOutputCap(t *testing.T) {
	model := config.ModelConfig{Limit: config.ModelLimit{Context: 400000, Input: 272000, Output: 128000}}
	got := clampEffectiveMaxTokens(model, 128000, 32000, RequestTuning{OpenAI: OpenAITuning{ReasoningEffort: "high"}}, "", []message.Message{{Role: "user", Content: "hi"}}, nil, 0)
	if got != 32000 {
		t.Fatalf("clampEffectiveMaxTokens() = %d, want 32000", got)
	}
}

func TestInputLimitForModelRefDerivesContextMinusOutputBudget(t *testing.T) {
	cfg := NewProviderConfig("prov", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"model": {Limit: config.ModelLimit{Context: 400000, Output: 128000}},
		},
	}, []string{"k"})
	c := NewClient(cfg, &scriptedProvider{}, "model", 128000, "")

	if got := c.InputLimitForModelRef("prov/model"); got != 368000 {
		t.Fatalf("default InputLimitForModelRef() = %d, want 368000", got)
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
			"fallback-model": {Limit: config.ModelLimit{Context: 400000, Output: 128000}},
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
		InputLimit:       368000,
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
}

func TestClassifyFallbackReasonUsesContextLengthExceededCode(t *testing.T) {
	err := &APIError{StatusCode: 400, Code: "context_length_exceeded", Message: "input is too long"}
	if got := classifyFallbackReason(err); got != "context_length_exceeded" {
		t.Fatalf("classifyFallbackReason() = %q, want context_length_exceeded", got)
	}
}

func TestCompleteStreamWithRetryKeepsRetryingWhileAllKeysCooling(t *testing.T) {
	primaryCfg := testProviderConfigWithKeys("primary-prov", "gpt-test", []string{"k1"})
	primaryCfg.MarkCooldown("k1", 20*time.Millisecond)
	impl := &recordingProvider{}
	impl.calls = []scriptedCall{
		{resp: &message.Response{Content: "ok after cooldown"}},
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
	fallbackCfg.MarkCooldown("k2", time.Second)
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
	got := mergeRoundWait(0, time.Minute)
	got = mergeRoundWait(got, time.Second)
	if got != time.Second {
		t.Fatalf("mergeRoundWait(1m, 1s) = %v, want 1s", got)
	}
	got = mergeRoundWait(0, 0)
	if got != time.Second {
		t.Fatalf("mergeRoundWait(0, 0) = %v, want 1s clamp", got)
	}
	got = mergeRoundWait(0, 2*time.Minute)
	if got != time.Minute {
		t.Fatalf("mergeRoundWait(0, 2m) = %v, want 1m cap", got)
	}

	got = mergePendingRoundWait(0, 0)
	if got != 0 {
		t.Fatalf("mergePendingRoundWait(0, 0) = %v, want 0 (no synthetic wait)", got)
	}
	got = mergePendingRoundWait(2*time.Second, 0)
	if got != 2*time.Second {
		t.Fatalf("mergePendingRoundWait(2s, 0) = %v, want 2s", got)
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

	got := clampEffectiveMaxTokens(
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

	got := clampEffectiveMaxTokens(
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

	var coolingDetails []string
	resp, err := c.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "hi"}}, nil, func(delta message.StreamDelta) {
		if delta.Type == "status" && delta.Status != nil && delta.Status.Type == "cooling" {
			coolingDetails = append(coolingDetails, delta.Status.Detail)
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
}

func TestClientCompleteStream401OAuthRefreshRotatesToNextKey(t *testing.T) {
	oldAccess := testProviderOAuthJWT(`{"chatgpt_account_id":"acc-1","chatgpt_user_id":"user-1"}`)
	newAccess := testProviderOAuthJWT(`{"chatgpt_account_id":"acc-1","exp":4102444800}`)
	refreshServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"`+newAccess+`","refresh_token":"new-refresh-token","expires_in":3600}`)
	}))
	defer refreshServer.Close()

	creds := []config.ProviderCredential{
		{OAuth: &config.OAuthCredential{Access: oldAccess, Refresh: "refresh-1", Expires: time.Now().Add(time.Hour).UnixMilli(), AccountUserID: "user-1__acc-1", AccountID: "acc-1"}},
		{APIKey: "key-2"},
	}
	auth := config.AuthConfig{"openai": creds}
	var authMu sync.Mutex
	primaryCfg := NewProviderConfig("openai", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"gpt-test": {Limit: config.ModelLimit{Context: 128000, Output: 4096}},
		},
	}, config.ExtractAPIKeys(creds))
	primaryCfg.SetOAuthRefresher(refreshServer.URL, "client-id", "", "", &auth, &authMu, map[string]OAuthKeySetup{creds[0].OAuth.Access: {CredentialIndex: 0, AccountUserID: "user-1__acc-1", AccountID: "acc-1", Expires: creds[0].OAuth.Expires}}, "")

	impl := &recordingProvider{}
	impl.calls = []scriptedCall{
		{err: &APIError{StatusCode: 401, Message: "account deactivated"}},
		{resp: &message.Response{Content: "ok from key-2"}},
	}
	c := NewClient(primaryCfg, impl, "gpt-test", 4096, "sys")

	resp, err := c.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err != nil {
		t.Fatalf("CompleteStream returned error: %v", err)
	}
	if resp == nil || resp.Content != "ok from key-2" {
		t.Fatalf("unexpected response: %#v", resp)
	}
	if len(impl.apiKeys) != 2 {
		t.Fatalf("expected 2 provider calls, got %d", len(impl.apiKeys))
	}
	if impl.apiKeys[0] != oldAccess {
		t.Fatalf("first call key = %q, want old access", impl.apiKeys[0])
	}
	if impl.apiKeys[1] != "key-2" {
		t.Fatalf("second call key = %q, want key-2", impl.apiKeys[1])
	}
}

func TestClientComplete403OAuthRefreshRotatesToNextKey(t *testing.T) {
	oldAccess := testProviderOAuthJWT(`{"chatgpt_account_id":"acc-1","chatgpt_user_id":"user-1"}`)
	newAccess := testProviderOAuthJWT(`{"chatgpt_account_id":"acc-1","exp":4102444800}`)
	refreshServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"`+newAccess+`","refresh_token":"new-refresh-token","expires_in":3600}`)
	}))
	defer refreshServer.Close()

	creds := []config.ProviderCredential{
		{OAuth: &config.OAuthCredential{Access: oldAccess, Refresh: "refresh-1", Expires: time.Now().Add(time.Hour).UnixMilli(), AccountUserID: "user-1__acc-1", AccountID: "acc-1"}},
		{APIKey: "key-2"},
	}
	auth := config.AuthConfig{"openai": creds}
	var authMu sync.Mutex
	primaryCfg := NewProviderConfig("openai", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"gpt-test": {Limit: config.ModelLimit{Context: 128000, Output: 4096}},
		},
	}, config.ExtractAPIKeys(creds))
	primaryCfg.SetOAuthRefresher(refreshServer.URL, "client-id", "", "", &auth, &authMu, map[string]OAuthKeySetup{creds[0].OAuth.Access: {CredentialIndex: 0, AccountUserID: "user-1__acc-1", AccountID: "acc-1", Expires: creds[0].OAuth.Expires}}, "")

	impl := &recordingProvider{}
	impl.calls = []scriptedCall{
		{err: &APIError{StatusCode: 403, Message: "forbidden"}},
		{resp: &message.Response{Content: "ok from key-2"}},
	}
	c := NewClient(primaryCfg, impl, "gpt-test", 4096, "sys")

	resp, err := c.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err != nil {
		t.Fatalf("CompleteStream returned error: %v", err)
	}
	if resp == nil || resp.Content != "ok from key-2" {
		t.Fatalf("unexpected response: %#v", resp)
	}
	if len(impl.apiKeys) != 2 {
		t.Fatalf("expected 2 provider calls, got %d", len(impl.apiKeys))
	}
	if impl.apiKeys[0] != oldAccess {
		t.Fatalf("first call key = %q, want old access", impl.apiKeys[0])
	}
	if impl.apiKeys[1] != "key-2" {
		t.Fatalf("second call key = %q, want key-2", impl.apiKeys[1])
	}
}

func TestClientCompleteStreamDeactivatedOnlyOAuthKeyReturnsNoUsableKeys(t *testing.T) {
	auth := config.AuthConfig{"openai": {{OAuth: &config.OAuthCredential{
		Access:    "deactivated-oauth-key",
		Refresh:   "refresh-token",
		Expires:   time.Now().Add(time.Hour).UnixMilli(),
		AccountID: "acc-deactivated",
		Status:    config.OAuthStatusDeactivated,
	}}}}
	var authMu sync.Mutex
	primaryCfg := NewProviderConfig("openai", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"gpt-test": {Limit: config.ModelLimit{Context: 128000, Output: 4096}},
		},
	}, []string{"deactivated-oauth-key"})
	primaryCfg.SetOAuthRefresher(config.OpenAIOAuthTokenURL, config.OpenAIOAuthClientID, "", "", &auth, &authMu, map[string]OAuthKeySetup{
		"deactivated-oauth-key": {CredentialIndex: 0, AccountID: "acc-deactivated", Expires: auth["openai"][0].OAuth.Expires, Status: config.OAuthStatusDeactivated},
	}, "")

	impl := &recordingProvider{}
	c := NewClient(primaryCfg, impl, "gpt-test", 4096, "sys")

	_, err := c.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err == nil {
		t.Fatal("expected NoUsableKeysError")
	}
	var noUsable *NoUsableKeysError
	if !errors.As(err, &noUsable) {
		t.Fatalf("expected NoUsableKeysError, got %T: %v", err, err)
	}
	if len(impl.apiKeys) != 0 {
		t.Fatalf("expected no provider calls, got %d", len(impl.apiKeys))
	}
}

func TestNewClientCodexWarmupProbesMultipleAccounts(t *testing.T) {
	seen := make(chan string, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("ChatGPT-Account-ID"); got != "" {
			select {
			case seen <- got:
			default:
			}
		}
		if got := r.URL.Path; got != "/backend-api/wham/usage" {
			t.Errorf("request path = %q, want /backend-api/wham/usage", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"rate_limit":{"primary_window":{"used_percent":20,"reset_after_seconds":3600},"secondary_window":{"used_percent":40,"reset_after_seconds":7200}},"credits":{"has_credits":true,"unlimited":false}}`)
	}))
	defer server.Close()

	expires := time.Now().Add(time.Hour).UnixMilli()
	creds := []config.ProviderCredential{
		{OAuth: &config.OAuthCredential{Access: testProviderOAuthJWT(`{"chatgpt_account_id":"acc-a","exp":4102444800}`), Refresh: "refresh-a", Expires: expires, AccountID: "acc-a"}},
		// Insert an API key between OAuth slots so slot index != OAuth credential index.
		{APIKey: "api-key-1"},
		{OAuth: &config.OAuthCredential{Access: testProviderOAuthJWT(`{"chatgpt_account_id":"acc-b","exp":4102444800}`), Refresh: "refresh-b", Expires: expires, AccountID: "acc-b"}},
	}
	auth := config.AuthConfig{"openai": creds}
	var authMu sync.Mutex
	prov := NewProviderConfig("openai", config.ProviderConfig{
		Type:   config.ProviderTypeResponses,
		APIURL: server.URL + "/backend-api/codex/responses",
		Preset: config.ProviderPresetCodex,
		Models: map[string]config.ModelConfig{
			"gpt-5.5": {Limit: config.ModelLimit{Context: 128000, Output: 1024}},
		},
	}, config.ExtractAPIKeys(creds))
	prov.SetOAuthRefresher(config.OpenAIOAuthTokenURL, config.OpenAIOAuthClientID, "", "", &auth, &authMu, map[string]OAuthKeySetup{
		creds[0].OAuth.Access: {CredentialIndex: 0, AccountID: "acc-a", Expires: expires},
		creds[2].OAuth.Access: {CredentialIndex: 2, AccountID: "acc-b", Expires: expires},
	}, "")
	prov.StartCodexRateLimitPolling(func(string, string) ([]*ratelimit.KeyRateLimitSnapshot, error) {
		return nil, nil
	})

	_ = NewClient(prov, noopProvider{}, "gpt-5.5", 1024, "")

	got := map[string]bool{}
	deadline := time.After(2 * time.Second)
	for len(got) < 2 {
		select {
		case accountID := <-seen:
			got[accountID] = true
		case <-deadline:
			t.Fatalf("warmup probes = %#v, want both accounts", got)
		}
	}
	for _, want := range []string{"acc-a", "acc-b"} {
		if !got[want] {
			t.Fatalf("warmup probes = %#v, want account %s", got, want)
		}
	}
}

func TestNewClientCodexWarmupCancelsOnInvalidateRouting(t *testing.T) {
	started := make(chan string, 1)
	canceled := make(chan struct{})
	var canceledOnce sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("ChatGPT-Account-ID"); got != "" {
			select {
			case started <- got:
			default:
			}
		}
		<-r.Context().Done()
		canceledOnce.Do(func() {
			close(canceled)
		})
	}))
	defer server.Close()

	expires := time.Now().Add(time.Hour).UnixMilli()
	creds := []config.ProviderCredential{
		{OAuth: &config.OAuthCredential{Access: testProviderOAuthJWT(`{"chatgpt_account_id":"acc-a","exp":4102444800}`), Refresh: "refresh-a", Expires: expires, AccountID: "acc-a"}},
		{OAuth: &config.OAuthCredential{Access: testProviderOAuthJWT(`{"chatgpt_account_id":"acc-b","exp":4102444800}`), Refresh: "refresh-b", Expires: expires, AccountID: "acc-b"}},
	}
	auth := config.AuthConfig{"openai": creds}
	var authMu sync.Mutex
	prov := NewProviderConfig("openai", config.ProviderConfig{
		Type:   config.ProviderTypeResponses,
		APIURL: server.URL + "/backend-api/codex/responses",
		Preset: config.ProviderPresetCodex,
		Models: map[string]config.ModelConfig{
			"gpt-5.5": {Limit: config.ModelLimit{Context: 128000, Output: 1024}},
		},
	}, config.ExtractAPIKeys(creds))
	prov.SetOAuthRefresher(config.OpenAIOAuthTokenURL, config.OpenAIOAuthClientID, "", "", &auth, &authMu, map[string]OAuthKeySetup{
		creds[0].OAuth.Access: {CredentialIndex: 0, AccountID: "acc-a", Expires: expires},
		creds[1].OAuth.Access: {CredentialIndex: 1, AccountID: "acc-b", Expires: expires},
	}, "")
	prov.StartCodexRateLimitPolling(func(string, string) ([]*ratelimit.KeyRateLimitSnapshot, error) {
		return nil, nil
	})

	c := NewClient(prov, noopProvider{}, "gpt-5.5", 1024, "")

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("warmup request did not start")
	}

	c.InvalidateRouting("model_client_swapped")

	select {
	case <-canceled:
	case <-time.After(2 * time.Second):
		t.Fatal("warmup request was not cancelled after InvalidateRouting")
	}
}

func TestClientCompleteStream401OAuthRefreshThenFallback(t *testing.T) {
	oldAccess := testProviderOAuthJWT(`{"chatgpt_account_id":"acc-1","chatgpt_user_id":"user-1"}`)
	newAccess := testProviderOAuthJWT(`{"chatgpt_account_id":"acc-1","exp":4102444800}`)
	refreshServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"`+newAccess+`","refresh_token":"new-refresh-token","expires_in":3600}`)
	}))
	defer refreshServer.Close()

	creds := []config.ProviderCredential{
		{OAuth: &config.OAuthCredential{Access: oldAccess, Refresh: "refresh-1", Expires: time.Now().Add(time.Hour).UnixMilli(), AccountUserID: "user-1__acc-1", AccountID: "acc-1"}},
		{APIKey: "key-2"},
	}
	auth := config.AuthConfig{"openai": creds}
	var authMu sync.Mutex
	primaryCfg := NewProviderConfig("openai", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"gpt-test": {Limit: config.ModelLimit{Context: 128000, Output: 4096}},
		},
	}, config.ExtractAPIKeys(creds))
	primaryCfg.SetOAuthRefresher(refreshServer.URL, "client-id", "", "", &auth, &authMu, map[string]OAuthKeySetup{creds[0].OAuth.Access: {CredentialIndex: 0, AccountUserID: "user-1__acc-1", AccountID: "acc-1", Expires: creds[0].OAuth.Expires}}, "")
	fallbackCfg := testProviderConfig("fallback-prov", "fallback-model")

	primaryImpl := &recordingProvider{}
	primaryImpl.calls = []scriptedCall{
		{err: &APIError{StatusCode: 401, Message: "account deactivated"}},
		{err: &APIError{StatusCode: 401, Message: "invalid api key"}},
	}
	fallbackImpl := &recordingProvider{}
	fallbackImpl.calls = []scriptedCall{{resp: &message.Response{Content: "ok from fallback"}}}
	c := NewClient(primaryCfg, primaryImpl, "gpt-test", 4096, "sys")
	c.SetFallbackModels([]FallbackModel{{
		ProviderConfig: fallbackCfg,
		ProviderImpl:   fallbackImpl,
		ModelID:        "fallback-model",
		MaxTokens:      4096,
		ContextLimit:   128000,
	}})

	resp, err := c.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err != nil {
		t.Fatalf("CompleteStream returned error: %v", err)
	}
	if resp == nil || resp.Content != "ok from fallback" {
		t.Fatalf("unexpected response: %#v", resp)
	}
	if len(primaryImpl.apiKeys) != 2 {
		t.Fatalf("expected 2 initial-entry calls, got %d", len(primaryImpl.apiKeys))
	}
	if primaryImpl.apiKeys[0] != oldAccess || primaryImpl.apiKeys[1] != "key-2" {
		t.Fatalf("initial-entry keys = %#v, want [old access key-2]", primaryImpl.apiKeys)
	}
	if len(fallbackImpl.apiKeys) != 1 {
		t.Fatalf("expected 1 fallback call, got %d", len(fallbackImpl.apiKeys))
	}
	st := c.LastCallStatus()
	if !st.FallbackTriggered {
		t.Fatal("expected fallback to trigger")
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

func TestModelPoolStickyCursorPreservesPinnedFallbackVariant(t *testing.T) {
	primaryCfg := testProviderConfig("primary-prov", "primary-model")
	fallbackCfg := NewProviderConfig("qt", config.ProviderConfig{
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
	if st1.RunningModelRef != "qt/gpt-5.5@xhigh" {
		t.Fatalf("first RunningModelRef = %q, want qt/gpt-5.5@xhigh", st1.RunningModelRef)
	}

	resp2, err := c.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "hi-2"}}, nil, nil)
	if err != nil {
		t.Fatalf("second CompleteStream() error = %v", err)
	}
	if resp2 == nil || resp2.Content != "fallback second" {
		t.Fatalf("second response = %#v, want fallback second", resp2)
	}
	st2 := c.LastCallStatus()
	if st2.SelectedModelRef != "qt/gpt-5.5@xhigh" {
		t.Fatalf("second SelectedModelRef = %q, want qt/gpt-5.5@xhigh", st2.SelectedModelRef)
	}
	if st2.RunningModelRef != "qt/gpt-5.5@xhigh" {
		t.Fatalf("second RunningModelRef = %q, want qt/gpt-5.5@xhigh", st2.RunningModelRef)
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

func TestClientCompleteStopsAfterAnthropicThinkingReplay400WithoutExtraRounds(t *testing.T) {
	primaryCfg := testProviderConfig("primary-prov", "primary-model")
	impl := &constantErrProvider{err: &APIError{StatusCode: 400, Message: "The `content[].thinking` in the thinking mode must be passed back to the API."}}

	c := NewClient(primaryCfg, impl, "primary-model", 4096, "sys")
	_, err := c.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != 400 {
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
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != 400 {
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
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != 400 {
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
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != 400 {
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

func TestClientSetVariantResetsModelDefaultsBeforeApplyingNewVariant(t *testing.T) {
	cfg := NewProviderConfig("anthropic", config.ProviderConfig{
		Type: config.ProviderTypeMessages,
		Models: map[string]config.ModelConfig{
			"claude": {
				Limit: config.ModelLimit{Context: 128000, Output: 4096},
				Thinking: &config.ThinkingConfig{
					Type:    "adaptive",
					Effort:  "medium",
					Display: "summarized",
				},
				Variants: map[string]config.ModelVariant{
					"high": {
						Thinking: &config.ThinkingConfig{Effort: "high"},
					},
					"hidden": {
						Thinking: &config.ThinkingConfig{Display: "omitted"},
					},
				},
			},
		},
	}, []string{"k"})
	c := NewClient(cfg, &scriptedProvider{}, "claude", 4096, "sys")

	c.SetVariant("high")
	if got := c.tuning.Anthropic.ThinkingEffort; got != "high" {
		t.Fatalf("after @high effort = %q, want high", got)
	}
	if got := c.tuning.Anthropic.ThinkingDisplay; got != "summarized" {
		t.Fatalf("after @high display = %q, want summarized", got)
	}

	c.SetVariant("hidden")
	if got := c.tuning.Anthropic.ThinkingEffort; got != "medium" {
		t.Fatalf("after switching to @hidden effort = %q, want model default medium", got)
	}
	if got := c.tuning.Anthropic.ThinkingDisplay; got != "omitted" {
		t.Fatalf("after switching to @hidden display = %q, want omitted", got)
	}
}

func TestClientSetVariantBudgetOnlyDoesNotImplicitlySwitchThinkingType(t *testing.T) {
	cfg := NewProviderConfig("anthropic", config.ProviderConfig{
		Type: config.ProviderTypeMessages,
		Models: map[string]config.ModelConfig{
			"claude": {
				Limit: config.ModelLimit{Context: 128000, Output: 4096},
				Thinking: &config.ThinkingConfig{
					Type:    "adaptive",
					Effort:  "medium",
					Display: "summarized",
				},
				Variants: map[string]config.ModelVariant{
					"manual": {
						Thinking: &config.ThinkingConfig{Budget: 12000},
					},
				},
			},
		},
	}, []string{"k"})
	c := NewClient(cfg, &scriptedProvider{}, "claude", 4096, "sys")

	c.SetVariant("manual")
	if got := c.tuning.Anthropic.ThinkingType; got != "adaptive" {
		t.Fatalf("after @manual type = %q, want adaptive", got)
	}
	if got := c.tuning.Anthropic.ThinkingBudget; got != 0 {
		t.Fatalf("after @manual budget = %d, want 0", got)
	}
	if got := c.tuning.Anthropic.ThinkingEffort; got != "medium" {
		t.Fatalf("after @manual effort = %q, want medium", got)
	}
	if got := c.tuning.Anthropic.ThinkingDisplay; got != "summarized" {
		t.Fatalf("after @manual display = %q, want summarized", got)
	}
}

func TestClientSetVariantMergesPromptCacheOverrides(t *testing.T) {
	cacheToolsFalse := false
	cfg := NewProviderConfig("anthropic", config.ProviderConfig{
		Type: config.ProviderTypeMessages,
		Models: map[string]config.ModelConfig{
			"claude": {
				Limit: config.ModelLimit{Context: 128000, Output: 4096},
				PromptCache: &config.PromptCacheConfig{
					Mode:       "auto",
					TTL:        "1h",
					CacheTools: new(cacheToolsFalse),
				},
				Variants: map[string]config.ModelVariant{
					"explicit-tools": {
						PromptCache: &config.PromptCacheConfig{
							Mode:       "explicit",
							CacheTools: new(true),
						},
					},
				},
			},
		},
	}, []string{"k"})
	c := NewClient(cfg, &scriptedProvider{}, "claude", 4096, "sys")

	c.SetVariant("explicit-tools")
	if got := c.tuning.Anthropic.PromptCacheMode; got != "explicit" {
		t.Fatalf("PromptCacheMode = %q, want explicit", got)
	}
	if got := c.tuning.Anthropic.PromptCacheTTL; got != "1h" {
		t.Fatalf("PromptCacheTTL = %q, want inherited 1h", got)
	}
	if !c.tuning.Anthropic.CacheTools {
		t.Fatal("CacheTools = false, want true")
	}
}

func TestClientSetVariantOverridesParallelToolCalls(t *testing.T) {
	cfg := NewProviderConfig("openai", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"gpt-5.5": {
				Limit:             config.ModelLimit{Context: 400000, Output: 128000},
				ParallelToolCalls: new(false),
				Variants: map[string]config.ModelVariant{
					"parallel": {
						ParallelToolCalls: new(true),
					},
				},
			},
		},
	}, []string{"k"})
	c := NewClient(cfg, &scriptedProvider{}, "gpt-5.5", 4096, "sys")

	if c.tuning.OpenAI.ParallelToolCalls == nil || *c.tuning.OpenAI.ParallelToolCalls {
		t.Fatalf("default parallel_tool_calls = %#v, want false", c.tuning.OpenAI.ParallelToolCalls)
	}

	c.SetVariant("parallel")
	if c.tuning.OpenAI.ParallelToolCalls == nil || !*c.tuning.OpenAI.ParallelToolCalls {
		t.Fatalf("after @parallel parallel_tool_calls = %#v, want true", c.tuning.OpenAI.ParallelToolCalls)
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

func TestCompleteStreamAppliesActiveVariantOpenAITuning(t *testing.T) {
	provider := &recordingTuningProvider{}
	cfg := NewProviderConfig("openai", config.ProviderConfig{
		Type: config.ProviderTypeResponses,
		Models: map[string]config.ModelConfig{
			"gpt-5.5": {
				Limit:     config.ModelLimit{Context: 400000, Input: 272000, Output: 128000},
				Reasoning: &config.ReasoningConfig{Summary: "auto"},
				Variants: map[string]config.ModelVariant{
					"xhigh": {Reasoning: &config.ReasoningConfig{Effort: "xhigh"}},
				},
			},
		},
	}, []string{"k"})
	c := NewClient(cfg, provider, "gpt-5.5", 4096, "sys")
	c.SetVariant("xhigh")

	if _, err := c.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "hi"}}, nil, nil); err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}
	if len(provider.tuning) != 1 {
		t.Fatalf("len(provider.tuning) = %d, want 1", len(provider.tuning))
	}
	got := provider.tuning[0].OpenAI
	if got.ReasoningEffort != "xhigh" || got.ReasoningSummary != "auto" {
		t.Fatalf("OpenAI tuning = %+v, want reasoning effort xhigh summary auto", got)
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

func TestClientServiceTierSkipsUnsupportedModel(t *testing.T) {
	cfg := testProviderConfig("openai", "gpt-5-codex")
	expect := RequestTuning{Anthropic: AnthropicTuning{PromptCacheMode: "explicit"}}
	impl := &scriptedProvider{calls: []scriptedCall{{resp: &message.Response{Content: "ok"}, expectTuning: &expect}}}
	c := NewClient(cfg, impl, "gpt-5-codex", 4096, "sys")
	c.SetStreamRetryRounds(1)
	c.SetServiceTier(config.ServiceTierFast)

	if _, err := c.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "hi"}}, nil, nil); err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}
}

func TestClientServiceTierDefaultsToCodexPreset(t *testing.T) {
	cfg := NewProviderConfig("openai", config.ProviderConfig{
		Type:   config.ProviderTypeResponses,
		Preset: config.ProviderPresetCodex,
		Models: map[string]config.ModelConfig{
			"gpt-5-codex": {Limit: config.ModelLimit{Context: 128000, Output: 4096}},
		},
	}, []string{"test-key"})
	expect := RequestTuning{
		Anthropic:             AnthropicTuning{PromptCacheMode: "explicit", ServiceTier: "fast"},
		OpenAI:                OpenAITuning{ServiceTier: "fast"},
		SupportedServiceTiers: map[config.ServiceTier]bool{config.ServiceTierFast: true, config.ServiceTierSlow: true},
	}
	impl := &scriptedProvider{calls: []scriptedCall{{resp: &message.Response{Content: "ok"}, expectTuning: &expect}}}
	c := NewClient(cfg, impl, "gpt-5-codex", 4096, "sys")
	c.SetStreamRetryRounds(1)
	c.SetServiceTier(config.ServiceTierFast)

	if _, err := c.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "hi"}}, nil, nil); err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}
}

func TestClientServiceTierOverridesRequestTuning(t *testing.T) {
	cfg := testFastProviderConfig("openai", "gpt-5-codex")
	expect := RequestTuning{
		Anthropic:             AnthropicTuning{PromptCacheMode: "explicit", ServiceTier: "fast"},
		OpenAI:                OpenAITuning{ServiceTier: "fast"},
		SupportedServiceTiers: map[config.ServiceTier]bool{config.ServiceTierFast: true},
	}
	impl := &scriptedProvider{calls: []scriptedCall{{resp: &message.Response{Content: "ok"}, expectTuning: &expect}}}
	c := NewClient(cfg, impl, "gpt-5-codex", 4096, "sys")
	c.SetStreamRetryRounds(1)
	c.SetServiceTier(config.ServiceTierFast)

	if _, err := c.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "hi"}}, nil, nil); err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}
}

func TestClientServiceTierAppliesToFallbackTuning(t *testing.T) {
	primaryCfg := testProviderConfig("primary", "primary-model")
	fallbackCfg := testFastProviderConfig("fallback", "gpt-5-codex")
	primaryImpl := &scriptedProvider{calls: []scriptedCall{{err: &APIError{StatusCode: 503, Message: "try fallback"}}}}
	expect := RequestTuning{
		Anthropic: AnthropicTuning{PromptCacheMode: "explicit", ServiceTier: "fast"},
		OpenAI:    OpenAITuning{ServiceTier: "fast"},

		SupportedServiceTiers: map[config.ServiceTier]bool{config.ServiceTierFast: true},
	}
	fallbackImpl := &scriptedProvider{calls: []scriptedCall{{resp: &message.Response{Content: "ok"}, expectTuning: &expect}}}
	c := NewClient(primaryCfg, primaryImpl, "primary-model", 4096, "sys")
	c.SetStreamRetryRounds(1)
	c.SetModelPool([]FallbackModel{
		{ProviderConfig: primaryCfg, ProviderImpl: primaryImpl, ModelID: "primary-model", MaxTokens: 4096, ContextLimit: 128000},
		{ProviderConfig: fallbackCfg, ProviderImpl: fallbackImpl, ModelID: "gpt-5-codex", MaxTokens: 4096, ContextLimit: 128000},
	}, 0)
	c.SetServiceTier(config.ServiceTierFast)

	if _, err := c.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "hi"}}, nil, nil); err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}
}

func TestClientServiceTierToggleAppliesToNextRetryRound(t *testing.T) {
	cfg := testFastProviderConfig("openai", "gpt-5-codex")
	expectFast := RequestTuning{
		Anthropic: AnthropicTuning{PromptCacheMode: "explicit", ServiceTier: "fast"},
		OpenAI:    OpenAITuning{ServiceTier: "fast"},

		SupportedServiceTiers: map[config.ServiceTier]bool{config.ServiceTierFast: true},
	}
	c := NewClient(cfg, nil, "gpt-5-codex", 4096, "sys")
	disableRetryDelayForTest(cfg)
	impl := &serviceTierToggleRetryProvider{client: c, expectFast: expectFast}
	c.providerImpl = impl

	resp, err := c.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}
	if resp == nil || resp.Content != "ok" {
		t.Fatalf("resp = %#v, want ok", resp)
	}
	if impl.calls != 2 {
		t.Fatalf("provider calls = %d, want 2", impl.calls)
	}
	if got := c.LastCallStatus().ServiceTier; got != config.ServiceTierFast {
		t.Fatalf("LastCallStatus().ServiceTier = %q, want fast from successful retry round", got)
	}
}

func TestClientLastCallStatusKeepsActualServiceTierWhenTierChangesDuringRequest(t *testing.T) {
	cfg := testFastProviderConfig("openai", "gpt-5-codex")
	c := NewClient(cfg, nil, "gpt-5-codex", 4096, "sys")
	c.SetStreamRetryRounds(1)
	c.SetServiceTier(config.ServiceTierFast)
	c.providerImpl = &serviceTierSwitchingProvider{client: c}

	resp, err := c.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}
	if resp == nil || resp.Content != "ok" {
		t.Fatalf("resp = %#v, want ok", resp)
	}
	if got := c.ServiceTier(); got != config.ServiceTierStandard {
		t.Fatalf("current ServiceTier() = %q, want standard after provider switched it", got)
	}
	if got := c.LastCallStatus().ServiceTier; got != config.ServiceTierFast {
		t.Fatalf("LastCallStatus().ServiceTier = %q, want actual request tier fast", got)
	}
}

func TestClientServiceTierSet(t *testing.T) {
	c := NewClient(testProviderConfig("openai", "gpt-5-codex"), &scriptedProvider{}, "gpt-5-codex", 4096, "sys")
	if got := c.ServiceTier(); got != config.ServiceTierStandard {
		t.Fatalf("ServiceTier() = %q, want standard by default", got)
	}
	c.SetServiceTier(config.ServiceTierFast)
	if got := c.ServiceTier(); got != config.ServiceTierFast {
		t.Fatalf("ServiceTier() = %q, want fast", got)
	}
	c.SetServiceTier(config.ServiceTierStandard)
	if got := c.ServiceTier(); got != config.ServiceTierStandard {
		t.Fatalf("ServiceTier() = %q, want standard", got)
	}
}

func TestClientNextRequestTuningOverrideIsOneShot(t *testing.T) {
	provider := &recordingTuningProvider{}
	cfg := NewProviderConfig("openai", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"gpt-5.5": {
				Limit:             config.ModelLimit{Context: 400000, Output: 128000},
				Reasoning:         &config.ReasoningConfig{Effort: "high", Summary: "auto"},
				Text:              &config.TextConfig{Verbosity: "low"},
				ParallelToolCalls: new(true),
			},
		},
	}, []string{"k"})
	c := NewClient(cfg, provider, "gpt-5.5", 4096, "sys")
	parallelFalse := false
	parallelTrue := true
	c.SetNextRequestTuningOverride(RequestTuning{OpenAI: OpenAITuning{ParallelToolCalls: &parallelFalse}})
	if _, err := c.CompleteStream(context.Background(), nil, nil, nil); err != nil {
		t.Fatalf("first Complete: %v", err)
	}
	if _, err := c.CompleteStream(context.Background(), nil, nil, nil); err != nil {
		t.Fatalf("second Complete: %v", err)
	}
	if len(provider.tuning) != 2 {
		t.Fatalf("len(provider.tuning) = %d, want 2", len(provider.tuning))
	}
	if provider.tuning[0].OpenAI.ParallelToolCalls == nil || *provider.tuning[0].OpenAI.ParallelToolCalls != parallelFalse {
		t.Fatalf("first tuning = %#v, want parallel_tool_calls=false", provider.tuning[0])
	}
	if got := provider.tuning[0].OpenAI; got.ReasoningEffort != "high" || got.ReasoningSummary != "auto" || got.TextVerbosity != "low" {
		t.Fatalf("first tuning OpenAI = %+v, want model defaults preserved", got)
	}
	if provider.tuning[1].OpenAI.ParallelToolCalls == nil || *provider.tuning[1].OpenAI.ParallelToolCalls != parallelTrue {
		t.Fatalf("second tuning = %#v, want parallel_tool_calls=true", provider.tuning[1])
	}
}

func TestClientEmptyNextRequestTuningOverridePreservesModelDefaults(t *testing.T) {
	tests := []struct {
		name       string
		providerID string
		provider   config.ProviderConfig
		modelID    string
		assert     func(t *testing.T, tuning RequestTuning)
	}{
		{
			name:       "openai",
			providerID: "openai",
			provider: config.ProviderConfig{
				Type: config.ProviderTypeResponses,
				Models: map[string]config.ModelConfig{
					"gpt-5.5": {
						Limit:     config.ModelLimit{Context: 400000, Output: 128000},
						Reasoning: &config.ReasoningConfig{Effort: "high", Summary: "auto"},
						Text:      &config.TextConfig{Verbosity: "low"},
					},
				},
			},
			modelID: "gpt-5.5",
			assert: func(t *testing.T, tuning RequestTuning) {
				t.Helper()
				got := tuning.OpenAI
				if got.ReasoningEffort != "high" || got.ReasoningSummary != "auto" || got.TextVerbosity != "low" {
					t.Fatalf("OpenAI tuning = %+v, want model defaults preserved", got)
				}
			},
		},
		{
			name:       "anthropic",
			providerID: "anthropic",
			provider: config.ProviderConfig{
				Type: config.ProviderTypeMessages,
				Models: map[string]config.ModelConfig{
					"claude-sonnet": {
						Limit: config.ModelLimit{Context: 200000, Output: 8192},
						Thinking: &config.ThinkingConfig{
							Type:    "adaptive",
							Effort:  "medium",
							Display: "summarized",
						},
						PromptCache: &config.PromptCacheConfig{
							Mode:       "auto",
							TTL:        "1h",
							CacheTools: new(true),
						},
					},
				},
			},
			modelID: "claude-sonnet",
			assert: func(t *testing.T, tuning RequestTuning) {
				t.Helper()
				got := tuning.Anthropic
				if got.ThinkingType != "adaptive" || got.ThinkingEffort != "medium" || got.ThinkingDisplay != "summarized" {
					t.Fatalf("Anthropic thinking tuning = %+v, want model defaults preserved", got)
				}
				if got.PromptCacheMode != "auto" || got.PromptCacheTTL != "1h" || !got.CacheTools {
					t.Fatalf("Anthropic prompt cache tuning = %+v, want model defaults preserved", got)
				}
			},
		},
		{
			name:       "gemini",
			providerID: "gemini",
			provider: config.ProviderConfig{
				Type: config.ProviderTypeGenerateContent,
				Models: map[string]config.ModelConfig{
					"gemini-test": {
						Limit: config.ModelLimit{Context: 1000000, Output: 8192},
						Thinking: &config.ThinkingConfig{
							Budget:          1024,
							Level:           "high",
							IncludeThoughts: new(true),
						},
					},
				},
			},
			modelID: "gemini-test",
			assert: func(t *testing.T, tuning RequestTuning) {
				t.Helper()
				got := tuning.Gemini
				if got.ThinkingBudget == nil || *got.ThinkingBudget != 1024 {
					t.Fatalf("Gemini thinking budget = %#v, want 1024", got.ThinkingBudget)
				}
				if got.ThinkingLevel != "high" {
					t.Fatalf("Gemini thinking level = %q, want high", got.ThinkingLevel)
				}
				if got.IncludeThoughts == nil || !*got.IncludeThoughts {
					t.Fatalf("Gemini include thoughts = %#v, want true", got.IncludeThoughts)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := &recordingTuningProvider{}
			cfg := NewProviderConfig(tt.providerID, tt.provider, []string{"k"})
			c := NewClient(cfg, provider, tt.modelID, 4096, "sys")
			c.SetNextRequestTuningOverride(RequestTuning{})

			if _, err := c.CompleteStream(context.Background(), nil, nil, nil); err != nil {
				t.Fatalf("Complete: %v", err)
			}
			if len(provider.tuning) != 1 {
				t.Fatalf("len(provider.tuning) = %d, want 1", len(provider.tuning))
			}
			tt.assert(t, provider.tuning[0])
		})
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

func TestCompleteStreamWithRetryStopsOnRoutingInvalidationDuringBackoff(t *testing.T) {
	cfg := testProviderConfigWithKeys("primary-prov", "primary-model", []string{"k1"})
	impl := &recordingProvider{}
	impl.calls = []scriptedCall{{err: &APIError{StatusCode: 500, Message: "retry later"}}}
	c := NewClient(cfg, impl, "primary-model", 4096, "sys")

	resultCh := make(chan error, 1)
	go func() {
		_, err := c.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "hi"}}, nil, nil)
		resultCh <- err
	}()

	time.Sleep(100 * time.Millisecond)
	c.InvalidateRouting("model_pool_changed")

	select {
	case err := <-resultCh:
		if !IsRoutingInvalidated(err) {
			t.Fatalf("CompleteStream err = %v, want routing invalidated", err)
		}
		if got := impl.CallCount(); got != 1 {
			t.Fatalf("provider calls = %d, want 1 after invalidation aborts retry chain", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for CompleteStream to stop after routing invalidation")
	}
}

func TestCompleteStreamReturnsSuccessWhenRoutingChangesDuringSuccessfulAttempt(t *testing.T) {
	cfg := testProviderConfigWithKeys("primary-prov", "primary-model", []string{"k1"})
	impl := &recordingProvider{}
	impl.calls = []scriptedCall{{
		streams: []message.StreamDelta{{Type: "text", Text: "visible"}},
		resp:    &message.Response{Content: "ok"},
	}}
	c := NewClient(cfg, impl, "primary-model", 4096, "sys")

	resp, err := c.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "hi"}}, nil, func(delta message.StreamDelta) {
		if delta.Type == "text" {
			c.InvalidateRouting("model_pool_changed")
		}
	})
	if err != nil {
		t.Fatalf("CompleteStream returned error: %v", err)
	}
	if resp == nil || resp.Content != "ok" {
		t.Fatalf("unexpected response: %#v", resp)
	}
}

func TestCompleteStreamFallbackVariantCarriesPromptCacheTuning(t *testing.T) {
	expect := RequestTuning{Anthropic: AnthropicTuning{PromptCacheMode: "auto", PromptCacheTTL: "1h", CacheTools: true}}
	primaryCfg := NewProviderConfig("primary", config.ProviderConfig{
		Type: config.ProviderTypeMessages,
		Models: map[string]config.ModelConfig{
			"claude-primary": {Limit: config.ModelLimit{Context: 128000, Output: 4096}},
		},
	}, []string{"k1"})
	fallbackCfg := NewProviderConfig("fallback", config.ProviderConfig{
		Type: config.ProviderTypeMessages,
		Models: map[string]config.ModelConfig{
			"claude-fallback": {
				Limit: config.ModelLimit{Context: 128000, Output: 4096},
				PromptCache: &config.PromptCacheConfig{
					Mode:       "explicit",
					TTL:        "1h",
					CacheTools: new(false),
				},
				Variants: map[string]config.ModelVariant{
					"fastcache": {
						PromptCache: &config.PromptCacheConfig{
							Mode:       "auto",
							CacheTools: new(true),
						},
					},
				},
			},
		},
	}, []string{"k2"})
	primaryImpl := &scriptedProvider{calls: []scriptedCall{{err: &APIError{StatusCode: 500, Message: "upstream unavailable"}}}}
	fallbackImpl := &scriptedProvider{calls: []scriptedCall{{resp: &message.Response{Content: "fallback ok"}, expectTuning: &expect}}}
	c := NewClient(primaryCfg, primaryImpl, "claude-primary", 4096, "sys")
	c.SetFallbackModels([]FallbackModel{{
		ProviderConfig: fallbackCfg,
		ProviderImpl:   fallbackImpl,
		ModelID:        "claude-fallback",
		MaxTokens:      4096,
		ContextLimit:   128000,
		Variant:        "fastcache",
	}})

	resp, err := c.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}
	if resp == nil || resp.Content != "fallback ok" {
		t.Fatalf("unexpected response: %#v", resp)
	}
}
