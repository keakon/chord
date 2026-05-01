package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/tools"
)

type agentValidationTool struct {
	name   string
	schema map[string]any
}

func (t agentValidationTool) Name() string               { return t.name }
func (agentValidationTool) Description() string          { return "stub" }
func (t agentValidationTool) Parameters() map[string]any { return t.schema }
func (agentValidationTool) Execute(context.Context, json.RawMessage) (string, error) {
	return "", nil
}
func (agentValidationTool) IsReadOnly() bool { return true }

func TestApplyConfirmedArgsEditsRejectsInvalidJSON(t *testing.T) {
	registry := tools.NewRegistry()
	registry.Register(agentValidationTool{
		name: "Bash",
		schema: map[string]any{
			"type":     "object",
			"required": []string{"command"},
			"properties": map[string]any{
				"command": map[string]any{"type": "string"},
			},
		},
	})

	_, err := applyConfirmedArgsEdits(registry, permission.Ruleset{{Permission: "Bash", Pattern: "*", Action: permission.ActionAsk}}, "Bash", json.RawMessage(`{"command":"pwd"}`), `{"command":`)
	if err == nil || !strings.Contains(err.Error(), "valid JSON") {
		t.Fatalf("err = %v, want invalid JSON error", err)
	}
}

func TestExecuteToolCallAskRequiresConfirmFunc(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.tools.Register(agentValidationTool{
		name: "Bash",
		schema: map[string]any{
			"type":     "object",
			"required": []string{"command"},
			"properties": map[string]any{
				"command": map[string]any{"type": "string"},
			},
		},
	})
	a.ruleset = permission.Ruleset{{Permission: "Bash", Pattern: "*", Action: permission.ActionAsk}}

	_, err := a.executeToolCall(context.Background(), message.ToolCall{
		ID:   "call-1",
		Name: "Bash",
		Args: json.RawMessage(`{"command":"pwd"}`),
	})
	if err == nil || !strings.Contains(err.Error(), "requires confirmation") {
		t.Fatalf("expected requires confirmation error, got %v", err)
	}
}

func TestApplyConfirmedArgsEditsRejectsDeniedPermissionAfterEdit(t *testing.T) {
	registry := tools.NewRegistry()
	registry.Register(agentValidationTool{
		name: "Read",
		schema: map[string]any{
			"type":     "object",
			"required": []string{"path"},
			"properties": map[string]any{
				"path": map[string]any{"type": "string"},
			},
		},
	})

	ruleset := permission.Ruleset{
		{Permission: "Read", Pattern: "*", Action: permission.ActionAsk},
		{Permission: "Read", Pattern: "secret/*", Action: permission.ActionDeny},
	}

	_, err := applyConfirmedArgsEdits(registry, ruleset, "Read", json.RawMessage(`{"path":"notes.txt"}`), `{"path":"secret/plan.txt"}`)
	if err == nil || !errors.Is(err, errEditedArgsPermissionDeny) {
		t.Fatalf("err = %v, want permission deny error", err)
	}
}

func TestApplyConfirmedArgsEditsBashDeniedBySubcommandPermission(t *testing.T) {
	registry := tools.NewRegistry()
	registry.Register(agentValidationTool{
		name: "Bash",
		schema: map[string]any{
			"type":     "object",
			"required": []string{"command"},
			"properties": map[string]any{
				"command": map[string]any{"type": "string"},
			},
		},
	})

	ruleset := permission.Ruleset{
		{Permission: "*", Pattern: "*", Action: permission.ActionDeny},
		{Permission: "Bash", Pattern: "*", Action: permission.ActionAsk},
		{Permission: "Bash", Pattern: "rm *", Action: permission.ActionDeny},
	}

	_, err := applyConfirmedArgsEdits(
		registry,
		ruleset,
		"Bash",
		json.RawMessage(`{"command":"pwd"}`),
		`{"command":"cd build && rm out.txt"}`,
	)
	if err == nil || !errors.Is(err, errEditedArgsPermissionDeny) {
		t.Fatalf("err = %v, want permission deny error", err)
	}
}

func TestValidateToolArgsAgainstSchemaRejectsWrongType(t *testing.T) {
	registry := tools.NewRegistry()
	registry.Register(agentValidationTool{
		name: "Bash",
		schema: map[string]any{
			"type":     "object",
			"required": []string{"timeout"},
			"properties": map[string]any{
				"timeout": map[string]any{"type": "integer"},
			},
		},
	})

	err := validateToolArgsAgainstSchema(registry, "Bash", json.RawMessage(`{"timeout":"fast"}`))
	if err == nil || !strings.Contains(err.Error(), "args.timeout must be an integer") {
		t.Fatalf("err = %v, want schema type error", err)
	}
}

func TestBuildToolArgsAuditMarksUserModified(t *testing.T) {
	audit := buildToolArgsAudit(json.RawMessage(`{"command":"pwd"}`), json.RawMessage(`{"command":"ls"}`), "edited")
	if audit == nil {
		t.Fatal("expected non-nil audit")
	}
	if !audit.UserModified {
		t.Fatal("expected user_modified=true")
	}
	if audit.OriginalArgsJSON != `{"command":"pwd"}` {
		t.Fatalf("OriginalArgsJSON = %q", audit.OriginalArgsJSON)
	}
	if audit.EffectiveArgsJSON != `{"command":"ls"}` {
		t.Fatalf("EffectiveArgsJSON = %q", audit.EffectiveArgsJSON)
	}
	if audit.EditSummary != "edited" {
		t.Fatalf("EditSummary = %q", audit.EditSummary)
	}
}
