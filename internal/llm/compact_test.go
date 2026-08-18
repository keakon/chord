package llm

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
)

type compactTestCall struct {
	key       string
	model     string
	maxTokens int
	tuning    RequestTuning
}

type compactTestProvider struct {
	mu        sync.Mutex
	errs      []error
	progress  []message.StreamProgressDelta
	response  *message.Response
	compactAt int
	calls     []compactTestCall
}

type compactUnsupportedProvider struct{}

func (compactUnsupportedProvider) CompleteStream(
	context.Context,
	string,
	string,
	string,
	[]message.Message,
	[]message.ToolDefinition,
	int,
	RequestTuning,
	StreamCallback,
) (*message.Response, error) {
	return nil, nil
}

func (p *compactTestProvider) CompleteStream(
	context.Context,
	string,
	string,
	string,
	[]message.Message,
	[]message.ToolDefinition,
	int,
	RequestTuning,
	StreamCallback,
) (*message.Response, error) {
	return nil, nil
}

func (p *compactTestProvider) Compact(
	_ context.Context,
	apiKey string,
	model string,
	_ string,
	_ []message.Message,
	_ []message.ToolDefinition,
	maxTokens int,
	tuning RequestTuning,
	cb StreamCallback,
) (*message.Response, error) {
	p.mu.Lock()
	p.calls = append(p.calls, compactTestCall{key: apiKey, model: model, maxTokens: maxTokens, tuning: tuning})
	callIndex := p.compactAt
	var progress message.StreamProgressDelta
	if callIndex < len(p.progress) {
		progress = p.progress[callIndex]
	}
	if p.compactAt < len(p.errs) {
		err := p.errs[p.compactAt]
		p.compactAt++
		p.mu.Unlock()
		if cb != nil && progress != (message.StreamProgressDelta{}) {
			cb(message.StreamDelta{Progress: &progress})
		}
		return nil, err
	}
	p.compactAt++
	response := p.response
	p.mu.Unlock()
	if cb != nil && progress != (message.StreamProgressDelta{}) {
		cb(message.StreamDelta{Progress: &progress})
	}
	return response, nil
}

func (p *compactTestProvider) callsSnapshot() []compactTestCall {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]compactTestCall(nil), p.calls...)
}

func newCompactTestProviderConfig(name, model string, keys []string) *ProviderConfig {
	return NewProviderConfig(name, config.ProviderConfig{
		Type: config.ProviderTypeResponses,
		Models: map[string]config.ModelConfig{
			model: {Limit: config.ModelLimit{Context: 8192, Output: 1024}},
		},
	}, keys)
}

func TestClientCompactTriesEveryTargetKeyBeforeFallback(t *testing.T) {
	primaryConfig := newCompactTestProviderConfig("primary", "model-1", []string{"key-1", "key-2"})
	primary := &compactTestProvider{errs: []error{
		&APIError{StatusCode: 500, Message: "temporary failure"},
		&APIError{StatusCode: 500, Message: "temporary failure"},
	}}
	fallbackConfig := newCompactTestProviderConfig("fallback", "model-2", []string{"fallback-key"})
	fallback := &compactTestProvider{response: &message.Response{Content: "summary"}}
	client := NewClient(primaryConfig, primary, "model-1", 512, "system")
	client.SetFallbackModels([]FallbackModel{{
		ProviderConfig: fallbackConfig,
		ProviderImpl:   fallback,
		ModelID:        "model-2",
		MaxTokens:      256,
	}})

	resp, err := client.Compact(context.Background(), []message.Message{{Role: message.RoleUser, Content: "compact"}}, nil, nil)
	if err != nil {
		t.Fatalf("Compact() error = %v", err)
	}
	if resp == nil || resp.Content != "summary" {
		t.Fatalf("Compact() response = %#v, want fallback summary", resp)
	}
	primaryCalls := primary.callsSnapshot()
	if len(primaryCalls) != 2 || primaryCalls[0].key != "key-1" || primaryCalls[1].key != "key-2" {
		t.Fatalf("primary compact calls = %#v, want key-1 then key-2", primaryCalls)
	}
	fallbackCalls := fallback.callsSnapshot()
	if len(fallbackCalls) != 1 || fallbackCalls[0].key != "fallback-key" {
		t.Fatalf("fallback compact calls = %#v, want one fallback-key call", fallbackCalls)
	}
	if fallbackCalls[0].maxTokens != 256 {
		t.Fatalf("fallback max tokens = %d, want 256", fallbackCalls[0].maxTokens)
	}
	if got := client.RunningModelRef(); got != "fallback/model-2" {
		t.Fatalf("running model ref = %q, want fallback/model-2", got)
	}
	if _, err := client.Compact(context.Background(), []message.Message{{Role: message.RoleUser, Content: "compact again"}}, nil, nil); err != nil {
		t.Fatalf("second Compact() error = %v", err)
	}
	if got := len(primary.callsSnapshot()); got != 2 {
		t.Fatalf("primary compact calls after fallback success = %d, want cursor pinned at fallback", got)
	}
}

func TestClientCompactSignalsRetryBoundaryBeforeNextAttempt(t *testing.T) {
	providerConfig := newCompactTestProviderConfig("provider", "model", []string{"key-1", "key-2"})
	provider := &compactTestProvider{
		errs:     []error{&APIError{StatusCode: 500, Message: "temporary failure"}},
		progress: []message.StreamProgressDelta{{Bytes: 12, Events: 2}, {Bytes: 4, Events: 1}},
		response: &message.Response{Content: "summary"},
	}
	client := NewClient(providerConfig, provider, "model", 512, "system")

	var deltas []message.StreamDelta
	if _, err := client.Compact(
		context.Background(),
		[]message.Message{{Role: message.RoleUser, Content: "compact"}},
		nil,
		func(delta message.StreamDelta) { deltas = append(deltas, delta) },
	); err != nil {
		t.Fatalf("Compact() error = %v", err)
	}
	if len(deltas) != 3 {
		t.Fatalf("callback deltas = %#v, want progress, retrying, progress", deltas)
	}
	if deltas[0].Progress == nil || deltas[1].Status == nil || deltas[1].Status.Type != "retrying" || deltas[2].Progress == nil {
		t.Fatalf("callback deltas = %#v, want progress, retrying, progress", deltas)
	}
}

func TestClientCompactSkipsUnsupportedCurrentTarget(t *testing.T) {
	primaryConfig := newCompactTestProviderConfig("primary", "model-1", []string{"key-1"})
	fallbackConfig := newCompactTestProviderConfig("fallback", "model-2", []string{"key-2"})
	fallback := &compactTestProvider{response: &message.Response{Content: "summary"}}
	client := NewClient(primaryConfig, compactUnsupportedProvider{}, "model-1", 512, "system")
	client.SetFallbackModels([]FallbackModel{{
		ProviderConfig: fallbackConfig,
		ProviderImpl:   fallback,
		ModelID:        "model-2",
		MaxTokens:      256,
	}})

	resp, err := client.Compact(context.Background(), []message.Message{{Role: message.RoleUser, Content: "compact"}}, nil, nil)
	if err != nil {
		t.Fatalf("Compact() error = %v", err)
	}
	if resp == nil || resp.Content != "summary" || len(fallback.callsSnapshot()) != 1 {
		t.Fatalf("Compact() response/calls = %#v/%#v, want fallback summary", resp, fallback.callsSnapshot())
	}
}

func TestClientSupportsCompactEndpointAcrossModelPool(t *testing.T) {
	primaryConfig := newCompactTestProviderConfig("primary", "model-1", []string{"key-1"})
	fallbackConfig := NewProviderConfig("codex", config.ProviderConfig{
		Preset: config.ProviderPresetCodex,
		Type:   config.ProviderTypeResponses,
		Models: map[string]config.ModelConfig{
			"model-2": {Limit: config.ModelLimit{Context: 8192, Output: 1024}},
		},
	}, []string{"key-2"})
	client := NewClient(primaryConfig, compactUnsupportedProvider{}, "model-1", 512, "system")
	client.SetFallbackModels([]FallbackModel{{
		ProviderConfig: fallbackConfig,
		ProviderImpl:   &compactTestProvider{response: &message.Response{Content: "summary"}},
		ModelID:        "model-2",
		MaxTokens:      256,
	}})

	if !client.SupportsCompactEndpoint() {
		t.Fatal("SupportsCompactEndpoint() = false, want fallback Codex capability")
	}
}

func TestClientCompactRetriesRefreshedOAuthKey(t *testing.T) {
	oldAccess := testProviderOAuthJWT(`{"chatgpt_account_id":"acc-1","chatgpt_user_id":"user-1"}`)
	newAccess := testProviderOAuthJWT(`{"chatgpt_account_id":"acc-1","chatgpt_user_id":"user-1","exp":4102444800}`)
	refreshServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"`+newAccess+`","refresh_token":"new-refresh-token","expires_in":3600}`)
	}))
	defer refreshServer.Close()

	creds := []config.ProviderCredential{{OAuth: &config.OAuthCredential{
		Access:  oldAccess,
		Refresh: "old-refresh-token",
		Expires: time.Now().Add(time.Hour).UnixMilli(),
	}}}
	auth := config.AuthConfig{"provider": creds}
	var authMu sync.Mutex
	providerConfig := newCompactTestProviderConfig("provider", "model", config.ExtractAPIKeys(creds))
	providerConfig.SetOAuthRefresher(refreshServer.URL, "client-id", "", "", &auth, &authMu, map[string]OAuthKeySetup{
		oldAccess: {CredentialIndex: 0, AccountUserID: "user-1__acc-1", AccountID: "acc-1", Expires: creds[0].OAuth.Expires},
	}, "")
	provider := &compactTestProvider{
		errs:     []error{&APIError{StatusCode: 401, Message: "unauthorized"}},
		response: &message.Response{Content: "summary"},
	}
	client := NewClient(providerConfig, provider, "model", 512, "system")

	if _, err := client.Compact(context.Background(), []message.Message{{Role: message.RoleUser, Content: "compact"}}, nil, nil); err != nil {
		t.Fatalf("Compact() error = %v", err)
	}
	calls := provider.callsSnapshot()
	if len(calls) != 2 || calls[0].key != oldAccess || calls[1].key != newAccess {
		t.Fatalf("compact OAuth calls = %#v, want old then refreshed access token", calls)
	}
}

func TestClientCompactCoolsRateLimitedKeyBeforeRetry(t *testing.T) {
	providerConfig := newCompactTestProviderConfig("provider", "model", []string{"key-1", "key-2"})
	provider := &compactTestProvider{
		errs:     []error{&APIError{StatusCode: 429, Message: "rate limited"}},
		response: &message.Response{Content: "summary"},
	}
	client := NewClient(providerConfig, provider, "model", 512, "system")

	if _, err := client.Compact(context.Background(), []message.Message{{Role: message.RoleUser, Content: "compact"}}, nil, nil); err != nil {
		t.Fatalf("Compact() error = %v", err)
	}
	calls := provider.callsSnapshot()
	if len(calls) != 2 || calls[0].key != "key-1" || calls[1].key != "key-2" {
		t.Fatalf("compact calls = %#v, want key-1 then key-2", calls)
	}
	if available, total := providerConfig.AvailableKeyCount(); available != 1 || total != 2 {
		t.Fatalf("available keys = %d/%d, want 1/2 after rate-limit cooldown", available, total)
	}
}

func TestClientCompactPreservesActiveVariantTuning(t *testing.T) {
	providerConfig := NewProviderConfig("provider", config.ProviderConfig{
		Type: config.ProviderTypeResponses,
		Models: map[string]config.ModelConfig{
			"model": {
				Limit: config.ModelLimit{Context: 8192, Output: 1024},
				Variants: map[string]config.ModelVariant{
					"high": {Reasoning: &config.ReasoningConfig{Effort: "high"}},
				},
			},
		},
	}, []string{"key"})
	provider := &compactTestProvider{response: &message.Response{Content: "summary"}}
	client := NewClient(providerConfig, provider, "model", 512, "system")
	client.SetVariant("high")

	if _, err := client.Compact(context.Background(), []message.Message{{Role: message.RoleUser, Content: "compact"}}, nil, nil); err != nil {
		t.Fatalf("Compact() error = %v", err)
	}
	calls := provider.callsSnapshot()
	if len(calls) != 1 || calls[0].tuning.OpenAI.ReasoningEffort != "high" {
		t.Fatalf("compact calls = %#v, want active high reasoning variant", calls)
	}
}
