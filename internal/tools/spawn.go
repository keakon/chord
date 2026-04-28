package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// SpawnTool starts a background process that runs independently of the current turn.
type SpawnTool struct {
	shellType string // "bash", "powershell", "git-bash", or "posix"
}

// NewSpawnTool creates a SpawnTool with the detected shell type.
func NewSpawnTool(shellType string) SpawnTool {
	return SpawnTool{shellType: shellType}
}

type spawnArgs struct {
	Command     string `json:"command"`
	Description string `json:"description"`
	Timeout     *int   `json:"timeout,omitempty"` // seconds; present → job semantics, absent → service semantics
	Workdir     string `json:"workdir,omitempty"`
}

func (SpawnTool) Name() string { return "Spawn" }

func (SpawnTool) ConcurrencyPolicy(_ json.RawMessage) ConcurrencyPolicy {
	return ConcurrencyPolicy{
		Resource: "process:spawn",
		Mode:     ConcurrencyModeExclusive,
	}
}

func (SpawnTool) Description() string {
	return spawnToolDescription(nil)
}

func (SpawnTool) DescriptionForTools(visible map[string]struct{}) string {
	return spawnToolDescription(visible)
}

func spawnToolDescription(_ map[string]struct{}) string {
	return "Start a background process that runs independently of the current turn.\n" +
		"Only use Spawn for processes with real background lifecycle needs — not as a way to parallelize ordinary commands.\n" +
		"Appropriate: dev servers, file watchers, long-running benchmarks, batch pipelines the user explicitly wants in background.\n" +
		"It uses the same detected shell environment as the foreground Bash tool.\n" +
		"Use foreground Bash for commands whose stdout/stderr you need in this turn.\n" +
		"You will NOT receive stdout/stderr directly from this tool. Job completion results are delivered automatically when the process exits. Services may expose a diagnostic log_file path that you can inspect with foreground Bash when needed.\n" +
		"Set timeout for tasks that should terminate after a duration. Omit timeout for services that should run indefinitely."
}

func (SpawnTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"command": map[string]any{
				"type":        "string",
				"description": "The shell command to run in background.",
			},
			"description": map[string]any{
				"type":        "string",
				"description": "What this background process does (required).",
			},
			"timeout": map[string]any{
				"type":        "integer",
				"description": "Max runtime in seconds (max 600). Set for tasks that should terminate; omit for long-running services.",
			},
			"workdir": map[string]any{
				"type":        "string",
				"description": "Working directory for the command. Defaults to current directory.",
			},
		},
		"required":             []string{"command", "description"},
		"additionalProperties": false,
	}
}

func (SpawnTool) IsReadOnly() bool { return false }

func (t SpawnTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var a spawnArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if strings.TrimSpace(a.Command) == "" {
		return "", fmt.Errorf("command is required")
	}
	if strings.TrimSpace(a.Description) == "" {
		return "", fmt.Errorf("description is required")
	}

	var kind spawnKind
	var timeoutInfo BashTimeoutInfo
	if a.Timeout != nil {
		kind = spawnKindJob
		timeoutInfo = ResolveSpawnTimeout(a.Timeout)
		if !timeoutInfo.HasLimit {
			return "", fmt.Errorf("timeout must be a positive value")
		}
	} else {
		kind = spawnKindService
		timeoutInfo = ResolveSpawnTimeout(nil)
	}

	// Service diagnostic logs are written to <sessionDir>/spawn-logs/<id>.log.
	// Jobs still write to a runtime-managed file for internal diagnostics, but the
	// path is only exposed to the model for long-lived services.
	var logDir string
	if sessionDir := SessionDirFromContext(ctx); sessionDir != "" {
		logDir = sessionSpawnLogsDir(sessionDir)
	}
	exposeLogToModel := kind == spawnKindService

	obj, err := globalSpawnRegistry.start(ctx, spawnedProcessStartRequest{
		Kind:             kind,
		Command:          a.Command,
		Description:      strings.TrimSpace(a.Description),
		Workdir:          a.Workdir,
		TimeoutInfo:      timeoutInfo,
		ShellType:        t.shellType,
		LogDir:           logDir,
		ExposeLogToModel: exposeLogToModel,
	})
	if err != nil {
		return "", err
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "id: %s\n", obj.ID)
	fmt.Fprintf(&sb, "status: running\n")
	if obj.ExposeLogToModel && obj.LogFile != "" {
		fmt.Fprintf(&sb, "log_file: %s\n", obj.LogFile)
	}
	if timeoutInfo.HasLimit {
		fmt.Fprintf(&sb, "max_runtime: %ds", timeoutInfo.EffectiveSec)
	} else {
		sb.WriteString("max_runtime: none")
	}
	return sb.String(), nil
}

// SpawnStatusTool reads lightweight lifecycle state for a background process started by Spawn.
type SpawnStatusTool struct{}

type spawnStatusArgs struct {
	ID string `json:"id"`
}

func (SpawnStatusTool) Name() string { return "SpawnStatus" }

func (SpawnStatusTool) Description() string {
	return "Inspect lightweight lifecycle state for a background process started by Spawn. Use this for status only; completion results are delivered automatically when background jobs exit."
}

func (SpawnStatusTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"id": map[string]any{
				"type":        "string",
				"description": "The process ID returned by Spawn.",
			},
		},
		"required":             []string{"id"},
		"additionalProperties": false,
	}
}

func (SpawnStatusTool) IsReadOnly() bool { return true }

func (SpawnStatusTool) Execute(_ context.Context, raw json.RawMessage) (string, error) {
	var a spawnStatusArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if strings.TrimSpace(a.ID) == "" {
		return "", fmt.Errorf("id is required")
	}
	state, ok := globalSpawnRegistry.getState(a.ID)
	if !ok {
		return "", fmt.Errorf("process %s not found", a.ID)
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "id: %s\n", state.ID)
	fmt.Fprintf(&sb, "kind: %s\n", state.Kind)
	fmt.Fprintf(&sb, "status: %s\n", state.Status)
	if !state.StartedAt.IsZero() {
		fmt.Fprintf(&sb, "started_at: %s\n", state.StartedAt.Format(time.DateTime))
	}
	if state.MaxRuntimeSec > 0 {
		fmt.Fprintf(&sb, "max_runtime: %ds", state.MaxRuntimeSec)
	} else {
		sb.WriteString("max_runtime: none")
	}
	if state.LogFile != "" && state.Kind == string(spawnKindService) {
		fmt.Fprintf(&sb, "\nlog_file: %s", state.LogFile)
	}
	return sb.String(), nil
}

// SpawnStopTool stops a background process started by Spawn.
type SpawnStopTool struct{}

type spawnStopArgs struct {
	ID string `json:"id"`
}

func (SpawnStopTool) Name() string { return "SpawnStop" }

func (SpawnStopTool) Description() string {
	return "Stop a background process started by Spawn. Sends SIGTERM with a grace period, then SIGKILL if needed."
}

func (SpawnStopTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"id": map[string]any{
				"type":        "string",
				"description": "The process ID returned by Spawn.",
			},
		},
		"required":             []string{"id"},
		"additionalProperties": false,
	}
}

func (SpawnStopTool) IsReadOnly() bool { return false }

func (SpawnStopTool) Execute(_ context.Context, raw json.RawMessage) (string, error) {
	var a spawnStopArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if strings.TrimSpace(a.ID) == "" {
		return "", fmt.Errorf("id is required")
	}
	if !globalSpawnRegistry.cancel(a.ID, "cancelled by SpawnStop") {
		return "", fmt.Errorf("process %s not found", a.ID)
	}
	return fmt.Sprintf("id: %s\nstatus: cancelled", a.ID), nil
}
