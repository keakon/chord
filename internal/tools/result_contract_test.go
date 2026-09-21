package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestCompileResultSchemaAcceptsSupportedSubset(t *testing.T) {
	raw, err := json.Marshal(resultContractTestSchema())
	if err != nil {
		t.Fatalf("marshal schema: %v", err)
	}
	schema, canonical, err := CompileResultSchema(raw)
	if err != nil {
		t.Fatalf("CompileResultSchema() error = %v", err)
	}
	if len(schema) == 0 || len(canonical) == 0 {
		t.Fatalf("CompileResultSchema() = (%#v, %q), want a decoded schema and its canonical encoding", schema, canonical)
	}
	var decoded map[string]any
	if err := json.Unmarshal(canonical, &decoded); err != nil {
		t.Fatalf("canonical encoding is not a JSON object: %v", err)
	}
	if !reflect.DeepEqual(decoded, schema) {
		t.Fatalf("canonical encoding = %#v, want it to decode back to the compiled schema %#v", decoded, schema)
	}
	// A restored task recompiles the persisted bytes, so the canonical form
	// must compile to the same schema.
	recompiled, secondCanonical, err := CompileResultSchema(canonical)
	if err != nil {
		t.Fatalf("recompiling the canonical encoding failed: %v", err)
	}
	if !reflect.DeepEqual(recompiled, schema) || string(secondCanonical) != string(canonical) {
		t.Fatalf("recompiled = (%#v, %q), want the same schema and encoding", recompiled, secondCanonical)
	}
}

func TestCompileResultSchemaTreatsAbsentSchemaAsNoContract(t *testing.T) {
	for _, raw := range []json.RawMessage{nil, json.RawMessage(""), json.RawMessage("  "), json.RawMessage("null")} {
		schema, canonical, err := CompileResultSchema(raw)
		if err != nil || schema != nil || canonical != nil {
			t.Fatalf("CompileResultSchema(%q) = (%#v, %q, %v), want no contract and no error", raw, schema, canonical, err)
		}
	}
}

func TestCompileResultSchemaRejectsUnsupportedSchemas(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		wantErr string
	}{
		{name: "not an object", raw: `[]`, wantErr: "must be a JSON object"},
		{name: "top-level array type", raw: `{"type":"array","items":{"type":"string"}}`, wantErr: `result_schema.type must be "object"`},
		{name: "missing top-level type", raw: `{"properties":{"a":{"type":"string"}}}`, wantErr: `result_schema.type must be "object"`},
		{name: "non-string type", raw: `{"type":5}`, wantErr: "result_schema.type must be a string"},
		{name: "additionalProperties", raw: `{"type":"object","additionalProperties":false}`, wantErr: `keyword "additionalProperties" is not supported`},
		{name: "oneOf", raw: `{"type":"object","oneOf":[{"type":"object"}]}`, wantErr: `keyword "oneOf" is not supported`},
		{name: "ref", raw: `{"type":"object","$ref":"#/definitions/x"}`, wantErr: `keyword "$ref" is not supported`},
		{name: "unknown type", raw: `{"type":"object","properties":{"a":{"type":"strng"}}}`, wantErr: `type "strng" is not supported`},
		{name: "properties not object", raw: `{"type":"object","properties":[]}`, wantErr: "result_schema.properties must be an object"},
		{name: "property not schema", raw: `{"type":"object","properties":{"a":"string"}}`, wantErr: "result_schema.properties.a must be a schema object"},
		{name: "required not array", raw: `{"type":"object","required":"a"}`, wantErr: "result_schema.required must be an array"},
		{name: "required item not string", raw: `{"type":"object","required":[1]}`, wantErr: "result_schema.required[0] must be a property name"},
		{name: "items not schema", raw: `{"type":"object","properties":{"a":{"type":"array","items":[]}}}`, wantErr: "result_schema.properties.a.items must be a schema object"},
		{name: "enum not array", raw: `{"type":"object","enum":{}}`, wantErr: "result_schema.enum must be an array"},
		{name: "description not string", raw: `{"type":"object","description":1}`, wantErr: "result_schema.description must be a string"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			schema, canonical, err := CompileResultSchema(json.RawMessage(tc.raw))
			if err == nil {
				t.Fatalf("CompileResultSchema(%s) = (%#v, %q), want an error mentioning %q", tc.raw, schema, canonical, tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("CompileResultSchema(%s) error = %q, want it to mention %q", tc.raw, err, tc.wantErr)
			}
		})
	}
}

func nestedResultSchema(levels int) string {
	var sb strings.Builder
	sb.WriteString(`{"type":"object","properties":{"child":`)
	for i := 1; i < levels; i++ {
		sb.WriteString(`{"type":"object","properties":{"child":`)
	}
	sb.WriteString(`{"type":"string"}`)
	sb.WriteString(strings.Repeat("}}", levels))
	return sb.String()
}

func TestCompileResultSchemaEnforcesDepthLimit(t *testing.T) {
	if _, _, err := CompileResultSchema(json.RawMessage(nestedResultSchema(maxResultSchemaDepth - 1))); err != nil {
		t.Fatalf("schema at the depth limit rejected: %v", err)
	}
	_, _, err := CompileResultSchema(json.RawMessage(nestedResultSchema(maxResultSchemaDepth)))
	if err == nil || !strings.Contains(err.Error(), "nests deeper than") {
		t.Fatalf("CompileResultSchema() error = %v, want a depth-limit rejection", err)
	}
}

func resultSchemaWithProperties(count int) string {
	var sb strings.Builder
	sb.WriteString(`{"type":"object","properties":{`)
	for i := range count {
		if i > 0 {
			sb.WriteByte(',')
		}
		fmt.Fprintf(&sb, `"p%d":{"type":"string"}`, i)
	}
	sb.WriteString("}}")
	return sb.String()
}

func TestCompileResultSchemaEnforcesPropertyLimit(t *testing.T) {
	if _, _, err := CompileResultSchema(json.RawMessage(resultSchemaWithProperties(maxResultSchemaProperties))); err != nil {
		t.Fatalf("schema at the property limit rejected: %v", err)
	}
	_, _, err := CompileResultSchema(json.RawMessage(resultSchemaWithProperties(maxResultSchemaProperties + 1)))
	if err == nil || !strings.Contains(err.Error(), "more than") {
		t.Fatalf("CompileResultSchema() error = %v, want a property-limit rejection", err)
	}
}

func TestDelegateToolRejectsUnusableResultSchemaBeforeAdmission(t *testing.T) {
	creator := &countingTaskCreator{}
	args := `{"description":"implement feature","agent_type":"builder","expected_write_scope":{"path_prefix":["internal/agent"]},"result_schema":{"type":"object","oneOf":[]}}`
	_, err := NewDelegateTool(creator).Execute(context.Background(), json.RawMessage(args))
	if err == nil || !strings.Contains(err.Error(), "oneOf") {
		t.Fatalf("Execute() error = %v, want the unsupported keyword named", err)
	}
	if creator.calls != 0 {
		t.Fatalf("CreateSubAgent() calls = %d, want the delegation rejected before admission", creator.calls)
	}
}

func TestDelegateToolPassesCompiledResultSchemaToCreator(t *testing.T) {
	creator := &countingTaskCreator{}
	schema, canonical, err := CompileResultSchema(json.RawMessage(`{"type":"object","required":["summary"],"properties":{"summary":{"type":"string"}}}`))
	if err != nil {
		t.Fatalf("compile reference schema: %v", err)
	}
	if len(schema) == 0 {
		t.Fatal("reference schema compiled empty")
	}
	args := `{"description":"implement feature","agent_type":"builder","expected_write_scope":{"path_prefix":["internal/agent"]},"result_schema":` + string(canonical) + `}`
	if _, err := NewDelegateTool(creator).Execute(context.Background(), json.RawMessage(args)); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if creator.calls != 1 {
		t.Fatalf("CreateSubAgent() calls = %d, want 1", creator.calls)
	}
	if string(creator.lastRequest.ResultSchema) != string(canonical) {
		t.Fatalf("CreateSubAgent() result schema = %q, want the canonical encoding %q", creator.lastRequest.ResultSchema, canonical)
	}
}

func TestDelegateToolParametersAdvertiseResultSchema(t *testing.T) {
	params := NewDelegateTool(taskTestCreator{}).Parameters()
	properties, ok := params["properties"].(map[string]any)
	if !ok {
		t.Fatalf("Parameters()[properties] = %#v, want a map", params["properties"])
	}
	declared, ok := properties["result_schema"].(map[string]any)
	if !ok {
		t.Fatalf("Parameters() does not declare result_schema: %#v", properties)
	}
	if declared["type"] != "object" {
		t.Fatalf("result_schema declaration = %#v, want a plain object type so the tool surface stays task-agnostic", declared)
	}
	if required, _ := params["required"].([]string); slices.Contains(required, "result_schema") {
		t.Fatalf("required = %v, want result_schema optional", required)
	}
}
