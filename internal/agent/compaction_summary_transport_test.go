package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/keakon/chord/internal/analytics"
	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
)

func TestSummarizeCompactionHeadCodexUsesOrdinaryResponses(t *testing.T) {
	wantSummary := validCompactionSummaryForTest("history-1.md")
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/v1/responses" {
			t.Errorf("request = %s %s, want POST /v1/responses", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-token" || r.Header.Get("originator") == "" {
			t.Errorf("missing Codex request identity or authorization")
		}
		if strings.Contains(r.Header.Get("x-codex-beta-features"), "remote_compaction_v2") {
			t.Error("request advertises remote compaction")
		}
		var body struct {
			Instructions string `json:"instructions"`
			Input        []struct {
				Type string `json:"type"`
			} `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if body.Instructions != compactionSystemPrompt || len(body.Input) == 0 {
			t.Errorf("request lacks compaction instructions or input: %+v", body)
		}
		for _, item := range body.Input {
			if item.Type == "compaction_trigger" {
				t.Error("request contains a remote compaction trigger")
			}
		}
		payload := map[string]any{
			"type": "response.completed",
			"response": map[string]any{
				"id": "response-1", "status": "completed",
				"output": []any{map[string]any{
					"type": "message", "role": "assistant",
					"content": []any{map[string]any{"type": "output_text", "text": wantSummary}},
				}},
				"usage": map[string]int{"input_tokens": 100, "output_tokens": 50},
			},
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: ")
		if err := json.NewEncoder(w).Encode(payload); err != nil {
			t.Errorf("encode response: %v", err)
		}
		_, _ = fmt.Fprint(w, "\ndata: [DONE]\n\n")
	}))
	defer server.Close()

	provider := llm.NewProviderConfig("sample", config.ProviderConfig{
		Type: config.ProviderTypeResponses, Preset: config.ProviderPresetCodex,
		APIURL: server.URL + "/v1/responses", ResponsesWebsocket: new(false),
		Models: map[string]config.ModelConfig{
			"test-model": {Limit: config.ModelLimit{Context: 16384, Output: 2048}},
		},
	}, []string{"test-token"})
	provider.SetOAuthRefresher(server.URL, "test-client", "", "", nil, nil, map[string]llm.OAuthKeySetup{
		"test-token": {Expires: time.Now().Add(time.Hour).UnixMilli(), AccountID: "test-account"},
	}, "")
	impl, err := llm.NewResponsesProviderWithClient(provider, server.Client(), "")
	if err != nil {
		t.Fatalf("create Responses provider: %v", err)
	}
	a := newTestMainAgent(t, t.TempDir())
	a.llmClient = llm.NewClient(provider, impl, "test-model", 2048, "")
	var usage []analytics.UsageEvent
	a.SetUsageEventSink(func(event analytics.UsageEvent) { usage = append(usage, event) })
	summary, modelRef, err := summarizeCompactionHeadForTest(a, []message.Message{
		{Role: message.RoleUser, Content: "Continue the current task."},
		{Role: message.RoleAssistant, Content: "I will inspect the implementation."},
	}, "history-1.md")
	if err != nil {
		t.Fatalf("summarizeCompactionHead: %v", err)
	}
	if summary != wantSummary || modelRef != "sample/test-model" || requests.Load() != 1 {
		t.Fatalf("summary/model/requests = %q / %q / %d", summary, modelRef, requests.Load())
	}
	if len(usage) != 1 {
		t.Fatalf("usage records = %d, want 1", len(usage))
	}
	if event := usage[0]; event.Purpose != "compaction" || event.RunningModelRef != modelRef || event.UsageRaw.InputTokens != 100 || event.UsageRaw.OutputTokens != 50 {
		t.Fatalf("compaction usage = %+v", event)
	}
}

type compactionPoolTraceProvider struct {
	countingCompactionProvider
	order *[]string
}

func (p *compactionPoolTraceProvider) CompleteStream(ctx context.Context, key, model, prompt string, messages []message.Message, tools []message.ToolDefinition, maxTokens int, tuning llm.RequestTuning, cb llm.StreamCallback) (*message.Response, error) {
	*p.order = append(*p.order, model+":"+key)
	return p.countingCompactionProvider.CompleteStream(ctx, key, model, prompt, messages, tools, maxTokens, tuning, cb)
}

func TestSummarizeCompactionHeadPreservesPoolCursorAndWalksWholePool(t *testing.T) {
	for _, allFail := range []bool{false, true} {
		t.Run(fmt.Sprintf("all_fail=%t", allFail), func(t *testing.T) {
			var order []string
			pool := make([]llm.FallbackModel, 3)
			providers := make([]*compactionPoolTraceProvider, len(pool))
			failure := &llm.APIError{StatusCode: http.StatusInternalServerError, Message: "temporary failure"}
			response := &message.Response{Content: validCompactionSummaryForTest("history-1.md"), StopReason: "stop"}
			for i := range pool {
				model := fmt.Sprintf("model-%d", i)
				cfg := config.ProviderConfig{
					Type: "stub", KeyOrder: config.KeyOrderSequential,
					Models: map[string]config.ModelConfig{model: {Limit: config.ModelLimit{Context: 16384, Output: 2048}}},
				}
				if i == 1 {
					cfg.Preset = config.ProviderPresetCodex
				}
				provider := llm.NewProviderConfig(fmt.Sprintf("provider-%d", i), cfg, []string{"key-1", "key-2"})
				providers[i] = &compactionPoolTraceProvider{err: failure, order: &order}
				pool[i] = llm.FallbackModel{ProviderConfig: provider, ProviderImpl: providers[i], ModelID: model, MaxTokens: 2048}
			}
			client := newAuxClientFromPool(pool, 0, 0, "")
			client.SetStreamRetryRounds(1)
			providers[1].err = nil
			providers[1].response = response
			if _, err := client.CompleteStream(t.Context(), []message.Message{{Role: message.RoleUser, Content: "Inspect the project."}}, nil, nil); err != nil {
				t.Fatalf("establish cursor through fallback: %v", err)
			}
			if _, cursor := client.ModelPoolSnapshot(); cursor != 1 {
				t.Fatalf("cursor = %d, want 1", cursor)
			}
			order = nil
			for i := range pool {
				providers[i].err = failure
				for _, key := range []string{"key-1", "key-2"} {
					pool[i].ProviderConfig.MarkKeySuccess(key)
				}
			}
			if !allFail {
				providers[0].err = nil
				providers[0].response = response
			}
			a := newTestMainAgent(t, t.TempDir())
			a.llmClient = client
			summary, modelRef, err := summarizeCompactionHeadForTest(a, []message.Message{{Role: message.RoleUser, Content: "Continue the current task."}}, "history-1.md")
			// The priming call left model-0 pinned to key-2; compaction preserves
			// both that key cursor and the main client's nonzero model cursor.
			wantOrder := []string{"model-1:key-1", "model-1:key-2", "model-2:key-1", "model-2:key-2", "model-0:key-2"}
			if allFail {
				wantOrder = append(wantOrder, "model-0:key-1")
				if err == nil || summary != "" {
					t.Fatalf("whole-pool failure = %q / %v, want no summary and an error", summary, err)
				}
			} else if err != nil || summary != response.Content || modelRef != "provider-0/model-0" {
				t.Fatalf("wrapped fallback = %q / %q / %v", summary, modelRef, err)
			}
			if !slices.Equal(order, wantOrder) {
				t.Fatalf("call order = %v, want %v", order, wantOrder)
			}
			if _, cursor := client.ModelPoolSnapshot(); cursor != 1 {
				t.Fatalf("compaction changed main client cursor to %d, want 1", cursor)
			}
		})
	}
}
