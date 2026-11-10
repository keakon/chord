package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/tools"
)

func TestToolAskPolicyPermissionDecisions(t *testing.T) {
	for _, name := range []string{tools.NameShell, tools.NameRead, tools.NameDelegate, tools.NameHandoff, tools.NameCancel, tools.NameDone, tools.NameCompactContext} {
		for _, policy := range []string{config.ConfirmAskPolicyWait, config.ConfirmAskPolicyDeny} {
			t.Run(name+"/"+policy, func(t *testing.T) {
				var events []AgentEvent
				confirms := 0
				p := toolExecutionPipeline{
					agentID:      "main-1",
					registry:     tools.NewRegistry(),
					denyToolAsks: policy == config.ConfirmAskPolicyDeny,
					currentRuleset: func() permission.Ruleset {
						return permission.Ruleset{{Permission: name, Pattern: "*", Action: permission.ActionAsk}}
					},
					emit: func(e AgentEvent) { events = append(events, e) },
					confirm: func(context.Context, string, string, []string, []string, []permission.Rule, []permission.Rule) (ConfirmResponse, error) {
						confirms++
						return ConfirmResponse{Approved: true}, nil
					},
				}
				call := message.ToolCall{ID: "call-1", Name: name, Args: json.RawMessage(`{"command":"pwd","path":"example.txt","agent_type":"worker"}`)}
				err := p.applyPermission(context.Background(), &call, &ToolExecutionResult{})
				denied := policy == config.ConfirmAskPolicyDeny && name != tools.NameDone && name != tools.NameCompactContext
				if denied {
					if !errors.Is(err, errToolAskPolicyDenied) || classifyToolError(err) != "ask_policy_denied" || confirms != 0 {
						t.Fatalf("err=%v confirms=%d", err, confirms)
					}
					if len(events) != 1 {
						t.Fatalf("events = %v, want one policy decision", events)
					}
					e, ok := events[0].(ToolAskPolicyDeniedEvent)
					if !ok || e.CallID != call.ID || e.AgentID != p.agentID || e.ToolName != name || e.Policy != policy || e.Reason != askPolicyDenyReason {
						t.Fatalf("event = %+v", events[0])
					}
					if strings.Contains(err.Error(), "rejected by user") || !strings.Contains(err.Error(), askPolicyDenyReason) {
						t.Fatalf("unexpected error: %v", err)
					}
					return
				}
				wantConfirms := 1
				if name == tools.NameDone {
					wantConfirms = 0
				}
				if err != nil || confirms != wantConfirms || len(events) != wantConfirms {
					t.Fatalf("err=%v confirms=%d events=%v", err, confirms, events)
				}
				for _, event := range events {
					if _, ok := event.(ToolCallUpdateEvent); !ok {
						t.Fatalf("approval produced unexpected event %T", event)
					}
				}
			})
		}
	}
}

func TestToolAskPolicySkipsOccupiedConfirmation(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.globalConfig = &config.Config{ConfirmAskPolicy: new(config.ConfirmAskPolicyDeny)}
	a.ruleset = permissionRuleset(t, "shell: ask")
	a.confirmFn = func(ctx context.Context, name, args string, needs, allowed []string, needsRules, allowedRules []permission.Rule) (ConfirmResponse, error) {
		return a.AwaitConfirmWithRuleContext(ctx, name, args, 0, needs, allowed, needsRules, allowedRules)
	}
	if err := a.interaction.beginConfirmFlow(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer a.interaction.endConfirmFlow()
	a.interaction.registerConfirm("req-existing", nil, confirmIdentity{ToolName: tools.NameCompactContext}, context.Background())
	defer a.interaction.unregisterConfirm("req-existing")

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	p := a.toolExecutionPipeline()
	call := message.ToolCall{ID: "call-1", Name: tools.NameShell, Args: json.RawMessage(`{"command":"pwd"}`)}
	if err := p.applyPermission(ctx, &call, &ToolExecutionResult{}); !errors.Is(err, errToolAskPolicyDenied) {
		t.Fatalf("occupied confirm blocked policy refusal: %v", err)
	}
	if len(a.interaction.confirmCh) != 1 || a.interaction.confirmCh["req-existing"] == nil {
		t.Fatal("policy refusal changed existing confirmation")
	}
	found := 0
	for _, event := range drainAgentEvents(a.outputCh) {
		switch event.(type) {
		case ToolAskPolicyDeniedEvent:
			found++
		case ConfirmRequestEvent, ConfirmResolvedEvent, NotificationEvent:
			t.Fatalf("policy refusal produced interaction event %T", event)
		}
	}
	if found != 1 {
		t.Fatalf("policy events=%d, want 1", found)
	}
}

func TestToolAskPolicySubAgentAndYolo(t *testing.T) {
	a, sub := newSubAgentWithRoleRules(t, "shell: ask\n")
	a.globalConfig = &config.Config{ConfirmAskPolicy: new(config.ConfirmAskPolicyDeny)}
	a.confirmFn = func(context.Context, string, string, []string, []string, []permission.Rule, []permission.Rule) (ConfirmResponse, error) {
		t.Fatal("confirmation called for a policy decision")
		return ConfirmResponse{}, nil
	}
	if err := subYoloApplyPermission(t, sub, tools.NameShell); !errors.Is(err, errToolAskPolicyDenied) {
		t.Fatalf("subagent error = %v", err)
	}
	events := drainAgentEvents(a.outputCh)
	if len(events) != 1 {
		t.Fatalf("subagent events = %v", events)
	}
	if event, ok := events[0].(ToolAskPolicyDeniedEvent); !ok || event.AgentID != sub.instanceID {
		t.Fatalf("subagent policy event = %+v", events[0])
	}
	a.yoloEnabled.Store(true)
	if err := subYoloApplyPermission(t, sub, tools.NameShell); err != nil {
		t.Fatalf("YOLO should precede policy: %v", err)
	}
}

func TestToolAskPolicyCancellationBeforeDecision(t *testing.T) {
	emitted := false
	p := toolExecutionPipeline{
		denyToolAsks: true,
		currentRuleset: func() permission.Ruleset {
			return permission.Ruleset{{Permission: tools.NameShell, Pattern: "*", Action: permission.ActionAsk}}
		},
		emit: func(AgentEvent) { emitted = true },
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	call := message.ToolCall{ID: "call-1", Name: tools.NameShell, Args: json.RawMessage(`{"command":"pwd"}`)}
	err := p.applyPermission(ctx, &call, &ToolExecutionResult{})
	if !errors.Is(err, context.Canceled) || emitted {
		t.Fatalf("err=%v emitted=%v", err, emitted)
	}
}

func TestToolAskPolicyDoesNotBecomeUserFeedback(t *testing.T) {
	text := wrapToolAskPolicyDenied(tools.NameShell).Error()
	if _, ok := extractToolRejectedByUserReason(text); ok {
		t.Fatal("policy decision became user rejection")
	}
	if _, ok := extractUserDoneRejectedReason(text); ok {
		t.Fatal("policy decision became Done rejection")
	}
}

func TestToolAskPolicyContinuesAfterDeniedCall(t *testing.T) {
	a, provider := scenarioAgent(t,
		scriptedStreamCall{resp: &message.Response{ToolCalls: []message.ToolCall{{ID: "call-denied", Name: "sample_denied", Args: json.RawMessage(`{"value":"sample"}`)}}, StopReason: "tool_use"}},
		scriptedStreamCall{resp: &message.Response{ToolCalls: []message.ToolCall{{ID: "call-allowed", Name: "sample_allowed", Args: json.RawMessage(`{"value":"sample"}`)}}, StopReason: "tool_use"}},
		scriptedStreamCall{resp: &message.Response{Content: "complete", StopReason: "end_turn"}},
	)
	a.globalConfig.ConfirmAskPolicy = new(config.ConfirmAskPolicyDeny)
	denied := &scenarioTool{name: "sample_denied"}
	allowed := &scenarioTool{name: "sample_allowed"}
	a.tools.Register(denied)
	a.tools.Register(allowed)
	a.ruleset = permissionRuleset(t, "sample_denied: ask\nsample_allowed: allow")
	a.confirmFn = func(context.Context, string, string, []string, []string, []permission.Rule, []permission.Rule) (ConfirmResponse, error) {
		t.Error("policy denial reached confirmation")
		return ConfirmResponse{}, nil
	}
	startMainAgentLoopForTest(t, a)
	a.SendUserMessageWithReceipt("run sample", "input-1")
	events := collectAgentEventsUntil(t, a.Events(), func(events []AgentEvent) bool {
		started := false
		for _, event := range events {
			if _, ok := event.(RequestCycleStartedEvent); ok {
				started = true
			}
			if _, ok := event.(GlobalIdleEvent); ok && started {
				return true
			}
		}
		return false
	})
	requests, _ := provider.snapshot()
	if len(requests) != 3 || denied.calls.Load() != 0 || allowed.calls.Load() != 1 {
		t.Fatalf("requests=%d denied executions=%d allowed executions=%d", len(requests), denied.calls.Load(), allowed.calls.Load())
	}
	found := false
	for _, msg := range requests[1] {
		if msg.ToolCallID == "call-denied" {
			found = msg.ToolStatus == message.ToolStatusError && strings.Contains(msg.Content, askPolicyDenyReason)
		}
	}
	if !found {
		t.Fatal("model continuation missed policy denial result")
	}
	decisions := 0
	for _, event := range events {
		switch event := event.(type) {
		case ToolAskPolicyDeniedEvent:
			decisions++
			if event.AgentID != identity.MainAgentID || event.CallID != "call-denied" {
				t.Fatalf("policy decision identity = %+v", event)
			}
		case ConfirmRequestEvent, ConfirmResolvedEvent, NotificationEvent:
			t.Fatalf("policy denial produced interaction event %T", event)
		}
	}
	if decisions != 1 {
		t.Fatalf("policy decisions=%d, want 1", decisions)
	}
	assertScenarioPersistence(t, a)
}

func TestToolAskPolicyCancellationAfterDecision(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.globalConfig.ConfirmAskPolicy = new(config.ConfirmAskPolicyDeny)
	a.ruleset = permissionRuleset(t, "sample_tool: ask")
	tool := &scenarioTool{name: "sample_tool"}
	a.tools.Register(tool)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := a.toolExecutionPipeline()
	decisions := 0
	p.emit = func(event AgentEvent) {
		if _, ok := event.(ToolAskPolicyDeniedEvent); !ok {
			t.Fatalf("unexpected event %T", event)
		}
		decisions++
		cancel()
	}
	p.confirm = func(context.Context, string, string, []string, []string, []permission.Rule, []permission.Rule) (ConfirmResponse, error) {
		t.Fatal("policy denial reached confirmation")
		return ConfirmResponse{}, nil
	}
	result, err := p.execute(ctx, message.ToolCall{ID: "call-1", Name: tool.Name(), Args: json.RawMessage(`{"value":"sample"}`)}, true)
	if !errors.Is(err, errToolAskPolicyDenied) || decisions != 1 || result.ExecutionStarted || tool.calls.Load() != 0 {
		t.Fatalf("err=%v decisions=%d started=%v executions=%d", err, decisions, result.ExecutionStarted, tool.calls.Load())
	}
	if len(a.interaction.confirmCh) != 0 {
		t.Fatal("policy denial left a confirmation waiter")
	}
}

func TestToolAskPolicyAgentConfigOverrides(t *testing.T) {
	for _, tt := range []struct {
		name    string
		global  *string
		project *config.Config
		deny    bool
	}{
		{name: "default"},
		{name: "global", global: new(config.ConfirmAskPolicyDeny), deny: true},
		{name: "inherit", global: new(config.ConfirmAskPolicyDeny), project: &config.Config{}, deny: true},
		{name: "project deny", global: new(config.ConfirmAskPolicyWait), project: &config.Config{ConfirmAskPolicy: new(config.ConfirmAskPolicyDeny)}, deny: true},
		{name: "project wait", global: new(config.ConfirmAskPolicyDeny), project: &config.Config{ConfirmAskPolicy: new(config.ConfirmAskPolicyWait)}},
		{name: "project empty", global: new(config.ConfirmAskPolicyDeny), project: &config.Config{ConfirmAskPolicy: new("")}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			a, sub := newSubAgentWithRoleRules(t, "shell: ask\n")
			a.globalConfig = &config.Config{ConfirmAskPolicy: tt.global}
			a.projectConfig = tt.project
			if a.toolExecutionPipeline().denyToolAsks != tt.deny || sub.toolExecutionPipeline().denyToolAsks != tt.deny {
				t.Fatalf("main/sub policy differs from effective config: want deny=%v", tt.deny)
			}
		})
	}
}
