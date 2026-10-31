package agent

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/keakon/chord/internal/config"
)

func TestPreflightExternalCommandRejectsMissingCLI(t *testing.T) {
	w := &externalWorker{backend: config.BackendConfig{Kind: config.BackendKindClaudeCode, Command: "definitely-missing-claude-binary"}}
	err := w.preflightExternalCommand()
	missing, ok := errors.AsType[*externalCommandMissingError](err)
	if !ok {
		t.Fatalf("preflight error = %v, want externalCommandMissingError", err)
	}
	if missing.command != "definitely-missing-claude-binary" {
		t.Fatalf("command = %q", missing.command)
	}
	if !strings.Contains(missing.Error(), "install") || !strings.Contains(missing.Error(), "sign in") {
		t.Fatalf("error text lacks install/sign-in guidance: %q", missing.Error())
	}
}

func TestPreflightExternalCommandAcceptsExistingScript(t *testing.T) {
	w := &externalWorker{backend: config.BackendConfig{Kind: config.BackendKindClaudeCode, Command: writeFakeClaude(t, "")}}
	if err := w.preflightExternalCommand(); err != nil {
		t.Fatalf("preflight rejected an existing executable: %v", err)
	}
}

func TestExternalLaunchFailureClassifiesAuthFromStderr(t *testing.T) {
	w := &externalWorker{}
	w.stderrTail.add("Invalid API key provided: sk-**redacted**")
	reason, toast := w.externalLaunchFailure(fmt.Errorf("start external agent: exit status 1"))
	if !strings.Contains(reason, "claude auth login") {
		t.Fatalf("reason = %q, want sign-in guidance", reason)
	}
	if toast == "" {
		t.Fatal("classified auth failure must raise a toast")
	}
}

func TestExternalLaunchFailurePassesUnclassifiedErrorsThrough(t *testing.T) {
	w := &externalWorker{}
	w.stderrTail.add("connect ECONNREFUSED 127.0.0.1:443")
	base := fmt.Errorf("start external agent: %w", errors.New("supervisor start failed"))
	reason, toast := w.externalLaunchFailure(base)
	if toast != "" {
		t.Fatalf("unclassified failure raised a toast: %q", toast)
	}
	if !strings.Contains(reason, "supervisor start failed") || !strings.Contains(reason, "ECONNREFUSED") {
		t.Fatalf("reason = %q, want the base error with the stderr tail", reason)
	}
}

func TestExternalLaunchFailureWithoutStderrKeepsBareError(t *testing.T) {
	w := &externalWorker{}
	reason, toast := w.externalLaunchFailure(errors.New("external initialization failed"))
	if toast != "" || reason != "external initialization failed" {
		t.Fatalf("reason=%q toast=%q, want the bare error and no toast", reason, toast)
	}
}

func TestExternalStderrTailBoundsAndResets(t *testing.T) {
	var tail externalStderrTail
	for i := range externalStderrTailLines + 3 {
		tail.add(fmt.Sprintf("line-%d", i))
	}
	got := tail.tail()
	if len(got) != externalStderrTailLines || got[0] != "line-3" {
		t.Fatalf("tail = %v, want the last %d lines", got, externalStderrTailLines)
	}
	tail.add("   ")
	if len(tail.tail()) != externalStderrTailLines {
		t.Fatal("blank lines must be dropped")
	}
	tail.reset()
	if len(tail.tail()) != 0 {
		t.Fatal("reset must clear the tail")
	}
}

func TestLastExternalStderrLineTruncatesLongLines(t *testing.T) {
	long := strings.Repeat("x", 500)
	got := lastExternalStderrLine([]string{"", long, "tail line"})
	if got != "tail line" {
		t.Fatalf("got %q, want the last non-blank line", got)
	}
	if truncated := lastExternalStderrLine([]string{long}); len(truncated) != 200 {
		t.Fatalf("got %d chars, want the 200-char cap", len(truncated))
	}
}

func TestStartSessionResetsStderrBeforeEarlyFailure(t *testing.T) {
	w := &externalWorker{
		parent:  &MainAgent{},
		backend: config.BackendConfig{Kind: config.BackendKindClaudeCode, Command: "definitely-missing-claude-binary"},
	}
	w.stderrTail.add("Invalid API key provided: sk-redacted")

	if _, _, err := w.startSession(externalTaskInput{}, false); err == nil {
		t.Fatal("startSession() error = nil, want a missing-command error")
	}
	if got := w.stderrTail.tail(); len(got) != 0 {
		t.Fatalf("stderr tail = %v, want it reset before preflight failure", got)
	}
}

func TestLastExternalStderrLineKeepsValidUTF8(t *testing.T) {
	line := strings.Repeat("错", 100)
	got := lastExternalStderrLine([]string{line})
	if !utf8.ValidString(got) {
		t.Fatalf("truncated line is not valid UTF-8: %q", got)
	}
	if len(got) > 200 {
		t.Fatalf("got %d bytes, want at most 200", len(got))
	}
}
