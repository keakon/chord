package tools

import (
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
)

func resetJobRegistryOnlyForTest(t *testing.T) {
	t.Helper()
	restore := ResetJobRegistryForTest()
	t.Cleanup(restore)
}

func parseBackgroundJobID(t *testing.T, out string) string {
	t.Helper()
	after, ok := strings.CutPrefix(out, "[background job ")
	if !ok {
		t.Fatalf("background job handle missing prefix:\n%s", out)
	}
	id, _, ok := strings.Cut(after, "]")
	if !ok || strings.TrimSpace(id) == "" {
		t.Fatalf("background job handle missing id:\n%s", out)
	}
	return strings.TrimSpace(id)
}

func TestAutoBackgroundAllowedExceptions(t *testing.T) {
	cases := []struct {
		command string
		want    bool
	}{
		{command: "go test ./...", want: true},
		{command: "sh -c 'sleep 1'", want: true},
		{command: "sleep 5", want: false},
		{command: "git status", want: false},
		// A promotable command chained with a short trailing query must still
		// promote: the very common "run the suite, then show the diff" shape
		// must not lose promotion because of its last clause.
		{command: "go test ./... && git status", want: true},
		{command: "npm test && git diff --stat", want: true},
		{command: "sleep 5 && git status", want: false},
		// Git network and maintenance operations legitimately run for minutes.
		{command: "git clone https://example.com/repo.git", want: true},
		{command: "git fetch origin", want: true},
		{command: "git -C /tmp submodule update --init", want: true},
		{command: "git gc", want: true},
		{command: `echo "unterminated`, want: false},
		{command: "", want: false},
	}
	for _, tc := range cases {
		if got := autoBackgroundAllowed(tc.command); got != tc.want {
			t.Errorf("autoBackgroundAllowed(%q) = %v, want %v", tc.command, got, tc.want)
		}
	}
}

// A detached job is started precisely because nobody waits for it, so it must
// not inherit the foreground default deadline: a watcher would otherwise be
// SIGKILLed ten minutes in without the model ever asking for a deadline.
func TestShellBackgroundWithoutTimeoutHasNoHardDeadline(t *testing.T) {
	resetJobRegistryOnlyForTest(t)
	out, err := ShellTool{}.Execute(context.Background(), mustMarshal(t, map[string]any{
		"command":           "sh -c 'sleep 30'",
		"run_in_background": true,
	}))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	id := parseBackgroundJobID(t, out)
	t.Cleanup(func() { globalJobRegistry.kill(id, "test cleanup") })
	j, ok := globalJobRegistry.get(id)
	if !ok {
		t.Fatalf("job %s not registered", id)
	}
	if j.MaxRuntimeSec != 0 {
		t.Fatalf("MaxRuntimeSec = %d, want 0 for a detached job with no timeout_ms", j.MaxRuntimeSec)
	}
	if strings.Contains(out, "deadline:") {
		t.Fatalf("handle advertises a deadline that does not exist:\n%s", out)
	}
}

func TestShellHardDeadlineKillsCommandBeforeYield(t *testing.T) {
	// timeout_ms is tighter than yield_time_ms, so promotion is disabled and the
	// command dies on its deadline instead of being handed back about to die.
	out, err := ShellTool{}.Execute(context.Background(), mustMarshal(t, map[string]any{
		"command":       "sh -c 'sleep 2'",
		"timeout_ms":    1000,
		"yield_time_ms": 60000,
	}))
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if out != "" {
		t.Fatalf("expected no output from timed-out command, got %q", out)
	}
	if !strings.Contains(err.Error(), "timed out after 1s") {
		t.Fatalf("expected effective timeout in error, got %v", err)
	}
	// A bare "timed out" invites the same command unchanged, which would hit the
	// same deadline; the error must name the escape hatches.
	if !strings.Contains(err.Error(), "run_in_background: true") {
		t.Fatalf("timeout error must steer away from an unchanged re-run, got %v", err)
	}
	if strings.Contains(out, "[background job ") {
		t.Fatalf("command shorter than the yield budget must not be promoted: %q", out)
	}
}

// A command excluded from auto-promotion (`sleep`, `git`, an unparseable
// command) has no yield timer to hand it off, so timeout_ms: 0 must not leave
// the foreground wait with no deadline at all — it would block the turn until
// the process exited.
func TestShellZeroTimeoutKeepsDefaultDeadlineForNonPromotableForeground(t *testing.T) {
	resetJobRegistryOnlyForTest(t)
	t.Cleanup(func() { StopAllJobsForShutdown() })

	if _, err := (ShellTool{}).Execute(context.Background(), mustMarshal(t, map[string]any{
		"command":    "sleep 0",
		"timeout_ms": 0,
	})); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	states := SnapshotJobs()
	if len(states) != 1 {
		t.Fatalf("jobs = %d, want the single foreground job", len(states))
	}
	if got, want := states[0].MaxRuntimeSec, ShellDefaultTimeoutMs/1000; got != want {
		t.Fatalf("foreground non-promotable MaxRuntimeSec = %d, want the default %d", got, want)
	}
}

// Switching promotion off (yield_time_ms: 0) leaves the command with no yield timer
// to hand it off either, so timeout_ms: 0 must not leave the foreground wait
// with no deadline at all. A promotable command reaches that state too, so the
// cap cannot be limited to the commands excluded from auto-promotion.
func TestShellZeroTimeoutWithPromotionDisabledKeepsDefaultDeadline(t *testing.T) {
	resetJobRegistryOnlyForTest(t)
	t.Cleanup(func() { StopAllJobsForShutdown() })

	out, err := (ShellTool{}).Execute(context.Background(), mustMarshal(t, map[string]any{
		// A promotable shape (nothing here is a deliberate wait or a short git
		// query) that still exits immediately.
		"command":       "sh -c 'true'",
		"timeout_ms":    0,
		"yield_time_ms": 0,
	}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if strings.Contains(out, "[background job ") {
		t.Fatalf("yield_time_ms:0 must not promote: %q", out)
	}
	states := SnapshotJobs()
	if len(states) != 1 {
		t.Fatalf("jobs = %d, want the single foreground job", len(states))
	}
	if got, want := states[0].MaxRuntimeSec, ShellDefaultTimeoutMs/1000; got != want {
		t.Fatalf("foreground MaxRuntimeSec = %d, want the default %d", got, want)
	}
}

// The detached path is the one place "no deadline" is legitimate: the call
// returns immediately and the job is managed by id.
func TestShellZeroTimeoutKeepsDetachedJobWithoutDeadline(t *testing.T) {
	resetJobRegistryOnlyForTest(t)
	t.Cleanup(func() { StopAllJobsForShutdown() })

	out, err := (ShellTool{}).Execute(context.Background(), mustMarshal(t, map[string]any{
		"command":           "sleep 5",
		"timeout_ms":        0,
		"run_in_background": true,
	}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	j, ok := globalJobRegistry.get(parseBackgroundJobID(t, out))
	if !ok {
		t.Fatalf("background job not found in %q", out)
	}
	if j.MaxRuntimeSec != 0 {
		t.Fatalf("detached MaxRuntimeSec = %d, want 0 (no deadline)", j.MaxRuntimeSec)
	}
}

func TestShellPromotesLongCommandToBackground(t *testing.T) {
	resetJobRegistryOnlyForTest(t)
	t.Cleanup(func() { StopAllJobsForShutdown() })
	out, err := ShellTool{}.Execute(context.Background(), mustMarshal(t, map[string]any{
		"command":       "sh -c 'sleep 2'",
		"timeout_ms":    5000,
		"yield_time_ms": 50,
	}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "[background job ") || !strings.Contains(out, "foreground budget") {
		t.Fatalf("output = %q, want a background job handle", out)
	}
	id := parseBackgroundJobID(t, out)
	if j, ok := globalJobRegistry.get(id); !ok || j.isFinished() {
		t.Fatalf("job %s should still be running after promotion", id)
	}
}

func TestShellYieldDisabledKeepsCommandInForeground(t *testing.T) {
	out, err := ShellTool{}.Execute(context.Background(), mustMarshal(t, map[string]any{
		"command":       "sh -c 'sleep 0.2'; echo done",
		"yield_time_ms": 0,
	}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if strings.Contains(out, "[background job ") {
		t.Fatalf("yield_time_ms:0 must not promote: %q", out)
	}
	if !strings.Contains(out, "done") {
		t.Fatalf("output = %q, want command output", out)
	}
}

func TestShellRunInBackgroundReturnsHandle(t *testing.T) {
	resetJobRegistryOnlyForTest(t)
	t.Cleanup(func() { StopAllJobsForShutdown() })
	out, err := ShellTool{}.Execute(context.Background(), mustMarshal(t, map[string]any{
		"command":           "sh -c 'sleep 1'",
		"run_in_background": true,
	}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "started in the background") {
		t.Fatalf("output = %q, want background handle", out)
	}
	id := parseBackgroundJobID(t, out)
	if j, ok := globalJobRegistry.get(id); !ok || j.isFinished() {
		t.Fatalf("job %s should be running", id)
	}
}

func TestShellReadOnlyAllowlistCoversNewCommands(t *testing.T) {
	allowed := []string{
		"head -n 5 file", "tail -n 5 file", "wc -l file", "stat file", "file x", "file -b x", "file --mime-type x", "du -sh .", "df -h",
	}
	for _, command := range allowed {
		if !shellReadOnlyCommandAllowed(mustMarshal(t, map[string]any{"command": command})) {
			t.Errorf("shellReadOnlyCommandAllowed(%q) = false, want true", command)
		}
	}
	denied := []string{
		"tail -f log", "tail -F log", "tail --follow log", "tail --follow=name log", "tail -fn 100 log", "rm -rf x", "go test ./...", "cat file > out",
		// file -C/--compile writes magic.mgc, so it is not a read.
		"file -C", "file --compile", "file -Cm custom.magic", "file --compile --magic custom.magic",
	}
	for _, command := range denied {
		if shellReadOnlyCommandAllowed(mustMarshal(t, map[string]any{"command": command})) {
			t.Errorf("shellReadOnlyCommandAllowed(%q) = true, want false", command)
		}
	}
	if shellReadOnlyCommandAllowed(mustMarshal(t, map[string]any{"command": "git status --short", "run_in_background": true})) {
		t.Error("shellReadOnlyCommandAllowed(background git status) = true, want false")
	}
}

func TestShellExitErrorReportsSignal(t *testing.T) {
	cmd := exec.Command("sh", "-c", "kill -TERM $$")
	err := cmd.Run()
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("Run error = %T %v, want *exec.ExitError", err, err)
	}
	if got := shellExitErrorForCommand("", exitErr, "").Error(); !strings.Contains(got, "signal:") || !strings.Contains(got, "terminated") {
		t.Fatalf("shellExitErrorForCommand = %q, want signal termination", got)
	}
}

func TestBashExecutesReadWithClosedStdinInsteadOfRejecting(t *testing.T) {
	tool := ShellTool{}
	command := "printf 'stdin_tty='; test -t 0 && echo yes || echo no; printf 'read_result='; IFS= read -r x; printf 'status:%s value:%s\\n' \"$?\" \"$x\""
	out, err := tool.Execute(context.Background(), mustMarshal(t, map[string]any{
		"command":    command,
		"timeout_ms": 5000,
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v\noutput: %s", err, out)
	}
	for _, want := range []string{"stdin_tty=no", "read_result=status:1 value:"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output = %q, want substring %q", out, want)
		}
	}
}

func TestShellRejectsInvalidWorkdirBeforeStartingCommand(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(file, []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name    string
		workdir string
		want    string
	}{
		{name: "missing", workdir: filepath.Join(dir, "missing"), want: "path not found"},
		{name: "regular file", workdir: file, want: "path is not a directory"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := (ShellTool{}).Execute(context.Background(), mustMarshal(t, map[string]any{
				"command": "pwd",
				"workdir": tc.workdir,
			}))
			if err == nil {
				t.Fatal("expected invalid workdir error")
			}
			for _, want := range []string{"invalid workdir", tc.want, tc.workdir} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("error %q missing %q", err, want)
				}
			}
			if strings.Contains(err.Error(), "starting command") || strings.Contains(err.Error(), "fork/exec") {
				t.Fatalf("invalid workdir reached process startup: %v", err)
			}
		})
	}
}

func TestBashDescriptionIncludesToolSpecificHintsOnlyWhenVisible(t *testing.T) {
	tool := ShellTool{}

	withoutHelpers := tool.DescriptionForTools(nil)
	if strings.Contains(withoutHelpers, "prefer them") {
		t.Fatalf("unexpected helper hint without visible tools: %q", withoutHelpers)
	}
	if strings.Contains(withoutHelpers, "rather than spawn") || strings.Contains(withoutHelpers, "spawn tool") {
		t.Fatalf("description still references the removed spawn tool: %q", withoutHelpers)
	}
	for _, want := range []string{
		"This tool is non-interactive: stdin is not provided, Unix commands run without a controlling TTY. Do not run interactive commands (login wizards, editors, TUIs, password prompts); obvious interactive commands are rejected before execution.",
		"Use shell mainly for tests, builds, git, and other system commands.",
		"Prefer the smallest safe number of tool calls.",
		"shell is appropriate when one direct command is clearly simpler and more atomic, such as move/rename, copy, mkdir, or archive/unarchive.",
		"If file reading, search, code-navigation, or file-editing tools are hidden or denied in this role, shell is not a substitute for them; do not simulate those capabilities with shell commands or inline scripts.",
		"For explicit file deletions, prefer `delete`; use shell removal only when shell semantics are actually required, such as directory trees or batch cleanup.",
		"Do not use shell redirection, heredocs, inline scripts, or `rm` as the default way to edit, write, or delete files when dedicated file tools are unavailable.",
		"This tool also runs background jobs. Set run_in_background:true for services or work you do not need to wait for",
		"Long one-shot commands (builds, test suites) are promoted to a background job after the yield budget (default 90s)",
		"Dependent commands must run in order",
		"Only set timeout_ms when you need a hard deadline other than the foreground default of 600000ms",
		"a job started with run_in_background:true has none until you set one, and accepts up to 21600000 for hour-scale work",
		"only set yield_time_ms when you need a foreground budget other than the default 90000ms.",
	} {
		if !strings.Contains(withoutHelpers, want) {
			t.Fatalf("missing guidance %q in %q", want, withoutHelpers)
		}
	}

	withHelpers := tool.DescriptionForTools(map[string]struct{}{
		NameLsp:  {},
		NameGrep: {},
		NameGlob: {},
		NameRead: {},
	})
	for _, want := range []string{
		"use LSP first for symbol-aware navigation",
		"use Grep for repo text search before reaching for rg",
		"use Glob for file or path discovery before reaching for rg --files or find",
		"use Read once you have narrowed the target files",
	} {
		if !strings.Contains(withHelpers, want) {
			t.Fatalf("missing helper hint %q in %q", want, withHelpers)
		}
	}
}

func TestShellParametersExposeYieldAndBackgroundControls(t *testing.T) {
	params := ShellTool{}.Parameters()
	props, ok := params["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties has unexpected type %T", params["properties"])
	}

	if _, ok := props["run_in_background"]; !ok {
		t.Fatal("Shell Parameters() must expose run_in_background")
	}
	timeoutProp, ok := props["timeout_ms"].(map[string]any)
	if !ok {
		t.Fatalf("timeout_ms has unexpected type %T", props["timeout_ms"])
	}
	timeoutDesc, _ := timeoutProp["description"].(string)
	if !strings.Contains(timeoutDesc, "0 means no deadline") {
		t.Fatalf("timeout_ms description missing no-deadline guidance in %q", timeoutDesc)
	}
	yieldProp, ok := props["yield_time_ms"].(map[string]any)
	if !ok {
		t.Fatalf("yield_time_ms has unexpected type %T", props["yield_time_ms"])
	}
	yieldDesc, _ := yieldProp["description"].(string)
	if !strings.Contains(yieldDesc, "default 90000") {
		t.Fatalf("yield_time_ms description missing default in %q", yieldDesc)
	}
	for _, want := range []string{
		"use it when this turn needs the result and the command fits the foreground deadline",
		"cancelling the turn kills the command",
	} {
		if !strings.Contains(yieldDesc, want) {
			t.Fatalf("yield_time_ms description missing %q in %q", want, yieldDesc)
		}
	}
}

func TestJobKillDescriptionClarifiesLifecycle(t *testing.T) {
	desc := JobKillTool{}.Description()
	for _, want := range []string{"SIGTERM", "grace period", "does not produce a completion notification"} {
		if !strings.Contains(desc, want) {
			t.Fatalf("description missing %q in %q", want, desc)
		}
	}
	params := JobKillTool{}.Parameters()
	props, ok := params["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties has unexpected type %T", params["properties"])
	}
	idProp, ok := props["job_id"].(map[string]any)
	if !ok {
		t.Fatalf("job_id has unexpected type %T", props["job_id"])
	}
	idDesc, _ := idProp["description"].(string)
	if !strings.Contains(idDesc, "job id") {
		t.Fatalf("job_id description missing job wording in %q", idDesc)
	}
}

func TestStopAllJobsForAgentStopsOnlyMatchingOwner(t *testing.T) {
	resetJobRegistryOnlyForTest(t)
	job1, err := globalJobRegistry.start(WithAgentID(context.Background(), "sub-1"), jobStartRequest{Command: "sh -c 'sleep 5'", Description: "Sub job"})
	if err != nil {
		t.Fatalf("start sub background: %v", err)
	}
	job2, err := globalJobRegistry.start(WithAgentID(context.Background(), "sub-2"), jobStartRequest{Command: "sh -c 'sleep 5'", Description: "Other service"})
	if err != nil {
		t.Fatalf("start other background: %v", err)
	}

	start := time.Now()
	stopped := StopAllJobsForAgent("sub-1", "terminated on session switch")
	elapsed := time.Since(start)
	if stopped != 1 {
		t.Fatalf("stopped = %d, want 1", stopped)
	}
	if elapsed >= 2*time.Second {
		t.Fatalf("StopAllJobsForAgent took %v, want quick cancellation well before natural exit", elapsed)
	}
	if !job1.isFinished() {
		t.Fatalf("expected %s to be terminated", job1.ID)
	}
	if job2.isFinished() {
		t.Fatalf("expected %s to remain running", job2.ID)
	}
	_, _ = JobKillTool{}.Execute(WithAgentID(context.Background(), "sub-2"), mustMarshal(t, map[string]any{"job_id": job2.ID}))
}

func TestStopAllJobsForShutdownStopsAll(t *testing.T) {
	resetJobRegistryOnlyForTest(t)
	if _, err := globalJobRegistry.start(context.Background(), jobStartRequest{Command: "sh -c 'sleep 5'", Description: "job one"}); err != nil {
		t.Fatalf("start background 1: %v", err)
	}
	if _, err := globalJobRegistry.start(context.Background(), jobStartRequest{Command: "sh -c 'sleep 5'", Description: "service one"}); err != nil {
		t.Fatalf("start background 2: %v", err)
	}
	stopped := StopAllJobsForShutdown()
	if stopped != 2 {
		t.Fatalf("stopped = %d, want 2", stopped)
	}
	for _, state := range SnapshotJobs() {
		if state.Status == string(jobStatusRunning) || state.Status == string(jobStatusStopping) {
			t.Fatalf("job %s status = %s, want terminal", state.ID, state.Status)
		}
	}
}

func mustMarshal(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func TestBashExecuteUsesDetectedShell(t *testing.T) {
	testCases := []struct {
		name      string
		shellType string
		command   string
		want      string
	}{
		{"bash default", "bash", "echo hello", "hello"},
		{"posix sh", "posix", "echo hello", "hello"},
		{"unknown falls back to bash", "unknown", "echo hello", "hello"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			tool := NewShellTool(tc.shellType)
			out, err := tool.Execute(context.Background(), mustMarshal(t, map[string]any{
				"command": tc.command,
			}))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !strings.Contains(out, tc.want) {
				t.Fatalf("output = %q, want to contain %q", out, tc.want)
			}
		})
	}
}

func TestBashRejectsInteractiveCommandBeforeExecution(t *testing.T) {
	out, err := ShellTool{}.Execute(context.Background(), mustMarshal(t, map[string]any{
		"command": "git rebase -i HEAD~2",
	}))
	if err == nil {
		t.Fatal("expected interactive command rejection")
	}
	if out != "" {
		t.Fatalf("output = %q, want empty", out)
	}
	if !strings.Contains(err.Error(), "interactive command rejected") || !strings.Contains(err.Error(), "git rebase -i") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestBashReadFromStdinSeesEOF(t *testing.T) {
	start := time.Now()
	out, err := ShellTool{}.Execute(context.Background(), mustMarshal(t, map[string]any{
		"command":    "read x",
		"timeout_ms": 5000,
	}))
	if err == nil {
		t.Fatal("expected read to return non-zero at EOF")
	}
	if time.Since(start) >= 2*time.Second {
		t.Fatalf("read command took too long; output=%q err=%v", out, err)
	}
	if !strings.Contains(err.Error(), "exit code 1") {
		t.Fatalf("expected read to exit after EOF, got output=%q err=%v", out, err)
	}
}

func TestBashParametersCommandDescription(t *testing.T) {
	params := ShellTool{}.Parameters()
	props, ok := params["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties has unexpected type %T", params["properties"])
	}
	cmdProp, ok := props["command"].(map[string]any)
	if !ok {
		t.Fatalf("command has unexpected type %T", props["command"])
	}
	desc, _ := cmdProp["description"].(string)
	if !strings.Contains(desc, "shell command") {
		t.Fatalf("command description should say 'shell command', got %q", desc)
	}
}

func TestTailWriterConcurrentWriteAndString(t *testing.T) {
	w := newTailWriter(1 << 20)
	var wg sync.WaitGroup
	for range 32 {
		wg.Go(func() {
			for range 200 {
				if _, err := w.Write([]byte("abcdef")); err != nil {
					t.Errorf("Write returned error: %v", err)
				}
				_ = w.String()
			}
		})
	}
	wg.Wait()
	if got := w.buf.total; got != 32*200*6 {
		t.Fatalf("total = %d, want %d", got, 32*200*6)
	}
}

func TestTailWriterKeepsRecentOutputAndReportsTheGap(t *testing.T) {
	w := newTailWriter(8)
	if _, err := w.Write([]byte("aaaa")); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if _, err := w.Write([]byte("bbbbbb")); err != nil {
		t.Fatalf("second write: %v", err)
	}
	chunk, next, dropped := w.readFrom(0)
	if chunk != "aabbbbbb" || dropped != 2 || next != 10 {
		t.Fatalf("readFrom(0) = %q, %d, %d; want the retained tail, a 2-byte gap, and cursor 10", chunk, next, dropped)
	}
	if chunk, next, dropped := w.readFrom(next); chunk != "" || next != 10 || dropped != 0 {
		t.Fatalf("caught-up read = %q, %d, %d; want no output and no gap", chunk, next, dropped)
	}
	if got := w.String(); !strings.Contains(got, "showing the most recent 8 of 10 bytes") {
		t.Fatalf("String() = %q, want a truncation note naming the retained window", got)
	}
}

func TestTailWriterReclaimsDroppedPrefix(t *testing.T) {
	w := newTailWriter(8)
	payload := []byte("abcdefghijklmnopqrstuvwxyz0123456789")
	for _, b := range payload {
		if _, err := w.Write([]byte{b}); err != nil {
			t.Fatalf("write %q: %v", b, err)
		}
		if int64(len(w.buf.window)) > 2*w.buf.maxBytes {
			t.Fatalf("buffer grew to %d bytes after %q, want the dropped prefix reclaimed", len(w.buf.window), b)
		}
	}
	want := string(payload[len(payload)-8:])
	chunk, next, dropped := w.readFrom(0)
	if chunk != want || next != int64(len(payload)) || dropped != int64(len(payload)-8) {
		t.Fatalf("readFrom(0) = %q, %d, %d; want %q, %d, %d", chunk, next, dropped, want, len(payload), len(payload)-8)
	}
	if got := w.String(); !strings.HasSuffix(got, want) || !strings.Contains(got, "showing the most recent 8 of 36 bytes") {
		t.Fatalf("String() = %q, want the retained tail and its truncation note", got)
	}
}

func TestShellAppendsDurationNoteForSlowCommand(t *testing.T) {
	out, err := ShellTool{}.Execute(context.Background(), mustMarshal(t, map[string]any{
		"command": "sleep 1.1; echo done",
	}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "done") || !strings.Contains(out, "(command took ") {
		t.Fatalf("output = %q, want duration note after slow command", out)
	}
}

func TestShellDurationNoteRoundsToWholeSeconds(t *testing.T) {
	cases := []struct {
		name    string
		elapsed time.Duration
		want    string
	}{
		{name: "below one second stays unannotated", elapsed: 900 * time.Millisecond, want: "ok"},
		{name: "rounds down", elapsed: 14400 * time.Millisecond, want: "ok\n(command took 14s)"},
		{name: "rounds up", elapsed: 14600 * time.Millisecond, want: "ok\n(command took 15s)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := appendShellDurationNote("ok", tc.elapsed); got != tc.want {
				t.Fatalf("appendShellDurationNote(ok, %v) = %q, want %q", tc.elapsed, got, tc.want)
			}
		})
	}
}

func TestShellOmitsDurationNoteForFastCommand(t *testing.T) {
	out, err := ShellTool{}.Execute(context.Background(), mustMarshal(t, map[string]any{
		"command": "echo quick",
	}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if strings.Contains(out, "(command took ") {
		t.Fatalf("output = %q, want no duration note for fast command", out)
	}
}

func TestShellDurationNoteOnFailedCommandKeepsExitError(t *testing.T) {
	out, err := ShellTool{}.Execute(context.Background(), mustMarshal(t, map[string]any{
		"command": "sleep 1.1; printf fail >&2; exit 7",
	}))
	if err == nil {
		t.Fatal("expected command failure")
	}
	if err.Error() != "exit code 7" {
		t.Fatalf("error = %q, want ordinary exit code", err.Error())
	}
	if !strings.Contains(out, "(command took ") {
		t.Fatalf("output = %q, want duration note on failed command output", out)
	}
}

func TestShellTimeoutSecFromMsSelectsCapByBackground(t *testing.T) {
	twoHoursMs := 2 * 60 * 60 * 1000
	cases := []struct {
		name          string
		timeoutMs     *int
		background    bool
		wantEffective int
	}{
		{
			name:          "omitted uses default",
			wantEffective: ShellDefaultTimeoutMs / 1000,
		},
		{
			name:          "omitted in background has no default deadline",
			background:    true,
			wantEffective: 0,
		},
		{
			name:          "foreground clamps two hours to ten minutes",
			timeoutMs:     new(twoHoursMs),
			wantEffective: ShellMaxTimeoutMs / 1000,
		},
		{
			name:          "background accepts two hours",
			timeoutMs:     new(twoHoursMs),
			background:    true,
			wantEffective: twoHoursMs / 1000,
		},
		{
			name:          "background clamps beyond six hours",
			timeoutMs:     new(ShellMaxBackgroundTimeoutMs + 60_000),
			background:    true,
			wantEffective: ShellMaxBackgroundTimeoutMs / 1000,
		},
		{
			name:       "zero means no hard deadline even in background",
			timeoutMs:  new(0),
			background: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shellTimeoutSecFromMs(tc.timeoutMs, tc.background); got != tc.wantEffective {
				t.Fatalf("timeoutSec = %d, want %d", got, tc.wantEffective)
			}
		})
	}
}

// A cancellation that arrives after the command already finished must report
// the real terminal result instead of turning a natural exit into a cancel.
func TestCancelOrForegroundResultKeepsFinishedJobResult(t *testing.T) {
	resetJobRegistryOnlyForTest(t)
	j, err := globalJobRegistry.start(context.Background(), jobStartRequest{Command: "sh -c 'exit 0'", Description: "quick"})
	if err != nil {
		t.Fatalf("start job: %v", err)
	}
	<-j.done

	if _, err := (ShellTool{}).cancelOrForegroundResult(j, time.Now()); err != nil {
		t.Fatalf("finished job reported as %v, want its natural exit status", err)
	}
}

// A job still running when the cancellation lands is reported as cancelled.
func TestCancelOrForegroundResultReportsRunningJobCancellation(t *testing.T) {
	resetJobRegistryOnlyForTest(t)
	j, err := globalJobRegistry.start(context.Background(), jobStartRequest{Command: "sh -c 'sleep 5'", Description: "long"})
	if err != nil {
		t.Fatalf("start job: %v", err)
	}

	_, err = (ShellTool{}).cancelOrForegroundResult(j, time.Now())
	if err == nil || !strings.Contains(err.Error(), "command cancelled") {
		t.Fatalf("error = %v, want cancellation for a running job", err)
	}
}

// The narrow window where the process has already exited (done closed) but the
// terminal state was not yet published as the job's own: the requested stop is
// accepted, yet the natural exit status must still win over the cancellation.
func TestCancelOrForegroundResultKeepsNaturalExitWhenKillRacesExit(t *testing.T) {
	resetJobRegistryOnlyForTest(t)
	done := make(chan struct{})
	close(done)
	j := &job{
		ID:       "race-exit",
		Command:  "sh -c 'exit 0'",
		cancelCh: make(chan string, 1),
		done:     done,
		status:   jobStatusCompleted,
		detail:   "exit code 0",
	}
	globalJobRegistry.mu.Lock()
	globalJobRegistry.jobs[j.ID] = j
	globalJobRegistry.mu.Unlock()

	out, err := (ShellTool{}).cancelOrForegroundResult(j, time.Now())
	if err != nil {
		t.Fatalf("err = %v, want the natural exit reported instead of a cancellation", err)
	}
	if strings.Contains(out, "cancelled") {
		t.Fatalf("out = %q, want no cancellation marker", out)
	}
}

func TestTerminateJobProcessGroupUsesPublishedNaturalExit(t *testing.T) {
	cmd := exec.Command("sh", "-c", "exit 7")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start command: %v", err)
	}
	waitCh := waitForCommand(cmd)
	<-waitCh.done

	err, natural := terminateJobProcessGroup(cmd, "cancelled", waitCh)
	if !natural {
		t.Fatalf("natural = false, want the published exit result to win")
	}
	exitErr, ok := errors.AsType[*exec.ExitError](err)
	if !ok || exitErr.ExitCode() != 7 {
		t.Fatalf("err = %v, want exit code 7", err)
	}
}
