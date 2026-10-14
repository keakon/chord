package schemaargs

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func validateRawArguments(schema map[string]any, raw json.RawMessage) error {
	value, err := DecodeArguments(raw, nil)
	if err != nil {
		return err
	}
	return ValidateArguments(value, schema, nil, nil)
}

func TestValidateArgumentsRejectsInvalidJSON(t *testing.T) {
	schema := map[string]any{
		"type": "object",
	}
	err := validateRawArguments(schema, json.RawMessage(`{"a":`))
	if err == nil || !strings.Contains(err.Error(), "valid JSON") {
		t.Fatalf("err = %v, want valid JSON error", err)
	}
}

func TestValidateArgumentsRejectsMissingRequiredField(t *testing.T) {
	schema := map[string]any{
		"type":     "object",
		"required": []string{"command"},
		"properties": map[string]any{
			"command": map[string]any{"type": "string"},
		},
	}
	err := validateRawArguments(schema, json.RawMessage(`{}`))
	if err == nil || !strings.Contains(err.Error(), "args.command is required") {
		t.Fatalf("err = %v, want required-field error", err)
	}
}

func TestValidateArgumentsRejectsWrongTypeInArray(t *testing.T) {
	schema := map[string]any{
		"type":     "object",
		"required": []string{"paths"},
		"properties": map[string]any{
			"paths": map[string]any{
				"type":     "array",
				"minItems": 1,
				"items":    map[string]any{"type": "string"},
			},
		},
	}
	err := validateRawArguments(schema, json.RawMessage(`{"paths":["ok", 3]}`))
	if err == nil || !strings.Contains(err.Error(), "args.paths[1] must be a string") {
		t.Fatalf("err = %v, want array type error", err)
	}
}

func TestValidateArgumentsRejectsEnumMismatch(t *testing.T) {
	schema := map[string]any{
		"type":     "object",
		"required": []string{"operation"},
		"properties": map[string]any{
			"operation": map[string]any{
				"type": "string",
				"enum": []any{"definition", "references"},
			},
		},
	}
	err := validateRawArguments(schema, json.RawMessage(`{"operation":"hover"}`))
	if err == nil || !strings.Contains(err.Error(), "must be one of definition, references") {
		t.Fatalf("err = %v, want enum error", err)
	}
}

func TestValidateArgumentsTypeErrorsIncludeActualValue(t *testing.T) {
	schema := map[string]any{
		"type":     "object",
		"required": []string{"path"},
		"properties": map[string]any{
			"path":   map[string]any{"type": "string"},
			"offset": map[string]any{"type": "integer"},
			"mode":   map[string]any{"type": "string", "enum": []any{"line", "byte"}},
		},
	}
	cases := []struct {
		name string
		args string
		want string
	}{
		{name: "quoted number for integer field", args: `{"path":"sample.go","offset":"1.0"}`, want: `args.offset must be an integer, got string "1.0"`},
		{name: "null for string field", args: `{"path":null}`, want: `args.path must be a string, got null`},
		{name: "number for string field", args: `{"path":7}`, want: `args.path must be a string, got number 7`},
		{name: "enum mismatch shows value", args: `{"path":"sample.go","mode":"hover"}`, want: `args.mode must be one of line, byte, got string "hover"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateRawArguments(schema, json.RawMessage(tc.args))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want error containing %q", err, tc.want)
			}
		})
	}
}

func TestValidateArgumentsTypeErrorsTruncateLongValues(t *testing.T) {
	schema := map[string]any{
		"type":     "object",
		"required": []string{"command"},
		"properties": map[string]any{
			"command": map[string]any{"type": "string"},
			"timeout": map[string]any{"type": "integer"},
		},
	}
	long := strings.Repeat("x", 200)
	err := validateRawArguments(schema, json.RawMessage(fmt.Sprintf(`{"command":"echo ok","timeout":%q}`, long)))
	if err == nil || !strings.Contains(err.Error(), `args.timeout must be an integer, got string "`+strings.Repeat("x", maxArgValueDisplayRunes)+`…"`) {
		t.Fatalf("err = %v, want truncated long value preview", err)
	}
}

func TestValidateArgumentsAcceptsValidArgs(t *testing.T) {
	schema := map[string]any{
		"type":     "object",
		"required": []string{"command", "timeout"},
		"properties": map[string]any{
			"command": map[string]any{"type": "string"},
			"timeout": map[string]any{"type": "integer"},
		},
	}
	if err := validateRawArguments(schema, json.RawMessage(`{"command":"pwd","timeout":30}`)); err != nil {
		t.Fatalf("ValidateArguments returned error: %v", err)
	}
}

func TestValidateArgumentsCoercesScalarIntoArrayWhenOptedIn(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"pattern": map[string]any{"type": "string"},
			"paths": map[string]any{
				"type":             "array",
				"items":            map[string]any{"type": "string"},
				"coerceFromString": true,
			},
		},
		"required":             []string{"pattern"},
		"additionalProperties": false,
	}
	if err := validateRawArguments(schema, json.RawMessage(`{"pattern":"x","paths":"internal/tools"}`)); err != nil {
		t.Fatalf("scalar paths with coerceFromString should pass, got %v", err)
	}
	if err := validateRawArguments(schema, json.RawMessage(`{"pattern":"x","paths":7}`)); err == nil || !strings.Contains(err.Error(), "args.paths must be an array") {
		t.Fatalf("non-matching scalar type should still fail, got %v", err)
	}
}

func TestValidateArgumentsScalarRejectedWithoutCoerceOptIn(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"paths": map[string]any{
				"type":  "array",
				"items": map[string]any{"type": "string"},
			},
		},
		"required":             []string{"paths"},
		"additionalProperties": false,
	}
	err := validateRawArguments(schema, json.RawMessage(`{"paths":"x"}`))
	if err == nil || !strings.Contains(err.Error(), "args.paths must be an array") {
		t.Fatalf("scalar without opt-in should still fail, got %v", err)
	}
}

func TestValidateArgumentsCoercesSingleObjectIntoArrayWhenOptedIn(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"questions": map[string]any{
				"type":             "array",
				"items":            map[string]any{"type": "object"},
				"coerceFromObject": true,
			},
		},
		"required":             []string{"questions"},
		"additionalProperties": false,
	}
	if err := validateRawArguments(schema, json.RawMessage(`{"questions":{"header":"h","question":"q"}}`)); err != nil {
		t.Fatalf("single object with coerceFromObject should pass, got %v", err)
	}
	if err := validateRawArguments(schema, json.RawMessage(`{"questions":[{"header":"h"}]}`)); err != nil {
		t.Fatalf("array form should still pass, got %v", err)
	}
	if err := validateRawArguments(schema, json.RawMessage(`{"questions":"oops"}`)); err == nil || !strings.Contains(err.Error(), "args.questions must be an array") {
		t.Fatalf("non-object scalar should still fail, got %v", err)
	}
}

func TestValidateArgumentsSingleObjectRejectedWithoutCoerceOptIn(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"questions": map[string]any{
				"type":  "array",
				"items": map[string]any{"type": "object"},
			},
		},
		"required":             []string{"questions"},
		"additionalProperties": false,
	}
	if err := validateRawArguments(schema, json.RawMessage(`{"questions":{"header":"h"}}`)); err == nil || !strings.Contains(err.Error(), "args.questions must be an array") {
		t.Fatalf("single object without opt-in should still fail, got %v", err)
	}
}

// TestDecodeArgumentsRejectsDeepNesting bounds recursive descent so a
// pathologically deep document errors out instead of overflowing the stack.
func TestDecodeArgumentsRejectsDeepNesting(t *testing.T) {
	deep := strings.Repeat(`{"a":`, maxSchemaJSONDepth+1) + `0` + strings.Repeat(`}`, maxSchemaJSONDepth+1)
	if _, err := DecodeArguments(json.RawMessage(deep), nil); err == nil || !strings.Contains(err.Error(), "deeper than") {
		t.Fatalf("deep nesting err = %v, want depth-limit error", err)
	}
}
