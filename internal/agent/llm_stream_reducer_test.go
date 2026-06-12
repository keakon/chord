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

func TestMainLLMStreamReducerEmitsSilentRetryError(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	retryErr := errors.New("rate limited")
	reducer := a.newMainLLMStreamReducer(nil, "provider/model-1", "", nil, false, nil)

	reducer.Handle(message.StreamDelta{
		Type:      message.StreamDeltaRetryError,
		Err:       retryErr,
		Provider:  "provider",
		Model:     "model-1",
		KeySuffix: "...abc1",
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
		if errEvt.Provider != "provider" || errEvt.Model != "model-1" || errEvt.Key != "...abc1" {
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
	reducer := sub.newSubLLMStreamReducer(&Turn{ID: 1}, func(string) {}, false)

	reducer.Handle(message.StreamDelta{
		Type:      message.StreamDeltaRetryError,
		Err:       retryErr,
		Provider:  "provider",
		Model:     "model-1",
		KeySuffix: "...abc1",
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
		if errEvt.Provider != "provider" || errEvt.Model != "model-1" || errEvt.Key != "...abc1" {
			t.Fatalf("error event metadata = %#v, want retry metadata", errEvt)
		}
	default:
		t.Fatal("missing retry ErrorEvent")
	}
}
