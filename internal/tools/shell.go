package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/keakon/golog/log"
)

const maxOutputBytes = 10 * 1024 * 1024 // 10 MB cap

// tailWriter is the concurrency-safe view of a TailBuffer that the job registry
// reads: the lock makes the incremental cursor reads atomic against the writes,
// and each output generation has a broadcast channel for waiters.
type tailWriter struct {
	mu    sync.Mutex
	buf   TailBuffer
	wrote chan struct{}
}

func newTailWriter(maxBytes int64) *tailWriter {
	return &tailWriter{buf: *NewTailBuffer(maxBytes), wrote: make(chan struct{})}
}

func (c *tailWriter) Write(p []byte) (int, error) {
	c.mu.Lock()
	n, err := c.buf.Write(p)
	if n > 0 {
		close(c.wrote)
		c.wrote = make(chan struct{})
	}
	c.mu.Unlock()
	return n, err
}

// waitSignalAfter returns the current output generation and whether output
// beyond the cursor is already retained. The generation channel is closed and
// replaced after each write, so every waiter for that generation is released.
// The channel and predicate are sampled under one lock to avoid a write
// landing between the predicate check and the wait.
func (c *tailWriter) waitSignalAfter(cursor int64) (<-chan struct{}, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.wrote, c.buf.hasDataAfter(cursor)
}

// readFrom returns the output after the absolute cursor, the cursor to pass to
// the next read, and how many bytes the reader missed because they had already
// been dropped from the window.
func (c *tailWriter) readFrom(cursor int64) (string, int64, int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.readFrom(cursor)
}

// tail returns up to the last maxLen bytes of retained output, how many earlier
// bytes were dropped from the window, and whether the excerpt was cut.
func (c *tailWriter) tail(maxLen int) (string, int64, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.tail(maxLen)
}

// raw returns the retained output with no truncation notice attached. Logic
// that classifies output — runtime-failure classification, build-failure
// sniffing — must read this rather than String: a model-facing notice is
// decoration and must never feed output-sniffing.
func (c *tailWriter) raw() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.raw()
}

func (c *tailWriter) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.String()
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
	Command         string `json:"command"`
	Description     string `json:"description,omitempty"`
	Workdir         string `json:"workdir,omitempty"`
	TimeoutMs       *int   `json:"timeout_ms,omitempty"`
	YieldTimeMs     *int   `json:"yield_time_ms,omitempty"`
	RunInBackground bool   `json:"run_in_background,omitempty"`
}

// killGracePeriod is how long a process group gets between SIGTERM and SIGKILL.
// It is a var so tests can shorten the escalation wait without weakening the
// production grace period.
var killGracePeriod = 3 * time.Second

const (
	// ShellDefaultTimeoutMs is the hard wall-clock deadline applied when
	// timeout_ms is omitted. It must be larger than the default foreground
	// yield, otherwise a long command would be killed before it could be
	// promoted to the background.
	ShellDefaultTimeoutMs = 600_000
	ShellMaxTimeoutMs     = 600_000
	// ShellMaxBackgroundTimeoutMs caps timeout_ms for an explicitly detached
	// job. Hour-scale work stays inside Chord (session/process lifetime is the
	// real bound), while day-scale work belongs to an external runner
	// (tmux/systemd/CI) because a session switch or client exit still kills
	// jobs. Foreground commands keep ShellMaxTimeoutMs so a command that cannot
	// be auto-promoted never blocks the turn for hours.
	ShellMaxBackgroundTimeoutMs = 6 * 60 * 60 * 1000
	// ShellDefaultYieldMs is how long a command may hold the foreground before
	// it is promoted to a background job. 0 disables auto-promotion.
	ShellDefaultYieldMs = 90_000
	shellMaxYieldMs     = 600_000
)

// shellTimeoutSecFromMs resolves the millisecond timeout_ms argument into the
// second-granularity hard deadline the job registry consumes; 0 means "no hard
// deadline". background selects the larger cap for an explicitly detached job;
// foreground callers keep the tighter cap so a non-promotable command cannot
// block the turn for hours.
func shellTimeoutSecFromMs(timeoutMs *int, background bool) int {
	if timeoutMs == nil {
		if background {
			// An explicitly detached job gets no default deadline. It is
			// started precisely because nobody waits for it, and its real bound
			// is the session/process lifetime; inheriting the foreground
			// deadline would SIGKILL a service or watcher ten minutes in
			// without the model ever asking for a deadline.
			return 0
		}
		return ShellDefaultTimeoutMs / 1000
	}
	if *timeoutMs <= 0 {
		return 0
	}
	maxMs := ShellMaxTimeoutMs
	if background {
		maxMs = ShellMaxBackgroundTimeoutMs
	}
	sec := (*timeoutMs + 999) / 1000
	if limit := maxMs / 1000; sec > limit {
		sec = limit
	}
	return sec
}

// resolveShellYieldMs clamps the yield_time_ms argument; 0 or negative disables
// auto-promotion.
func resolveShellYieldMs(yieldMs *int) int {
	if yieldMs == nil {
		return ShellDefaultYieldMs
	}
	if *yieldMs <= 0 {
		return 0
	}
	if *yieldMs > shellMaxYieldMs {
		return shellMaxYieldMs
	}
	return *yieldMs
}

// autoBackgroundAllowed reports whether a command may be promoted to the
// background on its own. A command is pinned to the foreground only when
// *every* one of its subcommands is a deliberate wait or a short git query:
// promotion must never reward a sleep-wait, and a `git status` gains nothing
// from a job handle. One promotable subcommand is enough to promote the whole
// command — the shell description tells the model to chain dependent work with
// `&&`, so the very common `go test ./... && git status` shape must not lose
// promotion because of its trailing query. A command that does not parse cannot
// be reasoned about as a single command, so it stays in the foreground.
func autoBackgroundAllowed(command string) bool {
	trimmed := strings.TrimSpace(command)
	if trimmed == "" {
		return false
	}
	analysis, err := AnalyzeShellCommand(trimmed)
	if err != nil {
		return false
	}
	sawSubcommand := false
	for _, subcommand := range analysis.Subcommands {
		if len(subcommand.LiteralArgs) == 0 {
			continue
		}
		sawSubcommand = true
		if !foregroundOnlySubcommand(subcommand.LiteralArgs) {
			return true
		}
	}
	// Nothing recognizable to reason about (every word came from an expansion)
	// keeps the previous permissive behavior; a command made only of waits and
	// short git queries has nothing worth a job handle.
	return !sawSubcommand
}

// foregroundOnlySubcommand reports whether one subcommand never justifies a
// background job on its own: a deliberate sleep-wait, or a git operation that
// is a short local query rather than a long transfer or repack.
func foregroundOnlySubcommand(args []string) bool {
	switch args[0] {
	case "sleep":
		return true
	case "git":
		return !longRunningGitSubcommand(args[1:])
	default:
		return false
	}
}

// longRunningGitSubcommand reports whether a git invocation names one of the
// operations that legitimately runs for minutes — network transfers and
// repository maintenance — so it may be promoted like any other long command.
// The check scans every word rather than locating the subcommand position,
// because global options (`-c k=v`, `-C dir`, `--git-dir=...`) sit in between.
// The trade-off is that a literal payload such as `git commit -m clone` also
// matches; that only makes a sub-second command eligible for a promotion it
// will never reach, so a false positive here is harmless.
func longRunningGitSubcommand(args []string) bool {
	for _, arg := range args {
		switch arg {
		case "clone", "fetch", "pull", "push", "submodule", "gc", "fsck", "repack", "bundle", "filter-branch":
			return true
		}
	}
	return false
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
// tools. Detached calls are excluded: starting a job is a process side effect,
// not a read. Everything else falls back to the exclusive ConcurrencyPolicy.
func (ShellTool) ConcurrencySafeReadOnly(args json.RawMessage) bool {
	return shellReadOnlyCommandAllowed(args)
}

func shellReadOnlyCommandAllowed(args json.RawMessage) bool {
	var parsed struct {
		Command         string `json:"command"`
		RunInBackground bool   `json:"run_in_background"`
	}
	if err := json.Unmarshal(unwrapToolArgs(args), &parsed); err != nil {
		return false
	}
	if parsed.RunInBackground {
		return false
	}
	command := strings.TrimSpace(parsed.Command)
	if command == "" || containsShellConstruct(command) {
		return false
	}
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return false
	}
	switch fields[0] {
	case "pwd", "ls", "cat", "which", "head", "tail", "wc", "stat", "file", "du", "df":
		if fields[0] == "tail" {
			for _, f := range fields[1:] {
				// A follow-mode tail never returns; cover --follow[=mode] and
				// short-option clusters such as -fn, not only the bare forms.
				if strings.HasPrefix(f, "--follow") {
					return false
				}
				if strings.HasPrefix(f, "-") && !strings.HasPrefix(f, "--") && strings.ContainsAny(f, "fF") {
					return false
				}
			}
		}
		if fields[0] == "file" {
			for _, f := range fields[1:] {
				// `file -C/--compile` writes magic.mgc (in the current
				// directory, or next to the -m path), so it is not a read.
				if f == "--compile" || strings.HasPrefix(f, "--compile=") {
					return false
				}
				if strings.HasPrefix(f, "-") && !strings.HasPrefix(f, "--") && strings.Contains(f[1:], "C") {
					return false
				}
			}
		}
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

// shellCommandChainingCharacters are the constructs that turn one command into
// a different or additional one: chaining, command substitution, redirection,
// and the newlines that start a fresh command. Any string containing one of
// them cannot be reasoned about as "this single command".
const shellCommandChainingCharacters = ";|&`$><\n\r"

// shellArgumentExpansionCharacters are the constructs the shell expands within
// a single command's own arguments: escapes, grouping, globs, and brace
// expansion. shellReadOnlyCommandAllowed refuses them even though they cannot
// introduce a second command by themselves, because for `ls`, `cat` and
// friends the expanded argument list is what decides which files the command
// actually touches: the allowlist must not depend on an expansion it does not
// perform.
const shellArgumentExpansionCharacters = `\()*?[]{}`

// containsShellConstruct reports whether a command carries any shell syntax
// beyond a plain word list, so it cannot be classified from its first words
// alone.
func containsShellConstruct(command string) bool {
	return strings.ContainsAny(command, shellCommandChainingCharacters+shellArgumentExpansionCharacters)
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
		"This tool also runs background jobs. Set run_in_background:true for services or work you do not need to wait for; the call returns a job id immediately and job_output/job_list/job_kill manage it.",
		fmt.Sprintf("Long one-shot commands (builds, test suites) are promoted to a background job after the yield budget (default %s) and keep running; you will be notified when they finish. Do not sleep-wait or busy-poll — do independent work, or end your turn and wait for the notification.", durationLabel(ShellDefaultYieldMs)),
		"Dependent commands must run in order: chain them in one call with `&&` or `;`, or wait for the previous result. A background job runs concurrently with other tool calls, so never start a command that depends on a job's output before that job finishes.",
		fmt.Sprintf("Only set timeout_ms when you need a hard deadline other than the foreground default of %dms — a job started with run_in_background:true has none until you set one, and accepts up to %d for hour-scale work; only set yield_time_ms when you need a foreground budget other than the default %dms.", ShellDefaultTimeoutMs, ShellMaxBackgroundTimeoutMs, ShellDefaultYieldMs),
	)
	return strings.Join(parts, "\n")
}

// durationLabel renders a whole-second millisecond budget as a compact human
// duration ("90s", "10m", "6h"), so the prose descriptions and the constants
// they advertise cannot drift apart.
func durationLabel(ms int) string {
	switch {
	case ms >= 3_600_000 && ms%3_600_000 == 0:
		return fmt.Sprintf("%dh", ms/3_600_000)
	case ms >= 60_000 && ms%60_000 == 0:
		return fmt.Sprintf("%dm", ms/60_000)
	default:
		return fmt.Sprintf("%ds", ms/1000)
	}
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
				"description": "Working directory the command runs in. Omit it to run in the current Working directory — do not prefix the command with `cd`; set workdir only when the command must run somewhere else. Relative paths resolve from it, except `~` for the current user's home directory.",
			},
			"timeout_ms": map[string]any{
				"type": "integer",
				"description": fmt.Sprintf("Optional hard deadline in milliseconds. A foreground command defaults to %d (%s); a job started with run_in_background:true has no deadline unless you set one. Capped at %d for a foreground command and at %d (%s) when run_in_background is true; 0 means no deadline, which suits long-running services — a foreground command that cannot be promoted to a job still keeps the default deadline.",
					ShellDefaultTimeoutMs, durationLabel(ShellDefaultTimeoutMs), ShellMaxTimeoutMs, ShellMaxBackgroundTimeoutMs, durationLabel(ShellMaxBackgroundTimeoutMs)),
			},
			"yield_time_ms": map[string]any{
				"type":        "integer",
				"description": fmt.Sprintf("Optional foreground budget in milliseconds before the command continues as a background job (max %d, default %d). 0 keeps the command in the foreground until it finishes or hits timeout_ms — use it when this turn needs the result and the command fits the foreground deadline; cancelling the turn kills the command.", shellMaxYieldMs, ShellDefaultYieldMs),
			},
			"run_in_background": map[string]any{
				"type":        "boolean",
				"description": "Set true to start the command as a background job without waiting (services, watchers, or work you do not need before continuing). Returns a job id; manage it with job_output, job_list, and job_kill.",
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
	if strings.TrimSpace(a.Command) == "" {
		return "", fmt.Errorf("command is required")
	}
	if a.TimeoutMs != nil && *a.TimeoutMs < 0 {
		return "", fmt.Errorf("timeout_ms must not be negative")
	}
	if a.YieldTimeMs != nil && *a.YieldTimeMs < 0 {
		return "", fmt.Errorf("yield_time_ms must not be negative")
	}
	if a.Description != "" {
		log.Debugf("shell tool description=%v command=%v", a.Description, a.Command)
	}

	if finding := DetectInteractiveShellCommand(a.Command); finding != nil {
		return "", finding.Error()
	}

	resolvedWorkdir, err := resolveCommandWorkdir(a.Workdir, t.BaseDir)
	if err != nil {
		return "", err
	}
	var logDir string
	if sessionDir := SessionDirFromContext(ctx); sessionDir != "" {
		logDir = sessionJobLogsDir(sessionDir)
	}
	// A foreground command must always be bounded by one of the two mechanisms
	// below: a hard deadline, or a yield budget that hands it to a background
	// job. With neither, the wait after start would block the turn until the
	// process exits. Both are resolved before the job starts so the deadline can
	// be decided with the yield budget in hand.
	promotable := false
	if !a.RunInBackground {
		promotable = autoBackgroundAllowed(a.Command)
	}
	yieldBudget := time.Duration(0)
	if yieldMs := resolveShellYieldMs(a.YieldTimeMs); yieldMs > 0 && promotable {
		yieldBudget = time.Duration(yieldMs) * time.Millisecond
	}
	timeoutSec := shellTimeoutSecFromMs(a.TimeoutMs, a.RunInBackground)
	if timeoutSec > 0 && time.Duration(timeoutSec)*time.Second <= yieldBudget {
		// The explicit deadline is tighter than the yield window, so promotion
		// would race the deadline and only ever hand back an about-to-die job.
		yieldBudget = 0
	}
	if !a.RunInBackground && timeoutSec <= 0 && yieldBudget <= 0 {
		// timeout_ms: 0 asks for "no deadline", which only an explicitly
		// detached job may have. A foreground command reaches here when it
		// cannot be promoted (no yield timer would ever hand it off) or when
		// promotion was switched off with yield_time_ms: 0, so it keeps the default
		// cap rather than blocking the turn until the process exits.
		timeoutSec = ShellDefaultTimeoutMs / 1000
	}
	job, err := globalJobRegistry.start(ctx, jobStartRequest{
		Command:     a.Command,
		Description: strings.TrimSpace(a.Description),
		Workdir:     resolvedWorkdir,
		TimeoutSec:  timeoutSec,
		ShellType:   t.shellType,
		LogDir:      logDir,
		Detached:    a.RunInBackground,
	})
	if err != nil {
		return "", err
	}
	started := time.Now()
	if a.RunInBackground {
		return backgroundJobHandle(job, "started in the background"), nil
	}

	if yieldBudget > 0 {
		timer := time.NewTimer(yieldBudget)
		defer timer.Stop()
		select {
		case <-job.done:
			return t.foregroundResult(job, started)
		case <-ctx.Done():
			globalJobRegistry.kill(job.ID, "cancelled")
			<-job.done
			return t.cancelledResult(job)
		case <-timer.C:
			if !job.detach() {
				<-job.done
				return t.foregroundResult(job, started)
			}
			return backgroundJobHandle(job, fmt.Sprintf("exceeded the %ds foreground budget", int(yieldBudget/time.Second))), nil
		}
	}
	select {
	case <-job.done:
		return t.foregroundResult(job, started)
	case <-ctx.Done():
		globalJobRegistry.kill(job.ID, "cancelled")
		<-job.done
		return t.cancelledResult(job)
	}
}

func (t ShellTool) foregroundResult(j *job, started time.Time) (string, error) {
	// Cleaned on the way out like a job_output read: the same command promoted
	// to a job comes back stripped, so a foreground result must not be the only
	// path that can hand raw escape sequences and redrawn progress to the model.
	output := cleanJobOutputText(j.outputString())
	// A deadline kill is reported through j.exitErr ("timed out after Ns"), and
	// its wall-clock span includes the process-group teardown grace period, so a
	// duration note here would contradict the error the model already sees.
	if !j.isKilled() {
		elapsed := time.Since(started)
		output = appendShellDurationNote(output, elapsed)
		output = appendShellCostNote(output, j.Command, elapsed)
	}
	// j.exitErr is published by finish() under j.mu after close(j.done); the
	// foreground path observes it through that happens-before, but reading it
	// here under the lock keeps the dependency explicit and robust if that sync
	// ever changes.
	j.mu.Lock()
	err := j.exitErr
	j.mu.Unlock()
	return output, err
}

func (t ShellTool) cancelledResult(j *job) (string, error) {
	output := cleanJobOutputText(j.outputString())
	return output, fmt.Errorf("command cancelled after output:\n%s", truncateForError(output, 500))
}

// backgroundJobHandle is the model-facing text returned when a command is still
// running after the foreground budget. It names the job, tells the model not to
// wait or poll, and warns that the job now runs concurrently.
func backgroundJobHandle(j *job, reason string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "[background job %s] %s\n", j.ID, reason)
	fmt.Fprintf(&sb, "status: %s\n", j.statusText())
	if j.LogFile != "" {
		fmt.Fprintf(&sb, "log_file: %s\n", j.LogFile)
	}
	if j.MaxRuntimeSec > 0 {
		// A detached job only carries a deadline when the caller asked for one,
		// so the model must be told: a silent SIGKILL mid-run is otherwise
		// indistinguishable from a crash.
		fmt.Fprintf(&sb, "deadline: %ds from start, then it is killed\n", j.MaxRuntimeSec)
	}
	sb.WriteString("The command keeps running and its output will not be lost. You will be notified when it finishes; do not sleep-wait or busy-poll.\n")
	sb.WriteString("It may run concurrently with other tool calls: do not start a command that depends on its output before it completes.\n")
	fmt.Fprintf(&sb, "Do independent work or end your turn; read incremental output with job_output(%s).", j.ID)
	return sb.String()
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

// shellCostNoteMin is the elapsed time above which a successful verification
// command gets an explicit cost note. Together with the failure-path guidance,
// this is the feedback loop that lets the model weigh a full re-run against a
// narrower check before spending the time again.
const shellCostNoteMin = 60 * time.Second

// shellTimeoutGuidance is appended to a deadline kill. Without it a timeout
// invites the same command unchanged, which would hit the same deadline again;
// promotion means a long but legitimate command belongs in a background job.
const shellTimeoutGuidance = "Do not re-run the same command unchanged: narrow it to the relevant subset, raise timeout_ms, or start it with run_in_background: true so it keeps running while you do other work."

func appendShellCostNote(output, command string, elapsed time.Duration) string {
	if elapsed < shellCostNoteMin || !isTestOrVerificationCommand(command) {
		return output
	}
	return output + fmt.Sprintf("\n(cost: this verification ran %.0fs — narrow it next time (single package, -run filter), or delegate long checks so you can keep working)", elapsed.Seconds())
}

// exitSignalName returns the signal that terminated a command, or "" when the
// error is not a signal-terminated exit, so the job status detail and the shell
// error agree on the wording.
func exitSignalName(err error) string {
	exitErr, ok := errors.AsType[*exec.ExitError](err)
	if !ok {
		return ""
	}
	status, ok := exitErr.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() {
		return ""
	}
	return status.Signal().String()
}

func shellExitErrorForCommand(command string, exitErr *exec.ExitError, output string) error {
	if exitErr == nil {
		return fmt.Errorf("command failed")
	}
	if name := exitSignalName(exitErr); name != "" {
		return fmt.Errorf("signal: %s", name)
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

// truncateForError trims output for inclusion in error messages.
func truncateForError(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "...(truncated)"
}
