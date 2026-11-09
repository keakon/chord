package llm

import "testing"

func TestGeminiSchemaDropsPrunedAnnotations(t *testing.T) {
	pruned := []any{
		map[string]any{"required": []string{"value"}, "description": "Provide a value"},
		map[string]any{"required": []string{"value"}, "title": "Value"},
		map[string]any{"$ref": "#/$defs/Value", "description": "Value"},
		map[string]any{"anyOf": []any{map[string]any{"required": []string{"value"}}}, "title": "Value"},
	}
	valid := map[string]any{
		"description": "A scalar",
		"anyOf": []any{
			map[string]any{"type": "string"},
			map[string]any{"type": "number"},
		},
	}
	wire := captureGeminiToolParameters(t, map[string]any{
		"type": "object",
		"properties": map[string]any{
			"value":      valid,
			"annotation": pruned[2],
		},
		"required": []string{"value", "annotation"},
		"anyOf":    append(pruned, valid),
	})
	assertGeminiSchemaValid(t, wire, "parameters")
	props := wire["properties"].(map[string]any)
	if _, exists := props["annotation"]; exists {
		t.Fatal("annotation-only property survived")
	}
	if required := RequiredFields(wire); len(required) != 1 || required[0] != "value" {
		t.Fatalf("required = %v", required)
	}
	branches := wire["anyOf"].([]any)
	if len(branches) != 1 {
		t.Fatalf("pruned branches survived: %#v", branches)
	}
	for _, node := range []map[string]any{props["value"].(map[string]any), branches[0].(map[string]any)} {
		if _, hasType := node["type"]; hasType || len(node["anyOf"].([]any)) != 2 {
			t.Fatalf("scalar union changed: %#v", node)
		}
	}
}
