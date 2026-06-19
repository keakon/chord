package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func resetSpawnRegistryOnlyForTest(t *testing.T) {
	t.Helper()
	restore := ResetSpawnRegistryForTest()
	t.Cleanup(restore)
}

func TestSpawnServiceExposesSessionScopedLogDir(t *testing.T) {
	resetSpawnRegistryOnlyForTest(t)
	sessionDir := t.TempDir()
	ctx := WithSessionDir(context.Background(), sessionDir)
	out, err := NewSpawnTool("").Execute(ctx, mustMarshal(t, map[string]any{
		"command":     "sleep 1",
		"description": "session scoped log",
	}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, filepath.Join(sessionDir, sessionSpawnLogsDirName)) {
		t.Fatalf("output %q should reference session-scoped log dir", out)
	}
	id := extractBackgroundID(t, out)
	_, _ = (SpawnStopTool{}).Execute(context.Background(), mustMarshal(t, map[string]any{"id": id}))
}

func TestSpawnJobDoesNotExposeLogFile(t *testing.T) {
	resetSpawnRegistryOnlyForTest(t)
	sessionDir := t.TempDir()
	ctx := WithSessionDir(context.Background(), sessionDir)
	out, err := NewSpawnTool("").Execute(ctx, mustMarshal(t, map[string]any{
		"command":     "sleep 1",
		"description": "job without log path",
		"timeout":     5,
	}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if strings.Contains(out, "log_file:") {
		t.Fatalf("job output %q should not expose log_file", out)
	}
	id := extractBackgroundID(t, out)
	_, _ = (SpawnStopTool{}).Execute(context.Background(), mustMarshal(t, map[string]any{"id": id}))
}

func TestSpawnRejectsInteractiveCommandBeforeExecution(t *testing.T) {
	resetSpawnRegistryOnlyForTest(t)
	out, err := NewSpawnTool("").Execute(context.Background(), mustMarshal(t, map[string]any{
		"command":     "gh auth login",
		"description": "interactive auth wizard",
	}))
	if err == nil {
		t.Fatal("expected interactive command rejection")
	}
	if out != "" {
		t.Fatalf("output = %q, want empty", out)
	}
	if !strings.Contains(err.Error(), "interactive command rejected") || !strings.Contains(err.Error(), "gh auth login") {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := len(SnapshotSpawnedProcesses()); got != 0 {
		t.Fatalf("spawn registry length = %d, want 0", got)
	}
}

func TestSpawnToolUsesDetectedShellDescription(t *testing.T) {
	desc := NewSpawnTool("posix").Description()
	for _, want := range []string{
		"real asynchronous execution is needed",
		"Do not use spawn as a replacement for foreground shell",
		"ordinary tests/builds/checks",
		"commands you will simply wait for, use shell instead",
		"same detected shell environment",
		"non-interactive",
		"obvious interactive commands are rejected",
		"Completion results are delivered automatically",
		"re-enter the conversation asynchronously",
	} {
		if !strings.Contains(desc, want) {
			t.Fatalf("description %q should mention %q", desc, want)
		}
	}
}

func TestSpawnParametersWarnAgainstForegroundReplacement(t *testing.T) {
	params := SpawnTool{}.Parameters()
	props, ok := params["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties has unexpected type %T", params["properties"])
	}
	commandProp, ok := props["command"].(map[string]any)
	if !ok {
		t.Fatalf("command has unexpected type %T", props["command"])
	}
	commandDesc, _ := commandProp["description"].(string)
	for _, want := range []string{"asynchronous execution", "use shell for ordinary commands", "output you need immediately"} {
		if !strings.Contains(commandDesc, want) {
			t.Fatalf("command description missing %q in %q", want, commandDesc)
		}
	}

	descriptionProp, ok := props["description"].(map[string]any)
	if !ok {
		t.Fatalf("description has unexpected type %T", props["description"])
	}
	descriptionDesc, _ := descriptionProp["description"].(string)
	if !strings.Contains(descriptionDesc, "why it needs to run asynchronously") {
		t.Fatalf("description field guidance missing async rationale in %q", descriptionDesc)
	}
}

func TestSpawnUsesDetectedShellExecution(t *testing.T) {
	resetSpawnRegistryOnlyForTest(t)
	sessionDir := t.TempDir()
	ctx := WithSessionDir(context.Background(), sessionDir)
	out, err := NewSpawnTool("posix").Execute(ctx, mustMarshal(t, map[string]any{
		"command":     "echo spawn-shell-check && sleep 1",
		"description": "posix spawn shell test",
	}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var logFile string
	for line := range strings.SplitSeq(out, "\n") {
		if after, ok := strings.CutPrefix(line, "log_file: "); ok {
			logFile = strings.TrimSpace(after)
			break
		}
	}
	if logFile == "" {
		t.Fatalf("spawn output %q should expose log_file for service", out)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		data, readErr := os.ReadFile(logFile)
		if readErr == nil && strings.Contains(string(data), "spawn-shell-check") {
			id := extractBackgroundID(t, out)
			_, _ = (SpawnStopTool{}).Execute(context.Background(), mustMarshal(t, map[string]any{"id": id}))
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	data, _ := os.ReadFile(logFile)
	t.Fatalf("log file %q did not contain marker, got %q", logFile, string(data))
}

func TestSpawnStatusExposesServiceLogFileOnly(t *testing.T) {
	resetSpawnRegistryOnlyForTest(t)
	sessionDir := t.TempDir()
	ctx := WithSessionDir(context.Background(), sessionDir)
	serviceOut, err := NewSpawnTool("").Execute(ctx, mustMarshal(t, map[string]any{
		"command":     "sleep 1",
		"description": "service status log",
	}))
	if err != nil {
		t.Fatalf("service Execute: %v", err)
	}
	serviceID := extractBackgroundID(t, serviceOut)
	statusOut, err := (SpawnStatusTool{}).Execute(context.Background(), mustMarshal(t, map[string]any{"id": serviceID}))
	if err != nil {
		t.Fatalf("SpawnStatus service: %v", err)
	}
	if !strings.Contains(statusOut, "log_file:") {
		t.Fatalf("service status %q should expose log_file", statusOut)
	}
	_, _ = (SpawnStopTool{}).Execute(context.Background(), mustMarshal(t, map[string]any{"id": serviceID}))

	jobOut, err := NewSpawnTool("").Execute(ctx, mustMarshal(t, map[string]any{
		"command":     "sleep 1",
		"description": "job status no log",
		"timeout":     5,
	}))
	if err != nil {
		t.Fatalf("job Execute: %v", err)
	}
	jobID := extractBackgroundID(t, jobOut)
	jobStatusOut, err := (SpawnStatusTool{}).Execute(context.Background(), mustMarshal(t, map[string]any{"id": jobID}))
	if err != nil {
		t.Fatalf("SpawnStatus job: %v", err)
	}
	if strings.Contains(jobStatusOut, "log_file:") {
		t.Fatalf("job status %q should not expose log_file", jobStatusOut)
	}
	_, _ = (SpawnStopTool{}).Execute(context.Background(), mustMarshal(t, map[string]any{"id": jobID}))
}

func TestPolicyForToolDefaultsExclusiveForUnknownTool(t *testing.T) {
	policy := PolicyForTool(NewRegistry(), "Unknown", json.RawMessage(`{}`))
	if policy.Mode != ConcurrencyModeExclusive {
		t.Fatalf("mode = %q, want exclusive", policy.Mode)
	}
}
