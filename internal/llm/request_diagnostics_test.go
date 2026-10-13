package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/contextdiag"
	"github.com/keakon/chord/internal/executiongate"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/toolname"
)

func diagnosticTestCollector() (*DiagnosticsCollector, *contextdiag.Store) {
	store := &contextdiag.Store{}
	return NewDiagnosticsCollector(contextdiag.Binding{TaskID: "main", Epoch: 1}, store), store
}
func diagnosticCategory(t *testing.T, categories []contextdiag.Category, kind string) contextdiag.Category {
	t.Helper()
	for _, c := range categories {
		if c.Kind == kind {
			return c
		}
	}
	t.Fatalf("missing category %s", kind)
	return contextdiag.Category{}
}
func TestDiagnosticEstimateUniqueCarriersAndSources(t *testing.T) {
	msgs := []message.Message{
		{Role: message.RoleUser, Content: strings.Repeat("mirror", 100), Parts: []message.ContentPart{{Type: message.ContentPartText, Text: "abc"}, {Type: message.ContentPartImage, Data: make([]byte, 1000)}}},
		{Role: message.RoleUser, Content: "env", DiagnosticSource: message.DiagnosticSourceInstructions},
		{Role: message.RoleUser, Content: "run", Kind: message.KindTurnOverlay},
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "skill-1", Name: toolname.Skill, Args: json.RawMessage(`{"name":"sample"}`)}}},
		{Role: message.RoleTool, ToolCallID: "skill-1", Content: "loaded", ToolStatus: message.ToolStatusSuccess},
		{Role: message.RoleTool, ToolCallID: "missing", Content: "missing"},
		{Role: message.RoleAssistant, Content: strings.Repeat("mirror", 100), ResponsesOutput: []message.ResponsesOutputItem{{Type: "message"}}},
		{Role: message.RoleUser, Content: "local", Kind: message.KindQuestionState},
	}
	categories, limitations, complete := estimateDiagnosticInput(newDiagnosticBudget(context.Background()), "system", msgs, nil, RequestTuning{})
	if complete != contextdiag.Partial || !slices.Contains(limitations, "local_state_in_projection") {
		t.Fatal("unknown content presented as complete")
	}
	if c := diagnosticCategory(t, categories, contextdiag.Attachments); c.Estimate.Tokens != 0 || c.Estimate.UnknownItems != 1 {
		t.Fatalf("binary estimated: %+v", c)
	}
	if c := diagnosticCategory(t, categories, contextdiag.Instructions); c.Estimate.Tokens != 1 {
		t.Fatalf("environment classification: %+v", c)
	}
	results := diagnosticCategory(t, categories, contextdiag.ToolResults)
	if !slices.ContainsFunc(results.Sources, func(s contextdiag.Source) bool { return s.Kind == contextdiag.Skill && s.Label == "sample" }) {
		t.Fatalf("skill missing: %+v", results)
	}
	for _, c := range categories {
		sum := c.Other
		for _, source := range c.Sources {
			addEstimate(&sum, source.Estimate)
		}
		if sum != c.Estimate {
			t.Fatalf("non-additive category %s", c.Kind)
		}
	}
}
func TestDiagnosticDirectoryRulesAndSkillNotices(t *testing.T) {
	messages := []message.Message{
		{Role: message.RoleUser, Kind: message.KindDirectoryInstructionProjection, Content: "Follow the module instructions."},
		{Role: message.RoleUser, Kind: message.KindSkillDiscovery, Content: "The sample skill is available."},
	}
	categories, limitations, _ := estimateDiagnosticInput(newDiagnosticBudget(context.Background()), "", messages, nil, RequestTuning{})
	if slices.Contains(limitations, "source_unspecified") {
		t.Fatal("known directory and skill sources were not classified")
	}
	for _, kind := range []string{contextdiag.Instructions, contextdiag.Runtime} {
		if category := diagnosticCategory(t, categories, kind); category.Estimate.Tokens == 0 {
			t.Fatalf("missing source tokens: %+v", category)
		}
	}
}

func TestDiagnosticEstimateLimitsAndSchemas(t *testing.T) {
	schema := map[string]any{"enum": []any{"a", "b", "c"}}
	if _, ok := boundedDiagnosticJSON(schema); !ok {
		t.Fatal("valid enum rejected")
	}
	for _, v := range []any{map[string]any{"text": strings.Repeat("x", diagnosticSchemaBytes+1)}, make([]any, diagnosticSchemaNodes+1), func() {}} {
		if _, ok := boundedDiagnosticJSON(v); ok {
			t.Fatal("unbounded schema accepted")
		}
	}
	cycle := map[string]any{}
	cycle["self"] = cycle
	if _, ok := boundedDiagnosticJSON(cycle); ok {
		t.Fatal("cycle accepted")
	}
	defs := make([]message.ToolDefinition, 300)
	for i := range defs {
		defs[i] = message.ToolDefinition{Name: fmt.Sprintf("tool-%d", i), InputSchema: schema}
	}
	msgs := make([]message.Message, diagnosticMaxMessages+1)
	budget := newDiagnosticBudget(context.Background())
	budget.deadline = time.Now().Add(time.Hour)
	categories, limitations, complete := estimateDiagnosticInput(budget, "", msgs, defs, RequestTuning{})
	if complete != contextdiag.Partial || !slices.Contains(limitations, diagnosticScanIncomplete) || !slices.Contains(limitations, "sources_incomplete") {
		t.Fatal("limits not disclosed", limitations)
	}
	c := diagnosticCategory(t, categories, contextdiag.Tools)
	if len(c.Sources) != 5 || c.Other.Tokens == 0 {
		t.Fatal("sources not bounded")
	}
	b := newDiagnosticBudget(context.Background())
	b.deadline = time.Now().Add(-time.Second)
	baseline := buildDiagnosticBaseline(b, strings.Repeat("x", 100000), nil, nil)
	if baseline.systemOK || !b.incomplete {
		t.Fatal("time budget not enforced")
	}
	b = newDiagnosticBudget(context.Background())
	baseline = buildDiagnosticBaseline(b, strings.Repeat("x", diagnosticMaxHashBytes+1), nil, nil)
	if baseline.systemOK {
		t.Fatal("partial hash claimed complete")
	}
}
func TestDiagnosticCacheAllChangesAndPrefixShapes(t *testing.T) {
	build := func(system string, msgs []message.Message, defs []message.ToolDefinition) *diagnosticBaseline {
		return buildDiagnosticBaseline(newDiagnosticBudget(context.Background()), system, msgs, defs)
	}
	a := message.Message{Role: message.RoleUser, Content: "aaa"}
	b := message.Message{Role: message.RoleUser, Content: "bbb"}
	old := build("system", []message.Message{a, b}, nil)
	for _, tc := range []struct {
		msgs  []message.Message
		state string
	}{{[]message.Message{a, b}, contextdiag.Same}, {[]message.Message{a, b, a}, contextdiag.Append}, {[]message.Message{a}, contextdiag.Truncate}, {[]message.Message{b, a}, contextdiag.Rewrite}} {
		result := compareDiagnosticBaseline(old, build("system", tc.msgs, nil))
		if result.PrefixState != tc.state {
			t.Fatalf("got %s want %s", result.PrefixState, tc.state)
		}
	}
	c := compareDiagnosticBaseline(old, build("changed", []message.Message{b}, []message.ToolDefinition{{Name: "sample"}}))
	if !reflect.DeepEqual(c.LocalChanges, []string{contextdiag.System, contextdiag.Tools, contextdiag.Messages}) || c.ExpectedUsable {
		t.Fatalf("lost simultaneous changes: %+v", c)
	}
	local := a
	local.InputID = "local"
	local.ToolStatus = message.ToolStatusError
	local.DiagnosticSource = message.DiagnosticSourceInstructions
	c = compareDiagnosticBaseline(build("", []message.Message{a}, nil), build("", []message.Message{local}, nil))
	if c.PrefixState != contextdiag.Same {
		t.Fatal("local metadata changed prefix")
	}
	local.Parts = []message.ContentPart{{Type: message.ContentPartImage}}
	c = compareDiagnosticBaseline(old, build("system", []message.Message{local}, nil))
	if c.PrefixState != contextdiag.Unknown || c.ExpectedUsable {
		t.Fatal("opaque comparison")
	}
}

type diagnosticRecordingProvider struct {
	calls    int
	response *message.Response
	err      error
	inspect  func(context.Context, []message.Message, []message.ToolDefinition)
}

func (p *diagnosticRecordingProvider) CompleteStream(ctx context.Context, _, _, _ string, msgs []message.Message, defs []message.ToolDefinition, _ int, _ RequestTuning, _ StreamCallback) (*message.Response, error) {
	p.calls++
	if p.inspect != nil {
		p.inspect(ctx, msgs, defs)
	}
	return p.response, p.err
}
func diagnosticProviderConfig(protocol string) *ProviderConfig {
	return NewProviderConfig("sample", config.ProviderConfig{Type: protocol, APIURL: "https://example.invalid/v1", Models: map[string]config.ModelConfig{"test-model": {}}}, []string{"sample-key"})
}
func TestDiagnosticCustomSubmissionAndNoninterference(t *testing.T) {
	cfg := diagnosticProviderConfig(config.ProviderTypeChatCompletions)
	original := message.TokenUsage{InputTokens: 10, OutputTokens: 2, CacheReadTokens: 5}
	p := &diagnosticRecordingProvider{response: &message.Response{Content: "sample", Usage: &original}}
	client := NewClient(cfg, p, "test-model", 100, "system")
	defer client.Close()
	collector, store := diagnosticTestCollector()
	msgs := []message.Message{{Role: message.RoleUser, Content: "sample"}}
	for _, enabled := range []bool{false, true} {
		opts := CompleteStreamOptions{}
		if enabled {
			opts.Diagnostics = collector.BeginRequest(7)
		}
		dispatched, started := 0, 0
		ctx, _ := WithRequestDispatch(context.Background(), func() { dispatched++ })
		ctx = WithRequestStarted(ctx, func() { started++ })
		_, err := client.CompleteStreamWithOptions(ctx, msgs, nil, nil, opts)
		if err != nil {
			t.Fatal(err)
		}
		if dispatched != 1 || started != 1 {
			t.Fatal("dispatch behavior changed")
		}
	}
	snapshot, ok := store.Latest()
	if !ok || snapshot.ID.Attempt != 1 || snapshot.HistoryGeneration != 7 || snapshot.State != contextdiag.Completed || snapshot.Transport != contextdiag.Custom {
		t.Fatalf("bad snapshot: %+v", snapshot)
	}
	if p.calls != 2 {
		t.Fatal("extra provider call")
	}
	p.inspect = func(ctx context.Context, _ []message.Message, _ []message.ToolDefinition) {
		prepareDiagnosticSubmission(ctx, contextdiag.HTTP)
		markRequestSent(ctx)
		prepareDiagnosticSubmission(ctx, contextdiag.WebSocket)
		markRequestSent(ctx)
	}
	_, err := client.CompleteStreamWithOptions(context.Background(), msgs, nil, nil, CompleteStreamOptions{Diagnostics: collector.BeginRequest(8)})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, _ = store.Latest()
	if snapshot.ID.Attempt != 3 || snapshot.Transport != contextdiag.WebSocket || snapshot.State != contextdiag.Completed {
		t.Fatal("usage attributed to wrong submission", snapshot.ID)
	}
}
func TestDiagnosticAdmissionAndStaleCallbacks(t *testing.T) {
	collector, store := diagnosticTestCollector()
	cfg := diagnosticProviderConfig(config.ProviderTypeChatCompletions)
	p := &diagnosticRecordingProvider{response: &message.Response{Content: "sample"}}
	d := collector.BeginRequest(0)
	ctx := context.WithValue(context.Background(), diagnosticsContextKey{}, d)
	gate := &executiongate.Gate{}
	gate.Freeze()
	paused := executiongate.WithContext(ctx, gate, "main")
	if _, err := completeObservedProvider(paused, p, cfg, "sample-key", "test-model", "", nil, nil, 10, RequestTuning{}, nil); !executiongate.IsPaused(err) {
		t.Fatal("admission bypassed")
	}
	if _, ok := store.Latest(); ok {
		t.Fatal("rejected admission published")
	}

	// The wrapper may prepare a summary, but without a submission no snapshot exists.
	scope := &diagnosticScope{request: d, baseline: &diagnosticBaseline{messagesOK: true}, snapshot: contextdiag.Snapshot{}}
	scope.prepare(contextdiag.HTTP)
	if _, ok := store.Latest(); ok {
		t.Fatal("preparation published")
	}
	scope.submit()
	id := scope.active
	d2 := collector.BeginRequest(0)
	_ = d2
	scope.prepare(contextdiag.HTTP)
	scope.submit()
	snapshot, _ := store.Latest()
	if snapshot.ID != id {
		t.Fatal("stale callback published")
	}
	collector.Invalidate()
	_, _ = completeObservedProvider(ctx, p, cfg, "sample-key", "test-model", "", nil, nil, 10, RequestTuning{}, nil)
	snapshot, _ = store.Latest()
	if snapshot.ID != id || snapshot.State != contextdiag.Dispatched {
		t.Fatal("invalidated observer changed snapshot")
	}
}
func TestDiagnosticUsageCopiesAndNormalizes(t *testing.T) {
	for _, protocol := range []string{config.ProviderTypeChatCompletions, config.ProviderTypeMessages, config.ProviderTypeResponses, config.ProviderTypeGenerateContent} {
		cfg := diagnosticProviderConfig(protocol)
		original := message.TokenUsage{InputTokens: 20, CacheReadTokens: 10, CacheWriteTokens: 5, CacheWrite1hTokens: 3, OutputTokens: 4, InputSemanticsKnown: true, InputIncludesCacheRead: false, InputIncludesCacheWrite: false}
		response := &message.Response{Usage: &original}
		before := original
		got := diagnosticUsage(cfg, response, false)
		if got.FullInput != 35 || got.CacheWrite != 5 || got.State != contextdiag.Reported || original != before {
			t.Fatalf("%s: %+v original=%+v", protocol, got, original)
		}
		if diagnosticUsage(cfg, response, true).State != contextdiag.Partial {
			t.Fatal("failed usage not partial")
		}
		if diagnosticUsage(cfg, &message.Response{Usage: &message.TokenUsage{}}, false).State != contextdiag.Missing {
			t.Fatal("zero usage reported")
		}
	}
}
func TestDiagnosticHTTPConnectionFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close()
	cfg := NewProviderConfig("sample", config.ProviderConfig{Type: config.ProviderTypeChatCompletions, APIURL: url, Models: map[string]config.ModelConfig{"test-model": {}}}, []string{"sample-key"})
	impl, err := NewOpenAIProvider(cfg, "")
	if err != nil {
		t.Fatal(err)
	}
	collector, store := diagnosticTestCollector()
	ctx := context.WithValue(context.Background(), diagnosticsContextKey{}, collector.BeginRequest(0))
	_, err = completeObservedProvider(ctx, impl, cfg, "sample-key", "test-model", "", []message.Message{{Role: message.RoleUser, Content: "sample"}}, nil, 10, RequestTuning{}, func(message.StreamDelta) {})
	snapshot, ok := store.Latest()
	if err == nil || !ok || snapshot.State != contextdiag.Failed || snapshot.Transport != contextdiag.HTTP || snapshot.Usage.State != contextdiag.Missing {
		t.Fatalf("connection failure: %v %+v", err, snapshot)
	}
}
func TestDiagnosticFailedUsageAndCancellation(t *testing.T) {
	cfg := diagnosticProviderConfig(config.ProviderTypeChatCompletions)
	collector, store := diagnosticTestCollector()
	p := &diagnosticRecordingProvider{response: &message.Response{Usage: &message.TokenUsage{InputTokens: 10}}, err: errors.New("sample")}
	ctx := context.WithValue(context.Background(), diagnosticsContextKey{}, collector.BeginRequest(0))
	_, _ = completeObservedProvider(ctx, p, cfg, "key", "test-model", "", nil, nil, 10, RequestTuning{}, nil)
	snapshot, _ := store.Latest()
	if snapshot.State != contextdiag.Failed || snapshot.Usage.State != contextdiag.Partial {
		t.Fatal("partial failure", snapshot)
	}
	ctx, cancel := context.WithCancel(context.Background())
	p.inspect = func(context.Context, []message.Message, []message.ToolDefinition) { cancel() }
	ctx = context.WithValue(ctx, diagnosticsContextKey{}, collector.BeginRequest(0))
	_, _ = completeObservedProvider(ctx, p, cfg, "key", "test-model", "", nil, nil, 10, RequestTuning{}, nil)
	snapshot, _ = store.Latest()
	if snapshot.State != contextdiag.Cancelled {
		t.Fatal("cancellation", snapshot.State)
	}
}

func TestDiagnosticWebSocketPrewarmAndIncremental(t *testing.T) {
	srv := newCodexWSCaptureServer(t, []string{"prewarm-1", "response-1", "response-2"})
	defer srv.Close()
	cfg := diagnosticProviderConfig(config.ProviderTypeResponses)
	collector, store := diagnosticTestCollector()
	d := collector.BeginRequest(0)
	scope := &diagnosticScope{request: d, provider: cfg, baseline: &diagnosticBaseline{messagesOK: true, systemOK: true, toolsOK: true}, snapshot: contextdiag.Snapshot{Usage: contextdiag.Usage{State: contextdiag.Missing}}}
	ctx := context.WithValue(context.Background(), diagnosticScopeContextKey{}, scope)
	r := &ResponsesProvider{}
	defer r.resetCodexWebSocketChain("test")
	input := []responsesInputItem{{Type: "message", Role: "user", Content: "sample"}}
	req := &responsesRequest{Model: "test-model", Input: input, Stream: true, Include: []string{}}
	resp, _, err := r.completeStreamCodexWebSocket(ctx, srv.ResponsesURL(), "key", "test-model", req, input, nil, time.Now(), codexWSCompleteOptions{})
	if err != nil {
		t.Fatal(err)
	}
	scope.finish(ctx, resp, err)
	snapshot, ok := store.Latest()
	if !ok || snapshot.ID.Attempt != 1 || snapshot.Transport != contextdiag.WebSocket {
		t.Fatalf("prewarm became attempt: %+v", snapshot)
	}
	if len(srv.Requests()) != 2 {
		t.Fatalf("fixture expected prewarm+request: %d", len(srv.Requests()))
	}
}

func TestDiagnosticDomainsRebuildRatherThanRememberModels(t *testing.T) {
	collector, store := diagnosticTestCollector()
	cfg := diagnosticProviderConfig(config.ProviderTypeChatCompletions)
	p := &diagnosticRecordingProvider{response: &message.Response{Content: "sample", Usage: &message.TokenUsage{CacheReadTokens: 3}}}
	for i, key := range []string{"key-a", "key-a", "key-b", "key-a"} {
		ctx := context.WithValue(context.Background(), diagnosticsContextKey{}, collector.BeginRequest(0))
		_, err := completeObservedProvider(ctx, p, cfg, key, "test-model", "sample", nil, nil, 10, RequestTuning{}, nil)
		if err != nil {
			t.Fatal(err)
		}
		snapshot, _ := store.Latest()
		want := contextdiag.DomainChanged
		if i == 0 {
			want = contextdiag.First
		} else if i == 1 {
			want = contextdiag.Comparable
		}
		if snapshot.Cache.BaselineState != want || snapshot.Cache.Explanation != contextdiag.ReadObserved {
			t.Fatalf("step %d: %+v", i, snapshot.Cache)
		}
	}
}

func TestDiagnosticRetryKeysFallbackAndFrozenReduction(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprint(enabled), func(t *testing.T) {
			primary := testProviderConfigWithKeys("sample", "model-a", []string{"key-a", "key-b"})
			fallback := testProviderConfig("sample-fallback", "model-b")
			p := &recordingProvider{calls: []scriptedCall{{err: &APIError{StatusCode: 503, Message: "sample failure"}}, {err: &APIError{StatusCode: 503, Message: "sample failure"}}}}
			f := &recordingProvider{calls: []scriptedCall{{resp: &message.Response{Content: "sample", Usage: &message.TokenUsage{InputTokens: 30, CacheReadTokens: 10}}}}}
			client := NewClient(primary, p, "model-a", 100, "system")
			defer client.Close()
			client.SetModelPool([]FallbackModel{{ProviderConfig: primary, ProviderImpl: p, ModelID: "model-a", MaxTokens: 100, ContextLimit: 128000}, {ProviderConfig: fallback, ProviderImpl: f, ModelID: "model-b", MaxTokens: 100, ContextLimit: 128000}}, 0)
			collector, store := diagnosticTestCollector()
			opts := CompleteStreamOptions{}
			if enabled {
				opts.Diagnostics = collector.BeginRequest(0)
				opts.Diagnostics.SetReduction(contextdiag.Reduction{State: contextdiag.Observed, Before: 20, After: 10})
			}
			opts.BeforeFallback = func(_ context.Context, msgs []message.Message, _ FallbackModel) ([]message.Message, error) {
				opts.Diagnostics.SetReduction(contextdiag.Reduction{State: contextdiag.Observed, Before: 20, After: 5})
				return msgs, nil
			}
			response, err := client.CompleteStreamWithOptions(context.Background(), []message.Message{{Role: message.RoleUser, Content: "sample"}}, nil, func(message.StreamDelta) {}, opts)
			if err != nil || response.Content != "sample" {
				t.Fatal("fallback failed", err)
			}
			if !reflect.DeepEqual(p.apiKeys, []string{"key-a", "key-b"}) || len(f.apiKeys) != 1 {
				t.Fatal("diagnostics changed retries", p.apiKeys, f.apiKeys)
			}
			snapshot, ok := store.Latest()
			if enabled && (!ok || snapshot.ID.Attempt != 3 || snapshot.ModelRef != "sample-fallback/model-b" || snapshot.Reduction.After != 5 || snapshot.Usage.FullInput != 30) {
				t.Fatalf("wrong final attribution %+v", snapshot)
			}
			if !enabled && ok {
				t.Fatal("disabled diagnostics collected")
			}
		})
	}
}
