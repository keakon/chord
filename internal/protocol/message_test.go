package protocol

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/message"
)

// ---------------------------------------------------------------------------
// Envelope helpers
// ---------------------------------------------------------------------------

func TestNewUUID(t *testing.T) {
	ids := make(map[string]struct{}, 1000)
	for range 1000 {
		id, err := newUUID()
		if err != nil {
			t.Fatalf("newUUID error: %v", err)
		}
		if len(id) != 36 { // 8-4-4-4-12
			t.Fatalf("bad UUID length: %q", id)
		}
		if _, dup := ids[id]; dup {
			t.Fatalf("duplicate UUID: %s", id)
		}
		ids[id] = struct{}{}
		// version nibble
		if id[14] != '4' {
			t.Fatalf("expected version 4 nibble, got %c in %s", id[14], id)
		}
		// variant nibble must be 8, 9, a, or b
		v := id[19]
		if v != '8' && v != '9' && v != 'a' && v != 'b' {
			t.Fatalf("expected variant nibble in [89ab], got %c in %s", v, id)
		}
	}
}

func TestNewEnvelope_WithPayload(t *testing.T) {
	env, err := NewEnvelope(TypeStreamText, StreamTextPayload{Text: "hello", AgentID: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if env.Type != TypeStreamText {
		t.Fatalf("type = %q, want %q", env.Type, TypeStreamText)
	}
	if env.ID == "" {
		t.Fatal("expected non-empty ID")
	}
	if len(env.Payload) == 0 {
		t.Fatal("expected non-empty payload")
	}
	// Round-trip the payload.
	p, err := ParsePayload[StreamTextPayload](env)
	if err != nil {
		t.Fatal(err)
	}
	if p.Text != "hello" || p.AgentID != "main" {
		t.Fatalf("payload mismatch: %+v", p)
	}
}

func TestNewEnvelope_NilPayload(t *testing.T) {
	env, err := NewEnvelope(TypeIdle, nil)
	if err != nil {
		t.Fatal(err)
	}
	if env.Payload != nil {
		t.Fatalf("expected nil payload, got %s", env.Payload)
	}
}

func TestParsePayload_Empty(t *testing.T) {
	env := &Envelope{ID: "x", Type: TypeStreamText}
	_, err := ParsePayload[StreamTextPayload](env)
	if err == nil {
		t.Fatal("expected error for empty payload")
	}
}

func TestMarshalUnmarshalEnvelope(t *testing.T) {
	orig, err := NewEnvelope(TypeStreamText, StreamTextPayload{
		Text:    "chunk",
		AgentID: "agent-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	orig.Seq = 42

	data, err := MarshalEnvelope(orig)
	if err != nil {
		t.Fatal(err)
	}

	got, err := UnmarshalEnvelope(data)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != orig.ID {
		t.Fatalf("ID mismatch: %q vs %q", got.ID, orig.ID)
	}
	if got.Type != orig.Type {
		t.Fatalf("Type mismatch: %q vs %q", got.Type, orig.Type)
	}
	if got.Seq != orig.Seq {
		t.Fatalf("Seq mismatch: %d vs %d", got.Seq, orig.Seq)
	}

	p, err := ParsePayload[StreamTextPayload](got)
	if err != nil {
		t.Fatal(err)
	}
	if p.Text != "chunk" || p.AgentID != "agent-1" {
		t.Fatalf("payload mismatch: %+v", p)
	}
}

func TestUnmarshalEnvelope_InvalidJSON(t *testing.T) {
	_, err := UnmarshalEnvelope([]byte("not json"))
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

// ---------------------------------------------------------------------------
// JSON wire-format spot checks
// ---------------------------------------------------------------------------

func TestEnvelopeJSON_SeqOmitted(t *testing.T) {
	env, _ := NewEnvelope(TypeIdle, nil)
	data, _ := MarshalEnvelope(env)
	if strings.Contains(string(data), `"seq"`) {
		t.Fatalf("seq should be omitted when zero, got: %s", data)
	}
}

func TestEnvelopeJSON_SessionIDOmitted(t *testing.T) {
	env, _ := NewEnvelope(TypeIdle, nil)
	data, _ := MarshalEnvelope(env)
	if strings.Contains(string(data), `"session_id"`) {
		t.Fatalf("session_id should be omitted when empty, got: %s", data)
	}
}

func TestEnvelopeJSON_SessionIDPresent(t *testing.T) {
	env, _ := NewEnvelope(TypeStreamText, StreamTextPayload{Text: "hi"})
	env.SessionID = "session-123"
	data, _ := MarshalEnvelope(env)
	if !strings.Contains(string(data), `"session_id":"session-123"`) {
		t.Fatalf("session_id should be present, got: %s", data)
	}
}

// ---------------------------------------------------------------------------
// All payload types round-trip
// ---------------------------------------------------------------------------

func TestAllEventPayloads(t *testing.T) {
	cases := []struct {
		typ     string
		payload any
	}{
		{TypeStreamText, StreamTextPayload{Text: "hi", AgentID: ""}},
		{TypeStreamThinking, StreamThinkingPayload{Text: "hmm", AgentID: "agent-1"}},
		{TypeStreamThinkingDelta, StreamThinkingDeltaPayload{Text: "delta", AgentID: ""}},
		{TypeStreamRollback, StreamRollbackPayload{Reason: "retry", AgentID: ""}},
		{TypeToolCallStart, ToolCallStartPayload{CallID: "c1", Name: "Read", ArgsJSON: `{}`, AgentID: ""}},
		{TypeToolCallUpdate, ToolCallUpdatePayload{CallID: "c1", Name: "Read", ArgsJSON: `{"path":"README.md"}`, ArgsStreamingDone: true, AgentID: "agent-1"}},
		{TypeToolResult, ToolResultPayload{CallID: "c1", Name: "Read", ArgsJSON: `{}`, Result: "ok", Status: string(agent.ToolResultStatusSuccess), AgentID: ""}},
		{TypeError, ErrorPayload{Message: "boom", AgentID: ""}},
		{TypeIdle, nil},
		{TypePlanComplete, PlanCompletePayload{Summary: "done", PlanPath: "/tmp/plan.md"}},
		{TypeStats, StatsPayload{FormattedStats: "tokens: 100"}},
		{TypeInfo, InfoPayload{Message: "exported"}},
		{TypeAgentDone, AgentDonePayload{AgentID: "agent-1", TaskID: "3", Summary: "done"}},
		{TypeAgentStatus, AgentStatusPayload{AgentID: "agent-1", Status: "running", Message: "working"}},
		{TypeModelSelectRequest, nil},
		{TypeSessionSelectRequest, nil},
		{TypeSessionSwitchStarted, SessionSwitchStartedPayload{Kind: "resume", SessionID: "123"}},
		{TypeSessionRestored, nil},
		{TypeConfirmRequest, ConfirmRequestPayload{ToolName: "Bash", ArgsJSON: `{"cmd":"rm"}`, TimeoutMS: 3000}},
		{TypeQuestionRequest, QuestionRequestPayload{
			ToolName:      "Question",
			Header:        "Confirm",
			Question:      "Continue?",
			Options:       []string{"yes", "no"},
			OptionDetails: []string{"Continue execution", "Cancel execution"},
			DefaultAnswer: "yes",
			TimeoutMS:     3000,
		}},
		{TypeToast, ToastPayload{Message: "saved", Level: "info"}},
	}
	for _, tc := range cases {
		env, err := NewEnvelope(tc.typ, tc.payload)
		if err != nil {
			t.Fatalf("[%s] NewEnvelope: %v", tc.typ, err)
		}
		data, err := MarshalEnvelope(env)
		if err != nil {
			t.Fatalf("[%s] Marshal: %v", tc.typ, err)
		}
		got, err := UnmarshalEnvelope(data)
		if err != nil {
			t.Fatalf("[%s] Unmarshal: %v", tc.typ, err)
		}
		if got.Type != tc.typ {
			t.Fatalf("[%s] type mismatch: %q", tc.typ, got.Type)
		}
	}
}

// ---------------------------------------------------------------------------
// FromAgentEvent converter
// ---------------------------------------------------------------------------

func TestFromAgentEvent_AllTypes(t *testing.T) {
	events := []struct {
		name     string
		event    agent.AgentEvent
		wantType string
	}{
		{"StreamText", agent.StreamTextEvent{Text: "hi", AgentID: "a1"}, TypeStreamText},
		{"StreamThinking", agent.StreamThinkingEvent{Text: "think", AgentID: ""}, TypeStreamThinking},
		{"StreamThinkingDelta", agent.StreamThinkingDeltaEvent{Text: "delta", AgentID: ""}, TypeStreamThinkingDelta},
		{"StreamRollback", agent.StreamRollbackEvent{Reason: "retry", AgentID: ""}, TypeStreamRollback},
		{"ToolCallStart", agent.ToolCallStartEvent{ID: "c1", Name: "Bash", ArgsJSON: `{}`, AgentID: ""}, TypeToolCallStart},
		{"ToolCallUpdate", agent.ToolCallUpdateEvent{ID: "c1", Name: "Bash", ArgsJSON: `{"command":"pwd"}`, ArgsStreamingDone: true, AgentID: ""}, TypeToolCallUpdate},
		{"ToolCallExecution", agent.ToolCallExecutionEvent{ID: "c1", Name: "Bash", ArgsJSON: `{}`, State: agent.ToolCallExecutionStateQueued, AgentID: ""}, TypeToolCallExecution},
		{"ToolResult", agent.ToolResultEvent{CallID: "c1", Name: "Bash", ArgsJSON: `{}`, Result: "ok", Status: agent.ToolResultStatusSuccess, AgentID: ""}, TypeToolResult},
		{"Error", agent.ErrorEvent{Err: errors.New("oops"), AgentID: "a2"}, TypeError},
		{"ErrorNil", agent.ErrorEvent{Err: nil, AgentID: ""}, TypeError},
		{"Idle", agent.IdleEvent{}, TypeIdle},
		{"PlanComplete", agent.HandoffEvent{PlanPath: "p"}, TypePlanComplete},
		{"Info", agent.InfoEvent{Message: "m"}, TypeInfo},
		{"BackgroundObjectFinished", agent.SpawnFinishedEvent{BackgroundID: "job-1", Kind: "job", Description: "Run build", MaxRuntimeSec: 300, Message: "done"}, TypeBackgroundObjectFinished},
		{"AgentDone", agent.AgentDoneEvent{AgentID: "a1", TaskID: "t1", Summary: "done"}, TypeAgentDone},
		{"AgentStatus", agent.AgentStatusEvent{AgentID: "a1", Status: "running", Message: "m"}, TypeAgentStatus},
		{"Activity", agent.AgentActivityEvent{AgentID: "main", Type: agent.ActivityConnecting, Detail: ""}, TypeActivity},
		{"ModelSelect", agent.ModelSelectEvent{}, TypeModelSelectRequest},
		{"SessionSelect", agent.SessionSelectEvent{}, TypeSessionSelectRequest},
		{"SessionSwitchStarted", agent.SessionSwitchStartedEvent{Kind: "resume", SessionID: "123"}, TypeSessionSwitchStarted},
		{"SessionRestored", agent.SessionRestoredEvent{}, TypeSessionRestored},
		{"ConfirmRequest", agent.ConfirmRequestEvent{ToolName: "Bash", ArgsJSON: `{}`, RequestID: "r1"}, TypeConfirmRequest},
		{"QuestionRequest", agent.QuestionRequestEvent{
			ToolName:      "Question",
			Header:        "Proceed",
			Question:      "Proceed?",
			Options:       []string{"yes"},
			OptionDetails: []string{"Continue to the next step"},
			RequestID:     "r2",
		}, TypeQuestionRequest},
	}

	for _, tc := range events {
		t.Run(tc.name, func(t *testing.T) {
			env, err := FromAgentEvent(tc.event, 99)
			if err != nil {
				t.Fatalf("FromAgentEvent(%s): %v", tc.name, err)
			}
			if env.Type != tc.wantType {
				t.Fatalf("type = %q, want %q", env.Type, tc.wantType)
			}
			if env.Seq != 99 {
				t.Fatalf("seq = %d, want 99", env.Seq)
			}
			if env.ID == "" {
				t.Fatal("expected non-empty ID")
			}
		})
	}
}

func TestFromAgentEvent_StreamTextPayload(t *testing.T) {
	env, err := FromAgentEvent(agent.StreamTextEvent{Text: "hello", AgentID: "a1"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	p, err := ParsePayload[StreamTextPayload](env)
	if err != nil {
		t.Fatal(err)
	}
	if p.Text != "hello" || p.AgentID != "a1" {
		t.Fatalf("payload mismatch: %+v", p)
	}
}

func TestFromAgentEvent_ToolCallUpdatePayload(t *testing.T) {
	env, err := FromAgentEvent(agent.ToolCallUpdateEvent{
		ID:                "c1",
		Name:              "Bash",
		ArgsJSON:          `{"command":"echo hi"}`,
		ArgsStreamingDone: true,
		AgentID:           "a2",
	}, 4)
	if err != nil {
		t.Fatal(err)
	}
	p, err := ParsePayload[ToolCallUpdatePayload](env)
	if err != nil {
		t.Fatal(err)
	}
	if p.CallID != "c1" || p.Name != "Bash" || !p.ArgsStreamingDone || p.AgentID != "a2" {
		t.Fatalf("payload mismatch: %+v", p)
	}
	got, err := ToAgentEvent(env)
	if err != nil {
		t.Fatal(err)
	}
	ev, ok := got.(agent.ToolCallUpdateEvent)
	if !ok {
		t.Fatalf("unexpected event type %T", got)
	}
	if !ev.ArgsStreamingDone {
		t.Fatal("expected ArgsStreamingDone after round-trip")
	}
}

func TestFromAgentEvent_ToolCallExecutionPayload(t *testing.T) {
	env, err := FromAgentEvent(agent.ToolCallExecutionEvent{
		ID:       "c1",
		Name:     "Bash",
		ArgsJSON: `{"command":"echo hi"}`,
		State:    agent.ToolCallExecutionStateQueued,
		AgentID:  "a2",
	}, 4)
	if err != nil {
		t.Fatal(err)
	}
	p, err := ParsePayload[ToolCallExecutionPayload](env)
	if err != nil {
		t.Fatal(err)
	}
	if p.CallID != "c1" || p.Name != "Bash" || p.State != string(agent.ToolCallExecutionStateQueued) || p.AgentID != "a2" {
		t.Fatalf("payload mismatch: %+v", p)
	}
	got, err := ToAgentEvent(env)
	if err != nil {
		t.Fatal(err)
	}
	ev, ok := got.(agent.ToolCallExecutionEvent)
	if !ok {
		t.Fatalf("unexpected event type %T", got)
	}
	if ev.State != agent.ToolCallExecutionStateQueued {
		t.Fatalf("state = %q, want %q", ev.State, agent.ToolCallExecutionStateQueued)
	}
}

func TestFromAgentEvent_ToolResultPayload(t *testing.T) {
	env, err := FromAgentEvent(agent.ToolResultEvent{
		CallID:   "c1",
		Name:     "Read",
		ArgsJSON: `{"path":"/tmp"}`,
		Audit: &message.ToolArgsAudit{
			OriginalArgsJSON:  `{"path":"/orig"}`,
			EffectiveArgsJSON: `{"path":"/tmp"}`,
			UserModified:      true,
		},
		Result:  "contents",
		Status:  agent.ToolResultStatusError,
		AgentID: "a2",
	}, 5)
	if err != nil {
		t.Fatal(err)
	}
	p, err := ParsePayload[ToolResultPayload](env)
	if err != nil {
		t.Fatal(err)
	}
	if p.CallID != "c1" || p.Name != "Read" || p.Status != string(agent.ToolResultStatusError) || p.AgentID != "a2" {
		t.Fatalf("payload mismatch: %+v", p)
	}
	if p.Audit == nil || !p.Audit.UserModified || p.Audit.OriginalArgsJSON != `{"path":"/orig"}` {
		t.Fatalf("payload audit mismatch: %+v", p.Audit)
	}
}

func TestFromAgentEvent_BackgroundObjectFinishedPayload(t *testing.T) {
	env, err := FromAgentEvent(agent.SpawnFinishedEvent{BackgroundID: "job-1", AgentID: "a1", Kind: "job", Status: "finished", Command: "sleep 1", Description: "Run build", MaxRuntimeSec: 300, Message: "done"}, 6)
	if err != nil {
		t.Fatal(err)
	}
	p, err := ParsePayload[BackgroundObjectFinishedPayload](env)
	if err != nil {
		t.Fatal(err)
	}
	if p.EffectiveID() != "job-1" || p.AgentID != "a1" || p.Kind != "job" || p.Status != "finished" || p.Command != "sleep 1" || p.Description != "Run build" || p.MaxRuntimeSec != 300 || p.Message != "done" {
		t.Fatalf("payload mismatch: %+v", p)
	}
	got, err := ToAgentEvent(env)
	if err != nil {
		t.Fatal(err)
	}
	ev, ok := got.(agent.SpawnFinishedEvent)
	if !ok {
		t.Fatalf("unexpected event type %T", got)
	}
	if ev.MaxRuntimeSec != 300 || ev.Kind != "job" || ev.Description != "Run build" {
		t.Fatalf("round-trip payload mismatch: %+v", ev)
	}
}

func TestFromAgentEvent_ErrorPayload(t *testing.T) {
	env, err := FromAgentEvent(agent.ErrorEvent{Err: errors.New("fail"), AgentID: "x"}, 10)
	if err != nil {
		t.Fatal(err)
	}
	p, err := ParsePayload[ErrorPayload](env)
	if err != nil {
		t.Fatal(err)
	}
	if p.Message != "fail" || p.AgentID != "x" {
		t.Fatalf("payload mismatch: %+v", p)
	}
}

func TestFromAgentEvent_ErrorNilErr(t *testing.T) {
	env, err := FromAgentEvent(agent.ErrorEvent{Err: nil, AgentID: ""}, 11)
	if err != nil {
		t.Fatal(err)
	}
	p, err := ParsePayload[ErrorPayload](env)
	if err != nil {
		t.Fatal(err)
	}
	if p.Message != "" {
		t.Fatalf("expected empty message for nil error, got %q", p.Message)
	}
}

func TestFromAgentEvent_IdleNoPayload(t *testing.T) {
	env, err := FromAgentEvent(agent.IdleEvent{}, 20)
	if err != nil {
		t.Fatal(err)
	}
	if env.Payload != nil {
		t.Fatalf("expected nil payload for idle, got %s", env.Payload)
	}
}

// NOTE: The unknown-event-type branch (default case in FromAgentEvent) cannot
// be tested from outside the agent package because AgentEvent.agentEvent() is
// unexported.  That branch is a defensive guard for future event types.

func TestFromAgentEvent_ThinkingStartedIsTUIOnly(t *testing.T) {
	_, err := FromAgentEvent(agent.ThinkingStartedEvent{}, 1)
	if err == nil {
		t.Fatal("expected ErrTUIOnlyEvent for ThinkingStartedEvent")
	}
	if !errors.Is(err, ErrTUIOnlyEvent) {
		t.Fatalf("expected ErrTUIOnlyEvent, got: %v", err)
	}
}

func TestFromAgentEvent_ToolProgressIsTUIOnly(t *testing.T) {
	_, err := FromAgentEvent(agent.ToolProgressEvent{
		CallID:  "c1",
		Name:    "Delete",
		AgentID: "a1",
		Progress: agent.ToolProgressSnapshot{
			Label:   "paths",
			Current: 2,
			Total:   5,
		},
	}, 1)
	if err == nil {
		t.Fatal("expected ErrTUIOnlyEvent for ToolProgressEvent")
	}
	if !errors.Is(err, ErrTUIOnlyEvent) {
		t.Fatalf("expected ErrTUIOnlyEvent, got: %v", err)
	}
}

func TestFromAgentEvent_StreamThinkingDeltaPayload(t *testing.T) {
	env, err := FromAgentEvent(agent.StreamThinkingDeltaEvent{Text: "think delta", AgentID: "a1"}, 7)
	if err != nil {
		t.Fatal(err)
	}
	p, err := ParsePayload[StreamThinkingDeltaPayload](env)
	if err != nil {
		t.Fatal(err)
	}
	if p.Text != "think delta" || p.AgentID != "a1" {
		t.Fatalf("payload mismatch: %+v", p)
	}
}

func TestConfirmAndQuestionRequestTimeoutRoundTrip(t *testing.T) {
	t.Run("confirm", func(t *testing.T) {
		ev := agent.ConfirmRequestEvent{
			ToolName:       "Bash",
			ArgsJSON:       `{"cmd":"ls"}`,
			RequestID:      "req-1",
			Timeout:        3 * time.Second,
			NeedsApproval:  []string{"a.go", "b.go"},
			AlreadyAllowed: []string{"c.go"},
		}
		env, err := FromAgentEvent(ev, 12)
		if err != nil {
			t.Fatal(err)
		}
		payload, err := ParsePayload[ConfirmRequestPayload](env)
		if err != nil {
			t.Fatal(err)
		}
		if payload.TimeoutMS != 3000 {
			t.Fatalf("timeout_ms = %d, want 3000", payload.TimeoutMS)
		}
		if got, want := payload.NeedsApproval, []string{"a.go", "b.go"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("needs_approval = %#v, want %#v", got, want)
		}
		if got, want := payload.AlreadyAllowed, []string{"c.go"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("already_allowed = %#v, want %#v", got, want)
		}
		got, err := ToAgentEvent(env)
		if err != nil {
			t.Fatal(err)
		}
		confirm, ok := got.(agent.ConfirmRequestEvent)
		if !ok {
			t.Fatalf("unexpected event type %T", got)
		}
		if confirm.Timeout != 3*time.Second {
			t.Fatalf("timeout = %s, want 3s", confirm.Timeout)
		}
	})

	t.Run("question", func(t *testing.T) {
		ev := agent.QuestionRequestEvent{
			ToolName:      "Question",
			Question:      "Continue?",
			Options:       []string{"yes", "no"},
			DefaultAnswer: "yes",
			RequestID:     "req-2",
			Timeout:       5 * time.Second,
		}
		env, err := FromAgentEvent(ev, 13)
		if err != nil {
			t.Fatal(err)
		}
		payload, err := ParsePayload[QuestionRequestPayload](env)
		if err != nil {
			t.Fatal(err)
		}
		if payload.TimeoutMS != 5000 {
			t.Fatalf("timeout_ms = %d, want 5000", payload.TimeoutMS)
		}
		got, err := ToAgentEvent(env)
		if err != nil {
			t.Fatal(err)
		}
		question, ok := got.(agent.QuestionRequestEvent)
		if !ok {
			t.Fatalf("unexpected event type %T", got)
		}
		if question.Timeout != 5*time.Second {
			t.Fatalf("timeout = %s, want 5s", question.Timeout)
		}
	})
}

func TestSessionSwitchStartedEventRoundTrip(t *testing.T) {
	orig := agent.SessionSwitchStartedEvent{Kind: "resume", SessionID: "123"}

	env, err := FromAgentEvent(orig, 42)
	if err != nil {
		t.Fatal(err)
	}
	if env.Type != TypeSessionSwitchStarted {
		t.Fatalf("env.Type = %q, want %q", env.Type, TypeSessionSwitchStarted)
	}

	evt, err := ToAgentEvent(env)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := evt.(agent.SessionSwitchStartedEvent)
	if !ok {
		t.Fatalf("unexpected event type %T", evt)
	}
	if got.Kind != orig.Kind || got.SessionID != orig.SessionID {
		t.Fatalf("roundtrip = %+v, want %+v", got, orig)
	}
}

func TestProtocolRoundTripAdditionalEvents(t *testing.T) {
	tests := []struct {
		name  string
		event agent.AgentEvent
		check func(t *testing.T, got agent.AgentEvent)
	}{
		{
			name:  "info",
			event: agent.InfoEvent{Message: "hello", AgentID: "main"},
			check: func(t *testing.T, got agent.AgentEvent) {
				ev := got.(agent.InfoEvent)
				if ev.Message != "hello" || ev.AgentID != "main" {
					t.Fatalf("InfoEvent = %+v", ev)
				}
			},
		},
		{
			name:  "activity",
			event: agent.AgentActivityEvent{AgentID: "a1", Type: agent.ActivityStreaming, Detail: "tokens"},
			check: func(t *testing.T, got agent.AgentEvent) {
				ev := got.(agent.AgentActivityEvent)
				if ev.AgentID != "a1" || ev.Type != agent.ActivityStreaming || ev.Detail != "tokens" {
					t.Fatalf("AgentActivityEvent = %+v", ev)
				}
			},
		},
		{
			name:  "session switch started",
			event: agent.SessionSwitchStartedEvent{Kind: "resume", SessionID: "s1"},
			check: func(t *testing.T, got agent.AgentEvent) {
				ev := got.(agent.SessionSwitchStartedEvent)
				if ev.Kind != "resume" || ev.SessionID != "s1" {
					t.Fatalf("SessionSwitchStartedEvent = %+v", ev)
				}
			},
		},
		{
			name:  "confirm request",
			event: agent.ConfirmRequestEvent{RequestID: "r1", ToolName: "Write", ArgsJSON: `{"path":"x"}`, NeedsApproval: []string{"write"}, AlreadyAllowed: []string{"read"}},
			check: func(t *testing.T, got agent.AgentEvent) {
				ev := got.(agent.ConfirmRequestEvent)
				if ev.RequestID != "r1" || ev.ToolName != "Write" || len(ev.NeedsApproval) != 1 || len(ev.AlreadyAllowed) != 1 {
					t.Fatalf("ConfirmRequestEvent = %+v", ev)
				}
			},
		},
		{
			name:  "background finished",
			event: agent.SpawnFinishedEvent{BackgroundID: "bg1", Kind: "spawn", Status: "done", Description: "run server", Message: "finished", AgentID: "a3"},
			check: func(t *testing.T, got agent.AgentEvent) {
				ev := got.(agent.SpawnFinishedEvent)
				if ev.BackgroundID != "bg1" || ev.Status != "done" || ev.Message != "finished" || ev.AgentID != "a3" {
					t.Fatalf("SpawnFinishedEvent = %+v", ev)
				}
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env, err := FromAgentEvent(tc.event, 123)
			if err != nil {
				t.Fatal(err)
			}
			got, err := ToAgentEvent(env)
			if err != nil {
				t.Fatal(err)
			}
			tc.check(t, got)
		})
	}
}

func TestToAgentEventAdditionalTypesAndErrors(t *testing.T) {
	t.Run("nil envelope", func(t *testing.T) {
		if _, err := ToAgentEvent(nil); err == nil || !strings.Contains(err.Error(), "nil envelope") {
			t.Fatalf("err = %v, want nil envelope", err)
		}
	})

	t.Run("unknown type", func(t *testing.T) {
		if _, err := ToAgentEvent(&Envelope{Type: "future_type"}); err == nil || !strings.Contains(err.Error(), "unknown envelope type") {
			t.Fatalf("err = %v, want unknown type", err)
		}
	})

	t.Run("snapshot is not agent event", func(t *testing.T) {
		_, err := ToAgentEvent(&Envelope{Type: TypeSnapshot})
		if !errors.Is(err, ErrNotAgentEvent) {
			t.Fatalf("err = %v, want ErrNotAgentEvent", err)
		}
	})

	t.Run("invalid payload", func(t *testing.T) {
		_, err := ToAgentEvent(&Envelope{Type: TypeStreamText, Payload: []byte(`{`)})
		if err == nil {
			t.Fatal("expected invalid payload error")
		}
	})

	t.Run("tool execution unknown state defaults queued", func(t *testing.T) {
		env, err := NewEnvelope(TypeToolCallExecution, ToolCallExecutionPayload{CallID: "c", Name: "Bash", State: "future"})
		if err != nil {
			t.Fatal(err)
		}
		ev, err := ToAgentEvent(env)
		if err != nil {
			t.Fatal(err)
		}
		got := ev.(agent.ToolCallExecutionEvent)
		if got.State != agent.ToolCallExecutionStateQueued {
			t.Fatalf("state = %q, want queued", got.State)
		}
	})

	t.Run("tool result unknown status defaults success", func(t *testing.T) {
		env, err := NewEnvelope(TypeToolResult, ToolResultPayload{CallID: "c", Name: "Read", Status: "future"})
		if err != nil {
			t.Fatal(err)
		}
		ev, err := ToAgentEvent(env)
		if err != nil {
			t.Fatal(err)
		}
		got := ev.(agent.ToolResultEvent)
		if got.Status != agent.ToolResultStatusSuccess {
			t.Fatalf("status = %q, want success", got.Status)
		}
	})

	t.Run("session select nil on malformed payload", func(t *testing.T) {
		ev, err := ToAgentEvent(&Envelope{Type: TypeSessionSelectRequest, Payload: []byte(`{`)})
		if err != nil {
			t.Fatal(err)
		}
		got := ev.(agent.SessionSelectEvent)
		if got.Sessions != nil {
			t.Fatalf("Sessions = %#v, want nil", got.Sessions)
		}
	})
}

func TestQuestionRequestEventRoundTripPreservesMetadata(t *testing.T) {
	orig := agent.QuestionRequestEvent{
		ToolName:      "Question",
		Header:        "Environment",
		Question:      "Which environment should be used?",
		Options:       []string{"staging", "prod"},
		OptionDetails: []string{"Staging environment", "Production environment"},
		DefaultAnswer: "staging",
		Multiple:      true,
		RequestID:     "req-1",
		Timeout:       3 * time.Second,
	}

	env, err := FromAgentEvent(orig, 42)
	if err != nil {
		t.Fatal(err)
	}

	evt, err := ToAgentEvent(env)
	if err != nil {
		t.Fatal(err)
	}

	got, ok := evt.(agent.QuestionRequestEvent)
	if !ok {
		t.Fatalf("unexpected event type %T", evt)
	}
	if got.Header != orig.Header {
		t.Fatalf("Header = %q, want %q", got.Header, orig.Header)
	}
	if len(got.OptionDetails) != len(orig.OptionDetails) {
		t.Fatalf("OptionDetails len = %d, want %d", len(got.OptionDetails), len(orig.OptionDetails))
	}
	for i := range got.OptionDetails {
		if got.OptionDetails[i] != orig.OptionDetails[i] {
			t.Fatalf("OptionDetails[%d] = %q, want %q", i, got.OptionDetails[i], orig.OptionDetails[i])
		}
	}
	if got.Timeout != orig.Timeout {
		t.Fatalf("Timeout = %s, want %s", got.Timeout, orig.Timeout)
	}
}
