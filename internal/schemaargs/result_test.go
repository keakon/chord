package schemaargs

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
)

func resultContractTestSchema() map[string]any {
	return map[string]any{
		"type":     "object",
		"required": []string{"summary"},
		"properties": map[string]any{
			"summary": map[string]any{"type": "string"},
			"status":  map[string]any{"type": "string", "enum": []any{"ok", "failed"}},
			"files": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type":     "object",
					"required": []string{"path"},
					"properties": map[string]any{
						"path": map[string]any{"type": "string"},
					},
				},
			},
		},
	}
}

// The delivered-result path must report the whole picture in one rejection:
// a missing required field, a wrong type, a bad enum and a nested array item
// violation all show up together instead of one per round trip.
func TestValidateResultCollectsAllViolations(t *testing.T) {
	violations, err := ValidateResult(json.RawMessage(
		`{"summary":7,"status":"weird","files":[{"path":1},{"size":2}]}`), resultContractTestSchema())
	if err != nil {
		t.Fatalf("ValidateResult returned error: %v", err)
	}
	got := make(map[string]Violation, len(violations))
	for _, violation := range violations {
		got[violation.Invalid.Path] = violation
	}
	if len(got) != len(violations) {
		t.Fatalf("violations = %#v, want one per path", violations)
	}
	cases := []struct {
		path       string
		reason     message.InvalidToolArgReason
		valueJSON  string
		wantInText string
	}{
		{path: "result.summary", reason: message.InvalidToolArgReasonInvalid, valueJSON: "7", wantInText: "result.summary must be a string, got number 7"},
		{path: "result.files[0].path", reason: message.InvalidToolArgReasonInvalid, valueJSON: "1", wantInText: "result.files[0].path must be a string, got number 1"},
		{path: "result.files[1].path", reason: message.InvalidToolArgReasonMissing, wantInText: "result.files[1].path is required"},
		{path: "result.status", reason: message.InvalidToolArgReasonInvalid, valueJSON: `"weird"`, wantInText: "result.status must be one of ok, failed, got string \"weird\""},
	}
	for _, tc := range cases {
		violation, ok := got[tc.path]
		if !ok {
			t.Fatalf("missing violation for %q in %#v", tc.path, violations)
		}
		if violation.Invalid.Reason != tc.reason {
			t.Fatalf("%s reason = %q, want %q", tc.path, violation.Invalid.Reason, tc.reason)
		}
		if violation.Invalid.ValueJSON != tc.valueJSON {
			t.Fatalf("%s ValueJSON = %q, want %q", tc.path, violation.Invalid.ValueJSON, tc.valueJSON)
		}
		if !strings.Contains(violation.Message, tc.wantInText) {
			t.Fatalf("%s message = %q, want it to contain %q", tc.path, violation.Message, tc.wantInText)
		}
	}
}

// A result contract is deliberately open: undeclared fields are silently
// accepted, and validating a delivered value never strips anything from it.
func TestValidateResultKeepsDeliveredValueAndAllowsExtraFields(t *testing.T) {
	schema := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"summary"},
		"properties": map[string]any{
			"summary": map[string]any{"type": "string"},
			"note":    map[string]any{"type": "string"},
		},
	}
	raw := json.RawMessage(`{"summary":"done","extra":{"keep":true},"note":null}`)
	violations, err := ValidateResult(raw, schema)
	if err != nil {
		t.Fatalf("ValidateResult returned error: %v", err)
	}
	if len(violations) != 0 {
		t.Fatalf("violations = %#v, want none (extra fields are allowed and an optional null means omitted)", violations)
	}
	if string(raw) != `{"summary":"done","extra":{"keep":true},"note":null}` {
		t.Fatal("ValidateResult mutated the delivered result")
	}

	// Pin the same guarantee on the decoded value the engine validates, so a
	// later "fix" that re-enables stripping for additionalProperties fails here.
	decoded := map[string]any{"summary": "done", "extra": map[string]any{"keep": true}, "note": nil}
	check := &schemaCheck{readOnly: true, collectAll: true}
	validateSchemaValue(decoded, schema, resultSchemaPath, check)
	if len(check.violations) != 0 {
		t.Fatalf("violations = %#v, want none", check.violations)
	}
	if _, ok := decoded["extra"]; !ok {
		t.Fatal("read-only validation stripped an undeclared field from the delivered value")
	}
	if value, ok := decoded["note"]; !ok || value != nil {
		t.Fatalf("read-only validation touched the optional null: %#v", decoded)
	}
}

func TestValidateResultRejectsNonObjectTopLevel(t *testing.T) {
	violations, err := ValidateResult(json.RawMessage(`[1,2]`), resultContractTestSchema())
	if err != nil {
		t.Fatalf("ValidateResult returned error: %v", err)
	}
	if len(violations) != 1 {
		t.Fatalf("violations = %#v, want one top-level type violation", violations)
	}
	if violations[0].Invalid.Path != "result" || !strings.Contains(violations[0].Message, "must be an object, got array with 2 item(s)") {
		t.Fatalf("violation = %#v, want a top-level object violation", violations[0])
	}
}

func TestValidateResultRejectsMalformedResult(t *testing.T) {
	cases := []struct {
		name   string
		result string
	}{
		{name: "empty", result: ``},
		{name: "truncated", result: `{"summary":`},
		{name: "trailing garbage", result: `{"summary":"done"} extra`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ValidateResult(json.RawMessage(tc.result), resultContractTestSchema()); err == nil {
				t.Fatalf("ValidateResult(%q) = nil error, want a decode error", tc.result)
			}
		})
	}
}

func TestValidateResultWithoutSchemaPassesAnything(t *testing.T) {
	violations, err := ValidateResult(json.RawMessage(`"not an object"`), nil)
	if err != nil || len(violations) != 0 {
		t.Fatalf("violations, err = %#v, %v, want no contract and no error", violations, err)
	}
}
