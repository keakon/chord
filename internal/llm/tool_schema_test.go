package llm

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"
)

func editLikeSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{"type": "string"},
			"edits": map[string]any{
				"type": "array",
				"items": map[string]any{"type": "object", "properties": map[string]any{
					"old_string":  map[string]any{"type": "string"},
					"new_string":  map[string]any{"type": "string"},
					"replace_all": map[string]any{"type": "boolean"},
				}, "required": []string{"old_string", "new_string"}},
			},
			"old_string":  map[string]any{"type": "string", "description": "a <b> & c"},
			"new_string":  map[string]any{"type": "string"},
			"replace_all": map[string]any{"type": "boolean"},
		},
		"required": []string{"path"},
		"anyOf":    []map[string]any{{"required": []string{"old_string", "new_string"}}, {"required": []string{"edits"}}},
	}
}

// propertyNames decodes the "properties" object at data's top level in wire
// order.
func propertyNames(t *testing.T, data []byte) []string {
	t.Helper()
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(data, &obj); err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(bytes.NewReader(obj["properties"]))
	if _, err := dec.Token(); err != nil {
		t.Fatal(err)
	}
	var names []string
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, tok.(string))
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			t.Fatal(err)
		}
	}
	return names
}

func TestToolSchemaListsRequiredPropertiesFirst(t *testing.T) {
	data, err := json.Marshal(toolSchema(editLikeSchema()))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := propertyNames(t, data), []string{"path", "old_string", "new_string", "edits", "replace_all"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("top-level property order = %v, want %v", got, want)
	}

	var obj struct {
		Properties struct {
			Edits struct {
				Items json.RawMessage `json:"items"`
			} `json:"edits"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(data, &obj); err != nil {
		t.Fatal(err)
	}
	if got, want := propertyNames(t, obj.Properties.Edits.Items), []string{"old_string", "new_string", "replace_all"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("nested property order = %v, want %v", got, want)
	}
}

// Branch-required fields of anyOf/oneOf combinators rank after the object's
// own required list and before the alphabetical rest, for both the
// []map[string]any form and the []any branch shape a decoded MCP schema
// carries.
func TestToolSchemaListsCombinatorBranchRequiredNext(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"zeta_extra": map[string]any{"type": "string"},
			"alpha_mid":  map[string]any{"type": "string"},
			"path":       map[string]any{"type": "string"},
			"edits": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"zeta_note":  map[string]any{"type": "string"},
						"new_string": map[string]any{"type": "string"},
						"old_string": map[string]any{"type": "string"},
					},
					"oneOf": []any{
						map[string]any{"required": []any{"old_string"}},
						map[string]any{"required": []any{"new_string"}},
					},
				},
			},
		},
		"required": []string{"path"},
		"oneOf":    []map[string]any{{"required": []string{"alpha_mid"}}, {"required": []string{"edits"}}},
	}
	data, err := json.Marshal(toolSchema(schema))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := propertyNames(t, data), []string{"path", "alpha_mid", "edits", "zeta_extra"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("top-level property order = %v, want %v", got, want)
	}

	var obj struct {
		Properties struct {
			Edits struct {
				Items json.RawMessage `json:"items"`
			} `json:"edits"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(data, &obj); err != nil {
		t.Fatal(err)
	}
	if got, want := propertyNames(t, obj.Properties.Edits.Items), []string{"old_string", "new_string", "zeta_note"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("nested []any oneOf property order = %v, want %v", got, want)
	}
}

func TestToolSchemaMarshalMatchesPlainEncoding(t *testing.T) {
	schema := editLikeSchema()
	ordered, err := json.Marshal(toolSchema(schema))
	if err != nil {
		t.Fatal(err)
	}
	plain, err := json.Marshal(schema)
	if err != nil {
		t.Fatal(err)
	}
	var got, want any
	if err := json.Unmarshal(ordered, &got); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(plain, &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ordered schema changed content:\n got %s\nwant %s", ordered, plain)
	}
	if len(ordered) != len(plain) {
		t.Fatalf("ordered schema encodes differently: %d bytes vs %d", len(ordered), len(plain))
	}
}

func TestToolSchemaNil(t *testing.T) {
	data, err := json.Marshal(openAIFunctionDef{Name: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte(`"parameters":null`)) {
		t.Fatalf("nil parameters = %s, want null as before", data)
	}
	data, err = json.Marshal(responsesTool{Type: "custom", Name: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("parameters")) {
		t.Fatalf("nil parameters must stay omitted: %s", data)
	}
}
