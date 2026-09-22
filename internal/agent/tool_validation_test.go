package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/llm"
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

func TestClassifyToolArgsAbnormality(t *testing.T) {
	registry := tools.NewRegistry()
	registry.Register(agentValidationTool{
		name: "RequiredTool",
		schema: map[string]any{
			"type":     "object",
			"required": []string{"path"},
			"properties": map[string]any{
				"path": map[string]any{"type": "string"},
			},
		},
	})
	registry.Register(agentValidationTool{
		name: "OptionalTool",
		schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{"type": "string"},
			},
		},
	})

	tests := []struct {
		name                string
		registry            *tools.Registry
		toolName            string
		args                json.RawMessage
		wantMalformed       bool
		wantEmptyRequired   bool
		wantRequiredFields  []string
		wantAbnormalToolArg bool
	}{
		{
			name:                "malformed args sentinel",
			registry:            registry,
			toolName:            "RequiredTool",
			args:                json.RawMessage(llm.MalformedArgsSentinel),
			wantMalformed:       true,
			wantAbnormalToolArg: true,
		},
		{
			name:                "empty args with required fields",
			registry:            registry,
			toolName:            "RequiredTool",
			args:                json.RawMessage(`{}`),
			wantEmptyRequired:   true,
			wantRequiredFields:  []string{"path"},
			wantAbnormalToolArg: true,
		},
		{
			name:     "empty args without required fields",
			registry: registry,
			toolName: "OptionalTool",
			args:     json.RawMessage(`{}`),
		},
		{
			name:     "empty args with missing tool",
			registry: registry,
			toolName: "MissingTool",
			args:     json.RawMessage(`{}`),
		},
		{
			name:     "empty args with nil registry",
			registry: nil,
			toolName: "RequiredTool",
			args:     json.RawMessage(`{}`),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyToolArgsAbnormality(tt.registry, tt.toolName, tt.args)
			if got.Malformed != tt.wantMalformed || got.EmptyRequired != tt.wantEmptyRequired {
				t.Fatalf("classifyToolArgsAbnormality() = %#v, want Malformed=%v EmptyRequired=%v", got, tt.wantMalformed, tt.wantEmptyRequired)
			}
			if strings.Join(got.RequiredFields, ",") != strings.Join(tt.wantRequiredFields, ",") {
				t.Fatalf("RequiredFields = %v, want %v", got.RequiredFields, tt.wantRequiredFields)
			}
			if isAbnormalToolArgs(tt.registry, tt.toolName, tt.args) != tt.wantAbnormalToolArg {
				t.Fatalf("isAbnormalToolArgs() = %v, want %v", isAbnormalToolArgs(tt.registry, tt.toolName, tt.args), tt.wantAbnormalToolArg)
			}
		})
	}
}

func TestApplyConfirmedArgsEditsRejectsInvalidJSON(t *testing.T) {
	registry := tools.NewRegistry()
	registry.Register(agentValidationTool{
		name: "shell",
		schema: map[string]any{
			"type":     "object",
			"required": []string{"command"},
			"properties": map[string]any{
				"command": map[string]any{"type": "string"},
			},
		},
	})

	_, err := applyConfirmedArgsEdits(registry, permission.Ruleset{{Permission: "shell", Pattern: "*", Action: permission.ActionAsk}}, "shell", json.RawMessage(`{"command":"pwd"}`), `{"command":`, permission.PathScope{})
	if err == nil || !strings.Contains(err.Error(), "valid JSON") {
		t.Fatalf("err = %v, want invalid JSON error", err)
	}
}

func TestApplyConfirmedArgsEditsToleratesUnknownFields(t *testing.T) {
	registry := tools.NewRegistry()
	registry.Register(agentValidationTool{
		name: "read",
		schema: map[string]any{
			"type":     "object",
			"required": []string{"path"},
			"properties": map[string]any{
				"path": map[string]any{"type": "string"},
			},
			"additionalProperties": false,
		},
	})

	ruleset := permission.Ruleset{{Permission: "read", Pattern: "*", Action: permission.ActionAllow}}
	edited, err := applyConfirmedArgsEdits(registry, ruleset, "read", json.RawMessage(`{"path":"notes.txt"}`), `{"path":"other.txt","extra":1}`, permission.PathScope{})
	if err != nil {
		t.Fatalf("applyConfirmedArgsEdits: %v", err)
	}
	if !strings.Contains(string(edited), `"other.txt"`) {
		t.Fatalf("edited = %s, want user edit preserved", edited)
	}
}

func TestExecuteToolCallAskRequiresConfirmFunc(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.tools.Register(agentValidationTool{
		name: "shell",
		schema: map[string]any{
			"type":     "object",
			"required": []string{"command"},
			"properties": map[string]any{
				"command": map[string]any{"type": "string"},
			},
		},
	})
	a.ruleset = permission.Ruleset{{Permission: "shell", Pattern: "*", Action: permission.ActionAsk}}

	_, err := a.executeToolCall(context.Background(), message.ToolCall{
		ID:   "call-1",
		Name: "shell",
		Args: json.RawMessage(`{"command":"pwd"}`),
	})
	if err == nil || !strings.Contains(err.Error(), "requires confirmation") {
		t.Fatalf("expected requires confirmation error, got %v", err)
	}
}

func TestApplyConfirmedArgsEditsRejectsDeniedPermissionAfterEdit(t *testing.T) {
	registry := tools.NewRegistry()
	registry.Register(agentValidationTool{
		name: "read",
		schema: map[string]any{
			"type":     "object",
			"required": []string{"path"},
			"properties": map[string]any{
				"path": map[string]any{"type": "string"},
			},
		},
	})

	ruleset := permission.Ruleset{
		{Permission: "read", Pattern: "*", Action: permission.ActionAsk},
		{Permission: "read", Pattern: "secret/*", Action: permission.ActionDeny},
	}

	_, err := applyConfirmedArgsEdits(registry, ruleset, "read", json.RawMessage(`{"path":"notes.txt"}`), `{"path":"secret/plan.txt"}`, permission.PathScope{})
	if err == nil || !errors.Is(err, errEditedArgsPermissionDeny) {
		t.Fatalf("err = %v, want permission deny error", err)
	}
}

func TestApplyConfirmedArgsEditsBashDeniedBySubcommandPermission(t *testing.T) {
	registry := tools.NewRegistry()
	registry.Register(agentValidationTool{
		name: "shell",
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
		{Permission: "shell", Pattern: "*", Action: permission.ActionAsk},
		{Permission: "shell", Pattern: "rm *", Action: permission.ActionDeny},
	}

	_, err := applyConfirmedArgsEdits(
		registry,
		ruleset,
		"shell",
		json.RawMessage(`{"command":"pwd"}`),
		`{"command":"cd build && rm out.txt"}`,
		permission.PathScope{},
	)
	if err == nil || !errors.Is(err, errEditedArgsPermissionDeny) {
		t.Fatalf("err = %v, want permission deny error", err)
	}
}

func TestValidateToolCallArgsRejectsWrongType(t *testing.T) {
	registry := tools.NewRegistry()
	registry.Register(agentValidationTool{
		name: "shell",
		schema: map[string]any{
			"type":     "object",
			"required": []string{"timeout"},
			"properties": map[string]any{
				"timeout": map[string]any{"type": "integer"},
			},
		},
	})

	pipeline := toolExecutionPipeline{registry: registry}
	tc := message.ToolCall{Name: "shell", Args: json.RawMessage(`{"timeout":"fast"}`)}
	_, _, err := pipeline.validateToolCallArgs(&tc, nil)
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

func TestSyncAuditEffectiveArgsCreatesAuditForHookOnlyMutation(t *testing.T) {
	audit := syncAuditEffectiveArgs(nil, json.RawMessage(`{"path":"before.txt"}`), json.RawMessage(`{"path":"after.txt"}`))
	if audit == nil {
		t.Fatal("expected hook-only args mutation to create audit")
	}
	if audit.OriginalArgsJSON != `{"path":"before.txt"}` || audit.EffectiveArgsJSON != `{"path":"after.txt"}` {
		t.Fatalf("audit = %#v, want original/effective args persisted", audit)
	}
	if !audit.UserModified {
		t.Fatal("expected hook-only args mutation to mark UserModified")
	}
}

func TestModelRequestedToolArgsJSONUsesOriginalAuditArguments(t *testing.T) {
	original := `{"query":"cats"}`
	effective := `{"query":"cats","apiKey":"runtime-secret"}`
	audit := &message.ToolArgsAudit{OriginalArgsJSON: original, EffectiveArgsJSON: effective, UserModified: true}

	if got := modelRequestedToolArgsJSON(effective, audit); got != original {
		t.Fatalf("modelRequestedToolArgsJSON() = %q, want %q", got, original)
	}
	if got := modelRequestedToolArgsJSON(effective, nil); got != effective {
		t.Fatalf("modelRequestedToolArgsJSON() without audit = %q, want %q", got, effective)
	}
}

// TestPromotedToolAuditKeepsExecutionTruth verifies that layering an
// on_tool_call hook's audit over a promoted speculative execution keeps the
// hook's provenance while reporting what actually ran: the sanitized arguments
// and the fields the schema check stripped. Promote only succeeds on
// canonically identical arguments, so the hook's own copy is the one that never
// executed, and taking it wholesale used to erase the IgnoredArgs record that
// the tool card renders struck through.
func TestPromotedToolAuditKeepsExecutionTruth(t *testing.T) {
	hookAudit := &message.ToolArgsAudit{
		OriginalArgsJSON:  `{"path":"a.go","bogus":1}`,
		EffectiveArgsJSON: `{"bogus":1,"path":"a.go"}`,
		UserModified:      true,
		EditSummary:       "hook rewrote args",
	}
	executed := &message.ToolArgsAudit{
		EffectiveArgsJSON: `{"path":"a.go"}`,
		IgnoredArgs: []message.IgnoredToolArg{
			{Path: "args.bogus", ValueJSON: "1", Reason: message.IgnoredToolArgReasonUnrecognized},
		},
		InvalidArgs: []message.InvalidToolArg{
			{Path: "args.path", ValueJSON: "1", Reason: message.InvalidToolArgReasonInvalid},
		},
	}

	merged := promotedToolAudit(hookAudit, executed, `{"path":"a.go"}`)
	if merged.EffectiveArgsJSON != `{"path":"a.go"}` {
		t.Fatalf("EffectiveArgsJSON = %q, want the arguments that actually ran", merged.EffectiveArgsJSON)
	}
	if len(merged.IgnoredArgs) != 1 || merged.IgnoredArgs[0].Path != "args.bogus" {
		t.Fatalf("IgnoredArgs = %#v, want the record from the promoted execution", merged.IgnoredArgs)
	}
	if len(merged.InvalidArgs) != 1 || merged.InvalidArgs[0].Path != "args.path" {
		t.Fatalf("InvalidArgs = %#v, want the record from the promoted execution", merged.InvalidArgs)
	}
	if merged.OriginalArgsJSON != hookAudit.OriginalArgsJSON || !merged.UserModified || merged.EditSummary != "hook rewrote args" {
		t.Fatalf("hook provenance was lost: %#v", merged)
	}

	merged.IgnoredArgs[0].Path = "changed"
	if executed.IgnoredArgs[0].Path != "args.bogus" {
		t.Fatal("promotedToolAudit aliased the promoted execution's IgnoredArgs")
	}
	if hookAudit.EffectiveArgsJSON != `{"bogus":1,"path":"a.go"}` {
		t.Fatal("promotedToolAudit mutated the hook audit in place")
	}

	if got := promotedToolAudit(nil, executed, `{"path":"a.go"}`); got != executed {
		t.Fatal("without a hook audit the promoted execution's audit must pass through untouched")
	}
}
