package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
)

func TestResponsesProvider_ResponsesWireHeaders(t *testing.T) {
	var gotHeaders http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"type":"response.completed","response":{"id":"resp-1","status":"completed","output":[],"usage":{"input_tokens":5,"output_tokens":2}}}`+"\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	providerCfg := NewProviderConfig("codex", config.ProviderConfig{
		Type:   config.ProviderTypeResponses,
		Preset: config.ProviderPresetCodex,
		APIURL: server.URL + "/v1/responses",
	}, []string{"test-key"})
	r := &ResponsesProvider{provider: providerCfg, client: server.Client()}

	_, err := r.CompleteStream(
		context.Background(), "test-key", "gpt-5.5", "",
		[]message.Message{{Role: "user", Content: "hello"}},
		nil, 0, RequestTuning{},
		func(message.StreamDelta) {},
	)
	if err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}

	if got := gotHeaders.Get("originator"); got != "codex_cli_rs" {
		t.Errorf("originator = %q, want codex_cli_rs", got)
	}
	if got := gotHeaders.Get("User-Agent"); got != defaultLLMUserAgent() {
		t.Errorf("User-Agent = %q, want %q", got, defaultLLMUserAgent())
	}
	if got := gotHeaders.Get("OpenAI-Beta"); got != "responses=experimental" {
		t.Errorf("OpenAI-Beta = %q, want responses=experimental", got)
	}
}

func TestResponsesProvider_GenericResponsesUsesResponsesWireHeaders(t *testing.T) {
	var gotHeaders http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"type":"response.completed","response":{"id":"resp-1","status":"completed","output":[],"usage":{"input_tokens":5,"output_tokens":2}}}`+"\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	// Generic Responses provider (no preset) still uses the shared Responses
	// header surface; preset:codex only changes runtime capabilities such as
	// OAuth/WebSocket/rate-limit handling.
	providerCfg := NewProviderConfig("generic", config.ProviderConfig{
		Type:   config.ProviderTypeResponses,
		APIURL: server.URL + "/v1/responses",
	}, []string{"test-key"})
	r := &ResponsesProvider{provider: providerCfg, client: server.Client()}

	_, err := r.CompleteStream(
		context.Background(), "test-key", "gpt-5.5", "",
		[]message.Message{{Role: "user", Content: "hello"}},
		nil, 0, RequestTuning{},
		func(message.StreamDelta) {},
	)
	if err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}

	if got := gotHeaders.Get("originator"); got != "codex_cli_rs" {
		t.Errorf("originator = %q, want codex_cli_rs", got)
	}
	if got := gotHeaders.Get("User-Agent"); got != defaultLLMUserAgent() {
		t.Errorf("User-Agent = %q, want %q", got, defaultLLMUserAgent())
	}
	if got := gotHeaders.Get("OpenAI-Beta"); got != "responses=experimental" {
		t.Errorf("OpenAI-Beta = %q, want responses=experimental", got)
	}
}

func TestResponsesProvider_EquivalentAzureWireConfig(t *testing.T) {
	var gotHeaders http.Header
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"type":"response.completed","response":{"id":"resp-1","status":"completed","output":[],"usage":{"input_tokens":5,"output_tokens":2}}}`+"\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	// Azure wire behavior uses a plain Responses provider plus request
	// overrides: api-key auth, store: true, and no Codex identity headers.
	overrides := &config.RequestOverridesConfig{
		Headers: map[string]*string{
			"OpenAI-Beta": nil,
			"originator":  nil,
		},
	}
	providerCfg := NewProviderConfig("azure-equivalent", config.ProviderConfig{
		Type:       config.ProviderTypeResponses,
		APIURL:     server.URL + "/openai/v1/responses?api-version=preview",
		AuthScheme: config.AuthSchemeAPIKey,
		Store:      new(true),
		Compat:     &config.ProviderCompatConfig{RequestOverrides: overrides},
	}, []string{"test-key"})
	r := &ResponsesProvider{provider: providerCfg, client: server.Client()}

	_, err := r.CompleteStream(
		context.Background(), "test-key", "gpt-5.5", "",
		[]message.Message{{Role: "user", Content: "hello"}},
		nil, 0, RequestTuning{},
		func(message.StreamDelta) {},
	)
	if err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}
	if got := gotHeaders.Get("api-key"); got != "test-key" {
		t.Fatalf("api-key header = %q, want test-key", got)
	}
	if got := gotHeaders.Get("Authorization"); got != "" {
		t.Fatalf("Authorization header = %q, want empty", got)
	}
	if got := gotHeaders.Get("OpenAI-Beta"); got != "" {
		t.Fatalf("OpenAI-Beta header = %q, want removed", got)
	}
	if got := gotHeaders.Get("originator"); got != "" {
		t.Fatalf("originator header = %q, want removed", got)
	}
	if got := gotBody["store"]; got != true {
		t.Fatalf("store = %#v, want true", got)
	}
}

func TestResponsesProvider_EquivalentAzureWireConfigUserAgent(t *testing.T) {
	var gotHeaders http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"type":"response.completed","response":{"id":"resp-1","status":"completed","output":[],"usage":{"input_tokens":5,"output_tokens":2}}}`+"\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	overrides := &config.RequestOverridesConfig{
		Headers: map[string]*string{
			"OpenAI-Beta": nil,
			"originator":  nil,
		},
	}
	providerCfg := NewProviderConfig("azure-equivalent", config.ProviderConfig{
		Type:       config.ProviderTypeResponses,
		APIURL:     server.URL + "/openai/v1/responses",
		AuthScheme: config.AuthSchemeAPIKey,
		Store:      new(true),
		UserAgent:  "AzureUA/1.0",
		Compat:     &config.ProviderCompatConfig{RequestOverrides: overrides},
	}, []string{"test-key"})
	r := &ResponsesProvider{provider: providerCfg, client: server.Client()}

	_, err := r.CompleteStream(
		context.Background(), "test-key", "gpt-5.5", "",
		[]message.Message{{Role: "user", Content: "hello"}},
		nil, 0, RequestTuning{},
		func(message.StreamDelta) {},
	)
	if err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}

	if got := gotHeaders.Get("User-Agent"); got != "AzureUA/1.0" {
		t.Fatalf("User-Agent = %q, want AzureUA/1.0", got)
	}
	if got := gotHeaders.Get("OpenAI-Beta"); got != "" {
		t.Fatalf("OpenAI-Beta header = %q, want removed", got)
	}
	if got := gotHeaders.Get("originator"); got != "" {
		t.Fatalf("originator header = %q, want removed", got)
	}
}

func TestResponsesProvider_AuthSchemeAPIKeyOverrideUsesAPIKeyHeader(t *testing.T) {
	var gotHeaders http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"type":"response.completed","response":{"id":"resp-1","status":"completed","output":[],"usage":{"input_tokens":5,"output_tokens":2}}}`+"\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	providerCfg := NewProviderConfig("custom", config.ProviderConfig{
		Type:       config.ProviderTypeResponses,
		APIURL:     server.URL + "/v1/responses",
		AuthScheme: config.AuthSchemeAPIKey,
	}, []string{"test-key"})
	r := &ResponsesProvider{provider: providerCfg, client: server.Client()}

	_, err := r.CompleteStream(
		context.Background(), "test-key", "gpt-test", "",
		[]message.Message{{Role: "user", Content: "hello"}},
		nil, 0, RequestTuning{},
		func(message.StreamDelta) {},
	)
	if err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}
	if got := gotHeaders.Get("api-key"); got != "test-key" {
		t.Fatalf("api-key header = %q, want test-key", got)
	}
	if got := gotHeaders.Get("Authorization"); got != "" {
		t.Fatalf("Authorization header = %q, want empty", got)
	}
	if got := gotHeaders.Get("Accept"); got != "text/event-stream" {
		t.Fatalf("Accept header = %q, want text/event-stream", got)
	}
	if got := gotHeaders.Get("OpenAI-Beta"); got != openAICodexBetaHeader {
		t.Fatalf("OpenAI-Beta header = %q, want %q for non-Azure override", got, openAICodexBetaHeader)
	}
	if got := gotHeaders.Get("originator"); got != openAICodexOriginator {
		t.Fatalf("originator header = %q, want %q for non-Azure override", got, openAICodexOriginator)
	}
}

func TestResponsesProvider_ProviderUserAgentOverridesResponsesDefault(t *testing.T) {
	var gotHeaders http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"type":"response.completed","response":{"id":"resp-1","status":"completed","output":[],"usage":{"input_tokens":5,"output_tokens":2}}}`+"\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	providerCfg := NewProviderConfig("generic", config.ProviderConfig{
		Type:      config.ProviderTypeResponses,
		APIURL:    server.URL + "/v1/responses",
		UserAgent: "ProviderUA/1.0",
	}, []string{"test-key"})
	r := &ResponsesProvider{provider: providerCfg, client: server.Client()}

	_, err := r.CompleteStream(
		context.Background(), "test-key", "gpt-5.5", "",
		[]message.Message{{Role: "user", Content: "hello"}},
		nil, 0, RequestTuning{},
		func(message.StreamDelta) {},
	)
	if err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}

	if got := gotHeaders.Get("User-Agent"); got != "ProviderUA/1.0" {
		t.Errorf("User-Agent = %q, want ProviderUA/1.0", got)
	}
	if got := gotHeaders.Get("originator"); got != "codex_cli_rs" {
		t.Errorf("originator = %q, want codex_cli_rs", got)
	}
}

func TestResponsesProvider_SendsSessionHeadersAndPromptCacheKey(t *testing.T) {
	var gotBody map[string]any
	var gotHeader http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Clone()
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"type":"response.completed","response":{"id":"resp-1","status":"completed","output":[],"usage":{"input_tokens":5,"output_tokens":2}}}`+"\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	providerCfg := NewProviderConfig("openai", config.ProviderConfig{
		Type:   config.ProviderTypeResponses,
		APIURL: server.URL + "/v1/responses",
	}, []string{"test-key"})
	r := &ResponsesProvider{provider: providerCfg, client: server.Client()}

	_, err := r.CompleteStream(
		context.Background(), "test-key", "gpt-5", "",
		[]message.Message{{Role: "user", Content: "hello"}},
		nil, 128, RequestTuning{SessionKey: "session-123"},
		func(message.StreamDelta) {},
	)
	if err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}
	wire := deriveResponsesWireIdentity("session-123", 0, false)
	if got := gotBody["prompt_cache_key"]; got != wire.sessionID {
		t.Fatalf("prompt_cache_key = %v, want the derived client id %q", got, wire.sessionID)
	}
	for _, name := range []string{"X-Session-Id", "session-id", "thread-id", "x-client-request-id"} {
		if got := gotHeader.Get(name); got != wire.sessionID {
			t.Fatalf("%s = %q, want %q", name, got, wire.sessionID)
		}
	}
	if got := gotHeader.Get(responsesClientMetadataWindowID); got != wire.windowID {
		t.Fatalf("x-codex-window-id = %q, want %q", got, wire.windowID)
	}
	metadata, ok := gotBody["client_metadata"].(map[string]any)
	if !ok {
		t.Fatalf("client_metadata = %#v, want object", gotBody["client_metadata"])
	}
	if got := metadata[responsesClientMetadataSessionID]; got != wire.sessionID {
		t.Fatalf("client_metadata session_id = %#v, want %q", got, wire.sessionID)
	}
	if got := metadata[responsesClientMetadataThreadID]; got != wire.sessionID {
		t.Fatalf("client_metadata thread_id = %#v, want %q", got, wire.sessionID)
	}
	if got := metadata[responsesClientMetadataWindowID]; got != wire.windowID {
		t.Fatalf("client_metadata window_id = %#v, want %q", got, wire.windowID)
	}
	installationID, ok := metadata[responsesClientMetadataInstallationID].(string)
	if !ok || installationID == "" {
		t.Fatalf("client_metadata installation id = %#v, want non-empty string", metadata[responsesClientMetadataInstallationID])
	}
	if got := gotHeader.Get(responsesClientMetadataInstallationID); got != installationID {
		t.Fatalf("x-codex-installation-id = %q, want %q", got, installationID)
	}
	turnMetadataRaw, ok := metadata[responsesClientMetadataTurnMetadata].(string)
	if !ok || turnMetadataRaw == "" {
		t.Fatalf("client_metadata turn metadata = %#v, want non-empty string", metadata[responsesClientMetadataTurnMetadata])
	}
	if got := gotHeader.Get(responsesClientMetadataTurnMetadata); got != turnMetadataRaw {
		t.Fatalf("x-codex-turn-metadata = %q, want %q", got, turnMetadataRaw)
	}
	var turnMetadata map[string]any
	if err := json.Unmarshal([]byte(turnMetadataRaw), &turnMetadata); err != nil {
		t.Fatalf("turn metadata is not valid JSON: %v", err)
	}
	if got := turnMetadata["session_id"]; got != wire.sessionID {
		t.Fatalf("turn metadata session_id = %#v, want %q", got, wire.sessionID)
	}
	if got := turnMetadata["thread_id"]; got != wire.sessionID {
		t.Fatalf("turn metadata thread_id = %#v, want %q", got, wire.sessionID)
	}
	if got := turnMetadata["thread_source"]; got != codexThreadSourceUser {
		t.Fatalf("turn metadata thread_source = %#v, want %q", got, codexThreadSourceUser)
	}
	if got := turnMetadata["window_id"]; got != wire.windowID {
		t.Fatalf("turn metadata window_id = %#v, want %q", got, wire.windowID)
	}
	if got := turnMetadata["context_window_id"]; got != wire.contextWindowID {
		t.Fatalf("turn metadata context_window_id = %#v, want %q", got, wire.contextWindowID)
	}
	if got := turnMetadata["window_number"]; got != float64(0) {
		t.Fatalf("turn metadata window_number = %#v, want 0", got)
	}
	if got := turnMetadata["installation_id"]; got != installationID {
		t.Fatalf("turn metadata installation_id = %#v, want %q", got, installationID)
	}
	if got := turnMetadata["request_kind"]; got != "turn" {
		t.Fatalf("turn metadata request_kind = %#v, want turn", got)
	}
	if got := turnMetadata["sandbox"]; got != "none" {
		t.Fatalf("turn metadata sandbox = %#v, want none", got)
	}
}

func TestOpenAIProvider_SendsSessionHeadersOnChatCompletions(t *testing.T) {
	var gotHeader http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Clone()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"choices":[{"delta":{"content":"ok"},"finish_reason":null}]}`+"\n\n")
		_, _ = io.WriteString(w, `data: {"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}`+"\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	providerCfg := NewProviderConfig("openai", config.ProviderConfig{
		Type:   config.ProviderTypeChatCompletions,
		APIURL: server.URL + "/v1/chat/completions",
	}, []string{"test-key"})
	o, err := NewOpenAIProviderWithClient(providerCfg, server.Client(), "")
	if err != nil {
		t.Fatalf("NewOpenAIProviderWithClient: %v", err)
	}

	_, err = o.CompleteStream(
		context.Background(), "test-key", "gpt-4", "",
		[]message.Message{{Role: "user", Content: "hello"}},
		nil, 128, RequestTuning{SessionKey: "session-456"},
		func(message.StreamDelta) {},
	)
	if err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}
	if got := gotHeader.Get("X-Session-Id"); got != "session-456" {
		t.Fatalf("X-Session-Id = %q, want session-456", got)
	}
	if got := gotHeader.Get("session-id"); got != "session-456" {
		t.Fatalf("session-id = %q, want session-456", got)
	}
}

func TestOpenAIProvider_AuthSchemeAPIKeyOverrideUsesAPIKeyHeader(t *testing.T) {
	var gotHeader http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Clone()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"choices":[{"delta":{"content":"ok"},"finish_reason":null}]}`+"\n\n")
		_, _ = io.WriteString(w, `data: {"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}`+"\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	providerCfg := NewProviderConfig("openai", config.ProviderConfig{
		Type:       config.ProviderTypeChatCompletions,
		APIURL:     server.URL + "/v1/chat/completions",
		AuthScheme: config.AuthSchemeAPIKey,
	}, []string{"test-key"})
	o, err := NewOpenAIProviderWithClient(providerCfg, server.Client(), "")
	if err != nil {
		t.Fatalf("NewOpenAIProviderWithClient: %v", err)
	}

	_, err = o.CompleteStream(
		context.Background(), "test-key", "gpt-4", "",
		[]message.Message{{Role: "user", Content: "hello"}},
		nil, 128, RequestTuning{},
		func(message.StreamDelta) {},
	)
	if err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}
	if got := gotHeader.Get("api-key"); got != "test-key" {
		t.Fatalf("api-key header = %q, want test-key", got)
	}
	if got := gotHeader.Get("Authorization"); got != "" {
		t.Fatalf("Authorization header = %q, want empty", got)
	}
	if got := gotHeader.Get("User-Agent"); got != defaultLLMUserAgent() {
		t.Fatalf("User-Agent = %q, want %q", got, defaultLLMUserAgent())
	}
}
