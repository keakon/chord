package acpagent

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	acp "github.com/coder/acp-go-sdk"

	"github.com/keakon/chord/internal/agent"
)

func TestEventMapperMap(t *testing.T) {
	turnErr := errors.New("provider failed")
	toolStartArgs := `{"path":"internal/acpagent/server.go","limit":20}`
	toolResult := agent.ToolResultEvent{
		CallID:  "call_1",
		Name:    "shell",
		Payload: "tests passed",
		Result:  "tests passed",
		Status:  agent.ToolResultStatusSuccess,
		Diff:    "--- a/x.go\n+++ b/x.go\n",
	}

	tests := []struct {
		name        string
		ev          agent.AgentEvent
		wantUpdates []acp.SessionUpdate
		want        eventEffects
	}{
		{
			name:        "stream text maps to agent message chunk",
			ev:          agent.StreamTextEvent{Text: "hello"},
			wantUpdates: []acp.SessionUpdate{acp.UpdateAgentMessageText("hello")},
			want:        eventEffects{busy: true, streamStarted: true},
		},
		{
			name:        "stream text reports the segment identity it belongs to",
			ev:          agent.StreamTextEvent{Text: "hello", TurnID: 4, RequestSeq: 2},
			wantUpdates: []acp.SessionUpdate{acp.UpdateAgentMessageText("hello")},
			want:        eventEffects{busy: true, streamStarted: true, segment: segmentOf(4, 2)},
		},
		{
			name: "sub-agent stream text is dropped",
			ev:   agent.StreamTextEvent{Text: "hello", AgentID: "sub-1"},
		},
		{
			name: "empty stream text marks the request streaming without an update",
			ev:   agent.StreamTextEvent{},
			want: eventEffects{busy: true, streamStarted: true},
		},
		{
			name:        "thinking delta maps to agent thought chunk",
			ev:          agent.StreamThinkingDeltaEvent{Text: "weighing options"},
			wantUpdates: []acp.SessionUpdate{acp.UpdateAgentThoughtText("weighing options")},
			want:        eventEffects{busy: true, streamStarted: true},
		},
		{
			name: "thinking start opens the stream before any chunk",
			ev:   agent.ThinkingStartedEvent{TurnID: 7, RequestSeq: 3},
			want: eventEffects{busy: true, streamStarted: true, segment: segmentOf(7, 3)},
		},
		{
			name: "sub-agent thinking start is dropped",
			ev:   agent.ThinkingStartedEvent{AgentID: "sub-1"},
		},
		{
			name: "empty thinking block end carries no update",
			ev:   agent.StreamThinkingEvent{},
			want: eventEffects{busy: true, streamStarted: true},
		},
		{
			name:        "unstreamed thinking block maps to agent thought chunk",
			ev:          agent.StreamThinkingEvent{Text: "full block"},
			wantUpdates: []acp.SessionUpdate{acp.UpdateAgentThoughtText("full block")},
			want:        eventEffects{busy: true, streamStarted: true},
		},
		{
			name: "main-agent segment end drains the stream",
			ev:   agent.StreamSegmentEndedEvent{TurnID: 7, RequestSeq: 3},
			want: eventEffects{busy: true, streamDrained: true, segment: segmentOf(7, 3)},
		},
		{
			name: "sub-agent segment end is dropped",
			ev:   agent.StreamSegmentEndedEvent{AgentID: "sub-1", TurnID: 7, RequestSeq: 3},
		},
		{
			name: "rollback cannot be expressed and only marks the turn busy",
			ev:   agent.StreamRollbackEvent{Reason: "retry"},
			want: eventEffects{busy: true},
		},
		{
			name: "tool call start maps kind, locations and raw input",
			ev:   agent.ToolCallStartEvent{ID: "call_1", Name: "read", ArgsJSON: toolStartArgs},
			wantUpdates: []acp.SessionUpdate{acp.StartToolCall(
				acp.ToolCallId("call_1"),
				"Read internal/acpagent/server.go",
				acp.WithStartKind(acp.ToolKindRead),
				acp.WithStartStatus(acp.ToolCallStatusPending),
				acp.WithStartLocations([]acp.ToolCallLocation{{Path: "internal/acpagent/server.go"}}),
				acp.WithStartRawInput(json.RawMessage(toolStartArgs)),
			)},
			want: eventEffects{busy: true},
		},
		{
			name: "tool call start with partial arguments drops raw input",
			ev:   agent.ToolCallStartEvent{ID: "call_1", Name: "shell", ArgsJSON: `{"command":`},
			wantUpdates: []acp.SessionUpdate{acp.StartToolCall(
				acp.ToolCallId("call_1"),
				"Shell",
				acp.WithStartKind(acp.ToolKindExecute),
				acp.WithStartStatus(acp.ToolCallStatusPending),
			)},
			want: eventEffects{busy: true},
		},
		{
			name: "streamed arguments update raw input and title",
			ev: agent.ToolCallUpdateEvent{
				ID:                "call_1",
				Name:              "shell",
				ArgsJSON:          `{"command":"go test ./..."}`,
				ArgsStreamingDone: true,
			},
			wantUpdates: []acp.SessionUpdate{acp.UpdateToolCall(
				acp.ToolCallId("call_1"),
				acp.WithUpdateRawInput(json.RawMessage(`{"command":"go test ./..."}`)),
				acp.WithUpdateTitle("Shell go test ./..."),
			)},
			want: eventEffects{busy: true},
		},
		{
			name: "incomplete streamed arguments produce no update",
			ev:   agent.ToolCallUpdateEvent{ID: "call_1", Name: "shell", ArgsJSON: `{"comm`},
			want: eventEffects{busy: true},
		},
		{
			name: "discarded tool call closes as failed",
			ev:   agent.ToolCallDiscardEvent{ID: "call_1", Name: "shell", Reason: "not committed"},
			wantUpdates: []acp.SessionUpdate{acp.UpdateToolCall(
				acp.ToolCallId("call_1"),
				acp.WithUpdateStatus(acp.ToolCallStatusFailed),
			)},
			want: eventEffects{busy: true},
		},
		{
			name: "running tool call turns in progress",
			ev:   agent.ToolCallExecutionEvent{ID: "call_1", Name: "shell", State: agent.ToolCallExecutionStateRunning},
			wantUpdates: []acp.SessionUpdate{acp.UpdateToolCall(
				acp.ToolCallId("call_1"),
				acp.WithUpdateStatus(acp.ToolCallStatusInProgress),
			)},
			want: eventEffects{busy: true},
		},
		{
			name: "receiving tool arguments keep the card pending",
			ev:   agent.ToolCallExecutionEvent{ID: "call_1", Name: "shell", State: agent.ToolCallExecutionStateReceiving},
			want: eventEffects{busy: true},
		},
		{
			name: "progress updates the title",
			ev: agent.ToolProgressEvent{
				CallID:   "call_1",
				Name:     "shell",
				Progress: agent.ToolProgressSnapshot{Current: 42, Total: 100},
			},
			wantUpdates: []acp.SessionUpdate{acp.UpdateToolCall(
				acp.ToolCallId("call_1"),
				acp.WithUpdateTitle("Shell (42/100)"),
			)},
			want: eventEffects{busy: true},
		},
		{
			name: "successful tool result carries output and diff",
			ev:   toolResult,
			wantUpdates: []acp.SessionUpdate{acp.UpdateToolCall(
				acp.ToolCallId("call_1"),
				acp.WithUpdateStatus(acp.ToolCallStatusCompleted),
				acp.WithUpdateContent([]acp.ToolCallContent{
					acp.ToolContent(acp.TextBlock("tests passed")),
					acp.ToolContent(acp.TextBlock("--- a/x.go\n+++ b/x.go")),
				}),
				acp.WithUpdateRawOutput("tests passed"),
			)},
			want: eventEffects{busy: true},
		},
		{
			name: "cancelled tool result closes as failed",
			ev: agent.ToolResultEvent{
				CallID: "call_1",
				Name:   "shell",
				Status: agent.ToolResultStatusCancelled,
			},
			wantUpdates: []acp.SessionUpdate{acp.UpdateToolCall(
				acp.ToolCallId("call_1"),
				acp.WithUpdateStatus(acp.ToolCallStatusFailed),
			)},
			want: eventEffects{busy: true},
		},
		{
			name: "sub-agent tool result is dropped",
			ev: agent.ToolResultEvent{
				CallID:  "call_1",
				Name:    "shell",
				Status:  agent.ToolResultStatusSuccess,
				AgentID: "sub-1",
			},
		},
		{
			name: "unstreamed assistant message is forwarded",
			ev:   agent.AssistantMessageEvent{Text: "answer"},
			wantUpdates: []acp.SessionUpdate{
				acp.UpdateAgentMessageText("answer"),
			},
			want: eventEffects{busy: true, recovered: true},
		},
		{
			name: "tool-only assistant message carries no text",
			ev:   agent.AssistantMessageEvent{Text: "", ToolCalls: 2},
			want: eventEffects{busy: true, recovered: true},
		},
		{
			name: "non-silent main-agent error aborts the turn",
			ev:   agent.ErrorEvent{Err: turnErr},
			want: eventEffects{busy: true, err: turnErr},
		},
		{
			name: "silent retry error does not abort the turn",
			ev:   agent.ErrorEvent{Err: turnErr, Silent: true},
			want: eventEffects{busy: true},
		},
		{
			name: "sub-agent error is dropped",
			ev:   agent.ErrorEvent{Err: turnErr, AgentID: "sub-1"},
		},
		{
			name: "global idle settles the turn",
			ev:   agent.GlobalIdleEvent{},
			want: eventEffects{settle: true},
		},
		{
			name: "scheduling idle is ignored",
			ev:   agent.IdleEvent{},
		},
		{
			name: "activity marks the turn busy",
			ev:   agent.AgentActivityEvent{Type: agent.ActivityStreaming},
			want: eventEffects{busy: true},
		},
		{
			name: "idle activity is ignored",
			ev:   agent.AgentActivityEvent{Type: agent.ActivityIdle},
		},
		{
			name: "other main-agent events keep the turn open",
			ev:   agent.SessionSwitchStartedEvent{Kind: "new"},
			want: eventEffects{busy: true},
		},
		{
			// The window-cancel path creates a turn for the accepted message and
			// closes it as cancelled; this event is what makes the waiter see
			// that turn as work, so the global idle that follows can settle it.
			name: "turn creation marks the turn busy",
			ev:   agent.RequestCycleStartedEvent{TurnID: 3},
			want: eventEffects{busy: true},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mapper := &eventMapper{}
			updates, effects := mapper.Map(tt.ev)
			if !reflect.DeepEqual(updates, tt.wantUpdates) {
				t.Fatalf("updates = %#v, want %#v", updates, tt.wantUpdates)
			}
			if effects.busy != tt.want.busy || effects.settle != tt.want.settle || effects.recovered != tt.want.recovered ||
				effects.streamStarted != tt.want.streamStarted || effects.streamDrained != tt.want.streamDrained ||
				effects.segment != tt.want.segment {
				t.Fatalf("effects = %#v, want %#v", effects, tt.want)
			}
			if !errors.Is(effects.err, tt.want.err) {
				t.Fatalf("effects err = %v, want %v", effects.err, tt.want.err)
			}
		})
	}
}

func TestEventMapperAssistantMessageFallback(t *testing.T) {
	mapper := &eventMapper{}

	mapper.Map(agent.StreamTextEvent{Text: "streamed"})
	if !mapper.sawText {
		t.Fatal("streaming text should mark the request as covered")
	}
	updates, _ := mapper.Map(agent.AssistantMessageEvent{Text: "streamed"})
	if len(updates) != 0 {
		t.Fatalf("finalized text was already streamed, got updates %#v", updates)
	}
	if mapper.sawText {
		t.Fatal("a finalized assistant message must start a fresh request window")
	}

	updates, _ = mapper.Map(agent.AssistantMessageEvent{Text: "never streamed"})
	want := []acp.SessionUpdate{acp.UpdateAgentMessageText("never streamed")}
	if !reflect.DeepEqual(updates, want) {
		t.Fatalf("updates = %#v, want %#v", updates, want)
	}
}
