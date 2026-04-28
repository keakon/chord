package agent

import (
	"context"
	"testing"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

type toolExecContextEventSender struct{}

func (toolExecContextEventSender) SendAgentEvent(eventType, sourceID string, payload any) {}

func TestBuildToolExecContextInjectsProgressReporter(t *testing.T) {
	var emitted []AgentEvent
	ctx := buildToolExecContext(
		context.Background(),
		message.ToolCall{ID: "call-1", Name: "Delete"},
		"agent-1",
		"adhoc-1",
		"/tmp/session",
		toolExecContextEventSender{},
		func(evt AgentEvent) { emitted = append(emitted, evt) },
	)

	reporter := tools.ToolProgressReporterFromContext(ctx)
	if reporter == nil {
		t.Fatal("expected tool progress reporter in context")
	}
	reporter.ReportToolProgress(tools.ToolProgressSnapshot{Label: "paths", Current: 2, Total: 5})

	if len(emitted) != 1 {
		t.Fatalf("emitted events = %d, want 1", len(emitted))
	}
	progress, ok := emitted[0].(ToolProgressEvent)
	if !ok {
		t.Fatalf("event type = %T, want ToolProgressEvent", emitted[0])
	}
	if progress.CallID != "call-1" || progress.Name != "Delete" || progress.AgentID != "agent-1" {
		t.Fatalf("unexpected progress identity: %+v", progress)
	}
	if got := tools.TaskIDFromContext(ctx); got != "adhoc-1" {
		t.Fatalf("TaskIDFromContext() = %q, want adhoc-1", got)
	}
	if progress.Progress.Label != "paths" || progress.Progress.Current != 2 || progress.Progress.Total != 5 {
		t.Fatalf("unexpected progress payload: %+v", progress.Progress)
	}
}

func TestToolProgressReporterFromContextReturnsNilWhenUnset(t *testing.T) {
	if reporter := tools.ToolProgressReporterFromContext(context.Background()); reporter != nil {
		t.Fatal("expected nil reporter when context is not instrumented")
	}
}
