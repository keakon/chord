package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestResolveBashTimeoutForegroundDefaultsAndClamps(t *testing.T) {
	cases := []struct {
		name          string
		requested     int
		hasTimeout    bool
		wantEffective int
		wantDefault   bool
		wantClamped   bool
	}{
		{name: "default", requested: 0, hasTimeout: false, wantEffective: BashDefaultTimeoutSec, wantDefault: true},
		{name: "non-positive uses default", requested: -5, hasTimeout: true, wantEffective: BashDefaultTimeoutSec, wantDefault: true},
		{name: "explicit accepted", requested: 45, hasTimeout: true, wantEffective: 45},
		{name: "clamped", requested: 2400, hasTimeout: true, wantEffective: BashMaxTimeoutSec, wantClamped: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ResolveBashTimeoutValue(tc.requested, tc.hasTimeout)
			if !got.HasLimit {
				t.Fatal("expected foreground timeout to always have a limit")
			}
			if got.EffectiveSec != tc.wantEffective {
				t.Fatalf("effective = %d, want %d", got.EffectiveSec, tc.wantEffective)
			}
			if got.UsesDefault != tc.wantDefault {
				t.Fatalf("usesDefault = %v, want %v", got.UsesDefault, tc.wantDefault)
			}
			if got.Clamped != tc.wantClamped {
				t.Fatalf("clamped = %v, want %v", got.Clamped, tc.wantClamped)
			}
		})
	}
}

func TestResolveSpawnTimeoutOptionalAndClamped(t *testing.T) {
	cases := []struct {
		name          string
		requested     int
		hasTimeout    bool
		wantHasLimit  bool
		wantEffective int
		wantClamped   bool
	}{
		{name: "none by default", requested: 0, hasTimeout: false, wantHasLimit: false},
		{name: "explicit accepted", requested: 300, hasTimeout: true, wantHasLimit: true, wantEffective: 300},
		{name: "non-positive without limit", requested: 0, hasTimeout: true, wantHasLimit: false},
		{name: "clamped", requested: 2400, hasTimeout: true, wantHasLimit: true, wantEffective: BashMaxTimeoutSec, wantClamped: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ResolveSpawnTimeoutValue(tc.requested, tc.hasTimeout)
			if got.HasLimit != tc.wantHasLimit {
				t.Fatalf("hasLimit = %v, want %v", got.HasLimit, tc.wantHasLimit)
			}
			if got.EffectiveSec != tc.wantEffective {
				t.Fatalf("effective = %d, want %d", got.EffectiveSec, tc.wantEffective)
			}
			if got.Clamped != tc.wantClamped {
				t.Fatalf("clamped = %v, want %v", got.Clamped, tc.wantClamped)
			}
		})
	}
}

func TestBashExecuteForegroundTimeoutUsesConfiguredValue(t *testing.T) {
	tool := BashTool{}
	out, err := tool.Execute(context.Background(), mustMarshal(t, map[string]any{
		"command": "sleep 2",
		"timeout": 1,
	}))
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if out != "" {
		t.Fatalf("expected no output from timed-out sleep command, got %q", out)
	}
	if !strings.Contains(err.Error(), "timed out after 1s") {
		t.Fatalf("expected effective timeout in error, got %v", err)
	}
}

func TestBashIgnoresLegacyBackgroundFields(t *testing.T) {
	tool := BashTool{}
	for name, args := range map[string]map[string]any{
		"mode":              {"command": "printf ok", "mode": "job", "timeout": 5},
		"run_in_background": {"command": "printf ok", "run_in_background": true},
	} {
		t.Run(name, func(t *testing.T) {
			out, err := tool.Execute(context.Background(), mustMarshal(t, args))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if out != "ok" {
				t.Fatalf("output = %q, want ok", out)
			}
		})
	}
}

func TestBashDescriptionIncludesToolSpecificHintsOnlyWhenVisible(t *testing.T) {
	tool := BashTool{}

	withoutHelpers := tool.DescriptionForTools(nil)
	if strings.Contains(withoutHelpers, "prefer them") {
		t.Fatalf("unexpected helper hint without visible tools: %q", withoutHelpers)
	}
	for _, want := range []string{
		"This tool is exclusively for foreground execution — all background process management uses the Spawn tool.",
		"Use Bash mainly for tests, builds, git, and other system commands.",
		"Prefer the smallest safe number of tool calls.",
		"Bash is appropriate when one direct command is clearly simpler and more atomic, such as move/rename, copy, mkdir, or archive/unarchive.",
		"If file-reading, search, or code-navigation tools are hidden or denied in this role, Bash is not a substitute for them.",
		"Do not use shell commands or inline scripts to simulate hidden or denied file reading, search, or code navigation capabilities.",
		"If file-editing tools are hidden or denied in this role, Bash is not a substitute for them.",
		"For explicit file deletions, prefer `Delete`; use shell removal only when shell semantics are actually required, such as directory trees or batch cleanup.",
		"Do not use shell redirection, heredocs, inline scripts, or `rm` as the default way to edit, write, or delete files when dedicated file tools are unavailable.",
		"If this turn needs the command's stdout/stderr, use this tool.",
		"Only set timeout when you need a value other than the default 30s.",
	} {
		if !strings.Contains(withoutHelpers, want) {
			t.Fatalf("missing guidance %q in %q", want, withoutHelpers)
		}
	}

	withHelpers := tool.DescriptionForTools(map[string]struct{}{
		"Lsp":  {},
		"Grep": {},
		"Glob": {},
		"Read": {},
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

func TestBashParametersEmphasizeForegroundAndBuiltinAlternatives(t *testing.T) {
	params := BashTool{}.Parameters()
	props, ok := params["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties has unexpected type %T", params["properties"])
	}

	if _, exists := props["mode"]; exists {
		t.Fatal("Bash Parameters() must not expose 'mode' — background process management is handled by Spawn")
	}
	if _, exists := props["run_in_background"]; exists {
		t.Fatal("Bash Parameters() must not expose 'run_in_background'")
	}

	timeoutProp, ok := props["timeout"].(map[string]any)
	if !ok {
		t.Fatalf("timeout has unexpected type %T", props["timeout"])
	}
	timeoutDesc, _ := timeoutProp["description"].(string)
	if !strings.Contains(timeoutDesc, "only set this field if you need a value other than the default 30 seconds") {
		t.Fatalf("timeout description missing foreground guidance in %q", timeoutDesc)
	}
}

func TestSpawnStopDescriptionClarifiesLifecycle(t *testing.T) {
	desc := SpawnStopTool{}.Description()
	for _, want := range []string{"SIGTERM", "grace period"} {
		if !strings.Contains(desc, want) {
			t.Fatalf("description missing %q in %q", want, desc)
		}
	}
	params := SpawnStopTool{}.Parameters()
	props, ok := params["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties has unexpected type %T", params["properties"])
	}
	idProp, ok := props["id"].(map[string]any)
	if !ok {
		t.Fatalf("id has unexpected type %T", props["id"])
	}
	idDesc, _ := idProp["description"].(string)
	if !strings.Contains(idDesc, "Spawn") {
		t.Fatalf("id description missing Spawn wording in %q", idDesc)
	}
}

func TestStopAllSpawnedForAgentStopsOnlyMatchingOwner(t *testing.T) {
	resetSpawnRegistryForTest(t)
	job1, err := globalSpawnRegistry.start(WithAgentID(context.Background(), "sub-1"), spawnedProcessStartRequest{Kind: spawnKindJob, Command: "sleep 5", Description: "Sub job"})
	if err != nil {
		t.Fatalf("start sub background: %v", err)
	}
	job2, err := globalSpawnRegistry.start(WithAgentID(context.Background(), "sub-2"), spawnedProcessStartRequest{Kind: spawnKindService, Command: "sleep 5", Description: "Other service"})
	if err != nil {
		t.Fatalf("start other background: %v", err)
	}

	waitForSpawnedProcessToStart(t, job1)
	waitForSpawnedProcessToStart(t, job2)

	start := time.Now()
	stopped := StopAllSpawnedForAgent("sub-1", "terminated on session switch")
	elapsed := time.Since(start)
	if stopped != 1 {
		t.Fatalf("stopped = %d, want 1", stopped)
	}
	if elapsed >= 2*time.Second {
		t.Fatalf("StopAllSpawnedForAgent took %v, want quick cancellation well before natural exit", elapsed)
	}
	if _, ok := globalSpawnRegistry.get(job1.ID); ok {
		t.Fatalf("expected %s to be removed", job1.ID)
	}
	if _, ok := globalSpawnRegistry.get(job2.ID); !ok {
		t.Fatalf("expected %s to remain", job2.ID)
	}
	_, _ = SpawnStopTool{}.Execute(context.Background(), mustMarshal(t, map[string]any{"id": job2.ID}))
}

func TestStopAllSpawnedForShutdownRemovesAll(t *testing.T) {
	resetSpawnRegistryForTest(t)
	if _, err := globalSpawnRegistry.start(context.Background(), spawnedProcessStartRequest{Kind: spawnKindJob, Command: "sleep 5", Description: "job one"}); err != nil {
		t.Fatalf("start background 1: %v", err)
	}
	if _, err := globalSpawnRegistry.start(context.Background(), spawnedProcessStartRequest{Kind: spawnKindService, Command: "sleep 5", Description: "service one"}); err != nil {
		t.Fatalf("start background 2: %v", err)
	}
	stopped := StopAllSpawnedForShutdown()
	if stopped != 2 {
		t.Fatalf("stopped = %d, want 2", stopped)
	}
	if got := len(SnapshotSpawnedProcesses()); got != 0 {
		t.Fatalf("len(SnapshotSpawnedProcesses()) = %d, want 0", got)
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

func extractBackgroundID(t *testing.T, out string) string {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if after, ok := strings.CutPrefix(line, "id: "); ok {
			return strings.TrimSpace(after)
		}
		if after, ok := strings.CutPrefix(line, "id: "); ok {
			return strings.TrimSpace(after)
		}
	}
	t.Fatalf("background id not found in output:\n%s", out)
	return ""
}

func waitForSpawnedProcessToStart(t *testing.T, proc *spawnedProcess) {
	t.Helper()
	select {
	case <-proc.startedCh:
		return
	case <-proc.done:
		t.Fatalf("process %s exited before reaching started state", proc.ID)
	case <-time.After(2 * time.Second):
		t.Fatalf("process %s did not reach started state before deadline", proc.ID)
	}
}

func resetSpawnRegistryForTest(t *testing.T) {
	t.Helper()
	restore := ResetSpawnRegistryForTest()
	t.Cleanup(restore)
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
			tool := NewBashTool(tc.shellType)
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

func TestBashParametersCommandDescription(t *testing.T) {
	params := BashTool{}.Parameters()
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
