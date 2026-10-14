package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
)

func TestResponsesProvider_SendsIncludeToolChoiceAndReasoning(t *testing.T) {
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"type":"response.completed","response":{"id":"resp-1","status":"completed","output":[],"usage":{"input_tokens":5,"output_tokens":2}}}`+"\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	// Generic Responses provider (no preset): these fields are part of Chord's
	// Responses wire shape and are not limited to preset:codex.
	providerCfg := NewProviderConfig("generic", config.ProviderConfig{
		Type:   config.ProviderTypeResponses,
		APIURL: server.URL + "/v1/responses",
	}, []string{"test-key"})
	r := &ResponsesProvider{provider: providerCfg, client: server.Client()}

	// Summary-only reasoning (no effort) must still produce a reasoning block.
	tuning := RequestTuning{OpenAI: OpenAITuning{ReasoningSummary: "auto"}}
	_, err := r.CompleteStream(
		context.Background(), "test-key", "gpt-5.5", "",
		[]message.Message{{Role: "user", Content: "hello"}},
		[]message.ToolDefinition{{Name: "done", Description: "Finish", InputSchema: map[string]any{"type": "object"}}}, 0, tuning,
		func(message.StreamDelta) {},
	)
	if err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}

	inc, ok := gotBody["include"].([]any)
	if !ok || len(inc) != 1 || inc[0] != responsesEncryptedReasoningInclude {
		t.Fatalf("include = %#v, want [%q]", gotBody["include"], responsesEncryptedReasoningInclude)
	}
	if got := gotBody["tool_choice"]; got != "auto" {
		t.Fatalf("tool_choice = %#v, want auto", got)
	}
	reasoning, ok := gotBody["reasoning"].(map[string]any)
	if !ok {
		t.Fatalf("reasoning = %#v, want object with summary", gotBody["reasoning"])
	}
	if got := reasoning["summary"]; got != "auto" {
		t.Fatalf("reasoning.summary = %#v, want auto", got)
	}
	if _, hasEffort := reasoning["effort"]; hasEffort {
		t.Fatalf("reasoning.effort should be omitted when no effort configured, got %#v", reasoning["effort"])
	}
}

func TestResponsesProvider_CompatTogglesOmitOptionalFields(t *testing.T) {
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"type":"response.completed","response":{"id":"resp-1","status":"completed","output":[],"usage":{"input_tokens":5,"output_tokens":2}}}`+"\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	providerCfg := NewProviderConfig("gateway", config.ProviderConfig{
		Type:   config.ProviderTypeResponses,
		APIURL: server.URL + "/v1/responses",
		Compat: &config.ProviderCompatConfig{
			Responses: &config.ResponsesCompatConfig{
				SendStore:             new(false),
				SendToolChoice:        new(false),
				SendParallelToolCalls: new(false),
				SendPromptCacheKey:    new(false),
				SendReasoningInclude:  new(false),
				SendMaxOutputTokens:   new(true),
			},
		},
	}, []string{"test-key"})
	r := &ResponsesProvider{provider: providerCfg, client: server.Client()}

	_, err := r.CompleteStream(
		context.Background(), "test-key", "gpt-5.5", "",
		[]message.Message{{Role: "user", Content: "hello"}},
		[]message.ToolDefinition{{Name: "done", Description: "Finish", InputSchema: map[string]any{"type": "object"}}}, 128, RequestTuning{SessionKey: "session-123"},
		func(message.StreamDelta) {},
	)
	if err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}
	if _, has := gotBody["store"]; has {
		t.Fatalf("store should be omitted, got %#v", gotBody["store"])
	}
	if _, has := gotBody["tool_choice"]; has {
		t.Fatalf("tool_choice should be omitted, got %#v", gotBody["tool_choice"])
	}
	if _, has := gotBody["parallel_tool_calls"]; has {
		t.Fatalf("parallel_tool_calls should be omitted, got %#v", gotBody["parallel_tool_calls"])
	}
	if _, has := gotBody["prompt_cache_key"]; has {
		t.Fatalf("prompt_cache_key should be omitted, got %#v", gotBody["prompt_cache_key"])
	}
	if _, has := gotBody["client_metadata"]; has {
		t.Fatalf("client_metadata should be omitted, got %#v", gotBody["client_metadata"])
	}
	if _, has := gotBody["include"]; has {
		t.Fatalf("include should be omitted, got %#v", gotBody["include"])
	}
	if got := gotBody["max_output_tokens"]; got != float64(128) {
		t.Fatalf("max_output_tokens = %#v, want 128", got)
	}
}

func TestResponsesProvider_CompatTogglesDefaultsKeepStableShape(t *testing.T) {
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"type":"response.completed","response":{"id":"resp-1","status":"completed","output":[],"usage":{"input_tokens":5,"output_tokens":2}}}`+"\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	providerCfg := NewProviderConfig("generic", config.ProviderConfig{
		Type:   config.ProviderTypeResponses,
		APIURL: server.URL + "/v1/responses",
	}, []string{"test-key"})
	r := &ResponsesProvider{provider: providerCfg, client: server.Client()}

	_, err := r.CompleteStream(
		context.Background(), "test-key", "gpt-5.5", "",
		[]message.Message{{Role: "user", Content: "hello"}},
		[]message.ToolDefinition{{Name: "done", Description: "Finish", InputSchema: map[string]any{"type": "object"}}}, 128, RequestTuning{SessionKey: "session-123"},
		func(message.StreamDelta) {},
	)
	if err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}
	if _, has := gotBody["store"]; !has {
		t.Fatalf("store should be present by default, got keys %v", sortedKeys(gotBody))
	}
	if got := gotBody["tool_choice"]; got != "auto" {
		t.Fatalf("tool_choice = %#v, want auto", got)
	}
	if _, has := gotBody["max_output_tokens"]; has {
		t.Fatalf("max_output_tokens should be omitted by default, got %#v", gotBody["max_output_tokens"])
	}
	if _, has := gotBody["prompt_cache_key"]; !has {
		t.Fatalf("prompt_cache_key should be present with session id by default")
	}
}

func TestResponsesProvider_IncludeAlwaysSerialized(t *testing.T) {
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"type":"response.completed","response":{"id":"resp-1","status":"completed","output":[],"usage":{"input_tokens":5,"output_tokens":2}}}`+"\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	// No preset and no explicit reasoning tuning: stateless Responses models can
	// still reason by default, so encrypted continuity is always requested.
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

	inc, ok := gotBody["include"].([]any)
	if !ok || len(inc) != 1 || inc[0] != responsesEncryptedReasoningInclude {
		t.Fatalf("include = %#v, want [%q]", gotBody["include"], responsesEncryptedReasoningInclude)
	}
	if _, has := gotBody["tool_choice"]; has {
		t.Fatalf("tool_choice should be omitted without tools, got %#v", gotBody["tool_choice"])
	}
	if _, has := gotBody["parallel_tool_calls"]; has {
		t.Fatalf("parallel_tool_calls should be omitted without tools, got %#v", gotBody["parallel_tool_calls"])
	}
	if got := gotBody["store"]; got != false {
		t.Fatalf("store = %#v, want false", got)
	}
	tools, ok := gotBody["tools"].([]any)
	if !ok || len(tools) != 0 {
		t.Fatalf("tools = %#v, want []", gotBody["tools"])
	}
}

func TestResponsesProvider_ProviderStoreOverridesDefault(t *testing.T) {
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"type":"response.completed","response":{"id":"resp-1","status":"completed","output":[],"usage":{"input_tokens":5,"output_tokens":2}}}`+"\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	storeTrue := true
	providerCfg := NewProviderConfig("generic", config.ProviderConfig{
		Type:   config.ProviderTypeResponses,
		APIURL: server.URL + "/v1/responses",
		Store:  &storeTrue,
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
	if got := gotBody["store"]; got != true {
		t.Fatalf("store = %#v, want true", got)
	}
}

func TestResponsesProvider_ModelStoreOverridesProviderStore(t *testing.T) {
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"type":"response.completed","response":{"id":"resp-1","status":"completed","output":[],"usage":{"input_tokens":5,"output_tokens":2}}}`+"\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	providerStore := true
	modelStore := false
	providerCfg := NewProviderConfig("generic", config.ProviderConfig{
		Type:   config.ProviderTypeResponses,
		APIURL: server.URL + "/v1/responses",
		Store:  &providerStore,
		Models: map[string]config.ModelConfig{
			"gpt-5.5": {Store: &modelStore},
		},
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
	if got := gotBody["store"]; got != false {
		t.Fatalf("store = %#v, want false from model override", got)
	}
}

func TestResponsesRequestAlwaysSerializesIncludeArray(t *testing.T) {
	body, err := json.Marshal(responsesRequest{Model: "gpt-5.5"})
	if err != nil {
		t.Fatalf("marshal responses request: %v", err)
	}
	if !bytes.Contains(body, []byte(`"include":[]`)) {
		t.Fatalf("nil include not serialized as []: %s", body)
	}
	if !bytes.Contains(body, []byte(`"input":[]`)) {
		t.Fatalf("nil input not serialized as []: %s", body)
	}
	if !bytes.Contains(body, []byte(`"tools":[]`)) {
		t.Fatalf("nil tools not serialized as []: %s", body)
	}
}

func TestResponsesProvider_AppliesRequestOverrides(t *testing.T) {
	var gotHeaders http.Header
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"type":"response.completed","response":{"id":"resp-1","status":"completed","output":[],"usage":{"input_tokens":5,"output_tokens":2}}}`+"\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	trace := "custom"
	providerCfg := NewProviderConfig("generic", config.ProviderConfig{
		Type:   config.ProviderTypeResponses,
		APIURL: server.URL + "/v1/responses",
		Compat: &config.ProviderCompatConfig{RequestOverrides: &config.RequestOverridesConfig{
			Body:    map[string]any{"reasoning": map[string]any{"effort": "max"}, "store": nil},
			Headers: map[string]*string{"OpenAI-Beta": nil, "X-Trace": &trace},
		}},
	}, []string{"test-key"})
	r := &ResponsesProvider{provider: providerCfg, client: server.Client()}

	_, err := r.CompleteStream(context.Background(), "test-key", "glm-5.2", "", []message.Message{{Role: "user", Content: "hello"}}, nil, 0, RequestTuning{}, func(message.StreamDelta) {})
	if err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}
	if gotBody["reasoning"].(map[string]any)["effort"] != "max" {
		t.Fatalf("reasoning = %#v, want effort=max", gotBody["reasoning"])
	}
	if _, ok := gotBody["store"]; ok {
		t.Fatal("store should be deleted by request override")
	}
	if got := gotHeaders.Get("OpenAI-Beta"); got != "" {
		t.Fatalf("OpenAI-Beta = %q, want removed", got)
	}
	if got := gotHeaders.Get("X-Trace"); got != "custom" {
		t.Fatalf("X-Trace = %q, want custom", got)
	}
}

func TestResponsesProvider_SendsSystemPromptAsInstructions(t *testing.T) {
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"type":"response.completed","response":{"id":"resp-1","status":"completed","output":[],"usage":{"input_tokens":5,"output_tokens":2}}}`+"\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	providerCfg := NewProviderConfig("sample", config.ProviderConfig{
		Type:   config.ProviderTypeResponses,
		APIURL: server.URL + "/v1/responses",
	}, []string{"test-key"})
	r := &ResponsesProvider{provider: providerCfg, client: server.Client()}

	_, err := r.CompleteStream(
		context.Background(), "test-key", "gpt-5", "You are helpful.",
		[]message.Message{{Role: "user", Content: "hello"}},
		nil, 128, RequestTuning{},
		func(message.StreamDelta) {},
	)
	if err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}
	if got := gotBody["instructions"]; got != "You are helpful." {
		t.Fatalf("instructions = %v, want system prompt", got)
	}
	input, ok := gotBody["input"].([]any)
	if !ok || len(input) != 1 {
		t.Fatalf("input = %#v, want one user message", gotBody["input"])
	}
	item, ok := input[0].(map[string]any)
	if !ok {
		t.Fatalf("input[0] = %#v, want object", input[0])
	}
	if got := item["type"]; got != "message" {
		t.Fatalf("input[0].type = %v, want message", got)
	}
	if got := item["role"]; got == "system" {
		t.Fatalf("unexpected system message in input: %#v", item)
	}
}

func TestResponsesProvider_ReasoningSummaryDefaultsToAuto(t *testing.T) {
	for _, tc := range []struct {
		name        string
		tuning      RequestTuning
		wantSummary any // nil means the summary key must be absent
	}{
		{
			name:        "effort without summary defaults to auto",
			tuning:      RequestTuning{OpenAI: OpenAITuning{ReasoningEffort: "high"}},
			wantSummary: "auto",
		},
		{
			name:        "explicit summary wins",
			tuning:      RequestTuning{OpenAI: OpenAITuning{ReasoningEffort: "high", ReasoningSummary: "detailed"}},
			wantSummary: "detailed",
		},
		{
			name:        "none opts out",
			tuning:      RequestTuning{OpenAI: OpenAITuning{ReasoningEffort: "high", ReasoningSummary: "none"}},
			wantSummary: nil,
		},
		{
			name:        "effort none keeps summary absent",
			tuning:      RequestTuning{OpenAI: OpenAITuning{ReasoningEffort: "none"}},
			wantSummary: nil,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var gotBody map[string]any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewDecoder(r.Body).Decode(&gotBody)
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, `data: {"type":"response.completed","response":{"id":"resp-1","status":"completed","output":[],"usage":{"input_tokens":5,"output_tokens":2}}}`+"\n\n")
				_, _ = io.WriteString(w, "data: [DONE]\n\n")
			}))
			defer server.Close()

			providerCfg := NewProviderConfig("generic", config.ProviderConfig{
				Type:   config.ProviderTypeResponses,
				APIURL: server.URL + "/v1/responses",
			}, []string{"test-key"})
			r := &ResponsesProvider{provider: providerCfg, client: server.Client()}
			_, err := r.CompleteStream(
				context.Background(), "test-key", "gpt-5.5", "",
				[]message.Message{{Role: "user", Content: "hello"}},
				nil, 0, tc.tuning,
				func(message.StreamDelta) {},
			)
			if err != nil {
				t.Fatalf("CompleteStream: %v", err)
			}
			reasoning, ok := gotBody["reasoning"].(map[string]any)
			if !ok {
				t.Fatalf("reasoning = %#v, want object", gotBody["reasoning"])
			}
			got, present := reasoning["summary"]
			if tc.wantSummary == nil {
				if present {
					t.Fatalf("reasoning.summary = %#v, want absent", got)
				}
				return
			}
			if got != tc.wantSummary {
				t.Fatalf("reasoning.summary = %#v, want %#v", got, tc.wantSummary)
			}
		})
	}
}

func TestResponsesProvider_SuppressesForcedToolChoiceUnderThinking(t *testing.T) {
	for _, tc := range []struct {
		name       string
		overrides  *config.RequestOverridesConfig
		tuning     RequestTuning
		wantChoice any
	}{
		{
			name:       "suppressed under thinking",
			tuning:     RequestTuning{OpenAI: OpenAITuning{ToolChoice: "required", ReasoningEffort: "high"}},
			wantChoice: "auto",
		},
		{
			name:       "kept when reasoning effort is none",
			tuning:     RequestTuning{OpenAI: OpenAITuning{ToolChoice: "required", ReasoningEffort: "none"}},
			wantChoice: "required",
		},
		{
			// Codex emits the reasoning block with the effort omitted and only a
			// summary carried. That request still thinks, so the probe must not
			// read the absent effort as an opt-out.
			name:       "suppressed when only a summary is configured",
			tuning:     RequestTuning{OpenAI: OpenAITuning{ToolChoice: "required", ReasoningSummary: "detailed"}},
			wantChoice: "auto",
		},
		{
			name:       "kept when no reasoning is configured",
			tuning:     RequestTuning{OpenAI: OpenAITuning{ToolChoice: "required"}},
			wantChoice: "required",
		},
		{
			name: "kept when override disables reasoning",
			overrides: &config.RequestOverridesConfig{Body: map[string]any{
				"reasoning": map[string]any{"effort": "none"},
			}},
			tuning:     RequestTuning{OpenAI: OpenAITuning{ToolChoice: "required", ReasoningEffort: "high"}},
			wantChoice: "required",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var gotBody map[string]any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewDecoder(r.Body).Decode(&gotBody)
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, `data: {"type":"response.completed","response":{"id":"resp-1","status":"completed","output":[],"usage":{"input_tokens":5,"output_tokens":2}}}`+"\n\n")
				_, _ = io.WriteString(w, "data: [DONE]\n\n")
			}))
			defer server.Close()

			providerCfg := NewProviderConfig("sample", config.ProviderConfig{
				Type:   config.ProviderTypeResponses,
				APIURL: server.URL + "/v1/responses",
				Models: map[string]config.ModelConfig{
					"test-model": {
						Compat: &config.ModelCompatConfig{
							ForcedToolChoice: &config.ForcedToolChoiceCompatConfig{SuppressInThinking: new(true)},
							RequestOverrides: tc.overrides,
						},
					},
				},
			}, []string{"test-key"})
			r := &ResponsesProvider{provider: providerCfg, client: server.Client()}
			_, err := r.CompleteStream(
				context.Background(), "test-key", "test-model", "",
				[]message.Message{{Role: "user", Content: "hello"}},
				[]message.ToolDefinition{{Name: "done", Description: "Finish", InputSchema: map[string]any{"type": "object"}}}, 0, tc.tuning,
				func(message.StreamDelta) {},
			)
			if err != nil {
				t.Fatalf("CompleteStream: %v", err)
			}
			if got := gotBody["tool_choice"]; got != tc.wantChoice {
				t.Fatalf("tool_choice = %#v, want %#v", got, tc.wantChoice)
			}
		})
	}
}

func TestResponsesProvider_AutoOnlyToolChoice(t *testing.T) {
	for _, tc := range []struct {
		name       string
		tuning     RequestTuning
		wantChoice any
	}{
		{
			name:       "required downgraded without reasoning",
			tuning:     RequestTuning{OpenAI: OpenAITuning{ToolChoice: "required"}},
			wantChoice: "auto",
		},
		{
			name:       "required downgraded with reasoning",
			tuning:     RequestTuning{OpenAI: OpenAITuning{ToolChoice: "required", ReasoningEffort: "high"}},
			wantChoice: "auto",
		},
		{
			name:       "explicit auto still sent",
			tuning:     RequestTuning{OpenAI: OpenAITuning{ToolChoice: "auto"}},
			wantChoice: "auto",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var gotBody map[string]any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewDecoder(r.Body).Decode(&gotBody)
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, `data: {"type":"response.completed","response":{"id":"resp-1","status":"completed","output":[],"usage":{"input_tokens":5,"output_tokens":2}}}`+"\n\n")
				_, _ = io.WriteString(w, "data: [DONE]\n\n")
			}))
			defer server.Close()

			providerCfg := NewProviderConfig("sample", config.ProviderConfig{
				Type:   config.ProviderTypeResponses,
				APIURL: server.URL + "/v1/responses",
				Models: map[string]config.ModelConfig{
					"test-model": {
						Compat: &config.ModelCompatConfig{
							ForcedToolChoice: &config.ForcedToolChoiceCompatConfig{AutoOnly: new(true)},
						},
					},
				},
			}, []string{"test-key"})
			r := &ResponsesProvider{provider: providerCfg, client: server.Client()}
			_, err := r.CompleteStream(
				context.Background(), "test-key", "test-model", "",
				[]message.Message{{Role: "user", Content: "hello"}},
				[]message.ToolDefinition{{Name: "done", Description: "Finish", InputSchema: map[string]any{"type": "object"}}}, 0, tc.tuning,
				func(message.StreamDelta) {},
			)
			if err != nil {
				t.Fatalf("CompleteStream: %v", err)
			}
			if got := gotBody["tool_choice"]; got != tc.wantChoice {
				t.Fatalf("tool_choice = %#v, want %#v", got, tc.wantChoice)
			}
		})
	}
}

func TestResponsesProvider_SynthesizesReasoningTextForReplay(t *testing.T) {
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"type":"response.completed","response":{"id":"resp-1","status":"completed","output":[],"usage":{"input_tokens":5,"output_tokens":2}}}`+"\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	providerCfg := NewProviderConfig("sample", config.ProviderConfig{
		Type:   config.ProviderTypeResponses,
		APIURL: server.URL + "/v1/responses",
		Models: map[string]config.ModelConfig{
			"test-model": {
				Compat: &config.ModelCompatConfig{
					ReasoningContinuity: &config.ReasoningContinuityCompatConfig{Mode: "openai_visible"},
				},
			},
		},
	}, []string{"test-key"})
	r := &ResponsesProvider{provider: providerCfg, client: server.Client()}
	_, err := r.CompleteStream(
		context.Background(), "test-key", "test-model", "",
		[]message.Message{
			{Role: "user", Content: "do the thing"},
			{Role: "assistant", Content: "done", ToolCalls: []message.ToolCall{{ID: "call_1", Name: "read", Args: json.RawMessage(`{}`)}}},
			{Role: "tool", ToolCallID: "call_1", Content: "result"},
		},
		nil, 0, RequestTuning{OpenAI: OpenAITuning{ReasoningEffort: "high"}},
		func(message.StreamDelta) {},
	)
	if err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}

	input, ok := gotBody["input"].([]any)
	if !ok {
		t.Fatalf("input = %#v, want array", gotBody["input"])
	}
	var kinds []string
	for _, raw := range input {
		item, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("input item = %#v, want object", raw)
		}
		kinds = append(kinds, item["type"].(string))
	}
	// A synthesized reasoning item must precede the replayed assistant turn
	// that has no native reasoning payload.
	want := "message,reasoning,message,function_call,function_call_output"
	if got := strings.Join(kinds, ","); got != want {
		t.Fatalf("input item types = %v, want %v", kinds, want)
	}
}
