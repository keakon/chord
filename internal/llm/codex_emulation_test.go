package llm

import (
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

// codexEmulationProvider builds a Responses provider carrying the emulation
// payload and selector exactly as the loader would deliver them, pointed at
// apiURL. No preset is set: the emulation gate is the provider type, and
// leaving the preset out keeps the OAuth and WebSocket transports out of the
// test.
func codexEmulationProvider(emulation *config.EmulationConfig, apiURL string) *ProviderConfig {
	return NewProviderConfig("sample", config.ProviderConfig{
		Type:      config.ProviderTypeResponses,
		APIURL:    apiURL,
		Compress:  config.RequestCompressionOff,
		Emulation: emulation,
		Emulate:   config.EmulationClientCodex,
		Models:    map[string]config.ModelConfig{"test-model": {Limit: config.ModelLimit{Context: 8192, Output: 1024}}},
	}, []string{"api-key"})
}

// captureCodexEmulationRequest sends one tool-less request through an emulated
// provider and returns the headers and decoded body the test server received.
func captureCodexEmulationRequest(t *testing.T, emulation *config.EmulationConfig) (http.Header, map[string]any) {
	t.Helper()
	var gotHeader http.Header
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Clone()
		data, err := readRequestBody(r)
		if err != nil {
			t.Errorf("read request body: %v", err)
		}
		if err := json.Unmarshal(data, &gotBody); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"type":"response.completed","response":{"id":"resp-1","status":"completed","output":[],"usage":{"input_tokens":5,"output_tokens":2}}}`+"\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(server.Close)

	providerCfg := codexEmulationProvider(emulation, server.URL+"/v1/responses")
	r := &ResponsesProvider{provider: providerCfg, client: server.Client()}
	if _, err := r.CompleteStream(
		context.Background(), "api-key", "test-model", "you are a helpful assistant",
		[]message.Message{{Role: "user", Content: "hello"}},
		nil, 128, RequestTuning{SessionKey: "20260102150405123"},
		func(message.StreamDelta) {},
	); err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}
	return gotHeader, gotBody
}

func TestCodexEmulationWritesCodexBodyShapeAndUserAgent(t *testing.T) {
	header, body := captureCodexEmulationRequest(t, &config.EmulationConfig{Codex: &config.CodexEmulationConfig{Version: "0.50.0"}})

	if _, ok := body["instructions"]; ok {
		t.Fatalf("emulated request still carries top-level instructions: %#v", body["instructions"])
	}
	input, ok := body["input"].([]any)
	if !ok || len(input) == 0 {
		t.Fatalf("body input = %#v, want a non-empty array", body["input"])
	}
	first, ok := input[0].(map[string]any)
	if !ok {
		t.Fatalf("input[0] = %#v, want an object", input[0])
	}
	if first["type"] != "message" || first["role"] != responsesDeveloperItemRole {
		t.Fatalf("input[0] = type %v role %v, want a developer message", first["type"], first["role"])
	}
	id, _ := first["id"].(string)
	want := baseInstructionsItemID(deriveResponsesWireIdentity("20260102150405123", 0, false).sessionID, "you are a helpful assistant")
	if !strings.HasPrefix(id, "msg_") {
		t.Fatalf("input[0] id = %q, want the deterministic msg_ prefix", id)
	}
	if id != want {
		t.Errorf("input[0] id = %q, want %q", id, want)
	}
	if text := inputItemText(first); text != "you are a helpful assistant" {
		t.Fatalf("input[0] text = %q, want the base instructions", text)
	}
	// The Codex client sends the tool fields even for a request with no tools.
	if body["tool_choice"] != "auto" {
		t.Fatalf("tool_choice = %#v, want \"auto\" for a tool-less emulated request", body["tool_choice"])
	}
	if body["parallel_tool_calls"] != true {
		t.Fatalf("parallel_tool_calls = %#v, want true for a tool-less emulated request", body["parallel_tool_calls"])
	}
	if want := codexClientUserAgent("0.50.0"); header.Get("User-Agent") != want {
		t.Fatalf("User-Agent = %q, want %q", header.Get("User-Agent"), want)
	}
	if got := header.Get("originator"); got != "codex-tui" {
		t.Fatalf("originator = %q, want the emulated TUI client name", got)
	}
}

func TestCodexEmulationDisabledKeepsChordRequestShape(t *testing.T) {
	header, body := captureCodexEmulationRequest(t, nil)

	if got, _ := body["instructions"].(string); got != "you are a helpful assistant" {
		t.Fatalf("instructions = %#v, want the base instructions as a top-level field", body["instructions"])
	}
	input, _ := body["input"].([]any)
	if len(input) == 0 {
		t.Fatalf("body input = %#v, want the conversation without an instructions prefix", body["input"])
	}
	if first, _ := input[0].(map[string]any); first["role"] == responsesDeveloperItemRole {
		t.Fatalf("input[0] = %#v, want no emulated instructions item", first)
	}
	if _, ok := body["tool_choice"]; ok {
		t.Fatalf("tool_choice = %#v, want it absent for a tool-less request", body["tool_choice"])
	}
	if _, ok := body["parallel_tool_calls"]; ok {
		t.Fatalf("parallel_tool_calls = %#v, want it absent for a tool-less request", body["parallel_tool_calls"])
	}
	if ua := header.Get("User-Agent"); !strings.HasPrefix(ua, "chord/") {
		t.Fatalf("User-Agent = %q, want Chord's own agent without the opt-in", ua)
	}
	if got := header.Get("originator"); got != openAICodexOriginator {
		t.Fatalf("originator = %q, want the baseline without the opt-in", got)
	}
}

func TestCodexEmulationWithoutVersionImitatesNothing(t *testing.T) {
	header, body := captureCodexEmulationRequest(t, &config.EmulationConfig{Codex: &config.CodexEmulationConfig{}})

	if got, _ := body["instructions"].(string); got != "you are a helpful assistant" {
		t.Fatalf("instructions = %#v, want Chord's top-level field: an opt-in without a version has nothing to imitate", body["instructions"])
	}
	if _, ok := body["tool_choice"]; ok {
		t.Fatalf("tool_choice = %#v, want it absent for a tool-less request", body["tool_choice"])
	}
	if _, ok := body["parallel_tool_calls"]; ok {
		t.Fatalf("parallel_tool_calls = %#v, want it absent for a tool-less request", body["parallel_tool_calls"])
	}
	if ua := header.Get("User-Agent"); !strings.HasPrefix(ua, "chord/") {
		t.Fatalf("User-Agent = %q, want Chord's own agent: there is no default Codex version to assume", ua)
	}
	if got := header.Get("originator"); got != openAICodexOriginator {
		t.Fatalf("originator = %q, want the baseline: there is no client to imitate without a version", got)
	}
}

func TestCodexEmulationProviderUserAgentStillWins(t *testing.T) {
	providerCfg := NewProviderConfig("sample", config.ProviderConfig{
		Type:      config.ProviderTypeResponses,
		APIURL:    "https://example.invalid/v1/responses",
		UserAgent: "my-agent/1.2.3",
		Emulation: &config.EmulationConfig{Codex: &config.CodexEmulationConfig{Version: "0.50.0"}},
		Emulate:   config.EmulationClientCodex,
		Models:    map[string]config.ModelConfig{"test-model": {}},
	}, []string{"api-key"})

	header := http.Header{}
	setProviderLLMUserAgent(header, providerCfg, "")
	if ua := header.Get("User-Agent"); ua != "my-agent/1.2.3" {
		t.Fatalf("User-Agent = %q, want the explicit provider override", ua)
	}
}

func TestCodexEmulationOnlyAppliesToResponsesProviders(t *testing.T) {
	emulation := &config.EmulationConfig{Codex: &config.CodexEmulationConfig{Version: "0.50.0"}}
	for _, providerType := range []string{config.ProviderTypeChatCompletions, config.ProviderTypeMessages, config.ProviderTypeGenerateContent} {
		providerCfg := NewProviderConfig("sample", config.ProviderConfig{
			Type:      providerType,
			APIURL:    "https://example.invalid/v1",
			Emulation: emulation,
			Emulate:   config.EmulationClientCodex,
			Models:    map[string]config.ModelConfig{"test-model": {}},
		}, []string{"api-key"})
		if providerCfg.codexShapeEmulated("") {
			t.Errorf("provider type %q emulates the Codex body shape", providerType)
		}
		if ua := providerCfg.codexEmulationUserAgent(""); ua != "" {
			t.Errorf("provider type %q User-Agent = %q, want empty", providerType, ua)
		}
	}

	responsesCfg := codexEmulationProvider(emulation, "https://example.invalid/v1/responses")
	if !responsesCfg.codexShapeEmulated("") {
		t.Fatal("a Responses provider with the opt-in must emulate the Codex body shape")
	}
	if ua := responsesCfg.codexEmulationUserAgent(""); !strings.HasPrefix(ua, "codex-tui/0.50.0 (") {
		t.Fatalf("User-Agent = %q, want the Codex client agent", ua)
	}
}

func TestNewProviderConfigCarriesCodexEmulation(t *testing.T) {
	emulation := &config.EmulationConfig{Codex: &config.CodexEmulationConfig{Version: "0.50.0"}}
	if got := codexEmulationProvider(emulation, "https://example.invalid/v1/responses").emulation; got != emulation {
		t.Fatalf("provider emulation = %#v, want the config pointer it was built from", got)
	}
	if got := codexEmulationProvider(nil, "https://example.invalid/v1/responses").emulation; got != nil {
		t.Fatalf("provider emulation = %#v, want nil without payload definitions", got)
	}
}

// Emulating the Codex client changes the request body layout and identity, not
// the transfer encoding. Only preset: codex turns zstd on by default, because a
// relay selected for the emulation does not necessarily accept compressed
// bodies; a user who wants zstd there sets compress explicitly.
func TestEmulateCodexDoesNotChangeTheCompressionDefault(t *testing.T) {
	emulation := &config.EmulationConfig{Codex: &config.CodexEmulationConfig{Version: "0.50.0"}}
	emulated := NewProviderConfig("sample", config.ProviderConfig{
		Type:      config.ProviderTypeResponses,
		APIURL:    "https://example.invalid/v1/responses",
		Emulation: emulation,
		Emulate:   config.EmulationClientCodex,
		Models:    map[string]config.ModelConfig{"test-model": {}},
	}, []string{"api-key"})
	if got := emulated.RequestCompression(); got != "" {
		t.Fatalf("emulate: codex without the codex preset compression = %q, want uncompressed", got)
	}

	preset := config.ProviderConfig{
		Type:   config.ProviderTypeResponses,
		Preset: config.ProviderPresetCodex,
		Models: map[string]config.ModelConfig{"test-model": {}},
	}
	if got := NewProviderConfig("sample", preset, []string{"api-key"}).RequestCompression(); got != config.RequestCompressionZstd {
		t.Fatalf("preset: codex compression = %q, want the preset default %q", got, config.RequestCompressionZstd)
	}
}

// inputItemText returns the concatenated text blocks of a message input item.
func inputItemText(item map[string]any) string {
	content, _ := item["content"].([]any)
	var text strings.Builder
	for _, block := range content {
		blockMap, _ := block.(map[string]any)
		if t, _ := blockMap["text"].(string); t != "" {
			text.WriteString(t)
		}
	}
	return text.String()
}
