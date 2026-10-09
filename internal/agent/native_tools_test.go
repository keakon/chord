package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/hook"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/recovery"
	"github.com/keakon/chord/internal/tools"
)

type nativeTestHooks struct{ hook.NoopEngine }

func (*nativeTestHooks) HasSyncHooks(point string) bool { return point == hook.OnToolCall }

func TestNativeSearchAuthorizationPolicy(t *testing.T) {
	allow := permission.Ruleset{{Permission: tools.NameWebSearch, Pattern: "*", Action: permission.ActionAllow}}
	for _, tc := range []struct {
		name   string
		rules  permission.Ruleset
		config config.HostedToolConfig
		hooks  hook.Manager
		want   bool
	}{
		{name: "explicit allow", rules: allow, want: true},
		{name: "no permission"},
		{name: "ask", rules: permission.Ruleset{{Permission: tools.NameWebSearch, Pattern: "*", Action: permission.ActionAsk}}},
		{name: "parameter rule", rules: append(append(permission.Ruleset{}, allow...), permission.Rule{Permission: tools.NameWebSearch, Pattern: "sample*", Action: permission.ActionDeny})},
		{name: "custom prompt", rules: allow, config: config.HostedToolConfig{Prompt: "Search {query}"}},
		{name: "tool pool", rules: allow, config: config.HostedToolConfig{ModelPool: "research"}},
		{name: "hook", rules: allow, hooks: &nativeTestHooks{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			catalog := tools.ResolveHostedToolCatalog(map[string]config.HostedToolConfig{tools.NameWebSearch: tc.config})
			registry := tools.NewRegistry()
			registry.Register(tools.NewHostedTool(catalog[tools.NameWebSearch], nil))
			if got := nativeToolPermitted(registry, tc.rules, tc.hooks, tools.NameWebSearch); got != tc.want {
				t.Fatalf("allowed=%v", got)
			}
		})
	}
}

func TestNativePolicyOnlyReleasesCompletedLocalContinuation(t *testing.T) {
	journal := recovery.NativeRequestJournal{SessionDir: t.TempDir(), AgentID: "main"}
	p := nativePolicy(journal, func(string) bool { return true }, nil)
	id, err := p.Begin(t.Context(), llm.NativeRequestRecord{})
	if err != nil {
		t.Fatal(err)
	}
	if err = p.Finish(id, message.NativeRequestCompleted, &message.Response{}, nil); err != nil {
		t.Fatal(err)
	}
	// A fresh request cannot rely on an in-memory message when canonical
	// persistence has not acknowledged the previous receipt.
	fresh := nativePolicy(journal, func(string) bool { return true }, nil)
	record := llm.NativeRequestRecord{Messages: []message.Message{{NativeTools: &message.NativeToolHistory{RequestIDs: []string{id}}}}}
	if _, err = fresh.Begin(t.Context(), record); err == nil {
		t.Fatal("in-memory history bypassed durable barrier")
	}
	next, err := p.Begin(t.Context(), record)
	if err != nil {
		t.Fatal(err)
	}
	if err = p.Finish(next, message.NativeRequestUnknown, &message.Response{}, errors.New("stream closed")); err != nil {
		t.Fatal(err)
	}
	if _, err = p.Begin(t.Context(), record); err == nil {
		t.Fatal("unknown result allowed continuation")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err = p.Begin(ctx, record); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
}

func TestNativePreflightBlocksOrdinaryRoutes(t *testing.T) {
	for _, route := range []string{"permission", "disabled", "unconfigured", "required"} {
		t.Run(route, func(t *testing.T) {
			journal := recovery.NativeRequestJournal{SessionDir: t.TempDir(), AgentID: "main"}
			id, err := journal.Begin(llm.NativeRequestRecord{Target: "sample/test-model"}, nil)
			if err != nil {
				t.Fatal(err)
			}
			requests := 0
			impl := &hostedScriptProvider{respond: func(_ int, _ context.Context) (*message.Response, error) {
				requests++
				return &message.Response{Content: "Complete", StopReason: "stop"}, nil
			}}
			native := &config.NativeWebSearchConfig{Contract: config.NativeWebSearchResponses, APIURL: "https://example.invalid/v1/responses", Preauthorized: route != "disabled"}
			if route == "unconfigured" {
				native = nil
			}
			provider := llm.NewProviderConfig("sample", config.ProviderConfig{Type: config.ProviderTypeResponses, APIURL: "https://example.invalid/v1/responses", Models: map[string]config.ModelConfig{"test-model": {NativeWebSearch: native}}}, []string{"sample-key"})
			client := llm.NewClient(provider, impl, "test-model", 1024, "")
			defer client.Close()
			client.SetStreamRetryRounds(3)
			if route == "required" {
				client.SetNextRequestTuningOverride(requiredToolChoiceTuning(llm.RequestTuning{}))
			}
			policy := nativePolicy(journal, func(string) bool { return route != "permission" }, nil)
			var failed *message.NativeToolHistory
			policy.Failed = func(receipt *message.NativeToolHistory) { failed = receipt }
			_, err = client.CompleteStreamWithOptions(t.Context(), []message.Message{{Role: message.RoleUser, Content: "Search the sample documentation"}}, nil, nil, llm.CompleteStreamOptions{NativeTools: policy})
			if !llm.IsNativeToolError(err) || requests != 0 || failed == nil || !failed.OutcomeUnknown || failed.RequestIDs[0] != id {
				t.Fatalf("requests=%d receipt=%+v err=%v", requests, failed, err)
			}
		})
	}
}

func TestNativeAuthorizationIndependentOfYolo(t *testing.T) {
	for _, action := range []permission.Action{permission.ActionAllow, permission.ActionAsk, permission.ActionDeny} {
		t.Run(string(action), func(t *testing.T) {
			a := newTestMainAgent(t, t.TempDir())
			a.tools.Register(tools.NewHostedTool(tools.BuiltinHostedToolSpecs()[tools.NameWebSearch], nil))
			a.ruleset = permission.Ruleset{{Permission: "*", Pattern: "*", Action: action}}
			turn := &Turn{ID: 1}
			for _, yolo := range []bool{false, true, false} {
				a.yoloEnabled.Store(yolo)
				policy := a.nativeRequestPolicy(turn)
				if policy == nil || policy.Permitted(tools.NameWebSearch) != (action == permission.ActionAllow) {
					t.Fatalf("YOLO=%v policy=%+v", yolo, policy)
				}
			}
		})
	}
}

func TestNativePermissionUsesRequestedToolIdentity(t *testing.T) {
	registry := tools.NewRegistry()
	registry.Register(tools.NewHostedTool(tools.HostedToolSpec{Name: "sample_tool", NativeMapping: true}, nil))
	rules := permission.Ruleset{{Permission: tools.NameWebSearch, Pattern: "*", Action: permission.ActionAllow}}
	if nativeToolPermitted(registry, rules, nil, "sample_tool") {
		t.Fatal("search permission authorized another tool")
	}
	rules = append(rules, permission.Rule{Permission: "sample_tool", Pattern: "*", Action: permission.ActionAllow})
	if !nativeToolPermitted(registry, rules, nil, "sample_tool") {
		t.Fatal("tool-specific authorization rejected")
	}
}
