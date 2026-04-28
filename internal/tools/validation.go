package tools

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"strings"
)

// ValidateToolArgs checks whether raw JSON arguments conform to a tool's
// declared input schema at a basic structural level.
func ValidateToolArgs(tool Tool, args json.RawMessage) error {
	if tool == nil {
		return nil
	}
	if !json.Valid(args) {
		return fmt.Errorf("arguments must be valid JSON")
	}

	var value any
	dec := json.NewDecoder(bytes.NewReader(args))
	dec.UseNumber()
	if err := dec.Decode(&value); err != nil {
		return fmt.Errorf("decode arguments: %w", err)
	}

	if err := validateValueAgainstSchema(value, tool.Parameters(), "args"); err != nil {
		return fmt.Errorf("arguments do not match %s schema: %w", tool.Name(), err)
	}
	return nil
}

func validateValueAgainstSchema(value any, schema map[string]any, path string) error {
	if len(schema) == 0 {
		return nil
	}
	if enum, ok := schema["enum"]; ok {
		values := schemaToSlice(enum)
		if len(values) > 0 && !valueInEnum(value, values) {
			return fmt.Errorf("%s must be one of %s", path, formatEnum(values))
		}
	}

	schemaType, _ := schema["type"].(string)
	if schemaType == "" {
		if _, ok := schema["properties"].(map[string]any); ok {
			schemaType = "object"
		}
	}

	switch schemaType {
	case "", "null":
		return nil
	case "object":
		obj, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("%s must be an object", path)
		}
		for _, key := range requiredFields(schema["required"]) {
			if _, ok := obj[key]; !ok {
				return fmt.Errorf("%s.%s is required", path, key)
			}
		}
		props, _ := schema["properties"].(map[string]any)
		for key, raw := range obj {
			childSchema, ok := props[key].(map[string]any)
			if !ok {
				continue
			}
			if err := validateValueAgainstSchema(raw, childSchema, path+"."+key); err != nil {
				return err
			}
		}
		return nil
	case "array":
		items, ok := value.([]any)
		if !ok {
			return fmt.Errorf("%s must be an array", path)
		}
		if minItems, ok := asInt(schema["minItems"]); ok && len(items) < minItems {
			return fmt.Errorf("%s must contain at least %d item(s)", path, minItems)
		}
		itemSchema, _ := schema["items"].(map[string]any)
		for i, item := range items {
			if err := validateValueAgainstSchema(item, itemSchema, fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
		return nil
	case "string":
		if _, ok := value.(string); !ok {
			return fmt.Errorf("%s must be a string", path)
		}
		return nil
	case "boolean":
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("%s must be a boolean", path)
		}
		return nil
	case "integer":
		if !isIntegerJSONValue(value) {
			return fmt.Errorf("%s must be an integer", path)
		}
		return nil
	case "number":
		if !isNumberJSONValue(value) {
			return fmt.Errorf("%s must be a number", path)
		}
		return nil
	default:
		return nil
	}
}

func requiredFields(raw any) []string {
	switch v := raw.(type) {
	case []string:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if item = strings.TrimSpace(item); item != "" {
				out = append(out, item)
			}
		}
		return out
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				if s = strings.TrimSpace(s); s != "" {
					out = append(out, s)
				}
			}
		}
		return out
	default:
		return nil
	}
}

func schemaToSlice(raw any) []any {
	values, _ := raw.([]any)
	return values
}

func valueInEnum(value any, enum []any) bool {
	for _, candidate := range enum {
		if jsonValueEqual(value, candidate) {
			return true
		}
	}
	return false
}

func jsonValueEqual(a, b any) bool {
	switch av := a.(type) {
	case json.Number:
		return compareNumericJSONValue(av, b)
	case float64:
		return compareNumericJSONValue(av, b)
	case string:
		bv, ok := b.(string)
		return ok && av == bv
	case bool:
		bv, ok := b.(bool)
		return ok && av == bv
	default:
		return fmt.Sprintf("%v", a) == fmt.Sprintf("%v", b)
	}
}

func compareNumericJSONValue(a any, b any) bool {
	af, aok := toFloat64(a)
	bf, bok := toFloat64(b)
	return aok && bok && af == bf
}

func isIntegerJSONValue(value any) bool {
	switch v := value.(type) {
	case json.Number:
		if _, err := v.Int64(); err == nil {
			return true
		}
		f, err := v.Float64()
		return err == nil && math.Trunc(f) == f
	case float64:
		return math.Trunc(v) == v
	default:
		return false
	}
}

func isNumberJSONValue(value any) bool {
	_, ok := toFloat64(value)
	return ok
}

func toFloat64(value any) (float64, bool) {
	switch v := value.(type) {
	case json.Number:
		f, err := v.Float64()
		return f, err == nil
	case float64:
		return v, true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	default:
		return 0, false
	}
}

func asInt(value any) (int, bool) {
	switch v := value.(type) {
	case int:
		return v, true
	case int64:
		return int(v), true
	case float64:
		if math.Trunc(v) == v {
			return int(v), true
		}
	case json.Number:
		if i, err := v.Int64(); err == nil {
			return int(i), true
		}
	}
	return 0, false
}

func formatEnum(values []any) string {
	parts := make([]string, 0, len(values))
	for _, value := range values {
		parts = append(parts, fmt.Sprintf("%v", value))
	}
	return strings.Join(parts, ", ")
}
