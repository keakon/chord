package llm

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
)

func testProviderOAuthJWT(payload string) string {
	return "e30." + base64.RawURLEncoding.EncodeToString([]byte(payload)) + ".sig"
}

func newTestProviderConfig(keys []string) *ProviderConfig {
	cfg := config.ProviderConfig{
		Type:   config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{},
	}
	return NewProviderConfig("test", cfg, keys)
}

func TestProviderConfigTimeoutSettings(t *testing.T) {
	p := NewProviderConfig("test", config.ProviderConfig{
		Type:                      config.ProviderTypeResponses,
		ResponseHeaderTimeout:     180,
		StreamIdleTimeout:         90,
		WebSocketHandshakeTimeout: 45,
	}, nil)
	if got := p.ResponseHeaderTimeout(); got != 180*time.Second {
		t.Fatalf("ResponseHeaderTimeout() = %v, want 180s", got)
	}
	if got := p.StreamIdleTimeout(); got != 90*time.Second {
		t.Fatalf("StreamIdleTimeout() = %v, want 90s", got)
	}
	if got := p.WebSocketHandshakeTimeout(); got != 45*time.Second {
		t.Fatalf("WebSocketHandshakeTimeout() = %v, want 45s", got)
	}
}

func TestNewProviderImplAppliesResponseHeaderTimeout(t *testing.T) {
	p := NewProviderConfig("test", config.ProviderConfig{
		Type:                  config.ProviderTypeResponses,
		ResponseHeaderTimeout: 180,
	}, nil)
	impl, err := NewProviderImpl(p, "")
	if err != nil {
		t.Fatalf("NewProviderImpl: %v", err)
	}
	r, ok := impl.(*ResponsesProvider)
	if !ok {
		t.Fatalf("impl = %T, want *ResponsesProvider", impl)
	}
	if r.client == nil {
		t.Fatal("responses HTTP client is nil")
	}
	if r.client.Timeout != 0 {
		t.Fatalf("responses HTTP total timeout = %v, want 0 for streaming", r.client.Timeout)
	}
	transport, ok := r.client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("responses HTTP transport = %T, want *http.Transport", r.client.Transport)
	}
	if transport.ResponseHeaderTimeout != 180*time.Second {
		t.Fatalf("responses response header timeout = %v, want 180s", transport.ResponseHeaderTimeout)
	}
}

func TestProviderDiagnosticsWritersAreRaceSafe(t *testing.T) {
	t.Parallel()

	openAI := &OpenAIProvider{responsesProvider: &ResponsesProvider{}}
	responses := &ResponsesProvider{}
	anthropic := &AnthropicProvider{}
	gemini := &GeminiProvider{}
	providers := []struct {
		name string
		p    Provider
		load func()
	}{
		{
			name: "openai",
			p:    openAI,
			load: func() {
				_ = openAI.dumpWriter.Load()
				_ = openAI.traceWriter.Load()
				_ = openAI.responsesProvider.dumpWriter.Load()
				_ = openAI.responsesProvider.traceWriter.Load()
			},
		},
		{
			name: "responses",
			p:    responses,
			load: func() {
				_ = responses.dumpWriter.Load()
				_ = responses.traceWriter.Load()
			},
		},
		{
			name: "anthropic",
			p:    anthropic,
			load: func() {
				_ = anthropic.dumpWriter.Load()
				_ = anthropic.traceWriter.Load()
			},
		},
		{
			name: "gemini",
			p:    gemini,
			load: func() {
				_ = gemini.dumpWriter.Load()
				_ = gemini.traceWriter.Load()
			},
		},
	}

	for _, tc := range providers {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			start := make(chan struct{})
			var wg sync.WaitGroup
			wg.Add(2)
			go func() {
				defer wg.Done()
				<-start
				for range 10_000 {
					SetProviderDumpWriter(tc.p, NewDumpWriter("dump"))
					SetProviderTraceWriter(tc.p, NewTraceWriter("trace.jsonl"))
				}
			}()
			go func() {
				defer wg.Done()
				<-start
				for range 10_000 {
					tc.load()
				}
			}()
			close(start)
			wg.Wait()
		})
	}
}

// TestProviderAuthSchemeConcurrentReadIsRaceSafe guards the lock-free
// AuthScheme accessor: authScheme is set once in NewProviderConfig and never
// mutated, so it is read without p.mu. Run under -race to confirm concurrent
// reads (and reads racing with other locked accessors) stay clean.
func TestProviderAuthSchemeConcurrentReadIsRaceSafe(t *testing.T) {
	t.Parallel()

	p := NewProviderConfig("custom", config.ProviderConfig{
		Type:       config.ProviderTypeMessages,
		APIURL:     "https://example.invalid/v1/messages",
		AuthScheme: config.AuthSchemeBearer,
	}, []string{"test-key"})
	if got := p.AuthScheme(); got != config.AuthSchemeBearer {
		t.Fatalf("AuthScheme() = %q, want %q", got, config.AuthSchemeBearer)
	}

	start := make(chan struct{})
	var wg sync.WaitGroup
	const readers = 8
	wg.Add(readers)
	for range readers {
		go func() {
			defer wg.Done()
			<-start
			for range 10_000 {
				if got := p.AuthScheme(); got != config.AuthSchemeBearer {
					t.Errorf("AuthScheme() = %q, want %q", got, config.AuthSchemeBearer)
					return
				}
				// Read a lock-taking accessor in the same loop so the race
				// detector also observes the lock-free read alongside a locked one.
				_ = p.Preset()
			}
		}()
	}
	close(start)
	wg.Wait()
}

func TestNewProviderConfig_PreservesValidPerRequestRandom(t *testing.T) {
	p := NewProviderConfig("openai", config.ProviderConfig{Type: config.ProviderTypeResponses, KeyRotation: config.KeyRotationPerRequest, KeyOrder: config.KeyOrderRandom}, []string{"k1", "k2"})
	if p.keyRotation != config.KeyRotationPerRequest {
		t.Fatalf("keyRotation = %q, want %q", p.keyRotation, config.KeyRotationPerRequest)
	}
	if p.keyOrder != config.KeyOrderRandom {
		t.Fatalf("keyOrder = %q, want %q", p.keyOrder, config.KeyOrderRandom)
	}
}

func selectCoolingError(t *testing.T, p *ProviderConfig) *AllKeysCoolingError {
	t.Helper()
	_, _, err := p.SelectKeyWithContext(context.Background())
	cooling, ok := errors.AsType[*AllKeysCoolingError](err)
	if !ok {
		t.Fatalf("SelectKeyWithContext err = %v, want *AllKeysCoolingError", err)
	}
	return cooling
}

// --- NewProviderConfig ---

func TestNewProviderConfig_NoDefaultURLForProviderTypes(t *testing.T) {
	tests := []string{
		config.ProviderTypeChatCompletions,
		config.ProviderTypeMessages,
		config.ProviderTypeGenerateContent,
	}

	for _, providerType := range tests {
		cfg := config.ProviderConfig{Type: providerType}
		p := NewProviderConfig("test", cfg, []string{"key"})
		if p.APIURL() != "" {
			t.Errorf("type=%q: got URL %q, want empty", providerType, p.APIURL())
		}
	}
}

func TestNewProviderConfig_CustomURL(t *testing.T) {
	cfg := config.ProviderConfig{
		Type:   config.ProviderTypeChatCompletions,
		APIURL: "https://custom.api.example.com/v1/chat",
	}
	p := NewProviderConfig("test", cfg, []string{"key"})
	if p.APIURL() != "https://custom.api.example.com/v1/chat" {
		t.Errorf("expected custom URL, got %s", p.APIURL())
	}
}

func TestNewProviderConfig_OpenAICodexPreset_DefaultURL(t *testing.T) {
	cfg, _, err := config.NormalizeProviderPreset(config.ProviderConfig{
		Type:   config.ProviderTypeResponses,
		Preset: config.ProviderPresetCodex,
	})
	if err != nil {
		t.Fatalf("NormalizeProviderPreset: %v", err)
	}
	p := NewProviderConfig("test", cfg, []string{"key"})
	if p.APIURL() != config.OpenAICodexResponsesURL {
		t.Fatalf("expected OpenAI Codex OAuth default URL %q, got %q", config.OpenAICodexResponsesURL, p.APIURL())
	}
}

func TestNewProviderConfig_KeyStatesInitialized(t *testing.T) {
	keys := []string{"a", "b", "c"}
	p := NewProviderConfig("test", config.ProviderConfig{Type: config.ProviderTypeChatCompletions}, keys)

	if len(p.keyStates) != 3 {
		t.Fatalf("expected 3 key states, got %d", len(p.keyStates))
	}
	for i, ks := range p.keyStates {
		if ks.Key != keys[i] {
			t.Errorf("keyStates[%d].Key = %q, want %q", i, ks.Key, keys[i])
		}
		if !ks.LastUsed.IsZero() {
			t.Errorf("keyStates[%d].LastUsed should be zero, got %v", i, ks.LastUsed)
		}
		if !ks.CooldownEnd.IsZero() {
			t.Errorf("keyStates[%d].CooldownEnd should be zero, got %v", i, ks.CooldownEnd)
		}
	}
}

// --- Accessors ---

func TestProviderConfig_Accessors(t *testing.T) {
	cfg := config.ProviderConfig{
		Type:   config.ProviderTypeMessages,
		APIURL: "https://test.example.com",
		Models: map[string]config.ModelConfig{
			"model-1": {Name: "Model One", Limit: config.ModelLimit{Context: 100000, Output: 4096}},
		},
	}
	p := NewProviderConfig("my-provider", cfg, []string{"key"})

	if p.Name() != "my-provider" {
		t.Errorf("Name() = %q, want %q", p.Name(), "my-provider")
	}
	if p.Type() != config.ProviderTypeMessages {
		t.Errorf("Type() = %q, want %q", p.Type(), config.ProviderTypeMessages)
	}

	m, ok := p.GetModel("model-1")
	if !ok {
		t.Fatal("GetModel(model-1) should return true")
	}
	if m.Limit.Context != 100000 {
		t.Errorf("model context = %d, want %d", m.Limit.Context, 100000)
	}

	_, ok = p.GetModel("nonexistent")
	if ok {
		t.Fatal("GetModel(nonexistent) should return false")
	}
}

func TestIsContextLengthExceeded(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "openai code",
			err:  &APIError{StatusCode: 400, Code: "context_length_exceeded", Message: "context_length_exceeded"},
			want: true,
		},
		{
			name: "openai message",
			err:  &APIError{StatusCode: 400, Message: "This model's maximum context length is 128000 tokens"},
			want: true,
		},
		{
			name: "anthropic prompt too long",
			err:  &APIError{StatusCode: 400, Message: "prompt is too long: 250000 tokens > 200000 max"},
			want: true,
		},
		{
			name: "ordinary 400",
			err:  &APIError{StatusCode: 400, Message: "invalid tool schema"},
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsContextLengthExceeded(tc.err); got != tc.want {
				t.Fatalf("IsContextLengthExceeded(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
