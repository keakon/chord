package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/toolname"
)

func TestNativeImageMutualExclusionAndSafeFallback(t *testing.T) {
	for _, scenario := range []string{"unsupported", "quota", "unknown", "refusal", "journal", "fallback_error", "decision_error", "hidden_tool", "text_only"} {
		t.Run(scenario, func(t *testing.T) {
			var requests atomic.Int32
			switched, finished := 0, 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := requests.Add(1)
				var body struct {
					Tools []map[string]any `json:"tools"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				native, local := false, false
				for _, d := range body.Tools {
					native = native || d["type"] == "image_generation"
					local = local || d["name"] == toolname.GenerateImage
				}
				if n == 1 && (!native || local) {
					t.Error("first request does not have an exclusive server image tool")
				}
				if n == 2 && (native || !local || finished != 1 || switched != 1) {
					t.Error("fallback was not authorized after durable rejection")
				}
				if n == 1 && scenario != "hidden_tool" && scenario != "text_only" {
					status, typ, code := 400, "invalid_request_error", "unsupported_tool"
					if scenario == "quota" {
						status, typ, code = 429, "rate_limit_error", "insufficient_quota"
					}
					if scenario == "unknown" {
						status, typ, code = 502, "server_error", "upstream_error"
					}
					if scenario == "refusal" {
						status, typ, code = 403, "permission_error", "permission_denied"
					}
					w.WriteHeader(status)
					fmt.Fprintf(w, `{"error":{"type":%q,"code":%q,"message":"Request rejected"}}`, typ, code)
					return
				}
				if scenario == "fallback_error" {
					w.WriteHeader(502)
					io.WriteString(w, `{"error":{"type":"server_error"}}`)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				output := `[]`
				if scenario != "text_only" {
					output = `[{"type":"function_call","id":"fc-1","call_id":"call-1","name":"generate_image","arguments":"{\"prompt\":\"tree\",\"operation\":\"generate\"}"}]`
				}
				fmt.Fprintf(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-1\",\"status\":\"completed\",\"output\":%s}}\n\n", output)
			}))
			defer srv.Close()
			provider := NewProviderConfig("sample", config.ProviderConfig{Type: config.ProviderTypeResponses, APIURL: srv.URL, Models: map[string]config.ModelConfig{"test-model": {NativeImageGeneration: &config.NativeImageGenerationConfig{Contract: config.NativeImageGenerationResponses, APIURL: srv.URL, Preauthorized: true, Model: "gpt-image-1.5"}}}}, []string{"key-1", "key-2"})
			impl, err := NewResponsesProvider(provider, "")
			if err != nil {
				t.Fatal(err)
			}
			client := NewClient(provider, impl, "test-model", 1024, "")
			defer client.Close()
			policy := &NativeToolPolicy{Permitted: func(string) bool { return true }, Begin: func(context.Context, NativeRequestRecord) (string, error) { return "request-1", nil }, Finish: func(_ string, outcome message.NativeRequestOutcome, _ *message.Response, _ error) error {
				finished++
				if scenario == "journal" {
					return fmt.Errorf("disk unavailable")
				}
				return nil
			}, ImageFallback: func() bool { switched++; return scenario != "decision_error" }}
			resp, err := client.CompleteStreamWithOptions(t.Context(), []message.Message{{Role: message.RoleUser, Content: "Generate a tree"}}, []message.ToolDefinition{{Name: toolname.GenerateImage, InputSchema: map[string]any{"type": "object"}}}, nil, CompleteStreamOptions{NativeTools: policy})
			want := int32(1)
			if scenario == "unsupported" || scenario == "quota" || scenario == "fallback_error" {
				want = 2
			}
			if requests.Load() != want {
				t.Fatalf("requests=%d want=%d err=%v", requests.Load(), want, err)
			}
			if scenario == "unsupported" || scenario == "quota" {
				if err != nil || resp == nil || len(resp.ToolCalls) != 1 || resp.NativeTools != nil {
					t.Fatalf("fallback response=%+v err=%v", resp, err)
				}
			} else if scenario == "text_only" {
				if err != nil || switched != 0 {
					t.Fatalf("text-only response incorrectly regenerated: %v", err)
				}
			} else if !IsNativeToolError(err) {
				t.Fatalf("non-terminal unsafe failure: %v", err)
			}
		})
	}
}

func TestNativeImageSearchCombinedAuthorization(t *testing.T) {
	p := NewProviderConfig("sample", config.ProviderConfig{Type: config.ProviderTypeResponses, APIURL: "https://example.invalid/v1/responses", Models: map[string]config.ModelConfig{"test-model": {NativeImageGeneration: &config.NativeImageGenerationConfig{Contract: config.NativeImageGenerationResponses, APIURL: "https://example.invalid/v1/responses", Preauthorized: true, Model: "gpt-image-1.5", MaxUses: 4}, NativeWebSearch: &config.NativeWebSearchConfig{Contract: config.NativeWebSearchResponses, APIURL: "https://example.invalid/v1/responses", Preauthorized: true, MaxUses: 2}}}}, nil)
	request, err := resolveNativeToolRequest(p, "test-model", RequestTuning{}, &NativeToolPolicy{Permitted: func(string) bool { return true }})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := request.applyDeclaration([]byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	var body struct {
		Tools []map[string]any `json:"tools"`
		Max   int              `json:"max_tool_calls"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Tools) != 2 || body.Max != 2 || !strings.Contains(string(request.authorization.Constraints), "Search") || request.formatContent == nil {
		t.Fatalf("combined declaration lost authorization: %s", raw)
	}
	request, err = resolveNativeToolRequest(p, "test-model", RequestTuning{}, &NativeToolPolicy{DisableImage: true, Permitted: func(string) bool { return true }})
	if err != nil || request.authorization.Tool != toolname.WebSearch {
		t.Fatal("fallback disabled server search")
	}
}
