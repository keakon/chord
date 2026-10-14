package llm

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
)

func TestClientContinuationResolvesEachFallbackTarget(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprint(enabled), func(t *testing.T) {
			counts := map[string]int{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				model := "model-a"
				if strings.Contains(r.URL.Path, "model-b:") {
					model = "model-b"
				}
				counts[model]++
				var body struct {
					Generation struct {
						Max int `json:"maxOutputTokens"`
					} `json:"generationConfig"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				on := enabled
				if model == "model-a" {
					on = !enabled
				}
				want := 65536
				if on {
					want = 262144
				}
				if body.Generation.Max != want {
					t.Errorf("%s budget=%d want=%d", model, body.Generation.Max, want)
				}
				if model == "model-a" {
					fmt.Fprint(w, continuationChunk("", "", "CONTINUATION", -1, 0))
					return
				}
				if enabled && counts[model] == 1 {
					fmt.Fprint(w, continuationChunk("A", "T1", "CONTINUATION", 100, 1))
					return
				}
				fmt.Fprint(w, `data: {"candidates":[{"content":{"parts":[{"functionCall":{"name":"sample_tool","args":{}}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":101,"candidatesTokenCount":1}}`+"\n\n")
			}))
			defer server.Close()
			models := map[string]config.ModelConfig{}
			for _, id := range []string{"model-a", "model-b"} {
				on := enabled
				if id == "model-a" {
					on = !enabled
				}
				models[id] = config.ModelConfig{Limit: config.ModelLimit{Context: 1048576, Output: 1048576}, Compat: &config.ModelCompatConfig{GenerateContent: &config.GenerateContentCompatConfig{AutomaticContinuation: new(on), SingleRequestOutputLimit: new(65536)}}}
			}
			cfg := NewProviderConfig("sample", config.ProviderConfig{Type: config.ProviderTypeGenerateContent, APIURL: server.URL + "/v1beta", Models: models}, []string{"test-key"})
			p, err := NewGeminiProviderWithClient(cfg, server.Client(), "")
			if err != nil {
				t.Fatal(err)
			}
			client := NewClient(cfg, p, "model-a", 262144, "")
			defer client.Close()
			client.SetOutputTokenMax(262144)
			client.SetFallbackModels([]FallbackModel{{ProviderConfig: cfg, ProviderImpl: p, ModelID: "model-b", MaxTokens: 262144}})
			toolEvents := 0
			resp, err := client.CompleteStream(t.Context(), []message.Message{{Role: message.RoleUser, Content: "sample"}}, nil, func(d message.StreamDelta) {
				if d.ToolCall != nil {
					toolEvents++
					if d.ToolCall.DeferExecution != enabled {
						t.Error("inherited wrong target execution policy")
					}
				}
			})
			if err != nil || len(resp.ToolCalls) != 1 || toolEvents != 3 || counts["model-a"] != 1 {
				t.Fatalf("resp=%+v err=%v counts=%v tools=%d", resp, err, counts, toolEvents)
			}
			want := 1
			if enabled {
				want = 2
			}
			if counts["model-b"] != want {
				t.Fatalf("counts=%v", counts)
			}
		})
	}
}

func TestClientContinuationExhaustionDoesNotMultiplyKeysOrRounds(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests++; fmt.Fprint(w, "data: {invalid}\n\n") }))
	defer server.Close()
	models := map[string]config.ModelConfig{}
	for _, id := range []string{"model-a", "model-b"} {
		models[id] = config.ModelConfig{Compat: &config.ModelCompatConfig{GenerateContent: &config.GenerateContentCompatConfig{AutomaticContinuation: new(true)}}}
	}
	cfg := NewProviderConfig("sample", config.ProviderConfig{Type: config.ProviderTypeGenerateContent, APIURL: server.URL + "/v1beta", Models: models}, []string{"key-1", "key-2", "key-3"})
	p, err := NewGeminiProviderWithClient(cfg, server.Client(), "")
	if err != nil {
		t.Fatal(err)
	}
	client := NewClient(cfg, p, "model-a", 100, "")
	defer client.Close()
	client.SetStreamRetryRounds(4)
	client.SetFallbackModels([]FallbackModel{{ProviderConfig: cfg, ProviderImpl: p, ModelID: "model-b", MaxTokens: 100}})
	_, err = client.CompleteStream(t.Context(), []message.Message{{Role: message.RoleUser, Content: "sample"}}, nil, nil)
	if _, ok := errors.AsType[*GeminiContinuationError](err); !ok || requests != 2 {
		t.Fatalf("requests=%d err=%v", requests, err)
	}
}

func TestClientContinuationInitialQuotaRotatesKey(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprint(enabled), func(t *testing.T) {
			var mu sync.Mutex
			firstKey := ""
			counts := map[string]int{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				key := r.Header.Get("x-goog-api-key")
				mu.Lock()
				if firstKey == "" {
					firstKey = key
				}
				reject := key == firstKey
				counts[key]++
				mu.Unlock()
				if reject {
					w.WriteHeader(http.StatusTooManyRequests)
					fmt.Fprint(w, `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","message":"quota exhausted"}}`)
					return
				}
				fmt.Fprint(w, continuationChunk("Ready", "", "STOP", 100, 1))
			}))
			defer server.Close()
			cfg := NewProviderConfig("sample", config.ProviderConfig{Type: config.ProviderTypeGenerateContent, APIURL: server.URL + "/v1beta", Models: map[string]config.ModelConfig{"test-model": {Compat: &config.ModelCompatConfig{GenerateContent: &config.GenerateContentCompatConfig{AutomaticContinuation: new(enabled)}}}}}, []string{"key-1", "key-2"})
			p, err := NewGeminiProviderWithClient(cfg, server.Client(), "")
			if err != nil {
				t.Fatal(err)
			}
			client := NewClient(cfg, p, "test-model", 100, "")
			defer client.Close()
			client.SetStreamRetryRounds(1)
			resp, err := client.CompleteStream(t.Context(), []message.Message{{Role: message.RoleUser, Content: "sample"}}, nil, nil)
			mu.Lock()
			defer mu.Unlock()
			if err != nil || resp == nil || resp.Content != "Ready" {
				t.Fatalf("response=%+v error=%v requests=%v", resp, err, counts)
			}
			if len(counts) != 2 || counts[firstKey] != 1 {
				t.Fatalf("key rotation requests=%v", counts)
			}
		})
	}
}
