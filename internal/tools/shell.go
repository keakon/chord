package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/shell"
)

const maxOutputBytes = 10 * 1024 * 1024 // 10 MB cap

// cappedWriter wraps a bytes.Buffer and stops accepting data after maxBytes,
// but continues counting total bytes written so callers can report the overflow.
type cappedWriter struct {
	mu       sync.Mutex
	buf      bytes.Buffer
	total    int64
	maxBytes int64
}

func (c *cappedWriter) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.total += int64(len(p))
	if remaining := c.maxBytes - int64(c.buf.Len()); remaining > 0 {
		if int64(len(p)) <= remaining {
			c.buf.Write(p)
		} else {
			c.buf.Write(p[:remaining])
		}
	}
	return len(p), nil
}

func (c *cappedWriter) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.buf.String()
	if c.total > c.maxBytes {
		s += fmt.Sprintf("\n...(output truncated: showed %d of %d bytes total)", c.buf.Len(), c.total)
	}
	return s
}

// ShellTool executes shell commands.
type ShellTool struct {
	shellType string // "bash", "powershell", "git-bash", or "posix"
	BaseDir   string // session working directory for relative workdir; empty keeps process cwd behavior
}

// NewShellTool creates a ShellTool with the detected shell type.
func NewShellTool(shellType string) ShellTool {
	return ShellTool{shellType: shellType}
}

type shellArgs struct {
	Command     string `json:"command"`
	Description string `json:"description,omitempty"`
	Workdir     string `json:"workdir,omitempty"`
	Timeout     *int   `json:"timeout,omitempty"`
}

const (
	defaultTimeoutSec = 30
	maxTimeoutSec     = 600
	killGracePeriod   = 3 * time.Second
)

const (
	ShellDefaultTimeoutSec = defaultTimeoutSec
	ShellMaxTimeoutSec     = maxTimeoutSec
)

type ShellTimeoutInfo struct {
	RequestedSec int
	EffectiveSec int
	HasRequested bool
	HasLimit     bool
	UsesDefault  bool
	Clamped      bool
}

func ResolveShellTimeout(timeout *int) ShellTimeoutInfo {
	if timeout == nil {
		return ResolveShellTimeoutValue(0, false)
	}
	return ResolveShellTimeoutValue(*timeout, true)
}

func ResolveShellTimeoutValue(requestedSec int, hasTimeout bool) ShellTimeoutInfo {
	info := ShellTimeoutInfo{
		RequestedSec: requestedSec,
		HasRequested: hasTimeout,
		HasLimit:     true,
	}
	if !hasTimeout || requestedSec <= 0 {
		info.EffectiveSec = defaultTimeoutSec
		info.UsesDefault = true
		return info
	}
	info.EffectiveSec = requestedSec
	if info.EffectiveSec > maxTimeoutSec {
		info.EffectiveSec = maxTimeoutSec
		info.Clamped = true
	}
	return info
}

func ResolveSpawnTimeout(timeout *int) ShellTimeoutInfo {
	if timeout == nil {
		return ResolveSpawnTimeoutValue(0, false)
	}
	return ResolveSpawnTimeoutValue(*timeout, true)
}

func ResolveSpawnTimeoutValue(requestedSec int, hasTimeout bool) ShellTimeoutInfo {
	info := ShellTimeoutInfo{
		RequestedSec: requestedSec,
		HasRequested: hasTimeout,
	}
	if !hasTimeout || requestedSec <= 0 {
		return info
	}
	info.HasLimit = true
	info.EffectiveSec = requestedSec
	if info.EffectiveSec > maxTimeoutSec {
		info.EffectiveSec = maxTimeoutSec
		info.Clamped = true
	}
	return info
}

func (ShellTool) Name() string { return NameShell }

func (ShellTool) ConcurrencyPolicy(_ json.RawMessage) ConcurrencyPolicy {
	return ConcurrencyPolicy{
		Resource:             "process:shell",
		Mode:                 ConcurrencyModeExclusive,
		AbortSiblingsOnError: true,
	}
}

// ConcurrencySafeReadOnly admits a narrow allowlist of side-effect-free shell
// commands (no metacharacters) so they can batch alongside other read-only
// tools. Everything else falls back to the exclusive ConcurrencyPolicy.
func (ShellTool) ConcurrencySafeReadOnly(args json.RawMessage) bool {
	return shellReadOnlyCommandAllowed(args)
}

func shellReadOnlyCommandAllowed(args json.RawMessage) bool {
	var parsed struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(unwrapToolArgs(args), &parsed); err != nil {
		return false
	}
	command := strings.TrimSpace(parsed.Command)
	if command == "" || containsShellMetachar(command) {
		return false
	}
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return false
	}
	switch fields[0] {
	case "pwd", "ls", "cat", "which":
		return true
	case "git":
		if len(fields) < 2 {
			return false
		}
		switch fields[1] {
		case "status", "log", "diff", "show", "branch", "rev-parse":
			return true
		default:
			return false
		}
	default:
		return false
	}
}

func containsShellMetachar(command string) bool {
	for _, r := range command {
		switch r {
		case '|', '&', ';', '>', '<', '$', '`', '\\', '(', ')', '*', '?', '[', ']', '{', '}', '\n', '\r':
			return true
		}
	}
	return false
}

func (t ShellTool) Description() string {
	return shellToolDescription(nil, t.shellType)
}

func (t ShellTool) DescriptionForTools(visible map[string]struct{}) string {
	return shellToolDescription(visible, t.shellType)
}

func shellToolDescription(visible map[string]struct{}, shellType string) string {
	var shellDesc string
	switch shellType {
	case "powershell":
		shellDesc = "Execute a shell command via PowerShell."
	case "git-bash":
		shellDesc = "Execute a shell command via Git Shell."
	case "posix":
		shellDesc = "Execute a shell command (POSIX sh; avoid bash-specific syntax like [[ ]])."
	default:
		shellDesc = "Execute a shell command via bash."
	}
	parts := []string{shellDesc}
	if len(visible) > 0 {
		discoveryHints := make([]string, 0, 4)
		if _, ok := visible[NameLsp]; ok {
			discoveryHints = append(discoveryHints, "use LSP first for symbol-aware navigation such as definitions, references, and implementations")
		}
		if _, ok := visible[NameGrep]; ok {
			discoveryHints = append(discoveryHints, "use Grep for repo text search before reaching for rg")
		}
		if _, ok := visible[NameGlob]; ok {
			discoveryHints = append(discoveryHints, "use Glob for file or path discovery before reaching for rg --files or find")
		}
		if _, ok := visible[NameRead]; ok {
			discoveryHints = append(discoveryHints, "use Read once you have narrowed the target files")
		}
		if len(discoveryHints) > 0 {
			parts = append(parts, "When the built-in tools can cover the discovery step, prefer them: "+strings.Join(discoveryHints, "; ")+".")
		}
	}
	parts = append(parts,
		"This tool is non-interactive: stdin is not provided, Unix commands run without a controlling TTY. Do not run interactive commands (login wizards, editors, TUIs, password prompts); obvious interactive commands are rejected before execution.",
		"Use shell mainly for tests, builds, git, and other system commands.",
		"Prefer the smallest safe number of tool calls. When one visible built-in tool can do the job directly, use it instead of simulating it in shell.",
		"For native filesystem operations with no dedicated built-in tool, shell is appropriate when one direct command is clearly simpler and more atomic, such as move/rename, copy, mkdir, or archive/unarchive.",
		"If file reading, search, code-navigation, or file-editing tools are hidden or denied in this role, shell is not a substitute for them; do not simulate those capabilities with shell commands or inline scripts.",
	)
	if line := shellFileDeletionHint(visible); line != "" {
		parts = append(parts, line)
	}
	parts = append(parts,
		"Do not use shell redirection, heredocs, inline scripts, or `rm` as the default way to edit, write, or delete files when dedicated file tools are unavailable.",
		"This tool is exclusively for foreground execution — all background process management uses the spawn tool.",
		"If this turn needs the command's stdout/stderr, use this tool.",
		"For long one-shot commands whose result is needed before continuing, use shell with an explicit timeout rather than spawn.",
		"Only set timeout when you need a value other than the default 30s.",
	)
	if _, ok := visible[NameSpawn]; ok {
		parts = append(parts, "For processes that must run independently of the current turn, use spawn instead.")
	}
	return strings.Join(parts, "\n")
}

// shellFileDeletionHint routes explicit file deletions to whichever dedicated
// deletion-capable tool is on the current surface. With no visibility
// information (static descriptions) the delete tool is assumed present; with a
// known surface that has neither delete nor apply_patch, no hint is emitted and
// the generic "do not default to rm" guidance stands alone.
func shellFileDeletionHint(visible map[string]struct{}) string {
	const suffix = "; use shell removal only when shell semantics are actually required, such as directory trees or batch cleanup."
	if visible == nil {
		return "For explicit file deletions, prefer `delete`" + suffix
	}
	if _, ok := visible[NameDelete]; ok {
		return "For explicit file deletions, prefer `delete`" + suffix
	}
	if _, ok := visible[NameApplyPatch]; ok {
		return "For explicit file deletions, prefer `apply_patch` with `*** Delete File:`" + suffix
	}
	return ""
}

func (ShellTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"command": map[string]any{
				"type":        "string",
				"description": "The shell command to execute.",
			},
			"description": map[string]any{
				"type":        "string",
				"description": "Brief description of what this command does (5-10 words).",
			},
			"workdir": map[string]any{
				"type":        "string",
				"description": "Working directory for the command. Relative paths resolve from the session working directory. Supports ~ for the current user's home directory. Defaults to the session working directory.",
			},
			"timeout": map[string]any{
				"type":        "integer",
				"description": "Optional execution timeout in seconds (max 600); only set this field if you need a value other than the default 30 seconds.",
			},
		},
		"required":             []string{"command"},
		"additionalProperties": false,
	}
}

func (ShellTool) IsReadOnly() bool { return false }

func (t ShellTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var a shellArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if a.Command == "" {
		return "", fmt.Errorf("command is required")
	}
	if a.Timeout != nil && *a.Timeout <= 0 {
		return "", fmt.Errorf("timeout must be a positive integer")
	}
	if a.Description != "" {
		log.Debugf("shell tool description=%v command=%v", a.Description, a.Command)
	}

	if finding := DetectInteractiveShellCommand(a.Command); finding != nil {
		return "", finding.Error()
	}

	timeoutInfo := ResolveShellTimeout(a.Timeout)
	timeout := time.Duration(timeoutInfo.EffectiveSec) * time.Second

	// Use the detected shell type to construct the correct command.
	binary, args := resolveShellExecution(t.shellType, a.Command)
	cmd := exec.Command(binary, args...)
	_, _ = configureCommandProcessGroup(cmd)
	resolvedWorkdir := strings.TrimSpace(t.BaseDir)
	if a.Workdir != "" {
		var err error
		resolvedWorkdir, err = resolveToolPathInDir(a.Workdir, t.BaseDir)
		if err != nil {
			return "", fmt.Errorf("resolve workdir: %w", err)
		}
	} else if resolvedWorkdir != "" {
		var err error
		resolvedWorkdir, err = resolveToolPath(resolvedWorkdir)
		if err != nil {
			return "", fmt.Errorf("resolve workdir: %w", err)
		}
	}
	if resolvedWorkdir != "" {
		cmd.Dir = resolvedWorkdir
	}
	buf := &cappedWriter{maxBytes: maxOutputBytes}
	cmd.Stdout = buf
	cmd.Stderr = buf
	// ShellTool is intentionally non-interactive. Leaving Stdin nil makes Go
	// connect the child process to the null device instead of the TUI stdin.
	cmd.Env = appendNonInteractiveEnv(nil)
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("starting command: %w", err)
	}

	started := time.Now()
	doneCh := make(chan error, 1)
	go func() { doneCh <- cmd.Wait() }()

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case err := <-doneCh:
		// Classify and format from the raw output; the duration note is
		// model-facing decoration and must not feed output-sniffing logic.
		raw := buf.String()
		output := appendShellDurationNote(raw, time.Since(started))
		if err != nil {
			if ClassifyNonInteractiveRuntimeFailure(a.Command, err, raw) != nil {
				return output, FormatNonInteractiveRuntimeError(NameShell, a.Command, err, raw)
			}
			if exitErr, ok := err.(*exec.ExitError); ok {
				return output, shellExitErrorForCommand(a.Command, exitErr, raw)
			}
			return output, fmt.Errorf("command error: %w", err)
		}
		return output, nil
	case <-timer.C:
		_ = terminateCommandProcessGroup(cmd)
		guidance := "Do not re-run the same command unchanged: either narrow it to the relevant subset, raise timeout if the command legitimately needs longer, or use spawn for background execution."
		if timeoutInfo.EffectiveSec >= maxTimeoutSec {
			guidance = "This is the maximum shell timeout: do not re-run the same command unchanged. Narrow it to the relevant subset (for example one package or test), or use spawn to run the long command in the background and poll its output."
		}
		return killProcessGroup(cmd, buf, fmt.Sprintf("timed out after %ds", timeoutInfo.EffectiveSec), guidance, doneCh)
	case <-ctx.Done():
		_ = terminateCommandProcessGroup(cmd)
		return killProcessGroup(cmd, buf, "cancelled", "", doneCh)
	}
}

// shellDurationNoteMin is the minimum elapsed time before a completed command's
// output gets a wall-clock duration note. The model cannot observe how long a
// command took, so without feedback it cannot weigh cheap checks against
// expensive ones when choosing verification scope; sub-second commands are
// left unannotated to keep short outputs clean.
const shellDurationNoteMin = time.Second

// appendShellDurationNote appends the elapsed wall-clock time to command output
// so the model can factor real cost into deciding what to run next.
func appendShellDurationNote(output string, elapsed time.Duration) string {
	if elapsed < shellDurationNoteMin {
		return output
	}
	return output + fmt.Sprintf("\n(command took %.1fs)", elapsed.Seconds())
}

// resolveShellExecution returns the binary and args to execute command in the
// detected shell. Falls back to bash for unknown shell types.
func resolveShellExecution(shellType, command string) (string, []string) {
	st := shell.ParseShellType(shellType)
	binary, args := shell.GetShellCommand(st, command)
	return binary, args
}

func shellExitErrorForCommand(command string, exitErr *exec.ExitError, output string) error {
	if exitErr == nil {
		return fmt.Errorf("command failed")
	}
	if status, ok := exitErr.Sys().(syscall.WaitStatus); ok && status.Signaled() {
		return fmt.Errorf("signal: %s", status.Signal())
	}
	msg := fmt.Sprintf("exit code %d", exitErr.ExitCode())
	if isTestOrVerificationCommand(command) {
		if testOutputShowsBuildFailure(output) {
			msg += ". The build failed before tests could run. Fix compilation first and confirm with a build-only check (for example go build ./... / go vet, cargo check, or tsc --noEmit) — it surfaces the same errors in seconds. Do not re-run the test suite until the build passes, then start with the affected package instead of the full suite"
		} else {
			msg += ". Test or verification command failed. Inspect the first relevant failure; before rerunning a broad test, prefer a focused reproduction for the affected package/test. Do not repeat the same failing command unchanged unless there is a clear reason to expect a different result"
		}
	}
	return fmt.Errorf("%s", msg)
}

// testOutputShowsBuildFailure reports whether a failed test command died in
// its compile/build phase rather than in test execution. In that case any
// time the runner spent on other packages was wasted, and the cheapest next
// step is a build-only check, so the error guidance steers there explicitly.
func testOutputShowsBuildFailure(output string) bool {
	if output == "" {
		return false
	}
	for _, marker := range []string{
		"[build failed]",           // go test
		"[setup failed]",           // go test
		"error: could not compile", // cargo test
		"error: linking with",      // cargo test
		"Compilation failed",       // various runners
		"compilation terminated",   // gcc/clang via make test
		"SyntaxError: ",            // node/python test entry
		"error TS",                 // tsc diagnostics in test pipelines
		"CompileError",             // ruby/elixir
	} {
		if strings.Contains(output, marker) {
			return true
		}
	}
	return false
}

func isTestOrVerificationCommand(command string) bool {
	analysis, err := AnalyzeShellCommand(command)
	if err != nil {
		return false
	}
	for _, subcommand := range analysis.Subcommands {
		if isTestOrVerificationArgs(subcommand.LiteralArgs) {
			return true
		}
	}
	return false
}

func isTestOrVerificationArgs(args []string) bool {
	if len(args) == 0 {
		return false
	}
	command := strings.ToLower(args[0])
	arg := func(index int) string {
		if index >= len(args) {
			return ""
		}
		return strings.ToLower(args[index])
	}
	switch command {
	case "go":
		return arg(1) == "test" || arg(1) == "vet" || arg(1) == "build"
	case "cargo":
		return arg(1) == "test" || arg(1) == "check" || arg(1) == "build"
	case "npm", "pnpm", "yarn":
		return arg(1) == "test"
	case "pytest":
		return true
	case "python", "python3":
		return arg(1) == "-m" && arg(2) == "pytest"
	case "mvn":
		return arg(1) == "test"
	case "gradle", "./gradlew", "gradlew":
		return arg(1) == "test"
	case "make":
		return arg(1) == "test" || arg(1) == "check"
	default:
		return false
	}
}

// killProcessGroup sends SIGTERM (then SIGKILL) to the process group and
// returns whatever output was captured along with an error. guidance, when
// non-empty, is appended after the output excerpt to steer the model's next
// step (timeouts otherwise invite an unchanged, equally slow re-run).
func killProcessGroup(cmd *exec.Cmd, buf *cappedWriter, reason, guidance string, doneCh <-chan error) (string, error) {
	pid := cmd.Process.Pid
	_ = pid
	_ = terminateCommandProcessGroup(cmd)
	select {
	case <-doneCh:
	case <-time.After(killGracePeriod):
		_ = forceTerminateCommandProcessGroup(cmd)
		select {
		case <-doneCh:
		case <-time.After(killGracePeriod):
			// Avoid hanging forever if the process refuses to die.
		}
	}
	output := buf.String()
	err := fmt.Errorf("command %s after output:\n%s", reason, truncateForError(output, 500))
	if guidance != "" {
		err = fmt.Errorf("%w\n%s", err, guidance)
	}
	return output, err
}

// truncateForError trims output for inclusion in error messages.
func truncateForError(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "...(truncated)"
}
