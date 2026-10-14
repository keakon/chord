package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/tools"
)

type fakeHeadlessRuntime struct {
	events  chan agent.AgentEvent
	backend headlessBackend
	closed  bool
	onClose func()
}

func (f *fakeHeadlessRuntime) Close() {
	if f.onClose != nil {
		f.onClose()
	}
	f.closed = true
}
func (f *fakeHeadlessRuntime) Events() <-chan agent.AgentEvent { return f.events }
func (f *fakeHeadlessRuntime) Backend() headlessBackend        { return f.backend }

type failingReader struct{ err error }

func (r failingReader) Read([]byte) (int, error) { return 0, r.err }

func decodeHeadlessJSONLines(t *testing.T, data []byte) []headlessEnvelope {
	t.Helper()
	var envs []headlessEnvelope
	for line := range bytes.SplitSeq(data, []byte{'\n'}) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var env headlessEnvelope
		if err := json.Unmarshal(line, &env); err != nil {
			t.Fatalf("unmarshal %q: %v", line, err)
		}
		envs = append(envs, env)
	}
	return envs
}

func TestRuntimeHeadlessAdapterNilRuntimeIsSafe(t *testing.T) {
	adapter := runtimeHeadlessAdapter{}
	adapter.Close()
	if backend := adapter.Backend(); backend != nil {
		t.Fatalf("Backend() = %#v, want nil", backend)
	}
	events := adapter.Events()
	if events == nil {
		t.Fatal("Events() returned nil channel")
	}
	if _, ok := <-events; ok {
		t.Fatal("nil runtime Events() channel should be closed")
	}
}

func TestRuntimeHeadlessAdapterUsesRuntimeAgent(t *testing.T) {
	ac := newTestAppContext(t)
	rt := &Runtime{Agent: ac.MainAgent}
	adapter := runtimeHeadlessAdapter{rt: rt}
	if got := adapter.Backend(); got != ac.MainAgent {
		t.Fatalf("Backend() = %#v, want main agent", got)
	}
	if got := adapter.Events(); got != ac.MainAgent.Events() {
		t.Fatal("Events() did not return main agent event channel")
	}
	adapter.Close()
}

func TestDefaultHeadlessRunDeps(t *testing.T) {
	deps := defaultHeadlessRunDeps()
	if deps.initApp == nil {
		t.Fatal("default initApp is nil")
	}
	if deps.createRuntime == nil {
		t.Fatal("default createRuntime is nil")
	}
	if deps.stdin != os.Stdin {
		t.Fatal("default stdin should be os.Stdin")
	}
	if deps.stdout != os.Stdout {
		t.Fatal("default stdout should be os.Stdout")
	}
	if !deps.watchParent {
		t.Fatal("default parent watcher should be enabled")
	}
	if deps.parentCheckInterval != time.Second {
		t.Fatalf("parentCheckInterval = %v, want %v", deps.parentCheckInterval, time.Second)
	}
	if deps.getppid == nil || deps.getppid() <= 0 {
		t.Fatal("default getppid should return a process id")
	}
}

func TestDefaultHeadlessRunDepsCreateRuntimePropagatesRuntimeError(t *testing.T) {
	deps := defaultHeadlessRunDeps()
	rt, err := deps.createRuntime(&AppContext{})
	if err == nil || rt != nil {
		t.Fatalf("createRuntime empty app context = (%#v, %v), want nil runtime and error", rt, err)
	}
	if !strings.Contains(err.Error(), "runtime requires an initialized main agent") {
		t.Fatalf("unexpected createRuntime error: %v", err)
	}
}

func TestRunHeadlessWithDepsEmitsReadyAndExitsOnStdinClose(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ac := &AppContext{Ctx: ctx, Cancel: cancel, SessionDir: filepath.Join(t.TempDir(), "session-abc")}
	events := make(chan agent.AgentEvent)
	close(events)
	rt := &fakeHeadlessRuntime{events: events, backend: &mockBackend{}}
	var stdout bytes.Buffer

	err := runHeadlessWithDeps(headlessRunDeps{
		initApp: func(bool, string, sessionStartupOptions) (*AppContext, error) { return ac, nil },
		createRuntime: func(*AppContext) (headlessRuntime, error) {
			return rt, nil
		},
		stdin:       strings.NewReader(""),
		stdout:      &stdout,
		watchParent: false,
	})
	if err != nil {
		t.Fatalf("runHeadlessWithDeps: %v", err)
	}
	if !rt.closed {
		t.Fatal("runtime was not closed")
	}
	envs := decodeHeadlessJSONLines(t, stdout.Bytes())
	ready := findHeadlessEnvelopeValue(envs, "ready")
	if ready == nil {
		t.Fatalf("ready envelope not found in %+v", envs)
	}
	payload, ok := ready.Payload.(map[string]any)
	if !ok || payload["session_id"] != "session-abc" {
		t.Fatalf("ready payload = %#v, want session_id session-abc", ready.Payload)
	}
}

func TestRunHeadlessWithDepsDrainsEventsBeforeClosingOutput(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ac := &AppContext{Ctx: ctx, Cancel: cancel, SessionDir: filepath.Join(t.TempDir(), "session-drain")}
	events := make(chan agent.AgentEvent, 1)
	events <- agent.ContextNoticeEvent{Level: "pressure", Message: "context pressure", MessageIndex: 1}
	close(events)
	rt := &fakeHeadlessRuntime{events: events, backend: &mockBackend{}}
	var stdout bytes.Buffer

	err := runHeadlessWithDeps(headlessRunDeps{
		initApp: func(bool, string, sessionStartupOptions) (*AppContext, error) { return ac, nil },
		createRuntime: func(*AppContext) (headlessRuntime, error) {
			return rt, nil
		},
		stdin:       strings.NewReader(""),
		stdout:      &stdout,
		watchParent: false,
	})
	if err != nil {
		t.Fatalf("runHeadlessWithDeps: %v", err)
	}
	if findHeadlessEnvelopeValue(decodeHeadlessJSONLines(t, stdout.Bytes()), "context_notice") == nil {
		t.Fatal("event emitted before shutdown was lost")
	}
}

func TestRunHeadlessWithDepsReportsScannerError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ac := &AppContext{Ctx: ctx, Cancel: cancel, SessionDir: filepath.Join(t.TempDir(), "session-scan")}
	events := make(chan agent.AgentEvent)
	close(events)
	var stdout bytes.Buffer

	err := runHeadlessWithDeps(headlessRunDeps{
		initApp: func(bool, string, sessionStartupOptions) (*AppContext, error) { return ac, nil },
		createRuntime: func(*AppContext) (headlessRuntime, error) {
			return &fakeHeadlessRuntime{events: events, backend: &mockBackend{}}, nil
		},
		stdin:       failingReader{err: errors.New("scan boom")},
		stdout:      &stdout,
		watchParent: false,
	})
	if err != nil {
		t.Fatalf("runHeadlessWithDeps: %v", err)
	}
	envs := decodeHeadlessJSONLines(t, stdout.Bytes())
	if findHeadlessEnvelopeValue(envs, "ready") == nil {
		t.Fatalf("ready envelope not found in %+v", envs)
	}
	errEnv := findHeadlessEnvelopeValue(envs, "error")
	if errEnv == nil {
		t.Fatalf("error envelope not found in %+v", envs)
	}
	payload, _ := errEnv.Payload.(map[string]any)
	msg, _ := payload["message"].(string)
	if !strings.Contains(msg, "stdin read error: scan boom") {
		t.Fatalf("error payload = %#v", errEnv.Payload)
	}
}

func TestReadHeadlessStdinLinesReportsTooLongAndContinues(t *testing.T) {
	input := strings.Repeat("x", headlessStdinMaxLineBytes+1) + "\n{}\n"
	lines := make(chan headlessStdinLine, 4)
	readHeadlessStdinLines(context.Background(), strings.NewReader(input), lines)

	first, ok := <-lines
	if !ok {
		t.Fatal("expected too-long error")
	}
	if first.code != "stdin_line_too_long" || first.err == nil || first.fatal {
		t.Fatalf("first line = %+v, want recoverable stdin_line_too_long", first)
	}
	second, ok := <-lines
	if !ok {
		t.Fatal("expected reader to continue after too-long line")
	}
	if string(second.line) != "{}" || second.err != nil {
		t.Fatalf("second line = %+v, want valid JSON line", second)
	}
}

func TestReadHeadlessStdinLinesKeepsLineContentsIndependent(t *testing.T) {
	// A line larger than the 64KB reader window forces several buffer refills,
	// so the bufio buffer is recycled before the next line arrives; each
	// delivered line must keep its own bytes.
	longLine := strings.Repeat("a", 70*1024)
	lines := make(chan headlessStdinLine, 4)
	readHeadlessStdinLines(context.Background(), strings.NewReader(longLine+"\n"+"b\n"), lines)

	first, ok := <-lines
	if !ok {
		t.Fatal("expected first line")
	}
	if string(first.line) != longLine || first.err != nil {
		t.Fatalf("first line mutated after later reads: len=%d err=%v", len(first.line), first.err)
	}
	second, ok := <-lines
	if !ok {
		t.Fatal("expected second line")
	}
	if string(second.line) != "b" || second.err != nil {
		t.Fatalf("second line = %q err=%v, want %q", second.line, second.err, "b")
	}
}

func TestHeadlessParentWatcherCancelsOnParentChange(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ac := &AppContext{Ctx: ctx, Cancel: cancel}
	calls := make(chan int, 4)
	getppid := func() int {
		calls <- 42
		return 42
	}
	startHeadlessParentWatcher(ac, 7, time.Millisecond, getppid)
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("parent watcher did not cancel context after parent changed")
	}
}

func TestRunHeadlessWithDepsClosesRuntimeBeforeDeferredAppClose(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ac := &AppContext{Ctx: ctx, Cancel: cancel, SessionDir: filepath.Join(t.TempDir(), "session-close-order")}
	var closeOrderMu sync.Mutex
	closeOrder := make([]string, 0, 2)
	origCancel := ac.Cancel
	ac.Cancel = func() {
		closeOrderMu.Lock()
		closeOrder = append(closeOrder, "app")
		closeOrderMu.Unlock()
		origCancel()
	}
	// AppContext.Close is a method, so verify ordering by observing runtime close
	// before context cancellation triggered by deferred ac.Close().
	events := make(chan agent.AgentEvent)
	close(events)
	rt := &fakeHeadlessRuntime{
		events:  events,
		backend: &mockBackend{},
		onClose: func() {
			closeOrderMu.Lock()
			closeOrder = append(closeOrder, "runtime")
			closeOrderMu.Unlock()
		},
	}
	var stdout bytes.Buffer

	err := runHeadlessWithDeps(headlessRunDeps{
		initApp: func(bool, string, sessionStartupOptions) (*AppContext, error) { return ac, nil },
		createRuntime: func(*AppContext) (headlessRuntime, error) {
			return rt, nil
		},
		stdin:       strings.NewReader(""),
		stdout:      &stdout,
		watchParent: false,
	})
	if err != nil {
		t.Fatalf("runHeadlessWithDeps: %v", err)
	}
	closeOrderMu.Lock()
	defer closeOrderMu.Unlock()
	if len(closeOrder) < 3 {
		t.Fatalf("close order = %#v, want stdin-cancel, runtime close, final app close", closeOrder)
	}
	// Transport shutdown can cancel the context again. Observe cancellation on
	// both sides of runtime close without assuming there is only one final call.
	for idx, label := range closeOrder {
		if label == "runtime" && idx > 0 && idx < len(closeOrder)-1 && closeOrder[idx-1] == "app" && closeOrder[idx+1] == "app" {
			return
		}
	}
	t.Fatalf("close order = %#v, want context cancellation before and after runtime close", closeOrder)
}

func TestRunHeadlessWithDepsReportsRuntimeInitFailureAndClosesApp(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ac := &AppContext{Ctx: ctx, Cancel: cancel, SessionDir: filepath.Join(t.TempDir(), "session-init-fail")}
	closed := make(chan struct{}, 1)
	origCancel := ac.Cancel
	ac.Cancel = func() {
		select {
		case closed <- struct{}{}:
		default:
		}
		origCancel()
	}

	err := runHeadlessWithDeps(headlessRunDeps{
		initApp: func(bool, string, sessionStartupOptions) (*AppContext, error) { return ac, nil },
		createRuntime: func(*AppContext) (headlessRuntime, error) {
			return nil, errors.New("runtime boom")
		},
		stdin:       strings.NewReader(""),
		stdout:      &bytes.Buffer{},
		watchParent: false,
	})
	if err == nil || !strings.Contains(err.Error(), "runtime boom") {
		t.Fatalf("runHeadlessWithDeps err = %v, want runtime boom", err)
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("app context close/cancel was not triggered on runtime init failure")
	}
}

func TestRunHeadlessWithDepsParentWatcherIsolationDisabled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ac := &AppContext{Ctx: ctx, Cancel: cancel, SessionDir: filepath.Join(t.TempDir(), "session-no-watch")}
	events := make(chan agent.AgentEvent)
	close(events)
	var getppidCalls int
	var stdout bytes.Buffer

	err := runHeadlessWithDeps(headlessRunDeps{
		initApp: func(bool, string, sessionStartupOptions) (*AppContext, error) { return ac, nil },
		createRuntime: func(*AppContext) (headlessRuntime, error) {
			return &fakeHeadlessRuntime{events: events, backend: &mockBackend{}}, nil
		},
		stdin:       strings.NewReader(""),
		stdout:      &stdout,
		watchParent: false,
		getppid: func() int {
			getppidCalls++
			return 1
		},
	})
	if err != nil {
		t.Fatalf("runHeadlessWithDeps: %v", err)
	}
	if getppidCalls != 0 {
		t.Fatalf("getppid calls = %d, want 0 when parent watcher disabled", getppidCalls)
	}
}

// A headless local shell command that outgrows the capture cap must keep its
// newest output, the same tail the shell tool keeps: the failure that made the
// output long lands at the end, so a stale head would hide exactly what the
// integration needs.
func TestRunHeadlessLocalShellKeepsTheNewestOutput(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not in PATH")
	}
	if _, err := exec.LookPath("yes"); err != nil {
		t.Skip("yes not in PATH")
	}
	// Well past the capture cap, with a marker at the very end.
	out, err := tools.RunLocalShellCapture(context.Background(), "", "yes x | head -c 600000; printf END_MARKER")
	if err != nil {
		t.Fatalf("RunLocalShellCapture: %v", err)
	}
	if !strings.Contains(out, "END_MARKER") {
		t.Fatalf("captured output dropped the newest bytes (len=%d)", len(out))
	}
	if !strings.HasPrefix(out, "...(output truncated:") {
		t.Fatalf("captured output is missing the truncation notice: %.60q", out)
	}
	if len(out) >= 600000 {
		t.Fatalf("captured output len = %d, want the window bounded below the produced size", len(out))
	}
}
