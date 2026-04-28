package agent

import (
	"fmt"
	"reflect"
	"testing"
	"time"
)

func TestEmitToTUIToolResultWaitsForSpaceInsteadOfDropping(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)

	for i := 0; i < cap(a.outputCh); i++ {
		a.outputCh <- StreamTextEvent{Text: "busy"}
	}

	delivered := make(chan struct{})
	go func() {
		a.emitToTUI(ToolResultEvent{CallID: "tool-1", Name: "Bash", Result: "ok", Status: ToolResultStatusSuccess})
		close(delivered)
	}()

	select {
	case <-delivered:
		t.Fatal("ToolResultEvent send completed while output channel was still full")
	case <-time.After(30 * time.Millisecond):
	}

	for i := 0; i < cap(a.outputCh); i++ {
		evt := <-a.outputCh
		if _, ok := evt.(ToolResultEvent); ok {
			t.Fatalf("received ToolResultEvent before space was made available at slot %d", i)
		}
	}

	select {
	case <-delivered:
	case <-time.After(time.Second):
		t.Fatal("ToolResultEvent was not delivered after channel space became available")
	}

	evt := <-a.outputCh
	result, ok := evt.(ToolResultEvent)
	if !ok {
		t.Fatalf("event type = %T, want ToolResultEvent", evt)
	}
	if result.CallID != "tool-1" || result.Result != "ok" || result.Status != ToolResultStatusSuccess {
		t.Fatalf("tool result = %+v, want call_id=tool-1 result=ok status=success", result)
	}
}

func TestEmitToTUIToolCallStartWaitsForSpaceInsteadOfDropping(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)

	for i := 0; i < cap(a.outputCh); i++ {
		a.outputCh <- StreamTextEvent{Text: "busy"}
	}

	delivered := make(chan struct{})
	go func() {
		a.emitToTUI(ToolCallStartEvent{ID: "tool-1", Name: "Bash", ArgsJSON: `{"command":"pwd"}`})
		close(delivered)
	}()

	select {
	case <-delivered:
		t.Fatal("ToolCallStartEvent send completed while output channel was still full")
	case <-time.After(30 * time.Millisecond):
	}

	for i := 0; i < cap(a.outputCh); i++ {
		evt := <-a.outputCh
		if _, ok := evt.(ToolCallStartEvent); ok {
			t.Fatalf("received ToolCallStartEvent before space was made available at slot %d", i)
		}
	}

	select {
	case <-delivered:
	case <-time.After(time.Second):
		t.Fatal("ToolCallStartEvent was not delivered after channel space became available")
	}

	evt := <-a.outputCh
	start, ok := evt.(ToolCallStartEvent)
	if !ok {
		t.Fatalf("event type = %T, want ToolCallStartEvent", evt)
	}
	if start.ID != "tool-1" || start.Name != "Bash" {
		t.Fatalf("tool start = %+v, want id=tool-1 name=Bash", start)
	}
}

func TestEmitToTUIControlEventsWaitForSpaceInsteadOfDropping(t *testing.T) {
	cases := []struct {
		name  string
		event AgentEvent
		check func(t *testing.T, evt AgentEvent)
	}{
		{
			name:  "ToolCallUpdateDone",
			event: ToolCallUpdateEvent{ID: "tool-1", Name: "Bash", ArgsJSON: `{"command":"pwd"}`, ArgsStreamingDone: true},
			check: func(t *testing.T, evt AgentEvent) {
				t.Helper()
				got, ok := evt.(ToolCallUpdateEvent)
				if !ok {
					t.Fatalf("event type = %T, want ToolCallUpdateEvent", evt)
				}
				if got.ID != "tool-1" || !got.ArgsStreamingDone {
					t.Fatalf("ToolCallUpdateEvent = %+v, want tool-1 with ArgsStreamingDone", got)
				}
			},
		},
		{
			name:  "SessionRestored",
			event: SessionRestoredEvent{},
			check: func(t *testing.T, evt AgentEvent) {
				t.Helper()
				if _, ok := evt.(SessionRestoredEvent); !ok {
					t.Fatalf("event type = %T, want SessionRestoredEvent", evt)
				}
			},
		},
		{
			name:  "PendingDraftConsumed",
			event: PendingDraftConsumedEvent{DraftID: "draft-1"},
			check: func(t *testing.T, evt AgentEvent) {
				t.Helper()
				got, ok := evt.(PendingDraftConsumedEvent)
				if !ok {
					t.Fatalf("event type = %T, want PendingDraftConsumedEvent", evt)
				}
				if got.DraftID != "draft-1" {
					t.Fatalf("DraftID = %q, want draft-1", got.DraftID)
				}
			},
		},
		{
			name:  "Error",
			event: ErrorEvent{Err: fmt.Errorf("boom")},
			check: func(t *testing.T, evt AgentEvent) {
				t.Helper()
				got, ok := evt.(ErrorEvent)
				if !ok {
					t.Fatalf("event type = %T, want ErrorEvent", evt)
				}
				if got.Err == nil || got.Err.Error() != "boom" {
					t.Fatalf("Err = %#v, want boom", got.Err)
				}
			},
		},
		{
			name:  "AgentStatus",
			event: AgentStatusEvent{AgentID: "agent-1", Status: "running", Message: "working"},
			check: func(t *testing.T, evt AgentEvent) {
				t.Helper()
				got, ok := evt.(AgentStatusEvent)
				if !ok {
					t.Fatalf("event type = %T, want AgentStatusEvent", evt)
				}
				if got.AgentID != "agent-1" || got.Status != "running" {
					t.Fatalf("AgentStatusEvent = %+v, want agent-1/running", got)
				}
			},
		},
		{
			name:  "Info",
			event: InfoEvent{Message: "session exported"},
			check: func(t *testing.T, evt AgentEvent) {
				t.Helper()
				got, ok := evt.(InfoEvent)
				if !ok {
					t.Fatalf("event type = %T, want InfoEvent", evt)
				}
				if got.Message != "session exported" {
					t.Fatalf("InfoEvent = %+v, want session exported", got)
				}
			},
		},
		{
			name:  "Toast",
			event: ToastEvent{Message: "warn", Level: "warn"},
			check: func(t *testing.T, evt AgentEvent) {
				t.Helper()
				got, ok := evt.(ToastEvent)
				if !ok {
					t.Fatalf("event type = %T, want ToastEvent", evt)
				}
				if got.Message != "warn" || got.Level != "warn" {
					t.Fatalf("ToastEvent = %+v, want warn/warn", got)
				}
			},
		},
		{
			name:  "LoopNotice",
			event: LoopNoticeEvent{Title: "LOOP", Text: "continue", DedupKey: "loop:1"},
			check: func(t *testing.T, evt AgentEvent) {
				t.Helper()
				got, ok := evt.(LoopNoticeEvent)
				if !ok {
					t.Fatalf("event type = %T, want LoopNoticeEvent", evt)
				}
				if got.Title != "LOOP" || got.Text != "continue" || got.DedupKey != "loop:1" {
					t.Fatalf("LoopNoticeEvent = %+v, want LOOP/continue/loop:1", got)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			projectRoot := t.TempDir()
			a := newTestMainAgent(t, projectRoot)

			for i := 0; i < cap(a.outputCh); i++ {
				a.outputCh <- StreamTextEvent{Text: "busy"}
			}

			delivered := make(chan struct{})
			go func() {
				a.emitToTUI(tc.event)
				close(delivered)
			}()

			select {
			case <-delivered:
				t.Fatalf("%T send completed while output channel was still full", tc.event)
			case <-time.After(30 * time.Millisecond):
			}

			for i := 0; i < cap(a.outputCh); i++ {
				evt := <-a.outputCh
				if reflect.TypeOf(evt) == reflect.TypeOf(tc.event) {
					t.Fatalf("received %T before space was made available at slot %d", evt, i)
				}
			}

			select {
			case <-delivered:
			case <-time.After(time.Second):
				t.Fatalf("%T was not delivered after channel space became available", tc.event)
			}

			tc.check(t, <-a.outputCh)
		})
	}
}

func TestEmitToTUIToolCallUpdateStillDropsWhileStreamingWhenChannelFull(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)

	for i := 0; i < cap(a.outputCh); i++ {
		a.outputCh <- StreamTextEvent{Text: "busy"}
	}

	a.emitToTUI(ToolCallUpdateEvent{ID: "tool-1", Name: "Bash", ArgsJSON: `{"command":"pw"}`})

	for i := 0; i < cap(a.outputCh); i++ {
		evt := <-a.outputCh
		stream, ok := evt.(StreamTextEvent)
		if !ok {
			t.Fatalf("event %d type = %T, want StreamTextEvent", i, evt)
		}
		if stream.Text != "busy" {
			t.Fatalf("event %d text = %q, want busy", i, stream.Text)
		}
	}

	select {
	case evt := <-a.outputCh:
		t.Fatalf("unexpected extra event after dropped tool update: %T", evt)
	default:
	}
}

func TestEmitToTUIStreamEventStillDropsWhenChannelFull(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)

	for i := 0; i < cap(a.outputCh); i++ {
		a.outputCh <- StreamTextEvent{Text: "busy"}
	}

	a.emitToTUI(StreamTextEvent{Text: "drop me"})

	for i := 0; i < cap(a.outputCh); i++ {
		evt := <-a.outputCh
		stream, ok := evt.(StreamTextEvent)
		if !ok {
			t.Fatalf("event %d type = %T, want StreamTextEvent", i, evt)
		}
		if stream.Text != "busy" {
			t.Fatalf("event %d text = %q, want busy", i, stream.Text)
		}
	}

	select {
	case evt := <-a.outputCh:
		t.Fatalf("unexpected extra event after dropped stream delta: %T", evt)
	default:
	}
}
