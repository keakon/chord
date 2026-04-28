package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
	"time"

	"github.com/keakon/chord/internal/shell"
)

const maxOutputBytes = 10 * 1024 * 1024 // 10 MB cap

// cappedWriter wraps a bytes.Buffer and stops accepting data after maxBytes,
// but continues counting total bytes written so callers can report the overflow.
type cappedWriter struct {
	buf      bytes.Buffer
	total    int64
	maxBytes int64
}

func (c *cappedWriter) Write(p []byte) (int, error) {
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
	s := c.buf.String()
	if c.total > c.maxBytes {
		s += fmt.Sprintf("\n...(output truncated: showed %d of %d bytes total)", c.buf.Len(), c.total)
	}
	return s
}

// BashTool executes shell commands.
type BashTool struct {
	shellType string // "bash", "powershell", "git-bash", or "posix"
}

// NewBashTool creates a BashTool with the detected shell type.
func NewBashTool(shellType string) BashTool {
	return BashTool{shellType: shellType}
}

type bashArgs struct {
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
	BashDefaultTimeoutSec = defaultTimeoutSec
	BashMaxTimeoutSec     = maxTimeoutSec
)

type BashTimeoutInfo struct {
	RequestedSec int
	EffectiveSec int
	HasRequested bool
	HasLimit     bool
	UsesDefault  bool
	Clamped      bool
}

func ResolveBashTimeout(timeout *int) BashTimeoutInfo {
	if timeout == nil {
		return ResolveBashTimeoutValue(0, false)
	}
	return ResolveBashTimeoutValue(*timeout, true)
}

func ResolveBashTimeoutValue(requestedSec int, hasTimeout bool) BashTimeoutInfo {
	info := BashTimeoutInfo{
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

func ResolveSpawnTimeout(timeout *int) BashTimeoutInfo {
	if timeout == nil {
		return ResolveSpawnTimeoutValue(0, false)
	}
	return ResolveSpawnTimeoutValue(*timeout, true)
}

func ResolveSpawnTimeoutValue(requestedSec int, hasTimeout bool) BashTimeoutInfo {
	info := BashTimeoutInfo{
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

func (BashTool) Name() string { return "Bash" }

func (BashTool) ConcurrencyPolicy(_ json.RawMessage) ConcurrencyPolicy {
	return ConcurrencyPolicy{
		Resource:             "process:bash",
		Mode:                 ConcurrencyModeExclusive,
		AbortSiblingsOnError: true,
	}
}

func (t BashTool) Description() string {
	return bashToolDescription(nil, t.shellType)
}

func (t BashTool) DescriptionForTools(visible map[string]struct{}) string {
	return bashToolDescription(visible, t.shellType)
}

func bashToolDescription(visible map[string]struct{}, shellType string) string {
	var shellDesc string
	switch shellType {
	case "powershell":
		shellDesc = "Execute a shell command via PowerShell."
	case "git-bash":
		shellDesc = "Execute a shell command via Git Bash."
	case "posix":
		shellDesc = "Execute a shell command (POSIX sh; avoid bash-specific syntax like [[ ]])."
	default:
		shellDesc = "Execute a shell command via bash."
	}
	parts := []string{shellDesc}
	if len(visible) > 0 {
		discoveryHints := make([]string, 0, 4)
		if _, ok := visible["Lsp"]; ok {
			discoveryHints = append(discoveryHints, "use LSP first for symbol-aware navigation such as definitions, references, and implementations")
		}
		if _, ok := visible["Grep"]; ok {
			discoveryHints = append(discoveryHints, "use Grep for repo text search before reaching for rg")
		}
		if _, ok := visible["Glob"]; ok {
			discoveryHints = append(discoveryHints, "use Glob for file or path discovery before reaching for rg --files or find")
		}
		if _, ok := visible["Read"]; ok {
			discoveryHints = append(discoveryHints, "use Read once you have narrowed the target files")
		}
		if len(discoveryHints) > 0 {
			parts = append(parts, "When the built-in tools can cover the discovery step, prefer them: "+strings.Join(discoveryHints, "; ")+".")
		}
	}
	parts = append(parts,
		"Use Bash mainly for tests, builds, git, and other system commands.",
		"Prefer the smallest safe number of tool calls. When one visible built-in tool can do the job directly, use it instead of simulating it in shell.",
		"For native filesystem operations with no dedicated built-in tool, Bash is appropriate when one direct command is clearly simpler and more atomic, such as move/rename, copy, mkdir, or archive/unarchive.",
		"If file-reading, search, or code-navigation tools are hidden or denied in this role, Bash is not a substitute for them.",
		"Do not use shell commands or inline scripts to simulate hidden or denied file reading, search, or code navigation capabilities.",
		"If file-editing tools are hidden or denied in this role, Bash is not a substitute for them.",
		"For explicit file deletions, prefer `Delete`; use shell removal only when shell semantics are actually required, such as directory trees or batch cleanup.",
		"Do not use shell redirection, heredocs, inline scripts, or `rm` as the default way to edit, write, or delete files when dedicated file tools are unavailable.",
		"This tool is exclusively for foreground execution — all background process management uses the Spawn tool.",
		"If this turn needs the command's stdout/stderr, use this tool.",
		"Only set timeout when you need a value other than the default 30s.",
	)
	if _, ok := visible["Spawn"]; ok {
		parts = append(parts, "For processes that must run independently of the current turn, use Spawn instead.")
	}
	return strings.Join(parts, " ")
}

func (BashTool) Parameters() map[string]any {
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
				"description": "Working directory for the command. Defaults to current directory.",
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

func (BashTool) IsReadOnly() bool { return false }

func (t BashTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var a bashArgs
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
		slog.Debug("bash tool", "description", a.Description, "command", a.Command)
	}

	timeoutInfo := ResolveBashTimeout(a.Timeout)
	timeout := time.Duration(timeoutInfo.EffectiveSec) * time.Second

	// Use the detected shell type to construct the correct command.
	binary, args := resolveShellExecution(t.shellType, a.Command)
	cmd := exec.Command(binary, args...)
	_, _ = configureCommandProcessGroup(cmd)
	if a.Workdir != "" {
		cmd.Dir = a.Workdir
	}
	buf := &cappedWriter{maxBytes: maxOutputBytes}
	cmd.Stdout = buf
	cmd.Stderr = buf
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("starting command: %w", err)
	}

	doneCh := make(chan error, 1)
	go func() { doneCh <- cmd.Wait() }()

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case err := <-doneCh:
		output := buf.String()
		if err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				return output, fmt.Errorf("exit code %d", exitErr.ExitCode())
			}
			return output, fmt.Errorf("command error: %w", err)
		}
		return output, nil
	case <-timer.C:
		_ = terminateCommandProcessGroup(cmd)
		return killProcessGroup(cmd, buf, fmt.Sprintf("timed out after %ds", timeoutInfo.EffectiveSec), doneCh)
	case <-ctx.Done():
		_ = terminateCommandProcessGroup(cmd)
		return killProcessGroup(cmd, buf, "cancelled", doneCh)
	}
}

// resolveShellExecution returns the binary and args to execute command in the
// detected shell. Falls back to bash for unknown shell types.
func resolveShellExecution(shellType, command string) (string, []string) {
	st := shell.ParseShellType(shellType)
	binary, args := shell.GetShellCommand(st, command)
	return binary, args
}

// killProcessGroup sends SIGTERM (then SIGKILL) to the process group and
// returns whatever output was captured along with an error.
func killProcessGroup(cmd *exec.Cmd, buf *cappedWriter, reason string, doneCh <-chan error) (string, error) {
	pid := cmd.Process.Pid
	_ = pid
	_ = terminateCommandProcessGroup(cmd)
	select {
	case <-doneCh:
	case <-time.After(killGracePeriod):
		_ = terminateCommandProcessGroup(cmd)
		<-doneCh
	}
	output := buf.String()
	return output, fmt.Errorf("command %s after output:\n%s", reason, truncateForError(output, 500))
}

// truncateForError trims output for inclusion in error messages.
func truncateForError(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "...(truncated)"
}
