package agent

import (
	"strings"
	"testing"

	"github.com/keakon/chord/internal/command"
	"github.com/keakon/chord/internal/hook"
	"github.com/keakon/chord/internal/message"
)

type recordingActivityObserver struct {
	agentID  string
	activity ActivityType
	calls    int
}

func (r *recordingActivityObserver) OnAgentActivity(agentID string, activity ActivityType) {
	r.agentID = agentID
	r.activity = activity
	r.calls++
}

func TestSetActivityObserverAndEmitActivity(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	obs := &recordingActivityObserver{}
	a.SetActivityObserver(obs)

	a.emitActivity("main", ActivityStreaming, "planning")
	if obs.calls != 1 || obs.agentID != "main" || obs.activity != ActivityStreaming {
		t.Fatalf("observer = %+v, want one thinking notification for main", obs)
	}

	a.SetActivityObserver(nil)
	a.emitActivity("main", ActivityIdle, "done")
	if obs.calls != 1 {
		t.Fatalf("observer calls after unregister = %d, want 1", obs.calls)
	}
}

func TestCustomSlashExpansionAndModelExpansion(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.SetCustomCommands([]*command.Definition{
		{Name: "Review", Template: "Review this change:\n$ARGUMENTS"},
		{Name: "Explain", Template: "Explain the code"},
	})

	expanded, ok := a.customSlashExpansion("/review file.go")
	if !ok || expanded != "Review this change:\nfile.go" {
		t.Fatalf("customSlashExpansion review = (%q, %v)", expanded, ok)
	}
	expanded, ok = a.customSlashExpansion("/EXPLAIN topic")
	if !ok || expanded != "Explain the code\n\ntopic" {
		t.Fatalf("customSlashExpansion explain = (%q, %v)", expanded, ok)
	}
	if _, ok := a.customSlashExpansion("review file.go"); ok {
		t.Fatal("customSlashExpansion without slash should not match")
	}
	if _, ok := a.customSlashExpansion("/missing args"); ok {
		t.Fatal("customSlashExpansion unknown command should not match")
	}

	content, parts := a.expandSlashCommandForModel("  /review main.go  ", nil)
	if content != "Review this change:\nmain.go" || parts != nil {
		t.Fatalf("expandSlashCommandForModel content = %q parts=%#v", content, parts)
	}
	content, parts = a.expandSlashCommandForModel("ignored", []message.ContentPart{{Type: "text", Text: " /review part.go "}})
	if content != "Review this change:\npart.go" || parts != nil {
		t.Fatalf("expandSlashCommandForModel text part = %q parts=%#v", content, parts)
	}
	origParts := []message.ContentPart{{Type: "image"}, {Type: "text", Text: "/review image"}}
	content, parts = a.expandSlashCommandForModel("original", origParts)
	if content != "original" || len(parts) != len(origParts) {
		t.Fatalf("image parts should bypass expansion, got content=%q parts=%#v", content, parts)
	}
}

func TestExpandCommandTemplate(t *testing.T) {
	if got := expandCommandTemplate("Run: $ARGUMENTS", "go test"); got != "Run: go test" {
		t.Fatalf("placeholder expansion = %q", got)
	}
	if got := expandCommandTemplate("Run", "go test"); got != "Run\n\ngo test" {
		t.Fatalf("argument append expansion = %q", got)
	}
	if got := expandCommandTemplate("Run", ""); got != "Run" {
		t.Fatalf("empty argument expansion = %q", got)
	}
}

func TestAutomationFeedbackFormattingAndPolicies(t *testing.T) {
	result := hook.AutomationResult{
		Status:  hook.AutomationStatusFailed,
		Summary: "lint failed",
		Body:    "line1\nline2\nline3",
	}
	formatted := formatAutomationFeedback(hook.HookDef{Name: "lint", ResultFormat: hook.ResultFormatTail, MaxResultLines: 2}, result)
	for _, want := range []string{"[hook:lint]", "status: failed", "summary: lint failed", "line2\nline3"} {
		if !strings.Contains(formatted, want) {
			t.Fatalf("formatted feedback = %q, want substring %q", formatted, want)
		}
	}
	if strings.Contains(formatted, "line1") {
		t.Fatalf("formatted feedback = %q, tail format should omit first line", formatted)
	}

	if got := selectAutomationBody(hook.HookDef{ResultFormat: hook.ResultFormatFull}, result); got != result.Body {
		t.Fatalf("full body = %q", got)
	}
	if got := selectAutomationBody(hook.HookDef{}, result); got != result.Summary {
		t.Fatalf("default summary body = %q", got)
	}
	if got := trimAutomationBody("a\nb\nc", 2, 100); got != "a\nb" {
		t.Fatalf("trim lines = %q", got)
	}
	if got := trimAutomationBody("abcdef", 50, 3); got != "abc\n... (truncated)" {
		t.Fatalf("trim bytes = %q", got)
	}

	if !shouldAppendAutomationResult(hook.HookDef{}, hook.AutomationResult{AppendContext: true}) {
		t.Fatal("AppendContext should force append")
	}
	if !shouldAppendAutomationResult(hook.HookDef{Result: hook.ResultAlwaysAppend}, hook.AutomationResult{}) {
		t.Fatal("always_append should append")
	}
	if !shouldAppendAutomationResult(hook.HookDef{Result: hook.ResultAppendOnFailure}, hook.AutomationResult{Status: hook.AutomationStatusFailed}) {
		t.Fatal("append_on_failure should append failed result")
	}
	if shouldAppendAutomationResult(hook.HookDef{Result: hook.ResultAppendOnFailure}, hook.AutomationResult{Status: hook.AutomationStatusSuccess}) {
		t.Fatal("append_on_failure should not append successful result")
	}

	for _, tc := range []struct {
		severity string
		want     string
	}{
		{severity: "warning", want: "warn"},
		{severity: "warn", want: "warn"},
		{severity: "error", want: "error"},
		{severity: "", want: "info"},
	} {
		if got := hookToastLevel(hook.AutomationResult{Severity: tc.severity}); got != tc.want {
			t.Fatalf("hookToastLevel(%q) = %q, want %q", tc.severity, got, tc.want)
		}
	}
}
