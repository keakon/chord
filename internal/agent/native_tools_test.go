package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/keakon/chord/internal/analytics"
	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/hook"
	"github.com/keakon/chord/internal/identity"
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

func TestLateNativeReceiptUsageStaysInOriginalSession(t *testing.T) {
	for _, kind := range []string{identity.MainAgentID, agentKindSub} {
		t.Run(kind, func(t *testing.T) {
			a := newTestMainAgent(t, t.TempDir())
			original := a.SessionDir()
			var policy *llm.NativeToolPolicy
			var turn *Turn
			agentID, agentName, taskID, journalAgentID := identity.MainAgentID, a.currentAgentName(), "", identity.MainAgentID
			if kind == agentKindSub {
				sub := newControllableTestSubAgent(t, a, "task-1")
				turn = sub.newTurn()
				agentID, agentName, taskID, journalAgentID = sub.instanceID, sub.agentDefName, sub.taskID, sub.taskID
				policy = sub.nativeRequestPolicy(turn, a.walltime.captureAt(agentID, agentName, turn.ID))
			} else {
				a.newTurn("")
				turn = a.turn
				policy = a.nativeRequestPolicy(turn, a.walltime.captureAt(agentID, agentName, turn.ID))
			}
			const selected, running = "sample/test-model", "sample/test-model@high"
			const firstOutput = int64(40 * time.Millisecond)
			id, err := policy.Begin(turn.Ctx, llm.NativeRequestRecord{Target: selected, RunningModelRef: running})
			if err != nil {
				t.Fatal(err)
			}
			turn.Cancel()
			a.resetSessionRuntimeState()
			a.installSessionTarget(t.TempDir())
			if err := policy.Finish(id, message.NativeRequestUnknown, &message.Response{FirstOutputNs: firstOutput}, context.Canceled); err != nil {
				t.Fatal(err)
			}

			restored := &loadedSessionState{}
			_, count := a.restoreSessionEvidence(restored, original)
			stats := restored.UsageStats
			byAgent := stats.ByAgent[agentID]
			if !restored.UsageScanned || count != 1 || stats.LLMCalls != 1 ||
				byAgent == nil || byAgent.LLMCalls != 1 {
				t.Fatalf("original session: events=%d, stats=%+v, agent=%+v", count, stats, byAgent)
			}
			want := analytics.ModelStats{Calls: 1, FirstOutputTotalNs: firstOutput, FirstOutputSamples: 1}
			if model := byAgent.ByModel[running]; model == nil || *model != want {
				t.Fatalf("original session %s stats = %+v; want one latency sample and no throughput", running, model)
			}
			if taskID != "" {
				task := stats.ByTask[taskID]
				if task == nil || task.LLMCalls != 1 || task.ByModel[running] == nil || *task.ByModel[running] != want {
					t.Fatalf("original session task stats = %+v", task)
				}
			}
			journal := recovery.NativeRequestJournal{SessionDir: original, AgentID: journalAgentID}
			receipt, err := journal.Check()
			if err == nil || receipt == nil || !receipt.OutcomeUnknown ||
				len(receipt.RequestIDs) != 1 || receipt.RequestIDs[0] != id {
				t.Fatalf("original journal = %+v, err = %v", receipt, err)
			}

			a.SetUsageEventSink(func(event analytics.UsageEvent) {
				t.Errorf("current session received old usage: %+v", event)
			})
			payload := waitForAgentEventType(t, a.eventCh, eventRequestUsage).Payload.(requestUsagePayload)
			event := payload.event
			if event.AgentID != agentID || event.AgentKind != kind || event.AgentName != agentName || event.TurnID != turn.ID ||
				event.TaskID != taskID || event.SelectedModelRef != selected || event.RunningModelRef != running {
				t.Fatalf("late native usage attribution = %+v", event)
			}
			a.handleRequestUsage(payload)
			if got := a.usageTracker.SessionStats(); got.LLMCalls != 0 {
				t.Fatalf("current tracker contains old usage: %+v", got)
			}
			_, count, err = a.usageLedger.BuildSessionStats()
			if err != nil || count != 0 {
				t.Fatalf("current ledger: events=%d, err=%v", count, err)
			}
		})
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
	t.Parallel()
	for _, action := range []permission.Action{permission.ActionAllow, permission.ActionAsk, permission.ActionDeny} {
		t.Run(string(action), func(t *testing.T) {
			a := newTestMainAgent(t, t.TempDir())
			a.tools.Register(tools.NewHostedTool(tools.BuiltinHostedToolSpecs()[tools.NameWebSearch], nil))
			a.ruleset = permission.Ruleset{{Permission: "*", Pattern: "*", Action: action}}
			turn := &Turn{ID: 1}
			for _, yolo := range []bool{false, true, false} {
				a.yoloEnabled.Store(yolo)
				policy := a.nativeRequestPolicy(turn, nil)
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
