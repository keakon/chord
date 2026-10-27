package agent

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/gorilla/websocket"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
)

func directoryTransportFixture(t *testing.T) (*directoryInstructionRuntime, []message.Message, []message.DirectoryInstructionFact) {
	t.Helper()
	r, root := newResolverFixture(t)
	facts := []message.DirectoryInstructionFact{
		{CheckoutRoot: root, Scope: ".", SourcePath: root + "/AGENTS.md", Operation: message.DirectoryInstructionUpsert, Body: "Root instruction: keep exact identifiers.\nPreserve this second line."},
		{CheckoutRoot: root, Scope: "module", SourcePath: root + "/module/AGENTS.md", Operation: message.DirectoryInstructionUpsert, Body: "Module instruction: inspect the schema before changing storage."},
	}
	for i := range facts {
		facts[i].BodyHash = message.DirectoryInstructionBodyHash(facts[i].Body)
	}
	r.view.Store(&directoryInstructionView{Initial: facts[:1], Dynamic: facts[1:]})
	messages := []message.Message{directoryInstructionProjection(facts[:1]), {Role: message.RoleUser, Content: "Inspect the module."}, directoryInstructionProjection(facts[1:])}
	messages = append(messages, message.Message{Role: message.RoleUser, Kind: message.KindSkillDiscovery, Content: "Optional matched skill: storage-review; call skill to load its instructions.", SkillDiscovery: &message.SkillDiscoveryRecord{ID: "local-notice-secret", TaskID: "main", CheckoutRoot: root, Skills: []message.SkillActivationRef{{Identity: "local-skill-secret", Revision: "v1", Name: "storage-review"}}}})
	return r, messages, facts
}
func directoryTransportText(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case []any:
		var b strings.Builder
		for _, item := range v {
			b.WriteString(directoryTransportText(item))
			b.WriteByte('\n')
		}
		return b.String()
	case map[string]any:
		var b strings.Builder
		for _, item := range v {
			b.WriteString(directoryTransportText(item))
			b.WriteByte('\n')
		}
		return b.String()
	}
	return ""
}
func directoryTransportResponse(protocol string) string {
	switch protocol {
	case config.ProviderTypeMessages:
		return "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg-1\",\"role\":\"assistant\",\"usage\":{\"input_tokens\":20,\"output_tokens\":0}}}\n\nevent: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"Complete\"}}\n\nevent: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	case config.ProviderTypeResponses:
		return `{"type":"response.completed","response":{"id":"response-1","status":"completed","output":[{"type":"message","id":"message-1","role":"assistant","content":[{"type":"output_text","text":"Complete","annotations":[]}]}],"usage":{"input_tokens":20,"output_tokens":1}}}`
	case config.ProviderTypeGenerateContent:
		return "data: " + `{"candidates":[{"content":{"role":"model","parts":[{"text":"Complete"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":20,"candidatesTokenCount":1,"totalTokenCount":21}}` + "\n\n"
	default:
		return "data: " + `{"choices":[{"index":0,"delta":{"content":"Complete"},"finish_reason":"stop"}]}` + "\n\ndata: [DONE]\n\n"
	}
}
func newDirectoryTransportProvider(t *testing.T, cfg *llm.ProviderConfig, protocol string) llm.Provider {
	t.Helper()
	var impl llm.Provider
	var err error
	switch protocol {
	case config.ProviderTypeMessages:
		impl, err = llm.NewAnthropicProvider(cfg, "")
	case config.ProviderTypeResponses:
		impl, err = llm.NewResponsesProvider(cfg, "")
	case config.ProviderTypeGenerateContent:
		impl, err = llm.NewGeminiProvider(cfg, "")
	default:
		impl, err = llm.NewOpenAIProvider(cfg, "")
	}
	if err != nil {
		t.Fatal(err)
	}
	return impl
}
func TestDirectoryInstructionTransportPreservesRulesAndAttemptEvidence(t *testing.T) {
	cases := []struct {
		protocol  string
		websocket bool
	}{{config.ProviderTypeChatCompletions, false}, {config.ProviderTypeMessages, false}, {config.ProviderTypeResponses, false}, {config.ProviderTypeGenerateContent, false}, {config.ProviderTypeResponses, true}}
	for _, test := range cases {
		t.Run(fmt.Sprintf("%s/websocket=%t", test.protocol, test.websocket), func(t *testing.T) {
			r, messages, facts := directoryTransportFixture(t)
			var submissions atomic.Int32
			assertWire := func(body any) {
				submission := submissions.Add(1)
				text := directoryTransportText(body)
				if !strings.Contains(text, "Optional matched skill: storage-review; call skill to load its instructions.") {
					t.Error("wire lost skill discovery text")
				}
				encoded, err := json.Marshal(body)
				if err != nil {
					t.Fatal(err)
				}
				for _, internal := range []string{"local-notice-secret", "local-skill-secret", "skill_discovery", "directory_instruction_refs"} {
					if strings.Contains(string(encoded), internal) {
						t.Errorf("wire leaked internal metadata %s", internal)
					}
				}
				for _, fact := range facts {
					if !strings.Contains(text, fact.Body) {
						t.Errorf("wire lost exact rule body for %s: %s", fact.Scope, text)
					}
				}
				attempt := r.attempt.Load()
				if attempt == nil {
					t.Error("transport submitted before request evidence")
					return
				}
				if attempt.request.RequestSeq != 7 || attempt.request.AttemptSeq != uint64(submission) || len(attempt.request.Visible) != len(facts) {
					t.Errorf("bad evidence: %+v", attempt.request)
				}
				for _, fact := range facts {
					if _, ok := attempt.request.Visible[fact.Ref()]; !ok {
						t.Errorf("scope %s missing from evidence", fact.Scope)
					}
				}
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if test.websocket {
					// Codex sends a non-generating prewarm followed by the
					// real request, potentially as a previous-response delta.
					if !websocket.IsWebSocketUpgrade(req) {
						if strings.HasSuffix(req.URL.Path, "/usage") {
							w.Header().Set("Content-Type", "application/json")
							_, _ = io.WriteString(w, `{}`)
							return
						}
						t.Errorf("unexpected HTTP fallback: %s", req.URL.Path)
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					conn, err := (&websocket.Upgrader{}).Upgrade(w, req, nil)
					if err != nil {
						t.Error(err)
						return
					}
					defer conn.Close()
					var baseline any
					for frame := range 2 {
						_, raw, err := conn.ReadMessage()
						if err != nil {
							t.Error(err)
							return
						}
						var body map[string]any
						if err := json.Unmarshal(raw, &body); err != nil {
							t.Error(err)
							return
						}
						if frame == 0 {
							if body["generate"] != false {
								t.Error("first frame is not a prewarm")
							}
							baseline = body
							assertWire(body)
							if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.completed","response":{"id":"warmup-1","status":"completed","output":[],"usage":{"input_tokens":20,"output_tokens":0}}}`)); err != nil {
								t.Error(err)
								return
							}
						} else {
							if prior, ok := body["previous_response_id"].(string); ok && prior != "" {
								if prior != "warmup-1" {
									t.Errorf("unknown replay baseline: %s", prior)
								}
								assertWire([]any{baseline, body})
							} else {
								assertWire(body)
							}
							if err := conn.WriteMessage(websocket.TextMessage, []byte(directoryTransportResponse(test.protocol))); err != nil {
								t.Error(err)
							}
						}
					}
					return
				}
				var body any
				if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
					t.Error(err)
					return
				}
				assertWire(body)
				w.Header().Set("Content-Type", "text/event-stream")
				response := directoryTransportResponse(test.protocol)
				if test.protocol == config.ProviderTypeResponses {
					response = "data: " + response + "\n\n"
				}
				_, _ = io.WriteString(w, response)
			}))
			defer server.Close()
			apiURL := server.URL + "/v1/chat/completions"
			switch test.protocol {
			case config.ProviderTypeMessages:
				apiURL = server.URL + "/v1/messages"
			case config.ProviderTypeResponses:
				apiURL = server.URL + "/v1/responses"
			case config.ProviderTypeGenerateContent:
				apiURL = server.URL + "/v1beta/"
			}
			configValue := config.ProviderConfig{Type: test.protocol, APIURL: apiURL, ResponsesWebsocket: new(test.websocket), Models: map[string]config.ModelConfig{"test-model": {Limit: config.ModelLimit{Context: 32000, Output: 512}}}}
			if test.websocket {
				configValue.Preset = config.ProviderPresetCodex
			}
			cfg := llm.NewProviderConfig("sample", configValue, []string{"key"})
			if test.websocket {
				cfg.SetOAuthRefresher(config.OpenAIOAuthTokenURL, config.OpenAIOAuthClientID, "", "", &config.AuthConfig{}, &sync.Mutex{}, map[string]llm.OAuthKeySetup{"key": {CredentialIndex: 0, AccountID: "account", Expires: 32503680000000}}, "")
			}
			client := llm.NewClient(cfg, newDirectoryTransportProvider(t, cfg, test.protocol), "test-model", 512, "")
			defer client.Close()
			prepare := prepareDirectoryInstructionAttempt(r, 7)
			if _, err := prepare(t.Context(), llm.RequestAttempt{Messages: messages, InputBudget: 32000}); err != nil {
				t.Fatal(err)
			}
			if r.attempt.Load() != nil {
				t.Fatal("preparing surface published transport evidence")
			}
			_, err := client.CompleteStreamWithOptions(t.Context(), messages, nil, func(message.StreamDelta) {}, llm.CompleteStreamOptions{PrepareAttempt: prepare})
			if err != nil {
				t.Fatal(err)
			}
			wantSubmissions := int32(1)
			if test.websocket {
				wantSubmissions = 2
			}
			if submissions.Load() != wantSubmissions {
				t.Fatalf("submissions=%d", submissions.Load())
			}
		})
	}
}
