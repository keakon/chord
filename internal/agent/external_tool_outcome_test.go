package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/executiongate"
	"github.com/keakon/chord/internal/hook"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func TestToolExecutionResultRecoveryStateExternalOutcome(t *testing.T) {
	unknown := tools.WrapExternalCallError(fmt.Errorf("mcp tools/call srv/tool: %w", io.ErrUnexpectedEOF), tools.ExternalCallOutcomeUnknown)
	notSent := tools.WrapExternalCallError(errors.New("invalid args"), tools.ExternalCallNotSent)
	reported := tools.WrapExternalCallError(errors.New("server rejected"), tools.ExternalCallReportedError)

	cases := []struct {
		name   string
		result ToolExecutionResult
		err    error
		want   string
	}{
		{name: "external outcome unknown", result: ToolExecutionResult{ExecutionStarted: true}, err: unknown, want: message.ToolRecoveryStateOutcomeUnknown},
		{name: "external not sent", result: ToolExecutionResult{ExecutionStarted: true}, err: notSent, want: ""},
		{name: "external reported error", result: ToolExecutionResult{ExecutionStarted: true}, err: reported, want: ""},
		{name: "paused before execution", result: ToolExecutionResult{}, err: &executiongate.ErrSessionPaused{}, want: message.ToolRecoveryStateNotStarted},
		{name: "hosted outcome unknown", result: ToolExecutionResult{ExecutionStarted: true}, err: &llm.HostedOutcomeUnknownError{Cause: io.ErrUnexpectedEOF}, want: message.ToolRecoveryStateOutcomeUnknown},
		{name: "ordinary failure", result: ToolExecutionResult{ExecutionStarted: true}, err: errors.New("exit status 1"), want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.result.recoveryState(tc.err); got != tc.want {
				t.Fatalf("recoveryState() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestFinalizeToolResultTextsExternalOutcomeNote(t *testing.T) {
	noHook := func(context.Context, string, uint64, map[string]any) (*hook.Result, error) { return nil, nil }
	turn := &Turn{ID: 1, Ctx: context.Background()}
	externalUnknown := fmt.Errorf("adapter: %w", tools.WrapExternalCallError(
		fmt.Errorf("mcp tools/call srv/tool: %w", io.ErrUnexpectedEOF),
		tools.ExternalCallOutcomeUnknown,
	))

	composed := finalizeToolResultTexts(context.Background(), turn, noHook, "call-1", "mcp_srv_tool", `{}`, "", externalUnknown, nil, nil)
	if !composed.IsError {
		t.Fatal("external failure composed as success")
	}
	if strings.Contains(composed.Display, externalOutcomeTransportNote) {
		t.Fatalf("card text must stay plain: %q", composed.Display)
	}
	if strings.Count(composed.Context, externalOutcomeTransportNote) != 1 {
		t.Fatalf("model context note count = %d, want 1:\n%s", strings.Count(composed.Context, externalOutcomeTransportNote), composed.Context)
	}
	if !strings.Contains(composed.Context, "mcp tools/call srv/tool") {
		t.Fatalf("model context lost the cause: %q", composed.Context)
	}

	reported := tools.WrapExternalCallError(errors.New("server rejected"), tools.ExternalCallReportedError)
	composed = finalizeToolResultTexts(context.Background(), turn, noHook, "call-2", "mcp_srv_tool", `{}`, "", reported, nil, nil)
	if strings.Contains(composed.Context, externalOutcomeTransportNote) {
		t.Fatalf("reported error must not carry the unknown-outcome note: %q", composed.Context)
	}

	composed = finalizeToolResultTexts(context.Background(), turn, noHook, "call-3", "mcp_srv_tool", `{}`, "ok", nil, nil, nil)
	if composed.Context != "ok" || composed.Display != "ok" || composed.IsError {
		t.Fatalf("success text changed: display=%q context=%q error=%v", composed.Display, composed.Context, composed.IsError)
	}
}
