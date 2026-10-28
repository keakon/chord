package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func TestCompactionClientCarriesSessionAndEmulatedInstructions(t *testing.T) {
	for _, configured := range []bool{false, true} {
		t.Run(map[bool]string{false: "inherited", true: "configured"}[configured], func(t *testing.T) {
			var body map[string]any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"summary\"}\n\n")
				_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"sample-response\",\"status\":\"completed\",\"output\":[]}}\n\n")
			}))
			defer server.Close()
			a := newTestMainAgent(t, t.TempDir())
			pc := llm.NewProviderConfig("sample", config.ProviderConfig{
				Type: config.ProviderTypeResponses, APIURL: server.URL, Compress: config.RequestCompressionOff,
				Emulate:   config.EmulationClientCodex,
				Emulation: &config.EmulationConfig{Codex: &config.CodexEmulationConfig{Version: "0.50.0"}},
				Models:    map[string]config.ModelConfig{"test-model": {Limit: config.ModelLimit{Context: 8192, Output: 1024}}},
			}, []string{"sample-key"})
			provider, err := llm.NewResponsesProvider(pc, "")
			if err != nil {
				t.Fatal(err)
			}
			source := llm.NewClient(pc, provider, "test-model", 128, "")
			a.llmClient = source
			if configured {
				a.globalConfig.Context.Compaction.ModelPool = "compact"
				a.globalConfig.ModelPools = map[string][]string{"compact": {"sample/test-model"}}
				a.SetModelSwitchFactory(func(string, []string, string) (*llm.Client, string, int, error) {
					return source, "test-model", 8192, nil
				})
			}
			client, _, err := a.newCompactionClient("")
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			client.SetSystemPrompt("sample instructions")
			if _, err := client.CompleteStream(t.Context(), []message.Message{{Role: message.RoleUser, Content: "sample query"}}, nil, func(message.StreamDelta) {}); err != nil {
				t.Fatal(err)
			}
			if body["instructions"] != nil || body["prompt_cache_key"] == nil {
				t.Fatalf("missing emulated shape or session: %#v", body)
			}
			item := body["input"].([]any)[0].(map[string]any)
			if item["role"] != "developer" || item["id"] == nil {
				t.Fatalf("missing stable developer item: %#v", item)
			}
		})
	}
}

func TestHostedClientUsesCallerSession(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	impl := &hostedScriptProvider{respond: func(int, context.Context) (*message.Response, error) {
		return hostedWebSearchResponse(), nil
	}}
	target := newHostedTestTarget("sample", hostedTestTargetOpts{
		typ: config.ProviderTypeMessages, modelID: "test-model", providerHosted: []string{tools.NameWebSearch},
	}, impl)
	setHostedTestPool(a, target)
	backend := newTestHostedBackend(t, a, tools.ResolveHostedToolCatalog(nil))
	if _, err := backend.Run(t.Context(), tools.NameWebSearch, map[string]any{"query": "sample query"}); err != nil {
		t.Fatal(err)
	}
	sub := &SubAgent{instanceID: "worker-1", taskID: "task-1", sessionDir: a.sessionDir, parent: a, llmClient: singleModelClient(target), cancel: func() {}}
	a.subs.add(sub)
	if _, err := a.HostedBackendForCaller(sub.instanceID).Run(t.Context(), tools.NameWebSearch, map[string]any{"query": "sample query"}); err != nil {
		t.Fatal(err)
	}
	calls := impl.callsSnapshot()
	if len(calls) != 2 {
		t.Fatalf("calls = %d, want 2", len(calls))
	}
	if calls[0].tuning.SessionKey != a.mainSessionIdentity(a.sessionDir).Key || calls[0].tuning.SubAgentSession {
		t.Fatalf("main identity = %+v", calls[0].tuning)
	}
	if calls[1].tuning.SessionKey != sub.sessionCacheKey() || !calls[1].tuning.SubAgentSession {
		t.Fatalf("subagent identity = %+v", calls[1].tuning)
	}
}
