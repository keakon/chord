package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
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
