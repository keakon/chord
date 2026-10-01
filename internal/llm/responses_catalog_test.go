package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
)

func TestCatalogResponsesRulesReachRequestBody(t *testing.T) {
	for _, tc := range []struct {
		name, provider, model                    string
		sendParallel, parallel, sendStore, store bool
	}{
		{name: "catalog defaults", sendParallel: true, parallel: true, sendStore: true},
		{name: "provider omission", provider: "    compat:\n      responses:\n        send_parallel_tool_calls: false\n        send_store: false\n", sendParallel: false, sendStore: false},
		{name: "model overrides provider", provider: "    compat:\n      responses:\n        send_parallel_tool_calls: false\n", model: "        compat:\n          responses:\n            send_parallel_tool_calls: true\n        parallel_tool_calls: false\n        store: true\n", sendParallel: true, parallel: false, sendStore: true, store: true},
		{name: "model null preserves provider contract", provider: "    compat:\n      responses:\n        send_parallel_tool_calls: false\n", model: "        compat: null\n", sendParallel: false, sendStore: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			yaml := fmt.Sprintf("providers:\n  sample:\n    preset: openai\n%s    models:\n      alias:\n        catalog: openai/gpt-6.1-sol\n%smodel_pools:\n  default: [sample/alias]\n", tc.provider, tc.model)
			if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
				t.Fatal(err)
			}
			rc, err := config.LoadResolvedConfig(path, "")
			if err != nil {
				t.Fatal(err)
			}
			if len(rc.Diagnostics) != 0 {
				t.Fatalf("diagnostics = %+v", rc.Diagnostics)
			}
			bodies := make(chan map[string]any, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Errorf("decode request: %v", err)
				}
				bodies <- body
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-1\",\"status\":\"completed\",\"output\":[]}}\n\ndata: [DONE]\n\n")
			}))
			defer server.Close()
			target, err := url.Parse(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			transport := server.Client().Transport
			client := &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				// Redirect the transport after resolution; the preset contract stays intact.
				clone := req.Clone(req.Context())
				clone.URL.Scheme, clone.URL.Host = target.Scheme, target.Host
				return transport.RoundTrip(clone)
			})}
			providerConfig := NewProviderConfig("sample", rc.Config.Providers["sample"], []string{"sample-key"})
			defer providerConfig.Close()
			provider, err := NewResponsesProviderWithClient(providerConfig, client, "")
			if err != nil {
				t.Fatal(err)
			}
			model := rc.Config.Providers["sample"].Models["alias"]
			tuning := RequestTuning{OpenAI: OpenAITuning{ParallelToolCalls: model.ParallelToolCalls}}
			_, err = provider.CompleteStream(context.Background(), "sample-key", "alias", "", []message.Message{{Role: message.RoleUser, Content: "hello"}}, []message.ToolDefinition{{Name: "sample_tool", InputSchema: map[string]any{"type": "object"}}}, 128, tuning, func(message.StreamDelta) {})
			if err != nil {
				t.Fatal(err)
			}
			body := <-bodies
			parallel, parallelPresent := body["parallel_tool_calls"]
			store, storePresent := body["store"]
			if parallelPresent != tc.sendParallel || (parallelPresent && parallel != tc.parallel) {
				t.Fatalf("parallel_tool_calls = %v (present %v), want %v (present %v)", parallel, parallelPresent, tc.parallel, tc.sendParallel)
			}
			if storePresent != tc.sendStore || (storePresent && store != tc.store) {
				t.Fatalf("store = %v (present %v), want %v (present %v)", store, storePresent, tc.store, tc.sendStore)
			}
		})
	}
}
