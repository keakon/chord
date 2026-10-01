package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/analytics"
	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/tools"
)

func TestHostedBackendCursorAndCallerUsage(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.ruleset = permission.Ruleset{{Permission: "*", Pattern: "*", Action: permission.ActionAllow}}
	first := &hostedScriptProvider{respond: func(int, context.Context) (*message.Response, error) {
		return nil, &llm.APIError{StatusCode: 401, Message: "invalid credential"}
	}}
	second := &hostedScriptProvider{respond: func(int, context.Context) (*message.Response, error) {
		r := hostedWebSearchResponse()
		r.Usage = &message.TokenUsage{InputTokens: 5, OutputTokens: 2}
		return r, nil
	}}
	targets := []llm.FallbackModel{newHostedTestTarget("first", hostedTestTargetOpts{typ: config.ProviderTypeMessages, modelID: "model-1", providerHosted: []string{tools.NameWebSearch}}, first), newHostedTestTarget("second", hostedTestTargetOpts{typ: config.ProviderTypeMessages, modelID: "model-2", providerHosted: []string{tools.NameWebSearch}}, second)}
	client := setHostedTestPool(a, targets...)
	if _, err := client.CompleteStream(context.Background(), []message.Message{{Role: message.RoleUser, Content: "sample"}}, nil, nil); err != nil {
		t.Fatal(err)
	}
	beforeFirst, beforeSecond := first.callCount(), second.callCount()
	_, initialCursor := client.ModelPoolSnapshot()
	if initialCursor != 1 {
		t.Fatalf("initial cursor = %d", initialCursor)
	}
	var events []analytics.UsageEvent
	a.SetUsageEventSink(func(e analytics.UsageEvent) { events = append(events, e) })
	ctx := tools.WithTurnID(context.Background(), 42)
	_, err := newTestHostedBackend(t, a, tools.ResolveHostedToolCatalog(nil)).Run(ctx, tools.NameWebSearch, map[string]any{"query": "sample"})
	if err != nil {
		t.Fatal(err)
	}
	if first.callCount() != beforeFirst || second.callCount() != beforeSecond+1 {
		t.Fatalf("calls = %d/%d", first.callCount(), second.callCount())
	}
	if len(events) != 1 || events[0].AgentID != identity.MainAgentID || events[0].AgentKind != identity.MainAgentID || events[0].TurnID != 42 || events[0].RunningModelRef != "second/model-2" {
		t.Fatalf("usage = %+v", events)
	}
	_, cursor := client.ModelPoolSnapshot()
	if cursor != 1 {
		t.Fatalf("main cursor changed to %d", cursor)
	}
}

func TestHostedBackendPauseContinuation(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.governor = newResourceGovernor(config.OrchestrationConfig{MaxActiveLLMRequests: 1})
	a.ruleset = permission.Ruleset{{Permission: "*", Pattern: "*", Action: permission.ActionAllow}}
	impl := &hostedScriptProvider{respond: func(index int, _ context.Context) (*message.Response, error) {
		if got := a.governor.snapshot(); got.LLMActive != 1 || got.ModelActive["provider/model-1"] != 1 {
			t.Errorf("continuation %d capacity = %+v, want one slot for provider/model-1", index, got)
		}
		if index == 0 {
			return &message.Response{StopReason: hostedPauseTurn, Hosted: &message.HostedObservation{Container: "container-1", Items: []json.RawMessage{json.RawMessage(`{"type":"server_tool_use","id":"call-1","name":"sample_execution","input":{"code":"print(1)"}}`)}, Calls: []message.HostedCall{{ID: "call-1", Name: sampleHostedTool, Kind: "server_tool_use", Input: json.RawMessage(`{"code":"print(1)"}`)}}}}, nil
		}
		return &message.Response{StopReason: "end_turn", Hosted: &message.HostedObservation{Items: []json.RawMessage{json.RawMessage(`{"type":"sample_execution_tool_result","tool_use_id":"call-1","content":{"stdout":"1"}}`)}, Calls: []message.HostedCall{{ID: "call-1", Result: json.RawMessage(`{"stdout":"1"}`)}}}}, nil
	}}
	setHostedTestPool(a, newHostedTestTarget("provider", hostedTestTargetOpts{typ: config.ProviderTypeMessages, modelID: "model-1", providerHosted: []string{sampleHostedTool}}, impl))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	obs, err := newTestHostedBackend(t, a, sampleHostedCatalog()).Run(ctx, sampleHostedTool, map[string]any{"code": "print(1)"})
	if err != nil {
		t.Fatal(err)
	}
	if got := a.governor.snapshot(); got.LLMActive != 0 || got.LLMQueued != 0 {
		t.Fatalf("capacity after continuation = %+v", got)
	}
	calls := impl.callsSnapshot()
	if len(calls) != 2 || len(calls[1].tuning.HostedTool.Messages) != 2 || calls[1].tuning.HostedTool.Container != "container-1" {
		t.Fatalf("calls = %+v", calls)
	}
	if obs.PendingCalls() != 0 || len(obs.Calls) != 1 || obs.Calls[0].Name != sampleHostedTool || !strings.Contains(string(obs.Calls[0].Input), "print(1)") {
		t.Fatalf("observation = %+v", obs)
	}
}

func TestHostedBackendUnsafeExecutionNeverReplays(t *testing.T) {
	for _, failure := range []string{"transport", "stream_error", "pending", "approval", "pause_limit"} {
		t.Run(failure, func(t *testing.T) {
			a := newTestMainAgent(t, t.TempDir())
			a.ruleset = permission.Ruleset{{Permission: "*", Pattern: "*", Action: permission.ActionAllow}}
			first := &hostedScriptProvider{respond: func(int, context.Context) (*message.Response, error) {
				switch failure {
				case "transport":
					return nil, fmt.Errorf("connection closed")
				case "stream_error":
					return nil, &llm.APIError{Origin: llm.APIErrorOriginSSEEvent, StatusCode: 400, Message: "execution failed"}
				case "approval":
					return &message.Response{StopReason: "stop", Hosted: &message.HostedObservation{RequiresApproval: true}}, nil
				case "pause_limit":
					return &message.Response{StopReason: hostedPauseTurn, Hosted: &message.HostedObservation{Items: []json.RawMessage{json.RawMessage(`{"type":"text","text":"waiting"}`)}}}, nil
				default:
					return &message.Response{StopReason: "stop", Hosted: &message.HostedObservation{Calls: []message.HostedCall{{ID: "call-1", Name: sampleHostedTool}}}}, nil
				}
			}}
			second := &hostedScriptProvider{}
			one := newHostedTestTarget("first", hostedTestTargetOpts{typ: config.ProviderTypeMessages, modelID: "model-1", providerHosted: []string{sampleHostedTool}}, first)
			one.ProviderConfig = llm.NewProviderConfig("first", config.ProviderConfig{Type: config.ProviderTypeMessages, Compat: &config.ProviderCompatConfig{HostedTools: new([]string{sampleHostedTool})}}, []string{"key-1", "key-2"})
			setHostedTestPool(a, one, newHostedTestTarget("second", hostedTestTargetOpts{typ: config.ProviderTypeMessages, modelID: "model-2", providerHosted: []string{sampleHostedTool}}, second))
			_, err := newTestHostedBackend(t, a, sampleHostedCatalog()).Run(context.Background(), sampleHostedTool, map[string]any{"code": "print(1)"})
			if err == nil {
				t.Fatal("wanted failure")
			}
			if failure != "approval" {
				if _, ok := errors.AsType[*llm.HostedOutcomeUnknownError](err); !ok {
					t.Fatalf("error = %T: %v", err, err)
				}
			}
			want := 1
			if failure == "pause_limit" {
				want = maxHostedContinuations + 1
			}
			if first.callCount() != want || second.callCount() != 0 {
				t.Fatalf("calls = %d/%d, want %d/0", first.callCount(), second.callCount(), want)
			}
		})
	}
}

func TestHostedBackendPermissionRevocationStopsContinuation(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.ruleset = permission.Ruleset{{Permission: "*", Pattern: "*", Action: permission.ActionAllow}}
	impl := &hostedScriptProvider{respond: func(int, context.Context) (*message.Response, error) {
		a.stateMu.Lock()
		a.ruleset = permission.Ruleset{{Permission: sampleHostedTool, Pattern: "*", Action: permission.ActionDeny}}
		a.stateMu.Unlock()
		return &message.Response{StopReason: hostedPauseTurn, Hosted: &message.HostedObservation{Items: []json.RawMessage{json.RawMessage(`{"type":"text","text":"paused"}`)}}}, nil
	}}
	setHostedTestPool(a, newHostedTestTarget("provider", hostedTestTargetOpts{typ: config.ProviderTypeMessages, modelID: "model-1", providerHosted: []string{sampleHostedTool}}, impl))
	_, err := newTestHostedBackend(t, a, sampleHostedCatalog()).Run(context.Background(), sampleHostedTool, map[string]any{"code": "print(1)"})
	if err == nil || !strings.Contains(err.Error(), "permission revoked") || impl.callCount() != 1 {
		t.Fatalf("err=%v calls=%d", err, impl.callCount())
	}
}

func TestHostedBackendApprovalStopsRetriesOnFailedStream(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	first := &hostedScriptProvider{respond: func(int, context.Context) (*message.Response, error) {
		return &message.Response{Hosted: &message.HostedObservation{RequiresApproval: true}}, fmt.Errorf("stream interrupted")
	}}
	second := &hostedScriptProvider{}
	setHostedTestPool(a, newHostedTestTarget("first", hostedTestTargetOpts{typ: config.ProviderTypeMessages, modelID: "model-1", providerHosted: []string{sampleHostedTool}}, first), newHostedTestTarget("second", hostedTestTargetOpts{typ: config.ProviderTypeMessages, modelID: "model-2", providerHosted: []string{sampleHostedTool}}, second))
	catalog := sampleHostedCatalog()
	spec := catalog[sampleHostedTool]
	spec.RetrySafe = true
	catalog[sampleHostedTool] = spec
	_, err := newTestHostedBackend(t, a, catalog).Run(context.Background(), sampleHostedTool, map[string]any{"code": "print(1)"})
	if err == nil || !strings.Contains(err.Error(), "approval") || first.callCount() != 1 || second.callCount() != 0 {
		t.Fatalf("err=%v calls=%d/%d", err, first.callCount(), second.callCount())
	}
}
