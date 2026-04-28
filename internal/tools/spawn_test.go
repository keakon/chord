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

func TestSpawnToolUsesDetectedShellDescription(t *testing.T) {
	desc := NewSpawnTool("posix").Description()
	if !strings.Contains(desc, "same detected shell environment") {
		t.Fatalf("description %q should mention same detected shell environment", desc)
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
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "log_file: ") {
			logFile = strings.TrimSpace(strings.TrimPrefix(line, "log_file: "))
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
