package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

type validationStubTool struct {
	name   string
	schema map[string]any
}

func (t validationStubTool) Name() string               { return t.name }
func (validationStubTool) Description() string          { return "stub" }
func (t validationStubTool) Parameters() map[string]any { return t.schema }
func (validationStubTool) Execute(context.Context, json.RawMessage) (string, error) {
	return "", nil
}
func (validationStubTool) IsReadOnly() bool { return true }

func TestValidateToolArgsRejectsInvalidJSON(t *testing.T) {
	tool := validationStubTool{
		name: "Stub",
		schema: map[string]any{
			"type": "object",
		},
	}
	err := ValidateToolArgs(tool, json.RawMessage(`{"a":`))
	if err == nil || !strings.Contains(err.Error(), "valid JSON") {
		t.Fatalf("err = %v, want valid JSON error", err)
	}
}

func TestValidateToolArgsRejectsMissingRequiredField(t *testing.T) {
	tool := validationStubTool{
		name: "Stub",
		schema: map[string]any{
			"type":     "object",
			"required": []string{"command"},
			"properties": map[string]any{
				"command": map[string]any{"type": "string"},
			},
		},
	}
	err := ValidateToolArgs(tool, json.RawMessage(`{}`))
	if err == nil || !strings.Contains(err.Error(), "args.command is required") {
		t.Fatalf("err = %v, want required-field error", err)
	}
}

func TestValidateToolArgsRejectsWrongTypeInArray(t *testing.T) {
	tool := validationStubTool{
		name: "Delete",
		schema: map[string]any{
			"type":     "object",
			"required": []string{"paths"},
			"properties": map[string]any{
				"paths": map[string]any{
					"type":     "array",
					"minItems": 1,
					"items":    map[string]any{"type": "string"},
				},
			},
		},
	}
	err := ValidateToolArgs(tool, json.RawMessage(`{"paths":["ok", 3]}`))
	if err == nil || !strings.Contains(err.Error(), "args.paths[1] must be a string") {
		t.Fatalf("err = %v, want array type error", err)
	}
}

func TestValidateToolArgsRejectsEnumMismatch(t *testing.T) {
	tool := validationStubTool{
		name: "Lsp",
		schema: map[string]any{
			"type":     "object",
			"required": []string{"operation"},
			"properties": map[string]any{
				"operation": map[string]any{
					"type": "string",
					"enum": []any{"definition", "references"},
				},
			},
		},
	}
	err := ValidateToolArgs(tool, json.RawMessage(`{"operation":"hover"}`))
	if err == nil || !strings.Contains(err.Error(), "must be one of definition, references") {
		t.Fatalf("err = %v, want enum error", err)
	}
}

func TestValidateToolArgsAcceptsValidArgs(t *testing.T) {
	tool := validationStubTool{
		name: "Bash",
		schema: map[string]any{
			"type":     "object",
			"required": []string{"command", "timeout"},
			"properties": map[string]any{
				"command": map[string]any{"type": "string"},
				"timeout": map[string]any{"type": "integer"},
			},
		},
	}
	if err := ValidateToolArgs(tool, json.RawMessage(`{"command":"pwd","timeout":30}`)); err != nil {
		t.Fatalf("ValidateToolArgs returned error: %v", err)
	}
}
