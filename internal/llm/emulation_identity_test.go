package llm

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
)

func TestResponsesIdentityIndependentOfTimezone(t *testing.T) {
	original := time.Local
	t.Cleanup(func() { time.Local = original })
	time.Local = time.FixedZone("sample-a", 0)
	first := deriveResponsesWireIdentity("20260102150405123", 2, false)
	time.Local = time.FixedZone("sample-b", 8*60*60)
	second := deriveResponsesWireIdentity("20260102150405123", 2, false)
	if first != second {
		t.Fatalf("same persistent key changed identity with timezone: %#v -> %#v", first, second)
	}
}

func TestClaudeHostedEmulationMetadata(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "baseline", true: "emulated"}[enabled], func(t *testing.T) {
			var body struct {
				Metadata *anthropicMetadata `json:"metadata"`
			}
			var header http.Header
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				header = r.Header.Clone()
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
			}))
			defer server.Close()
			var emulation *config.EmulationConfig
			if enabled {
				emulation = &config.EmulationConfig{Claude: &config.ClaudeEmulationConfig{
					Version: "2.1.311", SDKVersion: "0.128.0", RuntimeVersion: "v26.12.1",
				}}
			}
			p := claudeEmulationProvider(emulation, server.URL, 0)
			a := &AnthropicProvider{provider: p, client: server.Client()}
			_, err := a.CompleteStream(t.Context(), "sample-key", "test-model", "sample instructions",
				[]message.Message{{Role: message.RoleUser, Content: "sample query"}}, nil, 128,
				RequestTuning{SessionKey: "20260102150405123", HostedTool: &HostedToolRequest{
					Name: "sample_tool", Declaration: json.RawMessage(sampleAnthropicDeclaration),
				}}, func(message.StreamDelta) {})
			if err != nil {
				t.Fatal(err)
			}
			if !enabled {
				if body.Metadata != nil || header.Get(headerXClaudeCodeSessionID) != "" {
					t.Fatalf("baseline acquired emulation identity: %+v %v", body, header)
				}
				return
			}
			if body.Metadata == nil {
				t.Fatal("hosted request omitted metadata")
			}
			var userID struct {
				SessionID string `json:"session_id"`
			}
			if err := json.Unmarshal([]byte(body.Metadata.UserID), &userID); err != nil {
				t.Fatal(err)
			}
			if userID.SessionID == "" || userID.SessionID != header.Get(headerXClaudeCodeSessionID) {
				t.Fatalf("metadata/header session mismatch: %+v %v", userID, header)
			}
		})
	}
}

func TestCodexEmulationWithoutSessionUsesDeveloperInstructions(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"sample-response\",\"status\":\"completed\",\"output\":[]}}\n\n")
	}))
	defer server.Close()
	p := codexEmulationProvider(&config.EmulationConfig{Codex: &config.CodexEmulationConfig{Version: "0.50.0"}}, server.URL)
	r := &ResponsesProvider{provider: p, client: server.Client()}
	_, err := r.CompleteStream(t.Context(), "sample-key", "test-model", "sample instructions",
		[]message.Message{{Role: message.RoleUser, Content: "sample query"}}, nil, 128, RequestTuning{}, func(message.StreamDelta) {})
	if err != nil {
		t.Fatal(err)
	}
	if body["instructions"] != nil {
		t.Fatalf("emulated request kept top-level instructions: %v", body["instructions"])
	}
	input := body["input"].([]any)
	item := input[0].(map[string]any)
	if item["role"] != responsesDeveloperItemRole || item["id"] != nil {
		t.Fatalf("sessionless developer message = %#v", item)
	}
}
