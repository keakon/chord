package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
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

func TestValidateToolArgsTypeErrorsIncludeActualValue(t *testing.T) {
	tool := validationStubTool{
		name: "Read",
		schema: map[string]any{
			"type":     "object",
			"required": []string{"path"},
			"properties": map[string]any{
				"path":   map[string]any{"type": "string"},
				"offset": map[string]any{"type": "integer"},
				"mode":   map[string]any{"type": "string", "enum": []any{"line", "byte"}},
			},
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
			err := ValidateToolArgs(tool, json.RawMessage(tc.args))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want error containing %q", err, tc.want)
			}
		})
	}
}

func TestValidateToolArgsTypeErrorsTruncateLongValues(t *testing.T) {
	tool := validationStubTool{
		name: "Shell",
		schema: map[string]any{
			"type":     "object",
			"required": []string{"command"},
			"properties": map[string]any{
				"command": map[string]any{"type": "string"},
				"timeout": map[string]any{"type": "integer"},
			},
		},
	}
	long := strings.Repeat("x", 200)
	err := ValidateToolArgs(tool, json.RawMessage(fmt.Sprintf(`{"command":"echo ok","timeout":%q}`, long)))
	if err == nil || !strings.Contains(err.Error(), `args.timeout must be an integer, got string "`+strings.Repeat("x", maxArgValueDisplayRunes)+`…"`) {
		t.Fatalf("err = %v, want truncated long value preview", err)
	}
}

func TestNullOptionalFieldsTreatedAsOmitted(t *testing.T) {
	tool := validationStubTool{
		name: "TodoWrite",
		schema: map[string]any{
			"type":     "object",
			"required": []string{"todos"},
			"properties": map[string]any{
				"todos": map[string]any{
					"type": "array",
					"items": map[string]any{
						"type":     "object",
						"required": []string{"id"},
						"properties": map[string]any{
							"id":          map[string]any{"type": "string"},
							"active_form": map[string]any{"type": "string"},
						},
					},
				},
			},
			"additionalProperties": false,
		},
	}
	raw := json.RawMessage(`{"todos":[{"id":"1","active_form":null},{"id":"2"}]}`)
	if err := ValidateToolArgs(tool, raw); err != nil {
		t.Fatalf("ValidateToolArgs = %v, want null optional field tolerated as omitted", err)
	}
	sanitized, ignored, err := SanitizeUnknownArgs(tool, raw)
	if err != nil {
		t.Fatalf("SanitizeUnknownArgs = %v, want null optional field tolerated as omitted", err)
	}
	want := []message.IgnoredToolArg{{Path: "args.todos[0].active_form", ValueJSON: "null", Reason: message.IgnoredToolArgReasonNull}}
	if !reflect.DeepEqual(ignored, want) {
		t.Fatalf("ignored = %#v, want %#v", ignored, want)
	}
	if strings.Contains(string(sanitized), "active_form") {
		t.Fatalf("sanitized args still carry the null field: %s", sanitized)
	}
	if err := ValidateToolArgs(tool, sanitized); err != nil {
		t.Fatalf("re-validating sanitized args failed: %v", err)
	}
}

func TestNullRequiredFieldStillRejected(t *testing.T) {
	tool := validationStubTool{
		name: "Stub",
		schema: map[string]any{
			"type":     "object",
			"required": []string{"path"},
			"properties": map[string]any{
				"path": map[string]any{"type": "string"},
			},
		},
	}
	err := ValidateToolArgs(tool, json.RawMessage(`{"path":null}`))
	if err == nil || !strings.Contains(err.Error(), "args.path must be a string") {
		t.Fatalf("err = %v, want null required field rejected by the type check", err)
	}
	_, _, invalid, err := SanitizeUnknownArgsWithDiagnostics(tool, json.RawMessage(`{"path":null}`))
	if err == nil {
		t.Fatal("SanitizeUnknownArgsWithDiagnostics = nil err, want null required field rejected")
	}
	if len(invalid) != 1 || invalid[0].Path != "args.path" || invalid[0].Reason != message.InvalidToolArgReasonInvalid {
		t.Fatalf("invalid = %#v, want args.path recorded as invalid", invalid)
	}
}

func TestSanitizeUnknownArgsRemovesUnrecognizedFields(t *testing.T) {
	tool := validationStubTool{
		name: "Read",
		schema: map[string]any{
			"type":     "object",
			"required": []string{"path"},
			"properties": map[string]any{
				"path": map[string]any{"type": "string"},
			},
			"additionalProperties": false,
		},
	}
	sanitized, ignored, err := SanitizeUnknownArgs(tool, json.RawMessage(`{"path":"a.txt","offset":10,"mode":"line"}`))
	if err != nil {
		t.Fatalf("SanitizeUnknownArgs returned error: %v", err)
	}
	if string(sanitized) == `{"path":"a.txt","offset":10,"mode":"line"}` {
		t.Fatalf("expected unknown fields stripped, got %s", sanitized)
	}
	if len(ignored) != 2 {
		t.Fatalf("ignored = %v, want 2 entries", ignored)
	}
	var got struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(sanitized, &got); err != nil {
		t.Fatalf("sanitized args invalid JSON: %v", err)
	}
	if got.Path != "a.txt" {
		t.Fatalf("known field stripped: sanitized=%s", sanitized)
	}
}

func TestSanitizeUnknownArgsKeepsIgnoredValues(t *testing.T) {
	tool := validationStubTool{
		name: "Read",
		schema: map[string]any{
			"type":     "object",
			"required": []string{"path"},
			"properties": map[string]any{
				"path":  map[string]any{"type": "string"},
				"limit": map[string]any{"type": "integer"},
			},
			"additionalProperties": false,
		},
	}
	sanitized, ignored, err := SanitizeUnknownArgs(tool, json.RawMessage(`{"path":"sample.go","limit":40,"format":"json"}`))
	if err != nil {
		t.Fatalf("SanitizeUnknownArgs returned error: %v", err)
	}
	if string(sanitized) != `{"limit":40,"path":"sample.go"}` {
		t.Fatalf("sanitized = %s", sanitized)
	}
	want := []message.IgnoredToolArg{{Path: "args.format", ValueJSON: `"json"`, Reason: message.IgnoredToolArgReasonUnrecognized}}
	if !reflect.DeepEqual(ignored, want) {
		t.Fatalf("ignored = %#v, want %#v", ignored, want)
	}
}

func TestSanitizeUnknownArgsRecordsShadowedDuplicateValues(t *testing.T) {
	tool := validationStubTool{
		name: "Read",
		schema: map[string]any{
			"type":     "object",
			"required": []string{"path"},
			"properties": map[string]any{
				"path":   map[string]any{"type": "string"},
				"limit":  map[string]any{"type": "integer"},
				"offset": map[string]any{"type": "integer"},
			},
			"additionalProperties": false,
		},
	}
	raw := json.RawMessage(`{"limit":75,"offset":664,"path":"first.go","limit":40,"offset":300,"path":"second.go"}`)
	sanitized, ignored, err := SanitizeUnknownArgs(tool, raw)
	if err != nil {
		t.Fatalf("SanitizeUnknownArgs returned error: %v", err)
	}
	if string(sanitized) != `{"limit":40,"offset":300,"path":"second.go"}` {
		t.Fatalf("sanitized = %s", sanitized)
	}
	want := []message.IgnoredToolArg{
		{Path: "args.limit", ValueJSON: "75", Reason: message.IgnoredToolArgReasonShadowed},
		{Path: "args.offset", ValueJSON: "664", Reason: message.IgnoredToolArgReasonShadowed},
		{Path: "args.path", ValueJSON: `"first.go"`, Reason: message.IgnoredToolArgReasonShadowed},
	}
	if !reflect.DeepEqual(ignored, want) {
		t.Fatalf("ignored = %#v, want %#v", ignored, want)
	}
}

func TestSanitizeUnknownArgsStripsNestedAndArrayItemFields(t *testing.T) {
	tool := validationStubTool{
		name: "Grep",
		schema: map[string]any{
			"type":     "object",
			"required": []string{"query"},
			"properties": map[string]any{
				"query": map[string]any{"type": "string"},
				"scope": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path": map[string]any{"type": "string"},
					},
					"additionalProperties": false,
				},
				"globs": map[string]any{
					"type": "array",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"pattern": map[string]any{"type": "string"},
						},
						"additionalProperties": false,
					},
				},
			},
			"additionalProperties": false,
		},
	}
	sanitized, ignored, err := SanitizeUnknownArgs(tool, json.RawMessage(`{"query":"todo","scope":{"path":"src","deep":1},"globs":[{"pattern":"*.go","extra":2}]}`))
	if err != nil {
		t.Fatalf("SanitizeUnknownArgs returned error: %v", err)
	}
	want := []string{"args.globs[0].extra", "args.scope.deep"}
	if len(ignored) != len(want) {
		t.Fatalf("ignored = %v, want %v", ignored, want)
	}
	for i, path := range want {
		if ignored[i].Path != path {
			t.Fatalf("ignored[%d].Path = %q, want %q", i, ignored[i].Path, path)
		}
	}
	var got struct {
		Scope struct {
			Path string `json:"path"`
		} `json:"scope"`
		Globs []struct {
			Pattern string `json:"pattern"`
		} `json:"globs"`
	}
	if err := json.Unmarshal(sanitized, &got); err != nil {
		t.Fatalf("sanitized args invalid JSON: %v", err)
	}
	if got.Scope.Path != "src" || len(got.Globs) != 1 || got.Globs[0].Pattern != "*.go" {
		t.Fatalf("known nested fields stripped: sanitized=%s", sanitized)
	}
}

func TestSanitizeUnknownArgsKeepsArgsWhenNoUnknown(t *testing.T) {
	tool := validationStubTool{
		name: "Read",
		schema: map[string]any{
			"type":     "object",
			"required": []string{"path"},
			"properties": map[string]any{
				"path": map[string]any{"type": "string"},
			},
			"additionalProperties": false,
		},
	}
	raw := json.RawMessage(`{"path":"a.txt"}`)
	sanitized, ignored, err := SanitizeUnknownArgs(tool, raw)
	if err != nil {
		t.Fatalf("SanitizeUnknownArgs returned error: %v", err)
	}
	if len(ignored) != 0 {
		t.Fatalf("ignored = %v, want none", ignored)
	}
	if string(sanitized) != string(raw) {
		t.Fatalf("sanitized = %s, want original %s", sanitized, raw)
	}
}

func TestSanitizeUnknownArgsStillRejectsMissingRequired(t *testing.T) {
	tool := validationStubTool{
		name: "Read",
		schema: map[string]any{
			"type":     "object",
			"required": []string{"path"},
			"properties": map[string]any{
				"path": map[string]any{"type": "string"},
			},
			"additionalProperties": false,
		},
	}
	if _, _, err := SanitizeUnknownArgs(tool, json.RawMessage(`{"offset":1}`)); err == nil || !strings.Contains(err.Error(), "args.path is required") {
		t.Fatalf("missing required should still fail, got %v", err)
	}
}

func TestSanitizeUnknownArgsWithDiagnosticsKeepsUnknownAndMissingMetadata(t *testing.T) {
	tool := validationStubTool{
		name: "Glob",
		schema: map[string]any{
			"type":                 "object",
			"required":             []string{"patterns"},
			"properties":           map[string]any{"patterns": map[string]any{"type": "array"}},
			"additionalProperties": false,
		},
	}
	_, ignored, invalid, err := SanitizeUnknownArgsWithDiagnostics(tool, json.RawMessage(`{".patterns":["MEMORY.md"]}`))
	if err == nil || !strings.Contains(err.Error(), "args.patterns is required") {
		t.Fatalf("err = %v, want missing patterns", err)
	}
	if len(ignored) != 1 || ignored[0].Path != "args..patterns" || ignored[0].Reason != message.IgnoredToolArgReasonUnrecognized {
		t.Fatalf("ignored = %#v, want unrecognized .patterns", ignored)
	}
	if len(invalid) != 1 || invalid[0].Path != "args.patterns" || invalid[0].Reason != message.InvalidToolArgReasonMissing {
		t.Fatalf("invalid = %#v, want missing patterns", invalid)
	}
}

func TestSanitizeUnknownArgsWithDiagnosticsKeepsInvalidValueMetadata(t *testing.T) {
	tool := validationStubTool{
		name: "Read",
		schema: map[string]any{
			"type":     "object",
			"required": []string{"limit"},
			"properties": map[string]any{
				"limit": map[string]any{"type": "integer"},
			},
		},
	}
	_, _, invalid, err := SanitizeUnknownArgsWithDiagnostics(tool, json.RawMessage(`{"limit":"forty"}`))
	if err == nil || !strings.Contains(err.Error(), "args.limit must be an integer") {
		t.Fatalf("err = %v, want type error", err)
	}
	if len(invalid) != 1 || invalid[0].Path != "args.limit" || invalid[0].ValueJSON != `"forty"` || invalid[0].Reason != message.InvalidToolArgReasonInvalid {
		t.Fatalf("invalid = %#v, want limit value metadata", invalid)
	}
}

func TestSanitizeUnknownArgsStillRejectsWrongType(t *testing.T) {
	tool := validationStubTool{
		name: "Read",
		schema: map[string]any{
			"type":     "object",
			"required": []string{"path"},
			"properties": map[string]any{
				"path": map[string]any{"type": "string"},
			},
			"additionalProperties": false,
		},
	}
	if _, _, err := SanitizeUnknownArgs(tool, json.RawMessage(`{"path":7}`)); err == nil || !strings.Contains(err.Error(), "args.path must be a string") {
		t.Fatalf("wrong type should still fail, got %v", err)
	}
}

func TestSanitizeUnknownArgsPreservesAliasParameters(t *testing.T) {
	// argumentAliaser fields are renamed, not treated as unknown, so sanitizing
	// must not strip a tolerated alias.
	tool := GrepTool{}
	if _, ignored, err := SanitizeUnknownArgs(tool, json.RawMessage(`{"pattern":"x","path":"internal/tools"}`)); err != nil {
		t.Fatalf("SanitizeUnknownArgs returned error: %v", err)
	} else if len(ignored) != 0 {
		t.Fatalf("alias field wrongly treated as unknown: ignored=%v", ignored)
	}
}

func TestGrepPluralPatternsToleratedAsAlternation(t *testing.T) {
	// Models reach for glob's plural "patterns" field when searching with
	// Grep. A list of patterns means "a line matches when any of them does",
	// which is exactly the alternation of the individual regexes, so the list
	// must validate and decode into the canonical single pattern instead of
	// failing with "args.pattern is required".
	raw := json.RawMessage(`{"patterns":["func compactTextSnippet","CompactionAnchorsOpenTag|CompactionAnchorsSection"],"path":"internal"}`)
	if err := ValidateToolArgs(GrepTool{}, raw); err != nil {
		t.Fatalf("ValidateToolArgs = %v, want plural patterns accepted", err)
	}
	if _, ignored, err := SanitizeUnknownArgs(GrepTool{}, raw); err != nil {
		t.Fatalf("SanitizeUnknownArgs returned error: %v", err)
	} else if len(ignored) != 0 {
		t.Fatalf("plural patterns alias should be consumed, not ignored: ignored=%v", ignored)
	}
	var a grepArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		t.Fatalf("grepArgs decode = %v", err)
	}
	if a.Pattern != "func compactTextSnippet|CompactionAnchorsOpenTag|CompactionAnchorsSection" {
		t.Fatalf("Pattern = %q, want alternation of the plural list", a.Pattern)
	}
	if len(a.Paths) != 1 || a.Paths[0] != "internal" {
		t.Fatalf("Paths = %v, want the aliased path decoded", a.Paths)
	}
	// A single string under the plural key needs no joining.
	var single grepArgs
	if err := json.Unmarshal([]byte(`{"patterns":"x"}`), &single); err != nil || single.Pattern != "x" {
		t.Fatalf("single-string patterns: Pattern=%q err=%v, want x", single.Pattern, err)
	}
	// The canonical field wins when the model supplies both spellings.
	var both grepArgs
	if err := json.Unmarshal([]byte(`{"patterns":["x"],"pattern":"y"}`), &both); err != nil || both.Pattern != "y" {
		t.Fatalf("canonical + plural: Pattern=%q err=%v, want canonical y", both.Pattern, err)
	}
	// A list under the canonical "pattern" field is a type error, not an
	// alternation — lists belong under the plural "patterns" key.
	if err := json.Unmarshal([]byte(`{"pattern":["a","b"]}`), &grepArgs{}); err == nil || !strings.Contains(err.Error(), "\"patterns\"") {
		t.Fatalf("canonical pattern array: err = %v, want rejection naming the plural field", err)
	}
}

func TestGrepPluralPatternsRejectsUnshapableValue(t *testing.T) {
	tool := GrepTool{}
	// A plural value that cannot become a single regex — a non-string element
	// or an empty list — is left for the type check to report instead of being
	// silently dropped or guessed.
	if err := ValidateToolArgs(tool, json.RawMessage(`{"patterns":["a",5]}`)); err == nil || !strings.Contains(err.Error(), "args.pattern must be a string") {
		t.Fatalf("err = %v, want non-string plural element rejected", err)
	}
	if err := ValidateToolArgs(tool, json.RawMessage(`{"patterns":[]}`)); err == nil || !strings.Contains(err.Error(), "args.pattern must be a string") {
		t.Fatalf("err = %v, want empty plural list rejected", err)
	}
	// The canonical "pattern" field is never plural-shaped: a list under it
	// keeps failing type validation even though the same list would be a legal
	// value under the "patterns" alias.
	if err := ValidateToolArgs(tool, json.RawMessage(`{"pattern":["a","b"]}`)); err == nil || !strings.Contains(err.Error(), "args.pattern must be a string") {
		t.Fatalf("err = %v, want canonical pattern array rejected", err)
	}
}

func TestSanitizeUnknownArgsKeepsHTMLCharactersUnescaped(t *testing.T) {
	tool := validationStubTool{
		name: "Write",
		schema: map[string]any{
			"type":     "object",
			"required": []string{"content"},
			"properties": map[string]any{
				"content": map[string]any{"type": "string"},
			},
			"additionalProperties": false,
		},
	}
	const content = `<div class="x">a && b</div>`
	sanitized, ignored, err := SanitizeUnknownArgs(tool, json.RawMessage(`{"content":`+strconv.Quote(content)+`,"offset":1}`))
	if err != nil {
		t.Fatalf("SanitizeUnknownArgs returned error: %v", err)
	}
	if len(ignored) != 1 {
		t.Fatalf("ignored = %v, want the extra field stripped", ignored)
	}
	// Sanitized arguments are replayed to the model and rendered to the user, so
	// <-style escaping would inflate every markup payload sixfold per
	// character and hide from the reader what actually ran.
	if strings.Contains(string(sanitized), `\u00`) {
		t.Fatalf("sanitized = %s, want < > & preserved literally", sanitized)
	}
	var got struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal(sanitized, &got); err != nil {
		t.Fatalf("sanitized args invalid JSON: %v", err)
	}
	if got.Content != content {
		t.Fatalf("content = %q, want %q", got.Content, content)
	}
}

func TestValidateToolArgsStripsAdditionalPropertiesWhenDisallowed(t *testing.T) {
	tool := validationStubTool{
		name: "Edit",
		schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"patch": map[string]any{"type": "string"},
			},
			"required":             []string{"patch"},
			"additionalProperties": false,
		},
	}
	sanitized, ignored, err := SanitizeUnknownArgs(tool, json.RawMessage(`{"patch":"*** Begin Patch\n*** Update File: a.txt\n@@\n-a\n+b\n*** End Patch\n","path":"a.txt"}`))
	if err != nil {
		t.Fatalf("err = %v, want the extra field stripped rather than rejected", err)
	}
	if len(ignored) != 1 || ignored[0].Path != "args.path" {
		t.Fatalf("ignored = %v, want [args.path]", ignored)
	}
	if strings.Contains(string(sanitized), `"path"`) {
		t.Fatalf("sanitized = %s, want path removed", sanitized)
	}
}

func TestValidateToolArgsAcceptsValidArgs(t *testing.T) {
	tool := validationStubTool{
		name: "Shell",
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

func TestValidateToolArgsCoercesScalarIntoArrayWhenOptedIn(t *testing.T) {
	tool := validationStubTool{
		name: "Grep",
		schema: map[string]any{
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
		},
	}
	if err := ValidateToolArgs(tool, json.RawMessage(`{"pattern":"x","paths":"internal/tools"}`)); err != nil {
		t.Fatalf("scalar paths with coerceFromString should pass, got %v", err)
	}
	if err := ValidateToolArgs(tool, json.RawMessage(`{"pattern":"x","paths":7}`)); err == nil || !strings.Contains(err.Error(), "args.paths must be an array") {
		t.Fatalf("non-matching scalar type should still fail, got %v", err)
	}
}

func TestValidateToolArgsScalarRejectedWithoutCoerceOptIn(t *testing.T) {
	tool := validationStubTool{
		name: "Delete",
		schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"paths": map[string]any{
					"type":  "array",
					"items": map[string]any{"type": "string"},
				},
			},
			"required":             []string{"paths"},
			"additionalProperties": false,
		},
	}
	err := ValidateToolArgs(tool, json.RawMessage(`{"paths":"x"}`))
	if err == nil || !strings.Contains(err.Error(), "args.paths must be an array") {
		t.Fatalf("scalar without opt-in should still fail, got %v", err)
	}
}

func TestValidateToolArgsCoercesSingleObjectIntoArrayWhenOptedIn(t *testing.T) {
	tool := validationStubTool{
		name: "Question",
		schema: map[string]any{
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
		},
	}
	if err := ValidateToolArgs(tool, json.RawMessage(`{"questions":{"header":"h","question":"q"}}`)); err != nil {
		t.Fatalf("single object with coerceFromObject should pass, got %v", err)
	}
	if err := ValidateToolArgs(tool, json.RawMessage(`{"questions":[{"header":"h"}]}`)); err != nil {
		t.Fatalf("array form should still pass, got %v", err)
	}
	if err := ValidateToolArgs(tool, json.RawMessage(`{"questions":"oops"}`)); err == nil || !strings.Contains(err.Error(), "args.questions must be an array") {
		t.Fatalf("non-object scalar should still fail, got %v", err)
	}
}

func TestValidateToolArgsSingleObjectRejectedWithoutCoerceOptIn(t *testing.T) {
	tool := validationStubTool{
		name: "Question",
		schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"questions": map[string]any{
					"type":  "array",
					"items": map[string]any{"type": "object"},
				},
			},
			"required":             []string{"questions"},
			"additionalProperties": false,
		},
	}
	if err := ValidateToolArgs(tool, json.RawMessage(`{"questions":{"header":"h"}}`)); err == nil || !strings.Contains(err.Error(), "args.questions must be an array") {
		t.Fatalf("single object without opt-in should still fail, got %v", err)
	}
}

// TestSanitizeUnknownArgsRejectsTrailingGarbage guards the single-pass decoder:
// a valid JSON prefix followed by non-whitespace must be rejected exactly like
// json.Valid did, instead of silently decoding the prefix.
func TestSanitizeUnknownArgsRejectsTrailingGarbage(t *testing.T) {
	tool := validationStubTool{
		name: "Read",
		schema: map[string]any{
			"type":     "object",
			"required": []string{"path"},
			"properties": map[string]any{
				"path": map[string]any{"type": "string"},
			},
			"additionalProperties": false,
		},
	}
	if _, _, err := SanitizeUnknownArgs(tool, json.RawMessage(`{"path":"a.txt"} extra`)); err == nil {
		t.Fatal("expected trailing garbage to be rejected")
	}
}

// TestSanitizeUnknownArgsRejectsDeepNesting bounds recursive descent so a
// pathologically deep document errors out instead of overflowing the stack.
func TestSanitizeUnknownArgsRejectsDeepNesting(t *testing.T) {
	tool := validationStubTool{
		name: "Read",
		schema: map[string]any{
			"type":     "object",
			"required": []string{"path"},
			"properties": map[string]any{
				"path": map[string]any{"type": "string"},
			},
			"additionalProperties": false,
		},
	}
	deep := strings.Repeat(`{"a":`, maxSchemaJSONDepth+1) + `0` + strings.Repeat(`}`, maxSchemaJSONDepth+1)
	if _, _, err := SanitizeUnknownArgs(tool, json.RawMessage(deep)); err == nil || !strings.Contains(err.Error(), "deeper than") {
		t.Fatalf("deep nesting err = %v, want depth-limit error", err)
	}
}

// TestSanitizeUnknownArgsNestedShadowedDroppedWithUnrecognizedParent verifies
// that a duplicate key inside an unrecognized field is reported once (the
// unrecognized parent) rather than twice: once as shadowed at the child path
// and again as unrecognized at the parent path.
func TestSanitizeUnknownArgsNestedShadowedDroppedWithUnrecognizedParent(t *testing.T) {
	tool := validationStubTool{
		name: "Read",
		schema: map[string]any{
			"type":     "object",
			"required": []string{"path"},
			"properties": map[string]any{
				"path": map[string]any{"type": "string"},
			},
			"additionalProperties": false,
		},
	}
	_, ignored, err := SanitizeUnknownArgs(tool, json.RawMessage(`{"path":"a.txt","extra":{"k":1,"k":2}}`))
	if err != nil {
		t.Fatalf("SanitizeUnknownArgs returned error: %v", err)
	}
	want := []message.IgnoredToolArg{{Path: "args.extra", ValueJSON: `{"k":2}`, Reason: message.IgnoredToolArgReasonUnrecognized}}
	if !reflect.DeepEqual(ignored, want) {
		t.Fatalf("ignored = %#v, want %#v", ignored, want)
	}
}

// TestSanitizeUnknownArgsPreservesBytesWhenNothingIgnored verifies a clean call
// gets its exact bytes back. Re-encoding sorts object keys, so returning a
// canonical document here would rewrite the field order a user typed by hand in
// the permission prompt and show the edit back to them reordered.
func TestSanitizeUnknownArgsPreservesBytesWhenNothingIgnored(t *testing.T) {
	raw := json.RawMessage(`{"pattern":"x","paths":"internal/tools"}`)
	sanitized, ignored, err := SanitizeUnknownArgs(GrepTool{}, raw)
	if err != nil {
		t.Fatalf("SanitizeUnknownArgs returned error: %v", err)
	}
	if len(ignored) != 0 {
		t.Fatalf("clean args reported ignored values: %v", ignored)
	}
	if string(sanitized) != string(raw) {
		t.Fatalf("sanitized = %s, want the original bytes %s", sanitized, raw)
	}
}

// TestValidateToolArgsMatchesSanitizeVerdict pins the two entry points to one
// accept/reject decision. ValidateToolArgs skips the ignored-args bookkeeping
// that SanitizeUnknownArgs does — including the per-value encode that can in
// principle fail — so the shortcut must only change how much work is done to
// reach the verdict, never the verdict or the message the model sees.
func TestValidateToolArgsMatchesSanitizeVerdict(t *testing.T) {
	tool := validationStubTool{
		name: "Stub",
		schema: map[string]any{
			"type":                 "object",
			"required":             []string{"command"},
			"additionalProperties": false,
			"properties": map[string]any{
				"command": map[string]any{"type": "string"},
				"timeout": map[string]any{"type": "integer"},
				"nested": map[string]any{
					"type":                 "object",
					"additionalProperties": false,
					"properties":           map[string]any{"keep": map[string]any{"type": "string"}},
				},
			},
		},
	}
	cases := []struct {
		name string
		args string
	}{
		{"clean", `{"command":"pwd","timeout":30}`},
		{"unrecognized field", `{"command":"pwd","bogus":{"a":[1,2]}}`},
		{"duplicate keys", `{"command":"a","command":"b"}`},
		{"unrecognized nested field", `{"command":"pwd","nested":{"keep":"x","drop":1}}`},
		{"duplicate key inside a dropped field", `{"command":"pwd","bogus":{"a":1,"a":2}}`},
		{"missing required", `{"timeout":30}`},
		{"wrong type", `{"command":"pwd","timeout":"soon"}`},
		{"malformed json", `{"command":`},
		{"trailing garbage", `{"command":"pwd"} extra`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, sanitizeErr := SanitizeUnknownArgs(tool, json.RawMessage(tc.args))
			validateErr := ValidateToolArgs(tool, json.RawMessage(tc.args))
			if (sanitizeErr == nil) != (validateErr == nil) {
				t.Fatalf("verdicts diverged: SanitizeUnknownArgs err = %v, ValidateToolArgs err = %v", sanitizeErr, validateErr)
			}
			if sanitizeErr != nil && sanitizeErr.Error() != validateErr.Error() {
				t.Fatalf("error text diverged:\n  SanitizeUnknownArgs: %v\n  ValidateToolArgs:    %v", sanitizeErr, validateErr)
			}
		})
	}
}

func TestValidateToolArgsKeepsInputJSONUnchanged(t *testing.T) {
	raw := json.RawMessage(`{"command":"pwd","timeout":30}`)
	if err := ValidateToolArgs(ShellTool{}, raw); err != nil {
		t.Fatalf("ValidateToolArgs returned error: %v", err)
	}
	if string(raw) != `{"command":"pwd","timeout":30}` {
		t.Fatal("ValidateToolArgs mutated the input JSON")
	}
}
