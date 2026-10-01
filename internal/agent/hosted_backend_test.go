package agent

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

// sampleHostedTool is a config-only catalog entry used by the tests; it has no
// built-in spec, so it also exercises the configuration-driven path.
const sampleHostedTool = "sample_execution"

// hostedScriptCall records one sub-request that reached a scripted provider,
// so tests can assert the hosted declaration was lowered into the wire call
// instead of being consumed by the retry layer.
type hostedScriptCall struct {
	systemPrompt string
	messages     []message.Message
	tools        []message.ToolDefinition
	tuning       llm.RequestTuning
}

// hostedScriptProvider is a scripted llm.Provider for hosted backend tests.
// respond receives the zero-based call index and the call context.
type hostedScriptProvider struct {
	mu      sync.Mutex
	calls   []hostedScriptCall
	respond func(index int, ctx context.Context) (*message.Response, error)
}

func (p *hostedScriptProvider) CompleteStream(
	ctx context.Context,
	_ string,
	_ string,
	systemPrompt string,
	messages []message.Message,
	toolDefs []message.ToolDefinition,
	_ int,
	tuning llm.RequestTuning,
	_ llm.StreamCallback,
) (*message.Response, error) {
	p.mu.Lock()
	p.calls = append(p.calls, hostedScriptCall{
		systemPrompt: systemPrompt,
		messages:     append([]message.Message(nil), messages...),
		tools:        append([]message.ToolDefinition(nil), toolDefs...),
		tuning:       tuning,
	})
	index := len(p.calls) - 1
	p.mu.Unlock()
	if p.respond == nil {
		return &message.Response{Content: "ok", StopReason: "stop"}, nil
	}
	return p.respond(index, ctx)
}

func (p *hostedScriptProvider) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.calls)
}

func (p *hostedScriptProvider) callsSnapshot() []hostedScriptCall {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]hostedScriptCall(nil), p.calls...)
}

type hostedTestTargetOpts struct {
	typ            string
	modelID        string
	providerHosted []string
	modelHosted    []string
}

func newHostedTestTarget(name string, opts hostedTestTargetOpts, impl llm.Provider) llm.FallbackModel {
	pc := config.ProviderConfig{
		Type:   opts.typ,
		APIURL: "https://example.invalid/v1",
		Models: map[string]config.ModelConfig{
			opts.modelID: {Limit: config.ModelLimit{Context: 128000, Output: 4096}},
		},
	}
	if len(opts.providerHosted) > 0 {
		pc.Compat = &config.ProviderCompatConfig{HostedTools: new(opts.providerHosted)}
	}
	if len(opts.modelHosted) > 0 {
		model := pc.Models[opts.modelID]
		model.Compat = &config.ModelCompatConfig{HostedTools: new(opts.modelHosted)}
		pc.Models[opts.modelID] = model
	}
	providerCfg := llm.NewProviderConfig(name, pc, []string{"test-key"})
	return llm.FallbackModel{ProviderConfig: providerCfg, ProviderImpl: impl, ModelID: opts.modelID, MaxTokens: 4096}
}

func setHostedTestPool(a *MainAgent, targets ...llm.FallbackModel) *llm.Client {
	first := targets[0]
	client := llm.NewClient(first.ProviderConfig, first.ProviderImpl, first.ModelID, first.MaxTokens, "")
	client.SetModelPool(targets, 0)
	a.llmMu.Lock()
	a.llmClient = client
	a.forgetHostedCaller("")
	a.llmMu.Unlock()
	return client
}

// hostedWebSearchResponse is a successful sub-request: one completed hosted
// call with a result payload.
func hostedWebSearchResponse() *message.Response {
	return &message.Response{
		Content:    "A short factual summary.",
		StopReason: "stop",
		Hosted: &message.HostedObservation{
			Summary: "A short factual summary.",
			Calls: []message.HostedCall{{
				ID:     "srv_1",
				Name:   tools.NameWebSearch,
				Kind:   "server_tool_use",
				Status: "completed",
				Input:  json.RawMessage(`{"query":"golang release notes"}`),
				Result: json.RawMessage(`[{"url":"https://example.invalid/a","title":"Example A"}]`),
			}},
		},
	}
}

// sampleHostedCatalog resolves a catalog that contains only the config-only
// test entry.
func sampleHostedCatalog() map[string]tools.HostedToolSpec {
	return tools.ResolveHostedToolCatalog(map[string]config.HostedToolConfig{
		sampleHostedTool: {
			Description: "Run code in a sandbox.",
			Declarations: map[string]config.HostedToolDeclarationConfig{
				config.ProviderTypeMessages: {
					Tool: map[string]any{
						"type": "sample_execution_20250101",
						"name": sampleHostedTool,
						"code": map[string]any{"$arg": "code"},
					},
					Force: map[string]any{"type": "tool", "name": sampleHostedTool},
				},
			},
		},
	})
}

func decodeHostedJSON(t *testing.T, name string, raw json.RawMessage) map[string]any {
	t.Helper()
	if len(raw) == 0 {
		t.Fatalf("%s is empty", name)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("%s = %s: %v", name, raw, err)
	}
	return out
}

func TestHostedBackendCapabilityGate(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	backend := newTestHostedBackend(t, a, tools.ResolveHostedToolCatalog(nil))
	ctx := context.Background()

	// A model-level list replaces the provider default; this override no
	// longer names web_search, so the target stays out.
	modelOverridden := newHostedTestTarget("model-overridden", hostedTestTargetOpts{
		typ:            config.ProviderTypeMessages,
		modelID:        "model-1",
		providerHosted: []string{tools.NameWebSearch},
		modelHosted:    []string{sampleHostedTool},
	}, &hostedScriptProvider{})
	// An enabled model on a wire family without a hosted declaration for this
	// tool stays out.
	chatEnabled := newHostedTestTarget("chat-enabled", hostedTestTargetOpts{
		typ:            config.ProviderTypeChatCompletions,
		modelID:        "model-2",
		providerHosted: []string{tools.NameWebSearch},
	}, &hostedScriptProvider{})

	setHostedTestPool(a, modelOverridden, chatEnabled)
	if backend.Available(tools.NameWebSearch) {
		t.Fatal("Available() = true, want false when no target can carry the hosted declaration")
	}
	if _, err := backend.Run(ctx, tools.NameWebSearch, map[string]any{"query": "test query"}); err == nil {
		t.Fatal("Run() succeeded with no capable target")
	} else if !strings.Contains(err.Error(), "compat.hosted_tools") {
		t.Fatalf("unavailable error should point at the capability switch, got: %v", err)
	}

	// Enabling the Messages target makes the tool available without touching
	// the main pool cursor.
	messagesEnabled := newHostedTestTarget("messages-enabled", hostedTestTargetOpts{
		typ:            config.ProviderTypeMessages,
		modelID:        "model-3",
		providerHosted: []string{tools.NameWebSearch},
	}, &hostedScriptProvider{respond: func(int, context.Context) (*message.Response, error) {
		return hostedWebSearchResponse(), nil
	}})
	setHostedTestPool(a, messagesEnabled)
	if !backend.Available(tools.NameWebSearch) {
		t.Fatal("Available() = false, want true for an enabled Messages target")
	}
	if _, err := backend.Run(ctx, tools.NameWebSearch, map[string]any{"query": "test query"}); err != nil {
		t.Fatalf("Run() on an enabled target: %v", err)
	}
}

func TestHostedBackendFallsThroughAndLowersHostedRequest(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	// Transport success without any hosted block: the endpoint ignored the
	// declaration, so the backend must move on to the next target.
	first := &hostedScriptProvider{respond: func(int, context.Context) (*message.Response, error) {
		return &message.Response{Content: "I could not search.", StopReason: "stop"}, nil
	}}
	second := &hostedScriptProvider{respond: func(int, context.Context) (*message.Response, error) {
		return hostedWebSearchResponse(), nil
	}}
	firstTarget := newHostedTestTarget("first", hostedTestTargetOpts{
		typ:            config.ProviderTypeMessages,
		modelID:        "model-1",
		providerHosted: []string{tools.NameWebSearch},
	}, first)
	secondTarget := newHostedTestTarget("second", hostedTestTargetOpts{
		typ:            config.ProviderTypeResponses,
		modelID:        "model-2",
		providerHosted: []string{tools.NameWebSearch},
	}, second)
	mainClient := setHostedTestPool(a, firstTarget, secondTarget)
	cursorBefore := mainClient.PrimaryModelRef()

	backend := newTestHostedBackend(t, a, tools.ResolveHostedToolCatalog(nil))
	obs, err := backend.Run(context.Background(), tools.NameWebSearch, map[string]any{
		"query":           "golang release notes",
		"allowed_domains": []string{"go.dev"},
	})
	if err != nil {
		t.Fatalf("Run() = %v", err)
	}
	if obs.Summary != "A short factual summary." || !obs.HasCompleteResult() || len(obs.Calls) != 1 {
		t.Fatalf("Run() observation = %+v, want the second target's completed call", obs)
	}

	if first.callCount() != 1 {
		t.Fatalf("first target calls = %d, want 1", first.callCount())
	}
	if second.callCount() != 1 {
		t.Fatalf("second target calls = %d, want 1", second.callCount())
	}

	firstCall := first.callsSnapshot()[0]
	hosted := firstCall.tuning.HostedTool
	if hosted == nil {
		t.Fatal("the hosted declaration never reached the provider call")
	}
	if hosted.Name != tools.NameWebSearch {
		t.Fatalf("hosted request name = %q", hosted.Name)
	}
	decl := decodeHostedJSON(t, "Messages declaration", hosted.Declaration)
	if decl["type"] != "web_search_20250305" || decl["max_uses"] != float64(8) {
		t.Fatalf("Messages declaration = %v, want the built-in version and max_uses", decl)
	}
	if domains, ok := decl["allowed_domains"].([]any); !ok || len(domains) != 1 || domains[0] != "go.dev" {
		t.Fatalf("Messages declaration domains = %v, want the resolved allowed_domains", decl["allowed_domains"])
	}
	if _, ok := decl["blocked_domains"]; ok {
		t.Fatalf("Messages declaration kept an empty placeholder: %v", decl)
	}
	force := decodeHostedJSON(t, "Messages tool_choice", hosted.Force)
	if force["type"] != "tool" || force["name"] != tools.NameWebSearch {
		t.Fatalf("Messages tool_choice = %v, want the forced declaration", force)
	}
	if len(hosted.Include) != 0 {
		t.Fatalf("Messages include = %v, want none", hosted.Include)
	}
	if firstCall.systemPrompt != hostedToolSystemPrompt {
		t.Fatalf("system prompt = %q, want the minimal hosted prompt", firstCall.systemPrompt)
	}
	if len(firstCall.messages) != 1 || firstCall.messages[0].Role != message.RoleUser ||
		firstCall.messages[0].Content != "Perform a web search for the query: golang release notes" {
		t.Fatalf("sub-request messages = %+v, want the rendered prompt as the single user message", firstCall.messages)
	}
	if len(firstCall.tools) != 0 {
		t.Fatalf("sub-request declared %d client tools, want none", len(firstCall.tools))
	}

	responsesCall := second.callsSnapshot()[0]
	responsesHosted := responsesCall.tuning.HostedTool
	if responsesHosted == nil {
		t.Fatal("the Responses target got no hosted declaration")
	}
	responsesDecl := decodeHostedJSON(t, "Responses declaration", responsesHosted.Declaration)
	if responsesDecl["type"] != tools.NameWebSearch {
		t.Fatalf("Responses declaration = %v, want the web_search type", responsesDecl)
	}
	filters, _ := responsesDecl["filters"].(map[string]any)
	if domains, ok := filters["allowed_domains"].([]any); !ok || len(domains) != 1 || domains[0] != "go.dev" {
		t.Fatalf("Responses declaration filters = %v, want the resolved allowed_domains", responsesDecl["filters"])
	}
	if len(responsesHosted.Include) != 1 || responsesHosted.Include[0] != "web_search_call.action.sources" {
		t.Fatalf("Responses include = %v, want the built-in sources selector", responsesHosted.Include)
	}
	var responsesForce any
	if err := json.Unmarshal(responsesHosted.Force, &responsesForce); err != nil || responsesForce != "required" {
		t.Fatalf("Responses tool_choice = %s (err %v), want \"required\"", responsesHosted.Force, err)
	}

	if got := mainClient.PrimaryModelRef(); got != cursorBefore {
		t.Fatalf("main pool cursor moved from %q to %q during a sub-request", cursorBefore, got)
	}
}

func TestHostedBackendConfigOnlyCatalogEntry(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	catalog := sampleHostedCatalog()
	backend := newTestHostedBackend(t, a, catalog)
	impl := &hostedScriptProvider{respond: func(int, context.Context) (*message.Response, error) {
		return &message.Response{
			Content:    "Ran the sample code.",
			StopReason: "stop",
			Hosted: &message.HostedObservation{
				Summary: "Ran the sample code.",
				Calls: []message.HostedCall{{
					ID:     "srv_2",
					Name:   sampleHostedTool,
					Kind:   "server_tool_use",
					Status: "completed",
					Result: json.RawMessage(`{"type":"sample_execution_result","stdout":"ok"}`),
				}},
			},
		}, nil
	}}
	setHostedTestPool(a, newHostedTestTarget("messages", hostedTestTargetOpts{
		typ:            config.ProviderTypeMessages,
		modelID:        "model-1",
		providerHosted: []string{sampleHostedTool},
	}, impl))

	if backend.Available(tools.NameWebSearch) {
		t.Fatal("the built-in web_search must stay unavailable when only another tool is enabled")
	}
	if !backend.Available(sampleHostedTool) {
		t.Fatal("the config-only entry must be available once enabled")
	}
	obs, err := backend.Run(context.Background(), sampleHostedTool, map[string]any{"code": "print(1)"})
	if err != nil {
		t.Fatalf("Run() = %v", err)
	}
	if !obs.HasCompleteResult() || obs.Calls[0].Name != sampleHostedTool {
		t.Fatalf("Run() observation = %+v, want the config-only call", obs)
	}
	call := impl.callsSnapshot()[0]
	hosted := call.tuning.HostedTool
	if hosted == nil || hosted.Name != sampleHostedTool {
		t.Fatalf("hosted request = %+v, want the config-only entry", hosted)
	}
	decl := decodeHostedJSON(t, "sample declaration", hosted.Declaration)
	if decl["type"] != "sample_execution_20250101" || decl["code"] != "print(1)" {
		t.Fatalf("declaration = %v, want the resolved placeholder", decl)
	}
	if len(hosted.Force) == 0 {
		t.Fatal("the config-only entry's forced tool_choice was dropped")
	}
	if call.messages[0].Content != `{"code":"print(1)"}` {
		t.Fatalf("sub-request message = %q, want the serialized arguments without a prompt template", call.messages[0].Content)
	}
}

func TestHostedBackendInputLevelErrorStopsWalk(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	first := &hostedScriptProvider{respond: func(int, context.Context) (*message.Response, error) {
		return nil, &llm.APIError{StatusCode: 400, Code: "query_too_long", Message: "query too long"}
	}}
	second := &hostedScriptProvider{respond: func(int, context.Context) (*message.Response, error) {
		return hostedWebSearchResponse(), nil
	}}
	setHostedTestPool(a,
		newHostedTestTarget("first", hostedTestTargetOpts{
			typ:            config.ProviderTypeMessages,
			modelID:        "model-1",
			providerHosted: []string{tools.NameWebSearch},
		}, first),
		newHostedTestTarget("second", hostedTestTargetOpts{
			typ:            config.ProviderTypeResponses,
			modelID:        "model-2",
			providerHosted: []string{tools.NameWebSearch},
		}, second),
	)

	_, err := newTestHostedBackend(t, a, tools.ResolveHostedToolCatalog(nil)).Run(
		context.Background(), tools.NameWebSearch, map[string]any{"query": "test query"})
	if err == nil {
		t.Fatal("Run() succeeded, want the input-level rejection")
	}
	if !strings.Contains(err.Error(), "rejected before running") {
		t.Fatalf("error = %v, want the input-level rejection path", err)
	}
	if second.callCount() != 0 {
		t.Fatalf("second target calls = %d, want 0 after an input-level rejection", second.callCount())
	}
}

func TestHostedBackendDegradesToHintOnlyAfterRejection(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.governor = newResourceGovernor(config.OrchestrationConfig{MaxActiveLLMRequests: 1})
	impl := &hostedScriptProvider{respond: func(index int, _ context.Context) (*message.Response, error) {
		if got := a.governor.snapshot(); got.LLMActive != 1 || got.ModelActive["first/model-1"] != 1 {
			t.Errorf("attempt %d capacity = %+v, want one slot for first/model-1", index, got)
		}
		if index == 0 {
			// A 400 invalid_request_error is also how endpoints report a
			// rejected hosted declaration or tool_choice.
			return nil, &llm.APIError{StatusCode: 400, Code: "invalid_request_error", Message: "tool_choice is not supported"}
		}
		return hostedWebSearchResponse(), nil
	}}
	setHostedTestPool(a, newHostedTestTarget("first", hostedTestTargetOpts{
		typ:            config.ProviderTypeMessages,
		modelID:        "model-1",
		providerHosted: []string{tools.NameWebSearch},
	}, impl))

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	obs, err := newTestHostedBackend(t, a, tools.ResolveHostedToolCatalog(nil)).Run(
		ctx, tools.NameWebSearch, map[string]any{"query": "test query"})
	if err != nil {
		t.Fatalf("Run() = %v", err)
	}
	if obs.Summary != "A short factual summary." {
		t.Fatalf("observation = %+v, want the hint-only retry's summary", obs)
	}
	if impl.callCount() != 2 {
		t.Fatalf("provider calls = %d, want the forced attempt plus the hint-only retry", impl.callCount())
	}
	if got := a.governor.snapshot(); got.LLMActive != 0 || got.LLMQueued != 0 {
		t.Fatalf("capacity after hint-only retry = %+v", got)
	}
	calls := impl.callsSnapshot()
	if calls[0].tuning.HostedTool == nil || len(calls[0].tuning.HostedTool.Force) == 0 {
		t.Fatalf("first attempt = %+v, want a forced declaration", calls[0].tuning.HostedTool)
	}
	if calls[1].tuning.HostedTool == nil || len(calls[1].tuning.HostedTool.Force) != 0 {
		t.Fatalf("second attempt = %+v, want the hint-only declaration", calls[1].tuning.HostedTool)
	}
	if calls[1].systemPrompt != hostedToolHintSystemPrompt(tools.NameWebSearch) {
		t.Fatalf("hint-only system prompt = %q", calls[1].systemPrompt)
	}
}

func TestHostedBackendUnrelatedRejectionMovesToNextTarget(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	first := &hostedScriptProvider{respond: func(int, context.Context) (*message.Response, error) {
		return nil, &llm.APIError{StatusCode: 400, Code: "invalid_request_error", Message: "web search is not enabled"}
	}}
	second := &hostedScriptProvider{respond: func(int, context.Context) (*message.Response, error) {
		return hostedWebSearchResponse(), nil
	}}
	setHostedTestPool(a,
		newHostedTestTarget("first", hostedTestTargetOpts{
			typ:            config.ProviderTypeMessages,
			modelID:        "model-1",
			providerHosted: []string{tools.NameWebSearch},
		}, first),
		newHostedTestTarget("second", hostedTestTargetOpts{
			typ:            config.ProviderTypeResponses,
			modelID:        "model-2",
			providerHosted: []string{tools.NameWebSearch},
		}, second),
	)

	obs, err := newTestHostedBackend(t, a, tools.ResolveHostedToolCatalog(nil)).Run(
		context.Background(), tools.NameWebSearch, map[string]any{"query": "test query"})
	if err != nil {
		t.Fatalf("Run() = %v", err)
	}
	if !obs.HasCompleteResult() {
		t.Fatalf("observation = %+v, want the second target's result", obs)
	}
	if first.callCount() != 1 {
		t.Fatalf("first target calls = %d, want no hint-only retry for an unrelated rejection", first.callCount())
	}
	if second.callCount() != 1 {
		t.Fatalf("second target calls = %d, want 1", second.callCount())
	}
}

func TestHostedBackendCancellationPropagates(t *testing.T) {
	t.Run("before first target", func(t *testing.T) {
		a := newTestMainAgent(t, t.TempDir())
		impl := &hostedScriptProvider{}
		setHostedTestPool(a, newHostedTestTarget("first", hostedTestTargetOpts{
			typ:            config.ProviderTypeMessages,
			modelID:        "model-1",
			providerHosted: []string{tools.NameWebSearch},
		}, impl))

		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := newTestHostedBackend(t, a, tools.ResolveHostedToolCatalog(nil)).Run(
			ctx, tools.NameWebSearch, map[string]any{"query": "test query"})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run() error = %v, want context.Canceled", err)
		}
		if impl.callCount() != 0 {
			t.Fatalf("provider calls = %d, want 0 for a pre-cancelled run", impl.callCount())
		}
	})

	t.Run("during target", func(t *testing.T) {
		a := newTestMainAgent(t, t.TempDir())
		ctx, cancel := context.WithCancel(context.Background())
		first := &hostedScriptProvider{respond: func(_ int, callCtx context.Context) (*message.Response, error) {
			cancel()
			return nil, callCtx.Err()
		}}
		second := &hostedScriptProvider{respond: func(int, context.Context) (*message.Response, error) {
			return hostedWebSearchResponse(), nil
		}}
		setHostedTestPool(a,
			newHostedTestTarget("first", hostedTestTargetOpts{
				typ:            config.ProviderTypeMessages,
				modelID:        "model-1",
				providerHosted: []string{tools.NameWebSearch},
			}, first),
			newHostedTestTarget("second", hostedTestTargetOpts{
				typ:            config.ProviderTypeResponses,
				modelID:        "model-2",
				providerHosted: []string{tools.NameWebSearch},
			}, second),
		)

		_, err := newTestHostedBackend(t, a, tools.ResolveHostedToolCatalog(nil)).Run(
			ctx, tools.NameWebSearch, map[string]any{"query": "test query"})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run() error = %v, want context.Canceled", err)
		}
		if second.callCount() != 0 {
			t.Fatalf("second target calls = %d, want 0 after cancellation", second.callCount())
		}
	})
}

func TestHostedObservationFromResponse(t *testing.T) {
	tests := []struct {
		name       string
		resp       *message.Response
		wantErr    string
		wantCalls  int
		wantErrors []string
	}{
		{
			name:    "nil response",
			resp:    nil,
			wantErr: "no response",
		},
		{
			name:    "no hosted observation",
			resp:    &message.Response{Content: "text only", StopReason: "stop"},
			wantErr: "no sample_tool call was observed",
		},
		{
			name: "interrupted stream",
			resp: &message.Response{StopReason: "interrupted", Hosted: &message.HostedObservation{
				Calls: []message.HostedCall{{Result: json.RawMessage(`[]`)}},
			}},
			wantErr: "interrupted",
		},
		{
			name: "call without result",
			resp: &message.Response{StopReason: "stop", Hosted: &message.HostedObservation{
				Calls: []message.HostedCall{{ID: "srv_1"}},
			}},
			wantErr: "did not complete",
		},
		{
			name: "only errors",
			resp: &message.Response{StopReason: "stop", Hosted: &message.HostedObservation{
				Calls: []message.HostedCall{{ID: "srv_1", Error: "provider failure"}},
			}},
			wantErr: "returned only errors",
		},
		{
			name: "empty result list is success",
			resp: &message.Response{StopReason: "stop", Hosted: &message.HostedObservation{
				Calls: []message.HostedCall{{ID: "srv_1", Result: json.RawMessage(`[]`)}},
			}},
			wantCalls: 1,
		},
		{
			name: "pending and failed calls annotate a successful run",
			resp: &message.Response{StopReason: "stop", Hosted: &message.HostedObservation{
				Calls: []message.HostedCall{
					{ID: "srv_1", Result: json.RawMessage(`[{"url":"https://example.invalid/a"}]`)},
					{ID: "srv_2", Error: "provider failure"},
					{ID: "srv_3", Status: "in_progress"},
				},
			}},
			wantCalls:  3,
			wantErrors: []string{"provider failure", "sample_tool call did not complete before the stream ended"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			obs, err := hostedObservationFromResponse("sample_tool", tt.resp)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("observation error = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("observation error = %v", err)
			}
			if len(obs.Calls) != tt.wantCalls {
				t.Fatalf("calls = %d, want %d", len(obs.Calls), tt.wantCalls)
			}
			errs := obs.CallErrors()
			if len(errs) != len(tt.wantErrors) {
				t.Fatalf("call errors = %v, want %v", errs, tt.wantErrors)
			}
			for i, want := range tt.wantErrors {
				if errs[i] != want {
					t.Fatalf("call errors[%d] = %q, want %q", i, errs[i], want)
				}
			}
		})
	}
}

// newTestHostedBackend builds the hosted backend for tests, failing the test
// when a named model_pool snapshot cannot be constructed.
func newTestHostedBackend(t testing.TB, a *MainAgent, catalog map[string]tools.HostedToolSpec) tools.HostedToolBackend {
	t.Helper()
	modelPools := make(map[string][]string)
	if a.globalConfig != nil {
		maps.Copy(modelPools, a.globalConfig.ModelPools)
	}
	if a.projectConfig != nil {
		maps.Copy(modelPools, a.projectConfig.ModelPools)
	}
	backend, err := NewHostedBackend(a, catalog, modelPools)
	if err != nil {
		t.Fatalf("NewHostedBackend: %v", err)
	}
	return backend
}
