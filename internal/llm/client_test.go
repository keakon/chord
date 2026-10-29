package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sync"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
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
	messages  [][]message.Message
}

type rejectBinaryPartsProvider struct {
	mu      sync.Mutex
	count   int
	lastMsg []message.Message
	resp    *message.Response
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
	c.mu.RLock()
	generation := c.routingGeneration.Load()
	changedCh := c.routingChangedCh
	c.mu.RUnlock()
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
		nil,
		cb,
		fallbackEnabled,
		fallbackModels,
		maxAttempts,
		status,
		generation,
		changedCh,
		nil,
		nil,
		nil,
	)
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
	p.messages = append(p.messages, cloneMessagesForTest(messages))
	p.mu.Unlock()
	return p.scriptedProvider.CompleteStream(ctx, apiKey, model, systemPrompt, messages, tools, maxTokens, tuning, cb)
}

func (p *rejectBinaryPartsProvider) CompleteStream(
	_ context.Context,
	_ string,
	_ string,
	_ string,
	messages []message.Message,
	_ []message.ToolDefinition,
	_ int,
	_ RequestTuning,
	_ StreamCallback,
) (*message.Response, error) {
	p.mu.Lock()
	p.count++
	p.lastMsg = cloneMessagesForTest(messages)
	p.mu.Unlock()
	for _, msg := range messages {
		for _, part := range msg.Parts {
			if part.Type == message.ContentPartImage || part.Type == message.ContentPartPDF {
				return nil, &APIError{StatusCode: 400, Message: "unexpected binary part in request"}
			}
		}
	}
	if p.resp != nil {
		return p.resp, nil
	}
	return &message.Response{Content: "ok"}, nil
}

func (p *rejectBinaryPartsProvider) CallCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.count
}

func (p *rejectBinaryPartsProvider) LastMessages() []message.Message {
	p.mu.Lock()
	defer p.mu.Unlock()
	return cloneMessagesForTest(p.lastMsg)
}

func cloneMessagesForTest(in []message.Message) []message.Message {
	if len(in) == 0 {
		return nil
	}
	out := make([]message.Message, len(in))
	for i, msg := range in {
		out[i] = msg
		if len(msg.Parts) > 0 {
			out[i].Parts = make([]message.ContentPart, len(msg.Parts))
			for j, part := range msg.Parts {
				out[i].Parts[j] = part
				if len(part.Data) > 0 {
					out[i].Parts[j].Data = append([]byte(nil), part.Data...)
				}
			}
		}
		if len(msg.ToolCalls) > 0 {
			out[i].ToolCalls = make([]message.ToolCall, len(msg.ToolCalls))
			for j, tc := range msg.ToolCalls {
				out[i].ToolCalls[j] = tc
				if len(tc.Args) > 0 {
					out[i].ToolCalls[j].Args = append(json.RawMessage(nil), tc.Args...)
				}
			}
		}
		if len(msg.ThinkingBlocks) > 0 {
			out[i].ThinkingBlocks = append([]message.ThinkingBlock(nil), msg.ThinkingBlocks...)
		}
		if msg.Provenance != nil {
			cp := *msg.Provenance
			out[i].Provenance = &cp
		}
	}
	return out
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
		Type:           config.ProviderTypeResponses,
		APIURL:         "https://api.openai.com/v1/responses",
		TrustHTTP400:   new(true),
		RetryAfterMaxS: new(config.OfficialRetryAfterMaxS),
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
		Type:         config.ProviderTypeResponses,
		APIURL:       "https://gateway.example.com/v1/responses",
		TrustHTTP400: new(false),
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
