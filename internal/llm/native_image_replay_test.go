package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
)

func TestNativeFailedImageReplayPreservesConfirmedFailure(t *testing.T) {
	raw := json.RawMessage(`{"id":"image-1","type":"image_generation_call","status":"failed","output_format":"png"}`)
	resp := &message.Response{}
	recordResponsesHostedItem(resp, make(map[string]*responsesHostedCallState), raw, true)
	msg := message.Message{Role: message.RoleAssistant, NativeTools: &message.NativeToolHistory{Items: []json.RawMessage{raw}, Calls: resp.Hosted.Calls}}
	before, _ := json.Marshal(msg)
	projected, err := projectNativeImageReplay(t.Context(), []message.Message{msg})
	if err != nil {
		t.Fatal(err)
	}
	var item struct {
		Status string          `json:"status"`
		Result json.RawMessage `json:"result"`
		Format string          `json:"output_format"`
	}
	if err := json.Unmarshal(projected[0].NativeTools.Items[0], &item); err != nil {
		t.Fatal(err)
	}
	if item.Status != message.HostedCallStatusFailed || string(item.Result) != "null" || item.Format != "png" {
		t.Fatalf("failed receipt changed: %+v", item)
	}
	after, _ := json.Marshal(msg)
	if string(before) != string(after) {
		t.Fatal("replay mutated canonical failure")
	}
	var input []json.RawMessage
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Input []json.RawMessage `json:"input"`
			Store bool              `json:"store"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if request.Store {
			t.Error("failure replay depends on server storage")
		}
		input = request.Input
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"response-1\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"id\":\"message-1\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"Ready\"}]}]}}\n\n")
	}))
	defer server.Close()
	cfg := NewProviderConfig("sample", config.ProviderConfig{Type: config.ProviderTypeResponses, APIURL: server.URL, Models: map[string]config.ModelConfig{"test-model": {Limit: config.ModelLimit{Context: 32000, Output: 1024}}}}, []string{"sample-key"})
	provider, err := NewResponsesProvider(cfg, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.CompleteStream(t.Context(), "sample-key", "test-model", "", []message.Message{msg}, nil, 1024, RequestTuning{}, nil); err != nil {
		t.Fatal(err)
	}
	if len(input) != 1 {
		t.Fatalf("wrong replay surface: %d", len(input))
	}
	if err := json.Unmarshal(input[0], &item); err != nil {
		t.Fatal(err)
	}
	if item.Status != message.HostedCallStatusFailed || string(item.Result) != "null" {
		t.Fatalf("wire replay lost failure: %+v", item)
	}
}

func TestNativeImageReplayRejectsUnconfirmedOutcomes(t *testing.T) {
	for _, scenario := range []string{"missing_error", "changed_status", "unexpected_original", "unknown", "in_progress", "missing_receipt"} {
		t.Run(scenario, func(t *testing.T) {
			status := message.HostedCallStatusFailed
			call := message.HostedCall{ID: "image-1", Kind: message.HostedCallKindImageGeneration, Status: status, Error: "image_generation_call: failed"}
			native := &message.NativeToolHistory{}
			switch scenario {
			case "missing_error":
				call.Error = ""
			case "changed_status":
				call.Status = message.HostedCallStatusCompleted
			case "unexpected_original":
				call.Parts = []message.ContentPart{{Type: message.ContentPartImage}}
			case "unknown":
				native.OutcomeUnknown = true
			case "in_progress":
				status = "in_progress"
				call.Status = status
			}
			native.Items = []json.RawMessage{json.RawMessage(`{"id":"image-1","type":"image_generation_call","status":"` + status + `"}`)}
			if scenario != "missing_receipt" {
				native.Calls = []message.HostedCall{call}
			}
			if _, err := projectNativeImageReplay(t.Context(), []message.Message{{Role: message.RoleAssistant, NativeTools: native}}); err == nil || !strings.Contains(err.Error(), "native image") {
				t.Fatalf("unsafe receipt accepted: %v", err)
			}
		})
	}
}

func TestNativeImageUnexpectedStatesRemainUnknown(t *testing.T) {
	for _, tc := range []struct {
		status     string
		errorField bool
	}{
		{"cancelled", false}, {"incomplete", false}, {"in_progress", false}, {"generating", false},
		{"in_progress", true}, {"completed", true},
	} {
		t.Run(fmt.Sprintf("%s/error=%t", tc.status, tc.errorField), func(t *testing.T) {
			item := map[string]any{"id": "image-1", "type": message.HostedCallKindImageGeneration, "status": tc.status}
			if tc.errorField {
				item["error"] = map[string]string{"message": "Image unavailable"}
			}
			raw, err := json.Marshal(item)
			if err != nil {
				t.Fatal(err)
			}
			var requests atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprintf(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"response-1\",\"status\":\"completed\",\"output\":[%s]}}\n\n", raw)
			}))
			defer srv.Close()
			pc := NewProviderConfig("sample", config.ProviderConfig{Type: config.ProviderTypeResponses, APIURL: srv.URL,
				Models: map[string]config.ModelConfig{"test-model": {NativeImageGeneration: &config.NativeImageGenerationConfig{
					Contract: config.NativeImageGenerationResponses, APIURL: srv.URL, Preauthorized: true, Model: "gpt-image-1.5",
				}}}}, []string{"sample-key", "second-key"})
			impl, err := NewResponsesProvider(pc, "")
			if err != nil {
				t.Fatal(err)
			}
			client := NewClient(pc, impl, "test-model", 1024, "")
			defer client.Close()
			var receipt *message.NativeToolHistory
			policy := &NativeToolPolicy{Permitted: func(string) bool { return true },
				Begin: func(context.Context, NativeRequestRecord) (string, error) { return "request-1", nil },
				Finish: func(_ string, outcome message.NativeRequestOutcome, _ *message.Response, err error) error {
					if outcome != message.NativeRequestUnknown || err == nil {
						t.Errorf("unconfirmed response accepted: outcome=%s err=%v", outcome, err)
					}
					return nil
				}, Failed: func(h *message.NativeToolHistory) { receipt = h },
			}
			_, err = client.CompleteStreamWithOptions(t.Context(), []message.Message{{Role: message.RoleUser, Content: "Generate a tree"}}, nil, nil, CompleteStreamOptions{NativeTools: policy})
			if !IsNativeToolError(err) || receipt == nil || !receipt.OutcomeUnknown || requests.Load() != 1 {
				t.Fatalf("unknown execution retried or lost: requests=%d receipt=%+v err=%v", requests.Load(), receipt, err)
			}
			if len(receipt.Calls) != 1 || receipt.Calls[0].Error != "" || len(receipt.Calls[0].Result) != 0 {
				t.Fatalf("unexpected state became confirmed: %+v", receipt.Calls)
			}
		})
	}
}

func TestOtherHostedToolsRetainIncompleteFailure(t *testing.T) {
	resp := &message.Response{}
	recordResponsesHostedItem(resp, make(map[string]*responsesHostedCallState), json.RawMessage(`{"id":"code-1","type":"code_interpreter_call","status":"incomplete"}`), true)
	if len(resp.Hosted.Calls) != 1 || resp.Hosted.Calls[0].Error == "" {
		t.Fatal("code interpreter lost terminal failure")
	}
}
