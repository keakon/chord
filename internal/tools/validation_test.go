package tools

import (
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
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
	sanitized, ignored, _, err := SanitizeUnknownArgsWithDiagnostics(tool, raw)
	if err != nil {
		t.Fatalf("SanitizeUnknownArgsWithDiagnostics = %v, want null optional field tolerated as omitted", err)
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

// TestNullFieldOfAnyOfRequiredGroupStillRejected pins that a property named by
// an anyOf alternative's "required" is treated as required for the null-drop
// rule. Schemas offering alternative argument spellings can only express their
// mandatory fields through anyOf, so dropping an explicit null there would
// replace an accurate type error with a report about a field the model did
// supply.
func TestNullFieldOfAnyOfRequiredGroupStillRejected(t *testing.T) {
	tool := validationStubTool{
		name: "SaveArtifact",
		schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"filename":    map[string]any{"type": "string"},
				"content":     map[string]any{"type": "string"},
				"result_type": map[string]any{"type": "string"},
				"result":      map[string]any{"type": "object"},
				"mode":        map[string]any{"type": "string"},
			},
			"anyOf": []map[string]any{
				{"required": []string{"filename", "content"}},
				{"required": []string{"result_type", "result"}},
			},
			"additionalProperties": false,
		},
	}
	for _, tc := range []struct {
		raw  string
		want string
	}{
		{raw: `{"filename":null,"content":"body"}`, want: "args.filename must be a string"},
		{raw: `{"result_type":"sample","result":null}`, want: "args.result must be an object"},
	} {
		err := ValidateToolArgs(tool, json.RawMessage(tc.raw))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("ValidateToolArgs(%s) = %v, want %q", tc.raw, err, tc.want)
		}
	}
	// Properties no required group names keep the tolerant null handling.
	raw := json.RawMessage(`{"filename":"report.md","content":"body","mode":null}`)
	if err := ValidateToolArgs(tool, raw); err != nil {
		t.Fatalf("ValidateToolArgs = %v, want null optional field tolerated", err)
	}
	sanitized, ignored, _, err := SanitizeUnknownArgsWithDiagnostics(tool, raw)
	if err != nil {
		t.Fatalf("SanitizeUnknownArgsWithDiagnostics = %v", err)
	}
	want := []message.IgnoredToolArg{{Path: "args.mode", ValueJSON: "null", Reason: message.IgnoredToolArgReasonNull}}
	if !reflect.DeepEqual(ignored, want) {
		t.Fatalf("ignored = %#v, want %#v", ignored, want)
	}
	if strings.Contains(string(sanitized), "mode") {
		t.Fatalf("sanitized args still carry the null field: %s", sanitized)
	}
}

// TestArtifactExplicitNullFallsThroughToExecution pins the contract after the
// top-level combinators were removed: a null on a property no required group
// names is stripped like any optional null, and the execution layer rejects
// the resulting incomplete call.
func TestArtifactExplicitNullFallsThroughToExecution(t *testing.T) {
	ctx := WithSessionDir(context.Background(), t.TempDir())
	for _, tc := range []struct {
		name    string
		tool    Tool
		raw     string
		ignored message.IgnoredToolArg
		want    string
	}{
		{
			name:    "save artifact result",
			tool:    SaveArtifactTool{},
			raw:     `{"result_type":"sample","result":null}`,
			ignored: message.IgnoredToolArg{Path: "args.result", ValueJSON: "null", Reason: message.IgnoredToolArgReasonNull},
			want:    "result is required when result_type is provided",
		},
		{
			name:    "read artifact path",
			tool:    ReadArtifactTool{},
			raw:     `{"path":null}`,
			ignored: message.IgnoredToolArg{Path: "args.path", ValueJSON: "null", Reason: message.IgnoredToolArgReasonNull},
			want:    "artifact path is required",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := json.RawMessage(tc.raw)
			if err := ValidateToolArgs(tc.tool, raw); err != nil {
				t.Fatalf("ValidateToolArgs = %v, want the optional null dropped", err)
			}
			sanitized, ignored, _, err := SanitizeUnknownArgsWithDiagnostics(tc.tool, raw)
			if err != nil {
				t.Fatalf("SanitizeUnknownArgsWithDiagnostics = %v", err)
			}
			if want := []message.IgnoredToolArg{tc.ignored}; !reflect.DeepEqual(ignored, want) {
				t.Fatalf("ignored = %#v, want %#v", ignored, want)
			}
			if _, err := tc.tool.Execute(ctx, sanitized); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Execute = %v, want %q", err, tc.want)
			}
		})
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
	sanitized, ignored, _, err := SanitizeUnknownArgsWithDiagnostics(tool, json.RawMessage(`{"path":"a.txt","offset":10,"mode":"line"}`))
	if err != nil {
		t.Fatalf("SanitizeUnknownArgsWithDiagnostics returned error: %v", err)
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
	sanitized, ignored, _, err := SanitizeUnknownArgsWithDiagnostics(tool, json.RawMessage(`{"path":"sample.go","limit":40,"format":"json"}`))
	if err != nil {
		t.Fatalf("SanitizeUnknownArgsWithDiagnostics returned error: %v", err)
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
	sanitized, ignored, _, err := SanitizeUnknownArgsWithDiagnostics(tool, raw)
	if err != nil {
		t.Fatalf("SanitizeUnknownArgsWithDiagnostics returned error: %v", err)
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
	sanitized, ignored, _, err := SanitizeUnknownArgsWithDiagnostics(tool, json.RawMessage(`{"query":"todo","scope":{"path":"src","deep":1},"globs":[{"pattern":"*.go","extra":2}]}`))
	if err != nil {
		t.Fatalf("SanitizeUnknownArgsWithDiagnostics returned error: %v", err)
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
	sanitized, ignored, _, err := SanitizeUnknownArgsWithDiagnostics(tool, raw)
	if err != nil {
		t.Fatalf("SanitizeUnknownArgsWithDiagnostics returned error: %v", err)
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
	if _, _, _, err := SanitizeUnknownArgsWithDiagnostics(tool, json.RawMessage(`{"offset":1}`)); err == nil || !strings.Contains(err.Error(), "args.path is required") {
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
	if _, _, _, err := SanitizeUnknownArgsWithDiagnostics(tool, json.RawMessage(`{"path":7}`)); err == nil || !strings.Contains(err.Error(), "args.path must be a string") {
		t.Fatalf("wrong type should still fail, got %v", err)
	}
}

func TestSanitizeUnknownArgsPreservesAliasParameters(t *testing.T) {
	// argumentAliaser fields are renamed, not treated as unknown, so sanitizing
	// must not strip a tolerated alias.
	tool := GrepTool{}
	if _, ignored, _, err := SanitizeUnknownArgsWithDiagnostics(tool, json.RawMessage(`{"pattern":"x","path":"internal/tools"}`)); err != nil {
		t.Fatalf("SanitizeUnknownArgsWithDiagnostics returned error: %v", err)
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
	if _, ignored, _, err := SanitizeUnknownArgsWithDiagnostics(GrepTool{}, raw); err != nil {
		t.Fatalf("SanitizeUnknownArgsWithDiagnostics returned error: %v", err)
	} else if len(ignored) != 0 {
		t.Fatalf("plural patterns alias should be consumed, not ignored: ignored=%v", ignored)
	}
	var a grepArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		t.Fatalf("grepArgs decode = %v", err)
	}
	if a.Pattern != "(?:func compactTextSnippet)|(?:CompactionAnchorsOpenTag|CompactionAnchorsSection)" {
		t.Fatalf("Pattern = %q, want grouped alternation of the plural list", a.Pattern)
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
	sanitized, ignored, _, err := SanitizeUnknownArgsWithDiagnostics(tool, json.RawMessage(`{"content":`+strconv.Quote(content)+`,"offset":1}`))
	if err != nil {
		t.Fatalf("SanitizeUnknownArgsWithDiagnostics returned error: %v", err)
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
	sanitized, ignored, _, err := SanitizeUnknownArgsWithDiagnostics(tool, json.RawMessage(`{"patch":"*** Begin Patch\n*** Update File: a.txt\n@@\n-a\n+b\n*** End Patch\n","path":"a.txt"}`))
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

func TestValidateToolArgsNamesMultiToolWrapper(t *testing.T) {
	tool := validationStubTool{
		name: "shell",
		schema: map[string]any{
			"type":                 "object",
			"properties":           map[string]any{"command": map[string]any{"type": "string"}},
			"required":             []string{"command"},
			"additionalProperties": false,
		},
	}
	wrapped := json.RawMessage(`{"tool_uses":[{"recipient_name":"functions.shell","parameters":{"command":"git status"}}]}`)
	_, _, _, err := SanitizeUnknownArgsWithDiagnostics(tool, wrapped)
	if err == nil {
		t.Fatal("err = nil, want the wrapper rejected")
	}
	for _, want := range []string{"args.command is required", "tool_uses wrapper", "its own function call"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("err = %v, want substring %q", err, want)
		}
	}
	_, _, _, err = SanitizeUnknownArgsWithDiagnostics(tool, json.RawMessage(`{}`))
	if err == nil || strings.Contains(err.Error(), "tool_uses") {
		t.Fatalf("err = %v, want a plain missing-field error", err)
	}
}

// TestToolsAcceptingArgsTracesUnknownNameByShape runs real built-in schemas
// against argument shapes seen under garbled tool names. The candidates are
// the file and shell tools whose zero values have complete schemas.
func TestToolsAcceptingArgsTracesUnknownNameByShape(t *testing.T) {
	candidates := []Tool{ShellTool{}, ReadTool{}, WriteTool{}, EditTool{}, ApplyPatchTool{}, DeleteTool{}, GrepTool{}, GlobTool{}, WebFetchTool{}}
	cases := []struct {
		name string
		args string
		want []string
	}{
		{"shell with workdir", `{"command":"git status","workdir":"/tmp","description":"Show status"}`, []string{NameShell}},
		{"shell bare command", `{"command":"x"}`, []string{NameShell}},
		{"edit alias key", `{"filePath":"a.go","old_string":"a","new_string":"b"}`, []string{NameEdit}},
		{"undeclared key", `{"command":"x","bogus":1}`, nil},
		{"schema mismatch", `{"command":1}`, nil},
		{"empty object", `{}`, nil},
		{"not an object", `["command"]`, nil},
		{"malformed", `{"command":`, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ToolsAcceptingArgs(candidates, json.RawMessage(tc.args))
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("ToolsAcceptingArgs(%s) = %v, want %v", tc.args, got, tc.want)
			}
		})
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
	if _, _, _, err := SanitizeUnknownArgsWithDiagnostics(tool, json.RawMessage(`{"path":"a.txt"} extra`)); err == nil {
		t.Fatal("expected trailing garbage to be rejected")
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
	_, ignored, _, err := SanitizeUnknownArgsWithDiagnostics(tool, json.RawMessage(`{"path":"a.txt","extra":{"k":1,"k":2}}`))
	if err != nil {
		t.Fatalf("SanitizeUnknownArgsWithDiagnostics returned error: %v", err)
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
	sanitized, ignored, _, err := SanitizeUnknownArgsWithDiagnostics(GrepTool{}, raw)
	if err != nil {
		t.Fatalf("SanitizeUnknownArgsWithDiagnostics returned error: %v", err)
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
// that SanitizeUnknownArgsWithDiagnostics does — including the per-value encode that can in
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
			_, _, _, sanitizeErr := SanitizeUnknownArgsWithDiagnostics(tool, json.RawMessage(tc.args))
			validateErr := ValidateToolArgs(tool, json.RawMessage(tc.args))
			if (sanitizeErr == nil) != (validateErr == nil) {
				t.Fatalf("verdicts diverged: SanitizeUnknownArgsWithDiagnostics err = %v, ValidateToolArgs err = %v", sanitizeErr, validateErr)
			}
			if sanitizeErr != nil && sanitizeErr.Error() != validateErr.Error() {
				t.Fatalf("error text diverged:\n  SanitizeUnknownArgsWithDiagnostics: %v\n  ValidateToolArgs:    %v", sanitizeErr, validateErr)
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

// The TUI's streaming preview reads arguments before execution, so every tool
// that tolerates an alias must expose the same map validation renames through.
func TestToleratedArgAliasesExposeToleratedSpellings(t *testing.T) {
	for name, want := range map[string]map[string]string{
		NameGrep: {"path": "paths", "glob": "includes", "patterns": "pattern"},
		NameGlob: {"pattern": "patterns"},
		NameEdit: {"filePath": "path"},
	} {
		if got := ToleratedArgAliases(name); !reflect.DeepEqual(got, want) {
			t.Fatalf("ToleratedArgAliases(%s) = %v, want %v", name, got, want)
		}
	}
	for _, name := range []string{NameRead, NameWrite, NameShell, NameApplyPatch, NameWebFetch, NameDelete, NameQuestion, "unknown_tool"} {
		if got := ToleratedArgAliases(name); got != nil {
			t.Fatalf("ToleratedArgAliases(%s) = %v, want nil", name, got)
		}
	}
}

// builtinToolsForAliasContract enumerates every built-in tool's zero value. It
// is the single list the alias contract is checked against, so a tool that is
// added to the package but not to this list is reported by
// TestArgumentAliaserImplementationsAreEnumerated instead of going unnoticed.
var builtinToolsForAliasContract = []Tool{
	SaveArtifactTool{}, ReadArtifactTool{}, DeleteTool{},
	&DelegateTool{}, &GenerateImageTool{}, SkillTool{}, DoneTool{}, ShellTool{},
	&TodoWriteTool{}, ToolSearchTool{}, EditTool{}, PresentTool{},
	&QuestionTool{}, &NotifyTool{}, GrepTool{}, HandoffTool{},
	WebFetchTool{}, ReadTool{}, GlobTool{}, CompactContextTool{},
	JobOutputTool{}, JobListTool{}, JobKillTool{}, &EscalateTool{},
	&CancelTool{}, CompleteTool{}, WriteTool{}, ApplyPatchTool{},
	HostedTool{}, &ViewImageTool{},
}

// builtinToolTypeName reports the struct name behind a tool value, so the
// source scan can match a receiver type to its entry in the list above
// regardless of whether the list stores a value or a pointer.
func builtinToolTypeName(tool Tool) string {
	t := reflect.TypeOf(tool)
	for t != nil && t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if t == nil {
		return ""
	}
	return t.Name()
}

// Every built-in tool that tolerates argument aliases must be reachable through
// ToleratedArgAliases, and the returned map must be exactly the tool's own
// argumentAliases(). This catches a tool that implements argumentAliaser but is
// missing from the switch in ToleratedArgAliases — without it such a tool would
// silently lose its alias rendering in the TUI's streaming preview.
func TestToleratedArgAliasesCoversEveryAliaser(t *testing.T) {
	for _, tool := range builtinToolsForAliasContract {
		name := tool.Name()
		got := ToleratedArgAliases(name)
		aliaser, isAliaser := tool.(argumentAliaser)
		if !isAliaser {
			if got != nil {
				t.Fatalf("ToleratedArgAliases(%s) = %v, want nil for a tool without aliases", name, got)
			}
			continue
		}
		if got == nil {
			t.Fatalf("ToleratedArgAliases(%s) = nil, but %s implements argumentAliaser; register it in ToleratedArgAliases", name, name)
		}
		if want := aliaser.argumentAliases(); !reflect.DeepEqual(got, want) {
			t.Fatalf("ToleratedArgAliases(%s) = %v, want the tool's own argumentAliases %v", name, got, want)
		}
	}
}

// TestArgumentAliaserImplementationsAreEnumerated closes the hole
// TestToleratedArgAliasesCoversEveryAliaser cannot: that test only walks the
// tools it lists, so a tool that starts tolerating aliases without being added
// to the list is never visited. This one parses the package's own sources for
// argumentAliases methods and requires every receiver type to be in the list,
// which turns "added the alias tolerance but forgot the wiring" into a failure.
func TestArgumentAliaserImplementationsAreEnumerated(t *testing.T) {
	enumerated := make(map[string]bool, len(builtinToolsForAliasContract))
	for _, tool := range builtinToolsForAliasContract {
		if typeName := builtinToolTypeName(tool); typeName != "" {
			enumerated[typeName] = true
		}
	}
	implementations := argumentAliaserReceiverTypes(t)
	for typeName := range implementations {
		if !enumerated[typeName] {
			t.Fatalf("%s implements argumentAliases but is not in builtinToolsForAliasContract; add it there (and to ToleratedArgAliases if it tolerates aliases)", typeName)
		}
	}
	if len(implementations) == 0 {
		t.Fatal("found no argumentAliases implementations: the source scan is broken")
	}
}

// argumentAliaserReceiverTypes parses the package's non-test sources and
// returns the receiver type names that declare argumentAliases. Test files are
// skipped so a stub in a test cannot satisfy the contract.
func argumentAliaserReceiverTypes(t *testing.T) map[string]bool {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package directory: %v", err)
	}
	found := map[string]bool{}
	fset := token.NewFileSet()
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || len(fn.Recv.List) == 0 || fn.Name.Name != "argumentAliases" {
				continue
			}
			switch recv := fn.Recv.List[0].Type.(type) {
			case *ast.Ident:
				found[recv.Name] = true
			case *ast.StarExpr:
				if ident, ok := recv.X.(*ast.Ident); ok {
					found[ident.Name] = true
				}
			}
		}
	}
	return found
}
