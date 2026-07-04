package llm

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
)

func testOAuthJWT(payload string) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	body := base64.RawURLEncoding.EncodeToString([]byte(payload))
	return header + "." + body + ".sig"
}

func newOpenAITestOAuthProvider(t *testing.T, apiURL string) (*ProviderConfig, string) {
	t.Helper()

	accessToken := testOAuthJWT(`{"https://api.openai.com/auth":{"chatgpt_account_id":"acc-test","chatgpt_user_id":"user-test"}}`)
	creds := []config.ProviderCredential{{
		OAuth: &config.OAuthCredential{
			Refresh:       "refresh-token",
			Access:        accessToken,
			Expires:       time.Now().Add(time.Hour).UnixMilli(),
			AccountUserID: "user-test__acc-test",
			AccountID:     "acc-test",
		},
	}}
	responsesWSOff := false
	provider := NewProviderConfig("openai", config.ProviderConfig{
		Type:               config.ProviderTypeChatCompletions,
		Preset:             config.ProviderPresetCodex,
		APIURL:             apiURL,
		Models:             map[string]config.ModelConfig{},
		ResponsesWebsocket: &responsesWSOff, // httptest only speaks HTTP POST, not WSS
	}, config.ExtractAPIKeys(creds))
	auth := config.AuthConfig{"openai": creds}
	var authMu sync.Mutex
	provider.SetOAuthRefresher(apiURL, "app_EMoamEEZ73f0CkXaXp7hrann", "", "", &auth, &authMu, map[string]OAuthKeySetup{
		accessToken: {
			CredentialIndex: 0,
			AccountUserID:   "user-test__acc-test",
			AccountID:       "acc-test",
			Expires:         creds[0].OAuth.Expires,
		},
	}, "")
	return provider, accessToken
}

func TestResolveOpenAIOAuthAPIURL(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "default chat completions endpoint",
			in:   "https://api.openai.com/v1/chat/completions",
			want: openAICodexResponsesURL,
		},
		{
			name: "default responses endpoint",
			in:   "https://api.openai.com/v1/responses",
			want: openAICodexResponsesURL,
		},
		{
			name: "custom gateway preserved",
			in:   "https://example.com/openai/v1/responses",
			want: "https://example.com/openai/v1/responses",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolveOpenAIOAuthAPIURL(tt.in); got != tt.want {
				t.Fatalf("resolveOpenAIOAuthAPIURL(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestProviderConfig_RefreshOAuthKeyRequiresAccountUserID(t *testing.T) {
	refreshedAccess := testOAuthJWT(`{"https://api.openai.com/auth":{"chatgpt_account_id":"acc-test"}}`)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":`+strconv.Quote(refreshedAccess)+`,"expires_in":3600}`)
	}))
	defer server.Close()

	provider, accessToken := newOpenAITestOAuthProvider(t, server.URL)
	_, refreshed, err := provider.TryRefreshOAuthKey(context.Background(), accessToken)
	if err == nil {
		t.Fatal("expected missing account_user_id refresh error")
	}
	if refreshed {
		t.Fatal("expected refresh flag to be false on account_user_id error")
	}
	if !strings.Contains(err.Error(), "missing account_user_id") {
		t.Fatalf("expected missing account_user_id error, got %v", err)
	}
}

func TestResponsesProvider_OpenAIOAuthUsesCodexHeadersAndBody(t *testing.T) {
	var gotPath string
	var gotHeaders http.Header
	var gotBody map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotHeaders = r.Header.Clone()
		data, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request body: %v", err)
		}
		if err := json.Unmarshal(data, &gotBody); err != nil {
			t.Fatalf("unmarshal request body: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	provider, accessToken := newOpenAITestOAuthProvider(t, server.URL+"/v1/responses")
	r := &ResponsesProvider{provider: provider, client: server.Client()}

	_, err := r.CompleteStream(
		context.Background(),
		accessToken,
		"gpt-5.5",
		"system prompt",
		[]message.Message{{Role: "user", Content: "hello"}},
		nil,
		128,
		RequestTuning{OpenAI: OpenAITuning{ReasoningEffort: "xhigh", TextVerbosity: "medium"}},
		func(message.StreamDelta) {},
	)
	if err != nil {
		t.Fatalf("CompleteStream returned error: %v", err)
	}

	if gotPath != "/v1/responses" {
		t.Fatalf("expected request path /v1/responses, got %q", gotPath)
	}
	if gotHeaders.Get("Authorization") != "Bearer "+accessToken {
		t.Fatalf("unexpected Authorization header: %q", gotHeaders.Get("Authorization"))
	}
	if gotHeaders.Get("ChatGPT-Account-ID") != "acc-test" {
		t.Fatalf("unexpected ChatGPT-Account-ID header: %q", gotHeaders.Get("ChatGPT-Account-ID"))
	}
	if gotHeaders.Get("OpenAI-Beta") != openAICodexBetaHeader {
		t.Fatalf("unexpected OpenAI-Beta header: %q", gotHeaders.Get("OpenAI-Beta"))
	}
	if gotHeaders.Get("originator") != openAICodexOriginator {
		t.Fatalf("unexpected originator header: %q", gotHeaders.Get("originator"))
	}
	if gotHeaders.Get(headerUserAgent) != defaultLLMUserAgent() {
		t.Fatalf("unexpected User-Agent header: %q", gotHeaders.Get(headerUserAgent))
	}
	if gotHeaders.Get("session_id") == "" {
		t.Fatal("expected session_id header to be set")
	}
	if !strings.Contains(gotHeaders.Get("Accept"), "text/event-stream") {
		t.Fatalf("unexpected Accept header: %q", gotHeaders.Get("Accept"))
	}

	if gotBody["instructions"] != "system prompt" {
		t.Fatalf("expected instructions=system prompt, got %#v", gotBody["instructions"])
	}
	if gotBody["store"] != false {
		t.Fatalf("expected explicit store=false, got %#v", gotBody["store"])
	}
	if _, ok := gotBody["max_output_tokens"]; ok {
		t.Fatalf("did not expect max_output_tokens in OAuth responses request: %#v", gotBody["max_output_tokens"])
	}
	if gotBody["parallel_tool_calls"] != false {
		t.Fatalf("expected explicit parallel_tool_calls=false, got %#v", gotBody["parallel_tool_calls"])
	}
	inc, ok := gotBody["include"].([]any)
	if !ok || len(inc) != 1 || inc[0] != "reasoning.encrypted_content" {
		t.Fatalf("include = %#v, want [\"reasoning.encrypted_content\"]", gotBody["include"])
	}
	if _, ok := gotBody["messages"]; ok {
		t.Fatalf("did not expect chat-completions messages field in OAuth responses request: %#v", gotBody["messages"])
	}
	reasoning, ok := gotBody["reasoning"].(map[string]any)
	if !ok {
		t.Fatalf("expected reasoning object, got %#v", gotBody["reasoning"])
	}
	if reasoning["effort"] != "xhigh" {
		t.Fatalf("expected reasoning.effort=xhigh, got %#v", reasoning["effort"])
	}
	text, ok := gotBody["text"].(map[string]any)
	if !ok {
		t.Fatalf("expected text object, got %#v", gotBody["text"])
	}
	if text["verbosity"] != "medium" {
		t.Fatalf("expected text.verbosity=medium, got %#v", text["verbosity"])
	}
	input, ok := gotBody["input"].([]any)
	if !ok || len(input) != 1 {
		t.Fatalf("expected one input item, got %#v", gotBody["input"])
	}
	first, ok := input[0].(map[string]any)
	if !ok {
		t.Fatalf("expected first input item to be an object, got %#v", input[0])
	}
	if first["role"] != "user" {
		t.Fatalf("expected first input role=user, got %#v", first["role"])
	}
}

func TestResponsesProvider_OpenAIOAuthHonorsProviderUserAgent(t *testing.T) {
	var gotHeaders http.Header

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	provider, accessToken := newOpenAITestOAuthProvider(t, server.URL+"/v1/responses")
	provider.userAgent = "ProviderUA/1.0"
	r := &ResponsesProvider{provider: provider, client: server.Client()}

	_, err := r.CompleteStream(
		context.Background(),
		accessToken,
		"gpt-5.5",
		"system prompt",
		[]message.Message{{Role: "user", Content: "hello"}},
		nil,
		128,
		RequestTuning{},
		func(message.StreamDelta) {},
	)
	if err != nil {
		t.Fatalf("CompleteStream returned error: %v", err)
	}

	if got := gotHeaders.Get(headerUserAgent); got != "ProviderUA/1.0" {
		t.Fatalf("User-Agent = %q, want ProviderUA/1.0", got)
	}
	if got := gotHeaders.Get("originator"); got != openAICodexOriginator {
		t.Fatalf("originator = %q, want %q", got, openAICodexOriginator)
	}
}

func TestResponsesProvider_ResponsesWireOmitsMaxOutputTokens(t *testing.T) {
	var gotBody map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request body: %v", err)
		}
		if err := json.Unmarshal(data, &gotBody); err != nil {
			t.Fatalf("unmarshal request body: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	provider := NewProviderConfig("generic", config.ProviderConfig{
		Type:   config.ProviderTypeResponses,
		APIURL: server.URL + "/v1/responses",
	}, []string{"test-key"})
	apiKey := "test-key"
	r := &ResponsesProvider{provider: provider, client: server.Client()}

	_, err := r.CompleteStream(
		context.Background(),
		apiKey,
		"gpt-5.5-mini",
		"system prompt",
		[]message.Message{{Role: "user", Content: "hello"}},
		nil,
		128,
		RequestTuning{OpenAI: OpenAITuning{ReasoningEffort: "high", TextVerbosity: "medium"}},
		func(message.StreamDelta) {},
	)
	if err != nil {
		t.Fatalf("CompleteStream returned error: %v", err)
	}
	if _, ok := gotBody["max_output_tokens"]; ok {
		t.Fatalf("did not expect max_output_tokens in responses request: %#v", gotBody["max_output_tokens"])
	}
	if gotBody["instructions"] != "system prompt" {
		t.Fatalf("expected instructions=system prompt, got %#v", gotBody["instructions"])
	}
	store, ok := gotBody["store"].(bool)
	if !ok || store {
		t.Fatalf("expected explicit store=false for responses request, got %#v", gotBody["store"])
	}
	if _, ok := gotBody["reasoning"].(map[string]any); !ok {
		t.Fatalf("expected reasoning object, got %#v", gotBody["reasoning"])
	}
	if _, ok := gotBody["text"].(map[string]any); !ok {
		t.Fatalf("expected text object, got %#v", gotBody["text"])
	}
}

func TestResponsesProvider_OpenAIOAuthCompleteUsesStreamingHTTP(t *testing.T) {
	var gotBody map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request body: %v", err)
		}
		if err := json.Unmarshal(data, &gotBody); err != nil {
			t.Fatalf("unmarshal request body: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"type":"response.completed","response":{"id":"resp-complete","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"OK"}]}],"usage":{"input_tokens":5,"output_tokens":2}}}`+"\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	provider, accessToken := newOpenAITestOAuthProvider(t, server.URL+"/v1/responses")
	r := &ResponsesProvider{provider: provider, client: server.Client()}

	resp, err := r.CompleteStream(
		context.Background(),
		accessToken,
		"gpt-5.5",
		"system prompt",
		[]message.Message{{Role: "user", Content: "hello"}},
		nil,
		128,
		RequestTuning{OpenAI: OpenAITuning{TextVerbosity: "low"}},
		nil,
	)
	if err != nil {
		t.Fatalf("CompleteStream returned error: %v", err)
	}
	if resp == nil {
		t.Fatal("CompleteStream returned nil response")
	}
	if resp.ProviderResponseID != "resp-complete" {
		t.Fatalf("response id = %q, want resp-complete", resp.ProviderResponseID)
	}
	if gotBody["stream"] != true {
		t.Fatalf("expected stream=true on codex one-shot request, got %#v", gotBody["stream"])
	}
}

func TestOpenAIProvider_OpenAIOAuthDelegatesToResponses(t *testing.T) {
	var gotBody map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request body: %v", err)
		}
		if err := json.Unmarshal(data, &gotBody); err != nil {
			t.Fatalf("unmarshal request body: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	provider, accessToken := newOpenAITestOAuthProvider(t, server.URL+"/v1/chat/completions")
	rp := &ResponsesProvider{provider: provider, client: server.Client()}
	o := &OpenAIProvider{provider: provider, client: server.Client(), responsesProvider: rp}

	_, err := o.CompleteStream(
		context.Background(),
		accessToken,
		"gpt-5.5",
		"system prompt",
		[]message.Message{{Role: "user", Content: "hello"}},
		nil,
		128, RequestTuning{},
		func(message.StreamDelta) {},
	)
	if err != nil {
		t.Fatalf("CompleteStream returned error: %v", err)
	}

	if _, ok := gotBody["messages"]; ok {
		t.Fatalf("expected OAuth request to use responses payload, got chat messages field: %#v", gotBody["messages"])
	}
	if _, ok := gotBody["input"]; !ok {
		t.Fatalf("expected OAuth request to include responses input field, got %#v", gotBody)
	}
	if gotBody["instructions"] != "system prompt" {
		t.Fatalf("expected instructions=system prompt, got %#v", gotBody["instructions"])
	}
}

func TestResponsesProvider_OpenAIOAuthSendsParallelToolCallsWhenConfigured(t *testing.T) {
	var gotBody map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request body: %v", err)
		}
		if err := json.Unmarshal(data, &gotBody); err != nil {
			t.Fatalf("unmarshal request body: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	provider, accessToken := newOpenAITestOAuthProvider(t, server.URL+"/v1/responses")
	r := &ResponsesProvider{provider: provider, client: server.Client()}

	_, err := r.CompleteStream(
		context.Background(),
		accessToken,
		"gpt-5.5",
		"system prompt",
		[]message.Message{{Role: "user", Content: "hello"}},
		nil,
		128,
		RequestTuning{OpenAI: OpenAITuning{ParallelToolCalls: new(false)}},
		func(message.StreamDelta) {},
	)
	if err != nil {
		t.Fatalf("CompleteStream returned error: %v", err)
	}

	if gotBody["parallel_tool_calls"] != false {
		t.Fatalf("expected parallel_tool_calls=false, got %#v", gotBody["parallel_tool_calls"])
	}
}

func TestResponsesProvider_OpenAIOAuthCompactUsesCompactEndpoint(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	var gotHeaders http.Header

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotHeaders = r.Header.Clone()
		data, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request body: %v", err)
		}
		if err := json.Unmarshal(data, &gotBody); err != nil {
			t.Fatalf("unmarshal request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"output":[{"type":"compaction","encrypted_content":"## Goal\n- continue\n\n## User Constraints\n- none\n\n## Progress\n- progress\n\n## Key Decisions\n- decisions\n\n## Files and Evidence\n- Archived history: history-1.md\n\n## Todo State\n- none\n\n## SubAgent State\n- none\n\n## Open Problems\n- none\n\n## Next Step\n- continue"}]}`)
	}))
	defer server.Close()

	provider, accessToken := newOpenAITestOAuthProvider(t, server.URL+"/v1/responses")
	r := &ResponsesProvider{provider: provider, client: server.Client()}

	resp, err := r.Compact(
		context.Background(),
		accessToken,
		"gpt-5.5",
		"system prompt",
		[]message.Message{{Role: "user", Content: "hello"}},
		nil,
		128,
		RequestTuning{OpenAI: OpenAITuning{TextVerbosity: "low"}},
	)
	if err != nil {
		t.Fatalf("Compact returned error: %v", err)
	}
	if gotPath != "/v1/responses/compact" {
		t.Fatalf("compact request path = %q, want /v1/responses/compact", gotPath)
	}
	if got := gotHeaders.Get("Accept"); got != "" {
		t.Fatalf("compact request Accept = %q, want empty for unary JSON endpoint", got)
	}
	if got := gotHeaders.Get("originator"); got != openAICodexOriginator {
		t.Fatalf("compact request originator = %q, want %q", got, openAICodexOriginator)
	}
	if got := gotHeaders.Get(headerOpenAIBeta); got != openAICodexBetaHeader {
		t.Fatalf("compact request %s = %q, want %q", headerOpenAIBeta, got, openAICodexBetaHeader)
	}
	if gotHeaders.Get(headerSessionID) == "" {
		t.Fatal("expected compact request session_id header to be set")
	}
	if resp == nil || !strings.Contains(resp.Content, "## Goal") {
		t.Fatalf("compact response = %#v, want extracted summary text", resp)
	}
	if gotBody["instructions"] != "system prompt" {
		t.Fatalf("expected instructions=system prompt, got %#v", gotBody["instructions"])
	}
	if _, ok := gotBody["input"]; !ok {
		t.Fatalf("expected compact request to include input, got %#v", gotBody)
	}
	if gotBody["parallel_tool_calls"] != false {
		t.Fatalf("expected compact request parallel_tool_calls=false, got %#v", gotBody["parallel_tool_calls"])
	}
	for _, key := range []string{"tool_choice", "stream", "include", "store", "max_output_tokens"} {
		if _, ok := gotBody[key]; ok {
			t.Fatalf("compact request unexpectedly included %s: %#v", key, gotBody[key])
		}
	}
}

func TestResolveResponsesReasoningEffort(t *testing.T) {
	tests := []struct {
		name            string
		effort          string
		firstPartyCodex bool
		wantEffort      string
		wantDropped     bool
	}{
		// Empty effort is never carried, regardless of transport.
		{name: "empty passthrough", effort: "", firstPartyCodex: false, wantEffort: "", wantDropped: false},
		{name: "empty codex", effort: "", firstPartyCodex: true, wantEffort: "", wantDropped: false},
		// Casing/whitespace normalization applies on every transport.
		{name: "trim passthrough", effort: "  High ", firstPartyCodex: false, wantEffort: "high", wantDropped: false},
		{name: "trim codex", effort: "  High ", firstPartyCodex: true, wantEffort: "high", wantDropped: false},
		{name: "upper codex xhigh", effort: "XHIGH", firstPartyCodex: true, wantEffort: "xhigh", wantDropped: false},
		// Codex whitelist values pass through on both transports.
		{name: "xhigh passthrough", effort: "xhigh", firstPartyCodex: false, wantEffort: "xhigh", wantDropped: false},
		{name: "xhigh codex", effort: "xhigh", firstPartyCodex: true, wantEffort: "xhigh", wantDropped: false},
		// GLM-5.2 effort levels pass through on non-Codex gateways.
		{name: "max passthrough", effort: "max", firstPartyCodex: false, wantEffort: "max", wantDropped: false},
		{name: "minimal passthrough", effort: "minimal", firstPartyCodex: false, wantEffort: "minimal", wantDropped: false},
		{name: "none passthrough", effort: "none", firstPartyCodex: false, wantEffort: "none", wantDropped: false},
		{name: "Max trimmed passthrough", effort: "Max", firstPartyCodex: false, wantEffort: "max", wantDropped: false},
		// Codex backend drops values outside its whitelist rather than sending them.
		{name: "max codex dropped", effort: "max", firstPartyCodex: true, wantEffort: "", wantDropped: true},
		{name: "minimal codex dropped", effort: "minimal", firstPartyCodex: true, wantEffort: "", wantDropped: true},
		{name: "none codex dropped", effort: "none", firstPartyCodex: true, wantEffort: "", wantDropped: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotEffort, gotDropped := resolveResponsesReasoningEffort(tt.effort, tt.firstPartyCodex)
			if gotEffort != tt.wantEffort {
				t.Fatalf("effort = %q, want %q", gotEffort, tt.wantEffort)
			}
			if gotDropped != tt.wantDropped {
				t.Fatalf("dropped = %v, want %v", gotDropped, tt.wantDropped)
			}
		})
	}
}

func TestResponsesProvider_NonCodexPassesThroughGLMReasoningEffortMax(t *testing.T) {
	var gotBody map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request body: %v", err)
		}
		if err := json.Unmarshal(data, &gotBody); err != nil {
			t.Fatalf("unmarshal request body: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	provider := NewProviderConfig("glm-relay", config.ProviderConfig{
		Type:   config.ProviderTypeResponses,
		APIURL: server.URL + "/v1/responses",
	}, []string{"test-key"})
	apiKey := "test-key"
	r := &ResponsesProvider{provider: provider, client: server.Client()}

	if _, err := r.CompleteStream(
		context.Background(),
		apiKey,
		"glm-5.2",
		"system prompt",
		[]message.Message{{Role: "user", Content: "hello"}},
		nil,
		0,
		RequestTuning{OpenAI: OpenAITuning{ReasoningEffort: "max"}},
		func(message.StreamDelta) {},
	); err != nil {
		t.Fatalf("CompleteStream returned error: %v", err)
	}

	reasoning, ok := gotBody["reasoning"].(map[string]any)
	if !ok {
		t.Fatalf("expected reasoning object, got %#v", gotBody["reasoning"])
	}
	if reasoning["effort"] != "max" {
		t.Fatalf("expected reasoning.effort=max to pass through, got %#v", reasoning["effort"])
	}
	if provider.IsCodexOAuthTransport() {
		t.Fatalf("generic responses provider must not report as Codex transport")
	}
}

func TestResponsesProvider_CodexDropsUnsupportedReasoningEffortMax(t *testing.T) {
	var gotBody map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request body: %v", err)
		}
		if err := json.Unmarshal(data, &gotBody); err != nil {
			t.Fatalf("unmarshal request body: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	provider, accessToken := newOpenAITestOAuthProvider(t, server.URL+"/v1/responses")
	r := &ResponsesProvider{provider: provider, client: server.Client()}
	if !provider.IsCodexOAuthTransport() {
		t.Fatalf("expected Codex OAuth transport for preset codex provider")
	}

	if _, err := r.CompleteStream(
		context.Background(),
		accessToken,
		"gpt-5.5",
		"system prompt",
		[]message.Message{{Role: "user", Content: "hello"}},
		nil,
		0,
		RequestTuning{OpenAI: OpenAITuning{ReasoningEffort: "max"}},
		func(message.StreamDelta) {},
	); err != nil {
		t.Fatalf("CompleteStream returned error: %v", err)
	}

	// max must not reach the Codex backend. With effort dropped to "" and no
	// summary configured, the reasoning block is omitted entirely (matching the
	// prior behaviour), so reasoning is either absent or carries no effort.
	if reasoning, ok := gotBody["reasoning"].(map[string]any); ok {
		if effort, _ := reasoning["effort"].(string); effort == "max" {
			t.Fatalf("expected max effort dropped for Codex backend, got reasoning=%#v", reasoning)
		}
	}
}
