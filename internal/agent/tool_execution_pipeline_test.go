package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/hook"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/tools"
)

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
