package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
)

// sampleExecutionTool is a config-only catalog entry used by the tests; no
// built-in code execution tool exists.
const sampleExecutionTool = "sample_execution"

type stubHostedBackend struct {
	available bool
	obs       *message.HostedObservation
	err       error
	runs      []hostedRun
	sawBudget time.Duration
}

type hostedRun struct {
	tool string
	args map[string]any
}

func (s *stubHostedBackend) Available(string) bool { return s.available }

func (s *stubHostedBackend) ForCaller(string) HostedToolBackend { return s }

func (s *stubHostedBackend) Run(ctx context.Context, tool string, args map[string]any) (*message.HostedObservation, error) {
	s.runs = append(s.runs, hostedRun{tool: tool, args: args})
	if deadline, ok := ctx.Deadline(); ok {
		s.sawBudget = time.Until(deadline)
	}
	if s.err != nil {
		return nil, s.err
	}
	return s.obs, nil
}

func builtinWebSearchSpec(t *testing.T) HostedToolSpec {
	t.Helper()
	spec, ok := BuiltinHostedToolSpecs()[NameWebSearch]
	if !ok {
		t.Fatal("builtin web_search spec missing")
	}
	return spec
}

func TestHostedToolUnavailableWithoutBackend(t *testing.T) {
	spec := builtinWebSearchSpec(t)
	if NewHostedTool(spec, nil).IsAvailable() {
		t.Fatal("tool without backend must be unavailable")
	}
	if NewHostedTool(spec, &stubHostedBackend{}).IsAvailable() {
		t.Fatal("tool must follow the backend's availability")
	}
	if !NewHostedTool(spec, &stubHostedBackend{available: true}).IsAvailable() {
		t.Fatal("available backend must surface the tool")
	}
}

func TestHostedToolRejectsInvalidArgumentsBeforeRun(t *testing.T) {
	cases := []struct {
		name string
		args string
	}{
		{name: "missing query", args: `{}`},
		{name: "blank query", args: `{"query":"   "}`},
		{name: "both filters", args: `{"query":"q","allowed_domains":["example.com"],"blocked_domains":["other.example"]}`},
		{name: "scheme in domain", args: `{"query":"q","allowed_domains":["https://example.com"]}`},
		{name: "path in domain", args: `{"query":"q","allowed_domains":["example.com/docs"]}`},
		{name: "port in domain", args: `{"query":"q","allowed_domains":["example.com:443"]}`},
		{name: "wildcard domain", args: `{"query":"q","allowed_domains":["*.example.com"]}`},
		{name: "empty label", args: `{"query":"q","blocked_domains":["example..com"]}`},
		{name: "single label", args: `{"query":"q","blocked_domains":["localhost"]}`},
		{name: "domain not a string", args: `{"query":"q","allowed_domains":[7]}`},
		{name: "domains not an array", args: `{"query":"q","allowed_domains":"example.com"}`},
		{name: "too many domains", args: `{"query":"q","allowed_domains":` + tooManyDomainsJSON() + `}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			backend := &stubHostedBackend{available: true}
			tool := NewHostedTool(builtinWebSearchSpec(t), backend)
			if _, err := tool.Execute(context.Background(), json.RawMessage(tc.args)); err == nil {
				t.Fatal("expected argument error")
			}
			if len(backend.runs) != 0 {
				t.Fatalf("backend must not run for invalid arguments, got %#v", backend.runs)
			}
		})
	}
}

func tooManyDomainsJSON() string {
	domains := make([]string, webSearchMaxDomains+1)
	for i := range domains {
		domains[i] = `"d` + string(rune('a'+i%26)) + `.example"`
	}
	return "[" + strings.Join(domains, ",") + "]"
}

func TestHostedToolNormalizesWebSearchArguments(t *testing.T) {
	backend := &stubHostedBackend{available: true, obs: &message.HostedObservation{
		Calls: []message.HostedCall{{ID: "srvtoolu_1", Result: json.RawMessage(`[]`)}},
	}}
	tool := NewHostedTool(builtinWebSearchSpec(t), backend)
	args := `{"query":"  golang generics  ","allowed_domains":[" example.com ","docs.example.org"]}`
	if _, err := tool.Execute(context.Background(), json.RawMessage(args)); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(backend.runs) != 1 || backend.runs[0].tool != NameWebSearch {
		t.Fatalf("backend runs = %#v, want one web_search run", backend.runs)
	}
	got := backend.runs[0].args
	if got["query"] != "golang generics" {
		t.Fatalf("query = %#v, want trimmed", got["query"])
	}
	domains, ok := got["allowed_domains"].([]string)
	if !ok || len(domains) != 2 || domains[0] != "example.com" || domains[1] != "docs.example.org" {
		t.Fatalf("allowed domains = %#v", got["allowed_domains"])
	}
	if _, present := got["blocked_domains"]; present {
		t.Fatalf("empty blocked list must be dropped: %#v", got["blocked_domains"])
	}
	if backend.sawBudget <= 0 || backend.sawBudget > defaultHostedToolTimeout {
		t.Fatalf("run budget = %v, want a positive deadline within %v", backend.sawBudget, defaultHostedToolTimeout)
	}

	decl, err := ResolveHostedDeclaration(builtinWebSearchSpec(t), config.ProviderTypeMessages, got)
	if err != nil {
		t.Fatalf("ResolveHostedDeclaration: %v", err)
	}
	var wire map[string]any
	if err := json.Unmarshal(decl.Tool, &wire); err != nil {
		t.Fatalf("unmarshal declaration: %v", err)
	}
	wireDomains, _ := wire["allowed_domains"].([]any)
	if len(wireDomains) != 2 || wireDomains[0] != "example.com" {
		t.Fatalf("declaration domains = %#v", wire["allowed_domains"])
	}
	if _, present := wire["blocked_domains"]; present {
		t.Fatalf("declaration must drop the empty blocked_domains: %s", decl.Tool)
	}
	if wire["max_uses"] != float64(webSearchMaxUses) || wire["type"] != "web_search_20250305" {
		t.Fatalf("declaration = %s", decl.Tool)
	}
}

func TestResolveHostedDeclarationDropsAbsentArgs(t *testing.T) {
	spec := builtinWebSearchSpec(t)

	decl, err := ResolveHostedDeclaration(spec, config.ProviderTypeResponses, map[string]any{"query": "q"})
	if err != nil {
		t.Fatalf("ResolveHostedDeclaration: %v", err)
	}
	var wire map[string]any
	if err := json.Unmarshal(decl.Tool, &wire); err != nil {
		t.Fatalf("unmarshal declaration: %v", err)
	}
	if _, present := wire["filters"]; present {
		t.Fatalf("filters with no populated keys must be dropped: %s", decl.Tool)
	}
	if wire["type"] != NameWebSearch {
		t.Fatalf("declaration = %s", decl.Tool)
	}
	if string(decl.Force) != `"required"` {
		t.Fatalf("force = %s, want the raw required string", decl.Force)
	}
	if len(decl.Include) != 1 || decl.Include[0] != "web_search_call.action.sources" {
		t.Fatalf("include = %#v", decl.Include)
	}

	// A nested map that was empty from the start survives: {} is a valid
	// declaration shape, unlike a map emptied by dropped arguments.
	nested := HostedToolSpec{Name: "sample", Declarations: map[string]config.HostedToolDeclarationConfig{
		config.ProviderTypeMessages: {Tool: map[string]any{
			"type":  "sample_tool",
			"extra": map[string]any{},
			"gone":  map[string]any{"$arg": "missing"},
			"list": []any{
				map[string]any{"$arg": "missing"},
				map[string]any{"keep": true},
			},
		}},
	}}
	resolved, err := ResolveHostedDeclaration(nested, config.ProviderTypeMessages, map[string]any{})
	if err != nil {
		t.Fatalf("ResolveHostedDeclaration: %v", err)
	}
	var nestedWire map[string]any
	if err := json.Unmarshal(resolved.Tool, &nestedWire); err != nil {
		t.Fatalf("unmarshal nested declaration: %v", err)
	}
	if _, present := nestedWire["gone"]; present {
		t.Fatalf("placeholder without an argument must be dropped: %s", resolved.Tool)
	}
	if extra, ok := nestedWire["extra"].(map[string]any); !ok || len(extra) != 0 {
		t.Fatalf("originally empty map must survive: %s", resolved.Tool)
	}
	list, _ := nestedWire["list"].([]any)
	if len(list) != 1 {
		t.Fatalf("dropped array element must be removed: %s", resolved.Tool)
	}
	if string(resolved.Force) != "" {
		t.Fatalf("force without a value must stay nil, got %s", resolved.Force)
	}
}

func TestWebSearchDomainFiltersOnBothWireFamilies(t *testing.T) {
	for _, family := range []string{config.ProviderTypeMessages, config.ProviderTypeResponses} {
		for _, filter := range []string{"allowed_domains", "blocked_domains"} {
			t.Run(family+"/"+filter, func(t *testing.T) {
				spec := builtinWebSearchSpec(t)
				args := map[string]any{"query": "sample query", filter: []any{" example.com "}}
				if err := spec.Validate(args); err != nil {
					t.Fatal(err)
				}
				decl, err := ResolveHostedDeclaration(spec, family, args)
				if err != nil {
					t.Fatal(err)
				}
				var wire map[string]any
				if err := json.Unmarshal(decl.Tool, &wire); err != nil {
					t.Fatal(err)
				}
				filters := wire
				if family == config.ProviderTypeResponses {
					filters, _ = wire["filters"].(map[string]any)
				}
				domains, _ := filters[filter].([]any)
				if len(domains) != 1 || domains[0] != "example.com" {
					t.Fatalf("filters = %#v", filters)
				}
				other := "allowed_domains"
				if filter == other {
					other = "blocked_domains"
				}
				if _, present := filters[other]; present {
					t.Fatalf("absent filter was emitted: %#v", filters)
				}
			})
		}
	}
}

func TestResolveHostedDeclarationUnknownFamily(t *testing.T) {
	_, err := ResolveHostedDeclaration(builtinWebSearchSpec(t), config.ProviderTypeChatCompletions, map[string]any{"query": "q"})
	if err == nil || !strings.Contains(err.Error(), config.ProviderTypeChatCompletions) {
		t.Fatalf("err = %v, want a missing-family error naming the family", err)
	}
}

func TestRenderHostedPrompt(t *testing.T) {
	spec := builtinWebSearchSpec(t)
	if got := RenderHostedPrompt(spec, map[string]any{"query": "golang generics"}); got != "Perform a web search for the query: golang generics" {
		t.Fatalf("prompt = %q", got)
	}

	noTemplate := HostedToolSpec{Name: "sample"}
	if got := RenderHostedPrompt(noTemplate, map[string]any{"query": "q", "count": 2}); got != `{"count":2,"query":"q"}` {
		t.Fatalf("template-less prompt = %q, want the JSON arguments", got)
	}
	if got := RenderHostedPrompt(noTemplate, nil); got != "{}" {
		t.Fatalf("nil args prompt = %q, want an empty object", got)
	}
}

func TestFormatWebSearchObservation(t *testing.T) {
	obs := &message.HostedObservation{
		Summary: "A summary citing [1].",
		Calls: []message.HostedCall{
			{ID: "call_1", Kind: "web_search_call", Result: json.RawMessage(`{"action":{"sources":[{"url":"https://example.com/a","title":"Result A"},{"url":"https://example.com/b"}]}}`)},
			{ID: "srvtoolu_2", Kind: "server_tool_use", Result: json.RawMessage(`[{"url":"https://example.com/a","title":"Result A"},{"url":"https://example.com/c","title":"Result C"}]`)},
			{ID: "srvtoolu_3", Kind: "server_tool_use", Error: "web_search_tool_result: unavailable"},
		},
	}
	out := formatWebSearchObservation(obs)
	mustContain(t, out, "A summary citing [1].")
	mustContain(t, out, "[1] Result A\n    https://example.com/a")
	mustContain(t, out, "[2] https://example.com/b")
	mustContain(t, out, "[3] Result C\n    https://example.com/c")
	if strings.Contains(out, "[4]") {
		t.Fatalf("duplicate URLs must be deduplicated:\n%s", out)
	}
	mustContain(t, out, "Search errors: web_search_tool_result: unavailable")

	zeroHits := formatWebSearchObservation(&message.HostedObservation{
		Summary: "Nothing found.",
		Calls:   []message.HostedCall{{ID: "srvtoolu_9", Result: json.RawMessage(`[]`)}},
	})
	mustContain(t, zeroHits, "Nothing found.")
	mustContain(t, zeroHits, "(no search results returned)")
}

func TestHostedToolFormatsWebSearchResult(t *testing.T) {
	backend := &stubHostedBackend{available: true, obs: &message.HostedObservation{
		Summary: "Search summary.",
		Calls: []message.HostedCall{{
			ID:     "srvtoolu_1",
			Kind:   "server_tool_use",
			Result: json.RawMessage(`[{"url":"https://example.com/a","title":"Result A"}]`),
		}},
	}}
	tool := NewHostedTool(builtinWebSearchSpec(t), backend)
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"query":"q"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	mustContain(t, out, "Search summary.")
	mustContain(t, out, "[1] Result A\n    https://example.com/a")
}

func TestHostedToolFormatsGenericObservation(t *testing.T) {
	backend := &stubHostedBackend{available: true, obs: &message.HostedObservation{
		Summary: "Ran the sample.",
		Calls: []message.HostedCall{
			{ID: "call_1", Kind: "sample_tool_call", Status: "completed", Input: json.RawMessage(`{"arg":"value"}`), Result: json.RawMessage(`{"ok":true}`)},
			{ID: "call_2", Kind: "sample_tool_call", Status: "failed", Error: "sample_tool_call: failed"},
		},
	}}
	tool := NewHostedTool(HostedToolSpec{Name: "sample_tool", ReadOnly: true, ConcurrencySafe: true}, backend)
	out, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	mustContain(t, out, "Ran the sample.")
	mustContain(t, out, "sample_tool_call 1 (completed)")
	mustContain(t, out, `Input: {"arg":"value"}`)
	mustContain(t, out, `Result: {"ok":true}`)
	mustContain(t, out, "sample_tool_call 2 (failed)")
	mustContain(t, out, "Error: sample_tool_call: failed")

	big := &stubHostedBackend{available: true, obs: &message.HostedObservation{
		Calls: []message.HostedCall{{ID: "call_3", Kind: "sample_tool_call", Result: json.RawMessage(`"` + strings.Repeat("a", hostedPayloadSnippetBytes+500) + `"`)}},
	}}
	out, err = NewHostedTool(HostedToolSpec{Name: "sample_tool"}, big).Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	mustContain(t, out, "bytes truncated")
}

func TestHostedToolPropagatesBackendError(t *testing.T) {
	backend := &stubHostedBackend{available: true, err: errors.New("all hosted targets failed")}
	tool := NewHostedTool(builtinWebSearchSpec(t), backend)
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"query":"q"}`)); err == nil || !strings.Contains(err.Error(), "all hosted targets failed") {
		t.Fatalf("err = %v, want backend failure", err)
	}
}

func TestHostedToolNilObservationFails(t *testing.T) {
	backend := &stubHostedBackend{available: true}
	tool := NewHostedTool(builtinWebSearchSpec(t), backend)
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"query":"q"}`)); err == nil {
		t.Fatal("nil observation must fail instead of rendering an empty result")
	}
}

func TestResolveHostedToolCatalog(t *testing.T) {
	readOnly := true
	catalog := ResolveHostedToolCatalog(map[string]config.HostedToolConfig{
		NameWebSearch: {
			Description:     "Custom search description.",
			ReadOnly:        &readOnly,
			ConcurrencySafe: new(true),
			Declarations: map[string]config.HostedToolDeclarationConfig{
				config.ProviderTypeMessages: {
					Include: []string{"custom.include"},
					Headers: map[string]string{"anthropic-beta": "custom-beta"},
				},
			},
		},
		sampleExecutionTool: {
			Description: "Run code server-side.",
			Declarations: map[string]config.HostedToolDeclarationConfig{
				config.ProviderTypeMessages: {Tool: map[string]any{"type": "sample_execution"}},
			},
		},
	})

	search := catalog[NameWebSearch]
	if search.Description != "Custom search description." {
		t.Fatalf("description override = %q", search.Description)
	}
	// Untouched builtin fields survive the override.
	if search.Prompt == "" || len(search.Parameters) == 0 {
		t.Fatalf("builtin surface must survive field-level overrides: %#v", search)
	}
	decl := search.Declarations[config.ProviderTypeMessages]
	if decl.Tool == nil || decl.Force == nil {
		t.Fatalf("builtin declaration must survive a partial override: %#v", decl)
	}
	if len(decl.Include) != 1 || decl.Include[0] != "custom.include" {
		t.Fatalf("include override = %#v", decl.Include)
	}
	if decl.Headers["Anthropic-Beta"] != "custom-beta" {
		t.Fatalf("header merge = %#v", decl.Headers)
	}

	exec := catalog[sampleExecutionTool]
	if exec.Name != sampleExecutionTool || exec.ReadOnly || exec.ConcurrencySafe {
		t.Fatalf("config-only entry must start conservative: %#v", exec)
	}
	if exec.Parameters == nil || exec.Parameters["type"] != "object" {
		t.Fatalf("config-only entry must carry an object schema: %#v", exec.Parameters)
	}
	if exec.Declarations[config.ProviderTypeMessages].Tool["type"] != "sample_execution" {
		t.Fatalf("declaration = %#v", exec.Declarations)
	}
}

func TestHostedToolConcurrencyPolicy(t *testing.T) {
	readOnly := &stubHostedBackend{available: true}
	readTool := NewHostedTool(builtinWebSearchSpec(t), readOnly)
	if !readTool.ConcurrencySafeReadOnly(nil) || !readTool.IsReadOnly() {
		t.Fatal("builtin web_search must stay read-only and concurrency-safe")
	}
	policy := readTool.ConcurrencyPolicy(nil)
	if policy.Mode != ConcurrencyModeRead || policy.Resource != "hosted:"+NameWebSearch {
		t.Fatalf("read policy = %#v", policy)
	}

	writeTool := NewHostedTool(HostedToolSpec{Name: "sample_exec"}, readOnly)
	if writeTool.ConcurrencySafeReadOnly(nil) {
		t.Fatal("a tool that is not read-only must not join read batches")
	}
	policy = writeTool.ConcurrencyPolicy(nil)
	if policy.Mode != ConcurrencyModeWrite || policy.Resource != "hosted:sample_exec" {
		t.Fatalf("write policy = %#v", policy)
	}

	// concurrency_safe without read_only stays conservative.
	unsafeSpec := HostedToolSpec{Name: "sample_exec", ConcurrencySafe: true}
	if NewHostedTool(unsafeSpec, readOnly).ConcurrencySafeReadOnly(nil) {
		t.Fatal("concurrency_safe without read_only must not be batched")
	}
}

func TestHostedToolCatalogModelPoolOverlay(t *testing.T) {
	catalog := ResolveHostedToolCatalog(map[string]config.HostedToolConfig{
		NameWebSearch:       {ModelPool: "  tools  "},
		sampleExecutionTool: {ModelPool: "   "},
	})
	if got := catalog[NameWebSearch].ModelPool; got != "tools" {
		t.Fatalf("model_pool = %q, want trimmed %q", got, "tools")
	}
	if got := catalog[sampleExecutionTool].ModelPool; got != "" {
		t.Fatalf("blank model_pool = %q, want unset", got)
	}
}

func TestHostedArgumentsPreserveNumbersAndLiteralPlaceholders(t *testing.T) {
	backend := &stubHostedBackend{available: true, obs: &message.HostedObservation{}}
	spec := HostedToolSpec{
		Name: "sample", Prompt: "Find {query}. {query} {unknown} {record_id} {nested}",
		Parameters: map[string]any{"type": "object", "properties": map[string]any{
			"query":     map[string]any{"type": "string"},
			"label":     map[string]any{"type": "string"},
			"record_id": map[string]any{"type": "integer"},
			"nested":    map[string]any{"type": "array", "items": map[string]any{"type": "object", "properties": map[string]any{"value": map[string]any{"type": "number"}}}},
		}},
		Declarations: map[string]config.HostedToolDeclarationConfig{
			config.ProviderTypeResponses: {Tool: map[string]any{"type": "sample", "id": map[string]any{"$arg": "record_id"}, "nested": map[string]any{"$arg": "nested"}, "limit": json.Number("9007199254740993")}},
		},
	}
	tool := NewHostedTool(spec, backend)
	raw := json.RawMessage(`{"query":"literal {label}","label":"changed","record_id":9007199254740993,"nested":[{"value":1.234567890123456789}]}`)
	sanitized, _, _, err := SanitizeUnknownArgsWithDiagnostics(tool, raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tool.Execute(context.Background(), sanitized); err != nil {
		t.Fatal(err)
	}
	args := backend.runs[0].args
	for range 100 {
		got := RenderHostedPrompt(spec, args)
		want := `Find literal {label}. literal {label} {unknown} 9007199254740993 [{"value":1.234567890123456789}]`
		if got != want {
			t.Fatalf("prompt = %q, want %q", got, want)
		}
	}
	decl, err := ResolveHostedDeclaration(spec, config.ProviderTypeResponses, args)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"id":9007199254740993,"limit":9007199254740993,"nested":[{"value":1.234567890123456789}],"type":"sample"}`
	if string(decl.Tool) != want {
		t.Fatalf("declaration = %s", decl.Tool)
	}
	if got := RenderHostedPrompt(HostedToolSpec{}, args); !strings.Contains(got, `"record_id":9007199254740993`) {
		t.Fatalf("JSON prompt = %s", got)
	}
}

func TestHostedArgumentsRejectTrailingJSON(t *testing.T) {
	for _, raw := range []string{`{} {}`, `{} extra`, `null {}`, `[]`, `42`} {
		if _, err := parseHostedToolArgs(json.RawMessage(raw)); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
}
