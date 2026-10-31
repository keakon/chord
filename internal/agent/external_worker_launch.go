package agent

import (
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"sync"

	"github.com/keakon/chord/internal/texttruncate"
)

// externalStderrTailLines bounds the redacted stderr tail kept per run.
const externalStderrTailLines = 8

// externalStderrTail keeps the last redacted stderr lines of the current run so
// a failed launch can show the CLI's own reason instead of a bare
// "initialization failed". Only failed starts consult it.
type externalStderrTail struct {
	mu    sync.Mutex
	lines []string
}

func (t *externalStderrTail) add(line string) {
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.lines = append(t.lines, line)
	if len(t.lines) > externalStderrTailLines {
		t.lines = t.lines[len(t.lines)-externalStderrTailLines:]
	}
}

func (t *externalStderrTail) tail() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]string(nil), t.lines...)
}

func (t *externalStderrTail) reset() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.lines = nil
}

// externalCommandMissingError reports a preflight command lookup miss. It
// carries no CLI output because the CLI never ran.
type externalCommandMissingError struct {
	command string
}

func (e *externalCommandMissingError) Error() string {
	return fmt.Sprintf("external agent command %q not found in PATH; install the Claude Code CLI and sign in (https://code.claude.com/docs/en/setup)", e.command)
}

// externalAuthError reports a launch the CLI refused because it has no usable
// login or credentials.
type externalAuthError struct {
	detail string
}

func (e *externalAuthError) Error() string {
	msg := "external agent is not signed in; run `claude auth login` in a terminal, or provide credentials through backend.env"
	if e.detail != "" {
		msg += ": " + e.detail
	}
	return msg
}

// externalAuthMarkers are the substrings the CLI prints when it cannot
// authenticate. Matching is lowercase and best-effort: the classified guidance
// replaces a bare wrap like "start external agent: exit status 1", while quota
// or billing messages the CLI already states precisely pass through unclassified.
var externalAuthMarkers = []string{
	"/login",
	"invalid api key",
	"not logged in",
	"logged out",
	"oauth token",
	"authentication",
	"unauthorized",
}

func authFailureFromStderr(tail []string) bool {
	if len(tail) == 0 {
		return false
	}
	joined := strings.ToLower(strings.Join(tail, "\n"))
	for _, marker := range externalAuthMarkers {
		if strings.Contains(joined, marker) {
			return true
		}
	}
	return false
}

// preflightExternalCommand verifies the backend command resolves before any
// durable run state is written, so a missing CLI fails before the launch would
// leave a run record and supervisor receipt behind.
func (w *externalWorker) preflightExternalCommand() error {
	command := w.backend.EffectiveCommand()
	if _, err := exec.LookPath(command); err != nil {
		return &externalCommandMissingError{command: command}
	}
	return nil
}

// externalLaunchFailure renders a startSession failure for the user. The first
// result is the task-failure reason (task record, mailbox card); the second,
// when non-empty, is a warn toast for the classified guidance cases.
func (w *externalWorker) externalLaunchFailure(err error) (reason, toast string) {
	if missing, ok := errors.AsType[*externalCommandMissingError](err); ok {
		return missing.Error(), missing.Error()
	}
	tail := w.stderrTail.tail()
	if authFailureFromStderr(tail) {
		authErr := &externalAuthError{detail: lastExternalStderrLine(tail)}
		return authErr.Error(), authErr.Error()
	}
	return withExternalStderrTail(err, tail), ""
}

// withExternalStderrTail appends the run's last redacted stderr line when the
// error text carries none of it, so "exit status 1" style wraps still name the
// CLI's own complaint.
func withExternalStderrTail(err error, tail []string) string {
	msg := err.Error()
	detail := lastExternalStderrLine(tail)
	if detail == "" || strings.Contains(msg, detail) {
		return msg
	}
	return msg + "; last stderr: " + detail
}

func lastExternalStderrLine(tail []string) string {
	for _, entry := range slices.Backward(tail) {
		line := strings.TrimSpace(entry)
		if line == "" {
			continue
		}
		const maxExternalStderrDetail = 200
		return texttruncate.Bytes(line, maxExternalStderrDetail)
	}
	return ""
}
