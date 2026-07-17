package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/filelock"
	"github.com/keakon/chord/internal/hook"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/tools"
)

func TestWrapStaleEditError(t *testing.T) {
	base := errors.New("hunk did not apply")
	wrapped := wrapStaleEditError(base)
	if !errors.Is(wrapped, base) {
		t.Fatalf("wrapped error does not unwrap to the original: %v", wrapped)
	}
	msg := wrapped.Error()
	if !strings.HasPrefix(msg, base.Error()) {
		t.Fatalf("wrapped error should preserve the original prefix, got %q", msg)
	}
	if !strings.Contains(msg, "file changed on disk") {
		t.Fatalf("wrapped error missing stale-content note, got %q", msg)
	}
}

func TestFormatToolExecutionOutputKeepsQuestionResultUntruncated(t *testing.T) {
	longAnswer := strings.Repeat("长", tools.MaxLineLength+100)
	result := `[{"header":"你怎么教出来","selected":["` + longAnswer + `"]}]`

	got := formatToolExecutionOutput(result, t.TempDir(), "call-question", tools.NameQuestion, nil, "")

	if got != result {
		t.Fatalf("question result was changed or truncated: got len=%d want len=%d", len(got), len(result))
	}
	if !strings.Contains(got, longAnswer) {
		t.Fatal("question result lost the long free-text answer")
	}
}

func TestFormatToolExecutionOutputStillTruncatesOtherToolLongLines(t *testing.T) {
	result := strings.Repeat("a", tools.MaxLineLength+100)
	sessionDir := t.TempDir()

	got := formatToolExecutionOutput(result, sessionDir, "call-other", tools.NameShell, nil, "")

	want := strings.Repeat("a", tools.MaxLineLength) + "..."
	if !strings.Contains(got, want) {
		t.Fatalf("non-question result = %q, want truncated output containing %q", got, want)
	}
	if !strings.Contains(got, "Full output saved to ") || !strings.Contains(got, filepath.Join(sessionDir, "tool-outputs", "call-other.log")) {
		t.Fatalf("non-question truncation should include saved artifact guidance, got %q", got)
	}
}

func TestToolExecutionPipelineWriteUpdatesFileStateAndTracker(t *testing.T) {
	projectRoot := t.TempDir()
	path := filepath.Join(projectRoot, "notes.txt")
	if err := os.WriteFile(path, []byte("old\n"), 0o644); err != nil {
		t.Fatalf("write seed file: %v", err)
	}

	tracker := filelock.NewFileTracker()
	tracker.TrackSnapshot(path, "agent-1", computeFileHash(path))
	registry := tools.NewRegistry()
	registry.Register(tools.WriteTool{})
	pipeline := toolExecutionPipeline{
		agentID:     "agent-1",
		registry:    registry,
		fileTrack:   tracker,
		fileBackups: newFileBackupManager(filepath.Join(projectRoot, ".chord", "sessions", "test")),
		projectRoot: projectRoot,
	}
	call := message.ToolCall{
		ID:   "write-1",
		Name: tools.NameWrite,
		Args: json.RawMessage(`{"path":"` + path + `","content":"new\n"}`),
	}

	result, err := pipeline.execute(context.Background(), call, false)
	if err != nil {
		t.Fatalf("execute write: %v", err)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "new\n" {
		t.Fatalf("file content = %q, %v; want new\\n", got, err)
	}
	if result.FileState == nil || len(result.FileState.Writes) != 1 {
		t.Fatalf("FileState = %#v, want one write", result.FileState)
	}
	writeState := result.FileState.Writes[0]
	if writeState.Path != path || !writeState.Exists || writeState.SHA256 == "" {
		t.Fatalf("write state = %#v, want existing hashed path %q", writeState, path)
	}
	wantHash := computeFileHash(path)
	if writeState.SHA256 != wantHash {
		t.Fatalf("write hash = %q, want current disk hash %q", writeState.SHA256, wantHash)
	}

	status, err := tracker.AcquireWriteStatus(path, "agent-1", wantHash)
	if err != nil {
		t.Fatalf("AcquireWriteStatus after pipeline release: %v", err)
	}
	defer tracker.AbortWrite(path, "agent-1")
	if status.ExternalChanged {
		t.Fatal("tracker still reports external change after successful pipeline write")
	}
}

func TestToolExecutionPipelineStaleWriteBacksUpCurrentFile(t *testing.T) {
	projectRoot := t.TempDir()
	sessionDir := filepath.Join(projectRoot, ".chord", "sessions", "test")
	path := filepath.Join(projectRoot, "notes.txt")
	if err := os.WriteFile(path, []byte("old\n"), 0o644); err != nil {
		t.Fatalf("write seed file: %v", err)
	}

	tracker := filelock.NewFileTracker()
	tracker.TrackSnapshot(path, "agent-1", computeFileHash(path))
	if err := os.WriteFile(path, []byte("external\n"), 0o644); err != nil {
		t.Fatalf("write external change: %v", err)
	}
	registry := tools.NewRegistry()
	registry.Register(tools.WriteTool{})
	pipeline := toolExecutionPipeline{
		agentID:     "agent-1",
		registry:    registry,
		fileTrack:   tracker,
		fileBackups: newFileBackupManager(sessionDir),
		projectRoot: projectRoot,
	}
	call := message.ToolCall{
		ID:   "write-1",
		Name: tools.NameWrite,
		Args: json.RawMessage(`{"path":"` + path + `","content":"new\n"}`),
	}

	result, err := pipeline.execute(context.Background(), call, false)
	if err != nil {
		t.Fatalf("execute write: %v", err)
	}
	for _, want := range []string{"Warning: the file changed on disk", "Backup saved to:"} {
		if !strings.Contains(result.Result, want) {
			t.Fatalf("result = %q, want substring %q", result.Result, want)
		}
	}
	backups, err := filepath.Glob(filepath.Join(sessionDir, "backups", "*", "*"))
	if err != nil {
		t.Fatalf("glob backups: %v", err)
	}
	if len(backups) != 1 {
		t.Fatalf("backups = %#v, want one backup", backups)
	}
	if got, err := os.ReadFile(backups[0]); err != nil || string(got) != "external\n" {
		t.Fatalf("backup content = %q, %v; want external\\n", got, err)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "new\n" {
		t.Fatalf("file content = %q, %v; want new\\n", got, err)
	}
}

func TestToolExecutionPipelineWriteConflictIsWrappedAndDoesNotExecute(t *testing.T) {
	projectRoot := t.TempDir()
	path := filepath.Join(projectRoot, "notes.txt")
	if err := os.WriteFile(path, []byte("old\n"), 0o644); err != nil {
		t.Fatalf("write seed file: %v", err)
	}

	tracker := filelock.NewFileTracker()
	if err := tracker.AcquireWrite(path, "other-agent", computeFileHash(path)); err != nil {
		t.Fatalf("seed conflicting write lock: %v", err)
	}
	defer tracker.AbortWrite(path, "other-agent")
	registry := tools.NewRegistry()
	registry.Register(tools.WriteTool{})
	pipeline := toolExecutionPipeline{agentID: "agent-1", registry: registry, fileTrack: tracker, projectRoot: projectRoot}
	call := message.ToolCall{
		ID:   "write-1",
		Name: tools.NameWrite,
		Args: json.RawMessage(`{"path":"` + path + `","content":"new\n"}`),
	}

	_, err := pipeline.execute(context.Background(), call, false)
	if err == nil {
		t.Fatal("execute write succeeded, want conflict error")
	}
	if !strings.Contains(err.Error(), "file conflict:") || !strings.Contains(err.Error(), "being written by other-agent") {
		t.Fatalf("err = %q, want wrapped file conflict", err.Error())
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "old\n" {
		t.Fatalf("file content after conflict = %q, %v; want old\\n", got, err)
	}
}

type requiredValueTool struct{}

func (requiredValueTool) Name() string        { return "RequiredValue" }
func (requiredValueTool) Description() string { return "requires a value" }
func (requiredValueTool) Parameters() map[string]any {
	return map[string]any{
		"type":     "object",
		"required": []any{"value"},
		"properties": map[string]any{
			"value": map[string]any{"type": "string"},
		},
	}
}
func (requiredValueTool) Execute(_ context.Context, args json.RawMessage) (string, error) {
	var req struct {
		Value string `json:"value"`
	}
	if err := json.Unmarshal(args, &req); err != nil {
		return "", err
	}
	return "value=" + req.Value, nil
}
func (requiredValueTool) IsReadOnly() bool { return true }

func newToolPipelineConsistencyAgents(t *testing.T) (*MainAgent, *SubAgent) {
	t.Helper()
	parent := newTestMainAgent(t, t.TempDir())
	reg := tools.NewRegistry()
	reg.Register(requiredValueTool{})
	parent.tools = reg
	sub := NewSubAgent(SubAgentConfig{
		InstanceID:   "worker-1",
		TaskID:       "adhoc-1",
		AgentDefName: "worker",
		TaskDesc:     "check tool execution",
		LLMClient:    newTestLLMClient(),
		Recovery:     parent.recovery,
		Parent:       parent,
		ParentCtx:    parent.parentCtx,
		Cancel:       func() {},
		BaseTools:    reg,
		WorkDir:      t.TempDir(),
		SessionDir:   parent.sessionDir,
		ModelName:    "test-model",
	})
	return parent, sub
}

func TestMainAndSubToolExecutionPipelineConsistentValidation(t *testing.T) {
	mainAgent, subAgent := newToolPipelineConsistencyAgents(t)
	tests := []struct {
		name        string
		args        json.RawMessage
		wantErrText string
		wantResult  string
	}{
		{
			name:        "malformed sentinel",
			args:        json.RawMessage(`{"error":"malformed tool call arguments from model"}`),
			wantErrText: "malformed arguments",
		},
		{
			name:        "empty required args",
			args:        json.RawMessage(`{}`),
			wantErrText: "empty arguments",
		},
		{
			name:       "valid call",
			args:       json.RawMessage(`{"value":"ok"}`),
			wantResult: "value=ok",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			call := message.ToolCall{ID: "call-1", Name: "RequiredValue", Args: tc.args}
			mainResult, mainErr := mainAgent.executeToolCallWithHook(context.Background(), call, false)
			subResult, subErr := subAgent.executeToolCallWithHook(context.Background(), call, false)

			if tc.wantErrText != "" {
				if mainErr == nil || !strings.Contains(mainErr.Error(), tc.wantErrText) {
					t.Fatalf("main err = %v, want containing %q", mainErr, tc.wantErrText)
				}
				if subErr == nil || !strings.Contains(subErr.Error(), tc.wantErrText) {
					t.Fatalf("sub err = %v, want containing %q", subErr, tc.wantErrText)
				}
				if mainResult.EffectiveArgsJSON != subResult.EffectiveArgsJSON {
					t.Fatalf("effective args diverged: main=%q sub=%q", mainResult.EffectiveArgsJSON, subResult.EffectiveArgsJSON)
				}
				return
			}

			if mainErr != nil || subErr != nil {
				t.Fatalf("main err = %v, sub err = %v", mainErr, subErr)
			}
			if mainResult.Result != tc.wantResult || subResult.Result != tc.wantResult {
				t.Fatalf("results = main %q sub %q, want %q", mainResult.Result, subResult.Result, tc.wantResult)
			}
		})
	}
}

type fixedToolHookEngine struct {
	result *hook.Result
}

func (e fixedToolHookEngine) Fire(_ context.Context, env hook.Envelope) (*hook.Result, error) {
	if env.Point != hook.OnToolCall || e.result == nil {
		return &hook.Result{Action: hook.ActionContinue}, nil
	}
	return e.result, nil
}

func (e fixedToolHookEngine) FireBackground(context.Context, hook.Envelope) {}

func (e fixedToolHookEngine) RunAutomation(context.Context, hook.Envelope) ([]hook.AutomationJobResult, error) {
	return nil, nil
}

func TestMainAndSubToolExecutionPipelineConsistentPermissionDecisions(t *testing.T) {
	tests := []struct {
		name        string
		ruleset     permission.Ruleset
		confirm     ConfirmFunc
		wantErrText string
		wantResult  string
		wantArgs    string
	}{
		{
			name:        "deny",
			ruleset:     permission.Ruleset{{Permission: "RequiredValue", Pattern: "*", Action: permission.ActionDeny}},
			wantErrText: "denied by permission policy",
			wantArgs:    `{"value":"old"}`,
		},
		{
			name:    "ask allow with modified args",
			ruleset: permission.Ruleset{{Permission: "RequiredValue", Pattern: "*", Action: permission.ActionAsk}},
			confirm: func(context.Context, string, string, []string, []string, []string, []string) (ConfirmResponse, error) {
				return ConfirmResponse{Approved: true, FinalArgsJSON: `{"value":"new"}`, EditSummary: "changed value"}, nil
			},
			wantResult: "value=new",
			wantArgs:   `{"value":"new"}`,
		},
		{
			name:    "ask rejected",
			ruleset: permission.Ruleset{{Permission: "RequiredValue", Pattern: "*", Action: permission.ActionAsk}},
			confirm: func(context.Context, string, string, []string, []string, []string, []string) (ConfirmResponse, error) {
				return ConfirmResponse{Approved: false, DenyReason: "nope"}, nil
			},
			wantErrText: "rejected by user: nope",
			wantArgs:    `{"value":"old"}`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mainAgent, subAgent := newToolPipelineConsistencyAgents(t)
			mainAgent.ruleset = tc.ruleset
			subAgent.ruleset = tc.ruleset
			mainAgent.confirmFn = tc.confirm
			call := message.ToolCall{ID: "call-1", Name: "RequiredValue", Args: json.RawMessage(`{"value":"old"}`)}

			mainResult, mainErr := mainAgent.executeToolCallWithHook(context.Background(), call, false)
			subResult, subErr := subAgent.executeToolCallWithHook(context.Background(), call, false)

			if tc.wantErrText != "" {
				if mainErr == nil || !strings.Contains(mainErr.Error(), tc.wantErrText) {
					t.Fatalf("main err = %v, want containing %q", mainErr, tc.wantErrText)
				}
				if subErr == nil || !strings.Contains(subErr.Error(), tc.wantErrText) {
					t.Fatalf("sub err = %v, want containing %q", subErr, tc.wantErrText)
				}
			} else if mainErr != nil || subErr != nil {
				t.Fatalf("main err = %v, sub err = %v", mainErr, subErr)
			}
			if mainResult.Result != tc.wantResult || subResult.Result != tc.wantResult {
				t.Fatalf("results = main %q sub %q, want %q", mainResult.Result, subResult.Result, tc.wantResult)
			}
			if mainResult.EffectiveArgsJSON != tc.wantArgs || subResult.EffectiveArgsJSON != tc.wantArgs {
				t.Fatalf("effective args = main %q sub %q, want %q", mainResult.EffectiveArgsJSON, subResult.EffectiveArgsJSON, tc.wantArgs)
			}
		})
	}
}

func TestToolExecutionPipelineRejectsMalformedOrUnknownToolBeforePermission(t *testing.T) {
	registry := tools.NewRegistry()
	registry.Register(requiredValueTool{})
	pipeline := toolExecutionPipeline{
		registry: registry,
		currentRuleset: func() permission.Ruleset {
			return permission.Ruleset{{Permission: "*", Pattern: "*", Action: permission.ActionDeny}}
		},
	}

	tests := []struct {
		name        string
		toolName    string
		wantErrText string
	}{
		{name: "missing name", toolName: "", wantErrText: "malformed tool call: missing tool name"},
		{name: "whitespace name", toolName: "  ", wantErrText: "malformed tool call: missing tool name"},
		{name: "unknown tool", toolName: "missing_tool", wantErrText: "tool not found: missing_tool"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := pipeline.execute(context.Background(), message.ToolCall{ID: "call-1", Name: tc.toolName, Args: json.RawMessage(`{}`)}, false)
			if err == nil || !strings.Contains(err.Error(), tc.wantErrText) {
				t.Fatalf("err = %v, want containing %q", err, tc.wantErrText)
			}
			if strings.Contains(err.Error(), "permission policy") {
				t.Fatalf("err = %v, should not be classified as permission denial", err)
			}
		})
	}
}

func TestMainAndSubToolExecutionPipelineConsistentHookHandling(t *testing.T) {
	tests := []struct {
		name        string
		hookResult  *hook.Result
		wantErrText string
		wantResult  string
		wantArgs    string
	}{
		{
			name:        "block",
			hookResult:  &hook.Result{Action: hook.ActionBlock, Message: "policy block"},
			wantErrText: "policy block",
			wantArgs:    `{"value":"old"}`,
		},
		{
			name:       "modify",
			hookResult: &hook.Result{Action: hook.ActionModify, Data: map[string]any{"args": map[string]any{"value": "hooked"}}},
			wantResult: "value=hooked",
			wantArgs:   `{"value":"hooked"}`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mainAgent, subAgent := newToolPipelineConsistencyAgents(t)
			mainAgent.hookEngine = fixedToolHookEngine{result: tc.hookResult}
			call := message.ToolCall{ID: "call-1", Name: "RequiredValue", Args: json.RawMessage(`{"value":"old"}`)}

			mainResult, mainErr := mainAgent.executeToolCallWithHook(context.Background(), call, true)
			subResult, subErr := subAgent.executeToolCallWithHook(context.Background(), call, true)

			if tc.wantErrText != "" {
				if mainErr == nil || !strings.Contains(mainErr.Error(), tc.wantErrText) {
					t.Fatalf("main err = %v, want containing %q", mainErr, tc.wantErrText)
				}
				if subErr == nil || !strings.Contains(subErr.Error(), tc.wantErrText) {
					t.Fatalf("sub err = %v, want containing %q", subErr, tc.wantErrText)
				}
			} else if mainErr != nil || subErr != nil {
				t.Fatalf("main err = %v, sub err = %v", mainErr, subErr)
			}
			if mainResult.Result != tc.wantResult || subResult.Result != tc.wantResult {
				t.Fatalf("results = main %q sub %q, want %q", mainResult.Result, subResult.Result, tc.wantResult)
			}
			if mainResult.EffectiveArgsJSON != tc.wantArgs || subResult.EffectiveArgsJSON != tc.wantArgs {
				t.Fatalf("effective args = main %q sub %q, want %q", mainResult.EffectiveArgsJSON, subResult.EffectiveArgsJSON, tc.wantArgs)
			}
		})
	}
}

func TestToolExecutionPipelineRechecksDelegatePermissionAfterHookModification(t *testing.T) {
	registry := tools.NewRegistry()
	registry.Register(agentValidationTool{
		name: tools.NameDelegate,
		schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"description": map[string]any{"type": "string"},
				"agent_type":  map[string]any{"type": "string"},
			},
			"required": []string{"description", "agent_type"},
		},
	})
	ruleset := permission.Ruleset{
		{Permission: tools.NameDelegate, Pattern: "*", Action: permission.ActionDeny},
		{Permission: tools.NameDelegate, Pattern: "reviewer", Action: permission.ActionAllow},
	}
	pipeline := toolExecutionPipeline{
		registry:       registry,
		currentRuleset: func() permission.Ruleset { return ruleset },
		fireHook: func(context.Context, string, uint64, map[string]any) (*hook.Result, error) {
			return &hook.Result{Action: hook.ActionModify, Data: map[string]any{
				"args": map[string]any{"description": "inspect", "agent_type": "tester"},
			}}, nil
		},
	}

	_, err := pipeline.execute(context.Background(), message.ToolCall{
		Name: tools.NameDelegate,
		Args: json.RawMessage(`{"description":"inspect","agent_type":"reviewer"}`),
	}, true)
	if err == nil || !errors.Is(err, errToolPermissionDenied) {
		t.Fatalf("execute() err = %v, want Delegate permission deny", err)
	}
}

func TestToolExecutionPipelineRechecksPermissionAfterHookModification(t *testing.T) {
	registry := tools.NewRegistry()
	registry.Register(requiredValueTool{})
	ruleset := permission.Ruleset{
		{Permission: "RequiredValue", Pattern: "*", Action: permission.ActionDeny},
		{Permission: "RequiredValue", Pattern: "safe", Action: permission.ActionAllow},
	}
	pipeline := toolExecutionPipeline{
		registry:       registry,
		currentRuleset: func() permission.Ruleset { return ruleset },
		fireHook: func(context.Context, string, uint64, map[string]any) (*hook.Result, error) {
			return &hook.Result{Action: hook.ActionModify, Data: map[string]any{
				"args": map[string]any{"value": "denied"},
			}}, nil
		},
	}

	_, err := pipeline.execute(context.Background(), message.ToolCall{
		Name: "RequiredValue",
		Args: json.RawMessage(`{"value":"safe"}`),
	}, true)
	if err == nil || !errors.Is(err, errToolPermissionDenied) {
		t.Fatalf("execute() err = %v, want permission deny for hook-modified args", err)
	}
}
