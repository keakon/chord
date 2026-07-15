package agent

import (
	"errors"
	"testing"
	"time"

	"github.com/keakon/chord/internal/message"
)

func TestStreamContentReducerMainClosesThinkingBeforeTextAndIgnoresLateThinking(t *testing.T) {
	var events []AgentEvent
	reducer := streamContentReducer{
		emit:                    func(evt AgentEvent) { events = append(events, evt) },
		emitThinkingStarted:     true,
		ignoreThinkingAfterText: true,
		closeThinkingOnText:     true,
		closeThinkingOnFinish:   true,
		thinkingCommitMode:      streamContentCommitEmpty,
		textFlushInterval:       time.Hour,
		thinkingFlushInterval:   time.Hour,
	}

	reducer.Handle(message.StreamDelta{Type: message.StreamDeltaThinking, Text: "plan"})
	reducer.Handle(message.StreamDelta{Type: message.StreamDeltaText, Text: "answer"})
	reducer.Handle(message.StreamDelta{Type: message.StreamDeltaThinking, Text: "late"})
	reducer.Finish()

	if len(events) != 4 {
		t.Fatalf("events len = %d, want 4: %#v", len(events), events)
	}
	if _, ok := events[0].(ThinkingStartedEvent); !ok {
		t.Fatalf("events[0] = %T, want ThinkingStartedEvent", events[0])
	}
	if got, ok := events[1].(StreamThinkingDeltaEvent); !ok || got.Text != "plan" {
		t.Fatalf("events[1] = %#v, want thinking delta plan", events[1])
	}
	if got, ok := events[2].(StreamThinkingEvent); !ok || got.Text != "" {
		t.Fatalf("events[2] = %#v, want empty thinking commit", events[2])
	}
	if got, ok := events[3].(StreamTextEvent); !ok || got.Text != "answer" {
		t.Fatalf("events[3] = %#v, want text answer", events[3])
	}
}

func TestStreamContentReducerThinkingStartedIncludesAgentID(t *testing.T) {
	var events []AgentEvent
	reducer := streamContentReducer{
		agentID:               "agent-1",
		emit:                  func(evt AgentEvent) { events = append(events, evt) },
		emitThinkingStarted:   true,
		thinkingCommitMode:    streamContentCommitFullText,
		thinkingFlushInterval: time.Hour,
	}

	reducer.Handle(message.StreamDelta{Type: message.StreamDeltaThinking, Text: "thought"})

	if len(events) == 0 {
		t.Fatal("expected ThinkingStartedEvent")
	}
	started, ok := events[0].(ThinkingStartedEvent)
	if !ok {
		t.Fatalf("events[0] = %T, want ThinkingStartedEvent", events[0])
	}
	if started.AgentID != "agent-1" {
		t.Fatalf("ThinkingStartedEvent.AgentID = %q, want agent-1", started.AgentID)
	}
}

func TestStreamContentReducerSubAgentKeepsImmediateThinkingDeltaAndFinalText(t *testing.T) {
	var events []AgentEvent
	reducer := streamContentReducer{
		agentID:               "agent-1",
		emit:                  func(evt AgentEvent) { events = append(events, evt) },
		scrubThinkingFinal:    true,
		thinkingCommitMode:    streamContentCommitFullText,
		textFlushInterval:     time.Hour,
		thinkingFlushInterval: 0,
	}

	reducer.Handle(message.StreamDelta{Type: message.StreamDeltaThinking, Text: "sub plan"})
	reducer.Handle(message.StreamDelta{Type: message.StreamDeltaThinkingEnd})
	reducer.Handle(message.StreamDelta{Type: message.StreamDeltaText, Text: "done"})
	reducer.Finish()

	if len(events) != 3 {
		t.Fatalf("events len = %d, want 3: %#v", len(events), events)
	}
	if got, ok := events[0].(StreamThinkingDeltaEvent); !ok || got.AgentID != "agent-1" || got.Text != "sub plan" {
		t.Fatalf("events[0] = %#v, want subagent thinking delta", events[0])
	}
	if got, ok := events[1].(StreamThinkingEvent); !ok || got.AgentID != "agent-1" || got.Text != "sub plan" {
		t.Fatalf("events[1] = %#v, want subagent final thinking", events[1])
	}
	if got, ok := events[2].(StreamTextEvent); !ok || got.AgentID != "agent-1" || got.Text != "done" {
		t.Fatalf("events[2] = %#v, want subagent text", events[2])
	}
}

func TestStreamContentReducerRollbackDropsBufferedTextBeforeFinish(t *testing.T) {
	var events []AgentEvent
	reducer := streamContentReducer{
		emit:              func(evt AgentEvent) { events = append(events, evt) },
		textFlushInterval: time.Hour,
	}

	reducer.Handle(message.StreamDelta{Type: message.StreamDeltaText, Text: "first"})
	reducer.Handle(message.StreamDelta{Type: message.StreamDeltaText, Text: " buffered"})
	reducer.Rollback()
	reducer.Finish()

	if len(events) != 1 {
		t.Fatalf("events len = %d, want only first immediate text before rollback: %#v", len(events), events)
	}
	if got, ok := events[0].(StreamTextEvent); !ok || got.Text != "first" {
		t.Fatalf("events[0] = %#v, want first text", events[0])
	}
}

func TestLLMStreamReducerRollbackResetsContentBeforeFinish(t *testing.T) {
	var events []AgentEvent
	reducer := &llmStreamReducer{}
	reducer.content = streamContentReducer{
		emit:                  func(evt AgentEvent) { events = append(events, evt) },
		emitThinkingStarted:   true,
		closeThinkingOnFinish: true,
		thinkingCommitMode:    streamContentCommitEmpty,
		textFlushInterval:     time.Hour,
		thinkingFlushInterval: time.Hour,
	}
	reducer.tool = streamToolDeltaReducer{emit: func(evt AgentEvent) { events = append(events, evt) }}

	reducer.Handle(message.StreamDelta{Type: message.StreamDeltaThinking, Text: "failed thought"})
	reducer.Handle(message.StreamDelta{Type: message.StreamDeltaRollback, Rollback: &message.RollbackDelta{Reason: "interrupted"}})
	reducer.Finish()

	if len(events) != 2 {
		t.Fatalf("events len = %d, want ThinkingStartedEvent + StreamRollbackEvent: %#v", len(events), events)
	}
	if _, ok := events[0].(ThinkingStartedEvent); !ok {
		t.Fatalf("events[0] = %T, want ThinkingStartedEvent", events[0])
	}
	if rollback, ok := events[1].(StreamRollbackEvent); !ok || rollback.Reason != "interrupted" {
		t.Fatalf("events[1] = %#v, want rollback event", events[1])
	}
}

func TestLLMStreamReducerIgnoresTraceOnlyEventDelta(t *testing.T) {
	var events []AgentEvent
	var progress []*message.StreamProgressDelta
	activity := 0
	reducer := &llmStreamReducer{
		emitActivity: func(ActivityType, string) { activity++ },
		onProgress:   func(p *message.StreamProgressDelta) { progress = append(progress, p) },
	}
	reducer.content = streamContentReducer{emit: func(evt AgentEvent) { events = append(events, evt) }}
	reducer.tool = streamToolDeltaReducer{emit: func(evt AgentEvent) { events = append(events, evt) }}

	reducer.Handle(message.StreamDelta{Event: &message.StreamEventDelta{Type: "response.output_text.delta"}})
	reducer.Finish()

	if len(events) != 0 || len(progress) != 0 || activity != 0 {
		t.Fatalf("trace-only event produced visible effects: events=%#v progress=%#v activity=%d", events, progress, activity)
	}
}

func TestMainLLMStreamReducerEmitsSilentRetryError(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	retryErr := errors.New("rate limited")
	reducer := a.newMainLLMStreamReducer(nil, "provider/model-1", "", nil, false, nil)

	reducer.Handle(message.StreamDelta{
		Type:      message.StreamDeltaRetryError,
		Err:       retryErr,
		Provider:  "provider",
		Model:     "model-1",
		MaskedKey: "open...abc1",
		AccountID: "acc-1",
		Email:     "user@example.com",
	})

	select {
	case evt := <-a.outputCh:
		errEvt, ok := evt.(ErrorEvent)
		if !ok {
			t.Fatalf("event = %T, want ErrorEvent", evt)
		}
		if errEvt.Err != retryErr || !errEvt.Silent || errEvt.AgentID != a.instanceID {
			t.Fatalf("error event = %#v, want silent retry error for main agent", errEvt)
		}
		if errEvt.Provider != "provider" || errEvt.Model != "model-1" || errEvt.Key != "open...abc1" || errEvt.AccountID != "acc-1" || errEvt.Email != "user@example.com" {
			t.Fatalf("error event metadata = %#v, want retry metadata", errEvt)
		}
	default:
		t.Fatal("missing retry ErrorEvent")
	}
}

func TestSubLLMStreamReducerEmitsSilentRetryError(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	retryErr := errors.New("rate limited")
	sub := &SubAgent{
		instanceID: "agent-1",
		parent:     a,
	}
	reducer := sub.newSubLLMStreamReducer(&Turn{ID: 1}, func(string) {}, false, nil)

	reducer.Handle(message.StreamDelta{
		Type:      message.StreamDeltaRetryError,
		Err:       retryErr,
		Provider:  "provider",
		Model:     "model-1",
		MaskedKey: "open...abc1",
		AccountID: "acc-1",
		Email:     "user@example.com",
	})

	select {
	case evt := <-a.outputCh:
		errEvt, ok := evt.(ErrorEvent)
		if !ok {
			t.Fatalf("event = %T, want ErrorEvent", evt)
		}
		if errEvt.Err != retryErr || !errEvt.Silent || errEvt.AgentID != sub.instanceID {
			t.Fatalf("error event = %#v, want silent retry error for subagent", errEvt)
		}
		if errEvt.Provider != "provider" || errEvt.Model != "model-1" || errEvt.Key != "open...abc1" || errEvt.AccountID != "acc-1" || errEvt.Email != "user@example.com" {
			t.Fatalf("error event metadata = %#v, want retry metadata", errEvt)
		}
	default:
		t.Fatal("missing retry ErrorEvent")
	}
}

func TestSubLLMStreamReducerEmitsRequestProgress(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	sub := &SubAgent{
		instanceID: "agent-1",
		parent:     a,
	}
	state := &subLLMStreamState{}
	reducer := sub.newSubLLMStreamReducer(&Turn{ID: 1}, func(string) {}, false, state)

	reducer.Handle(message.StreamDelta{Progress: &message.StreamProgressDelta{Bytes: 40_934, Events: 95}})

	select {
	case evt := <-a.outputCh:
		progress, ok := evt.(RequestProgressEvent)
		if !ok {
			t.Fatalf("event = %T, want RequestProgressEvent", evt)
		}
		if progress.AgentID != sub.instanceID || progress.Bytes != 40_934 || progress.Events != 95 || progress.Done {
			t.Fatalf("progress event = %#v, want subagent progress", progress)
		}
	default:
		t.Fatal("missing RequestProgressEvent")
	}
	if state.requestProgressBytes != 40_934 || state.requestProgressEvents != 95 {
		t.Fatalf("progress state = %#v, want bytes=40934 events=95", state)
	}
}
