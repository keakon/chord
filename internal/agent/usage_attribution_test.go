package agent

import (
	"context"
	"testing"

	"github.com/keakon/chord/internal/analytics"
	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func TestNativeUsageRecorderCapturesTaskAttribution(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	var events []analytics.UsageEvent
	a.SetUsageEventSink(func(event analytics.UsageEvent) { events = append(events, event) })

	mainRecorder := a.nativeUsageRecorder(usageAttribution{
		AgentID: identity.MainAgentID, AgentKind: identity.MainAgentID, AgentName: "builder",
		Purpose: analytics.UsagePurposeChat, TurnID: 3,
	})
	mainRecorder(llm.NativeRequestRecord{Target: "provider/model-1"}, &message.Response{Usage: &message.TokenUsage{InputTokens: 10, OutputTokens: 2}}, nil)

	subRecorder := a.nativeUsageRecorder(usageAttribution{
		AgentID: "worker-1", AgentKind: agentKindSub, AgentName: "worker", TaskID: "task-1",
		Purpose: analytics.UsagePurposeChat, TurnID: 4,
	})
	subRecorder(llm.NativeRequestRecord{Target: "provider/model-1"}, &message.Response{Usage: &message.TokenUsage{InputTokens: 5, OutputTokens: 1}}, nil)

	if len(events) != 2 {
		t.Fatalf("events = %d, want 2", len(events))
	}
	mainEvent := events[0]
	if mainEvent.AgentID != identity.MainAgentID || mainEvent.TaskID != "" || mainEvent.Purpose != analytics.UsagePurposeChat ||
		mainEvent.TurnID != 3 || mainEvent.RunningModelRef != "provider/model-1" {
		t.Fatalf("main native event = %+v", mainEvent)
	}
	subEvent := events[1]
	if subEvent.AgentID != "worker-1" || subEvent.AgentKind != agentKindSub || subEvent.AgentName != "worker" ||
		subEvent.TaskID != "task-1" || subEvent.Purpose != analytics.UsagePurposeChat || subEvent.TurnID != 4 {
		t.Fatalf("sub native event = %+v", subEvent)
	}
}

func TestHostedSubCallerUsageCapturesTaskID(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	var events []analytics.UsageEvent
	a.SetUsageEventSink(func(event analytics.UsageEvent) { events = append(events, event) })
	backend := newTestHostedBackend(t, a, tools.ResolveHostedToolCatalog(nil)).(*hostedBackend)

	sub := &SubAgent{instanceID: "worker-1", taskID: "task-1", agentDefName: "worker"}
	caller := &hostedCaller{id: "worker-1", sub: sub, client: a.llmClient}
	backend.recordAttemptUsage(context.Background(), caller, a.llmClient, tools.NameWebSearch, hostedWireAttempt{
		response: &message.Response{Usage: &message.TokenUsage{InputTokens: 12, OutputTokens: 3}},
	})

	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	event := events[0]
	if event.AgentID != "worker-1" || event.AgentKind != agentKindSub || event.AgentName != "worker" {
		t.Fatalf("hosted attribution = %+v", event)
	}
	if event.TaskID != "task-1" {
		t.Fatalf("hosted task id = %q, want task-1", event.TaskID)
	}
	if event.Purpose != tools.NameWebSearch {
		t.Fatalf("hosted purpose = %q, want %q", event.Purpose, tools.NameWebSearch)
	}
	stats := a.usageTracker.SessionStats()
	if stats.ByTask["task-1"] == nil || stats.ByTask["task-1"].LLMCalls != 1 {
		t.Fatalf("task partition = %+v", stats.ByTask)
	}
	if stats.Untracked != nil {
		t.Fatalf("hosted sub call must not be untracked: %+v", stats.Untracked)
	}
}
