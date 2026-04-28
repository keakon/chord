package hook

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testEnv(point string, data map[string]any) Envelope {
	return Envelope{
		Point:         point,
		Timestamp:     time.Now().UTC(),
		SessionID:     "session-1",
		TurnID:        42,
		AgentID:       "main-1",
		AgentKind:     "main",
		ProjectRoot:   os.TempDir(),
		SelectedModel: "provider/selected",
		RunningModel:  "provider/running",
		Data:          data,
	}
}

func shellHook(name, point, command string) HookDef {
	return HookDef{
		Name:    name,
		Point:   point,
		Command: Command{Shell: command},
	}
}

func TestHookPureHelpers(t *testing.T) {
	h := normalizeHookDefaults(HookDef{Point: OnToolCall})
	if h.Name != OnToolCall || h.Join != JoinBackground || h.ResultFormat != ResultFormatSummary || h.MaxResultLines != 50 || h.MaxResultBytes != 4096 {
		t.Fatalf("normalizeHookDefaults = %+v", h)
	}
	if normalizeJoin(JoinBeforeNextLLM) != JoinBeforeNextLLM || normalizeJoin("unknown") != JoinBackground {
		t.Fatal("normalizeJoin returned unexpected values")
	}
	argvCmd := Command{Args: []string{"echo"}}
	shellCmd := Command{Shell: "echo hi"}
	emptyCmd := Command{}
	if argvCmd.mode() != "argv" || shellCmd.mode() != "shell" || !emptyCmd.IsZero() {
		t.Fatal("Command helpers returned unexpected values")
	}
}

func TestHookFiltersAndEnvHelpers(t *testing.T) {
	env := testEnv(OnToolBatchComplete, map[string]any{
		"tool_name":  "Bash",
		"path":       "internal/agent/main.go",
		"timeout_ms": float64(1500),
		"error_kind": "tool_error",
		"tool_calls": []any{
			map[string]any{"tool_name": "Read", "error": ""},
			map[string]any{"tool_name": "Edit", "error": "failed"},
		},
		"changed_files": []any{
			map[string]any{"path": "internal/agent/main.go"},
			map[string]any{"path": "README.md"},
		},
	})

	if !matchesToolFilter([]string{"Bash"}, env) || matchesToolFilter([]string{"Edit"}, env) || matchesToolFilter([]string{"Glob"}, env) {
		t.Fatal("matchesToolFilter unexpected")
	}
	if !matchesPathFilter([]string{"internal/**/*.go"}, env) || matchesPathFilter([]string{"docs/**"}, env) {
		t.Fatal("matchesPathFilter unexpected")
	}
	if !matchesModelFilter([]string{"provider/*"}, env) || matchesModelFilter([]string{"other/*"}, env) {
		t.Fatal("matchesModelFilter unexpected")
	}
	if changedFileCount(env) != 2 || !hasError(env) {
		t.Fatalf("changedFileCount/hasError unexpected")
	}
	if !matchesStringPatterns([]string{"main-*"}, env.AgentID) || matchesStringPatterns([]string{"sub-*"}, env.AgentID) {
		t.Fatal("matchesStringPatterns unexpected")
	}

	vars := buildHookEnv(env, HookDef{Environment: map[string]string{"CUSTOM": "value", "EMPTY": ""}})
	joined := "\n" + strings.Join(vars, "\n") + "\n"
	for _, want := range []string{"CHORD_HOOK_POINT=on_tool_batch_complete", "CHORD_HOOK_TURN_ID=42", "CHORD_HOOK_TOOL_NAME=Bash", "CHORD_HOOK_TIMEOUT_MS=1500", "CHORD_HOOK_ERROR_KIND=tool_error", "CUSTOM=value"} {
		if !strings.Contains(joined, "\n"+want+"\n") {
			t.Fatalf("buildHookEnv missing %q in %q", want, joined)
		}
	}
	if strings.Contains(joined, "EMPTY=") {
		t.Fatalf("buildHookEnv should omit empty vars: %q", joined)
	}
}

func TestNoopEngine(t *testing.T) {
	e := &NoopEngine{}
	result, err := e.Fire(context.Background(), testEnv(OnToolCall, map[string]any{
		"tool_name": "Bash",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Action != ActionContinue {
		t.Fatalf("expected continue, got %q", result.Action)
	}
}

func TestCommandEngine_SyncBlockAndModify(t *testing.T) {
	e := NewCommandEngine(map[string][]HookDef{
		OnToolCall: {
			shellHook("modify", OnToolCall, `echo '{"action":"modify","data":{"tool_name":"Bash","args":{"command":"echo safe"}}}'`),
			shellHook("block", OnToolCall, `echo '{"action":"block","message":"denied"}'`),
		},
	})

	result, err := e.Fire(context.Background(), testEnv(OnToolCall, map[string]any{
		"tool_name": "Bash",
		"args":      map[string]any{"command": "rm -rf /"},
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Action != ActionBlock {
		t.Fatalf("expected block, got %q", result.Action)
	}
	if result.Message != "denied" {
		t.Fatalf("expected block message, got %q", result.Message)
	}
}

func TestCommandEngine_SyncModifyCarriesData(t *testing.T) {
	e := NewCommandEngine(map[string][]HookDef{
		OnBeforeToolResultAppend: {
			shellHook("modify", OnBeforeToolResultAppend, `echo '{"action":"modify","data":{"display_result":"masked","context_result":"masked-ctx"}}'`),
		},
	})

	result, err := e.Fire(context.Background(), testEnv(OnBeforeToolResultAppend, map[string]any{
		"tool_name":      "Bash",
		"display_result": "raw",
		"context_result": "raw",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Action != ActionContinue {
		t.Fatalf("expected continue, got %q", result.Action)
	}
	m, ok := result.Data.(map[string]any)
	if !ok {
		t.Fatalf("expected modified map data, got %T", result.Data)
	}
	if m["display_result"] != "masked" || m["context_result"] != "masked-ctx" {
		t.Fatalf("unexpected modified data: %#v", m)
	}
}

func TestCommandEngine_ToolFilter(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "ran")
	e := NewCommandEngine(map[string][]HookDef{
		OnToolCall: {{
			Name:    "bash-only",
			Point:   OnToolCall,
			Command: Command{Shell: fmt.Sprintf(`touch %s && echo '{"action":"continue"}'`, marker)},
			Tools:   []string{"Bash"},
		}},
	})

	_, err := e.Fire(context.Background(), testEnv(OnToolCall, map[string]any{
		"tool_name": "Read",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("hook should not run for non-matching tool")
	}
}

func TestCommandEngine_PathFilter(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "ran")
	e := NewCommandEngine(map[string][]HookDef{
		OnToolBatchComplete: {{
			Name:    "go-only",
			Point:   OnToolBatchComplete,
			Command: Command{Shell: fmt.Sprintf(`touch %s && echo '{"status":"success","summary":"ok"}'`, marker)},
			Paths:   []string{"**/*.go"},
			Join:    JoinBeforeNextLLM,
		}},
	})

	_, err := e.RunAutomation(context.Background(), testEnv(OnToolBatchComplete, map[string]any{
		"tool_calls": []any{},
		"changed_files": []any{
			map[string]any{"path": "internal/agent/main.go"},
		},
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("hook should run for matching path filter")
	}
}

func TestCommandEngine_PathFilterForToolEventUsesPathAndPaths(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "ran")
	e := NewCommandEngine(map[string][]HookDef{
		OnToolCall: {{
			Name:    "go-only",
			Point:   OnToolCall,
			Command: Command{Shell: fmt.Sprintf(`touch %s && echo '{"action":"continue"}'`, marker)},
			Paths:   []string{"**/*.go"},
		}},
	})

	_, err := e.Fire(context.Background(), testEnv(OnToolCall, map[string]any{
		"tool_name": "Delete",
		"path":      "internal/tools/read.go",
		"paths":     []string{"internal/tools/read.go", "README.md"},
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("hook should run for matching tool event path filter")
	}
}

func TestCommandEngine_ObserverReceivesEnvelopeAndEnvVars(t *testing.T) {
	inputFile := filepath.Join(t.TempDir(), "hook-input.json")
	envFile := filepath.Join(t.TempDir(), "hook-env.txt")
	cmd := fmt.Sprintf(`cat > %s; printf '%%s' "$CHORD_HOOK_POINT" > %s`, inputFile, envFile)

	e := NewCommandEngine(map[string][]HookDef{
		OnSessionStart: {{
			Name:    "inspect",
			Point:   OnSessionStart,
			Command: Command{Shell: cmd},
		}},
	})

	if _, err := e.Fire(context.Background(), testEnv(OnSessionStart, map[string]any{"hello": "world"})); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, err := os.ReadFile(inputFile)
	if err != nil {
		t.Fatalf("read hook stdin: %v", err)
	}
	var env Envelope
	if err := json.Unmarshal(data, &env); err != nil {
		t.Fatalf("parse hook stdin: %v", err)
	}
	if env.Point != OnSessionStart || env.AgentID != "main-1" {
		t.Fatalf("unexpected envelope: %#v", env)
	}

	envVar, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatalf("read env file: %v", err)
	}
	if string(envVar) != OnSessionStart {
		t.Fatalf("expected env var %q, got %q", OnSessionStart, string(envVar))
	}
}

func TestCommandEngine_ArgvMode(t *testing.T) {
	e := NewCommandEngine(map[string][]HookDef{
		OnToolCall: {{
			Name:    "argv",
			Point:   OnToolCall,
			Command: Command{Args: []string{"sh", "-c", `echo '{"action":"continue"}'`}},
		}},
	})

	result, err := e.Fire(context.Background(), testEnv(OnToolCall, map[string]any{
		"tool_name": "Bash",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Action != ActionContinue {
		t.Fatalf("expected continue, got %q", result.Action)
	}
}

func TestCommandEngine_RunAutomationJoinAndBackground(t *testing.T) {
	bgMarker := filepath.Join(t.TempDir(), "background")
	e := NewCommandEngine(map[string][]HookDef{
		OnToolBatchComplete: {
			{
				Name:    "background",
				Point:   OnToolBatchComplete,
				Command: Command{Shell: fmt.Sprintf(`touch %s && echo '{"status":"success","summary":"bg"}'`, bgMarker)},
				Join:    JoinBackground,
			},
			{
				Name:    "join",
				Point:   OnToolBatchComplete,
				Command: Command{Shell: `printf '%s' '{"status":"failed","summary":"tests failed","body":"line1\\nline2","severity":"error"}'`},
				Join:    JoinBeforeNextLLM,
				Result:  ResultAppendOnFailure,
			},
		},
	})

	results, err := e.RunAutomation(context.Background(), testEnv(OnToolBatchComplete, map[string]any{
		"tool_calls":    []any{},
		"changed_files": []any{},
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 join result, got %d", len(results))
	}
	if results[0].Result.Status != AutomationStatusFailed {
		t.Fatalf("expected failed status, got %q", results[0].Result.Status)
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(bgMarker); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("background automation hook did not run")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestNewCommandEngineFromList(t *testing.T) {
	e := NewCommandEngineFromList([]HookDef{
		{Name: "a", Point: OnToolCall, Command: Command{Shell: "true"}},
		{Name: "b", Point: OnToolCall, Command: Command{Shell: "true"}},
		{Name: "c", Point: OnIdle, Command: Command{Shell: "true"}},
	})

	if got := len(e.hooks[OnToolCall]); got != 2 {
		t.Fatalf("expected 2 tool hooks, got %d", got)
	}
	if got := len(e.hooks[OnIdle]); got != 1 {
		t.Fatalf("expected 1 idle hook, got %d", got)
	}
}
