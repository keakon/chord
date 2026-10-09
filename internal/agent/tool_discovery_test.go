package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/tools"
)

func TestToolDiscoveryPreservesLoadedToolsAcrossProviderFallback(t *testing.T) {
	for _, worker := range []bool{false, true} {
		for _, rejectPrimary := range []bool{false, true} {
			t.Run(fmt.Sprintf("worker=%t/fallback=%t", worker, rejectPrimary), func(t *testing.T) {
				var primaryCalls, fallbackCalls atomic.Int32
				var toolMu sync.Mutex
				var firstTools string
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var body struct {
						Model string            `json:"model"`
						Tools []json.RawMessage `json:"tools"`
					}
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					rawTools, _ := json.Marshal(body.Tools)
					toolMu.Lock()
					if firstTools == "" {
						firstTools = string(rawTools)
					} else if firstTools != string(rawTools) {
						t.Error("fallback changed tool definitions")
					}
					toolMu.Unlock()
					found, foundDeferred := false, false
					for _, rawTool := range body.Tools {
						var tool struct {
							Function struct {
								Name string `json:"name"`
							} `json:"function"`
						}
						if err := json.Unmarshal(rawTool, &tool); err != nil {
							t.Error(err)
						}
						found = found || tool.Function.Name == "mcp_sample_eager"
						foundDeferred = foundDeferred || tool.Function.Name == "mcp_sample_deferred"
					}
					if !found || !foundDeferred {
						t.Error("eager or loaded deferred tool was removed")
					}
					if body.Model == "test-model" {
						primaryCalls.Add(1)
						if rejectPrimary {
							w.Header().Set("Content-Type", "application/json")
							w.WriteHeader(http.StatusBadRequest)
							_, _ = io.WriteString(w, `{"error":{"type":"invalid_request_error","code":"context_length_exceeded","message":"Input is too long"}}`)
							return
						}
					} else if body.Model == "fallback-model" {
						fallbackCalls.Add(1)
					} else {
						t.Errorf("unexpected model %q", body.Model)
					}
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Complete\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
				}))
				defer srv.Close()
				cfg := llm.NewProviderConfig("sample", config.ProviderConfig{Type: config.ProviderTypeChatCompletions, APIURL: srv.URL + "/v1/chat/completions", Models: map[string]config.ModelConfig{
					"test-model":     {Limit: config.ModelLimit{Context: 4000, Output: 512}},
					"fallback-model": {Limit: config.ModelLimit{Context: 32000, Output: 512}},
				}}, []string{"key"})
				impl, err := llm.NewOpenAIProvider(cfg, "")
				if err != nil {
					t.Fatal(err)
				}
				client := llm.NewClient(cfg, impl, "test-model", 512, "")
				t.Cleanup(client.Close)
				client.SetFallbackModels([]llm.FallbackModel{{ProviderConfig: cfg, ProviderImpl: impl, ModelID: "fallback-model", ContextLimit: 32000, MaxTokens: 512}})
				registry := tools.NewRegistry()
				registry.Register(anchoredManualMCPTool{name: "mcp_sample_eager", description: strings.Repeat("Schema documentation ", 600)})
				deferred := deferredTestTool{name: "mcp_sample_deferred", description: "Sample lookup"}
				registry.Register(deferred)
				def := llmToolDefinitionsFromVisibleTools([]tools.Tool{deferred})[0]
				loaded := discoveryHistory(t, "search-1", message.ToolDiscoveryResult{Tools: []message.ToolDiscoveryEntry{{Name: deferred.Name(), Status: message.ToolDiscoveryLoaded, Definition: &def}}})
				rules := permission.Ruleset{{Permission: "*", Pattern: "*", Action: permission.ActionAllow}}
				messages := append(append([]message.Message{{Role: message.RoleUser, Content: "Load sample tools"}}, loaded...), message.Message{Role: message.RoleUser, Content: "Inspect sample records"})
				if worker {
					_, sub := newMixedBatchTestSubAgent(t)
					sub.llmClient.Close()
					sub.llmClient = client
					sub.tools = registry
					sub.setRuleset(rules)
					for _, msg := range messages {
						sub.ctxMgr.Append(msg)
					}
					sub.asyncCallLLMWithFlightMarked(sub.turn, messages)
					select {
					case result := <-sub.llmCh:
						err = result.err
					case <-time.After(5 * time.Second):
						t.Fatal("request did not settle")
					}
					sub.llmWG.Wait()
				} else {
					a := newReadyTestMainAgent(t)
					a.tools = registry
					a.ruleset = rules
					for _, msg := range messages {
						a.ctxMgr.Append(msg)
					}
					a.swapLLMClientWithRef(client, "test-model", 4000, "sample/test-model")
					_, err = a.callLLMForRequest(t.Context(), messages, 0)
				}
				wantFallback := int32(0)
				if rejectPrimary {
					wantFallback = 1
				}
				if err != nil || primaryCalls.Load() != 1 || fallbackCalls.Load() != wantFallback {
					t.Fatalf("primary=%d fallback=%d err=%v", primaryCalls.Load(), fallbackCalls.Load(), err)
				}
			})
		}
	}
}

type deferredTestTool struct {
	anchoredManualMCPTool
	status string
}

func (deferredTestTool) IsDeferred() bool { return true }
func (t deferredTestTool) IsAvailable() bool {
	return t.status == "" || t.status == message.ToolDiscoveryLoaded
}
func (t deferredTestTool) DiscoveryStatus() string {
	if t.status == "" {
		return message.ToolDiscoveryLoaded
	}
	return t.status
}

func discoveryHistory(t testing.TB, id string, result message.ToolDiscoveryResult) []message.Message {
	t.Helper()
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	return []message.Message{
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: id, Name: tools.NameToolSearch, Args: json.RawMessage(`{"query":"sample"}`)}}},
		{Role: message.RoleTool, ToolCallID: id, Content: string(raw)},
	}
}

func TestToolDiscoveryLoadsOnlyAgentHistoryAndVisibleSchemas(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.tools = tools.NewRegistry()
	a.ruleset = permission.Ruleset{{Permission: "*", Pattern: "*", Action: permission.ActionAllow}}
	tool := deferredTestTool{name: "mcp_sample_lookup", description: "Search sample records"}
	a.tools.Register(tool)
	a.tools.Register(tools.NewToolSearchTool(a))
	if hasToolDefinition(llmToolDefinitionsFromVisibleTools(a.mainVisibleLLMTools()), tool.Name()) {
		t.Fatal("deferred schema exposed before discovery")
	}
	result, err := a.SearchTools(t.Context(), "records", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Tools) != 1 || result.Tools[0].Status != message.ToolDiscoveryLoaded {
		t.Fatalf("result=%+v", result)
	}
	history := discoveryHistory(t, "search-1", result)
	for _, msg := range history {
		a.ctxMgr.Append(msg)
	}
	if !hasToolDefinition(llmToolDefinitionsFromVisibleTools(a.mainVisibleLLMTools()), tool.Name()) {
		t.Fatal("successful discovery not mounted")
	}
	if got := projectDiscoveredTools([]tools.Tool{tool}, nil); len(got) != 0 {
		t.Fatal("load leaked to another agent/session")
	}
	projected := message.ProjectToolDiscoveryHistory(history)
	if strings.Contains(projected[1].Content, "input_schema") || !strings.Contains(history[1].Content, "input_schema") {
		t.Fatal("schema projection changed canonical history or retained duplicated schema")
	}
	tool.description = "Updated records schema"
	a.tools.Register(tool)
	defs := llmToolDefinitionsFromVisibleTools(a.mainVisibleLLMTools())
	if defs[0].Description != tool.description {
		t.Fatalf("schema update not reflected: %+v", defs)
	}
	tool.status = tools.DiscoveryDisabled
	a.tools.Register(tool)
	if hasToolDefinition(llmToolDefinitionsFromVisibleTools(a.mainVisibleLLMTools()), tool.Name()) {
		t.Fatal("disabled tool remains callable")
	}
	a.ctxMgr.RestoreMessages(history)
	tool.status = ""
	a.tools.Register(tool)
	if !hasToolDefinition(llmToolDefinitionsFromVisibleTools(a.mainVisibleLLMTools()), tool.Name()) {
		t.Fatal("restore lost load")
	}
	a.ctxMgr.RestoreMessages([]message.Message{{Role: message.RoleUser, Content: "Continue"}})
	if hasToolDefinition(llmToolDefinitionsFromVisibleTools(a.mainVisibleLLMTools()), tool.Name()) {
		t.Fatal("compacted load remained without durable evidence")
	}
}

func TestToolDiscoveryPermissionsAvailabilityAndBudgets(t *testing.T) {
	registry := tools.NewRegistry()
	var visible []tools.Tool
	for _, tc := range []struct{ name, status string }{{"mcp_sample_lookup", ""}, {"mcp_sample_disabled", tools.DiscoveryDisabled}, {"mcp_sample_failed", tools.DiscoveryUnavailable}, {"mcp_sample_private", ""}} {
		tool := deferredTestTool{name: tc.name, description: "Find records", status: tc.status}
		registry.Register(tool)
		if tool.IsAvailable() && !strings.Contains(tc.name, "private") {
			visible = append(visible, tool)
		}
	}
	rules := permission.Ruleset{{Permission: "*", Pattern: "*", Action: permission.ActionAllow}, {Permission: "mcp_sample_private", Pattern: "*", Action: permission.ActionDeny}}
	names := []string{"mcp_sample_lookup", "mcp_sample_disabled", "mcp_sample_failed", "mcp_sample_private", "missing"}
	result, err := searchDeferredTools(t.Context(), registry, visible, rules, "", names)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{message.ToolDiscoveryLoaded, tools.DiscoveryDisabled, tools.DiscoveryUnavailable, tools.DiscoveryDenied, tools.DiscoveryNotFound}
	for i, entry := range result.Tools {
		if entry.Status != want[i] {
			t.Fatalf("entry=%+v want=%s", entry, want[i])
		}
	}
	oversized := deferredTestTool{name: names[0], description: strings.Repeat("Sample definition ", 1000)}
	registry.Register(oversized)
	visible[0] = oversized
	result, err = searchDeferredTools(t.Context(), registry, visible, rules, "", names[:1])
	if err != nil {
		t.Fatal(err)
	}
	if result.Tools[0].Status != tools.DiscoveryBudgetExceeded {
		t.Fatalf("result=%+v", result)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := searchDeferredTools(ctx, registry, visible, rules, "records", nil); err == nil {
		t.Fatal("cancelled discovery succeeded")
	}
}

func TestToolDiscoveryRetainsLoadedDefinitionsUntilCompaction(t *testing.T) {
	registry := tools.NewRegistry()
	eager := dummyTool{name: "eager"}
	registry.Register(eager)
	visible := []tools.Tool{eager}
	var history []message.Message
	for i := range 40 {
		tool := deferredTestTool{name: fmt.Sprintf("mcp_sample_%02d", i), description: strings.Repeat("sample ", 250)}
		registry.Register(tool)
		visible = append(visible, tool)
		result, err := searchDeferredTools(t.Context(), registry, visible, nil, "", []string{tool.Name()})
		if err != nil || len(result.Tools) != 1 || result.Tools[0].Status != message.ToolDiscoveryLoaded {
			t.Fatalf("result=%+v err=%v", result, err)
		}
		history = append(history, discoveryHistory(t, fmt.Sprintf("search-%d", i), result)...)
		projected := projectDiscoveredTools(visible, message.ToolDiscoveryHistory(history))
		if len(projected) != len(visible) {
			t.Fatal("previously loaded schema was evicted")
		}
	}
	if toolSurfaceTokens(projectDiscoveredTools(visible, message.ToolDiscoveryHistory(history))) <= 16384 {
		t.Fatal("fixture must include a large loaded tool surface")
	}
	raw, err := json.Marshal(history)
	if err != nil {
		t.Fatal(err)
	}
	var restored []message.Message
	if err := json.Unmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	if len(projectDiscoveredTools(visible, message.ToolDiscoveryHistory(restored))) != len(visible) {
		t.Fatal("restore dropped loaded definitions")
	}
	visible[1] = deferredTestTool{name: "mcp_sample_00", description: strings.Repeat("Updated schema details ", 1000)}
	if len(projectDiscoveredTools(visible, message.ToolDiscoveryHistory(restored))) != len(visible) {
		t.Fatal("schema growth evicted loaded definitions")
	}
	if got := projectDiscoveredTools(visible, nil); len(got) != 1 || got[0].Name() != eager.Name() {
		t.Fatal("compaction did not release loaded definitions")
	}
}

func TestToolDiscoveryFixedResultLimitIsIndependentOfEagerSize(t *testing.T) {
	eager := anchoredManualMCPTool{name: "eager", description: strings.Repeat("sample ", 10000)}
	deferred := deferredTestTool{name: "mcp_sample_lookup", description: "Sample records"}
	registry := tools.NewRegistry()
	registry.Register(eager)
	registry.Register(deferred)
	visible := []tools.Tool{eager, deferred}
	result, err := searchDeferredTools(t.Context(), registry, visible, nil, "", []string{deferred.Name()})
	if err != nil || len(result.Tools) != 1 || result.Tools[0].Status != message.ToolDiscoveryLoaded {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestToolDiscoveryDefinitionsStayStableWithoutNewLoads(t *testing.T) {
	eager := tools.ShellTool{}
	deferred := deferredTestTool{name: "mcp_sample_lookup", description: "Sample records"}
	visible := []tools.Tool{eager, deferred}
	def := llmToolDefinitionsFromVisibleTools([]tools.Tool{deferred})[0]
	history := discoveryHistory(t, "search-1", message.ToolDiscoveryResult{Tools: []message.ToolDiscoveryEntry{{Name: deferred.Name(), Status: message.ToolDiscoveryLoaded, Definition: &def}}})
	before, err := json.Marshal(llmToolDefinitionsFromVisibleTools(projectDiscoveredTools(visible, message.ToolDiscoveryHistory(history))))
	if err != nil {
		t.Fatal(err)
	}
	history = append(history, message.Message{Role: message.RoleUser, Content: "Continue inspecting records"})
	history = append(history, discoveryHistory(t, "search-2", message.ToolDiscoveryResult{Tools: []message.ToolDiscoveryEntry{{Name: "mcp_sample_other", Status: tools.DiscoveryBudgetExceeded}}})...)
	after, err := json.Marshal(llmToolDefinitionsFromVisibleTools(projectDiscoveredTools(visible, message.ToolDiscoveryHistory(history))))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("unchanged loaded set changed the tool prefix")
	}
}
