package tools

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestClassifyNonInteractiveRuntimeFailureHighConfidence(t *testing.T) {
	cases := []struct {
		name   string
		output string
		want   string
	}{
		{name: "missing tty", output: "the input device is not a TTY", want: "requires a terminal/TTY"},
		{name: "git prompt disabled", output: "fatal: could not read Username for 'https://example.com': terminal prompts disabled", want: "credential prompt"},
		{name: "editor", output: "error: Terminal is dumb, but EDITOR unset\nPlease supply the message using either -m or -F option.", want: "interactive editor"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ClassifyNonInteractiveRuntimeFailure("cmd", errExitForTest{}, tc.output)
			if got == nil {
				t.Fatal("expected runtime finding")
			}
			if !strings.Contains(got.Reason, tc.want) {
				t.Fatalf("reason = %q, want to contain %q", got.Reason, tc.want)
			}
		})
	}
}

func TestNonInteractiveRuntimeAdviceForWindowsMentionsCleanupLimitation(t *testing.T) {
	got := nonInteractiveRuntimeAdviceForGOOS("windows")
	for _, want := range []string{"stdin is closed", "child-process cleanup may be less complete than on Unix"} {
		if !strings.Contains(got, want) {
			t.Fatalf("advice = %q, want to contain %q", got, want)
		}
	}
}

func TestNonInteractiveRuntimeAdviceForUnixOmitsWindowsSpecificLimitation(t *testing.T) {
	got := nonInteractiveRuntimeAdviceForGOOS("linux")
	if strings.Contains(got, "child-process cleanup may be less complete than on Unix") {
		t.Fatalf("advice = %q, want no Windows-specific cleanup warning", got)
	}
}

func TestClassifyNonInteractiveRuntimeFailureIgnoresOrdinaryFailure(t *testing.T) {
	got := ClassifyNonInteractiveRuntimeFailure("grep nope file", errExitForTest{}, "grep: file: No such file or directory")
	if got != nil {
		t.Fatalf("finding = %#v, want nil", got)
	}
}

func TestShellRuntimeFailureDiagnosticPreservesOutputAndExitCode(t *testing.T) {
	out, err := ShellTool{}.Execute(context.Background(), mustMarshal(t, map[string]any{
		"command": "printf 'fatal: could not read Username for https://example.invalid: terminal prompts disabled\n' >&2; exit 128",
	}))
	if err == nil {
		t.Fatal("expected command failure")
	}
	if !strings.Contains(out, "terminal prompts disabled") {
		t.Fatalf("output = %q, want original stderr", out)
	}
	for _, want := range []string{"exit code 128", "non-interactive shell failure", "provide input through files/arguments/pipes"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %q, want to contain %q", err.Error(), want)
		}
	}
}

func TestShellRuntimeFailureDoesNotRewriteOrdinaryFailure(t *testing.T) {
	_, err := ShellTool{}.Execute(context.Background(), mustMarshal(t, map[string]any{
		"command": "printf ordinary >&2; exit 7",
	}))
	if err == nil {
		t.Fatal("expected command failure")
	}
	if err.Error() != "exit code 7" {
		t.Fatalf("error = %q, want ordinary exit code", err.Error())
	}
}

func TestShellRuntimeFailureSuggestsFocusedReproductionForTestCommand(t *testing.T) {
	_, err := ShellTool{}.Execute(context.Background(), mustMarshal(t, map[string]any{
		"command": "go test ./definitely-missing-package",
	}))
	if err == nil {
		t.Fatal("expected command failure")
	}
	for _, want := range []string{"exit code ", "Test or verification command failed", "focused reproduction", "Do not repeat the same failing command unchanged"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %q, want to contain %q", err.Error(), want)
		}
	}
}

func TestShellRuntimeFailureIgnoresTestCommandTextInComment(t *testing.T) {
	_, err := ShellTool{}.Execute(context.Background(), mustMarshal(t, map[string]any{
		"command": "printf fail >&2; exit 7 # go test ./...",
	}))
	if err == nil {
		t.Fatal("expected command failure")
	}
	if err.Error() != "exit code 7" {
		t.Fatalf("error = %q, want ordinary exit code", err.Error())
	}
}

func TestShellRuntimeFailureIgnoresTestCommandTextInArgument(t *testing.T) {
	_, err := ShellTool{}.Execute(context.Background(), mustMarshal(t, map[string]any{
		"command": `printf '%s\n' 'go test ./...' >&2; exit 7`,
	}))
	if err == nil {
		t.Fatal("expected command failure")
	}
	if err.Error() != "exit code 7" {
		t.Fatalf("error = %q, want ordinary exit code", err.Error())
	}
}

func TestSpawnRuntimeFailureDiagnosticInCompletionEvent(t *testing.T) {
	resetSpawnRegistryOnlyForTest(t)
	sender := &recordingEventSender{ch: make(chan any, 1)}
	ctx := WithEventSender(context.Background(), sender)
	_, err := NewSpawnTool("").Execute(ctx, mustMarshal(t, map[string]any{
		"command":     "printf 'fatal: could not read Username for https://example.invalid: terminal prompts disabled\n' >&2; exit 128",
		"description": "runtime prompt failure",
		"timeout":     5,
	}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	select {
	case payload := <-sender.ch:
		finished, ok := payload.(*SpawnFinishedPayload)
		if !ok {
			t.Fatalf("payload type = %T, want *SpawnFinishedPayload", payload)
		}
		for _, want := range []string{"non-interactive Spawn failure", "terminal prompts disabled", "exit code 128"} {
			if !strings.Contains(finished.Status+finished.Message, want) {
				t.Fatalf("finished status/message = %q / %q, want %q", finished.Status, finished.Message, want)
			}
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for spawn completion event")
	}
}

func TestSpawnOrdinaryFailureCompletionPreservesRelevantOutput(t *testing.T) {
	resetSpawnRegistryOnlyForTest(t)
	sender := &recordingEventSender{ch: make(chan any, 1)}
	ctx := WithEventSender(context.Background(), sender)
	_, err := NewSpawnTool("").Execute(ctx, mustMarshal(t, map[string]any{
		"command":     "printf 'stdout line\n'; printf 'stderr line\n' >&2; exit 7",
		"description": "ordinary failure with output",
		"timeout":     5,
	}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	select {
	case payload := <-sender.ch:
		finished, ok := payload.(*SpawnFinishedPayload)
		if !ok {
			t.Fatalf("payload type = %T, want *SpawnFinishedPayload", payload)
		}
		if !strings.Contains(finished.Status, "exit code 7") {
			t.Fatalf("finished status = %q, want exit code 7", finished.Status)
		}
		for _, want := range []string{"Relevant output:", "stdout line", "stderr line"} {
			if !strings.Contains(finished.Message, want) {
				t.Fatalf("finished message = %q, want %q", finished.Message, want)
			}
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for spawn completion event")
	}
}

type errExitForTest struct{}

func (errExitForTest) Error() string { return "exit status 1" }

type recordingEventSender struct {
	ch        chan any
	eventType string
	sourceID  string
}

func (s *recordingEventSender) SendAgentEvent(eventType, sourceID string, payload any) {
	s.eventType = eventType
	s.sourceID = sourceID
	s.ch <- payload
}
